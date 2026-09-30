package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type executionFake struct {
	app.JobExecutionRepository
	claim     func(context.Context, string, bool, time.Duration) (domain.JobLease, error)
	heartbeat func(context.Context, domain.JobLease, time.Duration) (bool, error)
	next      func(context.Context, domain.JobLease) (domain.ScanDirectory, error)
	save      func(context.Context, domain.JobLease, domain.ScanDirectory, domain.ScanBatch) error
	finish    func(context.Context, domain.JobLease, string, string) error
	release   func(context.Context, domain.JobLease) error
}

func (f *executionFake) ClaimJob(c context.Context, o string, b bool, d time.Duration) (domain.JobLease, error) {
	return f.claim(c, o, b, d)
}
func (f *executionFake) HeartbeatJob(c context.Context, l domain.JobLease, d time.Duration) (bool, error) {
	return f.heartbeat(c, l, d)
}
func (f *executionFake) NextScanDirectory(c context.Context, l domain.JobLease) (domain.ScanDirectory, error) {
	return f.next(c, l)
}
func (f *executionFake) SaveScanBatch(c context.Context, l domain.JobLease, d domain.ScanDirectory, b domain.ScanBatch) error {
	return f.save(c, l, d, b)
}
func (f *executionFake) FinishJob(c context.Context, l domain.JobLease, s, e string) error {
	return f.finish(c, l, s, e)
}
func (f *executionFake) ReleaseJob(c context.Context, l domain.JobLease) error {
	return f.release(c, l)
}

type scannerFunc func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error

func (f scannerFunc) ScanDirectory(c context.Context, d domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
	return f(c, d, emit)
}

// Timers fire only when requested. Tests exercise hour-long job limits and
// heartbeat/stop interleavings without wall-clock sleeps or goroutine counting.
type testClock struct {
	mu      sync.Mutex
	timers  map[*testTimer]bool
	changed chan struct{}
}
type testTimer struct {
	owner    *testClock
	duration time.Duration
	ch       chan time.Time
}

