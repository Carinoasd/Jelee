package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// The mutations below model persisted pre-migration rows and damaged frontier
// records. They never change source files or run against a shared schema.
func TestInventoryScopeIncomparableBaselineCanRecover(t *testing.T) {
	for _, mode := range []string{"unknown-attributes", "mixed-attributes", "old-root-epoch", "mixed-epochs"} {
		t.Run(mode, func(t *testing.T) {
			f := newJobFixture(t)
			all := []domain.InventoryEntry{imageEntry("a.jpg", "image", 7, 10), imageEntry("b.jpg", "image", 7, 10)}
			finishImageInventory(t, f, startImageInventory(t, f, "baseline", all, true, 0), domain.JobSucceeded, "")
			var err error
			switch mode {
			case "unknown-attributes":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET attributes_known=false,kind=NULL,size=NULL,modified_unix_nano=NULL,inventory_generation=NULL`)
			case "mixed-attributes":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET attributes_known=false,kind=NULL,size=NULL,modified_unix_nano=NULL,inventory_generation=NULL WHERE path='b.jpg'`)
			case "old-root-epoch", "mixed-epochs":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET path=path||'-new-scope' WHERE library_id=$1::uuid`, f.registration.Library.ID)
				if err == nil && mode == "mixed-epochs" {
					_, err = f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline b SET inventory_generation=l.inventory_generation FROM libraries l WHERE b.library_id=l.id AND b.path='a.jpg'`)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			// b.jpg is absent from a complete new observation. Its old attributes
			// or scope cannot authorize either a general or an image removal.
			l := startImageInventory(t, f, "new-observation", all[:1], true, 0)
			p := finishImageInventory(t, f, l, domain.JobSucceeded, "")
			j := f.get(t, l.Job.ID)
			if j.Missing != 0 || j.ReviewRequired || p.Missing != 0 || p.ComparisonComplete {
				t.Fatalf("incomparable old baseline claimed missing or blocked recovery: job=%+v images=%+v", j, p)
			}
			var total, knownCurrent int64
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE b.attributes_known AND b.inventory_generation=l.inventory_generation AND b.path='a.jpg' AND b.kind='image' AND b.size=7 AND b.modified_unix_nano=10) FROM library_inventory_baseline b JOIN libraries l ON l.id=b.library_id WHERE b.library_id=$1::uuid`, j.LibraryID).Scan(&total, &knownCurrent); err != nil {
				t.Fatal(err)
			}
			if total != 1 || knownCurrent != 1 {
				t.Fatal("complete new observation did not establish a known current baseline", total, knownCurrent)
			}
			warm := finishImageInventory(t, f, startImageInventory(t, f, "warm", all[:1], true, 0), domain.JobSucceeded, "")
			if warm != (domain.ImageProgress{Unchanged: 1, ComparisonComplete: true}) {
				t.Fatal("recovered baseline remained incomparable", warm)
			}
		})
	}
}

func TestInventoryScopeEmptyBaselineIsComparable(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "one-image"
		if empty {
			name = "empty-library"
		}
		t.Run(name, func(t *testing.T) {
			f := newJobFixture(t)
			var entries []domain.InventoryEntry
			if !empty {
				entries = []domain.InventoryEntry{imageEntry("a.jpg", "image", 7, 10)}
			}
			l := startImageInventory(t, f, "cold", entries, true, 0)
			p := finishImageInventory(t, f, l, domain.JobSucceeded, "")
			if !p.ComparisonComplete || p.Missing != 0 || p.Added != int64(len(entries)) || f.get(t, l.Job.ID).ReviewRequired {
				t.Fatal("empty baseline was treated as an unknown historical scope", p)
			}
		})
	}
}

func TestInventoryScopeIncompleteRootCoverageCannotPublish(t *testing.T) {
	for _, mode := range []string{"missing-root", "all-roots-deleted", "foreign-root", "root-has-parent", "pending-root"} {
		t.Run(mode, func(t *testing.T) {
			f := newJobFixture(t)
			all := []domain.InventoryEntry{imageEntry("a.jpg", "image", 7, 10)}
			finishImageInventory(t, f, startImageInventory(t, f, "baseline", all, true, 0), domain.JobSucceeded, "")
			var secondRoot string
			if mode == "missing-root" {
				if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, f.registration.Library.ID, t.TempDir()).Scan(&secondRoot); err != nil {
					t.Fatal(err)
				}
			}
			f.submit(t, "coverage")
			l := f.claim(t, "scope-worker")
			for {
				d, err := f.s.NextScanDirectory(f.ctx, l)
				if errors.Is(err, domain.ErrNotFound) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "new.mkv", 8)}, Done: true}); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch mode {
			case "missing-root":
				_, err = f.s.Pool.Exec(f.ctx, `DELETE FROM job_directories WHERE job_id=$1::uuid AND root_id=$2::uuid AND path='.'`, l.Job.ID, secondRoot)
			case "all-roots-deleted":
				_, err = f.s.Pool.Exec(f.ctx, `DELETE FROM job_directories WHERE job_id=$1::uuid`, l.Job.ID)
			case "foreign-root":
				foreign, e := f.s.RegisterLibrary(f.ctx, "unrelated", t.TempDir())
				if e != nil {
					t.Fatal(e)
				}
				_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO job_directories(job_id,root_id,path,done) SELECT $1::uuid,id,'.',true FROM library_roots WHERE library_id=$2::uuid`, l.Job.ID, foreign.Library.ID)
			case "root-has-parent":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_directories SET parent_path='unexpected' WHERE job_id=$1::uuid AND path='.'`, l.Job.ID)
			case "pending-root":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_directories SET done=false WHERE job_id=$1::uuid AND path='.'`, l.Job.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := nfoSnapshot(t, nfoFixture{jobFixture: f})
			if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrConflict) {
				t.Fatal("incomplete or foreign root coverage was accepted", err)
			}
			if after := nfoSnapshot(t, nfoFixture{jobFixture: f}); after != before {
				t.Fatal("rejected root coverage changed persisted state")
			}
		})
	}
}

