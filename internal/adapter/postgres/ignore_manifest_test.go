package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func manifestFixture(t *testing.T, setup ...func(*testing.T, jobFixture)) (jobFixture, domain.JobLease, domain.IgnoreDirectoryProof) {
	t.Helper()
	f := newJobFixture(t, setup...)
	f.policy.MaxDirectories = 1000
	j := ignoreSubmit(t, f, "manifest")
	l := ignoreManufacturedLease(t, f, j.ID)
	p := domain.IgnoreDirectoryProof{RootID: f.registration.RootID, Directory: ".", Identity: [32]byte{1}, RulePresent: true, RuleIdentity: [32]byte{2}, RuleSize: 4, RuleModifiedNano: 123, RuleSHA256: [32]byte{3}}
	return f, l, p
}

func manifestChild(root domain.IgnoreDirectoryProof, name string) domain.IgnoreDirectoryProof {
	return domain.IgnoreDirectoryProof{RootID: root.RootID, Directory: name, ParentIdentity: root.Identity, Identity: [32]byte{4}}
}

func manifestCounts(t *testing.T, f jobFixture, id string) (int64, int64, int64, bool) {
	t.Helper()
	var count, bytes, charge int64
	var invalid bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT rows,source_bytes,charge_bytes,invalidated FROM job_ignore_manifests WHERE job_id=$1::uuid`, id).Scan(&count, &bytes, &charge, &invalid); err != nil {
		t.Fatal(err)
	}
	return count, bytes, charge, invalid
}

func TestIgnoreManifestReplayFreezeAndBoundedPages(t *testing.T) {
	f, l, root := manifestFixture(t)
	if _, err := f.s.ReadIgnoreProofPage(f.ctx, l, domain.IgnoreProofCursor{}); err == nil {
		t.Fatal("unrecorded manifest readable")
	}
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReadIgnoreProofPage(f.ctx, l, domain.IgnoreProofCursor{}); err != domain.ErrConflict {
		t.Fatal("mutable manifest readable", err)
	}
	all := []domain.IgnoreDirectoryProof{root}
	for i := 0; i < 260; i++ {
		all = append(all, manifestChild(root, fmt.Sprintf("d-%03d", i)))
	}
	for start := 1; start < len(all); start += 128 {
		end := min(start+128, len(all))
		if err := f.s.RecordIgnoreProofs(f.ctx, l, all[start:end]); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.RecordIgnoreProofs(f.ctx, l, all[:128]); err != nil {
		t.Fatal("replay", err)
	}
	count, bytes, _, invalid := manifestCounts(t, f, l.Job.ID)
	if count != 261 || bytes != 4 || invalid {
		t.Fatal("replay changed accounting")
	}
	if err := f.s.FreezeIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FreezeIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal("freeze replay", err)
	}
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != nil {
		t.Fatal("frozen exact replay", err)
	}
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{manifestChild(root, "new")}); err != domain.ErrConflict {
		t.Fatal("frozen append", err)
	}
	var got []domain.IgnoreDirectoryProof
	var cursor domain.IgnoreProofCursor
	for page := 0; page < 4; page++ {
		items, err := f.s.ReadIgnoreProofPage(f.ctx, l, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 128 {
			t.Fatal("unbounded page")
		}
		if len(items) == 0 {
			break
		}
		got = append(got, items...)
		last := items[len(items)-1]
		cursor = domain.IgnoreProofCursor{RootID: last.RootID, Directory: last.Directory}
	}
	if !reflect.DeepEqual(got, all) {
		t.Fatal("keyset lost or repeated rows")
	}
	// Evidence APIs never authorize successful filtered execution.
	if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != domain.ErrIgnoreUnavailable {
		t.Fatal("manifest bypassed execution guard", err)
	}
}

func TestIgnoreManifestConflictInvalidatesWithoutNewPrefix(t *testing.T) {
	f, l, root := manifestFixture(t)
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != nil {
		t.Fatal(err)
	}
	changed := root
	changed.RuleSHA256[0]++
	err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{manifestChild(root, "new-prefix"), changed})
	if err != domain.ErrInventoryInvalidated {
		t.Fatal("changed source", err)
	}
	count, bytes, charge, invalid := manifestCounts(t, f, l.Job.ID)
	var physical int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_ignore_proofs WHERE job_id=$1::uuid`, l.Job.ID).Scan(&physical); err != nil {
		t.Fatal(err)
	}
	if count != 1 || physical != 1 || bytes != 4 || charge != root.Charge() || !invalid {
		t.Fatal("conflict retained partial batch or lost invalidation")
	}
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != domain.ErrInventoryInvalidated {
		t.Fatal("stale replay revived invalid manifest", err)
	}
	if err := f.s.FreezeIgnoreManifest(f.ctx, l); err != domain.ErrInventoryInvalidated {
		t.Fatal("invalid manifest frozen", err)
	}
}