func newTestClock() *testClock {
	return &testClock{timers: make(map[*testTimer]bool), changed: make(chan struct{}, 100)}
}
func (c *testClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &testTimer{owner: c, duration: d, ch: make(chan time.Time, 1)}
	c.timers[t] = true
	c.notify()
	return t
}
func (c *testClock) notify() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}
func (t *testTimer) C() <-chan time.Time { return t.ch }
func (t *testTimer) Stop() bool {
	c := t.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	active := c.timers[t]
	delete(c.timers, t)
	c.notify()
	return active
}
func (c *testClock) count(d time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for t := range c.timers {
		if d == 0 || t.duration == d {
			n++
		}
	}
	return n
}
func (c *testClock) fire(d time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for t := range c.timers {
		if t.duration == d {
			delete(c.timers, t)
			t.ch <- time.Time{}
			n++
		}
	}
	c.notify()
	return n
}
func (c *testClock) waitFor(t *testing.T, d time.Duration, n int) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for c.count(d) != n {
		select {
		case <-c.changed:
		case <-deadline.C:
			t.Fatalf("timer count=%d want=%d for %v", c.count(d), n, d)
		}
	}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not complete bounded operation")
		var zero T
		return zero
	}
}
func checkDBContext(t *testing.T, ctx context.Context) {
	t.Helper()
	if ctx.Err() != nil {
		t.Errorf("repository got cancelled cleanup context: %v", ctx.Err())
	}
	checkDBDeadline(t, ctx)
}
func checkDBDeadline(t *testing.T, ctx context.Context) {
	t.Helper()
	if _, ok := ctx.Deadline(); !ok {
		t.Error("repository operation has no deadline")
	}
}
func stopRunner(t *testing.T, r *Runner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		t.Errorf("stop: %v", err)
	}
}
func makeRunner(t *testing.T, repo app.JobExecutionRepository, scan app.InventoryScanner, clock *testClock, opts Options, output io.Writer) *Runner {
	t.Helper()
	opts.Clock = clock
	r, err := New(repo, scan, opts, slog.New(slog.NewJSONHandler(output, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopRunner(t, r)
		if n := clock.count(0); n != 0 {
			t.Errorf("%d timers retained after Stop", n)
		}
	})
	return r
}
func startRunner(t *testing.T, r *Runner) {
	t.Helper()
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func doneScanner(_ context.Context, _ domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
	return emit(domain.ScanBatch{Done: true})
}

type terminal struct{ state, code string }
type jobFixture struct {
	repo     *executionFake
	terminal chan terminal
	released chan domain.JobLease
}

func oneJob(t *testing.T) jobFixture {
	t.Helper()
	var claimed, directory atomic.Bool
	f := jobFixture{repo: &executionFake{}, terminal: make(chan terminal, 2), released: make(chan domain.JobLease, 2)}
	f.repo.claim = func(c context.Context, owner string, _ bool, _ time.Duration) (domain.JobLease, error) {
		checkDBDeadline(t, c)
		if claimed.Swap(true) {
			return domain.JobLease{}, domain.ErrNotFound
		}
		return domain.JobLease{Job: domain.Job{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}, Owner: owner, Generation: 7}, nil
	}
	f.repo.heartbeat = func(c context.Context, _ domain.JobLease, _ time.Duration) (bool, error) {
		checkDBDeadline(t, c)
		return false, nil
	}
	f.repo.next = func(c context.Context, _ domain.JobLease) (domain.ScanDirectory, error) {
		checkDBDeadline(t, c)
		if directory.Swap(true) {
			return domain.ScanDirectory{}, domain.ErrNotFound
		}
		return domain.ScanDirectory{RootID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", RootPath: "private-root", Path: "."}, nil
	}
	f.repo.save = func(c context.Context, _ domain.JobLease, _ domain.ScanDirectory, _ domain.ScanBatch) error {
		checkDBDeadline(t, c)
		return nil
	}
	f.repo.finish = func(c context.Context, _ domain.JobLease, state, code string) error {
		checkDBContext(t, c)
		f.terminal <- terminal{state, code}
		return nil
	}
	f.repo.release = func(c context.Context, l domain.JobLease) error { checkDBContext(t, c); f.released <- l; return nil }
	return f
}

func TestRunnerOptionsAndLifecycle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := &executionFake{}
	scan := scannerFunc(doneScanner)
	for name, mutate := range map[string]func(*Options){
		"no workers": func(o *Options) { o.Workers = 0 }, "too many workers": func(o *Options) { o.Workers = 9 },
		"fast poll": func(o *Options) { o.PollInterval = 99 * time.Millisecond }, "slow poll": func(o *Options) { o.PollInterval = time.Minute + 1 },
		"short lease": func(o *Options) { o.LeaseDuration = 10*time.Second - 1 }, "long lease": func(o *Options) { o.LeaseDuration = 5*time.Minute + 1 },
		"zero DB bound": func(o *Options) { o.DBOperationTimeout = 0 }, "DB exceeds heartbeat": func(o *Options) { o.DBOperationTimeout = o.LeaseDuration / 3 },
		"short job": func(o *Options) { o.MaxJobRuntime = time.Minute - 1 }, "long job": func(o *Options) { o.MaxJobRuntime = 24*time.Hour + 1 },
		"invalid owner": func(o *Options) { o.Owner = "owner" },
	} {
		t.Run(name, func(t *testing.T) {
			opts := DefaultOptions()
			mutate(&opts)
			if _, err := New(repo, scan, opts, logger); err != domain.ErrInvalid {
				t.Fatalf("invalid option accepted: %v", err)
			}
		})
	}
	if _, err := New(nil, scan, DefaultOptions(), logger); err != domain.ErrInvalid {
		t.Fatal("nil repository")
	}
	if _, err := New(repo, nil, DefaultOptions(), logger); err != domain.ErrInvalid {
		t.Fatal("nil scanner")
	}
	if _, err := New(repo, scan, DefaultOptions(), nil); err != domain.ErrInvalid {
		t.Fatal("nil logger")
	}
	f := oneJob(t)
	clock := newTestClock()
	r := makeRunner(t, f.repo, scan, clock, DefaultOptions(), io.Discard)
	if !domain.ValidID(r.options.Owner) {
		t.Fatal("generated owner is not UUID")
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled Start accepted")
	}
	startRunner(t, r)
	if err := r.Start(context.Background()); err == nil {
		t.Fatal("duplicate Start accepted")
	}
	stopRunner(t, r)
	stopRunner(t, r)
}

func TestRunnerFixedConcurrencyAndCheckpointRelease(t *testing.T) {
	clock := newTestClock()
	opts := DefaultOptions()
	var claims, active, peak atomic.Int32
	entered := make(chan struct{}, 10)
	saved := make(chan domain.JobLease, 10)
	released := make(chan domain.JobLease, 10)
	repo := &executionFake{
		claim: func(c context.Context, o string, _ bool, _ time.Duration) (domain.JobLease, error) {
			checkDBContext(t, c)
			n := claims.Add(1)
			return domain.JobLease{Job: domain.Job{ID: fmt.Sprintf("%08d-bbbb-4bbb-8bbb-bbbbbbbbbbbb", n)}, Owner: o, Generation: int64(n)}, nil
		},
		heartbeat: func(context.Context, domain.JobLease, time.Duration) (bool, error) { return false, nil },
		next: func(context.Context, domain.JobLease) (domain.ScanDirectory, error) {
			return domain.ScanDirectory{Path: "checkpoint-directory"}, nil
		},
		save: func(c context.Context, l domain.JobLease, _ domain.ScanDirectory, b domain.ScanBatch) error {
			checkDBContext(t, c)
			if len(b.Entries) != 1 || b.Done {
				t.Error("checkpoint changed")
			}
			saved <- l
			return nil
		},
		release: func(c context.Context, l domain.JobLease) error { checkDBContext(t, c); released <- l; return nil },
		finish: func(context.Context, domain.JobLease, string, string) error {
			t.Error("graceful stop must release, not finish")
			return nil
		},
	}
	scan := scannerFunc(func(c context.Context, _ domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		if err := emit(domain.ScanBatch{Entries: []domain.InventoryEntry{{Path: "part.mkv"}}}); err != nil {
			return err
		}
		entered <- struct{}{}
		<-c.Done()
		return c.Err()
	})
	r := makeRunner(t, repo, scan, clock, opts, io.Discard)
	startRunner(t, r)
	receive(t, entered)
	receive(t, entered)
	first, second := receive(t, saved), receive(t, saved)
	if claims.Load() != 2 || active.Load() != 2 || peak.Load() != 2 {
		t.Fatalf("unbounded worker counts claims=%d active=%d peak=%d", claims.Load(), active.Load(), peak.Load())
	}
	stopRunner(t, r)
	a, b := receive(t, released), receive(t, released)
	byID := map[string]int64{a.Job.ID: a.Generation, b.Job.ID: b.Generation}
	if byID[first.Job.ID] != first.Generation || byID[second.Job.ID] != second.Generation || a.Owner != b.Owner || !domain.ValidID(a.Owner) {
		t.Fatal("release did not preserve exact checkpoint lease")
	}
	if active.Load() != 0 || claims.Load() != 2 || clock.count(0) != 0 {
		t.Fatal("Stop returned with active work, new claims, or timers")
	}
}

func TestRunnerIdlePollUsesTimerAndStopCancelsIt(t *testing.T) {
	clock := newTestClock()
	opts := DefaultOptions()
	opts.Workers = 1
	calls := make(chan struct{}, 10)
	repo := &executionFake{claim: func(context.Context, string, bool, time.Duration) (domain.JobLease, error) {
		calls <- struct{}{}
		return domain.JobLease{}, domain.ErrNotFound
	}}
	r := makeRunner(t, repo, scannerFunc(doneScanner), clock, opts, io.Discard)
	startRunner(t, r)
	receive(t, calls)
	clock.waitFor(t, opts.PollInterval, 1)
	select {
	case <-calls:
		t.Fatal("idle worker polled without timer")
	default:
	}
	clock.fire(opts.PollInterval)
	receive(t, calls)
	clock.waitFor(t, opts.PollInterval, 1)
	stopRunner(t, r)
	if clock.count(0) != 0 {
		t.Fatal("idle timer leaked")
	}
}

func TestRunnerFairnessThreeManualThenBackground(t *testing.T) {
	clock := newTestClock()
	opts := DefaultOptions()
	opts.Workers = 1
	var calls atomic.Int32
	preferences := make(chan bool, 9)
	finished := make(chan struct{}, 8)
	repo := &executionFake{
		claim: func(_ context.Context, o string, b bool, _ time.Duration) (domain.JobLease, error) {
			n := calls.Add(1)
			preferences <- b
			if n > 8 {
				return domain.JobLease{}, domain.ErrNotFound
			}
			return domain.JobLease{Owner: o, Generation: int64(n)}, nil
		},
		next: func(context.Context, domain.JobLease) (domain.ScanDirectory, error) {
			return domain.ScanDirectory{}, domain.ErrNotFound
		},
		finish: func(_ context.Context, _ domain.JobLease, s, e string) error {
			if s != domain.JobSucceeded || e != "" {
				t.Error("empty complete scan should succeed")
			}
			finished <- struct{}{}
			return nil
		},
	}
	r := makeRunner(t, repo, scannerFunc(doneScanner), clock, opts, io.Discard)
	startRunner(t, r)
	for i := 0; i < 8; i++ {
		receive(t, finished)
		if got := receive(t, preferences); got != (i%4 == 3) {
			t.Fatalf("claim %d background=%v", i, got)
		}
	}
	clock.waitFor(t, opts.PollInterval, 1)
	stopRunner(t, r)
}

func TestRunnerHeartbeatCancellationAndLeaseFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested bool
		failure   error
		terminal  bool
	}{
		{"persisted cancel", true, nil, true}, {"lease lost", false, domain.ErrJobLeaseLost, false}, {"database unavailable", false, errors.New("private database credentials"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := oneJob(t)
			opts := DefaultOptions()
			opts.Workers = 1
			clock := newTestClock()
			entered, exited := make(chan struct{}, 1), make(chan struct{}, 1)
			var output bytes.Buffer
			f.repo.heartbeat = func(c context.Context, l domain.JobLease, d time.Duration) (bool, error) {
				checkDBContext(t, c)
				if l.Generation != 7 || d != opts.LeaseDuration {
					t.Error("heartbeat fence changed")
				}
				return tc.requested, tc.failure
			}
			scan := scannerFunc(func(c context.Context, _ domain.ScanDirectory, _ func(domain.ScanBatch) error) error {
				entered <- struct{}{}
				<-c.Done()
				exited <- struct{}{}
				return c.Err()
			})
			r := makeRunner(t, f.repo, scan, clock, opts, &output)
			startRunner(t, r)
			receive(t, entered)
			if clock.fire(opts.LeaseDuration/3) != 1 {
				t.Fatal("heartbeat timer missing")
			}
			receive(t, exited)
			// Entering idle poll proves run joined its monitor and chose final state.
			clock.waitFor(t, opts.PollInterval, 1)
			stopRunner(t, r)
			if tc.terminal {
				result := receive(t, f.terminal)
				if result != (terminal{domain.JobCancelled, ""}) {
					t.Fatalf("terminal=%+v", result)
				}
			} else {
				select {
				case <-f.terminal:
					t.Fatal("unsafe lease was finalized")
				default:
				}
				if strings.Contains(output.String(), "inventory job completed") {
					t.Fatal("lost lease logged completion")
				}
			}
			select {
			case <-f.released:
				t.Fatal("failed heartbeat must await fenced recovery")
			default:
			}
			if strings.Contains(output.String(), "private") {
				t.Fatal("sensitive underlying error logged")
			}
		})
	}
}

