package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Forwarder defaults and bounds (G46.1, G46.8).
const (
	DefaultForwardQueueEntries  = 4096
	MaxForwardQueueEntries      = 1 << 20
	DefaultForwardBatchSize     = 100
	MaxForwardBatchSize         = 10000
	DefaultForwardBatchInterval = time.Second
	MinForwardBatchInterval     = 10 * time.Millisecond
	MaxForwardBatchInterval     = time.Minute
	DefaultForwardTimeout       = 10 * time.Second
	MaxForwardTimeout           = 2 * time.Minute
	// DefaultForwardFlushTimeout bounds Flush and Close when the caller
	// brings no deadline of its own, so shutdown never waits on a dead
	// collector.
	DefaultForwardFlushTimeout = 5 * time.Second
	// maxForwardBatchBytes flushes a batch early so one push stays small.
	maxForwardBatchBytes = 1 << 20
	// maxForwardRecordBytes is the longest record a collector gets; a longer
	// one is dropped and counted rather than cut in the middle of its JSON.
	maxForwardRecordBytes = 256 << 10
)

// Retry policy: exponential backoff with jitter between attempts for one
// batch; after forwardMaxAttempts the batch is dropped and counted.
const (
	forwardBackoffMin   = 500 * time.Millisecond
	forwardBackoffMax   = 30 * time.Second
	forwardMaxAttempts  = 8
	forwardMaxRetryWait = time.Minute
)

// ErrForwardDeadline is returned by Flush when the collector did not take the
// pending records before the deadline.
var ErrForwardDeadline = errors.New("log forwarder flush deadline exceeded")

// ForwardStat is what a forwarder lost or shipped since it started.
type ForwardStat struct {
	// Destination is the metric label: the configured name.
	Destination string
	Kind        ForwarderKind
	// Dropped counts records discarded because the queue was full or closed,
	// the record was oversized, or delivery gave up on its batch.
	Dropped uint64
	// Failures counts failed delivery attempts (including retried ones).
	Failures uint64
	// Sent counts records the collector accepted.
	Sent uint64
}

// forwardStatter is implemented by the forwarders of this package; the router
// reads it for the destination metrics.
type forwardStatter interface {
	ForwardStat() ForwardStat
}

// deliverError describes a failed push. Class is a fixed word safe to print
// (never the collector address or response body).
type deliverError struct {
	class string
	// retry is false for a rejection retrying cannot cure (HTTP 400).
	retry bool
	// wait is a collector requested minimum delay (Retry-After).
	wait time.Duration
}

func (e *deliverError) Error() string { return "log delivery failed: " + e.class }

// batchSender pushes one batch of complete JSON records to a collector. It is
// only called from the forwarder worker goroutine.
type batchSender interface {
	send(ctx context.Context, records [][]byte) error
	close()
}

type forwardFlush struct {
	ctx  context.Context
	done chan error
}

// forwardCore is the shared bounded queue, batching, backoff and shutdown
// logic of the syslog and Loki forwarders. Write only copies a record into
// the queue or counts a drop, so the request path never waits on a collector.
type forwardCore struct {
	name     string
	kind     ForwarderKind
	sender   batchSender
	queue    chan []byte
	flushReq chan forwardFlush
	stop     chan struct{}
	done     chan struct{}

	batchSize     int
	batchInterval time.Duration
	maxRecord     int
	flushTimeout  time.Duration
	backoffMin    time.Duration
	backoffMax    time.Duration
	maxAttempts   int
	// notify reports delivery state changes (never record content).
	notify func(string)

	dropped  atomic.Uint64
	failures atomic.Uint64
	sent     atomic.Uint64
	closed   atomic.Bool
	closing  sync.Once
	failing  bool
}

type forwardCoreOptions struct {
	name          string
	kind          ForwarderKind
	queue         int
	batchSize     int
	batchInterval time.Duration
	// maxRecord is the longest record the sender can carry (default 256 KiB).
	maxRecord int
}

