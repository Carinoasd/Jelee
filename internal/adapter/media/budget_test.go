package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

type budgetWriter struct {
	*httptest.ResponseRecorder
	t      *testing.T
	budget *resources.Budget
}

func (w budgetWriter) Write(p []byte) (int, error) {
	if s := w.budget.Stats(); s.IO != 1 || s.Total != 1 {
		w.t.Errorf("stream wrote without permit: %+v", s)
	}
	return w.ResponseRecorder.Write(p)
}
func TestDirectSharedBudgetRelease(t *testing.T) {
	source, _ := fixture(t)
	for _, missing := range []bool{false, true} {
		b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
		resolved := source
		if missing {
			resolved.RelativePath = "missing.mkv"
		}
		h, err := NewHandler(resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return resolved, nil }), Options{Budget: b, MaxConcurrent: 1, WriteTimeout: time.Second, WriteError: testWriteError})
		if err != nil {
			t.Fatal(err)
		}
		r := nativeRequest(http.MethodGet, "/stream")
		r.Header.Set("Range", "bytes=0-9")
		w := httptest.NewRecorder()
		h.ServeSource(budgetWriter{w, t, b}, r, "source")
		want := http.StatusPartialContent
		if missing {
			want = http.StatusNotFound
		}
		if w.Code != want {
			t.Fatalf("status %d want %d", w.Code, want)
		}
		if b.Stats() != (resources.Stats{}) {
			t.Fatal("stream leaked shared permit")
		}
	}
}
func TestDirectSharedTotalBackpressureAndCancel(t *testing.T) {
	for _, mode := range []string{"queue-full", "timeout", "cancel", "resume"} {
		t.Run(mode, func(t *testing.T) {
			queue := 1
			if mode == "queue-full" {
				queue = 0
			}
			b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: queue})
			held, err := b.Acquire(context.Background(), app.WorkCPU)
			if err != nil {
				t.Fatal(err)
			}
			defer held()
			source, _ := fixture(t)
			timeout := time.Second
			if mode == "timeout" {
				timeout = 20 * time.Millisecond
			}
			h, err := NewHandler(resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }), Options{Budget: b, MaxConcurrent: 1, LookupTimeout: timeout, WriteTimeout: time.Second, WriteError: testWriteError})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := nativeRequest(http.MethodGet, "/stream")
			r = r.WithContext(access.WithPrincipal(ctx, access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientNative}))
			r.Header.Set("Range", "bytes=0-9")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); h.ServeSource(w, r, "source") }()
			if mode == "cancel" || mode == "resume" {
				deadline := time.NewTimer(2 * time.Second)
				defer deadline.Stop()
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				for b.Stats().Waiting != 1 {
					select {
					case <-deadline.C:
						t.Fatal("stream never queued behind CPU total permit")
					case <-tick.C:
					}
				}
				if mode == "cancel" {
					cancel()
				} else {
					held()
				}
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("stream did not finish")
			}
			if mode == "cancel" {
				if w.Body.Len() != 0 {
					t.Fatal("cancelled stream wrote a body")
				}
			} else if mode == "resume" {
				if w.Code != 206 || w.Body.Len() != 10 {
					t.Fatal("queued stream did not resume")
				}
			} else if w.Code != 429 || w.Header().Get("Retry-After") != "1" {
				t.Fatal("backpressure was not retryable")
			}
			held()
			if b.Stats() != (resources.Stats{}) || len(h.slots) != 0 {
				t.Fatal("permit or local stream slot leaked")
			}
		})
	}
}