func TestRunnerJobTimeoutAndHeartbeatDBTimeout(t *testing.T) {
	for _, runtimeTimeout := range []bool{true, false} {
		t.Run(fmt.Sprintf("job-timeout=%v", runtimeTimeout), func(t *testing.T) {
			f := oneJob(t)
			clock := newTestClock()
			opts := DefaultOptions()
			opts.Workers = 1
			opts.DBOperationTimeout = 5 * time.Millisecond
			entered, exited := make(chan struct{}, 1), make(chan struct{}, 1)
			f.repo.heartbeat = func(c context.Context, _ domain.JobLease, _ time.Duration) (bool, error) {
				<-c.Done()
				return false, c.Err()
			}
			scan := scannerFunc(func(c context.Context, _ domain.ScanDirectory, _ func(domain.ScanBatch) error) error {
				entered <- struct{}{}
				<-c.Done()
				exited <- struct{}{}
				return c.Err()
			})
			r := makeRunner(t, f.repo, scan, clock, opts, io.Discard)
			startRunner(t, r)
			receive(t, entered)
			if runtimeTimeout {
				clock.fire(opts.MaxJobRuntime)
			} else {
				clock.fire(opts.LeaseDuration / 3)
			}
			receive(t, exited)
			clock.waitFor(t, opts.PollInterval, 1)
			stopRunner(t, r)
			if runtimeTimeout {
				if got := receive(t, f.terminal); got != (terminal{domain.JobFailed, "job_timeout"}) {
					t.Fatalf("terminal=%+v", got)
				}
			} else {
				select {
				case <-f.terminal:
					t.Fatal("DB timeout finalized unsafe lease")
				default:
				}
			}
		})
	}
}