func newForwardCore(o forwardCoreOptions, sender batchSender) *forwardCore {
	if o.queue < 1 {
		o.queue = DefaultForwardQueueEntries
	}
	if o.batchSize < 1 {
		o.batchSize = DefaultForwardBatchSize
	}
	if o.batchInterval <= 0 {
		o.batchInterval = DefaultForwardBatchInterval
	}
	if o.maxRecord < 1 {
		o.maxRecord = maxForwardRecordBytes
	}
	c := &forwardCore{
		name: o.name, kind: o.kind, sender: sender,
		queue: make(chan []byte, o.queue), flushReq: make(chan forwardFlush), stop: make(chan struct{}), done: make(chan struct{}),
		batchSize: o.batchSize, batchInterval: o.batchInterval, maxRecord: o.maxRecord, flushTimeout: DefaultForwardFlushTimeout,
		backoffMin: forwardBackoffMin, backoffMax: forwardBackoffMax, maxAttempts: forwardMaxAttempts,
		notify: func(msg string) { fmt.Fprintln(os.Stderr, msg) },
	}
	go c.run()
	return c
}

func (c *forwardCore) Kind() ForwarderKind { return c.kind }

// Name is the destination label of the metrics.
func (c *forwardCore) Name() string { return c.name }

func (c *forwardCore) ForwardStat() ForwardStat {
	return ForwardStat{Destination: c.name, Kind: c.kind, Dropped: c.dropped.Load(), Failures: c.failures.Load(), Sent: c.sent.Load()}
}

// Write queues one complete record. It never blocks and never fails: a full
// queue, an oversized record or a closed forwarder is counted as dropped.
func (c *forwardCore) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && (p[len(p)-1] == '\n' || p[len(p)-1] == '\r') {
		p = p[:len(p)-1]
	}
	if len(p) == 0 {
		return n, nil
	}
	if c.closed.Load() || len(p) > c.maxRecord {
		c.dropped.Add(1)
		return n, nil
	}
	select {
	case c.queue <- append([]byte(nil), p...):
	default:
		c.dropped.Add(1)
	}
	return n, nil
}

// Flush pushes every record queued before the call. Without a caller
// deadline it waits at most the flush timeout; on expiry the records still
// pending are dropped and counted.
func (c *forwardCore) Flush(ctx context.Context) error {
	if c.closed.Load() {
		return ErrClosed
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.flushTimeout)
		defer cancel()
	}
	req := forwardFlush{ctx: ctx, done: make(chan error, 1)}
	select {
	case c.flushReq <- req:
	case <-ctx.Done():
		return ErrForwardDeadline
	case <-c.done:
		return ErrClosed
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		// The worker keeps honouring the same context and counts the rest.
		return ErrForwardDeadline
	}
}

// Close stops accepting records, makes a bounded last attempt to deliver
// what is queued and releases the connection.
func (c *forwardCore) Close() error {
	c.closing.Do(func() {
		c.closed.Store(true)
		close(c.stop)
	})
	<-c.done
	return nil
}

func (c *forwardCore) run() {
	defer close(c.done)
	defer c.sender.close()
	var batch [][]byte
	var batchBytes int
	timer := time.NewTimer(c.batchInterval)
	defer timer.Stop()
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(c.batchInterval)
	}
	add := func(rec []byte) {
		batch = append(batch, rec)
		batchBytes += len(rec)
	}
	full := func() bool { return len(batch) >= c.batchSize || batchBytes >= maxForwardBatchBytes }
	// send delivers the pending batch; interrupt makes a pending retry wait
	// give way to shutdown. It reports whether the batch was left undelivered.
	send := func(ctx context.Context, interrupt bool) bool {
		if len(batch) == 0 {
			return false
		}
		if !c.deliver(ctx, batch, interrupt) {
			return true
		}
		batch, batchBytes = nil, 0
		return false
	}
	for {
		select {
		case rec := <-c.queue:
			add(rec)
			if full() {
				if send(context.Background(), true) {
					c.finish(&batch)
					return
				}
				resetTimer()
			}
		case <-timer.C:
			if send(context.Background(), true) {
				c.finish(&batch)
				return
			}
			timer.Reset(c.batchInterval)
		case req := <-c.flushReq:
			req.done <- c.drain(req.ctx, &batch, &batchBytes, add, full)
			resetTimer()
		case <-c.stop:
			c.finish(&batch)
			return
		}
	}
}

