package logging

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// gateSink blocks every write until released, simulating a stalled sink.
type gateSink struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	out     syncBuffer
	closed  bool
	mu      sync.Mutex
}

func newGateSink() *gateSink {
	return &gateSink{started: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateSink) Write(p []byte) (int, error) {
	g.once.Do(func() { close(g.started) })
	<-g.release
	return g.out.Write(p)
}

func (g *gateSink) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	return nil
}

func TestFullQueueDropsAndCountsWithoutBlocking(t *testing.T) {
	sink := newGateSink()
	w := newAsyncWriter(sink, 4)
	w.Write([]byte("first\n"))
	<-sink.started // the worker now holds "first" and is stalled
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			w.Write([]byte("queued\n"))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked on a stalled sink")
	}
	if got := w.Dropped(); got != 6 {
		t.Fatalf("dropped=%d want=6", got)
	}
	close(sink.release)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(sink.out.String(), "\n"); got != 5 {
		t.Fatalf("delivered=%d want=5", got)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
	w.Write([]byte("late\n"))
	if w.Dropped() != 7 || strings.Contains(sink.out.String(), "late") {
		t.Fatal("write after close was not dropped")
	}
	if !errors.Is(w.Flush(), ErrClosed) {
		t.Fatal("flush after close")
	}
	if err := w.Close(); err != nil {
		t.Fatal("second close")
	}
}

func TestWriteCopiesCallerBuffer(t *testing.T) {
	sink := newGateSink()
	w := newAsyncWriter(sink, 4)
	buf := []byte("original\n")
	w.Write(buf)
	copy(buf, "mutated!\n")
	close(sink.release)
	w.Close()
	if sink.out.String() != "original\n" {
		t.Fatalf("queued record aliased caller buffer: %q", sink.out.String())
	}
}

func TestFlushAndCloseDeliverEveryQueuedRecord(t *testing.T) {
	r, out := openTest(t, Options{BufferEntries: 10000})
	for i := 0; i < 1000; i++ {
		r.Logger().Info("flushed record", "count", i)
	}
	if got := strings.Count(flushed(t, r, out), "flushed record"); got != 1000 || r.Dropped() != 0 {
		t.Fatalf("flush delivered %d dropped %d", got, r.Dropped())
	}
	for i := 0; i < 500; i++ {
		r.Logger().Info("closed record", "count", i)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out.String(), "closed record"); got != 500 {
		t.Fatalf("close delivered %d", got)
	}
	r.Logger().Info("after close")
	if r.Dropped() != 1 || strings.Contains(out.String(), "after close") {
		t.Fatal("record after close not counted as dropped")
	}
}

func TestRouterDroppedAggregatesSinks(t *testing.T) {
	sink := newGateSink()
	r, err := Open(Options{BufferEntries: 1}, sink)
	if err != nil {
		t.Fatal(err)
	}
	r.Logger().Info("held")
	<-sink.started
	for i := 0; i < 5; i++ {
		r.Logger().Info("burst")
	}
	if r.Dropped() != 4 {
		t.Fatalf("dropped=%d want=4", r.Dropped())
	}
	close(sink.release)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if sink.closed {
		t.Fatal("router closed the process stdout")
	}
}
