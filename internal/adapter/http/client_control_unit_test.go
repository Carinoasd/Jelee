package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// gateStore is an in-memory ClientControlStore.
type gateStore struct {
	mu       sync.Mutex
	state    domain.ClientControlState
	loadErr  error
	loads    atomic.Int64
	acts     []domain.ClientActivity
	hits     []domain.ClientHit
	counts   []domain.ClientRuleCount
	revoked  []string
	recordFn func()
}

func (g *gateStore) ClientControlState(context.Context) (domain.ClientControlState, error) {
	g.loads.Add(1)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.loadErr != nil {
		return domain.ClientControlState{}, g.loadErr
	}
	return g.state, nil
}

func (g *gateStore) ClientControlVersion(context.Context) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state.Policy.Version, nil
}

func (g *gateStore) RecordClientActivity(_ context.Context, a []domain.ClientActivity) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.acts = append(g.acts, a...)
	return nil
}

func (g *gateStore) RecordClientHits(_ context.Context, h []domain.ClientHit, c []domain.ClientRuleCount) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hits, g.counts = append(g.hits, h...), append(g.counts, c...)
	return nil
}

func (g *gateStore) RevokeSessionByClientRule(_ context.Context, session, _ string, _ []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revoked = append(g.revoked, session)
	return nil
}

func (g *gateStore) set(version int64, policy string, rules ...domain.ClientRule) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = domain.ClientControlState{Policy: domain.ClientPolicy{UnknownClients: policy, ExemptAdmins: true, ExemptLoopback: true, Version: version}, Rules: rules}
}

// gateBackend authenticates every 43-byte token as one native viewer and
// reports the version its store currently holds, like the session lookup.
type gateBackend struct {
	fakeBackend
	store *gateStore
}

func (b *gateBackend) AuthenticateClient(ctx context.Context, token, _ string) (access.Principal, access.SessionClient, int64, error) {
	if len(token) != 43 {
		return access.Principal{}, access.SessionClient{}, 0, domain.ErrUnauthenticated
	}
	v, _ := b.store.ClientControlVersion(ctx)
	return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative}, access.SessionClient{IssuedAt: time.Unix(0, 0)}, v, nil
}

// gateClientStub satisfies the application service, which the gate only
// holds for the administrator API.
type gateClientStub struct{ app.ClientControlRepository }

