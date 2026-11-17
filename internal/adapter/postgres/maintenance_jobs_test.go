package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

// hookedMaintenance observes the checkpoints of the maintenance workers.
type hookedMaintenance struct {
	*Store
	mu      sync.Mutex
	records int
	after   func(n int, lease domain.JobLease)
}

func (h *hookedMaintenance) checkpoint(err error, lease domain.JobLease) {
	h.mu.Lock()
	h.records++
	n := h.records
	h.mu.Unlock()
	if err == nil && h.after != nil {
		h.after(n, lease)
	}
}

func (h *hookedMaintenance) RemoveExternalMetadataForJob(ctx context.Context, lease domain.JobLease, item string) (domain.ExternalMetadataRemovalOutcome, error) {
	o, err := h.Store.RemoveExternalMetadataForJob(ctx, lease, item)
	h.checkpoint(err, lease)
	return o, err
}

func (h *hookedMaintenance) RecordSubtitleCacheSources(ctx context.Context, lease domain.JobLease, cursor string, finished, failed int, sweep domain.SubtitleCacheSweep, last bool) error {
	err := h.Store.RecordSubtitleCacheSources(ctx, lease, cursor, finished, failed, sweep, last)
	h.checkpoint(err, lease)
	return err
}

// startMaintenanceRunner claims the three maintenance kinds; repository
// replaces the store for the cleanup and removal checkpoints.
func startMaintenanceRunner(t *testing.T, f jobFixture, sweeper app.SubtitleCacheSweeper, repository *hookedMaintenance) func() {
	t.Helper()
	if repository == nil {
		repository = &hookedMaintenance{Store: f.s}
	}
	cleanup, err := app.NewSubtitleCacheCleanupWorker(sweeper, repository, 0)
	if err != nil {
		t.Fatal(err)
	}
	reminder, err := app.NewBackupReminderWorker(f.s, 0)
	if err != nil {
		t.Fatal(err)
	}
	removal, err := app.NewExternalMetadataRemovalWorker(repository, 0)
	if err != nil {
		t.Fatal(err)
	}
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval = 1, 100*time.Millisecond
	opts.Kinds = map[string]jobs.KindOptions{
		domain.JobSubtitleCacheCleanup:    {Worker: cleanup, Finish: f.s.FinishSubtitleCacheCleanup, FailureCode: "subtitle_cache_cleanup_failed"},
		domain.JobBackupReminder:          {Worker: reminder, Finish: f.s.FinishBackupReminder, FailureCode: "backup_reminder_failed"},
		domain.JobExternalMetadataRemoval: {Worker: removal, Finish: f.s.FinishExternalMetadataRemoval, FailureCode: "external_metadata_removal_failed"},
	}
	runner, err := jobs.New(f.s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}
}

// tmdbItem is a movie with a local file and the four TMDB fields applied
// (revision 2).
func tmdbItem(t *testing.T, f jobFixture, n int) string {
	t.Helper()
	item := metadataItem(t, f)
	removeMediaSource(t, f, item, fmt.Sprintf("Movies/Film.%03d.mkv", n))
	if _, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, item, 1, tmdbMetadataUpdate(int32(100+n), true)); err != nil {
		t.Fatal(err)
	}
	return item
}

func tmdbFieldCount(t *testing.T, f jobFixture, item string) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM item_metadata_fields WHERE item_id=$1::uuid AND source='tmdb'`, item).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func refreshReview(t *testing.T, f jobFixture, item, state string) {
	t.Helper()
	rejected := "NULL"
	if state == domain.MetadataReviewRejected {
		rejected = "clock_timestamp()"
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO metadata_refresh_reviews(item_id,library_id,state,resource,confidence,reason,query_title,candidates,rejected_at) VALUES($1::uuid,$2::uuid,$3,'movie','low','year_mismatch','Heat','[{"providerId":949,"title":"Heat","originalTitle":"","date":"1995-12-15","similarity":100}]',`+rejected+`)`, item, f.registration.Library.ID, state); err != nil {
		t.Fatal(err)
	}
}

