package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

// 17 groups of eight items: 34 need a write, which is 16 + 16 + 2 entries.
const nfoExportTestItems = 136

// nfoExportEnable gives the library read-write NFO policy.
func nfoExportEnable(t *testing.T, f jobFixture, library string) {
	t.Helper()
	policy, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, library)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, library, "export-read-"+library, policy.Generation, domain.NFOModeReadOnly); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetNFOWritePolicy(f.ctx, f.a, library, true); err != nil {
		t.Fatal(err)
	}
}

type nfoExportItem struct {
	id   string
	nfo  string
	path string
	body string
}

// nfoExportLibrary fills a read-write library with n movies. By index modulo 8:
// 0 has a valid NFO and a manual title and overview that differ (written), 1 was
// applied from its NFO (unchanged), 2 has no NFO (skipped_missing), 3 an
// invalid one (skipped_invalid), 4 a valid NFO but no stored value (unchanged),
// 5 a manual tagline the NFO lacks (written, the element is added), 6 a locked
// NFO with a differing manual title (skipped_other) and 7 a manual title equal
// to the NFO's (unchanged).
func nfoExportLibrary(t *testing.T, f jobFixture, metadata *app.Metadata, library string, n int) []nfoExportItem {
	t.Helper()
	var root, rootID string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path,id::text FROM library_roots WHERE library_id=$1::uuid`, library).Scan(&root, &rootID); err != nil {
		t.Fatal(err)
	}
	nfoExportEnable(t, f, library)
	items := make([]nfoExportItem, n)
	for i := range items {
		it := &items[i]
		if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Scanned name','Movie') RETURNING id::text`, library).Scan(&it.id); err != nil {
			t.Fatal(err)
		}
		media := fmt.Sprintf("m%03d.mkv", i)
		if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska')`, it.id, library, rootID, media); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, media), []byte("synthetic media"), 0o600); err != nil {
			t.Fatal(err)
		}
		it.nfo = fmt.Sprintf("m%03d.nfo", i)
		it.path = filepath.Join(root, it.nfo)
		kind := i % 8
		switch kind {
		case 2:
		case 3:
			it.body = "<movie><title>broken"
		case 6:
			it.body = fmt.Sprintf("<movie>\n  <title>NFO title %d</title>\n  <lockdata>true</lockdata>\n</movie>\n", i)
		default:
			it.body = fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<movie>\n  <!-- keep %d -->\n  <title>NFO title %d</title>\n  <plot>NFO plot %d</plot>\n  <customtag attr=\"v%d\">x%d</customtag>\n</movie>\n", i, i, i, i, i)
		}
		if it.body != "" {
			if err := os.WriteFile(it.path, []byte(it.body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		patch := func(fields ...domain.ItemMetadataPatch) {
			if _, err := f.s.UpdateItemMetadata(f.ctx, f.a, it.id, 1, fields); err != nil {
				t.Fatal(err)
			}
		}
		text := func(field, value string) domain.ItemMetadataPatch {
			return domain.ItemMetadataPatch{Field: field, Value: &value}
		}
		switch kind {
		case 0:
			patch(text("title", fmt.Sprintf("DB title %d", i)), text("overview", fmt.Sprintf("DB plot %d", i)))
		case 1:
			if _, err := metadata.ApplyNFO(f.ctx, f.a, it.id, 1, true); err != nil {
				t.Fatal("pre-apply", err)
			}
		case 5:
			patch(text("tagline", fmt.Sprintf("DB tagline %d", i)))
		case 6:
			patch(text("title", fmt.Sprintf("DB title %d", i)))
		case 7:
			patch(text("title", fmt.Sprintf("NFO title %d", i)))
		}
	}
	return items
}

// nfoExportWant is what nfoExportLibrary's mix produces for n items.
func nfoExportWant(n int) domain.NFOExportCounts {
	var c domain.NFOExportCounts
	for i := 0; i < n; i++ {
		switch i % 8 {
		case 0, 5:
			c.Written++
		case 1, 4, 7:
			c.Unchanged++
		case 2:
			c.SkippedMissing++
		case 3:
			c.SkippedInvalid++
		case 6:
			c.SkippedOther++
		}
	}
	return c
}

func nfoExportCounts(s domain.NFOExportJobState) domain.NFOExportCounts {
	return domain.NFOExportCounts{Written: s.Written, Unchanged: s.Unchanged, SkippedMissing: s.SkippedMissing, SkippedInvalid: s.SkippedInvalid, SkippedOther: s.SkippedOther, Failed: s.Failed}
}

// hookedNFOExport observes and interrupts the steps of a run.
type hookedNFOExport struct {
	*Store
	steps         int
	beforeSubmit  func(n int, lease domain.JobLease)
	afterSubmit   func(n int, lease domain.JobLease)
	afterNextStep func(n int, lease domain.JobLease, step domain.NFOExportStep)
	submits       int
}

func (h *hookedNFOExport) NextNFOExportStep(ctx context.Context, lease domain.JobLease, limit int) (domain.NFOExportStep, error) {
	step, err := h.Store.NextNFOExportStep(ctx, lease, limit)
	h.steps++
	if err == nil && h.afterNextStep != nil {
		h.afterNextStep(h.steps, lease, step)
	}
	return step, err
}

func (h *hookedNFOExport) SubmitNFOExportChunk(ctx context.Context, lease domain.JobLease, actor domain.Actor, chunk int, cursor string, counts domain.NFOExportCounts, preparations []string) error {
	h.submits++
	if h.beforeSubmit != nil {
		h.beforeSubmit(h.submits, lease)
	}
	err := h.Store.SubmitNFOExportChunk(ctx, lease, actor, chunk, cursor, counts, preparations)
	if err == nil && h.afterSubmit != nil {
		h.afterSubmit(h.submits, lease)
	}
	return err
}

// startNFOExportRunner runs one worker that claims export, write and apply
// jobs, so an export can only finish if it yields its only worker.
func startNFOExportRunner(t *testing.T, f jobFixture, metadata *app.Metadata, repository app.NFOExportExecutionRepository) func() {
	t.Helper()
	return startNFOExportRunnerRecovering(t, f, metadata, repository, 10*time.Minute)
}

// startNFOExportRunnerRecovering is startNFOExportRunner with an explicit pace
// for the background write recovery and cleanup pass. That pass removes the
// retained files of finished children from the NFO directory, which the export
// reads item by item, and a change there during a read counts the item as
// failed after one retry. startNFOExportRunner therefore keeps the pass out of
// the way with the longest allowed interval; only tests that need a stopped
// write recovered while they run ask for a short one.
func startNFOExportRunnerRecovering(t *testing.T, f jobFixture, metadata *app.Metadata, repository app.NFOExportExecutionRepository, recovery time.Duration) func() {
	t.Helper()
	budget, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 64})
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := nfo.NewWritePreparer(budget)
	if err != nil {
		t.Fatal(err)
	}
	preparations, err := app.NewNFOWritePreparations(f.s, preparer)
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := app.NewNFOExportWorker(metadata, preparations, repository, 0)
	if err != nil {
		t.Fatal("export worker", err)
	}
	applier, err := app.NewNFOApplyWorker(metadata, f.s, 0)
	if err != nil {
		t.Fatal("apply worker", err)
	}
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval = 1, 100*time.Millisecond
	opts.NFOWrite = &jobs.NFOWriteOptions{Repository: f.s, Committer: nfoWriteTestWriter(t), RecoveryInterval: recovery}
	opts.NFOExport = &jobs.NFOExportOptions{Worker: exporter, Repository: f.s}
	opts.NFOApply = &jobs.NFOApplyOptions{Worker: applier, Repository: f.s}
	runner, err := jobs.New(f.s, idleInventoryScanner{}, opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal("new", err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal("start", err)
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}
}