func TestInventoryScopeDirectorySkippedCannotBeHiddenByJobCounter(t *testing.T) {
	f := newJobFixture(t)
	all := []domain.InventoryEntry{imageEntry("a.jpg", "image", 7, 10)}
	finishImageInventory(t, f, startImageInventory(t, f, "baseline", all, true, 0), domain.JobSucceeded, "")
	before := imageBaseline(t, f)
	all[0].Size++
	l := startImageInventory(t, f, "skipped-directory", all, true, 0)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE job_directories SET skipped=1 WHERE job_id=$1::uuid AND path='.'`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	p := finishImageInventory(t, f, l, domain.JobSucceeded, "")
	j := f.get(t, l.Job.ID)
	if j.Skipped != 0 || !j.ReviewRequired || j.Missing != 0 || p.ComparisonComplete || p.Missing != 0 || imageBaseline(t, f) != before {
		t.Fatal("directory skip was hidden by stale job counter", j, p)
	}
}

func TestInventoryScopeRootChangedAfterObservationRollsBack(t *testing.T) {
	f := newJobFixture(t)
	all := []domain.InventoryEntry{imageEntry("a.jpg", "image", 7, 10)}
	finishImageInventory(t, f, startImageInventory(t, f, "baseline", all, true, 0), domain.JobSucceeded, "")
	l := startImageInventory(t, f, "observed", nil, true, 0)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET path=path||'-after-scan' WHERE library_id=$1::uuid`, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	before := nfoSnapshot(t, nfoFixture{jobFixture: f})
	if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrInventoryInvalidated) {
		t.Fatal("changed root scope was silently rebound", err)
	}
	if after := nfoSnapshot(t, nfoFixture{jobFixture: f}); after != before {
		t.Fatal("invalidated root scope partially published")
	}
}