// finish makes the last, deadline bound attempt at shutdown and counts what
// is still undelivered.
func (c *forwardCore) finish(batch *[][]byte) {
	ctx, cancel := context.WithTimeout(context.Background(), c.flushTimeout)
	defer cancel()
	pending := *batch
	for {
		select {
		case rec := <-c.queue:
			pending = append(pending, rec)
			continue
		default:
		}
		break
	}
	c.deliverAll(ctx, pending)
	*batch = nil
}

// drain sends the pending batch and everything queued right now.
func (c *forwardCore) drain(ctx context.Context, batch *[][]byte, batchBytes *int, add func([]byte), full func() bool) error {
	for {
		select {
		case rec := <-c.queue:
			add(rec)
			if full() {
				if !c.deliver(ctx, *batch, false) {
					c.dropped.Add(uint64(len(*batch)))
					*batch, *batchBytes = nil, 0
					c.discardQueued()
					return ErrForwardDeadline
				}
				*batch, *batchBytes = nil, 0
			}
			continue
		default:
		}
		break
	}
	if len(*batch) == 0 {
		return nil
	}
	ok := c.deliver(ctx, *batch, false)
	if !ok {
		c.dropped.Add(uint64(len(*batch)))
	}
	*batch, *batchBytes = nil, 0
	if !ok {
		return ErrForwardDeadline
	}
	return nil
}

func (c *forwardCore) discardQueued() {
	for {
		select {
		case <-c.queue:
			c.dropped.Add(1)
		default:
			return
		}
	}
}

// deliverAll sends records in batches until the context ends.
func (c *forwardCore) deliverAll(ctx context.Context, records [][]byte) {
	for len(records) > 0 {
		n := min(len(records), c.batchSize)
		if !c.deliver(ctx, records[:n], false) {
			c.dropped.Add(uint64(len(records)))
			return
		}
		records = records[n:]
	}
}

// deliver pushes one batch with exponential backoff. It returns true when the
// collector took the batch or the batch was dropped for good (counted); it
// returns false only when ctx ended, or shutdown interrupted a retry wait
// while interrupt is set, so the caller still owns the batch.
func (c *forwardCore) deliver(ctx context.Context, batch [][]byte, interrupt bool) bool {
	backoff := c.backoffMin
	for attempt := 1; ; attempt++ {
		err := c.sender.send(ctx, batch)
		if err == nil {
			c.sent.Add(uint64(len(batch)))
			c.setFailing(false, "")
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		c.failures.Add(1)
		var de *deliverError
		class, retry, wait := "network", true, time.Duration(0)
		if errors.As(err, &de) {
			class, retry, wait = de.class, de.retry, de.wait
		}
		c.setFailing(true, class)
		if !retry || attempt >= c.maxAttempts {
			c.dropped.Add(uint64(len(batch)))
			return true
		}
		delay := backoff/2 + rand.N(backoff/2+1) //nolint:gosec // G404: jitter needs no cryptographic randomness
		backoff = min(backoff*2, c.backoffMax)
		if wait > delay {
			delay = min(wait, forwardMaxRetryWait)
		}
		t := time.NewTimer(delay)
		var stop <-chan struct{}
		if interrupt {
			stop = c.stop
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return false
		case <-stop:
			t.Stop()
			return false
		}
	}
}

// setFailing reports a state change once per transition so a dead collector
// does not flood stderr.
func (c *forwardCore) setFailing(failing bool, class string) {
	if failing == c.failing {
		return
	}
	c.failing = failing
	if c.notify == nil {
		return
	}
	if failing {
		c.notify("Jelee log forwarder " + c.name + ": delivery failing (" + class + "); retrying with backoff")
	} else {
		c.notify("Jelee log forwarder " + c.name + ": delivery recovered")
	}
}

var _ io.WriteCloser = (*forwardCore)(nil)
