package postgres

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// nfoFixPathItems adds the two items whose NFO location is illegal: one whose
// NFO is a symbolic link (the scanner skips it, so only the fix meets it) and
// one with two NFOs that differ only by case. A fix never touches either.
func nfoFixPathItems(t *testing.T, f jobFixture, library string) []*nfoFixItem {
	t.Helper()
	var root, rootID string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path,id::text FROM library_roots WHERE library_id=$1::uuid`, library).Scan(&root, &rootID); err != nil {
		t.Fatal(err)
	}
	body := "<movie><title>Elsewhere</title></movie>\n"
	var items []*nfoFixItem
	for _, name := range []string{"sym", "col"} {
		var id string
		if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Scanned name','Movie') RETURNING id::text`, library).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska')`, id, library, rootID, name+".mkv"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name+".mkv"), []byte("synthetic media"), 0o600); err != nil {
			t.Fatal(err)
		}
		value := "Stored " + name
		if _, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, id, 1, []domain.ItemMetadataPatch{{Field: "title", Value: &value}}, nil); err != nil {
			t.Fatal(err)
		}
		it := &nfoFixItem{category: -2, label: name + " path item", outcome: "unfixable", body: body}
		it.id, it.kind, it.existing = id, "Movie", true
		switch name {
		case "sym":
			target := filepath.Join(root, "sym-target.txt")
			if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("sym-target.txt", filepath.Join(root, "sym.nfo")); err != nil {
				t.Fatal(err)
			}
			it.nfo, it.path = "sym.nfo", filepath.Join(root, "sym.nfo")
		case "col":
			for _, n := range []string{"Col.nfo", "COL.NFO"} {
				if err := os.WriteFile(filepath.Join(root, n), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			it.nfo, it.path = "Col.nfo", filepath.Join(root, "Col.nfo")
		}
		info, err := os.Stat(it.path)
		if err != nil {
			t.Fatal(err)
		}
		it.before = info
		items = append(items, it)
	}
	return items
}

func requireNFOFixPathItems(t *testing.T, items []*nfoFixItem) {
	t.Helper()
	for _, it := range items {
		if it.category != -2 {
			continue
		}
		if it.nfo == "sym.nfo" {
			if info, err := os.Lstat(it.path); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("the symbolic link was replaced", err)
			}
		}
		for _, name := range []string{it.path, filepath.Join(filepath.Dir(it.path), "COL.NFO"), filepath.Join(filepath.Dir(it.path), "sym-target.txt")} {
			if _, err := os.Lstat(name + ".jelee.bak"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a path item got a backup", name)
			}
		}
		if got, err := os.ReadFile(it.path); err != nil || string(got) != it.body {
			t.Fatal("a path item's file changed", err)
		}
	}
}

