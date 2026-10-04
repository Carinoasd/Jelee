package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// prefixBox "seals" by prefixing the context, enough to prove contexts are
// checked; production uses AES-GCM (internal/platform/secretbox).
type prefixBox struct{ broken bool }

func (b prefixBox) Seal(context string, plain []byte) ([]byte, error) {
	return append([]byte(context+"|"), plain...), nil
}

func (b prefixBox) Open(context string, sealed []byte) ([]byte, error) {
	if b.broken || !strings.HasPrefix(string(sealed), context+"|") {
		return nil, errors.New("cannot open")
	}
	return sealed[len(context)+1:], nil
}

type memDelivery struct {
	WebhookDelivery
	state      domain.WebhookDeliveryState
	next       time.Time
	leaseUntil time.Time
}

// memWebhookStore is an in-memory WebhookRepository and WebhookDeliveryStore.
type memWebhookStore struct {
	mu         sync.Mutex
	endpoints  map[string]WebhookSealedEndpoint
	unplanned  []domain.WebhookEvent
	deliveries []*memDelivery
	attempts   []WebhookAttemptRecord
	purges     int
	tokens     int
}

func (m *memWebhookStore) ListDeliveryEndpoints(context.Context) ([]WebhookEndpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []WebhookEndpoint
	for _, e := range m.endpoints {
		if e.Endpoint.Enabled {
			out = append(out, e.Endpoint)
		}
	}
	return out, nil
}

func (m *memWebhookStore) LoadWebhookEndpoint(_ context.Context, id string) (WebhookSealedEndpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.endpoints[id]
	if !ok {
		return e, domain.ErrNotFound
	}
	return e, nil
}

func (m *memWebhookStore) FetchUnplanned(_ context.Context, limit int) ([]domain.WebhookEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.WebhookEvent(nil), m.unplanned[:min(limit, len(m.unplanned))]...), nil
}

func (m *memWebhookStore) CreateDeliveries(_ context.Context, event domain.WebhookEvent, ids []string, due time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.unplanned {
		if e.EventID == event.EventID {
			m.unplanned = append(m.unplanned[:i], m.unplanned[i+1:]...)
			for _, id := range ids {
				m.deliveries = append(m.deliveries, &memDelivery{WebhookDelivery: WebhookDelivery{ID: event.EventID + "/" + id, EndpointID: id, Event: event}, state: domain.WebhookDeliveryPending, next: due})
			}
			return nil
		}
	}
	return nil
}

func (m *memWebhookStore) ClaimDue(_ context.Context, now, leaseUntil time.Time, limit int) ([]WebhookDelivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []WebhookDelivery
	for _, d := range m.deliveries {
		if len(out) == limit {
			break
		}
		if d.state == domain.WebhookDeliveryPending && !d.next.After(now) && !d.leaseUntil.After(now) && m.endpoints[d.EndpointID].Endpoint.Enabled {
			m.tokens++
			d.LeaseToken = strings.Repeat("0", 35) + string(rune('a'+m.tokens%26))
			d.leaseUntil = leaseUntil
			out = append(out, d.WebhookDelivery)
		}
	}
	return out, nil
}

