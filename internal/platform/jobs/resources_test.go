package jobs

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func TestSharedBudgetNFOChangesClassWithoutNesting(t *testing.T) {
	f := newNFOWorkerFixture(t, 1)
	b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
	original := f.reader.read
	reads, parses := 0, 0
	f.reader.read = func(ctx context.Context, path domain.NFOSource) (app.NFOReadSource, error) {
		if s := b.Stats(); s != (resources.Stats{IO: 1, Total: 1}) {
			t.Errorf("read class: %+v", s)
		}
		reads++
		source, err := original(ctx, path)
		if err != nil {
			return source, err
		}
		return &nfoReadFake{stamp: source.Stamp(), parse: func(c context.Context) (domain.NFOValidationSummary, error) {
			if s := b.Stats(); s != (resources.Stats{CPU: 1, Total: 1}) {
				t.Errorf("parse class: %+v", s)
			}
			parses++
			return source.Parse(c)
		}}, nil
	}
	r := f.runner(t, func(o *Options) { o.Budget = b })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r.run(ctx, f.lease)
	if receive(t, f.base.terminal).state != domain.JobSucceeded || reads != 2 || parses != 1 {
		t.Fatal("NFO did not read/parse/revalidate successfully")
	}
	if b.Stats() != (resources.Stats{}) || len(r.nfoGate) != 0 {
		t.Fatal("NFO leaked shared or local permit")
	}
}

func TestSharedBudgetScanOverflowWaitCancelAndResume(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		f := oneJob(t)
		clock := newTestClock()
		b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
		held, err := b.Acquire(context.Background(), app.WorkCPU)
		if err != nil {
			t.Fatal(err)
		}
		defer held()
		entered := make(chan bool, 1)
		scan := scannerFunc(func(ctx context.Context, d domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
			if s := b.Stats(); s != (resources.Stats{IO: 1, Total: 1}) {
				t.Errorf("scan class: %+v", s)
			}
			entered <- true
			return doneScanner(ctx, d, emit)
		})
		opts := DefaultOptions()
		opts.Budget = b
		r := makeRunner(t, f.repo, scan, clock, opts, io.Discard)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { err, _ := r.executeInventory(ctx, domain.JobLease{}); done <- err }()
		clock.waitFor(t, opts.PollInterval, 1)
		select {
		case <-entered:
			t.Fatal("scanner ran while CPU held shared total")
		default:
		}
		if cancelled {
			cancel()
		} else {
			held()
			clock.fire(opts.PollInterval)
		}
		err = receive(t, done)
		cancel()
		if cancelled && !errors.Is(err, context.Canceled) || !cancelled && err != nil {
			t.Fatalf("result: %v", err)
		}
		if !cancelled {
			receive(t, entered)
		}
		held()
		if b.Stats() != (resources.Stats{}) {
			t.Fatal("scan leaked resources")
		}
	}
}

func TestSharedBudgetScanPanicReleasesPermit(t *testing.T) {
	f := oneJob(t)
	b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	opts := DefaultOptions()
	opts.Budget = b
	r := makeRunner(t, f.repo, scannerFunc(func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error {
		panic("private fixture")
	}), newTestClock(), opts, io.Discard)
	if err, _ := r.executeInventory(context.Background(), domain.JobLease{}); !errors.Is(err, domain.ErrScanIO) {
		t.Fatal("scanner panic classification changed")
	}
	if b.Stats() != (resources.Stats{}) {
		t.Fatal("panic leaked shared permit")
	}
}