func submitRemoval(t *testing.T, f jobFixture, key string, drop bool) domain.Job {
	t.Helper()
	job, replay, err := f.s.SubmitExternalMetadataRemovalJob(f.ctx, f.a, f.registration.Library.ID, key, domain.ExternalMetadataRemovalInput{Priority: domain.JobPriorityManual, Confirm: true, DropRejectedReviews: drop}, f.policy)
	if err != nil || replay || job.Kind != domain.JobExternalMetadataRemoval || job.State != domain.JobQueued {
		t.Fatal("submit removal", err, replay, job)
	}
	return job
}

func removalState(t *testing.T, f jobFixture, job string) domain.ExternalMetadataRemovalJobState {
	t.Helper()
	v, err := f.s.GetExternalMetadataRemovalState(f.ctx, f.a, job)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func maintenanceAudits(t *testing.T, f jobFixture, event string) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event=$1`, event).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExternalMetadataRemovalJobRespectsLocksAndReviews(t *testing.T) {
	f := newJobFixtureWithin(t, 3*time.Minute)
	plain := tmdbItem(t, f, 1)
	// A locked provider date stays; the other provider fields go.
	partly := tmdbItem(t, f, 2)
	lock := true
	if _, err := f.s.UpdateItemMetadata(f.ctx, f.a, partly, 2, []domain.ItemMetadataPatch{{Field: "date", Locked: &lock}}); err != nil {
		t.Fatal(err)
	}
	// Every provider field locked: nothing removed, no new revision.
	locked := tmdbItem(t, f, 3)
	if _, err := f.s.UpdateItemMetadata(f.ctx, f.a, locked, 2, []domain.ItemMetadataPatch{{Field: "title", Locked: &lock}, {Field: "originalTitle", Locked: &lock}, {Field: "overview", Locked: &lock}, {Field: "date", Locked: &lock}}); err != nil {
		t.Fatal(err)
	}
	// Manual values are not provider values and are never candidates.
	manual := metadataItem(t, f)
	note := "my own words"
	if _, err := f.s.UpdateItemMetadata(f.ctx, f.a, manual, 1, []domain.ItemMetadataPatch{{Field: "overview", Value: &note}}); err != nil {
		t.Fatal(err)
	}
	// Reviews hold provider candidate titles: pending ones go, a rejection
	// stays unless asked for.
	pending, rejected := metadataItem(t, f), metadataItem(t, f)
	refreshReview(t, f, pending, domain.MetadataReviewPending)
	refreshReview(t, f, rejected, domain.MetadataReviewRejected)
	refreshReview(t, f, plain, domain.MetadataReviewPending)
	revisions := map[string]int64{}
	for _, item := range []string{locked, manual, pending, rejected} {
		revisions[item] = refreshTestRevision(t, f, item)
	}

	stop := startMaintenanceRunner(t, f, nil, nil)
	defer stop()
	job := submitRemoval(t, f, "remove-1", false)
	if total, err := f.s.GetJob(f.ctx, f.a, job.ID); err != nil || total.Progress == nil || total.Progress.Total == nil || *total.Progress.Total != 4 {
		t.Fatal("admission total", err, total.Progress)
	}
	done := waitNFOApplyJob(t, f, job.ID)
	if done.State != domain.JobSucceeded || done.Files != 3 || done.Skipped != 1 {
		t.Fatal("removal job", done)
	}
	state := removalState(t, f, job.ID)
	if state.Removed != 2 || state.Fields != 7 || state.Kept != 1 || state.Skipped != 1 || state.Failed != 0 || state.Reviews != 2 || state.DropRejectedReviews || state.CursorItemID == nil {
		t.Fatalf("counters %+v", state)
	}
	if tmdbFieldCount(t, f, plain) != 0 || refreshTestField(t, f, plain, "title").Value != "Film.001" || refreshTestRevision(t, f, plain) != 3 {
		t.Fatal("plain item not cleared")
	}
	if tmdbFieldCount(t, f, partly) != 1 || refreshTestField(t, f, partly, "date").Source != "tmdb" || !refreshTestField(t, f, partly, "date").Locked {
		t.Fatal("locked field removed")
	}
	if tmdbFieldCount(t, f, locked) != 4 {
		t.Fatal("locked item changed")
	}
	if v := refreshTestField(t, f, manual, "overview"); v.Value != note || v.Source != "manual" {
		t.Fatal("manual value changed", v)
	}
	for item, revision := range revisions {
		if refreshTestRevision(t, f, item) != revision {
			t.Fatal("unchanged item got a revision", item)
		}
	}
	if _, err := f.s.GetMetadataRefreshReview(f.ctx, f.a, pending); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("pending review kept", err)
	}
	if r, err := f.s.GetMetadataRefreshReview(f.ctx, f.a, rejected); err != nil || r.State != domain.MetadataReviewRejected {
		t.Fatal("rejection dropped without being asked", err)
	}
	// One item audit per removed item naming the job, without the text.
	var after string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT after_state::text FROM audit_logs WHERE event='item.external_metadata_removed' AND target_id=$1::uuid AND actor_id=$2::uuid`, plain, f.a.UserID).Scan(&after); err != nil || !strings.Contains(after, job.ID) || strings.Contains(after, "Provider overview") {
		t.Fatal("item audit", err, after)
	}
	for event, want := range map[string]int{"item.external_metadata_removed": 2, "external_metadata_removal.submitted": 1, "external_metadata_removal.finished": 1} {
		if n := maintenanceAudits(t, f, event); n != want {
			t.Fatal("audit", event, n)
		}
	}

	// Asked for, the rejection goes too; a second run finds nothing else.
	job = submitRemoval(t, f, "remove-2", true)
	if done := waitNFOApplyJob(t, f, job.ID); done.State != domain.JobSucceeded {
		t.Fatal("second removal", done)
	}
	if state = removalState(t, f, job.ID); state.Reviews != 1 || state.Removed != 0 || state.Kept != 2 || state.Skipped != 1 {
		t.Fatalf("second counters %+v", state)
	}
	if _, err := f.s.GetMetadataRefreshReview(f.ctx, f.a, rejected); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("rejection kept", err)
	}
}