func TestInventoryScopeLegacyJobCannotBindCurrentEpoch(t *testing.T) {
	f := newJobFixture(t)
	all := []domain.InventoryEntry{imageEntry("a.jpg", "image", 7, 10), imageEntry("b.jpg", "image", 7, 10)}
	finishImageInventory(t, f, startImageInventory(t, f, "baseline", all, true, 0), domain.JobSucceeded, "")
	before := imageBaseline(t, f)
	l := startImageInventory(t, f, "legacy", all[:1], true, 0)
	// A pre-007 job has no frozen epoch. Disable only the immutable-update
	// trigger inside this isolated fixture to reproduce its persisted shape.
	if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs DISABLE TRIGGER job_inventory_generation_immutable; UPDATE jobs SET inventory_generation=NULL WHERE state='running'; SET CONSTRAINTS ALL IMMEDIATE; ALTER TABLE jobs ENABLE TRIGGER job_inventory_generation_immutable`); err != nil {
		t.Fatal(err)
	}
	p := finishImageInventory(t, f, l, domain.JobSucceeded, "")
	j := f.get(t, l.Job.ID)
	if j.Missing != 0 || !j.ReviewRequired || p != (domain.ImageProgress{Uncompared: 1}) || imageBaseline(t, f) != before {
		t.Fatal("legacy job claimed removals or republished without a captured epoch", j, p)
	}
	var epoch *int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT inventory_generation FROM jobs WHERE id=$1::uuid`, l.Job.ID).Scan(&epoch); err != nil || epoch != nil {
		t.Fatal("legacy job acquired a new epoch", epoch, err)
	}
}

func TestInventoryScopeSameScopeMissingAndKindTransition(t *testing.T) {
	f := newJobFixture(t)
	f.policy.MissingCountLimit, f.policy.MissingPercentLimit = 100, 100
	all := []domain.InventoryEntry{imageEntry("gone.jpg", "image", 7, 10), imageEntry("type.jpg", "image", 7, 10), imageEntry("stay.jpg", "image", 7, 10)}
	finishImageInventory(t, f, startImageInventory(t, f, "baseline", all, true, 0), domain.JobSucceeded, "")
	current := []domain.InventoryEntry{imageEntry("type.jpg", "video", 7, 10), all[2]}
	l := startImageInventory(t, f, "changed", current, true, 0)
	p := finishImageInventory(t, f, l, domain.JobSucceeded, "")
	j := f.get(t, l.Job.ID)
	if j.Missing != 1 || j.ReviewRequired || p != (domain.ImageProgress{Unchanged: 1, Missing: 2, ComparisonComplete: true}) {
		t.Fatal("same-scope missing or image-to-video classification changed", j, p)
	}
	warm := finishImageInventory(t, f, startImageInventory(t, f, "warm", current, true, 0), domain.JobSucceeded, "")
	if warm != (domain.ImageProgress{Unchanged: 1, ComparisonComplete: true}) {
		t.Fatal("accepted same-scope observation did not become baseline", warm)
	}
}

func TestInventoryScopeRebaselineFinalLeaseExpiryRollsBack(t *testing.T) {
	f := newJobFixture(t)
	all := []domain.InventoryEntry{imageEntry("a.jpg", "image", 7, 10)}
	finishImageInventory(t, f, startImageInventory(t, f, "baseline", all, true, 0), domain.JobSucceeded, "")
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET attributes_known=false,kind=NULL,size=NULL,modified_unix_nano=NULL,inventory_generation=NULL`); err != nil {
		t.Fatal(err)
	}
	l := startImageInventory(t, f, "rebaseline", all, true, 0)
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE inventory_scope_finish_seen; CREATE FUNCTION inventory_scope_finish_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='succeeded' THEN PERFORM nextval('inventory_scope_finish_seen'); PERFORM pg_sleep(0.5); END IF; RETURN NEW; END $$; CREATE TRIGGER inventory_scope_finish_pause BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION inventory_scope_finish_pause()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '350 milliseconds' WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	before := nfoSnapshot(t, nfoFixture{jobFixture: f})
	started := time.Now()
	err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, "")
	var entered bool
	if e := f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM inventory_scope_finish_seen`).Scan(&entered); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(err, domain.ErrJobLeaseLost) || !entered || time.Since(started) < 500*time.Millisecond {
		t.Fatal("final lease expiry was not observed after entering terminal update", err, entered)
	}
	if after := nfoSnapshot(t, nfoFixture{jobFixture: f}); after != before {
		t.Fatal("expired rebaseline transaction changed persisted state")
	}
}
