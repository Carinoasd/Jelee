package httpapi

import (
	"bytes"
	"context"
	"errors"
	"hash/maphash"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// shareRepository answers the share and network rule calls of the account
// service in memory and records what it was asked.
type shareRepository struct {
	httpAccountRepository
	created  []domain.ShareInput
	redeemed []domain.ShareRedemption
	rules    []domain.NetworkRuleInput
	redeem   error
}

func (r *shareRepository) ListShares(context.Context, domain.Actor) ([]domain.Share, error) {
	return []domain.Share{{ID: sessionID, State: domain.ShareActive}}, nil
}
func (r *shareRepository) GetShare(_ context.Context, _ domain.Actor, id string) (domain.Share, error) {
	return domain.Share{ID: id, State: domain.ShareActive}, nil
}
func (r *shareRepository) CreateShare(_ context.Context, _ domain.Actor, in domain.ShareInput) (domain.ShareGrant, error) {
	r.created = append(r.created, in)
	return domain.ShareGrant{Share: domain.Share{ID: sessionID}, Token: strings.Repeat("t", 43)}, nil
}
func (r *shareRepository) RevokeShare(_ context.Context, _ domain.Actor, id string) (domain.Share, error) {
	return domain.Share{ID: id, State: domain.ShareRevoked}, nil
}
func (r *shareRepository) ListShareAccess(context.Context, domain.Actor, string, string, int) ([]domain.ShareAccessRecord, string, error) {
	return []domain.ShareAccessRecord{{ID: 1, Event: "share.accessed", Route: "GET /api/v1/items"}}, "1", nil
}
func (r *shareRepository) RedeemShare(_ context.Context, in domain.ShareRedemption) (domain.SessionGrant, error) {
	r.redeemed = append(r.redeemed, in)
	if r.redeem != nil {
		return domain.SessionGrant{}, r.redeem
	}
	kind := string(access.ClientWeb)
	if in.Native {
		kind = string(access.ClientNative)
	}
	return domain.SessionGrant{User: domain.User{ID: userID}, Session: domain.Session{ID: sessionID, ClientKind: kind, ExpiresAt: time.Now().Add(time.Hour)}, Token: strings.Repeat("s", 43)}, nil
}
func (r *shareRepository) CurrentShare(context.Context, domain.Actor) (domain.GuestShare, error) {
	return domain.GuestShare{ID: sessionID}, nil
}
func (r *shareRepository) ListNetworkRules(context.Context, domain.Actor) ([]domain.NetworkRule, error) {
	return []domain.NetworkRule{}, nil
}
func (r *shareRepository) CreateNetworkRule(_ context.Context, _ domain.Actor, in domain.NetworkRuleInput) (domain.NetworkRule, error) {
	r.rules = append(r.rules, in)
	return domain.NetworkRule{ID: sessionID, NetworkRuleInput: in}, nil
}
func (r *shareRepository) UpdateNetworkRule(_ context.Context, _ domain.Actor, id string, in domain.NetworkRuleInput) (domain.NetworkRule, error) {
	r.rules = append(r.rules, in)
	return domain.NetworkRule{ID: id, NetworkRuleInput: in}, nil
}
func (r *shareRepository) DeleteNetworkRule(context.Context, domain.Actor, string) error { return nil }

// shareBackend authenticates "a" (administrator), "u" (user) and "g" (a
// read-only share guest) tokens and records guest access.
type shareBackend struct {
	fakeBackend
	records []domain.ShareAccess
	record  error
}

func (b *shareBackend) RecordShareAccess(_ context.Context, a domain.ShareAccess) error {
	b.records = append(b.records, a)
	return b.record
}

func newShareFixture(t *testing.T) (http.Handler, *shareRepository, *shareBackend) {
	t.Helper()
	cfg := validConfig()
	cfg.EnableAccounts, cfg.EnableCatalog = true, true
	cfg.Accounts = config.DefaultAccountsConfig()
	repo := &shareRepository{}
	backend := &shareBackend{}
	backend.auth = func(_ context.Context, token string) (access.Principal, error) {
		switch token {
		case strings.Repeat("a", 43), strings.Repeat("u", 43):
			return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative, Admin: token[0] == 'a'}, nil
		case strings.Repeat("g", 43):
			return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative, ShareID: sessionID, ShareReadOnly: true}, nil
		}
		return access.Principal{}, domain.ErrUnauthenticated
	}
	accounts, err := app.NewAccounts(repo, &httpAccountPasswords{}, app.AccountOptions{SessionTTL: 24 * time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(cfg, backend, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), accounts)
	if err != nil {
		t.Fatal(err)
	}
	return handler, repo, backend
}

