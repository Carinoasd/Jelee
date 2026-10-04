package subtitleocr

import (
	"context"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
)

// fakeClock only moves when a test advances it.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves time forward and fires every timer that became due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, waiter := range c.waiters {
		if !waiter.at.After(c.now) {
			waiter.ch <- c.now
			continue
		}
		kept = append(kept, waiter)
	}
	c.waiters = kept
	c.mu.Unlock()
}

// Pending reports how many timers wait.
func (c *fakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRateLimiterAdmitsAtMostNPerRollingMinute(t *testing.T) {
	clock := newFakeClock()
	limiter, err := NewRateLimiter(3, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 3 {
		if err := limiter.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	admitted := make(chan error, 1)
	go func() { admitted <- limiter.Wait(ctx) }()
	waitFor(t, func() bool { return clock.Pending() == 1 })
	clock.Advance(59 * time.Second)
	select {
	case <-admitted:
		t.Fatal("fourth picture admitted inside the first minute")
	case <-time.After(20 * time.Millisecond):
	}
	clock.Advance(time.Second)
	if err := <-admitted; err != nil {
		t.Fatal(err)
	}
	if limiter.Waits() != 1 {
		t.Fatalf("waits %d", limiter.Waits())
	}
	// The two other first-minute admissions have left the window as well.
	for range 2 {
		if err := limiter.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Cancellation ends a wait.
	cancelled, cancel := context.WithCancel(ctx)
	go func() { admitted <- limiter.Wait(cancelled) }()
	waitFor(t, func() bool { return clock.Pending() == 1 })
	cancel()
	if err := <-admitted; err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	if err := limiter.Wait(cancelled); err != context.Canceled {
		t.Fatal("cancelled context admitted")
	}
	for _, rate := range []int{0, -1, MaxPicturesPerMinute + 1} {
		if _, err := NewRateLimiter(rate, clock); err != ErrInvalidRate {
			t.Fatalf("rate %d accepted", rate)
		}
	}
	if system, err := NewRateLimiter(1, nil); err != nil || system.Wait(ctx) != nil {
		t.Fatal("system clock limiter")
	}
}

func TestRateLimiterWindowPropertyUnderRandomArrivals(t *testing.T) {
	clock := newFakeClock()
	const perMinute = 7
	limiter, err := NewRateLimiter(perMinute, clock)
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // G404: deterministic test arrivals
	var admissions []time.Time
	for range 200 {
		clock.Advance(time.Duration(random.IntN(20000)) * time.Millisecond)
		done := make(chan error, 1)
		go func() { done <- limiter.Wait(context.Background()) }()
		for {
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Millisecond):
				if clock.Pending() > 0 {
					clock.Advance(500 * time.Millisecond)
				}
				continue
			}
			break
		}
		admissions = append(admissions, clock.Now())
	}
	for i := range admissions {
		count := 0
		for _, other := range admissions[i:] {
			if other.Sub(admissions[i]) < time.Minute {
				count++
			}
		}
		if count > perMinute {
			t.Fatalf("%d admissions within one minute of %v", count, admissions[i])
		}
	}
}
