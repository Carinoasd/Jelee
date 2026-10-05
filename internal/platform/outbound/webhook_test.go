package outbound

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// webhookServer is an HTTPS server reached as hooks.example through a
// pinned public address; its certificate is trusted only through extra
// roots, as an explicitly configured self-signed CA would be.
func webhookServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, []byte) {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = false
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

func webhookClient(t *testing.T, srv *httptest.Server, hosts []string, lookup lookupFunc, roots []byte) (*Client, *atomic.Int32) {
	t.Helper()
	dials := new(atomic.Int32)
	c, err := newClient(hosts, lookup, func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.216.34:443" {
			t.Errorf("unpinned dial %s", address)
		}
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.configureWebhook(roots); err != nil {
		t.Fatal(err)
	}
	// httptest certificates name example.com; present it under that name.
	if c.transport.TLSClientConfig == nil {
		c.transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	c.transport.TLSClientConfig.ServerName = "example.com"
	t.Cleanup(c.CloseIdleConnections)
	return c, dials
}

func TestWebhookPostSendsOnceAndNeverFollowsRedirects(t *testing.T) {
	var got atomic.Value
	srv, roots := webhookServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.Store(r.Method + " " + r.Header.Get("X-Jelee-Signature") + " " + r.Header.Get("User-Agent") + " " + string(body))
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "https://127.0.0.1/steal", http.StatusFound)
			return
		}
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c, dials := webhookClient(t, srv, nil, publicLookup, roots)
	result, err := c.Post(context.Background(), "https://hooks.example/a", map[string]string{"X-Jelee-Signature": "v1=00", "Content-Type": "application/json"}, []byte(`{"a":1}`), 5*time.Second)
	if err != nil || result.Status != 503 || result.RetryAfter != "7" || got.Load() != `POST v1=00 Jelee-Webhook {"a":1}` {
		t.Fatalf("post %+v %v %v", result, err, got.Load())
	}
	result, err = c.Post(context.Background(), "https://hooks.example/moved", nil, nil, 5*time.Second)
	if err != nil || result.Status != http.StatusFound || dials.Load() != 1 {
		t.Fatalf("redirect %+v %v dials=%d", result, err, dials.Load())
	}
}

func TestWebhookPostRefusesPrivateTargetsAndBadCertificates(t *testing.T) {
	srv, roots := webhookServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	private := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
	}
	c, dials := webhookClient(t, srv, nil, private, roots)
	if _, err := c.Post(context.Background(), "https://hooks.example/a", nil, nil, time.Second); !errors.Is(err, ErrDenied) || dials.Load() != 0 {
		t.Fatalf("private answer: %v dials=%d", err, dials.Load())
	}
	for _, raw := range []string{"http://hooks.example/a", "https://user:pw@hooks.example/a", "https://127.0.0.1/a", "https://[::1]/a", "https://169.254.169.254/latest", "https://hooks.example/a#frag", "ftp://hooks.example/a", "https://other.example/a"} {
		allow, _ := webhookClient(t, srv, []string{"hooks.example"}, publicLookup, roots)
		if err := allow.CheckTarget(raw); !errors.Is(err, ErrDenied) {
			t.Errorf("%s allowed", raw)
		}
		if _, err := allow.Post(context.Background(), raw, nil, nil, time.Second); !errors.Is(err, ErrDenied) {
			t.Errorf("%s posted: %v", raw, err)
		}
	}
	// Without the extra root the self-signed certificate is rejected;
	// there is no switch to skip verification.
	untrusted, _ := webhookClient(t, srv, nil, publicLookup, nil)
	untrusted.transport.TLSClientConfig = &tls.Config{ServerName: "example.com", MinVersion: tls.VersionTLS12}
	if _, err := untrusted.Post(context.Background(), "https://hooks.example/a", nil, nil, 5*time.Second); !errors.Is(err, ErrCertificate) {
		t.Fatalf("untrusted certificate: %v", err)
	}
	// A certificate for another name is rejected too.
	wrong, _ := webhookClient(t, srv, nil, publicLookup, roots)
	wrong.transport.TLSClientConfig.ServerName = "hooks.example"
	if _, err := wrong.Post(context.Background(), "https://hooks.example/a", nil, nil, 5*time.Second); !errors.Is(err, ErrCertificate) {
		t.Fatalf("wrong name: %v", err)
	}
	if _, err := NewWebhookClient(nil, []byte("not pem"), nil); err == nil {
		t.Fatal("empty CA bundle accepted")
	}
}

func TestWebhookPostTimeoutAndCancellation(t *testing.T) {
	release := make(chan struct{})
	srv, roots := webhookServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	c, _ := webhookClient(t, srv, nil, publicLookup, roots)
	start := time.Now()
	if _, err := c.Post(context.Background(), "https://hooks.example/slow", nil, nil, 200*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout: %v after %v", err, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err := c.Post(ctx, "https://hooks.example/slow", nil, nil, 5*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := c.Post(context.Background(), "https://hooks.example/a", nil, nil, time.Minute+time.Second); !errors.Is(err, ErrDenied) {
		t.Fatal("unbounded timeout accepted")
	}
	if _, err := c.Post(context.Background(), "https://hooks.example/a", nil, make([]byte, MaxPostBytes+1), time.Second); !errors.Is(err, ErrDenied) {
		t.Fatal("oversized body accepted")
	}
	if strings.Contains(ErrCertificate.Error(), "hooks") {
		t.Fatal("error names the target")
	}
}
