package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// This backend is immutable: concurrent requests must not use fakeBackend's
// unsynchronized diagnostic counter.
type accountDeadlineBackend struct{}

func (accountDeadlineBackend) Ready(context.Context) error { return nil }
func (accountDeadlineBackend) Authenticate(_ context.Context, token string) (access.Principal, error) {
	if token != strings.Repeat("a", 43) {
		return access.Principal{}, domain.ErrUnauthenticated
	}
	return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientWeb, Admin: true}, nil
}

type accountDeadlineListener struct {
	net.Listener
	timedOut chan struct{}
}

func (l accountDeadlineListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tcp := conn.(*net.TCPConn)
	if err = tcp.SetWriteBuffer(1024); err != nil {
		_ = tcp.Close()
		return nil, err
	}
	return &accountDeadlineConn{Conn: tcp, timedOut: l.timedOut}, nil
}

type accountDeadlineConn struct {
	net.Conn
	timedOut chan struct{}
	once     sync.Once
}

func (c *accountDeadlineConn) Write(data []byte) (int, error) {
	n, err := c.Conn.Write(data)
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		c.once.Do(func() { c.timedOut <- struct{}{} })
	}
	return n, err
}

func TestAccountHTTPWriteDeadlineReleasesAdmissionSlots(t *testing.T) {
	const slots = 8 // Default password concurrency 2, four account slots each.
	// Every field stays within the public 128-byte limit. JSON escaping makes a
	// valid 100-user page exceed the small TCP buffers without a huge allocation.
	users := make([]domain.User, 100)
	for i := range users {
		users[i] = domain.User{ID: userID, Name: strings.Repeat("<", 128), DisplayName: strings.Repeat("<", 128), Locale: "en-US"}
	}
	allAdmitted := make(chan struct{})
	var admitted atomic.Int32
	repo := httpAccountRepository{
		list: func(ctx context.Context, _ domain.Actor, _ string, limit int, _ bool) ([]domain.User, error) {
			if limit != 100 {
				return nil, domain.ErrInvalid
			}
			if admitted.Add(1) == slots {
				close(allAdmitted)
			}
			select {
			case <-allAdmitted:
				return users, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		get: func(context.Context, domain.Actor, string) (domain.User, error) {
			return domain.User{ID: userID, Name: "admin", Locale: "en-US"}, nil
		},
	}
	accounts, err := app.NewAccounts(repo, &httpAccountPasswords{}, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	cfg := validConfig()
	cfg.EnableAccounts, cfg.Accounts, cfg.RequestTimeoutSeconds = true, config.DefaultAccountsConfig(), 2
	handler, err := New(cfg, accountDeadlineBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts)
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan struct{}, slots)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if r.URL.Path == "/api/v1/users" {
			completed <- struct{}{}
		}
	}))
	timedOut := make(chan struct{}, slots+1)
	server.Listener = accountDeadlineListener{Listener: server.Listener, timedOut: timedOut}
	// Intentionally leave Server.WriteTimeout unset: the account route must own
	// its deadline so long-running media streams can use their separate policy.
	server.Start()
	defer server.Close()
	defer server.CloseClientConnections()
	address, err := net.ResolveTCPAddr("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < slots; i++ {
		conn, err := net.DialTCP("tcp", nil, address)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err = conn.SetReadBuffer(1024); err != nil {
			t.Fatal(err)
		}
		if err = conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err = fmt.Fprintf(conn, "GET /api/v1/users?limit=100 HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\nConnection: close\r\n\r\n", strings.Repeat("a", 43)); err != nil {
			t.Fatal(err)
		}
		// Do not read a single response byte from these clients.
	}
	select {
	case <-allAdmitted:
	case <-time.After(3 * time.Second):
		t.Fatal("slow clients did not occupy all admission slots")
	}
	request := func() int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/users/me", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "localhost"
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 43))
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if status := request(); status != http.StatusServiceUnavailable {
		t.Fatalf("full admission budget returned %d, want 503", status)
	}
	// No socket or request is cancelled here. The server's write deadlines alone
	// must terminate every stalled response and release its admission slot.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for i := 0; i < slots; i++ {
		select {
		case <-timedOut:
		case <-timer.C:
			t.Fatal("stalled TCP writer did not report a deadline timeout")
		}
	}
	for i := 0; i < slots; i++ {
		select {
		case <-completed:
		case <-timer.C:
			t.Fatal("timed-out response retained an admission slot")
		}
	}
	if status := request(); status != http.StatusOK {
		t.Fatalf("deadline did not restore admission capacity: %d", status)
	}
}