func TestIgnoreManifestParentsMissingDirectoriesAndRootOwnership(t *testing.T) {
	for _, mode := range []string{"no-parent", "wrong-parent", "missing-parent", "foreign-root"} {
		t.Run(mode, func(t *testing.T) {
			f, l, root := manifestFixture(t)
			if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != nil {
				t.Fatal(err)
			}
			child := manifestChild(root, "child")
			batch := []domain.IgnoreDirectoryProof{child}
			switch mode {
			case "no-parent":
				child.Directory = "missing/child"
				batch = []domain.IgnoreDirectoryProof{child}
			case "wrong-parent":
				child.ParentIdentity[0]++
				batch = []domain.IgnoreDirectoryProof{child}
			case "missing-parent":
				child.MissingDirectory = true
				child.Identity = [32]byte{}
				nested := manifestChild(root, "child/nested")
				batch = []domain.IgnoreDirectoryProof{child, nested}
			case "foreign-root":
				other, err := f.s.RegisterLibrary(f.ctx, "Other manifest library", t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				child = root
				child.RootID = other.RootID
				batch = []domain.IgnoreDirectoryProof{child}
			}
			if err := f.s.RecordIgnoreProofs(f.ctx, l, batch); err != domain.ErrConflict {
				t.Fatal(mode, err)
			}
			count, _, _, invalid := manifestCounts(t, f, l.Job.ID)
			if count != 1 || invalid {
				t.Fatal("invalid input mutated ledger")
			}
		})
	}
	f, l, root := manifestFixture(t)
	missing := manifestChild(root, "gone")
	missing.MissingDirectory = true
	missing.Identity = [32]byte{}
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root, missing}); err != nil {
		t.Fatal("explicit child absence", err)
	}
	if err := f.s.FreezeIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	page, err := f.s.ReadIgnoreProofPage(f.ctx, l, domain.IgnoreProofCursor{})
	if err != nil || len(page) != 2 || page[1] != missing {
		t.Fatal("missing child proof changed", err)
	}
}