func TestNFOExportFixRepairsOnlySafeIssues(t *testing.T) {
	f := newJobFixtureWithin(t, 10*time.Minute)
	library := f.registration.Library.ID
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoFixLibrary(t, f, library, 10)
	items = append(items, nfoFixPathItems(t, f, library)...)
	if len(items) < 100 {
		t.Fatal("the library is too small", len(items))
	}
	want := nfoFixWant(items)
	if want.Fixed != 42 || want.Unfixable != 42 || want.Unchanged != 21 || want.SkippedMissing != 10 {
		t.Fatal("unexpected mix", want)
	}
	// The validation job lists what is wrong before the fix.
	reader := nfoFixReader(t)
	runNFOValidation(t, f, library, "validate-before", reader)
	before := nfoValidationObservations(t, f, library)
	requireNFOFixObservations(t, before, items, false)
	for _, name := range []string{"Col.nfo", "COL.NFO"} {
		if o := before[name]; o.status != domain.NFOStatusInvalid || o.entries != 0 || len(o.codes) != 1 || o.codes[0] != domain.NFOPathCaseCollision || o.errors != 1 {
			t.Fatalf("%s: %+v", name, o)
		}
	}
	if _, ok := before["sym.nfo"]; ok {
		t.Fatal("the scanner inventoried a symbolic link")
	}
	job := submitNFOFix(t, f, library, "fix-1")
	stop := startNFOExportRunner(t, f, metadata, f.s)
	defer stop()
	done := waitNFOExportJob(t, f, job.ID)
	if done.State != domain.JobSucceeded || done.ErrorCode != "" {
		t.Fatal("the fix did not succeed", done)
	}
	state := requireNFOExportCounts(t, f, job.ID, want)
	if state.Mode != domain.NFOExportFix || state.Written != 0 || state.Chunks < (want.Fixed+domain.NFOExportChunkSize-1)/domain.NFOExportChunkSize || state.ChildJobID != nil {
		t.Fatalf("state: %+v", state)
	}
	if done.Files != int64(want.Fixed+want.Unchanged) || done.Skipped != int64(want.Unfixable+want.SkippedMissing) {
		t.Fatalf("job progress files=%d skipped=%d", done.Files, done.Skipped)
	}
	children := nfoExportChildren(t, f, job.ID)
	if len(children) != state.Chunks || nfoMissingEntries(t, f, job.ID) != want.Fixed {
		t.Fatal("children or entries", len(children), nfoMissingEntries(t, f, job.ID))
	}
	for i, child := range children {
		if child.State != domain.JobSucceeded || child.Key != domain.NFOExportChildKey(job.ID, i+1) {
			t.Fatal("child", i, child)
		}
	}
	requireNFOFixFiles(t, items, "")
	requireNFOFixPathItems(t, items)
	requireNFOExportClean(t, f, job.ID)
	requireNoTokenFiles(t, f, metadata, nfoFixMissingItems(items))
	// Validating again: the repaired issues are gone, the others are still listed.
	runNFOValidation(t, f, library, "validate-after", reader)
	requireNFOFixObservations(t, nfoValidationObservations(t, f, library), items, true)
	// A second fix has nothing left to repair and writes nothing.
	second := submitNFOFix(t, f, library, "fix-2")
	if finished := waitNFOExportJob(t, f, second.ID); finished.State != domain.JobSucceeded {
		t.Fatal("second fix", finished)
	}
	again := requireNFOExportCounts(t, f, second.ID, domain.NFOExportCounts{Unchanged: want.Unchanged + want.Fixed, Unfixable: want.Unfixable, SkippedMissing: want.SkippedMissing})
	if again.Chunks != 0 || len(nfoExportChildren(t, f, second.ID)) != 0 {
		t.Fatal("an idempotent fix submitted writes", again)
	}
	requireNFOFixFiles(t, items, "")
}

