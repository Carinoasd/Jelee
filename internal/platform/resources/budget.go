// Package resources coordinates instance-wide CPU, I/O and total admission.
package resources

import (
	"container/list"
	"context"
	"sync"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type Limits struct{ CPU, IO, Total, Queue int }
type Stats struct{ CPU, IO, Total, Waiting int }
type waiter struct {
	ctx     context.Context
	class   app.WorkClass
	ready   chan struct{}
	element *list.Element
	granted bool
}
type Budget struct {
	mu      sync.Mutex
	limits  Limits
	stats   Stats
	pending list.List
}

// Limits returns a copy of the immutable admission limits.
func (b *Budget) Limits() Limits { return b.limits }

func New(l Limits) (*Budget, error) {
	if l.CPU < 1 || l.CPU > 256 || l.IO < 1 || l.IO > 1024 || l.Total < 1 || l.Total > 1024 || l.Queue < 0 || l.Queue > 4096 {
		return nil, domain.ErrInvalid
	}
	return &Budget{limits: l}, nil
}
func (b *Budget) fits(c app.WorkClass) bool {
	return b.stats.Total < b.limits.Total && (c == app.WorkCPU && b.stats.CPU < b.limits.CPU || c == app.WorkIO && b.stats.IO < b.limits.IO)
}
func (b *Budget) change(c app.WorkClass, delta int) {
	b.stats.Total += delta
	if c == app.WorkCPU {
		b.stats.CPU += delta
	} else {
		b.stats.IO += delta
	}
}

// Oldest eligible work wins. A saturated class cannot block the other class
// when it has both a class slot and a total slot. No helper goroutine is used.
func (b *Budget) dispatch() {
	for e := b.pending.Front(); e != nil; {
		next := e.Next()
		w := e.Value.(*waiter)
		if w.ctx.Err() == nil && b.fits(w.class) {
			b.pending.Remove(e)
			w.element = nil
			b.stats.Waiting--
			b.change(w.class, 1)
			w.granted = true
			close(w.ready)
		}
		e = next
	}
}
func (b *Budget) release(c app.WorkClass) func() {
	var once sync.Once
	return func() { once.Do(func() { b.mu.Lock(); defer b.mu.Unlock(); b.change(c, -1); b.dispatch() }) }
}
func (b *Budget) Acquire(ctx context.Context, c app.WorkClass) (func(), error) {
	if ctx == nil || c != app.WorkCPU && c != app.WorkIO {
		return nil, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	// Drain eligible older requests before considering a new arrival.
	b.dispatch()
	if b.fits(c) {
		b.change(c, 1)
		b.mu.Unlock()
		release := b.release(c)
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	}
	if b.stats.Waiting >= b.limits.Queue {
		b.mu.Unlock()
		return nil, domain.ErrResourceBusy
	}
	w := &waiter{ctx: ctx, class: c, ready: make(chan struct{})}
	w.element = b.pending.PushBack(w)
	b.stats.Waiting++
	b.mu.Unlock()
	select {
	case <-w.ready:
	case <-ctx.Done():
	}
	b.mu.Lock()
	if err := ctx.Err(); err != nil {
		if w.granted {
			b.change(c, -1)
		} else {
			b.pending.Remove(w.element)
			b.stats.Waiting--
			w.element = nil
		}
		b.dispatch()
		b.mu.Unlock()
		return nil, err
	}
	b.mu.Unlock()
	return b.release(c), nil
}
func (b *Budget) Stats() Stats { b.mu.Lock(); defer b.mu.Unlock(); return b.stats }

var _ app.WorkBudget = (*Budget)(nil)