func (m *memWebhookStore) RecordAttempt(_ context.Context, r WebhookAttemptRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.deliveries {
		if d.ID == r.DeliveryID {
			if d.LeaseToken != r.LeaseToken || d.state != domain.WebhookDeliveryPending || d.Attempts != r.Attempt-1 {
				return domain.ErrConflict
			}
			d.Attempts, d.state, d.next, d.LeaseToken, d.leaseUntil = r.Attempt, r.Decision.State, r.Decision.NextAt, "", time.Time{}
			m.attempts = append(m.attempts, r)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (m *memWebhookStore) PurgeWebhookHistory(context.Context, time.Time, int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purges++
	return 0, nil
}

type scriptedDeliverer struct {
	mu       sync.Mutex
	outcomes []domain.WebhookOutcome
	requests []WebhookRequest
	block    chan struct{}
}

func (s *scriptedDeliverer) Deliver(ctx context.Context, r WebhookRequest) (domain.WebhookOutcome, error) {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return domain.WebhookOutcome{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r)
	if len(s.outcomes) == 0 {
		return domain.WebhookOutcome{Kind: domain.WebhookOutcomeDelivered, StatusCode: 200}, nil
	}
	o := s.outcomes[0]
	s.outcomes = s.outcomes[1:]
	return o, nil
}

type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *stepClock) Set(t time.Time)     { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *stepClock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func dispatcherFixture(t *testing.T, policy domain.WebhookRetryPolicy, outcomes ...domain.WebhookOutcome) (*WebhookDispatcher, *memWebhookStore, *scriptedDeliverer, *stepClock) {
	t.Helper()
	box := prefixBox{}
	secret, _ := box.Seal(WebhookSecretContext("hook"), []byte(strings.Repeat("s", 40)))
	headers, _ := box.Seal(WebhookHeadersContext("hook"), []byte(`{"X-Team":"media"}`))
	store := &memWebhookStore{endpoints: map[string]WebhookSealedEndpoint{"hook": {
		Endpoint: WebhookEndpoint{ID: "hook", URL: "https://hooks.example/a", Enabled: true, Timeout: DefaultWebhookTimeout, Retry: policy},
		Keys:     WebhookSealedKeys{Current: secret}, Headers: headers,
	}}}
	store.unplanned = []domain.WebhookEvent{webhookTestEvent(t)}
	deliverer := &scriptedDeliverer{outcomes: outcomes}
	clock := &stepClock{t: webhookTestNow}
	d, err := NewWebhookDispatcher(store, store, box, deliverer, WebhookDispatcherOptions{Clock: clock, Jitter: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatal(err)
	}
	return d, store, deliverer, clock
}

// With a fake clock: a failing endpoint is retried on the exponential
// schedule (jitter 0.5 is the neutral factor), dead-lettered after the last
// attempt, and never claimed before it is due.
func TestWebhookDispatcherRetriesWithBackoffAndDeadLetters(t *testing.T) {
	policy := domain.WebhookRetryPolicy{MaxAttempts: 3, BaseDelay: 10 * time.Second, MaxDelay: time.Minute, Jitter: 0.2}
	fail := domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: 503}
	d, store, deliverer, clock := dispatcherFixture(t, policy, fail, fail, fail)
	ctx := context.Background()
	round, err := d.RunOnce(ctx)
	if err != nil || round.Planned != 1 || round.Claimed != 1 || round.Retrying != 1 || round.Purged != 0 || store.purges != 1 {
		t.Fatalf("first round %+v %v purges=%d", round, err, store.purges)
	}
	if got := store.deliveries[0].next; !got.Equal(webhookTestNow.Add(10 * time.Second)) {
		t.Fatalf("first retry at %v", got)
	}
	// Not yet due.
	clock.Add(9 * time.Second)
	if round, _ = d.RunOnce(ctx); round.Claimed != 0 {
		t.Fatalf("claimed early %+v", round)
	}
	clock.Set(store.deliveries[0].next)
	if round, _ = d.RunOnce(ctx); round.Retrying != 1 {
		t.Fatalf("second round %+v", round)
	}
	if got := store.deliveries[0].next.Sub(clock.Now()); got != 20*time.Second {
		t.Fatalf("second delay %v", got)
	}
	clock.Set(store.deliveries[0].next)
	if round, _ = d.RunOnce(ctx); round.Dead != 1 || store.deliveries[0].state != domain.WebhookDeliveryDead {
		t.Fatalf("third round %+v state %s", round, store.deliveries[0].state)
	}
	if len(store.attempts) != 3 || len(deliverer.requests) != 3 {
		t.Fatalf("attempts %d requests %d", len(store.attempts), len(deliverer.requests))
	}
	// Every attempt carries the same eventId, the endpoint's sealed
	// headers and a signature made with the attempt time.
	for i, r := range deliverer.requests {
		if r.Headers[domain.WebhookEventIDHeader] != "evt-1" || r.Headers["X-Team"] != "media" {
			t.Fatalf("request %d headers %v", i, r.Headers)
		}
		signed := domain.WebhookSignedHeaders{Timestamp: r.Headers[domain.WebhookTimestampHeader], Signature: r.Headers[domain.WebhookSignatureHeader]}
		if err := domain.VerifyWebhookSignature([]domain.WebhookSecret{domain.WebhookSecret(strings.Repeat("s", 40))}, signed, r.Body, store.attempts[i].StartedAt, 0); err != nil {
			t.Fatalf("request %d signature: %v", i, err)
		}
	}
	// A dead delivery is never claimed again.
	clock.Add(time.Hour)
	if round, _ = d.RunOnce(ctx); round.Claimed != 0 {
		t.Fatal("dead delivery claimed")
	}
}

// Non-retryable outcomes dead-letter at once; Retry-After lengthens a delay.
func TestWebhookDispatcherOutcomeClasses(t *testing.T) {
	policy := domain.WebhookRetryPolicy{MaxAttempts: 5, BaseDelay: 10 * time.Second, MaxDelay: time.Hour, Jitter: 0}
	for _, tc := range []struct {
		outcome domain.WebhookOutcome
		state   domain.WebhookDeliveryState
		delay   time.Duration
	}{
		{domain.WebhookOutcome{Kind: domain.WebhookOutcomeDelivered, StatusCode: 204}, domain.WebhookDeliveryDelivered, 0},
		{domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: 410}, domain.WebhookDeliveryDead, 0},
		{domain.WebhookOutcome{Kind: domain.WebhookOutcomeBlocked}, domain.WebhookDeliveryDead, 0},
		{domain.WebhookOutcome{Kind: domain.WebhookOutcomeTLS}, domain.WebhookDeliveryDead, 0},
		{domain.WebhookOutcome{Kind: domain.WebhookOutcomeTimeout}, domain.WebhookDeliveryPending, 10 * time.Second},
		{domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: 429, RetryAfter: 5 * time.Minute}, domain.WebhookDeliveryPending, 5 * time.Minute},
	} {
		d, store, _, clock := dispatcherFixture(t, policy, tc.outcome)
		if _, err := d.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := store.deliveries[0]
		if got.state != tc.state || tc.state == domain.WebhookDeliveryPending && got.next.Sub(clock.Now()) != tc.delay {
			t.Fatalf("%+v: state %s next %v", tc.outcome, got.state, got.next.Sub(clock.Now()))
		}
	}
}

// An event that no longer validates (for example unredacted data written by
// an older build) is dead-lettered as invalid without being sent.
func TestWebhookDispatcherInvalidEventIsNotSent(t *testing.T) {
	d, store, deliverer, _ := dispatcherFixture(t, domain.DefaultWebhookRetryPolicy())
	store.unplanned[0].Data = map[string]any{"password": "hunter2"}
	if _, err := d.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(deliverer.requests) != 0 || store.deliveries[0].state != domain.WebhookDeliveryDead || store.attempts[0].Outcome.Kind != domain.WebhookOutcomeInvalid {
		t.Fatalf("invalid event: requests=%d state=%s", len(deliverer.requests), store.deliveries[0].state)
	}
}

// At least once: an attempt cancelled mid-flight records nothing, and after
// the lease expires the same delivery (same eventId) is sent again. A stale
// claim cannot record over a newer one.
func TestWebhookDispatcherCancelledAttemptIsResent(t *testing.T) {
	d, store, deliverer, clock := dispatcherFixture(t, domain.DefaultWebhookRetryPolicy())
	deliverer.block = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan WebhookRound)
	go func() { r, _ := d.RunOnce(ctx); done <- r }()
	for {
		store.mu.Lock()
		leased := len(store.deliveries) == 1 && store.deliveries[0].LeaseToken != ""
		store.mu.Unlock()
		if leased {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if round := <-done; round.Skipped != 1 || len(store.attempts) != 0 {
		t.Fatalf("cancelled round %+v attempts %d", round, len(store.attempts))
	}
	stale := store.deliveries[0].WebhookDelivery
	close(deliverer.block)
	deliverer.block = nil
	if round, _ := d.RunOnce(context.Background()); round.Claimed != 0 {
		t.Fatal("claimed while leased")
	}
	clock.Add(3 * time.Minute)
	round, err := d.RunOnce(context.Background())
	if err != nil || round.Delivered != 1 || len(deliverer.requests) != 1 || deliverer.requests[0].Headers[domain.WebhookEventIDHeader] != stale.Event.EventID {
		t.Fatalf("resend %+v %v", round, err)
	}
	if err := store.RecordAttempt(context.Background(), ResolveWebhookAttempt(stale, domain.DefaultWebhookRetryPolicy(), domain.WebhookOutcome{Kind: domain.WebhookOutcomeDelivered}, clock.Now(), clock.Now(), nil)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale lease recorded: %v", err)
	}
}

// A master key that no longer opens the stored secrets sends nothing and
// records nothing: deliveries wait instead of being dead-lettered.
func TestWebhookDispatcherUnreadableSecretWaits(t *testing.T) {
	d, store, deliverer, _ := dispatcherFixture(t, domain.DefaultWebhookRetryPolicy())
	d.box = prefixBox{broken: true}
	round, err := d.RunOnce(context.Background())
	if err != nil || round.Skipped != 1 || len(deliverer.requests) != 0 || len(store.attempts) != 0 || store.deliveries[0].state != domain.WebhookDeliveryPending {
		t.Fatalf("sealed failure %+v %v", round, err)
	}
}

// Disabled endpoints get no fan-out and their pending deliveries wait.
func TestWebhookDispatcherDisabledEndpoint(t *testing.T) {
	d, store, deliverer, _ := dispatcherFixture(t, domain.DefaultWebhookRetryPolicy())
	e := store.endpoints["hook"]
	e.Endpoint.Enabled = false
	store.endpoints["hook"] = e
	round, err := d.RunOnce(context.Background())
	if err != nil || round.Planned != 1 || len(store.deliveries) != 0 || len(deliverer.requests) != 0 {
		t.Fatalf("disabled %+v %v", round, err)
	}
}

func TestWebhookDispatcherRunStopsGracefully(t *testing.T) {
	d, store, deliverer, _ := dispatcherFixture(t, domain.DefaultWebhookRetryPolicy())
	d.opts.PollInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { d.Run(ctx); close(stopped) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		store.mu.Lock()
		n := len(store.attempts)
		store.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not deliver")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
	if len(deliverer.requests) != 1 {
		t.Fatalf("requests %d", len(deliverer.requests))
	}
}

func TestWebhookDispatcherOptions(t *testing.T) {
	store := &memWebhookStore{}
	if _, err := NewWebhookDispatcher(store, store, prefixBox{}, &scriptedDeliverer{}, WebhookDispatcherOptions{Lease: 10 * time.Second}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("lease shorter than an attempt accepted: %v", err)
	}
	if _, err := NewWebhookDispatcher(nil, store, prefixBox{}, &scriptedDeliverer{}, WebhookDispatcherOptions{}); err == nil {
		t.Fatal("missing repository accepted")
	}
}

// Every delivery attempt runs in its own span (G46.6), started for the
// event it carries and ended with the recorded delivery state.
func TestWebhookDispatcherDeliverySpans(t *testing.T) {
	policy := domain.WebhookRetryPolicy{MaxAttempts: 1, BaseDelay: time.Second, MaxDelay: time.Second}
	d, _, _, _ := dispatcherFixture(t, policy, domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: 500})
	var mu sync.Mutex
	var events, outcomes []string
	d.opts.Spans = func(ctx context.Context, eventID string) (context.Context, func(string)) {
		mu.Lock()
		events = append(events, eventID)
		mu.Unlock()
		return ctx, func(outcome string) {
			mu.Lock()
			outcomes = append(outcomes, outcome)
			mu.Unlock()
		}
	}
	if round, err := d.RunOnce(context.Background()); err != nil || round.Dead != 1 {
		t.Fatalf("round %+v %v", round, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0] != "evt-1" || len(outcomes) != 1 || outcomes[0] != string(domain.WebhookDeliveryDead) {
		t.Fatalf("spans: events %v outcomes %v", events, outcomes)
	}
}
