package outbound_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/events"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
	"github.com/MoYuanCN/Jelee/internal/platform/secretbox"
)

// webhookCertificate is a self-signed CA certificate for hooks.example,
// trusted only through the explicit extra-roots configuration (G12.5).
func webhookCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spec := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "webhook consumer"}, DNSNames: []string{"hooks.example", "intranet.example"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, spec, spec, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// webhookConsumer verifies like a real consumer: signature over the raw
// body with its known secrets, the replay window, and eventId dedup.
type webhookConsumer struct {
	mu        sync.Mutex
	secrets   []domain.WebhookSecret
	dedup     *domain.WebhookDedup
	fail      int
	received  []domain.WebhookEvent
	verified  []error
	dupes     int
	sigCounts []int
}

func (c *webhookConsumer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	header := domain.WebhookSignedHeaders{Timestamp: r.Header.Get(domain.WebhookTimestampHeader), Signature: r.Header.Get(domain.WebhookSignatureHeader)}
	err := domain.VerifyWebhookSignature(c.secrets, header, body, time.Now(), 0)
	c.verified = append(c.verified, err)
	c.sigCounts = append(c.sigCounts, len(strings.Split(header.Signature, ",")))
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var event domain.WebhookEvent
	if json.Unmarshal(body, &event) != nil || event.EventID != r.Header.Get(domain.WebhookEventIDHeader) || r.Header.Get("Authorization") != "Bearer consumer-token" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if c.fail > 0 {
		c.fail--
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if c.dedup.Seen(event.EventID, time.Now()) {
		c.dupes++
		w.WriteHeader(http.StatusOK)
		return
	}
	c.received = append(c.received, event)
	w.WriteHeader(http.StatusNoContent)
}

type webhookClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *webhookClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *webhookClock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// TestWebhookEndToEndThroughGuardedTLS runs producers, the PostgreSQL
// outbox, the dispatcher, the SSRF-guarded client and a real HTTPS consumer
// together: a failed login becomes a signed user.login_failed event that is
// retried after a 502 and then delivered once; an endpoint whose name
// resolves to a private address is refused by the outbound policy and
// dead-lettered as blocked; replaying a delivered event re-sends the same
// eventId, which the consumer deduplicates; a rotated secret signs alongside
// the previous one.
func TestWebhookEndToEndThroughGuardedTLS(t *testing.T) {
	ctx, store, grant, _ := metadataApplyStore(t)
	admin := domain.Actor{UserID: grant.User.ID, SessionID: grant.Session.ID, IP: "127.0.0.1"}
	cert, roots := webhookCertificate(t)
	consumer := &webhookConsumer{dedup: domain.NewWebhookDedup(0)}
	srv := httptest.NewUnstartedServer(consumer)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	var dialMu sync.Mutex
	var dialed []string
	client, err := outbound.NewMappedWebhookTestClient(nil, func(_ context.Context, _ string, host string) ([]netip.Addr, error) {
		switch host {
		case "hooks.example":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		case "intranet.example":
			// DNS points a public-looking name at a private address.
			return []netip.Addr{netip.MustParseAddr("10.0.0.8")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}, func(ctx context.Context, network, address string) (net.Conn, error) {
		dialMu.Lock()
		dialed = append(dialed, address)
		dialMu.Unlock()
		if address != "93.184.216.34:443" {
			t.Errorf("dialed %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	deliverer, err := events.NewDeliverer(client)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, secretbox.KeyBytes)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	clock := &webhookClock{t: time.Now()}
	service, err := app.NewWebhooks(store, box, deliverer, deliverer, app.WebhookOptions{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := app.NewWebhookDispatcher(store, store, box, deliverer, app.WebhookDispatcherOptions{Clock: clock, Jitter: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatal(err)
	}
	store.EnableWebhookEvents(true)
	retry := &app.WebhookRetryView{MaxAttempts: 3, BaseDelaySeconds: 30, MaxDelaySeconds: 300, Jitter: 0.2}
	headers := map[string]string{"Authorization": "Bearer consumer-token"}
	hook, secret, err := service.Create(ctx, admin, app.WebhookEndpointInput{Name: "consumer", URL: "https://hooks.example/jelee", Enabled: true,
		Events: []domain.WebhookEventType{domain.WebhookUserLoginFailed}, Headers: headers, Retry: retry})
	if err != nil {
		t.Fatal(err)
	}
	intranet, _, err := service.Create(ctx, admin, app.WebhookEndpointInput{Name: "intranet", URL: "https://intranet.example/hook", Enabled: true,
		Events: []domain.WebhookEventType{domain.WebhookUserLoginFailed}, Headers: headers, Retry: retry})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Create(ctx, admin, app.WebhookEndpointInput{Name: "literal", URL: "https://10.0.0.8/hook"}); err == nil {
		t.Fatal("literal private address accepted")
	}
	consumer.mu.Lock()
	consumer.secrets = []domain.WebhookSecret{domain.WebhookSecret(secret)}
	consumer.fail = 1
	consumer.mu.Unlock()

	// A failed login raises user.login_failed in the login transaction.
	credentials, err := store.Credentials(ctx, grant.User.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CommitLogin(ctx, domain.LoginInput{Credentials: credentials, PasswordOK: false, IP: "203.0.113.9", MaxSessions: 8, SessionTTL: time.Hour, LockAfter: 5, LockFor: time.Minute}); err == nil {
		t.Fatal("bad password accepted")
	}
	round, err := dispatcher.RunOnce(ctx)
	if err != nil || round.Planned != 1 || round.Claimed != 2 || round.Retrying != 1 || round.Dead != 1 {
		t.Fatalf("first round %+v %v", round, err)
	}
	dead, err := service.Deliveries(ctx, admin, intranet.ID, domain.WebhookDeliveryDead, "", 10)
	if err != nil || len(dead) != 1 || dead[0].LastOutcome != domain.WebhookOutcomeBlocked {
		t.Fatalf("private target %+v %v", dead, err)
	}
	clock.Add(31 * time.Second)
	if round, err = dispatcher.RunOnce(ctx); err != nil || round.Delivered != 1 {
		t.Fatalf("retry round %+v %v", round, err)
	}
	consumer.mu.Lock()
	if len(consumer.received) != 1 || consumer.received[0].Type != domain.WebhookUserLoginFailed || consumer.received[0].Subject.ID != grant.User.ID {
		t.Fatalf("received %+v", consumer.received)
	}
	for _, err := range consumer.verified {
		if err != nil {
			t.Fatalf("signature: %v", err)
		}
	}
	body, _ := json.Marshal(consumer.received[0])
	if strings.Contains(string(body), "203.0.113.9") || strings.Contains(string(body), grant.User.Name) {
		t.Fatalf("payload leaks %s", body)
	}
	eventID := consumer.received[0].EventID
	consumer.mu.Unlock()

	// Replay a delivered event: same eventId, deduplicated by the consumer.
	delivered, err := service.Deliveries(ctx, admin, hook.ID, domain.WebhookDeliveryDelivered, "", 10)
	if err != nil || len(delivered) != 1 || delivered[0].EventID != eventID {
		t.Fatalf("delivered %+v %v", delivered, err)
	}
	if _, err = service.Replay(ctx, admin, hook.ID, delivered[0].ID); err != nil {
		t.Fatal(err)
	}
	if round, err = dispatcher.RunOnce(ctx); err != nil || round.Delivered != 1 {
		t.Fatalf("replay round %+v %v", round, err)
	}
	consumer.mu.Lock()
	if consumer.dupes != 1 || len(consumer.received) != 1 {
		t.Fatalf("replay not deduplicated: dupes=%d received=%d", consumer.dupes, len(consumer.received))
	}
	consumer.mu.Unlock()

	// Rotation: both secrets sign during the grace period, so a consumer
	// that still knows only the old one keeps verifying.
	_, rotated, err := service.Rotate(ctx, admin, hook.ID, time.Hour)
	if err != nil || rotated == secret {
		t.Fatal(err)
	}
	result, err := service.Test(ctx, admin, hook.ID)
	if err != nil || result.Outcome != domain.WebhookOutcomeDelivered {
		t.Fatalf("test send %+v %v", result, err)
	}
	consumer.mu.Lock()
	last := len(consumer.verified) - 1
	if consumer.verified[last] != nil || consumer.sigCounts[last] != 2 {
		t.Fatalf("rotated signature %v entries=%d", consumer.verified[last], consumer.sigCounts[last])
	}
	consumer.mu.Unlock()
	// The private endpoint's test send is refused before any connection.
	if result, err = service.Test(ctx, admin, intranet.ID); err != nil || result.Outcome != domain.WebhookOutcomeBlocked {
		t.Fatalf("intranet test %+v %v", result, err)
	}
	dialMu.Lock()
	defer dialMu.Unlock()
	for _, address := range dialed {
		if strings.HasPrefix(address, "10.") {
			t.Fatalf("private address dialled: %v", dialed)
		}
	}
}
