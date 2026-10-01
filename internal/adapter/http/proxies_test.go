package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

func TestProxyClientAddressTrustAndBounds(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8:1::/48")}
	peer := "10.1.2.3:4444"
	for _, tc := range []struct {
		name, peer   string
		headers      []string
		want, reason string
	}{
		{"no_header", peer, nil, "10.1.2.3", ""},
		{"single", peer, []string{"198.51.100.10"}, "198.51.100.10", ""},
		{"untrusted", "198.51.100.5:4444", []string{"198.51.100.10"}, "198.51.100.5", "untrusted_peer"},
		{"multiple", peer, []string{"198.51.100.10, 10.2.2.2"}, "198.51.100.10", ""},
		{"spoofed_left", peer, []string{"203.0.113.99, 198.51.100.10, 10.2.2.2"}, "198.51.100.10", ""},
		{"all_trusted", peer, []string{"10.1.1.1,10.2.2.2"}, "10.1.1.1", ""},
		{"repeated_headers", peer, []string{"198.51.100.10", "10.2.2.2"}, "198.51.100.10", ""},
		{"ipv6", "[2001:db8:1::2]:4444", []string{"2001:db8:2::3"}, "2001:db8:2::3", ""},
		{"mapped_peer", "[::ffff:10.1.2.3]:4444", []string{"::ffff:198.51.100.10"}, "198.51.100.10", ""},
		{"bad_peer", "private-secret", []string{"198.51.100.10"}, "", "untrusted_peer"},
		{"empty", peer, []string{""}, "10.1.2.3", "invalid_chain"},
		{"empty_node", peer, []string{"198.51.100.10,,10.2.2.2"}, "10.1.2.3", "invalid_chain"},
		{"hostname", peer, []string{"private-secret.invalid"}, "10.1.2.3", "invalid_chain"},
		{"port", peer, []string{"198.51.100.10:1234"}, "10.1.2.3", "invalid_chain"},
		{"zone", peer, []string{"fe80::1%private-secret"}, "10.1.2.3", "invalid_chain"},
		{"max_hops", peer, []string{strings.TrimSuffix(strings.Repeat("10.1.1.1,", 32), ",")}, "10.1.1.1", ""},
		{"too_many_hops", peer, []string{strings.TrimSuffix(strings.Repeat("10.1.1.1,", 33), ",")}, "10.1.2.3", "invalid_chain"},
		{"max_bytes", peer, []string{"198.51.100.10" + strings.Repeat(" ", 8192-len("198.51.100.10"))}, "198.51.100.10", ""},
		{"too_many_bytes", peer, []string{strings.Repeat(" ", 8193)}, "10.1.2.3", "invalid_chain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://localhost/healthz", nil)
			r.RemoteAddr = tc.peer
			for _, value := range tc.headers {
				r.Header.Add("X-Forwarded-For", value)
			}
			got, reason := proxyClientIP(r, prefixes)
			if got != tc.want || reason != tc.reason {
				t.Fatalf("got=%q/%q want=%q/%q", got, reason, tc.want, tc.reason)
			}
			if r.RemoteAddr != tc.peer {
				t.Fatal("transport peer changed")
			}
		})
	}
}

func TestTrustedProxyHTTPLoginLimitsAndWarnings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		trusted    bool
		invalid    bool
		wantSecond int
	}{
		{"trusted_separate", true, false, 401}, {"untrusted_shared", false, false, 429}, {"invalid_shared", true, true, 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookups := 0
			f := newAccountHTTPFixture(t, httpAccountRepository{credentials: func(context.Context, string) (domain.Credentials, error) {
				lookups++
				return domain.Credentials{}, domain.ErrNotFound
			}}, func(c *config.Config) {
				c.Accounts.LoginIPLimit = 1
				c.Accounts.LoginUserLimit = 5
				if tc.trusted {
					c.TrustedProxies = []string{"198.51.100.0/24"}
				}
			})
			for index, name := range []string{"alice", "bob"} {
				r := accountRequest("POST", "/api/v1/auth/login", `{"name":"`+name+`","password":"private-password"}`, "")
				value := []string{"203.0.113.10", "203.0.113.11"}[index]
				if tc.invalid {
					value = "private-secret.invalid"
				}
				r.Header.Set("X-Forwarded-For", value)
				r.Header.Set("Forwarded", "for=private-forwarded-secret")
				w := f.serve(r)
				want := 401
				if index == 1 {
					want = tc.wantSecond
				}
				if w.Code != want {
					t.Fatalf("status=%d want=%d", w.Code, want)
				}
			}
			wantCalls := 1
			if tc.wantSecond == 401 {
				wantCalls = 2
			}
			if lookups != wantCalls {
				t.Fatalf("credential lookups=%d", lookups)
			}
			logs := f.logs.String()
			wantWarnings := 2
			if tc.trusted && !tc.invalid {
				wantWarnings = 0
			}
			if strings.Count(logs, "forwarding headers ignored") != wantWarnings {
				t.Fatalf("warning count mismatch: %s", logs)
			}
			for _, secret := range []string{"private-password", "private-secret", "private-forwarded-secret", "203.0.113.10", "198.51.100.23"} {
				if strings.Contains(logs, secret) {
					t.Fatal("warning leaked private value")
				}
			}
		})
	}
}

