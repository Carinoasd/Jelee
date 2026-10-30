package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type kindWorkerFunc func(context.Context, domain.JobLease) error

func (f kindWorkerFunc) Run(ctx context.Context, l domain.JobLease) error { return f(ctx, l) }

func kindRunner(t *testing.T, run kindWorkerFunc, finished *[][2]string, claim func(context.Context, domain.ScanCapabilities) (domain.JobLease, error)) *Runner {
	t.Helper()
	repo := claimStagesFake{JobExecutionRepository: &executionFake{
		heartbeat: func(context.Context, domain.JobLease, time.Duration) (bool, error) { return false, nil },
		release:   func(context.Context, domain.JobLease) error { return nil },
		finish: func(context.Context, domain.JobLease, string, string) error {
			t.Error("a maintenance job finished through the scan path")
			return nil
		},
	}, claim: claim}
	finish := func(_ context.Context, _ domain.JobLease, state, code string) error {
		*finished = append(*finished, [2]string{state, code})
		return nil
	}
	opts := DefaultOptions()
	opts.Workers = 1
	opts.Kinds = map[string]KindOptions{
		domain.JobBackupReminder:          {Worker: run, Finish: finish, FailureCode: "backup_reminder_failed"},
		domain.JobExternalMetadataRemoval: {Worker: run, Finish: finish, FailureCode: "external_metadata_removal_failed"},
	}
	r, err := New(repo, scannerFunc(nil), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func kindLease(kind string) domain.JobLease {
	return domain.JobLease{Job: domain.Job{ID: consistencyJobID, LibraryID: consistencyLibraryID, Kind: kind, State: domain.JobRunning}, Owner: "worker", Generation: 1, Policy: domain.JobPolicy{HistoryLimit: 10}}
}

func TestKindJobsRunFinishAndFailWithTheirCode(t *testing.T) {
	var finished [][2]string
	var ran []string
	run := kindWorkerFunc(func(_ context.Context, l domain.JobLease) error {
		ran = append(ran, l.Job.Kind)
		if l.Job.Kind == domain.JobExternalMetadataRemoval {
			return errors.New("item refused")
		}
		return nil
	})
	r := kindRunner(t, run, &finished, nil)
	r.run(context.Background(), kindLease(domain.JobBackupReminder))
	r.run(context.Background(), kindLease(domain.JobExternalMetadataRemoval))
	if !slices.Equal(ran, []string{domain.JobBackupReminder, domain.JobExternalMetadataRemoval}) ||
		!slices.Equal(finished, [][2]string{{domain.JobSucceeded, ""}, {domain.JobFailed, "external_metadata_removal_failed"}}) {
		t.Fatalf("ran %v finished %v", ran, finished)
	}
	finished = nil
	cancelled := kindRunner(t, func(context.Context, domain.JobLease) error { return context.Canceled }, &finished, nil)
	cancelled.run(context.Background(), kindLease(domain.JobBackupReminder))
	if len(finished) != 1 || finished[0][0] != domain.JobCancelled {
		t.Fatalf("cancelled %v", finished)
	}
}

func TestKindJobsAreAdvertisedAndValidated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var advertised []string
	claim := func(_ context.Context, caps domain.ScanCapabilities) (domain.JobLease, error) {
		advertised = caps.Kinds
		cancel()
		return domain.JobLease{}, domain.ErrNotFound
	}
	var finished [][2]string
	kindRunner(t, func(context.Context, domain.JobLease) error { return nil }, &finished, claim).work(ctx)
	if !slices.Equal(advertised, []string{domain.JobBackupReminder, domain.JobExternalMetadataRemoval}) {
		t.Fatal("advertised kinds", advertised)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := kindWorkerFunc(func(context.Context, domain.JobLease) error { return nil })
	finish := func(context.Context, domain.JobLease, string, string) error { return nil }
	for name, kinds := range map[string]map[string]KindOptions{
		"unknown kind":  {"inventory_scan": {Worker: worker, Finish: finish, FailureCode: "scan_failed"}},
		"wrong code":    {domain.JobBackupReminder: {Worker: worker, Finish: finish, FailureCode: "scan_failed"}},
		"no worker":     {domain.JobBackupReminder: {Finish: finish, FailureCode: "backup_reminder_failed"}},
		"no finish":     {domain.JobBackupReminder: {Worker: worker, FailureCode: "backup_reminder_failed"}},
		"plain storage": nil,
	} {
		opts := DefaultOptions()
		opts.Kinds = kinds
		var err error
		if kinds == nil {
			opts.Kinds = map[string]KindOptions{domain.JobBackupReminder: {Worker: worker, Finish: finish, FailureCode: "backup_reminder_failed"}}
			_, err = New(&executionFake{}, scannerFunc(nil), opts, logger)
		} else {
			_, err = New(claimStagesFake{JobExecutionRepository: &executionFake{}}, scannerFunc(nil), opts, logger)
		}
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatal(name, "accepted", err)
		}
	}
}