func TestExternalMetadataRemovalJobCancelsResumesAndIsolates(t *testing.T) {
	f := newJobFixtureWithin(t, 4*time.Minute)
	const n = 40
	var items []string
	for i := range n {
		items = append(items, tmdbItem(t, f, i))
	}
	slices.Sort(items)

	// Cancellation after the third item keeps exactly the finished items.
	job := submitRemoval(t, f, "cancel-me", false)
	repo := &hookedMaintenance{Store: f.s, after: func(n int, lease domain.JobLease) {
		if n == 3 {
			if _, err := f.s.CancelJob(f.ctx, f.a, lease.Job.ID); err != nil {
				t.Error(err)
			}
		}
	}}
	stop := startMaintenanceRunner(t, f, nil, repo)
	if done := waitNFOApplyJob(t, f, job.ID); done.State != domain.JobCancelled {
		stop()
		t.Fatal("job not cancelled", done)
	}
	stop()
	state := removalState(t, f, job.ID)
	if state.CursorItemID == nil || state.Removed < 3 || state.Removed >= n {
		t.Fatalf("cancelled counters %+v", state)
	}
	cleared := 0
	for _, item := range items {
		fields := tmdbFieldCount(t, f, item)
		switch {
		case item <= *state.CursorItemID && fields != 0:
			t.Fatal("finished item kept provider fields", item)
		case item > *state.CursorItemID && fields != 4:
			t.Fatal("item after the cursor changed", item)
		case fields == 0:
			cleared++
		}
	}
	if cleared != state.Removed {
		t.Fatal("counters disagree with the items", cleared, state.Removed)
	}

	// One item refused by the database fails alone; a lease lost after the
	// first checkpoint resumes after the cursor without repeating an item.
	broken := items[n-5]
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION refuse_removal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.item_id='`+broken+`'::uuid THEN RAISE EXCEPTION 'refused' USING ERRCODE='23514'; END IF; RETURN OLD; END $$;
CREATE TRIGGER refuse_removal BEFORE DELETE ON item_metadata_fields FOR EACH ROW EXECUTE FUNCTION refuse_removal()`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.s.Pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS refuse_removal ON item_metadata_fields; DROP FUNCTION IF EXISTS refuse_removal()`)
	}()
	job = submitRemoval(t, f, "resume-me", false)
	repo = &hookedMaintenance{Store: f.s, after: func(n int, lease domain.JobLease) {
		if n == 1 {
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, lease.Job.ID); err != nil {
				t.Error(err)
			}
		}
	}}
	stop = startMaintenanceRunner(t, f, nil, repo)
	defer stop()
	done := waitNFOApplyJob(t, f, job.ID)
	if done.State != domain.JobSucceeded || done.Attempts != 2 {
		t.Fatal("job did not resume once and succeed", done)
	}
	resumed := removalState(t, f, job.ID)
	if resumed.Failed != 1 || resumed.Removed != n-cleared-1 || resumed.Removed+resumed.Failed+resumed.Kept+resumed.Skipped != n-cleared {
		t.Fatalf("resumed counters %+v", resumed)
	}
	for _, item := range items {
		if fields := tmdbFieldCount(t, f, item); item == broken && fields != 4 || item != broken && fields != 0 {
			t.Fatal("item state after resume", item, fields)
		}
	}
	if audits := maintenanceAudits(t, f, "item.external_metadata_removed"); audits != n-1 {
		t.Fatal("an item was audited twice or not at all", audits)
	}
}

