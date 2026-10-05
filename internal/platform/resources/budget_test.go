package resources

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func queued(t *testing.T, b *Budget, n int) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for b.Stats().Waiting != n {
		select {
		case <-deadline.C:
			t.Fatal("waiter did not reach expected queue state")
		case <-tick.C:
		}
	}
}
func take(t *testing.T, b *Budget, c app.WorkClass) func() {
	t.Helper()
	r, e := b.Acquire(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestClassesTotalQueueAndCancellation(t *testing.T) {
	b, _ := New(Limits{CPU: 1, IO: 2, Total: 2, Queue: 1})
	cpu := take(t, b, app.WorkCPU)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		r, e := b.Acquire(ctx, app.WorkCPU)
		if r != nil {
			r()
		}
		done <- e
	}()
	queued(t, b, 1)
	// The waiting CPU task does not reserve a total slot or block eligible I/O.
	io := take(t, b, app.WorkIO)
	if r, e := b.Acquire(context.Background(), app.WorkIO); r != nil || e != domain.ErrResourceBusy {
		t.Fatal("total or queue bound bypassed")
	}
	if s := b.Stats(); s != (Stats{CPU: 1, IO: 1, Total: 2, Waiting: 1}) {
		t.Fatalf("unexpected admission: %+v", s)
	}
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal("queued cancellation lost")
	}
	cpu()
	cpu()
	io()
	if b.Stats() != (Stats{}) {
		t.Fatal("permit or waiter leaked")
	}
}
func TestOldestEligibleWaiterIsAdmittedFirst(t *testing.T) {
	b, _ := New(Limits{CPU: 1, IO: 1, Total: 1, Queue: 2})
	held := take(t, b, app.WorkIO)
	admitted := make(chan int, 2)
	finish := make(chan struct{})
	var wg sync.WaitGroup
	for i, c := range []app.WorkClass{app.WorkCPU, app.WorkIO} {
		wg.Go(func() {
			release, err := b.Acquire(context.Background(), c)
			if err != nil {
				t.Error(err)
				return
			}
			admitted <- i
			<-finish
			release()
		})
		queued(t, b, i+1)
	}
	held()
	if first := <-admitted; first != 0 {
		t.Fatal("newer waiter overtook older eligible work")
	}
	finish <- struct{}{}
	if second := <-admitted; second != 1 {
		t.Fatal("second waiter lost")
	}
	finish <- struct{}{}
	wg.Wait()
	if b.Stats() != (Stats{}) {
		t.Fatal("final permit leaked")
	}
}
func TestConcurrentAdmissionNeverExceedsLimits(t *testing.T) {
	b, _ := New(Limits{CPU: 2, IO: 3, Total: 4, Queue: 64})
	var wg sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wg.Go(func() {
			for iteration := 0; iteration < 100; iteration++ {
				ctx, cancel := context.WithCancel(context.Background())
				if iteration%3 == 0 {
					cancel()
				}
				c := app.WorkCPU
				if worker%2 == 0 {
					c = app.WorkIO
				}
				release, err := b.Acquire(ctx, c)
				cancel()
				if err == nil {
					s := b.Stats()
					if s.CPU > 2 || s.IO > 3 || s.Total > 4 || s.Waiting > 64 {
						t.Errorf("budget exceeded: %+v", s)
					}
					release()
					release()
				} else if !errors.Is(err, context.Canceled) && !errors.Is(err, domain.ErrResourceBusy) {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if b.Stats() != (Stats{}) {
		t.Fatal("stress leaked permits")
	}
}

// Queue the request before racing cancellation against release. Either success
// or cancellation is valid, but both paths must return all class/total permits.
func TestQueuedCancellationRacesGrant(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		b, _ := New(Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
		held := take(t, b, app.WorkIO)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		done := make(chan error, 1)
		go func() {
			release, err := b.Acquire(ctx, app.WorkCPU)
			if release != nil {
				release()
				release()
			}
			done <- err
		}()
		queued(t, b, 1)
		start := make(chan struct{})
		var racers sync.WaitGroup
		racers.Go(func() { <-start; cancel() })
		racers.Go(func() { <-start; held() })
		close(start)
		racers.Wait()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("queued acquire did not finish after release and cancellation")
		}
		if s := b.Stats(); s != (Stats{}) {
			t.Fatalf("iteration %d leaked: %+v", iteration, s)
		}
	}
}

func TestInvalidLimitsAndRequests(t *testing.T) {
	for _, limits := range []Limits{
		{0, 1, 1, 0}, {257, 1, 1, 0}, {1, 0, 1, 0}, {1, 1025, 1, 0},
		{1, 1, 0, 0}, {1, 1, 1025, 0}, {1, 1, 1, -1}, {1, 1, 1, 4097},
	} {
		if b, err := New(limits); b != nil || !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid limits accepted: %+v", limits)
		}
	}
	b, err := New(Limits{1, 1, 1, 0})
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range []app.WorkClass{0, 3, 255} {
		if release, err := b.Acquire(context.Background(), class); release != nil || !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("invalid class accepted")
		}
	}
	if release, err := b.Acquire(nil, app.WorkCPU); release != nil || !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil context accepted")
	}
	release := take(t, b, app.WorkIO)
	if next, err := b.Acquire(context.Background(), app.WorkCPU); next != nil || !errors.Is(err, domain.ErrResourceBusy) {
		t.Fatal("zero queue did not reject immediately")
	}
	release()
	if b.Stats() != (Stats{}) {
		t.Fatal("invalid request changed counters")
	}
}

func TestCancellationAfterGrantRefundsBeforeAcquireReturns(t *testing.T) {
	b, _ := New(Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
	// Reserve the initial slot directly so its release and dispatch can be
	// controlled under the mutex below.
	b.mu.Lock()
	b.change(app.WorkIO, 1)
	b.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		release, err := b.Acquire(ctx, app.WorkCPU)
		if release != nil {
			release()
		}
		done <- err
	}()
	queued(t, b, 1)
	// Hold the admission mutex across dispatch and cancellation so the awakened
	// waiter must observe cancellation after its permit was already granted.
	b.mu.Lock()
	b.change(app.WorkIO, -1)
	b.dispatch()
	granted := b.stats == (Stats{CPU: 1, Total: 1})
	cancel()
	b.mu.Unlock()
	if !granted {
		t.Fatal("test did not reach granted-before-return state")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled granted waiter stalled")
	}
	if s := b.Stats(); s != (Stats{}) {
		t.Fatalf("granted cancellation leaked: %+v", s)
	}
}
