package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

const nfoApplyTestItems = 120

func nfoApplyTestMetadata(t *testing.T, f jobFixture) *app.Metadata {
	t.Helper()
	reader, err := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	local, err := app.NewLocalMetadata(f.s)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := local.WithNFOItemFields(reader)
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}

// nfoApplyLibrary fills a read-only library with n movies. By index modulo 6:
// 0 and 5 have a valid NFO, 1 has none, 2 has an invalid one, 3 has a valid
// NFO but a manual title and a locked overview, and 4 was applied before.
func nfoApplyLibrary(t *testing.T, f jobFixture, metadata *app.Metadata, library string, n int) []string {
	t.Helper()
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE library_id=$1::uuid`, library).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='read-only',nfo_generation=2 WHERE id=$1::uuid`, library); err != nil {
		t.Fatal(err)
	}
	items := make([]string, n)
	for i := range items {
		var rootID string
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM library_roots WHERE library_id=$1::uuid`, library).Scan(&rootID); err != nil {
			t.Fatal(err)
		}
		if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Original title','Movie') RETURNING id::text`, library).Scan(&items[i]); err != nil {
			t.Fatal(err)
		}
		media := fmt.Sprintf("m%03d.mkv", i)
		if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska')`, items[i], library, rootID, media); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, media), []byte("synthetic media"), 0o600); err != nil {
			t.Fatal(err)
		}
		document := ""
		switch i % 6 {
		case 1:
		case 2:
			document = "<movie><title>broken"
		default:
			document = fmt.Sprintf("<movie><title>NFO title %d</title><plot>NFO plot %d</plot><premiered>2024-02-%02d</premiered></movie>", i, i, i%28+1)
		}
		if document != "" {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("m%03d.nfo", i)), []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		switch i % 6 {
		case 3:
			title, plot, locked := fmt.Sprintf("Manual %d", i), "Locked plot", true
			if _, err := f.s.UpdateItemMetadata(f.ctx, f.a, items[i], 1, []domain.ItemMetadataPatch{{Field: "title", Value: &title}, {Field: "overview", Value: &plot, Locked: &locked}}); err != nil {
				t.Fatal(err)
			}
		case 4:
			if _, err := metadata.ApplyNFO(f.ctx, f.a, items[i], 1, true); err != nil {
				t.Fatal("pre-apply", err)
			}
		}
	}
	return items
}

// nfoApplySnapshot renders everything a metadata apply may change.
func nfoApplySnapshot(t *testing.T, f jobFixture, items []string) []string {
	t.Helper()
	out := make([]string, len(items))
	for i, item := range items {
		value, err := f.s.ItemMetadata(f.ctx, f.a, item)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		var title string
		if err = f.s.Pool.QueryRow(f.ctx, `SELECT title FROM items WHERE id=$1::uuid`, item).Scan(&title); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "rev=%d title=%q", value.Revision, title)
		for _, field := range value.Fields {
			fmt.Fprintf(&b, "|%s=%q/%s/%t", field.Field, field.Value, field.Source, field.Locked)
		}
		for _, fact := range value.Facts {
			fmt.Fprintf(&b, "|%s=%s/%s/%t", fact.Field, fact.Value, fact.Source, fact.Locked)
		}
		out[i] = b.String()
	}
	return out
}