func TestRunnerFailureMappingAndCallbackContract(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		scan                 scannerFunc
		nextError, saveError error
		state, code          string
		wantSave             int
	}{
		{name: "success", scan: doneScanner, state: domain.JobSucceeded, wantSave: 1},
		{name: "unavailable root", scan: func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error {
			return domain.ErrScanUnavailable
		}, state: domain.JobFailed, code: "scan_unavailable"},
		{name: "IO", scan: func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error {
			return domain.ErrScanIO
		}, state: domain.JobFailed, code: "scan_io"},
		{name: "limit", scan: func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error {
			return domain.ErrScanLimit
		}, state: domain.JobFailed, code: "scan_limit"},
		{name: "unknown scanner error", scan: func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error {
			return errors.New("private/media/title.mkv")
		}, state: domain.JobFailed, code: "scan_io"},
		{name: "scanner panic", scan: func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error {
			panic("private/media/title.mkv")
		}, state: domain.JobFailed, code: "scan_io"},
		{name: "missing completion", scan: func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error { return nil }, state: domain.JobFailed, code: "scan_io"},
		{name: "oversize combined batch", scan: func(_ context.Context, _ domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
			return emit(domain.ScanBatch{Entries: make([]domain.InventoryEntry, 128), Directories: []string{"child"}, Done: true})
		}, state: domain.JobFailed, code: "scan_limit"},
		{name: "negative skipped", scan: func(_ context.Context, _ domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
			return emit(domain.ScanBatch{Skipped: -1, Done: true})
		}, state: domain.JobFailed, code: "scan_limit"},
		{name: "emit after done", scan: func(_ context.Context, _ domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
			if err := emit(domain.ScanBatch{Done: true}); err != nil {
				return err
			}
			return emit(domain.ScanBatch{Done: true})
		}, state: domain.JobFailed, code: "scan_io", wantSave: 1},
		{name: "checkpoint database failure", scan: doneScanner, saveError: errors.New("private database credentials"), state: domain.JobFailed, code: "scan_unavailable", wantSave: 1},
		{name: "swallowed callback failure", scan: func(_ context.Context, _ domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
			_ = emit(domain.ScanBatch{Done: true})
			_ = emit(domain.ScanBatch{Done: true})
			return nil
		}, saveError: domain.ErrScanLimit, state: domain.JobFailed, code: "scan_limit", wantSave: 1},
		{name: "next database failure", nextError: context.DeadlineExceeded, state: domain.JobFailed, code: "scan_unavailable"},
		{name: "next persisted cancel", nextError: context.Canceled, state: domain.JobCancelled},
		{name: "save persisted cancel", scan: doneScanner, saveError: context.Canceled, state: domain.JobCancelled, wantSave: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := oneJob(t)
			clock := newTestClock()
			opts := DefaultOptions()
			opts.Workers = 1
			var saved atomic.Int32
			var output bytes.Buffer
			if tc.nextError != nil {
				f.repo.next = func(context.Context, domain.JobLease) (domain.ScanDirectory, error) {
					return domain.ScanDirectory{}, tc.nextError
				}
			}
			f.repo.save = func(context.Context, domain.JobLease, domain.ScanDirectory, domain.ScanBatch) error {
				saved.Add(1)
				return tc.saveError
			}
			scan := tc.scan
			if scan == nil {
				scan = func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error {
					t.Error("scanner ran after next error")
					return nil
				}
			}
			r := makeRunner(t, f.repo, scan, clock, opts, &output)
			startRunner(t, r)
			got := receive(t, f.terminal)
			clock.waitFor(t, opts.PollInterval, 1)
			stopRunner(t, r)
			if got != (terminal{tc.state, tc.code}) {
				t.Fatalf("terminal=%+v want=%s/%s", got, tc.state, tc.code)
			}
			if int(saved.Load()) != tc.wantSave {
				t.Fatalf("saved=%d want=%d", saved.Load(), tc.wantSave)
			}
			if strings.Contains(output.String(), "private") {
				t.Fatal("sensitive scanner error or path logged")
			}
		})
	}
}