func TestNFOValidationJobListsIllegalPaths(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	nfoExportEnable(t, f, library)
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE library_id=$1::uuid`, library).Scan(&root); err != nil {
		t.Fatal(err)
	}
	write := func(relative, body string) {
		full := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const valid = "<movie><title>T</title><uniqueid type=\"x\">1</uniqueid></movie>\n"
	for _, name := range []string{"ok.nfo", "target.nfo", "link.nfo", "fifo.nfo", "dirnfo.nfo", "Dup.nfo", "DUP.NFO", "real/inner.nfo"} {
		write(name, valid)
	}
	mutate := func() {
		must := func(err error) {
			if err != nil {
				t.Error(err)
			}
		}
		// A link, a FIFO and a directory take the place of regular files; a
		// directory of the inventory becomes a link to its moved self.
		must(os.Remove(filepath.Join(root, "link.nfo")))
		must(os.Symlink("target.nfo", filepath.Join(root, "link.nfo")))
		must(os.Remove(filepath.Join(root, "fifo.nfo")))
		must(syscall.Mkfifo(filepath.Join(root, "fifo.nfo"), 0o600))
		must(os.Remove(filepath.Join(root, "dirnfo.nfo")))
		must(os.Mkdir(filepath.Join(root, "dirnfo.nfo"), 0o700))
		must(os.Rename(filepath.Join(root, "real"), filepath.Join(root, "real-moved")))
		must(os.Symlink("real-moved", filepath.Join(root, "real")))
	}
	reader := &nfoMutatingReader{SummaryReader: nfoFixReader(t), mutate: mutate}
	done := runNFOValidation(t, f, library, "paths-1", reader)
	got := nfoValidationObservations(t, f, library)
	for name, code := range map[string]string{
		"link.nfo": domain.NFOPathSymlink, "fifo.nfo": domain.NFOPathNotRegular, "dirnfo.nfo": domain.NFOPathNotRegular,
		"Dup.nfo": domain.NFOPathCaseCollision, "DUP.NFO": domain.NFOPathCaseCollision, "real/inner.nfo": domain.NFOPathElementSymlink,
	} {
		o, ok := got[name]
		if !ok || o.status != domain.NFOStatusInvalid || o.entries != 0 || o.failure != "" || o.errors != 1 || len(o.codes) != 1 || o.codes[0] != code {
			t.Fatalf("%s: %+v, want %s", name, o, code)
		}
	}
	for _, name := range []string{"ok.nfo", "target.nfo"} {
		if o := got[name]; o.status != domain.NFOStatusValid || o.entries != 1 || len(o.codes) != 0 {
			t.Fatalf("%s: %+v", name, o)
		}
	}
	summary, err := f.s.GetNFOJobSummary(f.ctx, f.a, done.ID)
	if err != nil || summary.Invalid != 6 || summary.Valid != 2 || summary.Unavailable != 0 || summary.Processed != 8 {
		t.Fatalf("job summary %+v %v", summary, err)
	}
	// Nothing was weakened: the refusals still stand for the item path.
	// Put the files back: the next validation replaces the path observations.
	for _, name := range []string{"link.nfo", "fifo.nfo"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
		write(name, valid)
	}
	if err := os.Remove(filepath.Join(root, "dirnfo.nfo")); err != nil {
		t.Fatal(err)
	}
	write("dirnfo.nfo", valid+"<!-- changed -->")
	if err := os.Remove(filepath.Join(root, "real")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "real-moved"), filepath.Join(root, "real")); err != nil {
		t.Fatal(err)
	}
	runNFOValidation(t, f, library, "paths-2", nfoFixReader(t))
	got = nfoValidationObservations(t, f, library)
	for name, o := range got {
		wantCollision := name == "Dup.nfo" || name == "DUP.NFO"
		if wantCollision != (len(o.codes) == 1 && o.codes[0] == domain.NFOPathCaseCollision) || (!wantCollision && (o.status != domain.NFOStatusValid || len(o.codes) != 0)) {
			t.Fatalf("%s after the files were restored: %+v", name, o)
		}
	}
}

func TestNFOExportFixCancellation(t *testing.T) {
	t.Run("child cancelled before it ran", func(t *testing.T) {
		f := newJobFixture(t)
		metadata := nfoApplyTestMetadata(t, f)
		items := nfoFixLibrary(t, f, f.registration.Library.ID, 6)
		job := submitNFOFix(t, f, f.registration.Library.ID, "cancel-early")
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
		children := nfoExportChildren(t, f, job.ID)
		if done.State != domain.JobCancelled || len(children) != 1 || children[0].State != domain.JobCancelled {
			t.Fatal("job not cancelled or later chunks were submitted", done, children)
		}
		entries := nfoMissingEntries(t, f, job.ID)
		state, err := f.s.GetNFOExportState(f.ctx, f.a, job.ID)
		if err != nil || state.Chunks != 1 || state.ChildJobID != nil || state.Fixed != 0 || state.Written != 0 || entries < 1 || state.Failed != entries {
			t.Fatalf("state after cancellation: %+v %d %v", state, entries, err)
		}
		requireNFOFixFiles(t, items, "00000000-0000-0000-0000-000000000000")
		requireNFOExportClean(t, f, job.ID)
	})
	t.Run("cancelled between chunks", func(t *testing.T) {
		f := newJobFixture(t)
		metadata := nfoApplyTestMetadata(t, f)
		items := nfoFixLibrary(t, f, f.registration.Library.ID, 10)
		job := submitNFOFix(t, f, f.registration.Library.ID, "cancel-late")
		repo := &hookedNFOExport{Store: f.s}
		var cancelled atomic.Bool
		repo.afterNextStep = func(_ int, lease domain.JobLease, step domain.NFOExportStep) {
			if !step.Waiting && step.Settle == nil && step.Chunk == 1 && cancelled.CompareAndSwap(false, true) {
				if _, err := f.s.CancelJob(f.ctx, f.a, lease.Job.ID); err != nil {
					t.Error(err)
				}
			}
		}
		stop := startNFOExportRunner(t, f, metadata, repo)
		defer stop()
		done := waitNFOExportJob(t, f, job.ID)
		children := nfoExportChildren(t, f, job.ID)
		if done.State != domain.JobCancelled || len(children) != 1 || children[0].State != domain.JobSucceeded {
			t.Fatal("expected a cancelled job with one finished child", done, children)
		}
		entries := nfoMissingEntries(t, f, job.ID)
		state, err := f.s.GetNFOExportState(f.ctx, f.a, job.ID)
		if err != nil || state.CursorItemID == nil || state.Chunks != 1 || entries < 1 || state.Fixed != entries || state.Failed != 0 || state.ChildJobID != nil {
			t.Fatalf("state after cancellation: %+v %d %v", state, entries, err)
		}
		// The first chunk is repaired; nothing after its cursor is touched.
		requireNFOFixFiles(t, items, *state.CursorItemID)
		fixed := 0
		for _, it := range items {
			if it.outcome == "fixed" && it.id <= *state.CursorItemID {
				fixed++
			}
		}
		if fixed != entries {
			t.Fatal("entries differ from the repaired files before the cursor", fixed, entries)
		}
		requireNFOExportClean(t, f, job.ID)
	})
}

func TestNFOExportFixResumesAfterLeaseLoss(t *testing.T) {
	f := newJobFixtureWithin(t, 10*time.Minute)
	metadata := nfoApplyTestMetadata(t, f)
	oracleLibrary, err := f.s.RegisterLibrary(f.ctx, "oracle", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oracleItems := nfoFixLibrary(t, f, oracleLibrary.Library.ID, 10)
	stopOracle := startNFOExportRunner(t, f, metadata, f.s)
	oracleJob := submitNFOFix(t, f, oracleLibrary.Library.ID, "oracle")
	if done := waitNFOExportJob(t, f, oracleJob.ID); done.State != domain.JobSucceeded {
		t.Fatal("uninterrupted fix", done)
	}
	stopOracle()
	oracleState := requireNFOExportCounts(t, f, oracleJob.ID, nfoFixWant(oracleItems))
	if oracleState.Chunks < 3 {
		t.Fatal("the library needs at least three chunks", oracleState)
	}
	want := nfoExportFiles(t, nfoFixExportItems(oracleItems))
	for _, tc := range []struct {
		name string
		key  string
		hook func(h *hookedNFOExport, expire func(domain.JobLease))
	}{
		{"after the child was recorded", "after", func(h *hookedNFOExport, expire func(domain.JobLease)) {
			h.afterSubmit = func(n int, lease domain.JobLease) {
				if n == 2 {
					expire(lease)
				}
			}
		}},
		{"before the child was recorded", "before", func(h *hookedNFOExport, expire func(domain.JobLease)) {
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
			items := nfoFixLibrary(t, f, registration.Library.ID, 10)
			job := submitNFOFix(t, f, registration.Library.ID, "resume-"+tc.key)
			repo := &hookedNFOExport{Store: f.s}
			var expired atomic.Int32
			tc.hook(repo, func(lease domain.JobLease) {
				expired.Add(1)
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, lease.Job.ID); err != nil {
					t.Error(err)
				}
			})
			stop := startNFOExportRunner(t, f, metadata, repo)
			defer stop()
			done := waitNFOExportJob(t, f, job.ID)
			// The attempt count is not asserted beyond "claimed again": a later
			// claim may be late under load.
			if done.State != domain.JobSucceeded || done.Attempts < 2 || expired.Load() != 1 {
				t.Fatal("job did not resume and succeed", done, expired.Load())
			}
			state := requireNFOExportCounts(t, f, job.ID, nfoFixWant(items))
			children := nfoExportChildren(t, f, job.ID)
			if state.Chunks < 3 || len(children) != state.Chunks {
				t.Fatalf("a child was duplicated or lost: %d chunks, %+v", state.Chunks, children)
			}
			for i, child := range children {
				if child.State != domain.JobSucceeded || child.Key != domain.NFOExportChildKey(job.ID, i+1) {
					t.Fatal("child", i, child)
				}
			}
			if entries := nfoMissingEntries(t, f, job.ID); entries != state.Fixed {
				t.Fatal("an entry was duplicated or lost", entries, state.Fixed)
			}
			got := nfoExportFiles(t, nfoFixExportItems(items))
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("item %d differs from an uninterrupted run:\n got:  %s\n want: %s", i, got[i], want[i])
				}
			}
			requireNFOFixFiles(t, items, "")
			requireNFOExportClean(t, f, job.ID)
		})
	}
}

func TestNFOExportFixKeepsNFOsWhenWritingIsRevokedBeforeItRuns(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoFixLibrary(t, f, f.registration.Library.ID, 3)
	job := submitNFOFix(t, f, f.registration.Library.ID, "revoked")
	if _, err := f.s.SetNFOWritePolicy(f.ctx, f.a, f.registration.Library.ID, false); err != nil {
		t.Fatal(err)
	}
	stop := startNFOExportRunner(t, f, metadata, f.s)
	defer stop()
	done := waitNFOExportJob(t, f, job.ID)
	if done.State != domain.JobFailed {
		t.Fatal("the fix ran without the write permission", done)
	}
	requireNFOFixFiles(t, items, "00000000-0000-0000-0000-000000000000")
	if len(nfoExportChildren(t, f, job.ID)) != 0 {
		t.Fatal("a write job was submitted without the permission")
	}
}
