package logging

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned by Flush after the writer has been closed.
var ErrClosed = errors.New("log writer closed")

type asyncEntry struct {
	line []byte
	ack  chan struct{}
}

// asyncWriter decouples request paths from sink latency. Each Write copies
// one formatted record into a bounded queue; when the queue is full the
// record is dropped and counted instead of blocking the caller.
type asyncWriter struct {
	sink    io.Writer
	queue   chan asyncEntry
	dropped atomic.Uint64
	failed  atomic.Uint64
	mu      sync.RWMutex
	closed  bool
	done    chan struct{}
}

func newAsyncWriter(sink io.Writer, capacity int) *asyncWriter {
	if capacity < 1 {
		capacity = 1
	}
	w := &asyncWriter{sink: sink, queue: make(chan asyncEntry, capacity), done: make(chan struct{})}
	go w.run()
	return w
}

func (w *asyncWriter) run() {
	defer close(w.done)
	for entry := range w.queue {
		if entry.ack != nil {
			switch s := w.sink.(type) {
			case interface{ Flush(context.Context) error }:
				_ = s.Flush(context.Background())
			case interface{ Sync() error }:
				_ = s.Sync()
			}
			close(entry.ack)
			continue
		}
		if _, err := w.sink.Write(entry.line); err != nil {
			w.failed.Add(1)
		}
	}
}

// Write never blocks on the sink. It reports success even when the record is
// dropped so slog handlers do not surface backpressure to callers.
func (w *asyncWriter) Write(p []byte) (int, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		w.dropped.Add(1)
		return len(p), nil
	}
	line := append([]byte(nil), p...)
	select {
	case w.queue <- asyncEntry{line: line}:
	default:
		w.dropped.Add(1)
	}
	return len(p), nil
}

// Flush waits until every record queued before the call reached the sink.
func (w *asyncWriter) Flush() error {
	w.mu.RLock()
	if w.closed {
		w.mu.RUnlock()
		return ErrClosed
	}
	ack := make(chan struct{})
	w.queue <- asyncEntry{ack: ack}
	w.mu.RUnlock()
	<-ack
	return nil
}

// Close drains the queue into the sink and closes the sink when it is a
// Closer. Later writes are counted as dropped.
func (w *asyncWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		<-w.done
		return nil
	}
	w.closed = true
	close(w.queue)
	w.mu.Unlock()
	<-w.done
	if c, ok := w.sink.(io.Closer); ok {
		return c.Close()
	}
	if s, ok := w.sink.(interface{ Sync() error }); ok {
		_ = s.Sync()
	}
	return nil
}

func (w *asyncWriter) Dropped() uint64 { return w.dropped.Load() }

func (w *asyncWriter) Failed() uint64 { return w.failed.Load() }