func submitNFOExport(t *testing.T, f jobFixture, library, key string) domain.Job {
	t.Helper()
	job, replay, err := f.s.SubmitNFOExportJob(f.ctx, f.a, library, key, domain.JobPriorityManual, f.policy)
	if err != nil || replay || job.Kind != domain.JobNFOExport || job.State != domain.JobQueued {
		t.Fatal("submit nfo export", err, replay, job)
	}
	return job
}

func waitNFOExportJob(t *testing.T, f jobFixture, id string) domain.Job {
	t.Helper()
	deadline := time.Now().Add(150 * time.Second)
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
	t.Fatal("nfo export job did not finish")
	return domain.Job{}
}

type nfoExportChild struct {
	ID    string
	State string
	Key   string
}

func nfoExportChildren(t *testing.T, f jobFixture, job string) []nfoExportChild {
	t.Helper()
	rows, err := f.s.Pool.Query(f.ctx, `SELECT id::text,state,idempotency_key FROM jobs WHERE export_job_id=$1::uuid AND kind='nfo_write' ORDER BY created_at,id`, job)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []nfoExportChild
	for rows.Next() {
		var c nfoExportChild
		if err := rows.Scan(&c.ID, &c.State, &c.Key); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func requireNFOExportCounts(t *testing.T, f jobFixture, job string, want domain.NFOExportCounts) domain.NFOExportJobState {
	t.Helper()
	state, err := f.s.GetNFOExportState(f.ctx, f.a, job)
	if err != nil {
		t.Fatal(err)
	}
	if got := nfoExportCounts(state); got != want || state.JobID != job {
		t.Fatalf("counters %+v, want %+v", state, want)
	}
	return state
}

var nfoExportUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// nfoExportFiles renders every NFO and backup of the items, with generated
// UUIDs masked so two libraries compare equal.
func nfoExportFiles(t *testing.T, items []nfoExportItem) []string {
	t.Helper()
	out := make([]string, len(items))
	for i, it := range items {
		var b strings.Builder
		for _, name := range []string{it.path, it.path + ".jelee.bak"} {
			data, err := os.ReadFile(name)
			switch {
			case err == nil:
				fmt.Fprintf(&b, "[%s]%s", filepath.Ext(name), nfoExportUUID.ReplaceAllString(string(data), "UUID"))
			case errors.Is(err, os.ErrNotExist):
				b.WriteString("[absent]")
			default:
				t.Fatal(err)
			}
		}
		out[i] = b.String()
	}
	return out
}

// requireNFOExportFiles checks every file of the mix against the database.
func requireNFOExportFiles(t *testing.T, items []nfoExportItem, upTo string) {
	t.Helper()
	for i, it := range items {
		if upTo != "" && it.id > upTo {
			continue
		}
		data, err := os.ReadFile(it.path)
		backup, backupErr := os.ReadFile(it.path + ".jelee.bak")
		switch i % 8 {
		case 0:
			got := string(data)
			if err != nil || !strings.Contains(got, fmt.Sprintf("<title>DB title %d</title>", i)) || !strings.Contains(got, fmt.Sprintf("<plot>DB plot %d</plot>", i)) ||
				!strings.Contains(got, fmt.Sprintf("<!-- keep %d -->", i)) || !strings.Contains(got, fmt.Sprintf(`<customtag attr="v%d">x%d</customtag>`, i, i)) {
				t.Fatalf("item %d was not exported with its unknown content kept: %q %v", i, got, err)
			}
			if backupErr != nil || string(backup) != it.body {
				t.Fatalf("item %d has no backup of the original", i)
			}
		case 5:
			got := string(data)
			if err != nil || !strings.Contains(got, fmt.Sprintf("<tagline>DB tagline %d</tagline>", i)) || !strings.Contains(got, fmt.Sprintf("<title>NFO title %d</title>", i)) || !strings.Contains(got, fmt.Sprintf("<!-- keep %d -->", i)) {
				t.Fatalf("item %d did not get its tagline: %q %v", i, got, err)
			}
			if backupErr != nil || string(backup) != it.body {
				t.Fatalf("item %d has no backup of the original", i)
			}
		case 2:
			if !errors.Is(err, os.ErrNotExist) || !errors.Is(backupErr, os.ErrNotExist) {
				t.Fatalf("item %d without an NFO got one", i)
			}
		default:
			if err != nil || string(data) != it.body || !errors.Is(backupErr, os.ErrNotExist) {
				t.Fatalf("item %d was touched: %q %v", i, data, backupErr)
			}
		}
	}
}

func requireNFOExportClean(t *testing.T, f jobFixture, job string) {
	t.Helper()
	var preparations int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_preparations WHERE starts_with(idempotency_key,$1)`, "nfo-export:"+job+":").Scan(&preparations); err != nil || preparations != 0 {
		t.Fatal("export preparations were kept", preparations, err)
	}
}

func TestNFOExportJobWritesDatabaseValuesIntoExistingNFOs(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoExportLibrary(t, f, metadata, f.registration.Library.ID, nfoExportTestItems)
	job := submitNFOExport(t, f, f.registration.Library.ID, "export-all")
	stop := startNFOExportRunner(t, f, metadata, f.s)
	defer stop()
	done := waitNFOExportJob(t, f, job.ID)
	if done.State != domain.JobSucceeded || done.ErrorCode != "" {
		t.Fatal("job did not succeed", done)
	}
	want := nfoExportWant(nfoExportTestItems)
	state := requireNFOExportCounts(t, f, job.ID, want)
	// A chunk ends at 16 entries or at the end of a 50 item read, so the
	// number of children depends on how the random item IDs fall; 34 entries
	// need at least three.
	if state.Chunks < 3 || state.Chunks > 6 || state.ChildJobID != nil || state.CursorItemID == nil || state.LibraryID != f.registration.Library.ID {
		t.Fatalf("state after the run: %+v", state)
	}
	if done.Files != int64(want.Written+want.Unchanged) || done.Skipped != int64(want.SkippedMissing+want.SkippedInvalid+want.SkippedOther+want.Failed) {
		t.Fatalf("job progress files=%d skipped=%d", done.Files, done.Skipped)
	}
	// Each yield gave the claim back; only the last one stays.
	if done.Attempts != 1 {
		t.Fatal("yielding consumed attempts", done.Attempts)
	}
	children := nfoExportChildren(t, f, job.ID)
	if len(children) != state.Chunks {
		t.Fatal("children", children)
	}
	var entries int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_entries e JOIN jobs j ON j.id=e.job_id WHERE j.export_job_id=$1::uuid`, job.ID).Scan(&entries); err != nil || entries != want.Written {
		t.Fatal("entries of the children", entries, err)
	}
	for i, child := range children {
		if child.State != domain.JobSucceeded || child.Key != domain.NFOExportChildKey(job.ID, i+1) {
			t.Fatal("child", i, child)
		}
	}
	requireNFOExportFiles(t, items, "")
	requireNFOExportClean(t, f, job.ID)
	var audited, childAudited int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FILTER(WHERE event='nfo.export_submitted' AND target_id=$1),count(*) FILTER(WHERE event='nfo.write_submitted') FROM audit_logs`, job.ID).Scan(&audited, &childAudited); err != nil || audited != 1 || childAudited != state.Chunks {
		t.Fatal("submissions not audited", err, audited, childAudited)
	}
	// A second export has nothing left to write.
	second := submitNFOExport(t, f, f.registration.Library.ID, "export-again")
	if finished := waitNFOExportJob(t, f, second.ID); finished.State != domain.JobSucceeded {
		t.Fatal("second export", finished)
	}
	again := requireNFOExportCounts(t, f, second.ID, domain.NFOExportCounts{Unchanged: want.Written + want.Unchanged, SkippedMissing: want.SkippedMissing, SkippedInvalid: want.SkippedInvalid, SkippedOther: want.SkippedOther})
	if again.Chunks != 0 || len(nfoExportChildren(t, f, second.ID)) != 0 {
		t.Fatal("an export of exported files submitted writes", again)
	}
	requireNFOExportFiles(t, items, "")
}

// nfoExportSnapshot renders the stored metadata of the items without
// revisions, which an apply advances.
func nfoExportSnapshot(t *testing.T, f jobFixture, items []nfoExportItem) (out []string, generated []int) {
	t.Helper()
	out, generated = make([]string, len(items)), make([]int, len(items))
	for i, it := range items {
		value, err := f.s.ItemMetadata(f.ctx, f.a, it.id)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, field := range value.Fields {
			fmt.Fprintf(&b, "|%s=%q/%s/%t", field.Field, field.Value, field.Source, field.Locked)
		}
		for _, fact := range value.Facts {
			if fact.Field == "uniqueIds" && len(fact.Value) > 0 && string(fact.Value) != "null" {
				// Every controlled write adds an identifier of its own
				// (type jelee) to the NFO; applying the NFO stores it.
				var ids []struct{ Type, Value string }
				if err := json.Unmarshal(fact.Value, &ids); err != nil {
					t.Fatalf("item %d uniqueIds %x len %d: %v", i, []byte(fact.Value), len(fact.Value), err)
				}
				var kept []string
				for _, id := range ids {
					if id.Type == "jelee" {
						generated[i]++
					} else {
						kept = append(kept, id.Type+":"+id.Value)
					}
				}
				if len(kept) == 0 {
					continue
				}
				fmt.Fprintf(&b, "|uniqueIds=%v/%s/%t", kept, fact.Source, fact.Locked)
				continue
			}
			fmt.Fprintf(&b, "|%s=%s/%s/%t", fact.Field, fact.Value, fact.Source, fact.Locked)
		}
		out[i] = b.String()
	}
	return out, generated
}

func TestNFOExportThenApplyRoundTripsMetadata(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoExportLibrary(t, f, metadata, f.registration.Library.ID, 48)
	stop := startNFOExportRunner(t, f, metadata, f.s)
	defer stop()
	// Import first: only values the database already holds can come back
	// unchanged (an import of a never imported NFO adds its values).
	imported := submitNFOApply(t, f, f.registration.Library.ID, "round-trip-import")
	if done := waitNFOApplyJob(t, f, imported.ID); done.State != domain.JobSucceeded {
		t.Fatal("import", done)
	}
	before, beforeIDs := nfoExportSnapshot(t, f, items)
	job := submitNFOExport(t, f, f.registration.Library.ID, "round-trip")
	if done := waitNFOExportJob(t, f, job.ID); done.State != domain.JobSucceeded {
		t.Fatal("export", done)
	}
	requireNFOExportCounts(t, f, job.ID, nfoExportWant(48))
	requireNFOExportFiles(t, items, "")
	applied := submitNFOApply(t, f, f.registration.Library.ID, "round-trip-apply")
	if done := waitNFOApplyJob(t, f, applied.ID); done.State != domain.JobSucceeded {
		t.Fatal("apply", done)
	}
	after, afterIDs := nfoExportSnapshot(t, f, items)
	for i := range after {
		if after[i] != before[i] {
			t.Fatalf("item %d differs after export and apply:\n before: %s\n after:  %s", i, before[i], after[i])
		}
		// The one addition: the identifier the write generated, only for written items.
		wantID := 0
		if i%8 == 0 || i%8 == 5 {
			wantID = 1
		}
		if beforeIDs[i] != 0 || afterIDs[i] != wantID {
			t.Fatalf("item %d: generated identifiers %d before, %d after, want %d after", i, beforeIDs[i], afterIDs[i], wantID)
		}
	}
}

func TestNFOExportJobCancellation(t *testing.T) {
	t.Run("child cancelled before it ran", func(t *testing.T) {
		f := newJobFixture(t)
		metadata := nfoApplyTestMetadata(t, f)
		items := nfoExportLibrary(t, f, metadata, f.registration.Library.ID, nfoExportTestItems)
		before := nfoExportFiles(t, items)
		job := submitNFOExport(t, f, f.registration.Library.ID, "cancel-early")
		repo := &hookedNFOExport{Store: f.s, afterSubmit: func(n int, lease domain.JobLease) {
			if n == 1 {
				if _, err := f.s.CancelJob(f.ctx, f.a, lease.Job.ID); err != nil {
					t.Error(err)
				}
			}
		}}
		stop := startNFOExportRunner(t, f, metadata, repo)
		defer stop()
		done := waitNFOExportJob(t, f, job.ID)
		if done.State != domain.JobCancelled {
			t.Fatal("job not cancelled", done)
		}
		children := nfoExportChildren(t, f, job.ID)
		if len(children) != 1 || children[0].State != domain.JobCancelled {
			t.Fatal("later chunks were submitted or the child ran", children)
		}
		state, err := f.s.GetNFOExportState(f.ctx, f.a, job.ID)
		var entries int
		if err == nil {
			err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_entries WHERE job_id=$1::uuid`, children[0].ID).Scan(&entries)
		}
		// The entries the cancelled child never wrote are failed ones.
		if err != nil || state.Chunks != 1 || state.ChildJobID != nil || state.Written != 0 || entries < 1 || state.Failed != entries {
			t.Fatalf("state after cancellation: %+v %d %v", state, entries, err)
		}
		if after := nfoExportFiles(t, items); strings.Join(after, "\x00") != strings.Join(before, "\x00") {
			t.Fatal("a file changed although the only child was cancelled")
		}
		requireNFOExportClean(t, f, job.ID)
	})
	t.Run("cancelled between chunks", func(t *testing.T) {
		f := newJobFixture(t)
		metadata := nfoApplyTestMetadata(t, f)
		items := nfoExportLibrary(t, f, metadata, f.registration.Library.ID, nfoExportTestItems)
		job := submitNFOExport(t, f, f.registration.Library.ID, "cancel-late")
		repo := &hookedNFOExport{Store: f.s}
		repo.afterNextStep = func(_ int, lease domain.JobLease, step domain.NFOExportStep) {
			// The first child has finished and is folded in: stop before the next chunk.
			if !step.Waiting && step.Chunk == 1 {
				if _, err := f.s.CancelJob(f.ctx, f.a, lease.Job.ID); err != nil {
					t.Error(err)
				}
			}
		}
		stop := startNFOExportRunner(t, f, metadata, repo)
		defer stop()
		done := waitNFOExportJob(t, f, job.ID)
		if done.State != domain.JobCancelled {
			t.Fatal("job not cancelled", done)
		}
		children := nfoExportChildren(t, f, job.ID)
		if len(children) != 1 || children[0].State != domain.JobSucceeded {
			t.Fatal("expected one finished child", children)
		}
		state, err := f.s.GetNFOExportState(f.ctx, f.a, job.ID)
		var entries int
		if err == nil {
			err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_entries WHERE job_id=$1::uuid`, children[0].ID).Scan(&entries)
		}
		if err != nil || state.CursorItemID == nil || state.Chunks != 1 || entries < 1 || state.Written != entries || state.Failed != 0 || state.ChildJobID != nil {
			t.Fatalf("state after cancellation: %+v %d %v", state, entries, err)
		}
		// The first chunk is written; nothing after its cursor is touched.
		requireNFOExportFiles(t, items, *state.CursorItemID)
		for i, it := range items {
			if it.id > *state.CursorItemID && i%8 != 2 {
				if data, err := os.ReadFile(it.path); err != nil || string(data) != it.body {
					t.Fatalf("item %d after the cursor changed", i)
				}
				if _, err := os.Stat(it.path + ".jelee.bak"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("item %d after the cursor has a backup", i)
				}
			}
		}
	})
	t.Run("cancelled while queued without a child", func(t *testing.T) {
		f := newJobFixture(t)
		nfoExportEnable(t, f, f.registration.Library.ID)
		job := submitNFOExport(t, f, f.registration.Library.ID, "cancel-queued")
		cancelled, err := f.s.CancelJob(f.ctx, f.a, job.ID)
		if err != nil || cancelled.State != domain.JobCancelled {
			t.Fatal("queued export not cancelled at once", cancelled, err)
		}
	})
}

func TestNFOExportJobResumesAfterLeaseLoss(t *testing.T) {
	// Three complete exports run back to back; a loaded machine needs more
	// than the default budget of one.
	f := newJobFixtureWithin(t, 5*time.Minute)
	metadata := nfoApplyTestMetadata(t, f)
	oracleLibrary, err := f.s.RegisterLibrary(f.ctx, "oracle", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oracleItems := nfoExportLibrary(t, f, metadata, oracleLibrary.Library.ID, nfoExportTestItems)
	stopOracle := startNFOExportRunner(t, f, metadata, f.s)
	oracleJob := submitNFOExport(t, f, oracleLibrary.Library.ID, "oracle")
	if done := waitNFOExportJob(t, f, oracleJob.ID); done.State != domain.JobSucceeded {
		t.Fatal("uninterrupted export", done)
	}
	stopOracle()
	want := nfoExportFiles(t, oracleItems)
	expire := func(lease domain.JobLease) {
		// The owner stalls past its lease.
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, lease.Job.ID); err != nil {
			t.Error(err)
		}
	}
	for _, tc := range []struct {
		name string
		key  string
		hook func(*hookedNFOExport)
	}{
		{"after the child was recorded", "after", func(h *hookedNFOExport) {
			h.afterSubmit = func(n int, lease domain.JobLease) {
				if n == 2 {
					expire(lease)
				}
			}
		}},
		{"before the child was recorded", "before", func(h *hookedNFOExport) {
			h.beforeSubmit = func(n int, lease domain.JobLease) {
				if n == 2 {
					expire(lease)
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registration, err := f.s.RegisterLibrary(f.ctx, "resume-"+tc.key, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			items := nfoExportLibrary(t, f, metadata, registration.Library.ID, nfoExportTestItems)
			job := submitNFOExport(t, f, registration.Library.ID, "resume-"+tc.key)
			repo := &hookedNFOExport{Store: f.s}
			tc.hook(repo)
			stop := startNFOExportRunner(t, f, metadata, repo)
			defer stop()
			done := waitNFOExportJob(t, f, job.ID)
			if done.State != domain.JobSucceeded || done.Attempts != 2 {
				t.Fatal("job did not resume once and succeed", done)
			}
			state := requireNFOExportCounts(t, f, job.ID, nfoExportWant(nfoExportTestItems))
			children := nfoExportChildren(t, f, job.ID)
			if state.Chunks < 3 || len(children) != state.Chunks {
				t.Fatalf("a child was duplicated or lost: %d chunks, %+v", state.Chunks, children)
			}
			for i, child := range children {
				if child.State != domain.JobSucceeded || child.Key != domain.NFOExportChildKey(job.ID, i+1) {
					t.Fatal("child", i, child)
				}
			}
			got := nfoExportFiles(t, items)
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("item %d differs from an uninterrupted run:\n got:  %s\n want: %s", i, got[i], want[i])
				}
			}
			requireNFOExportClean(t, f, job.ID)
		})
	}
}

func TestNFOExportJobAdmission(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	if _, _, err := f.s.SubmitNFOExportJob(f.ctx, f.a, library, "off", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrNFOWriteDisabled) {
		t.Fatal("library without NFO policy accepted", err)
	}
	policy, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, library)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, library, "export-read", policy.Generation, domain.NFOModeReadOnly); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitNFOExportJob(f.ctx, f.a, library, "read-only", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrNFOWriteDisabled) {
		t.Fatal("read-only library without the write permission accepted", err)
	}
	if _, err = f.s.SetNFOWritePolicy(f.ctx, f.a, library, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitNFOExportJob(f.ctx, f.a, "00000000-0000-4000-8000-000000000001", "none", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown library accepted", err)
	}
	if _, _, err = f.s.SubmitNFOExportJob(f.ctx, f.a, library, "bad-priority", "urgent", f.policy); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("bad priority accepted", err)
	}
	if _, _, err = f.s.SubmitNFOExportJob(f.ctx, domain.Actor{UserID: f.a.UserID, SessionID: "00000000-0000-4000-8000-000000000002"}, library, "no-session", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("submission without a session", err)
	}
	first := submitNFOExport(t, f, library, "once")
	var cursor *string
	var chunks int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT cursor_item_id::text,chunk FROM nfo_export_job_state WHERE job_id=$1::uuid`, first.ID).Scan(&cursor, &chunks); err != nil || cursor != nil || chunks != 0 {
		t.Fatal("state row missing or advanced", err)
	}
	replay, replayed, err := f.s.SubmitNFOExportJob(f.ctx, f.a, library, "once", domain.JobPriorityManual, f.policy)
	if err != nil || !replayed || replay.ID != first.ID {
		t.Fatal("replay", err, replayed, replay)
	}
	if _, _, err = f.s.SubmitNFOExportJob(f.ctx, f.a, library, "once", domain.JobPriorityBackground, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("same key with another priority accepted", err)
	}
	if _, _, err = f.s.SubmitNFOApplyJob(f.ctx, f.a, library, "once", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("same key reused by another job kind", err)
	}
	other, err := f.s.RegisterLibrary(f.ctx, "other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitNFOExportJob(f.ctx, f.a, other.Library.ID, "once", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("same key for another library accepted", err)
	}
	if _, _, err = f.s.SubmitNFOExportJob(f.ctx, f.a, library, "twice", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("second active job accepted", err)
	}
	// Writes revoked after a job was queued: the replay still answers.
	if _, err = f.s.SetNFOWritePolicy(f.ctx, f.a, library, false); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err = f.s.SubmitNFOExportJob(f.ctx, f.a, library, "once", domain.JobPriorityManual, f.policy); err != nil || !replayed {
		t.Fatal("replay after the write permission was revoked", err)
	}
	limited := f.policy
	limited.QueueLimit = 1
	nfoExportEnable(t, f, other.Library.ID)
	if _, _, err = f.s.SubmitNFOExportJob(f.ctx, f.a, other.Library.ID, "full", domain.JobPriorityManual, limited); !errors.Is(err, domain.ErrJobQueueFull) {
		t.Fatal("queue limit ignored", err)
	}
	// Another job kind has no export state.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',finished_at=clock_timestamp() WHERE id=$1::uuid`, first.ID); err != nil {
		t.Fatal(err)
	}
	scan, _, err := f.s.SubmitJob(f.ctx, f.a, library, "other-scan", domain.JobPriorityManual, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GetNFOExportState(f.ctx, f.a, scan.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("scan job has export state", err)
	}
	if _, err = f.s.GetNFOExportState(f.ctx, f.a, first.ID); err != nil {
		t.Fatal("export state of a cancelled job", err)
	}
	if _, err = f.s.GetNFOExportState(f.ctx, domain.Actor{UserID: f.a.UserID, SessionID: "00000000-0000-4000-8000-000000000002"}, first.ID); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("state read without a session", err)
	}
}