func newGate(t testing.TB, store *gateStore, logs io.Writer, opts ClientControlOptions) *ClientControl {
	t.Helper()
	service, err := app.NewClientControl(gateClientStub{}, ValidateClientRule)
	if err != nil {
		t.Fatal(err)
	}
	if logs == nil {
		logs = io.Discard
	}
	if opts.FlushInterval == 0 {
		opts.FlushInterval = time.Hour
	}
	c, err := NewClientControl(context.Background(), store, service, slog.New(slog.NewJSONHandler(logs, nil)), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// gateHandler is the boundary and authentication middleware in front of a
// handler that answers 204.
func gateHandler(store *gateStore, gate *ClientControl) http.Handler {
	s := &Server{cfg: validConfig(), backend: &gateBackend{store: store}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), clients: gate}
	return s.boundary(s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
}

func gateRequest(h http.Handler, method, agent string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost/api/v1/users/me", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("n", 43))
	r.Header.Set("User-Agent", agent)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func gateRule(id, action, pattern string) domain.ClientRule {
	r := domain.ClientRule{ID: id, ClientRuleInput: domain.ClientRuleInput{Dimension: "user_agent", Match: "exact", Pattern: pattern, Action: action, ScopeKind: "global", Enabled: true}}
	if action == "observe" || action == "shadow" {
		r.Intent = "deny"
	}
	return r
}

func TestClientGateReloadsOnlyForNewerVersions(t *testing.T) {
	store := &gateStore{}
	store.set(1, "allow")
	gate := newGate(t, store, nil, ClientControlOptions{})
	h := gateHandler(store, gate)
	if w := gateRequest(h, http.MethodGet, "Bad/1"); w.Code != http.StatusNoContent || store.loads.Load() != 1 {
		t.Fatalf("initial: %d loads=%d", w.Code, store.loads.Load())
	}
	store.set(2, "allow", gateRule("11111111-1111-4111-8111-111111111111", "deny", "Bad/1"))
	var wg sync.WaitGroup
	var blocked atomic.Int64
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if gateRequest(h, http.MethodGet, "Bad/1").Code == http.StatusForbidden {
				blocked.Add(1)
			}
		}()
	}
	wg.Wait()
	// Every request after the change is evaluated against it, and the new
	// version is compiled once.
	if blocked.Load() != 20 || store.loads.Load() != 2 {
		t.Fatalf("blocked=%d loads=%d", blocked.Load(), store.loads.Load())
	}
	gateRequest(h, http.MethodGet, "Bad/1")
	if store.loads.Load() != 2 {
		t.Fatal("unchanged version reloaded")
	}
}

func TestClientGateKeepsLastVersionWhenReloadFails(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	store := &gateStore{}
	store.set(1, "allow", gateRule("11111111-1111-4111-8111-111111111111", "deny", "Bad/1"))
	var logs bytes.Buffer
	gate := newGate(t, store, &logs, ClientControlOptions{Now: func() time.Time { return time.Unix(0, clock.Load()) }})
	h := gateHandler(store, gate)
	store.set(2, "allow")
	store.mu.Lock()
	store.loadErr = errors.New("database down")
	store.mu.Unlock()
	if w := gateRequest(h, http.MethodGet, "Bad/1"); w.Code != http.StatusForbidden {
		t.Fatalf("a failed reload must keep the last rules: %d", w.Code)
	}
	loads := store.loads.Load()
	gateRequest(h, http.MethodGet, "Bad/1")
	if store.loads.Load() != loads {
		t.Fatal("reload retried within the backoff")
	}
	if !strings.Contains(logs.String(), "client_control_reload_failed") {
		t.Fatal("reload failure not logged")
	}
	store.mu.Lock()
	store.loadErr = nil
	store.mu.Unlock()
	clock.Add(int64(2 * time.Second))
	if w := gateRequest(h, http.MethodGet, "Bad/1"); w.Code != http.StatusNoContent {
		t.Fatalf("recovered reload: %d", w.Code)
	}
}

func TestClientGateRecordsAggregatedHitsAndThrottledActivity(t *testing.T) {
	store := &gateStore{}
	deny, allow, observe, shadow := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"
	store.set(1, "allow", gateRule(deny, "deny", "Bad/1"), gateRule(allow, "allow", "Good/1"), gateRule(observe, "observe", "Watched/1"), gateRule(shadow, "shadow", "Watched/1"))
	var logs bytes.Buffer
	gate := newGate(t, store, &logs, ClientControlOptions{BlockAlert: 3})
	h := gateHandler(store, gate)
	for range 5 {
		gateRequest(h, http.MethodGet, "Bad/1")
		gateRequest(h, http.MethodGet, "Good/1")
		gateRequest(h, http.MethodGet, "Watched/1")
	}
	if err := gate.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	// One activity record per client identity and session per interval.
	if len(store.acts) != 3 {
		t.Fatalf("activity records: %d", len(store.acts))
	}
	byRule := map[string]domain.ClientHit{}
	for _, h := range store.hits {
		byRule[h.RuleID+"/"+h.Mode] = h
	}
	if len(store.hits) != 3 || byRule[deny+"/enforced"].Hits != 5 || byRule[observe+"/observe"].Hits != 5 || byRule[shadow+"/shadow"].Hits != 5 || byRule[deny+"/enforced"].IP != "192.0.2.1" {
		t.Fatalf("hits: %+v", store.hits)
	}
	counts := map[string]int64{}
	for _, c := range store.counts {
		counts[c.RuleID] += c.Hits
	}
	if counts[allow] != 5 || counts[deny] != 5 || counts[observe] != 5 || counts[shadow] != 5 {
		t.Fatalf("allow rules only count, every rule counts: %v", counts)
	}
	// The security log names rules and counts, never a user agent or address.
	out := logs.String()
	if !strings.Contains(out, "client_control_block_burst") || !strings.Contains(out, deny+"=5") || strings.Contains(out, "Bad/1") || strings.Contains(out, "192.0.2.1") {
		t.Fatalf("security log: %s", out)
	}
	store.acts, store.hits, store.counts = nil, nil, nil
	gate.Flush(context.Background())
	if len(store.acts)+len(store.hits)+len(store.counts) != 0 {
		t.Fatal("flush wrote records twice")
	}
}

func TestClientGateRecorderIsBounded(t *testing.T) {
	var r clientRecorder
	r.init()
	for i := range clientHitBucketMax + 10 {
		r.hit(domain.ClientHit{RuleID: "r", Mode: "observe", Action: "deny", UserAgent: fmt.Sprint(i), Hits: 1})
	}
	hits, _, _, _ := r.drain()
	if dropped := r.takeDropped(); len(hits) != clientHitBucketMax || dropped != 10 || r.takeDropped() != 0 {
		t.Fatalf("buckets=%d dropped=%d", len(hits), dropped)
	}
	long := strings.Repeat("é", 200)
	r.hit(domain.ClientHit{UserAgent: long, Hits: 1})
	hits, _, _, _ = r.drain()
	if len(hits[0].UserAgent) > 256 || !strings.HasPrefix(long, hits[0].UserAgent) {
		t.Fatal("user agent not clipped on a rune boundary")
	}
}

func TestClientGateForceReloginAndCloseFlushes(t *testing.T) {
	store := &gateStore{}
	rule := gateRule("11111111-1111-4111-8111-111111111111", "force_relogin", "Old/1")
	rule.UpdatedAt = time.Unix(10, 0) // after the session was issued
	store.set(1, "allow", rule)
	gate := newGate(t, store, nil, ClientControlOptions{})
	h := gateHandler(store, gate)
	if w := gateRequest(h, http.MethodGet, "Old/1"); w.Code != http.StatusUnauthorized || len(store.revoked) != 1 {
		t.Fatalf("forced relogin: %d revoked=%v", w.Code, store.revoked)
	}
	if err := gate.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.hits) != 1 || store.hits[0].Action != "force_relogin" {
		t.Fatalf("close did not flush: %+v", store.hits)
	}
}

