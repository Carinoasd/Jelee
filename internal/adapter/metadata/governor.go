package metadata

import (
	"context"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

// requestGovernor is owned by one provider adapter, with no background workers
// or reservations that survive a cancelled waiter.
type requestGovernor struct {
	slots    chan struct{}
	mu       sync.Mutex
	next     time.Time
	cooldown time.Time
	interval time.Duration
	now      func() time.Time
	wait     func(context.Context, time.Duration) error
}

func newRequestGovernor() *requestGovernor {
	return &requestGovernor{slots: make(chan struct{}, 4), interval: 250 * time.Millisecond, now: time.Now, wait: waitRetry}
}

func (g *requestGovernor) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-g.slots }
	for {
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		g.mu.Lock()
		now := g.now()
		ready := g.next
		if g.cooldown.After(ready) {
			ready = g.cooldown
		}
		if !ready.After(now) {
			g.next = now.Add(g.interval)
			g.mu.Unlock()
			return release, nil
		}
		g.mu.Unlock()
		if err := g.wait(ctx, ready.Sub(now)); err != nil {
			release()
			return nil, err
		}
		// Recheck under the mutex: another response may extend the cooldown.
	}
}

func (g *requestGovernor) postpone(delay time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	until := g.now().Add(delay)
	if until.After(g.cooldown) {
		g.cooldown = until
	}
}

func (t *TMDB) governedFetch(ctx context.Context, target string, maxBytes int64, attempt int) (outbound.Response, error) {
	if t.governor == nil {
		return t.fetch(ctx, target, maxBytes)
	} // private protocol-test seam
	release, err := t.governor.acquire(ctx)
	if err != nil {
		return outbound.Response{}, err
	}
	defer release()
	r, err := t.fetch(ctx, target, maxBytes)
	// Publish before releasing capacity to another caller. Requests already
	// in flight cannot be retroactively prevented by this local governor.
	if err == nil && (r.Status == 429 || r.Status == 503) {
		if delay, ok := retryDelay(r.RetryAfter, t.now(), attempt); ok {
			t.governor.postpone(delay)
		} else {
			t.governor.postpone(15 * time.Second)
		}
	}
	return r, err
}
