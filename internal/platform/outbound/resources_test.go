package outbound

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func TestFetchSharedBudgetQueueCancellationAndRecovery(t *testing.T) {
	for _, queue := range []int{0, 1} {
		t.Run(map[int]string{0: "full", 1: "queued"}[queue], func(t *testing.T) {
			b, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: queue})
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if s := b.Stats(); s.IO != 1 || s.Total != 1 || s.CPU != 0 {
					t.Errorf("fetch without I/O permit: %+v", s)
				}
				_, _ = w.Write([]byte("ok"))
			}))
			defer srv.Close()
			c, dials := mappedClient(t, srv, publicLookup)
			c.budget = b
			hold, err := b.Acquire(context.Background(), app.WorkCPU)
			if err != nil {
				t.Fatal(err)
			}
			defer hold()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := c.Fetch(ctx, "http://fetch.example/", 16); done <- err }()
			if queue == 1 {
				deadline := time.Now().Add(3 * time.Second)
				for b.Stats().Waiting != 1 {
					if time.Now().After(deadline) {
						t.Fatal("request did not queue")
					}
					time.Sleep(time.Millisecond)
				}
				cancel()
			}
			select {
			case err := <-done:
				want := domain.ErrResourceBusy
				if queue == 1 {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatalf("admission: %v, want %v", err, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("admission did not return")
			}
			if requests.Load() != 0 || dials.Load() != 0 || b.Stats().Waiting != 0 {
				t.Fatal("blocked request reached network or leaked waiter")
			}
			hold()
			response, err := c.Fetch(context.Background(), "http://fetch.example/", 16)
			if err != nil || string(response.Body) != "ok" || requests.Load() != 1 {
				t.Fatalf("recovery response/error: %+v/%v", response, err)
			}
			if s := b.Stats(); s != (resources.Stats{}) {
				t.Fatalf("leaked permit: %+v", s)
			}
		})
	}
}

func TestFetchSharedBudgetHeldThroughBodyAndFailure(t *testing.T) {
	b, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-unblock:
		}
		_, _ = w.Write([]byte("large response"))
	}))
	defer srv.Close()
	defer close(unblock)
	c, _ := mappedClient(t, srv, publicLookup)
	c.budget = b
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Fetch(ctx, "http://fetch.example/", 4); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("no headers")
	}
	if s := b.Stats(); s.IO != 1 || s.Total != 1 {
		t.Fatalf("body read released early: %+v", s)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("body cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("body read did not cancel")
	}
	if s := b.Stats(); s != (resources.Stats{}) {
		t.Fatalf("body cancel leaked: %+v", s)
	}
	if _, err := NewWithBudget(nil, nil); !errors.Is(err, ErrDenied) {
		t.Fatal("nil shared budget accepted")
	}
}
