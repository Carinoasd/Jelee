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
	mu     sync.Mutex
	limits Limits
	// effective is the admission ceiling in force. It equals limits unless
	// the adaptive controller (G41.7) lowered it; it never exceeds limits.
	effective    Limits
	pressure     string
	stats        Stats
	pending      list.List
	payloadLimit int64
	payloadUsed  int64
}

// Limits returns a copy of the immutable configured admission limits.
func (b *Budget) Limits() Limits { return b.limits }

// EffectiveLimits returns the ceiling currently in force and the pressure
// source that lowered it ("" at the configured limits).
func (b *Budget) EffectiveLimits() (Limits, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.effective, b.pressure
}

// SetEffective lowers or restores the CPU, I/O and total ceilings. Each value
// is clamped to [1, configured]; the queue bound is never changed. Lowering
// revokes no permit: admitted work finishes and only new admissions wait.
// Raising dispatches waiting work at once.
func (b *Budget) SetEffective(l Limits, source string) Limits {
	clamp := func(v, limit int) int { return max(1, min(v, limit)) }
	b.mu.Lock()
	defer b.mu.Unlock()
	b.effective = Limits{CPU: clamp(l.CPU, b.limits.CPU), IO: clamp(l.IO, b.limits.IO), Total: clamp(l.Total, b.limits.Total), Queue: b.limits.Queue}
	b.pressure = source
	if b.effective == b.limits {
		b.pressure = ""
	}
	b.dispatch()
	return b.effective
}

func New(l Limits) (*Budget, error) {
	return NewWithPayloadLimit(l, 192<<20)
}

// NewWithPayloadLimit configures one shared raw-payload reservation limit.
// Runtime consumers must share this Budget, including across Writer instances.
func NewWithPayloadLimit(l Limits, payloadBytes int64) (*Budget, error) {
	if l.CPU < 1 || l.CPU > 256 || l.IO < 1 || l.IO > 1024 || l.Total < 1 || l.Total > 1024 || l.Queue < 0 || l.Queue > 4096 {
		return nil, domain.ErrInvalid
	}
	if payloadBytes < 1 || payloadBytes > 1<<40 {
		return nil, domain.ErrInvalid
	}
	return &Budget{limits: l, effective: l, payloadLimit: payloadBytes}, nil
}

func (b *Budget) PayloadBytes() (used, limit int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.payloadUsed, b.payloadLimit
}

func (b *Budget) ReservePayloadBytes(ctx context.Context, bytes int64) (func(), error) {
	if ctx == nil || bytes < 1 {
		return nil, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if bytes > b.payloadLimit-b.payloadUsed {
		b.mu.Unlock()
		return nil, domain.ErrResourceBusy
	}
	b.payloadUsed += bytes
	b.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { b.mu.Lock(); b.payloadUsed -= bytes; b.mu.Unlock() }) }
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
func (b *Budget) fits(c app.WorkClass) bool {
	return b.stats.Total < b.effective.Total && (c == app.WorkCPU && b.stats.CPU < b.effective.CPU || c == app.WorkIO && b.stats.IO < b.effective.IO)
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
