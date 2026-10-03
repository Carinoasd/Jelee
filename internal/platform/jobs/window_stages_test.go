package jobs

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func TestWindowProbeStagesJoinReleaseAndResume(t *testing.T) {
	for _, stage := range []string{"inspect", "probe"} {
		t.Run(stage, func(t *testing.T) {
			f := newProbeWorkerFixture(t, 1)
			clock := newTestClock()
			window := &switchWindow{}
			window.open.Store(true)
			repo := &pauseFake{executionFake: f.base.repo, paused: make(chan domain.JobLease, 1)}
			entered := make(chan bool, 1)
			originalInspect, originalProbe := f.prober.inspect, f.prober.probe
			if stage == "inspect" {
				f.prober.inspect = func(c context.Context, _ domain.ProbeSource) (domain.ProbeStamp, error) {
					entered <- true
					<-c.Done()
					return domain.ProbeStamp{}, c.Err()
				}
			} else {
				f.prober.probe = func(c context.Context, _ domain.ProbeSource) (domain.ProbeObservation, error) {
					entered <- true
					<-c.Done()
					return domain.ProbeObservation{}, c.Err()
				}
			}
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
			opts := DefaultOptions()
			opts.Budget = budget
			opts.Workers = 1
			opts.Window = window
			opts.Probe = &ProbeOptions{Repository: f.repo, Prober: f.prober, LeaseDuration: 10 * time.Second, MaxConcurrent: 1}
			r := makeRunner(t, repo, scannerFunc(doneScanner), clock, opts, io.Discard)
			done := make(chan bool, 1)
			go func() { r.run(context.Background(), f.lease); done <- true }()
			receive(t, entered)
			window.open.Store(false)
			clock.fire(time.Second)
			receive(t, repo.paused)
			receive(t, done)
			if budget.Stats() != (resources.Stats{}) {
				t.Fatal("window cancellation leaked shared permit")
			}
			if len(r.probeGate) != 0 || len(f.batches) != 0 || len(f.aborts) != 0 {
				t.Fatal("cancel leaked slot or committed probe outcome")
			}
			// Cancellation returns the child lease to the parent pause transaction;
			// it must not try a child transaction with a cancelled context.
			wantAcquired := 0
			if stage == "probe" {
				wantAcquired = 1
			}
			if f.acquires != wantAcquired || f.releases != 0 {
				t.Fatal("unexpected child lease ownership")
			}

			f.prober.inspect, f.prober.probe = originalInspect, originalProbe
			window.open.Store(true)
			f.lease.Generation++
			r.run(context.Background(), f.lease)
			if receive(t, f.base.terminal).state != domain.JobSucceeded || f.phase.Progress.Succeeded != 1 || f.phase.Progress.Processed != 1 {
				t.Fatal("probe did not resume exactly once")
			}
		})
	}
}

func TestWindowNFOStagesJoinReleaseAndResume(t *testing.T) {
	for _, stage := range []string{"read", "parse"} {
		t.Run(stage, func(t *testing.T) {
			f := newNFOWorkerFixture(t, 1)
			clock := newTestClock()
			window := &switchWindow{}
			window.open.Store(true)
			repo := &pauseFake{executionFake: f.base.repo, paused: make(chan domain.JobLease, 1)}
			entered := make(chan bool, 1)
			original := f.reader.read
			f.reader.read = func(c context.Context, _ domain.NFOSource) (app.NFOReadSource, error) {
				if stage == "read" {
					entered <- true
					<-c.Done()
					return nil, c.Err()
				}
				return &nfoReadFake{stamp: nfoWorkerStamp(), parse: func(c context.Context) (domain.NFOValidationSummary, error) {
					entered <- true
					<-c.Done()
					return domain.NFOValidationSummary{}, c.Err()
				}}, nil
			}
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
			opts := DefaultOptions()
			opts.Budget = budget
			opts.Workers = 1
			opts.Window = window
			opts.NFO = &NFOOptions{Repository: f.repo, Reader: f.reader, MaxConcurrent: 2}
			r := makeRunner(t, repo, scannerFunc(doneScanner), clock, opts, io.Discard)
			done := make(chan bool, 1)
			go func() { r.run(context.Background(), f.lease); done <- true }()
			receive(t, entered)
			window.open.Store(false)
			clock.fire(time.Second)
			receive(t, repo.paused)
			receive(t, done)
			if budget.Stats() != (resources.Stats{}) {
				t.Fatal("window cancellation leaked shared permit")
			}
			if len(r.nfoGate) != 0 || len(f.batches) != 0 || len(f.aborts) != 0 {
				t.Fatal("cancel leaked NFO slot or committed outcome")
			}
			f.reader.read = original
			window.open.Store(true)
			f.lease.Generation++
			r.run(context.Background(), f.lease)
			if receive(t, f.base.terminal).state != domain.JobSucceeded || f.phase.Progress.Processed != 1 || f.phase.Progress.Valid != 1 {
				t.Fatal("NFO did not resume exactly once")
			}
		})
	}
}
