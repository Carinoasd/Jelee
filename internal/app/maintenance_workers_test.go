package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type fakeSweeper struct {
	names    []string
	swept    []string
	live     map[string]bool
	fail     map[string]bool
	broken   string
	enforced int
	listErr  error
	cancel   func(string)
}

func (f *fakeSweeper) CountSources(context.Context) (int64, error) {
	return int64(len(f.names)), f.listErr
}

func (f *fakeSweeper) ListSources(_ context.Context, after string, limit int) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []string
	for _, n := range f.names {
		if n > after && len(out) < limit {
			out = append(out, n)
		}
	}
	return out, nil
}

func (f *fakeSweeper) SweepSource(_ context.Context, source string, live bool, _ time.Time) (domain.SubtitleCacheSweep, error) {
	if source == f.broken {
		return domain.SubtitleCacheSweep{}, errors.New("cache root vanished")
	}
	f.swept = append(f.swept, source)
	if f.live == nil {
		f.live = map[string]bool{}
	}
	f.live[source] = live
	if f.cancel != nil {
		f.cancel(source)
	}
	if f.fail[source] {
		return domain.SubtitleCacheSweep{Failed: 1}, nil
	}
	if !live {
		return domain.SubtitleCacheSweep{Orphaned: 1, Bytes: 10}, nil
	}
	return domain.SubtitleCacheSweep{Revisions: 1, Bytes: 1}, nil
}

func (f *fakeSweeper) EnforceCapacity(context.Context) (domain.SubtitleCacheSweep, error) {
	f.enforced++
	return domain.SubtitleCacheSweep{Capacity: 2, Bytes: 7}, nil
}

type fakeCleanupRepo struct {
	state    domain.SubtitleCacheCleanupJobState
	total    int64
	records  int
	live     map[string]bool
	finished map[string]bool
	failed   int
	sweep    domain.SubtitleCacheSweep
}

func (r *fakeCleanupRepo) NextSubtitleCacheCleanup(_ context.Context, _ domain.JobLease, sources []string, total int64) (domain.SubtitleCacheCleanupBatch, error) {
	if total >= 0 && r.total < 0 {
		r.total = total
	}
	batch := domain.SubtitleCacheCleanupBatch{State: r.state, Live: map[string]bool{}}
	for _, s := range sources {
		batch.Live[s] = r.live[s]
	}
	return batch, nil
}

func (r *fakeCleanupRepo) RecordSubtitleCacheSources(_ context.Context, _ domain.JobLease, cursor string, finished, failed int, sweep domain.SubtitleCacheSweep, last bool) error {
	r.records++
	if cursor != "" {
		if r.state.CursorSource != nil && cursor <= *r.state.CursorSource {
			return domain.ErrConflict
		}
		c := cursor
		r.state.CursorSource = &c
	}
	r.state.Sources += finished
	r.failed += failed
	r.sweep.Add(sweep)
	if last {
		r.state.Phase = domain.SubtitleCachePhaseCapacity
	}
	return nil
}

func (r *fakeCleanupRepo) RecordSubtitleCacheCapacity(_ context.Context, _ domain.JobLease, sweep domain.SubtitleCacheSweep) error {
	if r.state.Phase != domain.SubtitleCachePhaseCapacity {
		return domain.ErrConflict
	}
	r.sweep.Add(sweep)
	r.state.Phase = domain.SubtitleCachePhaseDone
	return nil
}

func (r *fakeCleanupRepo) FinishSubtitleCacheCleanup(context.Context, domain.JobLease, string, string) error {
	return nil
}

func cacheNames(n int) []string {
	var names []string
	for i := range n {
		names = append(names, fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1))
	}
	return names
}

