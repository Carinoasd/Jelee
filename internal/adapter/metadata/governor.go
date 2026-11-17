package metadata

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

// ErrDailyBudget reports that the configured daily request limit of this
// adapter is spent. No request is sent until the next UTC day.
var ErrDailyBudget = errors.New("tmdb_daily_budget_exhausted")

// Governance is the request budget of one TMDB adapter. It is process local:
// separate processes or adapters each enforce their own copy.
type Governance struct {
	// RequestInterval is the minimum spacing between request starts.
	RequestInterval time.Duration
	// MaxConcurrent bounds requests holding capacity, including waits.
	MaxConcurrent int
	// DailyLimit bounds request attempts (retries included) per UTC day;
	// zero means no daily limit.
	DailyLimit int
}

// Governance bounds shared with the configuration loader.
const (
	MinRequestInterval = 20 * time.Millisecond
	MaxRequestInterval = time.Minute
	MaxConcurrentLimit = 32
	MaxDailyLimit      = 10_000_000
)

// DefaultGovernance is the budget used when nothing is configured.
func DefaultGovernance() Governance {
	return Governance{RequestInterval: 250 * time.Millisecond, MaxConcurrent: 4}
}

func (g Governance) valid() bool {
	return g.RequestInterval >= MinRequestInterval && g.RequestInterval <= MaxRequestInterval && g.MaxConcurrent >= 1 && g.MaxConcurrent <= MaxConcurrentLimit && g.DailyLimit >= 0 && g.DailyLimit <= MaxDailyLimit
}

// requestGovernor is owned by one provider adapter, with no background workers
// or reservations that survive a cancelled waiter.
type requestGovernor struct {
	slots    chan struct{}
	mu       sync.Mutex
	next     time.Time
	cooldown time.Time
	interval time.Duration
	daily    int
	day      time.Time
	used     int
	now      func() time.Time
	wait     func(context.Context, time.Duration) error
}

func newRequestGovernor() *requestGovernor {
	return newGovernedRequests(DefaultGovernance())
}

func newGovernedRequests(g Governance) *requestGovernor {
	return &requestGovernor{slots: make(chan struct{}, g.MaxConcurrent), interval: g.RequestInterval, daily: g.DailyLimit, now: time.Now, wait: waitRetry}
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
			if g.daily > 0 {
				day := now.UTC().Truncate(24 * time.Hour)
				if !day.Equal(g.day) {
					g.day, g.used = day, 0
				}
				if g.used >= g.daily {
					g.mu.Unlock()
					release()
					return nil, ErrDailyBudget
				}
				g.used++
			}
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
