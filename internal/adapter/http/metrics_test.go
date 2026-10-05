package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type metricsHTTPBackend struct {
	authCalls atomic.Int32
	auth      func(context.Context, string) (access.Principal, error)
}

func (*metricsHTTPBackend) Ready(context.Context) error { return nil }
func (b *metricsHTTPBackend) Authenticate(ctx context.Context, token string) (access.Principal, error) {
	b.authCalls.Add(1)
	if b.auth != nil {
		return b.auth(ctx, token)
	}
	if token != strings.Repeat("a", 43) && token != strings.Repeat("u", 43) {
		return access.Principal{}, domain.ErrUnauthenticated
	}
	return access.Principal{UserID: userID, SessionID: sessionID, Admin: token == strings.Repeat("a", 43)}, nil
}

func metricsConfig() config.Config {
	cfg := validConfig()
	cfg.EnableAccounts, cfg.EnableMetrics = true, true
	cfg.Accounts = config.DefaultAccountsConfig()
	cfg.RequestTimeoutSeconds = 15
	return cfg
}

func metricsAccounts(t *testing.T) *app.Accounts {
	t.Helper()
	accounts, err := app.NewAccounts(httpAccountRepository{}, &httpAccountPasswords{}, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return accounts
}

func metricsHTTPHandler(t *testing.T, cfg config.Config, backend Backend, exporter http.Handler) http.Handler {
	t.Helper()
	handler, err := NewWithTelemetry(cfg, backend, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)), metricsAccounts(t), nil, nil, exporter)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestMetricsHTTPRequiresLiveAdministrator(t *testing.T) {
	for _, test := range []struct {
		name, method, target, role, host string
		status                           int
		authCalls                        int32
	}{
		{name: "administrator", method: "GET", target: "/metrics", role: "a", status: 200, authCalls: 1},
		{name: "no credential", method: "GET", target: "/metrics", status: 401},
		{name: "user with forged headers", method: "GET", target: "/metrics", role: "u", status: 403, authCalls: 1},
		{name: "invalid credential", method: "GET", target: "/metrics", role: "x", status: 401, authCalls: 1},
		{name: "query credential", method: "GET", target: "/metrics?access_token=private-token", status: 401},
		{name: "unknown query", method: "GET", target: "/metrics?format=json", role: "a", status: 400, authCalls: 1},
		{name: "duplicate query", method: "GET", target: "/metrics?x=1&x=2", role: "a", status: 400, authCalls: 1},
		{name: "malformed query", method: "GET", target: "/metrics?x=%zz", role: "a", status: 400, authCalls: 1},
		{name: "post", method: "POST", target: "/metrics", role: "a", status: 405},
		{name: "head", method: "HEAD", target: "/metrics", role: "a", status: 405},
		{name: "invalid host with forwarded override", method: "GET", target: "/metrics", role: "a", host: "evil.example", status: 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &metricsHTTPBackend{}
			var collections atomic.Int32
			exporter := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				collections.Add(1)
				w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
				_, _ = io.WriteString(w, "jelee_test 1\n")
			})
			handler := metricsHTTPHandler(t, metricsConfig(), backend, exporter)
			r := accountRequest(test.method, test.target, "", test.role)
			r.Header.Set("X-Forwarded-User", "admin")
			r.Header.Set("X-Admin", "true")
			r.Header.Set("X-Forwarded-Host", "localhost")
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			if test.host != "" {
				r.Host = test.host
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status || backend.authCalls.Load() != test.authCalls {
				t.Fatal("unexpected authorization result", w.Code, backend.authCalls.Load())
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("metrics bypassed HTTP boundary")
			}
			if test.status == http.StatusOK {
				if collections.Load() != 1 || w.Body.String() != "jelee_test 1\n" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain;") {
					t.Fatal("exporter response was collected twice or wrapped")
				}
			} else if collections.Load() != 0 {
				t.Fatal("rejected request collected metrics")
			}
		})
	}
}

func TestMetricsHTTPReauthenticatesAfterPrivilegeOrSessionChanges(t *testing.T) {
	state := "active"
	backend := &metricsHTTPBackend{auth: func(context.Context, string) (access.Principal, error) {
		switch state {
		case "expired", "revoked", "disabled":
			return access.Principal{}, domain.ErrUnauthenticated
		case "database unavailable":
			return access.Principal{}, domain.ErrDatabase
		}
		return access.Principal{UserID: userID, SessionID: sessionID, Admin: state == "active"}, nil
	}}
	var collections atomic.Int32
	handler := metricsHTTPHandler(t, metricsConfig(), backend, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		collections.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	for _, test := range []struct {
		state  string
		status int
	}{{"active", 200}, {"demoted", 403}, {"expired", 401}, {"revoked", 401}, {"disabled", 401}, {"database unavailable", 503}} {
		state = test.state
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, accountRequest("GET", "/metrics", "", "a"))
		if w.Code != test.status {
			t.Fatal("cached authorization survived session change", state, w.Code)
		}
	}
	if backend.authCalls.Load() != 6 || collections.Load() != 1 {
		t.Fatal("every scrape must authenticate before collecting")
	}
}