func TestSubtitleCacheCleanupWorkerCancelsAndResumes(t *testing.T) {
	lease := domain.JobLease{Job: domain.Job{ID: "00000000-0000-4000-8000-0000000000aa", Kind: domain.JobSubtitleCacheCleanup}}
	names := cacheNames(domain.SubtitleCacheBatchSize + 10)
	repo := &fakeCleanupRepo{state: domain.SubtitleCacheCleanupJobState{MaxIdleDays: 30, Phase: domain.SubtitleCachePhaseSources}, total: -1, live: map[string]bool{names[0]: true}}
	ctx, cancel := context.WithCancel(context.Background())
	sweeper := &fakeSweeper{names: names, fail: map[string]bool{names[1]: true}, cancel: func(source string) {
		if source == names[4] {
			cancel()
		}
	}}
	worker, err := NewSubtitleCacheCleanupWorker(sweeper, repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Run(ctx, lease); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled run", err)
	}
	if repo.total != int64(len(names)) || repo.state.CursorSource == nil || *repo.state.CursorSource != names[4] || repo.state.Sources != 4 || repo.failed != 1 || repo.sweep.Orphaned != 3 || repo.sweep.Revisions != 1 || sweeper.enforced != 0 {
		t.Fatalf("cancelled state %+v failed=%d sweep=%+v", repo.state, repo.failed, repo.sweep)
	}
	if !sweeper.live[names[0]] || sweeper.live[names[2]] {
		t.Fatal("liveness not passed through")
	}
	// The resumed run continues after the cursor, sweeps every source once
	// and enforces the size bound once.
	sweeper.cancel, sweeper.swept = nil, nil
	if err = worker.Run(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sweeper.swept, names[5:]) || repo.state.Phase != domain.SubtitleCachePhaseDone || sweeper.enforced != 1 || repo.sweep.Capacity != 2 || repo.state.Sources != len(names)-1 {
		t.Fatalf("resumed state %+v swept=%d", repo.state, len(sweeper.swept))
	}
	if err = worker.Run(context.Background(), lease); err != nil || sweeper.enforced != 1 {
		t.Fatal("finished job ran again", err)
	}
	// A failing root stops the run after recording the finished sources.
	repo = &fakeCleanupRepo{state: domain.SubtitleCacheCleanupJobState{MaxIdleDays: 1, Phase: domain.SubtitleCachePhaseSources}, total: -1}
	sweeper = &fakeSweeper{names: cacheNames(3), broken: cacheNames(3)[2]}
	if worker, err = NewSubtitleCacheCleanupWorker(sweeper, repo, time.Second); err != nil {
		t.Fatal(err)
	}
	if err = worker.Run(context.Background(), lease); err == nil || repo.state.Sources != 2 || repo.state.Phase != domain.SubtitleCachePhaseSources {
		t.Fatalf("broken root %v %+v", err, repo.state)
	}
	sweeper.listErr = errors.New("unlisted")
	if err = worker.Run(context.Background(), lease); err == nil {
		t.Fatal("listing failure ignored")
	}
	// Without a sweeper the job fails; other kinds are refused.
	none, err := NewSubtitleCacheCleanupWorker(nil, repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = none.Run(context.Background(), lease); !errors.Is(err, domain.ErrSubtitleCacheUnavailable) {
		t.Fatal("no sweeper", err)
	}
	if err = none.Run(context.Background(), domain.JobLease{Job: domain.Job{Kind: domain.JobBackupReminder}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("other kind", err)
	}
	if _, err = NewSubtitleCacheCleanupWorker(nil, nil, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil repository", err)
	}
}

type fakeRemovalRepo struct {
	items    []string
	cursor   string
	fail     map[string]error
	removed  []string
	failed   []string
	cancel   func(string)
	nextErr  error
	finished int
}

func (r *fakeRemovalRepo) NextExternalMetadataRemovalBatch(_ context.Context, _ domain.JobLease, limit int) ([]string, error) {
	if r.nextErr != nil {
		return nil, r.nextErr
	}
	var out []string
	for _, item := range r.items {
		if item > r.cursor && len(out) < limit {
			out = append(out, item)
		}
	}
	return out, nil
}

func (r *fakeRemovalRepo) RemoveExternalMetadataForJob(_ context.Context, _ domain.JobLease, item string) (domain.ExternalMetadataRemovalOutcome, error) {
	if r.cancel != nil {
		r.cancel(item)
	}
	if err := r.fail[item]; err != nil {
		return domain.ExternalMetadataRemovalOutcome{}, err
	}
	r.removed, r.cursor = append(r.removed, item), item
	return domain.ExternalMetadataRemovalOutcome{Removed: true, Fields: 1}, nil
}

func (r *fakeRemovalRepo) RecordExternalMetadataRemovalFailure(_ context.Context, _ domain.JobLease, item string) error {
	r.failed, r.cursor = append(r.failed, item), item
	return nil
}

func (r *fakeRemovalRepo) FinishExternalMetadataRemoval(context.Context, domain.JobLease, string, string) error {
	r.finished++
	return nil
}

func TestExternalMetadataRemovalWorkerIsolatesFailures(t *testing.T) {
	lease := domain.JobLease{Job: domain.Job{Kind: domain.JobExternalMetadataRemoval}}
	items := cacheNames(domain.ExternalMetadataRemovalBatchSize + 5)
	repo := &fakeRemovalRepo{items: items, fail: map[string]error{items[3]: domain.ErrInvalid, items[20]: domain.ErrConflict}}
	worker, err := NewExternalMetadataRemovalWorker(repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Run(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if len(repo.failed) != 2 || len(repo.removed) != len(items)-2 || repo.failed[0] != items[3] {
		t.Fatalf("isolation removed=%d failed=%v", len(repo.removed), repo.failed)
	}
	// A storage failure stops the run without counting the item.
	repo = &fakeRemovalRepo{items: items, fail: map[string]error{items[2]: domain.ErrDatabase}}
	if worker, err = NewExternalMetadataRemovalWorker(repo, time.Second); err != nil {
		t.Fatal(err)
	}
	if err = worker.Run(context.Background(), lease); !errors.Is(err, domain.ErrDatabase) || len(repo.removed) != 2 || len(repo.failed) != 0 {
		t.Fatalf("storage failure %v removed=%d", err, len(repo.removed))
	}
	// Ten refused items in a row end the run.
	fail := map[string]error{}
	for _, item := range items[:domain.ExternalMetadataRemovalMaxConsecutiveFailures] {
		fail[item] = domain.ErrInvalid
	}
	repo = &fakeRemovalRepo{items: items, fail: fail}
	worker, _ = NewExternalMetadataRemovalWorker(repo, 0)
	if err = worker.Run(context.Background(), lease); !errors.Is(err, errExternalRemovalFailures) || len(repo.failed) != domain.ExternalMetadataRemovalMaxConsecutiveFailures || len(repo.removed) != 0 {
		t.Fatalf("consecutive failures %v failed=%d", err, len(repo.failed))
	}
	// Cancellation stops between items; lost rights stop the run.
	ctx, cancel := context.WithCancel(context.Background())
	repo = &fakeRemovalRepo{items: items, cancel: func(item string) {
		if item == items[1] {
			cancel()
		}
	}}
	worker, _ = NewExternalMetadataRemovalWorker(repo, 0)
	if err = worker.Run(ctx, lease); !errors.Is(err, context.Canceled) || len(repo.removed) != 2 {
		t.Fatalf("cancel %v removed=%d", err, len(repo.removed))
	}
	repo = &fakeRemovalRepo{items: items, nextErr: domain.ErrForbidden}
	worker, _ = NewExternalMetadataRemovalWorker(repo, 0)
	if err = worker.Run(context.Background(), lease); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("lost rights", err)
	}
	if err = worker.Run(context.Background(), domain.JobLease{Job: domain.Job{Kind: domain.JobMetadataRefresh}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("other kind", err)
	}
	if _, err = NewExternalMetadataRemovalWorker(nil, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil repository", err)
	}
}

type fakeReminderRepo struct {
	calls int
	err   error
}

func (r *fakeReminderRepo) CheckBackupReminder(context.Context, domain.JobLease) (domain.BackupStatus, error) {
	r.calls++
	return domain.BackupStatus{Status: domain.BackupReminderNever}, r.err
}

func (r *fakeReminderRepo) FinishBackupReminder(context.Context, domain.JobLease, string, string) error {
	return nil
}

func TestBackupReminderWorker(t *testing.T) {
	repo := &fakeReminderRepo{}
	worker, err := NewBackupReminderWorker(repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Run(context.Background(), domain.JobLease{Job: domain.Job{Kind: domain.JobBackupReminder}}); err != nil || repo.calls != 1 {
		t.Fatal(err, repo.calls)
	}
	repo.err = context.Canceled
	if err = worker.Run(context.Background(), domain.JobLease{Job: domain.Job{Kind: domain.JobBackupReminder}}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel", err)
	}
	if err = worker.Run(context.Background(), domain.JobLease{Job: domain.Job{Kind: domain.JobNFOApply}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("other kind", err)
	}
	if _, err = NewBackupReminderWorker(nil, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil repository")
	}
	if _, err = NewBackupReminderWorker(repo, -time.Second); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("negative timeout")
	}
}

type fakeMaintenanceRepo struct {
	MaintenanceJobRepository
	calls []string
}

func (r *fakeMaintenanceRepo) SubmitSubtitleCacheCleanupJob(_ context.Context, _ domain.Actor, _, _ string, days int, _ domain.JobPolicy) (domain.Job, bool, error) {
	r.calls = append(r.calls, fmt.Sprint("cleanup:", days))
	return domain.Job{}, false, nil
}

func (r *fakeMaintenanceRepo) SubmitBackupReminderJob(_ context.Context, _ domain.Actor, _, _ string, days int, _ domain.JobPolicy) (domain.Job, bool, error) {
	r.calls = append(r.calls, fmt.Sprint("reminder:", days))
	return domain.Job{}, false, nil
}

func (r *fakeMaintenanceRepo) BackupStatus(_ context.Context, _ domain.Actor, days int) (domain.BackupStatus, error) {
	r.calls = append(r.calls, fmt.Sprint("status:", days))
	return domain.BackupStatus{}, nil
}

func (r *fakeMaintenanceRepo) SubmitExternalMetadataRemovalJob(context.Context, domain.Actor, string, string, domain.ExternalMetadataRemovalInput, domain.JobPolicy) (domain.Job, bool, error) {
	r.calls = append(r.calls, "remove")
	return domain.Job{}, false, nil
}

func (r *fakeMaintenanceRepo) GetSubtitleCacheCleanupState(context.Context, domain.Actor, string) (domain.SubtitleCacheCleanupJobState, error) {
	r.calls = append(r.calls, "cleanup-state")
	return domain.SubtitleCacheCleanupJobState{}, nil
}

func (r *fakeMaintenanceRepo) GetBackupReminderState(context.Context, domain.Actor, string) (domain.BackupReminderJobState, error) {
	r.calls = append(r.calls, "reminder-state")
	return domain.BackupReminderJobState{}, nil
}

func (r *fakeMaintenanceRepo) GetExternalMetadataRemovalState(context.Context, domain.Actor, string) (domain.ExternalMetadataRemovalJobState, error) {
	r.calls = append(r.calls, "remove-state")
	return domain.ExternalMetadataRemovalJobState{}, nil
}

type fakeReminderSchedule struct{ calls []int }

func (f *fakeReminderSchedule) DispatchBackupReminder(_ context.Context, days int, interval time.Duration, _ domain.JobPolicy) (bool, error) {
	if interval != domain.BackupReminderInterval {
		return false, domain.ErrInvalid
	}
	f.calls = append(f.calls, days)
	return true, nil
}

func TestMaintenanceJobAdmission(t *testing.T) {
	ctx := context.Background()
	actor := domain.Actor{UserID: "00000000-0000-4000-8000-000000000001", SessionID: "00000000-0000-4000-8000-000000000002"}
	library := "00000000-0000-4000-8000-000000000003"
	base, err := NewJobs(jobRepositoryFake{}, jobTestPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = base.SubmitBackupReminder(ctx, actor, "k", domain.JobPriorityManual, 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unwired maintenance", err)
	}
	if _, err = NewJobsWithMaintenance(base, &fakeMaintenanceRepo{}, true, 366); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("bad default threshold", err)
	}
	repo := &fakeMaintenanceRepo{}
	jobs, err := NewJobsWithMaintenance(base, repo, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = jobs.SubmitSubtitleCacheCleanup(ctx, actor, "k", domain.JobPriorityManual, 0); !errors.Is(err, domain.ErrSubtitleCacheUnavailable) {
		t.Fatal("cache unavailable", err)
	}
	if jobs, err = NewJobsWithMaintenance(base, repo, true, 14); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []error{
		func() error { _, _, e := jobs.SubmitSubtitleCacheCleanup(ctx, actor, "k", "urgent", 0); return e }(),
		func() error {
			_, _, e := jobs.SubmitSubtitleCacheCleanup(ctx, actor, "k", domain.JobPriorityManual, -1)
			return e
		}(),
		func() error {
			_, _, e := jobs.SubmitSubtitleCacheCleanup(ctx, actor, "", domain.JobPriorityManual, 0)
			return e
		}(),
		func() error {
			_, _, e := jobs.SubmitBackupReminder(ctx, actor, "k", domain.JobPriorityManual, 400)
			return e
		}(),
		func() error {
			_, _, e := jobs.SubmitBackupReminder(ctx, domain.Actor{}, "k", domain.JobPriorityManual, 0)
			return e
		}(),
		func() error {
			_, _, e := jobs.SubmitExternalMetadataRemoval(ctx, actor, "x", "k", domain.ExternalMetadataRemovalInput{Priority: domain.JobPriorityManual, Confirm: true})
			return e
		}(),
		func() error { _, e := jobs.SubtitleCacheCleanupState(ctx, actor, "x"); return e }(),
		func() error { _, e := jobs.BackupReminderState(ctx, actor, "x"); return e }(),
		func() error { _, e := jobs.ExternalMetadataRemovalState(ctx, actor, "x"); return e }(),
		func() error { _, e := jobs.BackupStatus(ctx, domain.Actor{}); return e }(),
	} {
		if !errors.Is(bad, domain.ErrInvalid) {
			t.Fatal("invalid input accepted", bad)
		}
	}
	if _, _, err = jobs.SubmitExternalMetadataRemoval(ctx, actor, library, "k", domain.ExternalMetadataRemovalInput{Priority: domain.JobPriorityManual}); !errors.Is(err, domain.ErrConfirmationRequired) {
		t.Fatal("unconfirmed removal", err)
	}
	if len(repo.calls) != 0 {
		t.Fatal("invalid input reached the repository", repo.calls)
	}
	_, _, _ = jobs.SubmitSubtitleCacheCleanup(ctx, actor, "k", domain.JobPriorityBackground, 0)
	_, _, _ = jobs.SubmitBackupReminder(ctx, actor, "k", domain.JobPriorityManual, 0)
	_, _, _ = jobs.SubmitBackupReminder(ctx, actor, "k", domain.JobPriorityManual, 3)
	_, _ = jobs.BackupStatus(ctx, actor)
	_, _, _ = jobs.SubmitExternalMetadataRemoval(ctx, actor, library, "k", domain.ExternalMetadataRemovalInput{Priority: domain.JobPriorityManual, Confirm: true})
	_, _ = jobs.SubtitleCacheCleanupState(ctx, actor, library)
	_, _ = jobs.BackupReminderState(ctx, actor, library)
	_, _ = jobs.ExternalMetadataRemovalState(ctx, actor, library)
	want := []string{"cleanup:30", "reminder:14", "reminder:3", "status:14", "remove", "cleanup-state", "reminder-state", "remove-state"}
	if !slices.Equal(repo.calls, want) {
		t.Fatal("calls", repo.calls)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, _, err = jobs.SubmitBackupReminder(cancelled, actor, "k", domain.JobPriorityManual, 0); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled context", err)
	}
}

func TestBackupReminderScheduleSpacing(t *testing.T) {
	schedule := &fakeReminderSchedule{}
	b := &backupReminderSchedule{repository: schedule, days: 5, now: time.Now}
	clock := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return clock }
	if worked, err := b.dispatch(context.Background(), jobTestPolicy()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if worked, _ := b.dispatch(context.Background(), jobTestPolicy()); worked {
		t.Fatal("dispatched twice within the spacing")
	}
	clock = clock.Add(consistencyDispatchSpacing)
	if worked, _ := b.dispatch(context.Background(), jobTestPolicy()); !worked || !slices.Equal(schedule.calls, []int{5, 5}) {
		t.Fatal("spacing", schedule.calls)
	}
	if _, err := NewJobsWithBackupReminderSchedule(&Jobs{}, schedule, 5); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("schedule without definitions", err)
	}
}