func TestRunnerLeaseLostInSaveNeverFinishesOrReleases(t *testing.T) {
	f := oneJob(t)
	clock := newTestClock()
	opts := DefaultOptions()
	opts.Workers = 1
	f.repo.save = func(context.Context, domain.JobLease, domain.ScanDirectory, domain.ScanBatch) error {
		return domain.ErrJobLeaseLost
	}
	r := makeRunner(t, f.repo, scannerFunc(doneScanner), clock, opts, io.Discard)
	startRunner(t, r)
	clock.waitFor(t, opts.PollInterval, 1)
	stopRunner(t, r)
	select {
	case <-f.terminal:
		t.Fatal("stale lease finalized")
	default:
	}
	select {
	case <-f.released:
		t.Fatal("stale lease released")
	default:
	}
}

func TestRunnerFinishFailureDoesNotClaimSuccessOrRetryOldLease(t *testing.T) {
	f := oneJob(t)
	clock := newTestClock()
	opts := DefaultOptions()
	opts.Workers = 1
	var attempts atomic.Int32
	var output bytes.Buffer
	f.repo.finish = func(c context.Context, _ domain.JobLease, _ string, _ string) error {
		checkDBContext(t, c)
		attempts.Add(1)
		return errors.New("private database credentials")
	}
	r := makeRunner(t, f.repo, scannerFunc(doneScanner), clock, opts, &output)
	startRunner(t, r)
	clock.waitFor(t, opts.PollInterval, 1)
	stopRunner(t, r)
	if attempts.Load() != 1 || strings.Contains(output.String(), "inventory job completed") || strings.Contains(output.String(), "private") {
		t.Fatal("unsafe completion retry or sensitive/success log")
	}
}