func TestReadOnlyAllowedRoutes(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/api/v1/auth/logout", true},
		{http.MethodPost, "/api/v1/auth/rotate", true},
		{http.MethodPost, "/api/v1/items/0b0e5fd5-6a0a-4c4f-9a3b-0e8d2b1c4d5e/playback/check", true},
		{http.MethodPost, "/api/v1/items/x/y/playback/check", false},
		{http.MethodPut, "/api/v1/users/me/profile", false},
		{http.MethodDelete, "/api/v1/auth/logout", false},
		{http.MethodPost, "/compat/Sessions/Logout", true},
		{http.MethodPost, "/COMPAT/sessions/logout/", true},
		{http.MethodPost, "/compat/Items/abc/PlaybackInfo", true},
		{http.MethodPost, "/compat/Items//PlaybackInfo", false},
		{http.MethodPost, "/compat/Sessions/Playing", false},
		{http.MethodDelete, "/compat/UserPlayedItems/abc", false},
	} {
		req := &clientRequest{method: c.method, path: c.path, compat: strings.HasPrefix(strings.ToLower(c.path), "/compat")}
		if got := readOnlyAllowed(req); got != c.want {
			t.Errorf("%s %s: %t", c.method, c.path, got)
		}
	}
}

// C7: the gate's share of the authenticated request path, with no rules and
// with large mixed rule sets (exact, prefix, glob, regex, CIDR, with
// observe rules among them), against the same path without the gate. The
// backend here answers from memory, so the ratios overstate the overhead
// of a real request whose session lookup is a database round trip; see
// docs/client-control.md for recorded results.
func benchmarkGate(b *testing.B, rules int) {
	store := &gateStore{}
	var list []domain.ClientRule
	for i, r := range benchRulesForGate(rules) {
		list = append(list, r)
		_ = i
	}
	store.set(1, "allow", list...)
	var handler http.Handler
	if rules < 0 {
		s := &Server{cfg: validConfig(), backend: &gateBackend{store: store}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		handler = s.boundary(s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	} else {
		handler = gateHandler(store, newGate(b, store, nil, ClientControlOptions{}))
	}
	r := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/users/me", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("n", 43))
	r.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			b.Fatalf("status %d", w.Code)
		}
	}
}