func shareCall(handler http.Handler, method, path, body, role string, header ...string) *httptest.ResponseRecorder {
	r := accountRequest(method, path, body, role)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestShareAdministrationRoutes(t *testing.T) {
	handler, repo, _ := newShareFixture(t)
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for _, c := range []struct {
		method, path, body, role string
		status                   int
	}{
		{"GET", "/api/v1/shares", "", "a", 200},
		{"GET", "/api/v1/shares", "", "u", 403},
		{"POST", "/api/v1/shares", `{"libraryId":"` + userID + `","expiresAt":"` + expires + `","readOnly":true,"allowPlayback":true,"maxStreams":2,"note":"x"}`, "a", 201},
		{"POST", "/api/v1/shares", `{"itemId":"` + userID + `","expiresAt":"` + expires + `","readOnly":false,"allowPlayback":false}`, "a", 201},
		{"POST", "/api/v1/shares", `{"libraryId":"` + userID + `","expiresAt":"` + expires + `","readOnly":true}`, "a", 400},
		{"POST", "/api/v1/shares", `{"libraryId":"` + userID + `","expiresAt":"` + time.Now().UTC().Format(time.RFC3339) + `","readOnly":true,"allowPlayback":false}`, "a", 400},
		{"POST", "/api/v1/shares", `{"libraryId":"` + userID + `","expiresAt":"` + expires + `","readOnly":true,"allowPlayback":false,"token":"x"}`, "a", 400},
		{"GET", "/api/v1/shares/" + sessionID, "", "a", 200},
		{"GET", "/api/v1/shares/not-an-id", "", "a", 404},
		{"POST", "/api/v1/shares/" + sessionID + "/revoke", "{}", "a", 200},
		{"POST", "/api/v1/shares/" + sessionID + "/revoke", `{"x":1}`, "a", 400},
		{"GET", "/api/v1/shares/" + sessionID + "/access?limit=5&cursor=9", "", "a", 200},
		{"GET", "/api/v1/shares/" + sessionID + "/access?limit=500", "", "a", 400},
		{"GET", "/api/v1/shares/" + sessionID + "/access?path=x", "", "a", 400},
		{"GET", "/api/v1/shares/current", "", "u", 200},
		{"GET", "/api/v1/access/network-rules", "", "a", 200},
		{"POST", "/api/v1/access/network-rules", `{"libraryId":"` + userID + `","network":"lan","cidrs":["10.0.0.0/8"],"clientKinds":["web"],"includeAdmins":true,"enabled":true}`, "a", 201},
		{"POST", "/api/v1/access/network-rules", `{"libraryId":"` + userID + `","network":"lan"}`, "a", 400},
		{"POST", "/api/v1/access/network-rules", `{"libraryId":"` + userID + `","network":"office","enabled":true}`, "a", 400},
		{"PUT", "/api/v1/access/network-rules/" + sessionID, `{"libraryId":"` + userID + `","network":"any","enabled":false}`, "a", 200},
		{"PUT", "/api/v1/access/network-rules/" + sessionID, `{"libraryId":"x","network":"any","enabled":false}`, "a", 400},
		{"DELETE", "/api/v1/access/network-rules/" + sessionID, "", "a", 204},
		{"DELETE", "/api/v1/access/network-rules/" + sessionID, "", "u", 403},
	} {
		if w := shareCall(handler, c.method, c.path, c.body, c.role); w.Code != c.status {
			t.Errorf("%s %s as %s: %d %s, want %d", c.method, c.path, c.role, w.Code, w.Body, c.status)
		}
	}
	if len(repo.created) != 2 || repo.created[0].MaxStreams != 2 || repo.created[1].MaxStreams != 1 || !repo.created[0].AllowPlayback {
		t.Fatalf("created %+v", repo.created)
	}
	if len(repo.rules) != 2 || repo.rules[0].CIDRs[0] != "10.0.0.0/8" || !repo.rules[0].IncludeAdmins || len(repo.rules[1].CIDRs) != 0 || repo.rules[1].ClientKinds == nil {
		t.Fatalf("rules %+v", repo.rules)
	}
}

func TestShareRedemptionRoutes(t *testing.T) {
	handler, repo, _ := newShareFixture(t)
	w := shareCall(handler, "POST", "/api/v1/shares/redeem", `{"token":"`+strings.Repeat("t", 43)+`","deviceName":"browser"}`, "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Set-Cookie"), sessionCookieName) || !strings.Contains(w.Body.String(), `"csrf"`) {
		t.Fatalf("web redemption %d %q %s", w.Code, w.Header().Get("Set-Cookie"), w.Body)
	}
	if w = shareCall(handler, "POST", "/api/v1/shares/redeem/native", `{"token":"x","client":"c","deviceId":"d"}`, "", "Sec-Fetch-Site", "same-origin"); w.Code != 403 {
		t.Fatalf("browser native redemption %d", w.Code)
	}
	w = shareCall(handler, "POST", "/api/v1/shares/redeem/native", `{"token":"`+strings.Repeat("t", 43)+`","client":"Player","deviceId":"tv","device":"TV","version":"1"}`, "")
	if w.Code != 200 || w.Header().Get("Set-Cookie") != "" || !strings.Contains(w.Body.String(), `"clientKind":"native"`) {
		t.Fatalf("native redemption %d %s", w.Code, w.Body)
	}
	if last := repo.redeemed[len(repo.redeemed)-1]; !last.Native || last.Client.DeviceID != "tv" || last.IP != "198.51.100.23" || last.MaxSessions != 8 {
		t.Fatalf("native redemption input %+v", last)
	}
	if w = shareCall(handler, "POST", "/api/v1/shares/redeem/native", `{"token":"x","client":" spaced ","deviceId":"d"}`, ""); w.Code != 400 {
		t.Fatalf("invalid native client %d", w.Code)
	}
	if w = shareCall(handler, "POST", "/api/v1/shares/redeem", `{"token":1}`, ""); w.Code != 400 {
		t.Fatalf("invalid body %d", w.Code)
	}
	if w = shareCall(handler, "POST", "/api/v1/shares/redeem?x=1", `{"token":"x"}`, ""); w.Code != 400 {
		t.Fatalf("query %d", w.Code)
	}
	if w = shareCall(handler, "POST", "/api/v1/shares/redeem/native?x=1", `{"token":"x"}`, ""); w.Code != 400 {
		t.Fatalf("native query %d", w.Code)
	}
	for err, want := range map[error]string{domain.ErrShareUnavailable: "share_unavailable", domain.ErrSharePlaybackDisabled: "share_playback_disabled", domain.ErrSessionLimit: "session_limit"} {
		repo.redeem = err
		w = shareCall(handler, "POST", "/api/v1/shares/redeem/native", `{"token":"x","client":"c","deviceId":"d"}`, "")
		if !strings.Contains(w.Body.String(), `"code":"`+want+`"`) {
			t.Errorf("%v: %d %s", err, w.Code, w.Body)
		}
		if w = shareCall(handler, "POST", "/api/v1/shares/redeem", `{"token":"x"}`, ""); !strings.Contains(w.Body.String(), `"code":"`+want+`"`) {
			t.Errorf("web %v: %d %s", err, w.Code, w.Body)
		}
	}
}