func TestExternalMetadataRemovalAdmission(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	input := domain.ExternalMetadataRemovalInput{Priority: domain.JobPriorityManual}
	if _, _, err := f.s.SubmitExternalMetadataRemovalJob(f.ctx, f.a, library, "unconfirmed", input, f.policy); !errors.Is(err, domain.ErrConfirmationRequired) {
		t.Fatal("unconfirmed removal admitted", err)
	}
	input.Confirm = true
	if _, _, err := f.s.SubmitExternalMetadataRemovalJob(f.ctx, f.a, "00000000-0000-4000-8000-000000000001", "nowhere", input, f.policy); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown library admitted", err)
	}
	job := submitRemoval(t, f, "admit", false)
	if again, replay, err := f.s.SubmitExternalMetadataRemovalJob(f.ctx, f.a, library, "admit", input, f.policy); err != nil || !replay || again.ID != job.ID {
		t.Fatal("replay", err, replay)
	}
	input.Priority = domain.JobPriorityBackground
	if _, _, err := f.s.SubmitExternalMetadataRemovalJob(f.ctx, f.a, library, "admit", input, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("replay with other priority", err)
	}
	if _, _, err := f.s.SubmitExternalMetadataRemovalJob(f.ctx, f.a, library, "busy", input, f.policy); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("second job admitted", err)
	}
	if _, err := f.s.GetMetadataRefreshState(f.ctx, f.a, job.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("removal read as another kind", err)
	}
	if _, err := f.s.CancelJob(f.ctx, f.a, job.ID); err != nil {
		t.Fatal(err)
	}
	scanJob := f.submit(t, "other")
	for _, read := range []func() error{
		func() error { _, err := f.s.GetExternalMetadataRemovalState(f.ctx, f.a, scanJob.ID); return err },
		func() error { _, err := f.s.GetSubtitleCacheCleanupState(f.ctx, f.a, scanJob.ID); return err },
		func() error { _, err := f.s.GetBackupReminderState(f.ctx, f.a, scanJob.ID); return err },
	} {
		if err := read(); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("other kind read as maintenance", err)
		}
	}
	// Instance-wide jobs need an idle library; a scan keeps the only one busy.
	if _, _, err := f.s.SubmitBackupReminderJob(f.ctx, f.a, "reminder", domain.JobPriorityManual, 7, f.policy); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("reminder admitted under a busy library", err)
	}
	if _, err := f.s.CancelJob(f.ctx, f.a, scanJob.ID); err != nil {
		t.Fatal(err)
	}
	reminder, _, err := f.s.SubmitBackupReminderJob(f.ctx, f.a, "reminder", domain.JobPriorityManual, 7, f.policy)
	if err != nil || reminder.LibraryID != library || reminder.Kind != domain.JobBackupReminder {
		t.Fatal("reminder", err, reminder)
	}
	if _, _, err = f.s.SubmitSubtitleCacheCleanupJob(f.ctx, f.a, "cleanup", domain.JobPriorityManual, 30, f.policy); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("cleanup admitted while the anchor is busy", err)
	}
	second, err := f.s.RegisterLibrary(f.ctx, "second", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cleanup, _, err := f.s.SubmitSubtitleCacheCleanupJob(f.ctx, f.a, "cleanup", domain.JobPriorityManual, 30, f.policy)
	if err != nil || cleanup.LibraryID != second.Library.ID {
		t.Fatal("cleanup not anchored to the idle library", err, cleanup.LibraryID)
	}
	if _, err = f.s.CancelJob(f.ctx, f.a, reminder.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitSubtitleCacheCleanupJob(f.ctx, f.a, "cleanup-2", domain.JobPriorityManual, 30, f.policy); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("second cleanup admitted", err)
	}
	if _, _, err = f.s.SubmitSubtitleCacheCleanupJob(f.ctx, f.a, "bad", domain.JobPriorityManual, 0, f.policy); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("zero idle days", err)
	}
	if again, replay, err := f.s.SubmitSubtitleCacheCleanupJob(f.ctx, f.a, "cleanup", domain.JobPriorityManual, 30, f.policy); err != nil || !replay || again.ID != cleanup.ID {
		t.Fatal("cleanup replay", err, replay)
	}
	for event, want := range map[string]int{"subtitle_cache_cleanup.submitted": 1, "backup_reminder.submitted": 1, "external_metadata_removal.submitted": 1} {
		if n := maintenanceAudits(t, f, event); n != want {
			t.Fatal("audit", event, n)
		}
	}
}

