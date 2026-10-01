package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
)

func TestTMDBPreflightProductionCancellation(t *testing.T) {
	if tmdbPreflight("") != nil {
		t.Fatal("unconfigured provider enabled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tmdbPreflight(strings.Repeat("a", 32))(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if err := tmdbPreflight("secret")(context.Background()); err != metadata.ErrCredentials {
		t.Fatalf("unsafe credential error=%v", err)
	}
}

func TestTMDBPreflightFailureClosesResourcesBeforeBinding(t *testing.T) {
	l := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
	l.server = &http.Server{Addr: "127.0.0.1:0"}
	closed := new(atomic.Int32)
	l.closeStore = func() { closed.Add(1) }
	l.listen = func(context.Context, string, string) (net.Listener, error) {
		t.Fatal("listener exposed before preflight succeeded")
		return nil, nil
	}
	l.prepareTMDB = func(context.Context) error { return metadata.ErrRateLimited }
	if err := l.start(context.Background()); err != metadata.ErrRateLimited {
		t.Fatalf("error=%v", err)
	}
	if closed.Load() != 1 || l.ctx.Err() != context.Canceled {
		t.Fatal("startup failure did not close owned resources")
	}
	l.closePool()
	if closed.Load() != 1 {
		t.Fatal("double close")
	}
}

func TestTMDBPreflightSuccessPrecedesListener(t *testing.T) {
	l := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
	l.server = &http.Server{Addr: "127.0.0.1:0"}
	prepared := false
	l.prepareTMDB = func(context.Context) error { prepared = true; return nil }
	l.listen = func(context.Context, string, string) (net.Listener, error) {
		if !prepared {
			t.Fatal("preflight not invoked")
		}
		return nil, errors.New("controlled bind failure")
	}
	if err := l.start(context.Background()); err == nil {
		t.Fatal("expected bind failure")
	}
}