func TestShareGuestGate(t *testing.T) {
	handler, _, backend := newShareFixture(t)
	if w := shareCall(handler, "GET", "/api/v1/shares/current", "", "g"); w.Code != 200 {
		t.Fatalf("guest current share %d %s", w.Code, w.Body)
	}
	// A second request in the same minute shares the record.
	shareCall(handler, "GET", "/api/v1/shares/current", "", "g")
	if len(backend.records) != 1 || backend.records[0].Route != "GET /api/v1/shares/current" || backend.records[0].Refused || backend.records[0].ShareID != sessionID || backend.records[0].Actor.IP != "198.51.100.23" {
		t.Fatalf("guest records %+v", backend.records)
	}
	if w := shareCall(handler, "GET", "/api/v1/users", "", "g"); w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"share_forbidden"`) {
		t.Fatalf("guest user list %d %s", w.Code, w.Body)
	}
	if last := backend.records[len(backend.records)-1]; !last.Refused || last.Route != "GET /api/v1/users" {
		t.Fatalf("refusal record %+v", last)
	}
	if w := shareCall(handler, "PUT", "/api/v1/items/"+userID+"/played", "{}", "g"); w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"share_read_only"`) {
		t.Fatalf("read-only guest write %d %s", w.Code, w.Body)
	}
	// An access that cannot be audited is refused.
	backend.record = errors.New("down")
	if w := shareCall(handler, "GET", "/api/v1/users/me", "", "g"); w.Code != 503 {
		t.Fatalf("unaudited guest access %d", w.Code)
	}
	// Ordinary sessions are not gated or recorded.
	before := len(backend.records)
	if w := shareCall(handler, "GET", "/api/v1/shares", "", "a"); w.Code != 200 || len(backend.records) != before {
		t.Fatalf("administrator request %d, records %d", w.Code, len(backend.records)-before)
	}
}

