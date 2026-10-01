package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/fx"
)

type testWorker struct {
	ctx        context.Context
	startErr   error
	started    bool
	cancelled  chan struct{}
	stopCalled chan struct{}
	release    chan struct{}
	done       chan struct{}
	once       sync.Once
}

func newTestWorker() *testWorker {
	return &testWorker{cancelled: make(chan struct{}), stopCalled: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
}

func (w *testWorker) Start(ctx context.Context) error {
	if w.startErr != nil {
		return w.startErr
	}
	w.ctx, w.started = ctx, true
	go func() {
		<-ctx.Done()
		close(w.cancelled)
		<-w.release
		close(w.done)
	}()
	return nil
}

func (w *testWorker) Stop(ctx context.Context) error {
	w.once.Do(func() { close(w.stopCalled) })
	if !w.started {
		return nil
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitChannel(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("lifecycle event did not complete")
	}
}

func testLifetime(t *testing.T, worker serviceWorker, handler http.Handler) (*lifetime, *fx.App, *atomic.Int32, func() string) {
	t.Helper()
	l := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
	l.worker = worker
	l.server = &http.Server{Addr: "127.0.0.1:0", Handler: handler}
	closed := new(atomic.Int32)
	l.closeStore = func() { closed.Add(1) }
	address := ""
	l.listen = func(ctx context.Context, network, addr string) (net.Listener, error) {
		listener, err := (&net.ListenConfig{}).Listen(ctx, network, addr)
		if err == nil {
			address = listener.Addr().String()
		}
		return listener, err
	}
	a := build(l, fx.NopLogger, fx.Invoke(func(lc fx.Lifecycle) {
		lc.Append(fx.Hook{OnStart: l.start, OnStop: l.stop})
	}))
	if a.Err() != nil {
		t.Fatal(a.Err())
	}
	return l, a, closed, func() string { return "http://" + address }
}

func TestCoordinatedStopJoinsWorkersAndHTTPBeforeClosingStore(t *testing.T) {
	w := newTestWorker()
	entered, releaseHTTP, requestDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	l, a, closed, address := testLifetime(t, w, http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-releaseHTTP
		out.WriteHeader(http.StatusNoContent)
	}))
	startCtx, cancelStart := context.WithTimeout(context.Background(), 3*time.Second)
	if err := a.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	cancelStart()
	if w.ctx.Err() != nil {
		t.Fatal("worker inherited the short-lived startup context")
	}
	go func() {
		defer close(requestDone)
		response, err := http.Get(address())
		if err == nil {
			response.Body.Close()
		}
	}()
	waitChannel(t, entered)
	stopResult := make(chan error, 1)
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelStop()
	go func() { stopResult <- a.Stop(stopCtx) }()
	waitChannel(t, w.cancelled)
	waitChannel(t, w.stopCalled)
	close(w.release)
	waitChannel(t, w.done)
	if closed.Load() != 0 {
		t.Fatal("store closed before HTTP drained")
	}
	close(releaseHTTP)
	waitChannel(t, requestDone)
	if err := <-stopResult; err != nil {
		t.Fatal(err)
	}
	waitChannel(t, l.stopped)
	if closed.Load() != 1 {
		t.Fatal("store was not closed exactly once")
	}
	if err := l.stop(context.Background()); err != nil || closed.Load() != 1 {
		t.Fatalf("repeated stop: %v, closes=%d", err, closed.Load())
	}
}

func TestFxStopDeadlineStillCancelsWorkersAndEventuallyReclaimsPool(t *testing.T) {
	w := newTestWorker()
	entered, handlerCancelled, requestDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	l, a, closed, address := testLifetime(t, w, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(entered)
		<-request.Context().Done()
		close(handlerCancelled)
	}))
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(requestDone)
		response, err := http.Get(address())
		if err == nil {
			response.Body.Close()
		}
	}()
	waitChannel(t, entered)
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelStop()
	if err := a.Stop(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop error = %v", err)
	}
	waitChannel(t, w.cancelled)
	waitChannel(t, w.stopCalled)
	waitChannel(t, handlerCancelled)
	waitChannel(t, requestDone)
	if closed.Load() != 0 {
		t.Fatal("pool closed while worker cleanup was still blocked")
	}
	close(w.release)
	waitChannel(t, l.stopped)
	if closed.Load() != 1 {
		t.Fatal("pool not reclaimed after the worker joined")
	}
}

func TestConstructionAndStartFailuresReclaimStore(t *testing.T) {
	t.Run("graph construction", func(t *testing.T) {
		l := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
		var closed int
		a := build(l, fx.NopLogger, fx.Invoke(func() { l.closeStore = func() { closed++ } }), fx.Invoke(func() error { return errors.New("handler initialization failed") }))
		if a.Err() == nil || closed != 1 || l.ctx.Err() == nil {
			t.Fatalf("failed graph leaked resources: err=%v closes=%d", a.Err(), closed)
		}
	})
	t.Run("listener bind", func(t *testing.T) {
		w := newTestWorker()
		l, a, closed, _ := testLifetime(t, w, http.NotFoundHandler())
		l.listen = func(context.Context, string, string) (net.Listener, error) {
			return nil, errors.New("private listener detail")
		}
		err := a.Start(context.Background())
		if err == nil || strings.Contains(err.Error(), "private") || closed.Load() != 1 || w.started || l.ctx.Err() == nil {
			t.Fatalf("bind failure cleanup: err=%v closes=%d started=%v", err, closed.Load(), w.started)
		}
	})
	t.Run("worker startup", func(t *testing.T) {
		w := newTestWorker()
		w.startErr = errors.New("private worker detail")
		l, a, closed, address := testLifetime(t, w, http.NotFoundHandler())
		err := a.Start(context.Background())
		if err == nil || strings.Contains(err.Error(), "private") || closed.Load() != 1 || l.ctx.Err() == nil {
			t.Fatalf("worker startup cleanup: err=%v closes=%d", err, closed.Load())
		}
		waitChannel(t, w.stopCalled)
		connection, err := net.DialTimeout("tcp", strings.TrimPrefix(address(), "http://"), time.Second)
		if err == nil {
			connection.Close()
			t.Fatal("listener remained open after worker startup failed")
		}
	})
}

func TestLifecycleWithoutWorkersStillClosesStore(t *testing.T) {
	l, a, closed, _ := testLifetime(t, nil, http.NotFoundHandler())
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	waitChannel(t, l.stopped)
	if closed.Load() != 1 {
		t.Fatal("disabled workers prevented store cleanup")
	}
}