func TestRunnerStopDeadlineDoesNotPretendBlockedScannerJoined(t *testing.T) {
	f := oneJob(t)
	clock := newTestClock()
	opts := DefaultOptions()
	opts.Workers = 1
	entered, observedCancel := make(chan struct{}, 1), make(chan struct{}, 1)
	unblock := make(chan struct{})
	var unblockOnce sync.Once
	defer unblockOnce.Do(func() { close(unblock) })
	scan := scannerFunc(func(c context.Context, _ domain.ScanDirectory, _ func(domain.ScanBatch) error) error {
		entered <- struct{}{}
		<-c.Done()
		observedCancel <- struct{}{}
		<-unblock
		return c.Err()
	})
	r := makeRunner(t, f.repo, scan, clock, opts, io.Discard)
	startRunner(t, r)
	receive(t, entered)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked stop=%v", err)
	}
	receive(t, observedCancel)
	select {
	case <-f.released:
		t.Fatal("released before scanner stopped")
	default:
	}
	unblockOnce.Do(func() { close(unblock) })
	stopRunner(t, r)
	receive(t, f.released)
}

func TestRunnerCompletionCancellationRaceChecksFlagAndLease(t *testing.T) {
	for _, tc := range []struct {
		name         string
		requested    bool
		failure      error
		wantAttempts int
	}{
		{"confirmed cancel", true, nil, 2},
		{"different conflict", false, nil, 1},
		{"lease changed", true, domain.ErrJobLeaseLost, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := oneJob(t)
			clock := newTestClock()
			opts := DefaultOptions()
			opts.Workers = 1
			var attempts, checked atomic.Int32
			var output bytes.Buffer
			f.repo.finish = func(c context.Context, _ domain.JobLease, state, code string) error {
				checkDBContext(t, c)
				if attempts.Add(1) == 1 {
					if state != domain.JobSucceeded || code != "" {
						t.Error("first finish must be success")
					}
					return domain.ErrConflict
				}
				if state != domain.JobCancelled || code != "" {
					t.Error("confirmed cancellation was not persisted")
				}
				return nil
			}
			f.repo.heartbeat = func(c context.Context, _ domain.JobLease, _ time.Duration) (bool, error) {
				checkDBContext(t, c)
				checked.Add(1)
				if clock.count(opts.LeaseDuration/3) != 0 {
					t.Error("monitor was not joined before reconciliation")
				}
				return tc.requested, tc.failure
			}
			r := makeRunner(t, f.repo, scannerFunc(doneScanner), clock, opts, &output)
			startRunner(t, r)
			clock.waitFor(t, opts.PollInterval, 1)
			stopRunner(t, r)
			if attempts.Load() != int32(tc.wantAttempts) || checked.Load() != 1 {
				t.Fatal("unbounded or missing cancellation reconciliation")
			}
			if strings.Contains(output.String(), `"state":"succeeded"`) {
				t.Fatal("conflicting completion logged success")
			}
		})
	}
}

