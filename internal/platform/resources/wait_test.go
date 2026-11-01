package resources

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
)

// fakeNow is a settable clock for the wait histogram.
type fakeNow struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeNow) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeNow) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func waitSeries(t *testing.T, b *Budget, class, outcome string) WaitHistogram {
	t.Helper()
	series, _ := b.WaitStats()
	for _, s := range series {
		if s.Class == class && s.Outcome == outcome {
			return s.Histogram
		}
	}
	t.Fatalf("no series %s/%s", class, outcome)
	return WaitHistogram{}
}

// TestWaitHistogramByClassAndOutcome: immediate grants count as zero, a
// queued grant with the time it waited, a cancelled wait under its own
// outcome; a full queue is not observed. Buckets are exclusive, 1 ms to
// 60 s and +Inf.
func TestWaitHistogramByClassAndOutcome(t *testing.T) {
	b, err := New(Limits{CPU: 1, IO: 1, Total: 2, Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeNow{t: time.Unix(1_700_000_000, 0)}
	b.now = clock.now
	cpu, err := b.Acquire(context.Background(), app.WorkCPU)
	if err != nil {
		t.Fatal(err)
	}
	io, err := b.Acquire(context.Background(), app.WorkIO)
	if err != nil {
		t.Fatal(err)
	}
	// A queued CPU request granted after 2 s.
	done := make(chan error, 1)
	go func() {
		release, err := b.Acquire(context.Background(), app.WorkCPU)
		if release != nil {
			release()
		}
		done <- err
	}()
	queued(t, b, 1)
	// The queue is full: refused at once and not observed.
	if _, err := b.Acquire(context.Background(), app.WorkIO); err == nil {
		t.Fatal("full queue admitted")
	}
	clock.add(2 * time.Second)
	cpu()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// A queued I/O request cancelled after 90 s lands in +Inf.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		release, err := b.Acquire(ctx, app.WorkIO)
		if release != nil {
			release()
		}
		done <- err
	}()
	queued(t, b, 1)
	clock.add(90 * time.Second)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
	io()

	granted := waitSeries(t, b, "cpu", WaitGranted)
	if granted.Count != 2 || granted.SumSeconds != 2 || granted.BucketCounts[0] != 1 || granted.BucketCounts[9] != 1 {
		t.Fatalf("cpu granted: %+v", granted)
	}
	if h := waitSeries(t, b, "io", WaitGranted); h.Count != 1 || h.SumSeconds != 0 || h.BucketCounts[0] != 1 {
		t.Fatalf("io granted: %+v", h)
	}
	if h := waitSeries(t, b, "io", WaitCancelled); h.Count != 1 || h.SumSeconds != 90 || h.BucketCounts[len(h.BucketCounts)-1] != 1 {
		t.Fatalf("io cancelled: %+v", h)
	}
	if h := waitSeries(t, b, "cpu", WaitCancelled); h.Count != 0 {
		t.Fatalf("cpu cancelled: %+v", h)
	}
	bounds := WaitBoundsSeconds()
	if bounds[0] != 0.001 || bounds[len(bounds)-1] != 60 {
		t.Fatalf("bounds %v", bounds)
	}
	for i := 1; i < len(bounds); i++ {
		if bounds[i] <= bounds[i-1] {
			t.Fatalf("bounds not increasing: %v", bounds)
		}
	}
	if s := b.Stats(); s.Total != 0 || s.Waiting != 0 {
		t.Fatalf("permits left: %+v", s)
	}
}

// A sample equal to a bound belongs to that bucket.
func TestWaitHistogramBoundIsInclusive(t *testing.T) {
	b, err := New(Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.observe(app.WorkIO, true, time.Millisecond)
	b.observe(app.WorkIO, true, 60*time.Second)
	b.observe(app.WorkIO, true, -time.Second)
	b.mu.Unlock()
	h := waitSeries(t, b, "io", WaitGranted)
	if h.Count != 3 || h.BucketCounts[0] != 2 || h.BucketCounts[13] != 1 || h.BucketCounts[14] != 0 {
		t.Fatalf("buckets %+v", h)
	}
}
