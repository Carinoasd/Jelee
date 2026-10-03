package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// claimFake models the directory frontier: children become claimable only
// after their parent completes, and a claimed directory is never handed out
// twice. It records the peak number of directories in flight.
type claimFake struct {
	executionFake
	mu        sync.Mutex
	children  map[string][]string
	ready     []string
	claimed   map[string]bool
	completed []string
	inFlight  int
	peak      int
	claimErr  error
}

func (f *claimFake) ClaimScanDirectory(ctx context.Context, _ domain.JobLease) (domain.ScanDirectory, error) {
	if err := ctx.Err(); err != nil {
		return domain.ScanDirectory{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return domain.ScanDirectory{}, f.claimErr
	}
	if len(f.ready) == 0 {
		return domain.ScanDirectory{}, domain.ErrNotFound
	}
	next := f.ready[0]
	f.ready = f.ready[1:]
	if f.claimed[next] {
		return domain.ScanDirectory{}, errors.New("directory claimed twice")
	}
	f.claimed[next] = true
	f.inFlight++
	if f.inFlight > f.peak {
		f.peak = f.inFlight
	}
	return domain.ScanDirectory{RootID: "root", Path: next, ClaimToken: next}, nil
}

func newClaimFake(levels, fanout int) *claimFake {
	f := &claimFake{children: map[string][]string{}, ready: []string{"."}, claimed: map[string]bool{}}
	var build func(parent string, depth int)
	build = func(parent string, depth int) {
		if depth == levels {
			return
		}
		for i := 0; i < fanout; i++ {
			child := "d" + strconv.Itoa(i)
			if parent != "." {
				child = parent + "/" + child
			}
			f.children[parent] = append(f.children[parent], child)
			build(child, depth+1)
		}
	}
	build(".", 0)
	f.save = func(_ context.Context, _ domain.JobLease, d domain.ScanDirectory, b domain.ScanBatch) error {
		if d.ClaimToken != d.Path {
			return errors.New("batch from another slot")
		}
		if b.Done {
			f.mu.Lock()
			f.inFlight--
			f.completed = append(f.completed, d.Path)
			f.ready = append(f.ready, f.children[d.Path]...)
			f.mu.Unlock()
		}
		return nil
	}
	return f
}

func concurrentRunner(t *testing.T, f *claimFake, scanner scannerFunc, slots int) *Runner {
	t.Helper()
	r, err := New(f, scanner, Options{Workers: 1, ScanConcurrency: slots, PollInterval: 100 * time.Millisecond, LeaseDuration: 10 * time.Second, DBOperationTimeout: time.Second, MaxJobRuntime: time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestJobsConcurrentScanCoversFrontierWithinBound(t *testing.T) {
	for _, slots := range []int{2, 4, 16} {
		t.Run(strconv.Itoa(slots), func(t *testing.T) {
			f := newClaimFake(3, 4) // 1+4+16+64 directories
			var active, peak atomic.Int32
			scanner := scannerFunc(func(ctx context.Context, d domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
				n := active.Add(1)
				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}
				defer active.Add(-1)
				time.Sleep(time.Millisecond)
				var dirs []string
				dirs = append(dirs, f.children[d.Path]...)
				return emit(domain.ScanBatch{Directories: dirs, Done: true})
			})
			r := concurrentRunner(t, f, scanner, slots)
			err, repository := r.executeInventory(context.Background(), domain.JobLease{})
			if err != nil || repository {
				t.Fatal(err, repository)
			}
			if len(f.completed) != 85 || len(f.claimed) != 85 {
				t.Fatalf("completed=%d claimed=%d", len(f.completed), len(f.claimed))
			}
			if int(peak.Load()) > slots || f.peak > slots {
				t.Fatalf("peak %d/%d exceeds %d slots", peak.Load(), f.peak, slots)
			}
			if slots > 1 && peak.Load() < 2 {
				t.Fatal("no directories were scanned concurrently")
			}
		})
	}
}

func TestJobsConcurrentScanFirstFailureStopsSiblings(t *testing.T) {
	f := newClaimFake(2, 6)
	failure := errors.New("disk vanished")
	var cancelled atomic.Int32
	scanner := scannerFunc(func(ctx context.Context, d domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
		switch {
		case d.Path == ".":
			return emit(domain.ScanBatch{Directories: f.children["."], Done: true})
		case d.Path == "d3":
			return failure
		}
		<-ctx.Done() // siblings block until the failure cancels them
		cancelled.Add(1)
		return ctx.Err()
	})
	r := concurrentRunner(t, f, scanner, 4)
	done := make(chan struct{})
	var err error
	var repository bool
	go func() {
		err, repository = r.executeInventory(context.Background(), domain.JobLease{})
		close(done)
	}()
	receive(t, done)
	if !errors.Is(err, failure) || repository {
		t.Fatalf("first failure was not reported: %v %t", err, repository)
	}
	if cancelled.Load() == 0 {
		t.Fatal("siblings were not cancelled")
	}
}

func TestJobsConcurrentScanCancellationAndClaimFailure(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		f := newClaimFake(2, 6)
		ctx, cancel := context.WithCancel(context.Background())
		var started atomic.Int32
		scanner := scannerFunc(func(c context.Context, d domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
			if d.Path == "." {
				return emit(domain.ScanBatch{Directories: f.children["."], Done: true})
			}
			if started.Add(1) == 3 {
				cancel()
			}
			<-c.Done()
			return c.Err()
		})
		r := concurrentRunner(t, f, scanner, 3)
		err, repository := r.executeInventory(ctx, domain.JobLease{})
		if !errors.Is(err, context.Canceled) || repository {
			t.Fatal(err, repository)
		}
	})
	t.Run("claim", func(t *testing.T) {
		f := newClaimFake(1, 2)
		f.claimErr = domain.ErrJobLeaseLost
		r := concurrentRunner(t, f, scannerFunc(func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error { return nil }), 2)
		err, repository := r.executeInventory(context.Background(), domain.JobLease{})
		if !errors.Is(err, domain.ErrJobLeaseLost) || !repository {
			t.Fatal(err, repository)
		}
	})
	t.Run("incomplete", func(t *testing.T) {
		f := newClaimFake(1, 2)
		r := concurrentRunner(t, f, scannerFunc(func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error { return nil }), 2)
		err, _ := r.executeInventory(context.Background(), domain.JobLease{})
		if !errors.Is(err, domain.ErrScanIO) {
			t.Fatal("directory without a completed checkpoint was accepted", err)
		}
	})
	t.Run("panic", func(t *testing.T) {
		f := newClaimFake(1, 2)
		r := concurrentRunner(t, f, scannerFunc(func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error { panic("scanner bug") }), 2)
		err, _ := r.executeInventory(context.Background(), domain.JobLease{})
		if !errors.Is(err, domain.ErrScanIO) {
			t.Fatal("slot panic escaped", err)
		}
	})
}

func TestJobsScanConcurrencyOptionBounds(t *testing.T) {
	f := newClaimFake(0, 0)
	for _, slots := range []int{-1, MaxScanConcurrency + 1} {
		if _, err := New(f, scannerFunc(nil), Options{Workers: 1, ScanConcurrency: slots, PollInterval: 100 * time.Millisecond, LeaseDuration: 10 * time.Second, DBOperationTimeout: time.Second, MaxJobRuntime: time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil))); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("unbounded scan concurrency accepted", slots)
		}
	}
	if DefaultOptions().ScanConcurrency != DefaultScanConcurrency || DefaultScanConcurrency < 1 || DefaultScanConcurrency > 4 {
		t.Fatal("default scan concurrency is not conservative")
	}
}