func TestIgnoreManifestLeaseCancellationEpochAndQuota(t *testing.T) {
	for _, mode := range []string{"expired", "generation", "cancelled", "epoch", "quota"} {
		t.Run(mode, func(t *testing.T) {
			f, l, root := manifestFixture(t)
			if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != nil {
				t.Fatal(err)
			}
			var query string
			want := domain.ErrJobLeaseLost
			switch mode {
			case "expired":
				query = `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`
			case "generation":
				l.Generation++
			case "cancelled":
				query = `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`
				want = context.Canceled
			case "epoch":
				query = `UPDATE libraries SET inventory_generation=inventory_generation+1 WHERE id=(SELECT library_id FROM jobs WHERE id=$1::uuid)`
				want = domain.ErrInventoryInvalidated
			case "quota":
				query = `UPDATE jobs SET max_directories=1 WHERE id=$1::uuid`
				want = domain.ErrScanLimit
			}
			if query != "" {
				if _, err := f.s.Pool.Exec(f.ctx, query, l.Job.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{manifestChild(root, "new")}); !errors.Is(err, want) {
				t.Fatal(mode, err)
			}
			count, _, _, invalid := manifestCounts(t, f, l.Job.ID)
			if count != 1 || invalid {
				t.Fatal("failed batch mutated ledger")
			}
		})
	}
}

func TestIgnoreManifestLateLeaseExpiryRollsBack(t *testing.T) {
	f, l, root := manifestFixture(t)
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE manifest_entered; CREATE FUNCTION hold_manifest() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('manifest_entered'); PERFORM pg_sleep(0.2); RETURN NEW; END $$; CREATE TRIGGER hold_manifest BEFORE INSERT ON job_ignore_proofs FOR EACH ROW EXECUTE FUNCTION hold_manifest()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '120 milliseconds' WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{manifestChild(root, "late")}); err != domain.ErrJobLeaseLost {
		t.Fatal("late guard", err)
	}
	var entered bool
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM manifest_entered`).Scan(&entered); err != nil || !entered {
		t.Fatal("did not reach delayed write", err)
	}
	count, _, _, invalid := manifestCounts(t, f, l.Job.ID)
	if count != 1 || invalid {
		t.Fatal("expired write persisted")
	}
}

func TestIgnoreManifestReclaimRetainsProofsAndFreezeNeedsAllRoots(t *testing.T) {
	f := newJobFixture(t)
	second, err := f.s.RegisterLibrary(f.ctx, f.registration.Library.Name, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j := ignoreSubmit(t, f, "two-roots")
	l := ignoreManufacturedLease(t, f, j.ID)
	root := domain.IgnoreDirectoryProof{RootID: f.registration.RootID, Directory: ".", Identity: [32]byte{1}}
	if err = f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != nil {
		t.Fatal(err)
	}
	if err = f.s.FreezeIgnoreManifest(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("missing configured root admitted", err)
	}
	other := root
	other.RootID = second.RootID
	other.Identity = [32]byte{2}
	if err = f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{other}); err != nil {
		t.Fatal(err)
	}
	if err = f.s.FreezeIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err = f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	reclaimed := ignoreManufacturedLease(t, f, j.ID)
	if _, err = f.s.ReadIgnoreProofPage(f.ctx, l, domain.IgnoreProofCursor{}); err != domain.ErrJobLeaseLost {
		t.Fatal("old lease admitted", err)
	}
	page, err := f.s.ReadIgnoreProofPage(f.ctx, reclaimed, domain.IgnoreProofCursor{})
	if err != nil || len(page) != 2 {
		t.Fatal("reclaim lost immutable proofs", err)
	}
}

func TestIgnoreManifestMigrationPreservesLedgerOnRefusedDown(t *testing.T) {
	f, l, root := manifestFixture(t, legacyMigrationAt44)
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != nil {
		t.Fatal(err)
	}
	body, err := migrationFiles.ReadFile("migrations/000009_ignore_manifest.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.s.Pool.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, denied := c.Exec(f.ctx, string(body))
	_, rollback := c.Exec(f.ctx, "ROLLBACK")
	c.Release()
	if denied == nil || rollback != nil {
		t.Fatal("retained ledger downgrade was not rejected")
	}
	count, _, _, invalid := manifestCounts(t, f, l.Job.ID)
	if count != 1 || invalid {
		t.Fatal("refused migration changed ledger")
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal(err)
	}
	// Terminal history owns these records. Explicitly remove only this test's
	// job, as history trimming would; baseline data is independent of the job.
	if _, err = f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_ignore_proofs`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("history cleanup orphaned proofs", err)
	}
	nfoMigrateVersion(t, f, "down", 43)
	nfoMigrateVersion(t, f, "down", 42)
	nfoMigrateVersion(t, f, "down", 41)
	nfoMigrateVersion(t, f, "down", 40)
	nfoMigrateVersion(t, f, "down", 39)
	nfoMigrateVersion(t, f, "down", 38)
	nfoMigrateVersion(t, f, "down", 37)
	nfoMigrateVersion(t, f, "down", 36)
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "down", 31)
	nfoMigrateVersion(t, f, "down", 30)
	nfoMigrateVersion(t, f, "down", 29)
	nfoMigrateVersion(t, f, "down", 28)
	nfoMigrateVersion(t, f, "down", 27)
	nfoMigrateVersion(t, f, "down", 26)
	nfoMigrateVersion(t, f, "down", 25)
	nfoMigrateVersion(t, f, "down", 24)
	nfoMigrateVersion(t, f, "down", 23)
	nfoMigrateVersion(t, f, "down", 22)
	nfoMigrateVersion(t, f, "down", 21)
	nfoMigrateVersion(t, f, "down", 20)
	nfoMigrateVersion(t, f, "down", 19)
	nfoMigrateVersion(t, f, "down", 18)
	nfoMigrateVersion(t, f, "down", 17)
	nfoMigrateVersion(t, f, "down", 16)
	nfoMigrateVersion(t, f, "down", 15)
	nfoMigrateVersion(t, f, "down", 14)
	nfoMigrateVersion(t, f, "down", 13)
	nfoMigrateVersion(t, f, "down", 12)
	nfoMigrateVersion(t, f, "down", 11)
	nfoMigrateVersion(t, f, "down", 10)
	nfoMigrateVersion(t, f, "down", 9)
	nfoMigrateVersion(t, f, "down", 8)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
}