// TestNFOExportChildMayRunBesideItsParentOnly pins the one-active-job rule:
// an export job and its own write job coexist, nothing else does.
func TestNFOExportChildMayRunBesideItsParentOnly(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	nfoExportEnable(t, f, library)
	job := submitNFOExport(t, f, library, "parent")
	// The write-job trigger checks its request at commit; the unique index
	// answers at once, so a rolled back transaction is enough.
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	insert := func(key string, parent *string) error {
		_, err := tx.Exec(f.ctx, `SAVEPOINT attempt`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(f.ctx, `INSERT INTO jobs(library_id,idempotency_key,kind,priority,export_job_id,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1::uuid,$2,'nfo_write','manual',$3::uuid,1,1,100,1,1,1,1)`, library, key, parent)
		if err != nil {
			if _, e := tx.Exec(f.ctx, `ROLLBACK TO SAVEPOINT attempt`); e != nil {
				t.Fatal(e)
			}
		}
		return err
	}
	if err := insert("stranger", nil); err == nil {
		t.Fatal("a second top-level job was accepted")
	}
	if err := insert("child", &job.ID); err != nil {
		t.Fatal("the child of the export job was refused", err)
	}
	if err := insert("second-child", &job.ID); err == nil {
		t.Fatal("a second child was accepted")
	}
	if _, err := tx.Exec(f.ctx, `UPDATE jobs SET export_job_id=$1::uuid WHERE id=$1::uuid`, job.ID); err == nil {
		t.Fatal("a job of another kind accepted a parent")
	}
}

func TestNFOExportMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	jobMetricMigration(t, f, "down", downgradeAboveMigration(t, f, "nfo_export_jobs")-1)
	var removed bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('nfo_export_job_state') IS NULL AND NOT EXISTS(SELECT 1 FROM job_metric_totals WHERE kind='nfo_export') AND NOT EXISTS(SELECT 1 FROM job_metric_buckets WHERE kind='nfo_export')`).Scan(&removed); err != nil || !removed {
		t.Fatal("empty downgrade left export objects", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO jobs(library_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1::uuid,'k','nfo_export','manual',1,1,100,1,1,1,1)`, f.registration.Library.ID); err == nil {
		t.Fatal("older schema accepts nfo_export jobs")
	}
	// The older index again allows a single active job per library.
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO jobs(library_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1::uuid,'a','inventory_scan','manual',1,1,100,1,1,1,1),($1::uuid,'b','inventory_scan','manual',1,1,100,1,1,1,1)`, f.registration.Library.ID); err == nil {
		t.Fatal("older schema allows two active jobs in a library")
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("round-trip schema is not ready", err)
	}
}

func TestNFOExportMigrationRefusesRetainedData(t *testing.T) {
	f := newJobFixture(t)
	nfoExportEnable(t, f, f.registration.Library.ID)
	job := submitNFOExport(t, f, f.registration.Library.ID, "retained")
	want := downgradeAboveMigration(t, f, "nfo_export_jobs")
	nfoMigrationDenied(t, f, "000089_nfo_export_jobs.down.sql")
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("retained export job downgraded")
	}
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != want-1 || !dirty {
		t.Fatal("refused downgrade lost dirty status", version, dirty, err)
	}
	var kept int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_export_job_state WHERE job_id=$1::uuid`, job.ID).Scan(&kept); err != nil || kept != 1 {
		t.Fatal("refused downgrade removed the state row", err)
	}
}