func TestTrustedProxyHTTPAuditAndRoleBoundary(t *testing.T) {
	calls := 0
	f := newAccountHTTPFixture(t, httpAccountRepository{get: func(_ context.Context, a domain.Actor, _ string) (domain.User, error) {
		calls++
		if a.IP != "203.0.113.10" || a.UserID != userID || a.SessionID != sessionID {
			t.Fatalf("actor=%+v", a)
		}
		return domain.User{ID: userID}, nil
	}}, func(c *config.Config) { c.TrustedProxies = []string{"198.51.100.0/24"} })
	r := accountRequest("GET", "/api/v1/users/me", "", "u")
	r.Header.Set("X-Forwarded-For", "203.0.113.10")
	r.Header.Set("X-Forwarded-Host", "attacker.invalid")
	r.Header.Set("X-Role", "admin")
	if w := f.serve(r); w.Code != 200 {
		t.Fatalf("profile status=%d", w.Code)
	}
	r = accountRequest("GET", "/api/v1/users", "", "u")
	r.Header.Set("X-Forwarded-For", "203.0.113.10")
	r.Header.Set("X-Role", "admin")
	if w := f.serve(r); w.Code != 403 {
		t.Fatalf("role status=%d", w.Code)
	}
	r = accountRequest("GET", "/api/v1/users/me", "", "u")
	r.Host = "attacker.invalid"
	r.Header.Set("X-Forwarded-Host", "localhost")
	r.Header.Set("X-Forwarded-For", "203.0.113.10")
	if w := f.serve(r); w.Code != 400 {
		t.Fatalf("Host status=%d", w.Code)
	}
	if calls != 1 {
		t.Fatalf("audit calls=%d", calls)
	}
}

func TestTrustedProxyRealHTTPContextWiring(t *testing.T) {
	observed := make(chan string, 1)
	f := newAccountHTTPFixture(t, httpAccountRepository{get: func(_ context.Context, actor domain.Actor, _ string) (domain.User, error) {
		observed <- actor.IP
		return domain.User{ID: userID}, nil
	}}, func(c *config.Config) { c.TrustedProxies = []string{"127.0.0.0/8"} })
	origin := httptest.NewServer(f.handler)
	defer origin.Close()
	target := httptest.NewRequest("GET", origin.URL, nil).URL
	proxy := httputil.NewSingleHostReverseProxy(target)
	oldDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		oldDirector(r)
		r.Header.Set("X-Forwarded-For", "203.0.113.10")
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()
	request, err := http.NewRequest("GET", gateway.URL+"/api/v1/users/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("u", 43))
	response, err := gateway.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	select {
	case ip := <-observed:
		if ip != "203.0.113.10" {
			t.Fatalf("audit address=%s", ip)
		}
	default:
		t.Fatal("proxy request did not reach account repository")
	}
	if strings.Contains(string(body), "203.0.113.10") || strings.Contains(f.logs.String(), "203.0.113.10") || strings.Contains(f.logs.String(), "forwarding headers ignored") {
		t.Fatal("valid proxy request exposed address or warned")
	}
}

func TestUntrustedForwardingHeaderWarningsAreSafe(t *testing.T) {
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, false, false)
			r := httptest.NewRequest("GET", "http://localhost/healthz", nil)
			r.Header.Set(name, "private-secret")
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			if w.Code != 200 || strings.Count(f.logs.String(), "forwarding headers ignored") != 1 {
				t.Fatal("missing warning or changed response")
			}
			if strings.Contains(f.logs.String(), "private-secret") || strings.Contains(w.Body.String(), "private-secret") {
				t.Fatal("forwarding value leaked")
			}
		})
	}
}

func TestTrustedProxyConstructorCopiesPrefixes(t *testing.T) {
	cfg := validConfig()
	cfg.TrustedProxies = []string{"10.0.0.0/8"}
	var logs bytes.Buffer
	handler, err := New(cfg, &fakeBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	cfg.TrustedProxies[0] = "0.0.0.0/0"
	r := httptest.NewRequest("GET", "http://localhost/healthz", nil)
	r.RemoteAddr = "198.51.100.23:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.10")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(logs.String(), "untrusted_peer") {
		t.Fatal("external configuration mutation changed trust")
	}
}