func cacheSource(t *testing.T, root, source string, revisions map[string]time.Time) {
	t.Helper()
	for revision, used := range revisions {
		directory := filepath.Join(root, source, revision)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		entry := filepath.Join(directory, "entry.json")
		if err := os.WriteFile(entry, []byte(`{"version":1,"tracks":1,"subtitles":[{"id":1,"format":"srt","file":"t1.srt"}],"attachments":[],"bytes":5}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "t1.srt"), []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(entry, used, used); err != nil {
			t.Fatal(err)
		}
	}
}

func cacheEntries(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestSubtitleCacheCleanupJobCancelsResumesAndCleans(t *testing.T) {
	f := newJobFixtureWithin(t, 4*time.Minute)
	root := filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sweeper, err := matroska.NewSweeper(matroska.Config{CacheRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	fresh, idle := strings.Repeat("a", 32), strings.Repeat("b", 32)
	// A live source keeps its fresh revision and loses its idle one.
	item := metadataItem(t, f)
	removeMediaSource(t, f, item, "Movies/Live.mkv")
	var live string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM media_sources WHERE item_id=$1::uuid`, item).Scan(&live); err != nil {
		t.Fatal(err)
	}
	cacheSource(t, root, live, map[string]time.Time{fresh: now, idle: now.Add(-40 * 24 * time.Hour)})
	const orphans = domain.SubtitleCacheBatchSize*2 + 6
	for i := range orphans {
		cacheSource(t, root, fmt.Sprintf("00000000-0000-4000-9000-%012d", i), map[string]time.Time{fresh: now})
	}
	if err = os.WriteFile(filepath.Join(root, "README"), []byte("not ours"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Cancelled after the first batch: exactly that batch is cleaned. The
	// live source has a random ID, which sorts after every orphan.
	job, _, err := f.s.SubmitSubtitleCacheCleanupJob(f.ctx, f.a, "cancel-me", domain.JobPriorityManual, 30, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	repo := &hookedMaintenance{Store: f.s, after: func(n int, lease domain.JobLease) {
		if n == 1 {
			if _, err := f.s.CancelJob(f.ctx, f.a, lease.Job.ID); err != nil {
				t.Error(err)
			}
		}
	}}
	stop := startMaintenanceRunner(t, f, sweeper, repo)
	if done := waitNFOApplyJob(t, f, job.ID); done.State != domain.JobCancelled {
		stop()
		t.Fatal("cleanup not cancelled", done)
	}
	stop()
	state, err := f.s.GetSubtitleCacheCleanupState(f.ctx, f.a, job.ID)
	if err != nil || state.Phase != domain.SubtitleCachePhaseSources || state.CursorSource == nil || state.Sources != domain.SubtitleCacheBatchSize || state.Orphaned != domain.SubtitleCacheBatchSize || state.Revisions != 0 {
		t.Fatalf("cancelled cleanup %+v %v", state, err)
	}
	if left := len(cacheEntries(t, root)); left != orphans+2-domain.SubtitleCacheBatchSize {
		t.Fatal("cancelled cleanup removed more than its batch", left)
	}
	if j, _ := f.s.GetJob(f.ctx, f.a, job.ID); j.Progress == nil || j.Progress.Total == nil || *j.Progress.Total != orphans+1 || j.Files != domain.SubtitleCacheBatchSize || j.Bytes <= 0 {
		t.Fatal("cleanup progress", j)
	}

	// A lease lost after the first checkpoint resumes after the cursor.
	job, _, err = f.s.SubmitSubtitleCacheCleanupJob(f.ctx, f.a, "resume-me", domain.JobPriorityBackground, 30, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	repo = &hookedMaintenance{Store: f.s, after: func(n int, lease domain.JobLease) {
		if n == 1 {
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, lease.Job.ID); err != nil {
				t.Error(err)
			}
		}
	}}
	stop = startMaintenanceRunner(t, f, sweeper, repo)
	defer stop()
	done := waitNFOApplyJob(t, f, job.ID)
	if done.State != domain.JobSucceeded || done.Attempts != 2 {
		t.Fatal("cleanup did not resume once and succeed", done)
	}
	state, err = f.s.GetSubtitleCacheCleanupState(f.ctx, f.a, job.ID)
	remaining := orphans - domain.SubtitleCacheBatchSize
	if err != nil || state.Phase != domain.SubtitleCachePhaseDone || state.Orphaned != remaining || state.Sources != remaining+1 || state.Revisions != 1 || state.Failed != 0 {
		t.Fatalf("resumed cleanup %+v %v", state, err)
	}
	if names := cacheEntries(t, root); !slices.Equal(names, []string{live, "README"}) && !slices.Equal(names, []string{"README", live}) {
		t.Fatal("cache after cleanup", names)
	}
	if _, err = os.Stat(filepath.Join(root, live, fresh)); err != nil {
		t.Fatal("fresh revision of a live source removed", err)
	}
	if n := maintenanceAudits(t, f, "subtitle_cache_cleanup.submitted"); n != 2 {
		t.Fatal("submit audit", n)
	}
}

// exportedAt records a metadata export age ago. The append-only insert
// guard stamps every row with the current time, so it is bypassed for this
// one row inside a transaction.
func exportedAt(t *testing.T, f jobFixture, age time.Duration) {
	t.Helper()
	at := time.Now().Add(-age).UTC().Format(time.RFC3339Nano)
	if _, err := f.s.Pool.Exec(f.ctx, `BEGIN;
ALTER TABLE audit_logs DISABLE TRIGGER guard_audit_logs_insert;
INSERT INTO audit_logs(event,category,target_ref,before_state,after_state,occurred_at) VALUES('metadata.exported','security','sha256:`+strings.Repeat("0", 64)+`','{}','{}','`+at+`');
ALTER TABLE audit_logs ENABLE TRIGGER guard_audit_logs_insert;
COMMIT`); err != nil {
		t.Fatal(err)
	}
}

func TestBackupReminderJob(t *testing.T) {
	f := newJobFixtureWithin(t, 3*time.Minute)
	stop := startMaintenanceRunner(t, f, nil, nil)
	defer stop()
	run := func(key string, days int) domain.BackupReminderJobState {
		t.Helper()
		job, _, err := f.s.SubmitBackupReminderJob(f.ctx, f.a, key, domain.JobPriorityManual, days, f.policy)
		if err != nil {
			t.Fatal(err)
		}
		if done := waitNFOApplyJob(t, f, job.ID); done.State != domain.JobSucceeded || done.Files != 1 || done.Progress == nil || done.Progress.Total == nil || *done.Progress.Total != 1 {
			t.Fatal("reminder job", done)
		}
		state, err := f.s.GetBackupReminderState(f.ctx, f.a, job.ID)
		if err != nil || state.Result == nil || state.Trigger != domain.BackupReminderManual || state.ThresholdDays != days {
			t.Fatal("reminder state", err, state)
		}
		return state
	}
	if s := run("never", 7); s.Result.Status != domain.BackupReminderNever || s.Result.LastExportAt != nil || maintenanceAudits(t, f, "backup.reminder") != 1 {
		t.Fatalf("no export %+v", s.Result)
	}
	exportedAt(t, f, 10*24*time.Hour)
	if s := run("overdue", 7); s.Result.Status != domain.BackupReminderOverdue || s.Result.AgeSeconds == nil || *s.Result.AgeSeconds < 10*86400-60 || maintenanceAudits(t, f, "backup.reminder") != 2 {
		t.Fatalf("old export %+v", s.Result)
	}
	if s := run("lenient", 30); s.Result.Status != domain.BackupReminderOK || maintenanceAudits(t, f, "backup.reminder") != 2 {
		t.Fatalf("within threshold %+v", s.Result)
	}
	exportedAt(t, f, time.Hour)
	if status, err := f.s.BackupStatus(f.ctx, f.a, 1); err != nil || status.Status != domain.BackupReminderOK || status.AgeSeconds == nil || *status.AgeSeconds > 3700 {
		t.Fatal("status", err, status)
	}
	var after string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT after_state::text FROM audit_logs WHERE event='backup.reminder' ORDER BY id DESC LIMIT 1`).Scan(&after); err != nil || !strings.Contains(after, `"overdue"`) || !strings.Contains(after, `"thresholdDays": 7`) {
		t.Fatal("reminder audit", err, after)
	}
}

func TestBackupReminderResumeDispatchAndCancel(t *testing.T) {
	f := newJobFixture(t)
	// A resumed check returns the recorded result without a second entry.
	job, _, err := f.s.SubmitBackupReminderJob(f.ctx, f.a, "resume", domain.JobPriorityManual, 7, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	claim := func(owner string) domain.JobLease {
		t.Helper()
		l, err := f.s.ClaimJobWithCapabilities(f.ctx, owner, false, time.Minute, domain.ScanCapabilities{Kinds: []string{domain.JobBackupReminder}})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	lease := claim("first")
	if lease.Job.ID != job.ID {
		t.Fatal("claimed", lease.Job.ID)
	}
	first, err := f.s.CheckBackupReminder(f.ctx, lease)
	if err != nil || first.Status != domain.BackupReminderNever {
		t.Fatal(err, first)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CheckBackupReminder(f.ctx, lease); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("expired lease", err)
	}
	second := claim("second")
	again, err := f.s.CheckBackupReminder(f.ctx, second)
	if err != nil || again.Status != first.Status || !again.CheckedAt.Equal(first.CheckedAt) || maintenanceAudits(t, f, "backup.reminder") != 1 {
		t.Fatal("resumed check", err, again, first)
	}
	if err = f.s.FinishBackupReminder(f.ctx, second, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	// Cancellation before the check records nothing.
	job, _, err = f.s.SubmitBackupReminderJob(f.ctx, f.a, "cancel", domain.JobPriorityManual, 7, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	lease = claim("third")
	if _, err = f.s.CancelJob(f.ctx, f.a, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CheckBackupReminder(f.ctx, lease); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled check", err)
	}
	if err = f.s.FinishBackupReminder(f.ctx, lease, domain.JobCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if state, err := f.s.GetBackupReminderState(f.ctx, f.a, job.ID); err != nil || state.Result != nil {
		t.Fatal("cancelled check recorded", err, state)
	}
	// The schedule queues one background check per interval.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET created_at=created_at-interval '2 days' WHERE kind='backup_reminder'`); err != nil {
		t.Fatal(err)
	}
	if queued, err := f.s.DispatchBackupReminder(f.ctx, 3, 24*time.Hour, f.policy); err != nil || !queued {
		t.Fatal("dispatch", err, queued)
	}
	if queued, err := f.s.DispatchBackupReminder(f.ctx, 3, 24*time.Hour, f.policy); err != nil || queued {
		t.Fatal("second dispatch", err, queued)
	}
	var trigger string
	var days int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT s.trigger,s.threshold_days FROM backup_reminder_job_state s JOIN jobs j ON j.id=s.job_id WHERE j.actor_id IS NULL AND j.priority='background'`).Scan(&trigger, &days); err != nil || trigger != domain.BackupReminderSchedule || days != 3 {
		t.Fatal("scheduled job", err, trigger, days)
	}
	if _, err = f.s.DispatchBackupReminder(f.ctx, 3, time.Minute, f.policy); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("short interval", err)
	}
}

func TestMaintenanceJobsMigrationRefusesRetainedState(t *testing.T) {
	f := newJobFixture(t)
	if _, _, err := f.s.SubmitBackupReminderJob(f.ctx, f.a, "kept", domain.JobPriorityManual, 7, f.policy); err != nil {
		t.Fatal(err)
	}
	refuseRetainedDowngrade(t, f, "maintenance_jobs", "a retained maintenance job was downgraded")
}

func TestMaintenanceJobsMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	item := tmdbItem(t, f, 1)
	version := downgradeAboveMigration(t, f, "maintenance_jobs")
	dsn := f.s.Pool.Config().ConnString()
	previous, dirty, err := Migrate(f.ctx, dsn, "down")
	if err != nil || dirty || previous >= version {
		t.Fatalf("downgrade: version=%d dirty=%t error=%v", previous, dirty, err)
	}
	var exists bool
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('subtitle_cache_cleanup_job_state') IS NOT NULL OR to_regclass('backup_reminder_job_state') IS NOT NULL OR to_regclass('external_metadata_removal_job_state') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatal("downgrade kept maintenance tables", err)
	}
	for _, kind := range []string{"subtitle_cache_cleanup", "backup_reminder", "external_metadata_removal"} {
		if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO job_metric_totals(kind,priority) VALUES($1,'manual')`, kind); itemImageSQLState(err) != "23514" {
			t.Fatal("schema below maintenance accepted the kind", kind, err)
		}
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || v != SchemaVersion {
		t.Fatalf("upgrade: version=%d dirty=%t error=%v", v, dirty, err)
	}
	if tmdbFieldCount(t, f, item) != 4 {
		t.Fatal("round trip lost provider fields")
	}
	job := submitRemoval(t, f, "after-round-trip", false)
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET error_code='backup_reminder_failed' WHERE id=$1::uuid`, job.ID); itemImageSQLState(err) != "23514" {
		t.Fatal("another kind accepted the reminder failure code", err)
	}
}