func TestRunnerHeartbeatRenewsWithoutResettingJobRuntime(t *testing.T) {
	f := oneJob(t)
	clock := newTestClock()
	opts := DefaultOptions()
	opts.Workers = 1
	entered, renewed := make(chan struct{}, 1), make(chan domain.JobLease, 2)
	f.repo.heartbeat = func(c context.Context, l domain.JobLease, _ time.Duration) (bool, error) {
		checkDBContext(t, c)
		renewed <- l
		return false, nil
	}
	scan := scannerFunc(func(c context.Context, _ domain.ScanDirectory, _ func(domain.ScanBatch) error) error {
		entered <- struct{}{}
		<-c.Done()
		return c.Err()
	})
	r := makeRunner(t, f.repo, scan, clock, opts, io.Discard)
	startRunner(t, r)
	receive(t, entered)
	for i := 0; i < 2; i++ {
		if clock.fire(opts.LeaseDuration/3) != 1 {
			t.Fatal("missing unique heartbeat timer")
		}
		lease := receive(t, renewed)
		if lease.Generation != 7 || lease.Owner != r.options.Owner {
			t.Fatal("renewal changed fence")
		}
		clock.waitFor(t, opts.LeaseDuration/3, 1)
		if clock.count(opts.MaxJobRuntime) != 1 {
			t.Fatal("renewal leaked or removed job timer")
		}
	}
	clock.fire(opts.MaxJobRuntime)
	if got := receive(t, f.terminal); got != (terminal{domain.JobFailed, "job_timeout"}) {
		t.Fatalf("terminal=%+v", got)
	}
	clock.waitFor(t, opts.PollInterval, 1)
	stopRunner(t, r)
}

func TestRunnerStreamsCompleteDirectoriesBeforeAdvancingFrontier(t *testing.T) {
	f := oneJob(t)
	clock := newTestClock()
	opts := DefaultOptions()
	opts.Workers = 1
	next, saved := 0, 0
	completed := true
	paths := []string{".", "child"}
	f.repo.next = func(c context.Context, l domain.JobLease) (domain.ScanDirectory, error) {
		checkDBDeadline(t, c)
		if !completed {
			t.Error("advanced frontier before completed checkpoint")
		}
		if next == len(paths) {
			return domain.ScanDirectory{}, domain.ErrNotFound
		}
		d := domain.ScanDirectory{Path: paths[next]}
		next++
		completed = false
		return d, nil
	}
	f.repo.save = func(c context.Context, l domain.JobLease, d domain.ScanDirectory, b domain.ScanBatch) error {
		checkDBDeadline(t, c)
		if l.Generation != 7 || !domain.ValidID(l.Owner) || d.Path != paths[next-1] {
			t.Error("checkpoint lost its lease or directory identity")
		}
		if saved == 0 && (len(b.Entries) != 128 || b.Done) {
			t.Error("maximum batch did not stream before completion")
		}
		saved++
		completed = b.Done
		return nil
	}
	scan := scannerFunc(func(c context.Context, d domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
		if d.Path == "." {
			if err := emit(domain.ScanBatch{Entries: make([]domain.InventoryEntry, 128)}); err != nil {
				return err
			}
		}
		return emit(domain.ScanBatch{Done: true})
	})
	r := makeRunner(t, f.repo, scan, clock, opts, io.Discard)
	startRunner(t, r)
	if got := receive(t, f.terminal); got != (terminal{domain.JobSucceeded, ""}) {
		t.Fatalf("terminal=%+v", got)
	}
	clock.waitFor(t, opts.PollInterval, 1)
	stopRunner(t, r)
	if next != 2 || saved != 3 || !completed {
		t.Fatal("frontier or bounded checkpoint sequence incomplete")
	}
}