// nfoApplyOracle builds the same library again and applies every item with
// the single-item path.
func nfoApplyOracle(t *testing.T, f jobFixture, metadata *app.Metadata, n int) []string {
	t.Helper()
	registration, err := f.s.RegisterLibrary(f.ctx, "oracle", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := nfoApplyLibrary(t, f, metadata, registration.Library.ID, n)
	for _, item := range items {
		value, err := f.s.ItemMetadata(f.ctx, f.a, item)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = metadata.ApplyNFO(f.ctx, f.a, item, value.Revision, true); err != nil && !errors.Is(err, domain.ErrMetadataUnavailable) {
			t.Fatal(err)
		}
	}
	return nfoApplySnapshot(t, f, items)
}

// hookedNFOApply observes checkpoints of a run.
type hookedNFOApply struct {
	*Store
	records int
	after   func(n int, lease domain.JobLease)
}

func (h *hookedNFOApply) RecordNFOApplyBatch(ctx context.Context, lease domain.JobLease, cursor string, counts domain.NFOApplyCounts) error {
	err := h.Store.RecordNFOApplyBatch(ctx, lease, cursor, counts)
	h.records++
	if err == nil && h.after != nil {
		h.after(h.records, lease)
	}
	return err
}

func startNFOApplyRunner(t *testing.T, f jobFixture, metadata *app.Metadata, repository app.NFOApplyExecutionRepository) func() {
	t.Helper()
	worker, err := app.NewNFOApplyWorker(metadata, repository, 0)
	if err != nil {
		t.Fatal("worker", err)
	}
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval = 1, 100*time.Millisecond
	opts.NFOApply = &jobs.NFOApplyOptions{Worker: worker, Repository: f.s}
	runner, err := jobs.New(f.s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal("new", err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal("start", err)
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}
}

func waitNFOApplyJob(t *testing.T, f jobFixture, id string) domain.Job {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		job, err := f.s.GetJob(f.ctx, f.a, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State != domain.JobQueued && job.State != domain.JobRunning {
			return job
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("nfo apply job did not finish")
	return domain.Job{}
}

func submitNFOApply(t *testing.T, f jobFixture, library, key string) domain.Job {
	t.Helper()
	job, replay, err := f.s.SubmitNFOApplyJob(f.ctx, f.a, library, key, domain.JobPriorityManual, f.policy)
	if err != nil || replay || job.Kind != domain.JobNFOApply || job.State != domain.JobQueued {
		t.Fatal("submit nfo apply", err, replay, job)
	}
	return job
}

func requireNFOApplyCounts(t *testing.T, f jobFixture, job string, want domain.NFOApplyCounts, cursor bool) domain.NFOApplyJobState {
	t.Helper()
	state, err := f.s.GetNFOApplyState(f.ctx, f.a, job)
	if err != nil {
		t.Fatal(err)
	}
	got := domain.NFOApplyCounts{Applied: state.Applied, Unchanged: state.Unchanged, Skipped: state.Skipped, Failed: state.Failed}
	if got != want || (state.CursorItemID != nil) != cursor || state.JobID != job {
		t.Fatalf("counters %+v cursor=%v, want %+v", state, state.CursorItemID, want)
	}
	return state
}

// nfoApplyWant is what nfoApplyLibrary's mix produces for n items.
func nfoApplyWant(n int) domain.NFOApplyCounts {
	var c domain.NFOApplyCounts
	for i := 0; i < n; i++ {
		switch i % 6 {
		case 0, 3, 5:
			c.Applied++
		case 4:
			c.Unchanged++
		default:
			c.Skipped++
		}
	}
	return c
}

func TestNFOApplyJobAppliesLibraryLikeSingleItemApply(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoApplyLibrary(t, f, metadata, f.registration.Library.ID, nfoApplyTestItems)
	want := nfoApplyOracle(t, f, metadata, nfoApplyTestItems)
	job := submitNFOApply(t, f, f.registration.Library.ID, "apply-all")
	stop := startNFOApplyRunner(t, f, metadata, f.s)
	defer stop()
	done := waitNFOApplyJob(t, f, job.ID)
	if done.State != domain.JobSucceeded || done.ErrorCode != "" {
		t.Fatal("job did not succeed", done)
	}
	counts := nfoApplyWant(nfoApplyTestItems)
	state := requireNFOApplyCounts(t, f, job.ID, counts, true)
	if state.LibraryID != f.registration.Library.ID {
		t.Fatal("state names another library")
	}
	if done.Files != int64(counts.Applied+counts.Unchanged) || done.Skipped != int64(counts.Skipped+counts.Failed) {
		t.Fatalf("job progress files=%d skipped=%d", done.Files, done.Skipped)
	}
	var last string
	for _, item := range items {
		if item > last {
			last = item
		}
	}
	if *state.CursorItemID != last {
		t.Fatal("cursor is not the last item")
	}
	got := nfoApplySnapshot(t, f, items)
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("item %d differs from single-item apply:\n job: %s\n one: %s", i, got[i], want[i])
		}
	}
	// Manual and locked values stayed.
	if !strings.Contains(got[3], `title="Manual 3"`) || !strings.Contains(got[3], `Locked plot`) {
		t.Fatal("manual or locked field overwritten", got[3])
	}
	var audited int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='nfo.apply_submitted' AND target_id=$1`, job.ID).Scan(&audited); err != nil || audited != 1 {
		t.Fatal("submission not audited", err, audited)
	}
}

func TestNFOApplyJobCancellationKeepsAppliedItems(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoApplyLibrary(t, f, metadata, f.registration.Library.ID, nfoApplyTestItems)
	before := nfoApplySnapshot(t, f, items)
	job := submitNFOApply(t, f, f.registration.Library.ID, "cancel-me")
	repo := &hookedNFOApply{Store: f.s, after: func(n int, lease domain.JobLease) {
		if n == 1 {
			if _, err := f.s.CancelJob(f.ctx, f.a, lease.Job.ID); err != nil {
				t.Error(err)
			}
		}
	}}
	stop := startNFOApplyRunner(t, f, metadata, repo)
	defer stop()
	done := waitNFOApplyJob(t, f, job.ID)
	if done.State != domain.JobCancelled {
		t.Fatal("job not cancelled", done)
	}
	state, err := f.s.GetNFOApplyState(f.ctx, f.a, job.ID)
	if err != nil || state.CursorItemID == nil {
		t.Fatal("no checkpoint", err)
	}
	finished := 0
	for _, item := range items {
		if item <= *state.CursorItemID {
			finished++
		}
	}
	if total := state.Applied + state.Unchanged + state.Skipped + state.Failed; total != finished || finished < domain.NFOApplyBatchSize || finished >= len(items) {
		t.Fatalf("counters %+v do not match %d finished items", state, finished)
	}
	after := nfoApplySnapshot(t, f, items)
	for i, item := range items {
		if item > *state.CursorItemID && after[i] != before[i] {
			t.Fatal("item after the cursor changed", i)
		}
		if item <= *state.CursorItemID && i%6 == 0 && after[i] == before[i] {
			t.Fatal("applied item reverted", i)
		}
	}
}

func TestNFOApplyJobResumesAfterLeaseLossWithoutRepeatingItems(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoApplyLibrary(t, f, metadata, f.registration.Library.ID, nfoApplyTestItems)
	want := nfoApplyOracle(t, f, metadata, nfoApplyTestItems)
	job := submitNFOApply(t, f, f.registration.Library.ID, "resume-me")
	repo := &hookedNFOApply{Store: f.s, after: func(n int, lease domain.JobLease) {
		if n == 1 {
			// The owner stalls past its lease right after the first checkpoint.
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, lease.Job.ID); err != nil {
				t.Error(err)
			}
		}
	}}
	stop := startNFOApplyRunner(t, f, metadata, repo)
	defer stop()
	done := waitNFOApplyJob(t, f, job.ID)
	if done.State != domain.JobSucceeded || done.Attempts != 2 {
		t.Fatal("job did not resume once and succeed", done)
	}
	requireNFOApplyCounts(t, f, job.ID, nfoApplyWant(nfoApplyTestItems), true)
	got := nfoApplySnapshot(t, f, items)
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("item %d differs after resume:\n job: %s\n one: %s", i, got[i], want[i])
		}
	}
}

func TestNFOApplyJobAdmission(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	if _, _, err := f.s.SubmitNFOApplyJob(f.ctx, f.a, library, "off", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("library without NFO policy accepted", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='read-only',nfo_generation=2 WHERE id=$1::uuid`, library); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.SubmitNFOApplyJob(f.ctx, f.a, "00000000-0000-4000-8000-000000000001", "none", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown library accepted", err)
	}
	if _, _, err := f.s.SubmitNFOApplyJob(f.ctx, f.a, library, "bad priority", "urgent", f.policy); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("bad priority accepted", err)
	}
	first := submitNFOApply(t, f, library, "once")
	var cursor *string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT cursor_item_id::text FROM nfo_apply_job_state WHERE job_id=$1::uuid`, first.ID).Scan(&cursor); err != nil || cursor != nil {
		t.Fatal("state row missing or has a cursor", err)
	}
	replay, replayed, err := f.s.SubmitNFOApplyJob(f.ctx, f.a, library, "once", domain.JobPriorityManual, f.policy)
	if err != nil || !replayed || replay.ID != first.ID {
		t.Fatal("replay", err, replayed, replay)
	}
	if _, _, err = f.s.SubmitNFOApplyJob(f.ctx, f.a, library, "once", domain.JobPriorityBackground, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("same key with another priority accepted", err)
	}
	if _, _, err = f.s.SubmitNFOApplyJob(f.ctx, f.a, library, "twice", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("second active job accepted", err)
	}
	limited := f.policy
	limited.QueueLimit = 1
	other, err := f.s.RegisterLibrary(f.ctx, "other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='read-only',nfo_generation=2 WHERE id=$1::uuid`, other.Library.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitNFOApplyJob(f.ctx, f.a, other.Library.ID, "full", domain.JobPriorityManual, limited); !errors.Is(err, domain.ErrJobQueueFull) {
		t.Fatal("queue limit ignored", err)
	}
	// Another job kind has no apply state.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',finished_at=clock_timestamp() WHERE id=$1::uuid`, first.ID); err != nil {
		t.Fatal(err)
	}
	scanJob, _, err := f.s.SubmitJob(f.ctx, f.a, library, "other-scan", domain.JobPriorityManual, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GetNFOApplyState(f.ctx, f.a, scanJob.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("scan job has apply state", err)
	}
	if _, err = f.s.GetNFOApplyState(f.ctx, f.a, first.ID); err != nil {
		t.Fatal("apply state of a cancelled job", err)
	}
	if _, err = f.s.GetNFOApplyState(f.ctx, domain.Actor{UserID: f.a.UserID, SessionID: "00000000-0000-4000-8000-000000000002"}, first.ID); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("state read without a session", err)
	}
}

func TestNFOApplyMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	jobMetricMigration(t, f, "down", downgradeAboveMigration(t, f, "nfo_apply_jobs")-1)
	var removed bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('nfo_apply_job_state') IS NULL AND NOT EXISTS(SELECT 1 FROM job_metric_totals WHERE kind='nfo_apply') AND NOT EXISTS(SELECT 1 FROM job_metric_buckets WHERE kind='nfo_apply')`).Scan(&removed); err != nil || !removed {
		t.Fatal("empty downgrade left apply objects", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO jobs(library_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1::uuid,'k','nfo_apply','manual',1,1,100,1,1,1,1)`, f.registration.Library.ID); err == nil {
		t.Fatal("older schema accepts nfo_apply jobs")
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("round-trip schema is not ready", err)
	}
}

func TestNFOApplyMigrationRefusesRetainedData(t *testing.T) {
	f := newJobFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='read-only',nfo_generation=2 WHERE id=$1::uuid`, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	job := submitNFOApply(t, f, f.registration.Library.ID, "retained")
	want := downgradeAboveMigration(t, f, "nfo_apply_jobs")
	nfoMigrationDenied(t, f, "000088_nfo_apply_jobs.down.sql")
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("retained apply job downgraded")
	}
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != want-1 || !dirty {
		t.Fatal("refused downgrade lost dirty status", version, dirty, err)
	}
	var kept int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_apply_job_state WHERE job_id=$1::uuid`, job.ID).Scan(&kept); err != nil || kept != 1 {
		t.Fatal("refused downgrade removed the state row", err)
	}
}