func TestIgnoreManifestConcurrentReplayAndSourceBudget(t *testing.T) {
	f, l, root := manifestFixture(t)
	root.RuleSize = 0
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { results <- f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}) }()
	}
	for i := 0; i < 4; i++ {
		if err := <-results; err != nil {
			t.Error("concurrent replay", err)
		}
	}
	count, _, _, _ := manifestCounts(t, f, l.Job.ID)
	if count != 1 {
		t.Fatal("concurrent replay duplicated evidence")
	}
	for batch := 0; batch < 2; batch++ {
		proofs := make([]domain.IgnoreDirectoryProof, 128)
		for i := range proofs {
			p := manifestChild(root, fmt.Sprintf("budget-%03d", batch*128+i))
			p.RulePresent = true
			p.RuleIdentity = [32]byte{5}
			p.RuleSHA256 = [32]byte{6}
			p.RuleSize = domain.IgnoreRuleMaxBytes
			proofs[i] = p
		}
		if err := f.s.RecordIgnoreProofs(f.ctx, l, proofs); err != nil {
			t.Fatal("valid source budget", err)
		}
	}
	count, bytes, charge, invalid := manifestCounts(t, f, l.Job.ID)
	if count != 257 || bytes != domain.IgnoreManifestMaxBytes || invalid {
		t.Fatal("source accounting boundary")
	}
	last := manifestChild(root, "overflow")
	last.RulePresent = true
	last.RuleIdentity = [32]byte{5}
	last.RuleSHA256 = [32]byte{6}
	last.RuleSize = 1
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{last}); err != domain.ErrScanLimit {
		t.Fatal("source bytes limit", err)
	}
	newCount, newBytes, newCharge, _ := manifestCounts(t, f, l.Job.ID)
	if newCount != count || newBytes != bytes || newCharge != charge {
		t.Fatal("overflow changed accounting")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_proofs SET rule_modified_nano=1 WHERE job_id=$1::uuid`, l.Job.ID); err == nil {
		t.Fatal("SQL updated immutable proof")
	}
}

func TestIgnoreManifestRejectsOffJobAndInvalidBatch(t *testing.T) {
	f := newJobFixture(t)
	f.submit(t, "off-manifest")
	l := f.claim(t, "off-owner")
	p := domain.IgnoreDirectoryProof{RootID: f.registration.RootID, Directory: ".", Identity: [32]byte{1}}
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{p}); err != domain.ErrConflict {
		t.Fatal("off job accepted proofs", err)
	}
	for _, batch := range [][]domain.IgnoreDirectoryProof{nil, make([]domain.IgnoreDirectoryProof, 129), {{RootID: p.RootID, Directory: "../escape"}}} {
		if err := f.s.RecordIgnoreProofs(f.ctx, l, batch); err != domain.ErrInvalid {
			t.Fatal("invalid batch", err)
		}
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_ignore_manifests`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejection persisted header", err)
	}
}

func TestIgnoreManifestPagePlanReadsOnlyRequestedPrefix(t *testing.T) {
	f, l, root := manifestFixture(t)
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != nil {
		t.Fatal(err)
	}
	// Populate this isolated schema directly to measure the actual production
	// query on a 10,001-row manifest without 79 unrelated API round trips.
	child := manifestChild(root, "d-00001")
	_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_proofs(job_id,root_id,directory,parent_path,parent_identity,identity,missing_directory,rule_present,rule_identity,rule_size,rule_modified_nano,rule_sha256)
 SELECT $1::uuid,$2::uuid,'d-'||lpad(n::text,5,'0'),'.',$3,$4,false,false,decode(repeat('00',32),'hex'),0,0,decode(repeat('00',32),'hex') FROM generate_series(1,10000)n`, l.Job.ID, root.RootID, root.Identity[:], child.Identity[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET max_directories=20000 WHERE id=$1::uuid;`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_manifests SET rows=10001,charge_bytes=$2 WHERE job_id=$1::uuid`, l.Job.ID, root.Charge()+10000*child.Charge()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `ANALYZE job_ignore_proofs`); err != nil {
		t.Fatal(err)
	}
	plan := nfoWorkerPlan(t, f, listIgnoreProofsSQL, l.Job.ID, root.RootID, "d-05000", 128)
	if plan.Type != "Limit" || plan.Rows != 128 {
		t.Fatal("unbounded query result")
	}
	visited := float64(0)
	indexed := false
	walkNFOWorkerPlan(plan, func(p nfoWorkerPlanNode) {
		if p.Type == "Sort" || p.Type == "Incremental Sort" {
			t.Error("page sorts the remaining manifest")
		}
		if p.Relation == "job_ignore_proofs" {
			visited += (p.Rows + p.Removed) * p.Loops
			indexed = p.Index == "job_ignore_proofs_pkey"
		}
	})
	if !indexed || visited > 128 {
		t.Fatalf("page scan indexed=%t visited=%g", indexed, visited)
	}
	t.Logf("10,001 proofs: cursor page visited %g rows for limit 128", visited)
}
