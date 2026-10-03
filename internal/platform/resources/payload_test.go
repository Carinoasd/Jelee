package resources

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.PayloadBudget = (*Budget)(nil)

func TestPayloadReservationsConcurrentAndIndependent(t *testing.T) {
	b, err := NewWithPayloadLimit(Limits{CPU: 1, IO: 1, Total: 1, Queue: 0}, 64)
	if err != nil {
		t.Fatal(err)
	}
	class := take(t, b, app.WorkCPU)
	results := make(chan func(), 128)
	for i := 0; i < 128; i++ {
		go func() {
			release, err := b.ReservePayloadBytes(context.Background(), 1)
			if err != nil && !errors.Is(err, domain.ErrResourceBusy) {
				t.Error(err)
			}
			results <- release
		}()
	}
	var releases []func()
	for i := 0; i < 128; i++ {
		if r := <-results; r != nil {
			releases = append(releases, r)
		}
	}
	used, limit := b.PayloadBytes()
	if len(releases) != 64 || used != 64 || limit != 64 || b.Stats().CPU != 1 {
		t.Fatal("shared payload bound or class interference")
	}
	var wg sync.WaitGroup
	for _, r := range releases {
		for i := 0; i < 4; i++ {
			wg.Go(r)
		}
	}
	wg.Wait()
	class()
	used, _ = b.PayloadBytes()
	if used != 0 || b.Stats() != (Stats{}) {
		t.Fatal("reservation leaked or released twice")
	}
}

func TestPayloadReservationsInvalidBusyAndCancelled(t *testing.T) {
	for _, limit := range []int64{-1, 0, 1<<40 + 1} {
		if _, err := NewWithPayloadLimit(Limits{CPU: 1, IO: 1, Total: 1}, limit); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("invalid limit accepted")
		}
	}
	b, _ := NewWithPayloadLimit(Limits{CPU: 1, IO: 1, Total: 1}, 3)
	for _, size := range []int64{-1, 0} {
		if r, err := b.ReservePayloadBytes(context.Background(), size); r != nil || !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("invalid size accepted")
		}
	}
	if r, err := b.ReservePayloadBytes(nil, 1); r != nil || !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil context accepted")
	}
	for _, size := range []int64{4, math.MaxInt64} {
		if r, err := b.ReservePayloadBytes(context.Background(), size); r != nil || !errors.Is(err, domain.ErrResourceBusy) {
			t.Fatal("oversize admission")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := b.ReservePayloadBytes(ctx, 1); r != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled reservation")
	}
	r, err := b.ReservePayloadBytes(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := b.ReservePayloadBytes(context.Background(), 1); second != nil || !errors.Is(err, domain.ErrResourceBusy) {
		t.Fatal("capacity exceeded")
	}
	r()
	r()
	if used, _ := b.PayloadBytes(); used != 0 {
		t.Fatal("bytes leaked")
	}
	defaultBudget, _ := New(Limits{CPU: 1, IO: 1, Total: 1})
	if _, limit := defaultBudget.PayloadBytes(); limit != 192<<20 {
		t.Fatal("default changed")
	}
}
