package jobs

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type switchWindow struct{ open atomic.Bool }

func (w *switchWindow) Allows(time.Time) bool { return w.open.Load() }

type pauseFake struct {
	*executionFake
	paused chan domain.JobLease
}

func (f *pauseFake) PauseJob(ctx context.Context, l domain.JobLease) error {
	f.paused <- l
	return ctx.Err()
}

func TestWindowClosedDoesNotClaimAndReopeningClaims(t *testing.T) {
	clock := newTestClock()
	window := &switchWindow{}
	f := oneJob(t)
	claims := make(chan bool, 2)
	f.repo.claim = func(context.Context, string, bool, time.Duration) (domain.JobLease, error) {
		claims <- true
		return domain.JobLease{}, domain.ErrNotFound
	}
	repo := &pauseFake{executionFake: f.repo, paused: make(chan domain.JobLease, 1)}
	opts := DefaultOptions()
	opts.Workers = 1
	opts.Window = window
	r := makeRunner(t, repo, scannerFunc(doneScanner), clock, opts, io.Discard)
	startRunner(t, r)
	clock.waitFor(t, opts.PollInterval, 1)
	select {
	case <-claims:
		t.Fatal("closed window claimed work")
	default:
	}
	window.open.Store(true)
	clock.fire(opts.PollInterval)
	receive(t, claims)
}

func TestWindowClosureJoinsScanAndPauses(t *testing.T) {
	clock := newTestClock()
	window := &switchWindow{}
	window.open.Store(true)
	f := oneJob(t)
	repo := &pauseFake{executionFake: f.repo, paused: make(chan domain.JobLease, 1)}
	entered, joined := make(chan bool, 1), make(chan bool, 1)
	scanner := scannerFunc(func(ctx context.Context, _ domain.ScanDirectory, _ func(domain.ScanBatch) error) error {
		entered <- true
		<-ctx.Done()
		joined <- true
		return ctx.Err()
	})
	opts := DefaultOptions()
	opts.Workers = 1
	opts.Window = window
	r := makeRunner(t, repo, scanner, clock, opts, io.Discard)
	startRunner(t, r)
	receive(t, entered)
	window.open.Store(false)
	clock.fire(time.Second)
	receive(t, repo.paused)
	select {
	case <-joined:
	default:
		t.Fatal("pause preceded scanner join")
	}
	select {
	case <-f.terminal:
		t.Fatal("window closure finished job")
	case <-f.released:
		t.Fatal("window closure consumed failure attempt")
	default:
	}
	clock.waitFor(t, opts.PollInterval, 1)
}

func TestWindowClosesDuringClaimWithoutStartingScan(t *testing.T) {
	clock := newTestClock()
	window := &switchWindow{}
	window.open.Store(true)
	f := oneJob(t)
	original := f.repo.claim
	f.repo.claim = func(c context.Context, o string, b bool, d time.Duration) (domain.JobLease, error) {
		l, err := original(c, o, b, d)
		window.open.Store(false)
		return l, err
	}
	repo := &pauseFake{executionFake: f.repo, paused: make(chan domain.JobLease, 1)}
	scanner := scannerFunc(func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error {
		t.Error("scan began outside window")
		return nil
	})
	opts := DefaultOptions()
	opts.Workers = 1
	opts.Window = window
	r := makeRunner(t, repo, scanner, clock, opts, io.Discard)
	startRunner(t, r)
	receive(t, repo.paused)
	clock.waitFor(t, opts.PollInterval, 1)
}

func TestWindowClosureDoesNotHideScanFailure(t *testing.T) {
	clock := newTestClock()
	window := &switchWindow{}
	window.open.Store(true)
	f := oneJob(t)
	repo := &pauseFake{executionFake: f.repo, paused: make(chan domain.JobLease, 1)}
	entered := make(chan bool, 1)
	scanner := scannerFunc(func(ctx context.Context, _ domain.ScanDirectory, _ func(domain.ScanBatch) error) error {
		entered <- true
		<-ctx.Done()
		return domain.ErrScanIO
	})
	opts := DefaultOptions()
	opts.Workers = 1
	opts.Window = window
	r := makeRunner(t, repo, scanner, clock, opts, io.Discard)
	startRunner(t, r)
	receive(t, entered)
	window.open.Store(false)
	clock.fire(time.Second)
	terminal := receive(t, f.terminal)
	if terminal.state != domain.JobFailed || terminal.code != "scan_io" {
		t.Fatalf("failure hidden: %+v", terminal)
	}
	select {
	case <-repo.paused:
		t.Fatal("failure refunded as planned pause")
	default:
	}
}
