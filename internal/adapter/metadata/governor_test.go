package metadata

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

func TestGovernorSpacesStartsWithoutCancellationReservations(t *testing.T) {
	g := newRequestGovernor()
	g.interval = 60 * time.Millisecond
	release, err := g.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if _, err := g.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	started := time.Now()
	release, err = g.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatal("cancelled waiter reserved another future request")
	}
	if len(g.slots) != 0 {
		t.Fatal("cancelled limiter leaked a slot")
	}
}

func TestGovernorBoundsConcurrencyAndReleasesAllSlots(t *testing.T) {
	c, _ := NewTMDB(testKey)
	defer c.Close()
	c.governor.interval = 0
	active := new(atomic.Int32)
	peak := new(atomic.Int32)
	entered := make(chan struct{}, 8)
	unblock := make(chan struct{})
	c.fetch = func(ctx context.Context, _ string, _ int64) (outbound.Response, error) {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		defer active.Add(-1)
		entered <- struct{}{}
		select {
		case <-unblock:
			return outbound.Response{Status: 200}, nil
		case <-ctx.Done():
			return outbound.Response{}, ctx.Err()
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.governedFetch(context.Background(), "ignored", 1, 0) }()
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(unblock) }); wg.Wait() })
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("four requests did not enter")
		}
	}
	select {
	case <-entered:
		t.Fatal("fifth request exceeded concurrency bound")
	case <-time.After(30 * time.Millisecond):
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.governedFetch(ctx, "ignored", 1, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued request error=%v", err)
	}
	once.Do(func() { close(unblock) })
	wg.Wait()
	if peak.Load() != 4 || active.Load() != 0 || len(c.governor.slots) != 0 {
		t.Fatalf("peak=%d active=%d slots=%d", peak.Load(), active.Load(), len(c.governor.slots))
	}
}

func TestGovernorRechecksExtendedCooldown(t *testing.T) {
	g := newRequestGovernor()
	g.interval = 0
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	g.postpone(60 * time.Millisecond)
	var delays []time.Duration
	g.wait = func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		if len(delays) == 1 {
			g.postpone(100 * time.Millisecond)
		}
		now = now.Add(d)
		return nil
	}
	release, err := g.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if len(delays) != 2 || delays[0] != 60*time.Millisecond || delays[1] != 40*time.Millisecond {
		t.Fatalf("extended cooldown waits=%v", delays)
	}
}

func TestLast429PublishesCooldownForOtherCalls(t *testing.T) {
	c, _ := NewTMDB(testKey)
	defer c.Close()
	c.governor.interval = 0
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		return outbound.Response{Status: 429, RetryAfter: "86400"}, nil
	}
	if err := c.ValidateCredentials(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error=%v", err)
	}
	c.governor.mu.Lock()
	until := c.governor.cooldown
	c.governor.mu.Unlock()
	if time.Until(until) < 23*time.Hour {
		t.Fatal("server minimum shortened for other calls")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.governedFetch(ctx, "ignored", 1, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared cooldown bypassed: %v", err)
	}
	if len(c.governor.slots) != 0 {
		t.Fatal("cooldown cancellation leaked slot")
	}
}