func TestNFOExportMigrationRefusesRetainedChild(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	job := f.submit(t, "scan")
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',finished_at=clock_timestamp() WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	// A write job needs its request rows at commit; the fixture skips that check.
	if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs DISABLE TRIGGER verify_nfo_write_job`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO jobs(library_id,idempotency_key,kind,state,priority,export_job_id,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit,finished_at) VALUES($1::uuid,'child','nfo_write','cancelled','manual',$2::uuid,1,1,100,1,1,1,1,clock_timestamp())`, library, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs ENABLE TRIGGER verify_nfo_write_job`); err != nil {
		t.Fatal(err)
	}
	downgradeAboveMigration(t, f, "nfo_export_jobs")
	nfoMigrationDenied(t, f, "000089_nfo_export_jobs.down.sql")
}

func TestNFOExportCoversSeriesSeasonAndEpisode(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	library, rootID := f.registration.Library.ID, f.registration.RootID
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, rootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	nfoExportEnable(t, f, library)
	write := func(name, body string) string {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	newItem := func(kind string) string {
		var id string
		if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Scanned name',$2) RETURNING id::text`, library, kind).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	directory := func(item, kind, dir string) {
		if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_directory_sources(item_id,library_id,kind,root_id,relative_path) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5)`, item, library, kind, rootID, dir); err != nil {
			t.Fatal(err)
		}
	}
	text := func(field, value string) domain.ItemMetadataPatch {
		return domain.ItemMetadataPatch{Field: field, Value: &value}
	}
	edit := func(item string, fields []domain.ItemMetadataPatch, facts []domain.ItemMetadataFactPatch) {
		if _, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, item, 1, fields, facts); err != nil {
			t.Fatal(err)
		}
	}
	series, season, episode, bare := newItem("Series"), newItem("Season"), newItem("Episode"), newItem("Movie")
	directory(series, "Series", "Show")
	directory(season, "Season", "Show/Season 1")
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'Show/Season 1/e01.mkv','video/x-matroska'),($4::uuid,$2::uuid,$3::uuid,'bare.mkv','video/x-matroska')`, episode, library, rootID, bare); err != nil {
		t.Fatal(err)
	}
	write("Show/Season 1/e01.mkv", "media")
	write("bare.mkv", "media")
	seriesNFO := "<tvshow>\n  <!-- show -->\n  <title>Old show</title>\n  <status>Continuing</status>\n  <extra a=\"1\">kept</extra>\n</tvshow>\n"
	seasonNFO := "<season>\n  <!-- season -->\n  <title>Old season</title>\n  <seasonnumber>1</seasonnumber>\n</season>\n"
	episodeNFO := "<episodedetails>\n  <!-- episode -->\n  <title>Old episode</title>\n  <showtitle>Old show</showtitle>\n  <season>1</season>\n  <episode>1</episode>\n</episodedetails>\n"
	seriesPath, seasonPath, episodePath := write("Show/tvshow.nfo", seriesNFO), write("Show/Season 1/season.nfo", seasonNFO), write("Show/Season 1/e01.nfo", episodeNFO)
	status, showTitle := json.RawMessage(`"Ended"`), json.RawMessage(`"New show"`)
	edit(series, []domain.ItemMetadataPatch{text("title", "New show")}, []domain.ItemMetadataFactPatch{{Field: "seriesStatus", Value: status}})
	edit(season, []domain.ItemMetadataPatch{text("title", "New season")}, nil)
	edit(episode, []domain.ItemMetadataPatch{text("title", "New episode")}, []domain.ItemMetadataFactPatch{{Field: "showTitle", Value: showTitle}})
	edit(bare, []domain.ItemMetadataPatch{text("title", "Bare")}, nil)
	job := submitNFOExport(t, f, library, "kinds")
	stop := startNFOExportRunner(t, f, metadata, f.s)
	defer stop()
	if done := waitNFOExportJob(t, f, job.ID); done.State != domain.JobSucceeded {
		t.Fatal("export", done)
	}
	requireNFOExportCounts(t, f, job.ID, domain.NFOExportCounts{Written: 3, SkippedMissing: 1})
	for _, c := range []struct {
		path string
		want []string
		gone string
	}{
		{seriesPath, []string{"<title>New show</title>", "<status>Ended</status>", "<!-- show -->", `<extra a="1">kept</extra>`}, "Old show"},
		{seasonPath, []string{"<title>New season</title>", "<!-- season -->", "<seasonnumber>1</seasonnumber>"}, "Old season"},
		{episodePath, []string{"<title>New episode</title>", "<showtitle>New show</showtitle>", "<!-- episode -->", "<episode>1</episode>"}, "Old episode"},
	} {
		data, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range c.want {
			if !strings.Contains(string(data), want) {
				t.Fatalf("%s lacks %q:\n%s", c.path, want, data)
			}
		}
		if strings.Contains(string(data), c.gone) {
			t.Fatalf("%s keeps %q", c.path, c.gone)
		}
		if _, err := os.Stat(c.path + ".jelee.bak"); err != nil {
			t.Fatalf("%s has no backup", c.path)
		}
	}
}

func TestNFOExportJobStopsWhenWritingIsRevoked(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoExportLibrary(t, f, metadata, f.registration.Library.ID, 48)
	job := submitNFOExport(t, f, f.registration.Library.ID, "revoked")
	// Admitted, then the permission goes away before the first child.
	if _, err := f.s.SetNFOWritePolicy(f.ctx, f.a, f.registration.Library.ID, false); err != nil {
		t.Fatal(err)
	}
	stop := startNFOExportRunner(t, f, metadata, f.s)
	defer stop()
	done := waitNFOExportJob(t, f, job.ID)
	if done.State != domain.JobFailed || done.ErrorCode != "nfo_export_failed" {
		t.Fatal("job did not fail", done)
	}
	if children := nfoExportChildren(t, f, job.ID); len(children) != 0 {
		t.Fatal("a write job was created without the permission", children)
	}
	for i, it := range items {
		if i%8 == 2 {
			continue
		}
		if data, err := os.ReadFile(it.path); err != nil || string(data) != it.body {
			t.Fatalf("item %d changed", i)
		}
	}
	// The chunk's preparations went with the job.
	requireNFOExportClean(t, f, job.ID)
}