func TestGuestCheckAndAccessLog(t *testing.T) {
	guest := access.Principal{ShareID: sessionID}
	if guestCheck("GET /api/v1/items/{id}", guest) != nil || guestCheck("POST /api/v1/playback/start", guest) != nil {
		t.Fatal("guest route refused")
	}
	if !errors.Is(guestCheck("GET /api/v1/users", guest), domain.ErrShareForbidden) || !errors.Is(guestCheck("GET ?", guest), domain.ErrShareForbidden) {
		t.Fatal("non-guest route allowed")
	}
	guest.ShareReadOnly = true
	if !errors.Is(guestCheck("DELETE /api/v1/items/{id}/played", guest), domain.ErrShareReadOnly) || guestCheck("GET /api/v1/items", guest) != nil {
		t.Fatal("read-only share")
	}
	for route := range guestRoutes {
		if method, _, _ := strings.Cut(route, " "); method == "" || !strings.Contains(route, " /") {
			t.Errorf("malformed guest route %q", route)
		}
	}
	log := shareAccessLog{}
	log.seed = maphash.MakeSeed()
	now := time.Date(2026, 10, 4, 12, 0, 30, 0, time.UTC)
	a := domain.ShareAccess{Actor: domain.Actor{SessionID: sessionID}, Route: "GET /api/v1/items"}
	if !log.due(a, now) || log.due(a, now.Add(20*time.Second)) || !log.due(a, now.Add(40*time.Second)) {
		t.Fatal("one record per minute")
	}
	a.Refused = true
	if !log.due(a, now.Add(40*time.Second)) {
		t.Fatal("refusals are recorded apart")
	}
	clock := &Server{accessNow: func() time.Time { return now }}
	if scope := clock.requestScope("::ffff:192.168.1.2", access.ClientWeb, []string{}); scope.IP != netip.MustParseAddr("192.168.1.2") || !scope.LAN() || scope.Libraries == nil || scope.Kind != access.ClientWeb || !scope.At.Equal(now) {
		t.Fatalf("scope %+v", scope)
	}
	if scope := (&Server{}).requestScope("not an address", access.ClientNative, nil); scope.IP.IsValid() || scope.LAN() || scope.Libraries != nil || scope.At.IsZero() {
		t.Fatalf("scope without address %+v", scope)
	}
}