func TestMetricsHTTPRolloutAndConstructorDependencies(t *testing.T) {
	cfg := metricsConfig()
	backend, catalog, resolver := &metricsHTTPBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}
	logger, accounts := slog.New(slog.NewTextHandler(io.Discard, nil)), metricsAccounts(t)
	for _, construct := range []func() (http.Handler, error){
		func() (http.Handler, error) {
			return NewWithTelemetry(cfg, backend, catalog, resolver, logger, accounts, nil, nil, nil)
		},
		func() (http.Handler, error) {
			return NewWithJobs(cfg, backend, catalog, resolver, logger, accounts, nil)
		},
		func() (http.Handler, error) { return New(cfg, backend, catalog, resolver, logger, accounts) },
	} {
		if handler, err := construct(); err == nil || handler != nil {
			t.Fatal("enabled metrics accepted a missing exporter")
		}
	}
	cfg.EnableMetrics = false
	var collections atomic.Int32
	handler := metricsHTTPHandler(t, cfg, backend, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { collections.Add(1) }))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, accountRequest("GET", "/metrics", "", "a"))
	if w.Code != http.StatusNotFound || collections.Load() != 0 || backend.authCalls.Load() != 0 {
		t.Fatal("disabled metrics exposed a route or collected data")
	}
	if _, err := NewWithJobs(cfg, backend, catalog, resolver, logger, accounts, nil); err != nil {
		t.Fatal("legacy constructor compatibility changed", err)
	}
}

func TestMetricsHTTPAdmissionIncludesAuthenticationAndCollection(t *testing.T) {
	for _, phase := range []string{"authentication", "collection"} {
		t.Run(phase, func(t *testing.T) {
			entered := make(chan struct{}, 2)
			done := make(chan int, 2)
			var block atomic.Bool
			block.Store(true)
			backend := &metricsHTTPBackend{auth: func(ctx context.Context, _ string) (access.Principal, error) {
				if phase == "authentication" && block.Load() {
					entered <- struct{}{}
					<-ctx.Done()
					return access.Principal{}, ctx.Err()
				}
				return access.Principal{UserID: userID, SessionID: sessionID, Admin: true}, nil
			}}
			var collections atomic.Int32
			handler := metricsHTTPHandler(t, metricsConfig(), backend, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				collections.Add(1)
				if phase == "collection" && block.Load() {
					entered <- struct{}{}
					<-r.Context().Done()
					WriteError(w, r, r.Context().Err())
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			var cancelRequests []context.CancelFunc
			for range 2 {
				ctx, cancel := context.WithCancel(context.Background())
				cancelRequests = append(cancelRequests, cancel)
				defer cancel()
				r := accountRequest("GET", "/metrics", "", "a").WithContext(ctx)
				go func() {
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					done <- w.Code
				}()
			}
			for range 2 {
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("requests did not occupy both admission slots")
				}
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, accountRequest("GET", "/metrics", "", "a"))
			wantCollections := int32(0)
			if phase == "collection" {
				wantCollections = 2
			}
			if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" || backend.authCalls.Load() != 2 || collections.Load() != wantCollections {
				t.Fatal("full metrics admission allowed more authentication or collection", w.Code, backend.authCalls.Load(), collections.Load())
			}
			for _, cancel := range cancelRequests {
				cancel()
			}
			for range 2 {
				select {
				case status := <-done:
					if status != http.StatusRequestTimeout {
						t.Fatal("cancelled request did not terminate", status)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("cancelled request retained admission")
				}
			}
			block.Store(false)
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, accountRequest("GET", "/metrics", "", "a"))
			if w.Code != http.StatusOK || collections.Load() != wantCollections+1 {
				t.Fatal("cancellation did not restore metrics admission")
			}
		})
	}
}

type metricsDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *metricsDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestMetricsHTTPDeadlineBoundsAuthenticationAndExporter(t *testing.T) {
	for _, earlierParent := range []bool{false, true} {
		var authDeadline, exporterDeadline time.Time
		backend := &metricsHTTPBackend{auth: func(ctx context.Context, _ string) (access.Principal, error) {
			authDeadline, _ = ctx.Deadline()
			return access.Principal{UserID: userID, SessionID: sessionID, Admin: true}, nil
		}}
		handler := metricsHTTPHandler(t, metricsConfig(), backend, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			exporterDeadline, _ = r.Context().Deadline()
			w.WriteHeader(http.StatusOK)
		}))
		r := accountRequest("GET", "/metrics", "", "a")
		var parentDeadline time.Time
		if earlierParent {
			parentDeadline = time.Now().Add(time.Second)
			ctx, cancel := context.WithDeadline(r.Context(), parentDeadline)
			defer cancel()
			r = r.WithContext(ctx)
		}
		w := &metricsDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		before := time.Now()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK || authDeadline.IsZero() || !authDeadline.Equal(exporterDeadline) || !authDeadline.Equal(w.deadline) {
			t.Fatal("authentication, collection and socket writes did not share the deadline")
		}
		if earlierParent {
			if !authDeadline.Equal(parentDeadline) {
				t.Fatal("metrics widened the caller's deadline")
			}
		} else if authDeadline.Before(before.Add(3*time.Second)) || authDeadline.After(time.Now().Add(3*time.Second)) {
			t.Fatal("metrics deadline was not three seconds")
		}
	}
}

func TestMetricsHTTPCancellationNeverStartsCollection(t *testing.T) {
	for _, duringAuthentication := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		backend := &metricsHTTPBackend{auth: func(context.Context, string) (access.Principal, error) {
			cancel()
			return access.Principal{UserID: userID, SessionID: sessionID, Admin: true}, nil
		}}
		var collections atomic.Int32
		handler := metricsHTTPHandler(t, metricsConfig(), backend, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { collections.Add(1) }))
		if !duringAuthentication {
			cancel()
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, accountRequest("GET", "/metrics", "", "a").WithContext(ctx))
		wantAuth := int32(0)
		if duringAuthentication {
			wantAuth = 1
		}
		if w.Code != http.StatusRequestTimeout || backend.authCalls.Load() != wantAuth || collections.Load() != 0 {
			t.Fatal("cancelled request reached collection", w.Code, backend.authCalls.Load(), collections.Load())
		}
	}
}
