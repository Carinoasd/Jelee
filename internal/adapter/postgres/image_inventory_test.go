package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func imageEntry(name, kind string, size, modified int64) domain.InventoryEntry {
	return domain.InventoryEntry{Path: name, Kind: kind, Size: size, ModifiedUnixNano: modified}
}
func startImageInventory(t *testing.T, f jobFixture, key string, entries []domain.InventoryEntry, done bool, skipped int64) domain.JobLease {
	t.Helper()
	f.submit(t, key)
	l := f.claim(t, "image-worker")
	d := f.directory(t, l)
	copyEntries := append([]domain.InventoryEntry(nil), entries...)
	for i := range copyEntries {
		copyEntries[i].RootID = d.RootID
	}
	if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: copyEntries, Done: done, Skipped: skipped}); err != nil {
		t.Fatal(err)
	}
	return l
}
func finishImageInventory(t *testing.T, f jobFixture, l domain.JobLease, state, code string) domain.ImageProgress {
	t.Helper()
	if err := f.s.FinishJob(f.ctx, l, state, code); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.GetImageJobSummary(f.ctx, f.a, l.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p.ImageProgress
}
func imageBaseline(t *testing.T, f jobFixture) string {
	t.Helper()
	var out string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(b) ORDER BY root_id,path),'[]'::jsonb)::text FROM library_inventory_baseline b WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestImageComparisonColdWarmChangesAndKindTransitions(t *testing.T) {
	f := newJobFixture(t)
	f.policy.MissingCountLimit, f.policy.MissingPercentLimit = 100, 100
	initial := []domain.InventoryEntry{imageEntry("same.jpg", "image", 7, 10), imageEntry("size.jpg", "image", 7, 10), imageEntry("mtime.jpg", "image", 7, 10), imageEntry("gone.jpg", "image", 7, 10), imageEntry("type.jpg", "image", 7, 10), imageEntry("other.bin", "other", 7, 10)}
	l := startImageInventory(t, f, "cold", initial, true, 0)
	if p := finishImageInventory(t, f, l, domain.JobSucceeded, ""); p != (domain.ImageProgress{Added: 5, ComparisonComplete: true}) {
		t.Fatal("cold image classification", p)
	}
	l = startImageInventory(t, f, "warm", initial, true, 0)
	if p := finishImageInventory(t, f, l, domain.JobSucceeded, ""); p != (domain.ImageProgress{Unchanged: 5, ComparisonComplete: true}) {
		t.Fatal("warm image classification", p)
	}
	changed := []domain.InventoryEntry{initial[0], imageEntry("size.jpg", "image", 8, 10), imageEntry("mtime.jpg", "image", 7, 11), imageEntry("type.jpg", "other", 7, 10), imageEntry("other.bin", "image", 7, 10), imageEntry("new.png", "image", 2, 11)}
	l = startImageInventory(t, f, "changed", changed, true, 0)
	if p := finishImageInventory(t, f, l, domain.JobSucceeded, ""); p != (domain.ImageProgress{Added: 2, Changed: 2, Unchanged: 1, Missing: 2, ComparisonComplete: true}) {
		t.Fatal("attribute and kind comparison", p)
	}
	l = startImageInventory(t, f, "next", changed, true, 0)
	if p := finishImageInventory(t, f, l, domain.JobSucceeded, ""); p != (domain.ImageProgress{Unchanged: 5, ComparisonComplete: true}) {
		t.Fatal("new baseline was not published", p)
	}
}
func TestImageIncompleteAndReviewNeverAcknowledgeMissing(t *testing.T) {
	for _, state := range []string{"skipped", domain.JobFailed, domain.JobCancelled, "review"} {
		t.Run(state, func(t *testing.T) {
			f := newJobFixture(t)
			all := []domain.InventoryEntry{imageEntry("a.jpg", "image", 7, 10), imageEntry("b.jpg", "image", 7, 10)}
			finishImageInventory(t, f, startImageInventory(t, f, "baseline", all, true, 0), domain.JobSucceeded, "")
			baseline := imageBaseline(t, f)
			done, skipped, terminal, code := true, int64(0), domain.JobSucceeded, ""
			if state == "skipped" {
				skipped = 1
			}
			if state == domain.JobFailed {
				done = false
				terminal = state
				code = "scan_io"
			}
			if state == domain.JobCancelled {
				done = false
				terminal = state
			}
			l := startImageInventory(t, f, "partial", all[:1], done, skipped)
			p := finishImageInventory(t, f, l, terminal, code)
			if state == "review" {
				if !p.ComparisonComplete || p.Missing != 1 {
					t.Fatal("complete review lost known missing", p)
				}
			} else if p.ComparisonComplete || p.Missing != 0 {
				t.Fatal("partial inventory claimed missing", p)
			}
			if imageBaseline(t, f) != baseline {
				t.Fatal("partial/review replaced accepted baseline")
			}
			if state == "review" {
				p = finishImageInventory(t, f, startImageInventory(t, f, "review-again", all[:1], true, 0), domain.JobSucceeded, "")
				if p.Missing != 1 || imageBaseline(t, f) != baseline {
					t.Fatal("review silently acknowledged removal")
				}
			}
		})
	}
}
func TestImageUnknownBaselineAndRootEpochRecovery(t *testing.T) {
	f := newJobFixture(t)
	all := []domain.InventoryEntry{imageEntry("a.jpg", "image", 7, 10), imageEntry("b.jpg", "image", 7, 10)}
	finishImageInventory(t, f, startImageInventory(t, f, "known", all, true, 0), domain.JobSucceeded, "")
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET attributes_known=false,kind=NULL,size=NULL,modified_unix_nano=NULL,inventory_generation=NULL`); err != nil {
		t.Fatal(err)
	}
	partial := finishImageInventory(t, f, startImageInventory(t, f, "old-baseline-missing", all[:1], true, 0), domain.JobSucceeded, "")
	if partial != (domain.ImageProgress{Uncompared: 1}) {
		t.Fatal("unknown old attributes produced missing images", partial)
	}
	p := finishImageInventory(t, f, startImageInventory(t, f, "old-baseline", all, true, 0), domain.JobSucceeded, "")
	if p != (domain.ImageProgress{Unchanged: 1, Added: 1, ComparisonComplete: true}) {
		t.Fatal("complete previous observation did not replace unknown historical attributes", p)
	}
	p = finishImageInventory(t, f, startImageInventory(t, f, "upgraded-baseline", all, true, 0), domain.JobSucceeded, "")
	if p != (domain.ImageProgress{Unchanged: 2, ComparisonComplete: true}) {
		t.Fatal("known baseline not established", p)
	}
	l := startImageInventory(t, f, "root-race", all, true, 0)
	before := imageBaseline(t, f)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET path=path||'-replacement' WHERE id=$1::uuid`, allRoot(t, f)); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrInventoryInvalidated) {
		t.Fatal("root swap published old inventory", err)
	}
	if imageBaseline(t, f) != before || f.get(t, l.Job.ID).State != domain.JobRunning {
		t.Fatal("rejected publication was not atomic")
	}
	p = finishImageInventory(t, f, l, domain.JobFailed, "scan_unavailable")
	if p != (domain.ImageProgress{Uncompared: 2}) || imageBaseline(t, f) != before {
		t.Fatal("failed root epoch claimed removals", p)
	}
	p = finishImageInventory(t, f, startImageInventory(t, f, "new-root", all, true, 0), domain.JobSucceeded, "")
	if p != (domain.ImageProgress{Uncompared: 2}) {
		t.Fatal("root epoch reused old attributes", p)
	}
	p = finishImageInventory(t, f, startImageInventory(t, f, "new-root-warm", all, true, 0), domain.JobSucceeded, "")
	if p != (domain.ImageProgress{Unchanged: 2, ComparisonComplete: true}) {
		t.Fatal("root epoch recovery", p)
	}
}
func allRoot(t *testing.T, f jobFixture) string {
	t.Helper()
	var id string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM library_roots WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func TestImageLateTerminalLeaseExpiryRollsBackPublication(t *testing.T) {
	f := newJobFixture(t)
	all := []domain.InventoryEntry{imageEntry("a.jpg", "image", 7, 10)}
	finishImageInventory(t, f, startImageInventory(t, f, "before", all, true, 0), domain.JobSucceeded, "")
	all[0].Size++
	l := startImageInventory(t, f, "late", all, true, 0)
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE image_finish_seen; CREATE FUNCTION image_finish_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='succeeded' THEN PERFORM nextval('image_finish_seen'); PERFORM pg_sleep(0.5); END IF; RETURN NEW; END $$; CREATE TRIGGER image_finish_pause BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION image_finish_pause()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '350 milliseconds' WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	before := nfoSnapshot(t, nfoFixture{jobFixture: f})
	started := time.Now()
	err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, "")
	var called bool
	if e := f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM image_finish_seen`).Scan(&called); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(err, domain.ErrJobLeaseLost) || !called || time.Since(started) < 500*time.Millisecond || before != nfoSnapshot(t, nfoFixture{jobFixture: f}) {
		t.Fatal("late terminal fence did not roll back image/baseline/job", err)
	}
}

func TestImageUnfinishedTerminalPathsRetainStableUncomparedCounts(t *testing.T) {
	for _, path := range []string{"queued-cancel", "release-cancel", "release-exhausted", "expiry-exhausted", "expiry-cancel"} {
		t.Run(path, func(t *testing.T) {
			f := newJobFixture(t)
			all := []domain.InventoryEntry{imageEntry("a.jpg", "image", 7, 10), imageEntry("b.bin", "other", 7, 10)}
			finishImageInventory(t, f, startImageInventory(t, f, "baseline", all, true, 0), domain.JobSucceeded, "")
			baseline := imageBaseline(t, f)
			if path == "release-exhausted" || path == "expiry-exhausted" {
				f.policy.MaxAttempts = 1
			}
			l := startImageInventory(t, f, "partial", all, false, 0)
			if path == "release-cancel" || path == "expiry-cancel" {
				if _, err := f.s.CancelJob(f.ctx, f.a, l.Job.ID); err != nil {
					t.Fatal(err)
				}
			}
			switch path {
			case "queued-cancel":
				if err := f.s.ReleaseJob(f.ctx, l); err != nil {
					t.Fatal(err)
				}
				if _, err := f.s.CancelJob(f.ctx, f.a, l.Job.ID); err != nil {
					t.Fatal(err)
				}
			case "release-cancel", "release-exhausted":
				if err := f.s.ReleaseJob(f.ctx, l); err != nil {
					t.Fatal(err)
				}
			default:
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.s.ClaimJob(f.ctx, "recovery", false, time.Minute); !errors.Is(err, domain.ErrNotFound) {
					t.Fatal("expiry did not end exhausted/cancelled job", err)
				}
			}
			before, err := f.s.GetImageJobSummary(f.ctx, f.a, l.Job.ID)
			if err != nil || before.ImageProgress != (domain.ImageProgress{Uncompared: 1}) || imageBaseline(t, f) != baseline {
				t.Fatal("terminal partial observations lost", before, err)
			}
			all[0].Size++
			finishImageInventory(t, f, startImageInventory(t, f, "later-baseline", all, true, 0), domain.JobSucceeded, "")
			after, err := f.s.GetImageJobSummary(f.ctx, f.a, l.Job.ID)
			if err != nil || after != before {
				t.Fatal("later baseline changed historical incomplete comparison", err)
			}
		})
	}
}

func TestInventoryEpochIsIndependentOfNFOAndProbePolicy(t *testing.T) {
	f := newNFOFixture(t)
	read := func() int64 {
		t.Helper()
		var generation int64
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT inventory_generation FROM libraries WHERE id=$1::uuid`, f.registration.Library.ID).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		return generation
	}
	before := read()
	p, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, f.registration.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, p.LibraryID, "off-independent", p.Generation, domain.NFOModeOff); err != nil {
		t.Fatal(err)
	}
	if err = f.s.InvalidateProbeLibrary(f.ctx, f.a, p.LibraryID); err != nil {
		t.Fatal(err)
	}
	if read() != before {
		t.Fatal("metadata invalidation changed inventory root epoch")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET path=path WHERE library_id=$1::uuid`, p.LibraryID); err != nil {
		t.Fatal(err)
	}
	if read() != before {
		t.Fatal("no-op root update changed epoch")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2)`, p.LibraryID, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if read() != before+1 {
		t.Fatal("new root did not advance independent epoch")
	}
}
