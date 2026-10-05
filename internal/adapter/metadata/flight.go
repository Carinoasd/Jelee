package metadata

import (
	"context"
	"sync"
	"time"
)

// flightGroup coalesces concurrent cache misses for one key. Unlike a plain
// singleflight, the shared fetch does not run on the first caller's context:
// each waiter may leave on its own cancellation or deadline without failing
// the others, and the fetch is cancelled once every waiter has left. A result
// is published only while at least one waiter still wants it, and only after
// a successful fetch, so errors and abandoned results are never cached.
type flightGroup[K comparable, V any] struct {
	mu      sync.Mutex
	flights map[K]*flight[V]
}

type flight[V any] struct {
	done    chan struct{}
	cancel  context.CancelFunc
	next    uint64
	waiters map[uint64]context.Context
	value   V
	err     error
}

// do runs fetch at most once per key among overlapping callers. fetch receives
// a context that keeps the first caller's values, drops its cancellation and
// deadline, and carries the provider budget. publish stores a successful
// result; it runs under the group lock and must not call back into the group.
func (g *flightGroup[K, V]) do(ctx context.Context, key K, budget time.Duration, fetch func(context.Context) (V, error), publish func(V)) (V, error) {
	var zero V
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	g.mu.Lock()
	if g.flights == nil {
		g.flights = make(map[K]*flight[V])
	}
	f := g.flights[key]
	if f == nil {
		shared, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
		f = &flight[V]{done: make(chan struct{}), cancel: cancel, waiters: make(map[uint64]context.Context)}
		g.flights[key] = f
		// One goroutine per in-flight key; it ends with the bounded fetch.
		go g.run(shared, key, f, fetch, publish)
	}
	id := f.next
	f.next++
	f.waiters[id] = ctx
	g.mu.Unlock()
	select {
	case <-f.done:
		// A caller cancelled at completion still sees its own cancellation.
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return f.value, f.err
	case <-ctx.Done():
		g.mu.Lock()
		if _, waiting := f.waiters[id]; waiting {
			delete(f.waiters, id)
			if len(f.waiters) == 0 {
				// Nobody is waiting: stop the fetch and let a later caller
				// start a fresh flight instead of joining a cancelled one.
				if g.flights[key] == f {
					delete(g.flights, key)
				}
				f.cancel()
			}
		}
		g.mu.Unlock()
		return zero, ctx.Err()
	}
}

func (g *flightGroup[K, V]) run(ctx context.Context, key K, f *flight[V], fetch func(context.Context) (V, error), publish func(V)) {
	defer f.cancel()
	var value V
	err := ErrUnavailable
	// Waiters are released even if fetch panics or exits the goroutine; the
	// failure is reported as unavailable and nothing is published.
	defer func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.flights[key] == f {
			delete(g.flights, key)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil && f.wanted() {
			publish(value)
		}
		f.value, f.err = value, err
		close(f.done)
	}()
	value, err = fetch(ctx)
}

// wanted reports whether a registered waiter has not been cancelled yet.
// Cancellation is checked directly, so a caller that cancelled just before
// completion never publishes a result it did not wait for.
func (f *flight[V]) wanted() bool {
	for _, ctx := range f.waiters {
		if ctx.Err() == nil {
			return true
		}
	}
	return false
}