// benchRulesForGate mirrors the engine benchmark's mix as stored rules.
func benchRulesForGate(n int) []domain.ClientRule {
	out := make([]domain.ClientRule, 0, max(n, 0))
	for i := 0; i < n; i++ {
		in := domain.ClientRuleInput{Action: "deny", Priority: i % 7, ScopeKind: "global", Enabled: true}
		switch i % 10 {
		case 0, 1, 2, 3:
			in.Dimension, in.Match, in.Pattern = "device_id", "exact", fmt.Sprintf("device-%d", i)
		case 4:
			in.Dimension, in.Match, in.Pattern, in.Action, in.Intent = "app_name", "exact", fmt.Sprintf("Observed%d", i), "observe", "deny"
		case 5:
			in.Dimension, in.Match, in.Pattern, in.CaseFold = "app_name", "exact", fmt.Sprintf("App%d", i), true
		case 6:
			in.Dimension, in.Match, in.Pattern, in.CaseFold = "user_agent", "prefix", fmt.Sprintf("Client%d/", i), true
		case 7:
			in.Dimension, in.Match, in.Pattern = "user_agent", "glob", fmt.Sprintf("*Bot%d*(*)", i)
		case 8:
			if i%100 == 8 {
				in.Dimension, in.Match, in.Pattern = "user_agent", "regex", fmt.Sprintf(`(?:crawler|spider)-%d\b`, i)
			} else {
				in.Dimension, in.Match, in.Pattern = "device_name", "exact", fmt.Sprintf("Room%d", i)
			}
		case 9:
			in.Dimension, in.Match, in.Pattern = "ip", "cidr", fmt.Sprintf("10.%d.%d.0/24", (i/256)%256, i%256)
		}
		out = append(out, domain.ClientRule{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), ClientRuleInput: in})
	}
	return out
}

func BenchmarkClientGateOff(b *testing.B)      { benchmarkGate(b, -1) }
func BenchmarkClientGateNoRules(b *testing.B)  { benchmarkGate(b, 0) }
func BenchmarkClientGateRules1k(b *testing.B)  { benchmarkGate(b, 1000) }
func BenchmarkClientGateRules10k(b *testing.B) { benchmarkGate(b, 10000) }

// A restrict_libraries decision reaches the principal the handler sees, as
// the request scope the unified storage filter reads (G47, G48.5).
func TestClientGateRestrictLibrariesReachesRequestScope(t *testing.T) {
	store := &gateStore{}
	lib := "55555555-5555-4555-8555-555555555555"
	rule := gateRule("66666666-6666-4666-8666-666666666666", "restrict_libraries", "Kiosk/1")
	rule.Libraries = []string{lib}
	store.set(1, "allow", rule)
	gate := newGate(t, store, nil, ClientControlOptions{})
	var seen *access.RequestScope
	s := &Server{cfg: validConfig(), backend: &gateBackend{store: store}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), clients: gate}
	h := s.boundary(s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := access.PrincipalFromContext(r.Context())
		seen = p.Request
		w.WriteHeader(http.StatusNoContent)
	})))
	if w := gateRequest(h, http.MethodGet, "Kiosk/1"); w.Code != http.StatusNoContent || seen == nil || len(seen.Libraries) != 1 || seen.Libraries[0] != lib || seen.IP.String() != "192.0.2.1" || seen.Kind != access.ClientNative {
		t.Fatalf("restricted request: %d %+v", w.Code, seen)
	}
	if w := gateRequest(h, http.MethodGet, "Other/1"); w.Code != http.StatusNoContent || seen == nil || seen.Libraries != nil {
		t.Fatalf("unrestricted request: %d %+v", w.Code, seen)
	}
	if err := ValidateClientRule(domain.ClientRuleInput{Dimension: "user_agent", Match: "exact", Pattern: "x", Action: "restrict_libraries", ScopeKind: "global", Enabled: true}); err == nil {
		t.Fatal("restrict_libraries without libraries compiled")
	}
}
