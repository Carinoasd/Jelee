package postgres

import (
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func nfoMigrationDenied(t *testing.T, f jobFixture, name string) {
	t.Helper()
	body, err := migrationFiles.ReadFile("migrations/" + name)
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
		t.Fatal("migration guard did not reject atomically")
	}
}
func nfoMigrateVersion(t *testing.T, f jobFixture, action string, want uint) {
	t.Helper()
	v, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), action)
	if err != nil || dirty || v != want {
		t.Fatal("NFO worker migration version", v, dirty, err)
	}
}
func TestNFOWorkerUpgradeRejectsAllActiveLegacyReadOnlyPhases(t *testing.T) {
	for _, state := range []string{domain.NFOPhaseWaiting, domain.NFOPhaseRunning, domain.NFOPhaseDone, domain.NFOPhaseAborted} {
		t.Run(state, func(t *testing.T) {
			f := newNFOFixture(t)
			j := f.submit(t, "historical")
			l := f.claim(t, "before-upgrade")
			if err := f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
				t.Fatal(err)
			}
			nfoMigrateVersion(t, f.jobFixture, "down", 36)
			nfoMigrateVersion(t, f.jobFixture, "down", 35)
			nfoMigrateVersion(t, f.jobFixture, "down", 34)
			nfoMigrateVersion(t, f.jobFixture, "down", 33)
			nfoMigrateVersion(t, f.jobFixture, "down", 32)
			nfoMigrateVersion(t, f.jobFixture, "down", 31)
			nfoMigrateVersion(t, f.jobFixture, "down", 30)
			nfoMigrateVersion(t, f.jobFixture, "down", 29)
			nfoMigrateVersion(t, f.jobFixture, "down", 28)
			nfoMigrateVersion(t, f.jobFixture, "down", 27)
			nfoMigrateVersion(t, f.jobFixture, "down", 26)
			nfoMigrateVersion(t, f.jobFixture, "down", 25)
			nfoMigrateVersion(t, f.jobFixture, "down", 24)
			nfoMigrateVersion(t, f.jobFixture, "down", 23)
			nfoMigrateVersion(t, f.jobFixture, "down", 22)
			nfoMigrateVersion(t, f.jobFixture, "down", 21)
			nfoMigrateVersion(t, f.jobFixture, "down", 20)
			nfoMigrateVersion(t, f.jobFixture, "down", 19)
			nfoMigrateVersion(t, f.jobFixture, "down", 18)
			nfoMigrateVersion(t, f.jobFixture, "down", 17)
			nfoMigrateVersion(t, f.jobFixture, "down", 16)
			nfoMigrateVersion(t, f.jobFixture, "down", 15)
			nfoMigrateVersion(t, f.jobFixture, "down", 14)
			nfoMigrateVersion(t, f.jobFixture, "down", 13)
			nfoMigrateVersion(t, f.jobFixture, "down", 12)
			nfoMigrateVersion(t, f.jobFixture, "down", 11)
			nfoMigrateVersion(t, f.jobFixture, "down", 10)
			nfoMigrateVersion(t, f.jobFixture, "down", 9)
			nfoMigrateVersion(t, f.jobFixture, "down", 8)
			nfoMigrateVersion(t, f.jobFixture, "down", 7)
			nfoMigrateVersion(t, f.jobFixture, "down", 6)
			code := ""
			if state == domain.NFOPhaseAborted {
				code = string(domain.NFOPhaseUnavailable)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_job_state SET phase=$2,error_code=$3 WHERE job_id=$1::uuid`, j.ID, state, code); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='queued',finished_at=NULL,error_code='' WHERE id=$1::uuid`, j.ID); err != nil {
				t.Fatal(err)
			}
			nfoMigrationDenied(t, f.jobFixture, "000007_nfo_worker.up.sql")
			var missing bool
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('nfo_job_requests') IS NULL`).Scan(&missing); err != nil || !missing {
				t.Fatal("failed upgrade left generated request table", err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='failed',finished_at=clock_timestamp(),error_code='scan_io' WHERE id=$1::uuid`, j.ID); err != nil {
				t.Fatal(err)
			}
			nfoMigrateVersion(t, f.jobFixture, "up", SchemaVersion)
			for _, optIn := range []bool{false, true} {
				got, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, j.LibraryID, "historical", domain.JobPriorityManual, domain.ScanIntent{NFO: optIn}, f.policy, nil, &f.identity)
				if !errors.Is(err, domain.ErrConflict) || got != (domain.Job{}) || replay {
					t.Fatal("missing old public intent was guessed", optIn, err)
				}
			}
			if _, _, err := f.s.RetryScanWithStages(f.ctx, f.a, j.ID, "historical-retry", f.policy, nil, &f.identity); !errors.Is(err, domain.ErrConflict) {
				t.Fatal("historical retry inferred opt-in", err)
			}
			summary, err := f.s.GetNFOJobSummary(f.ctx, f.a, j.ID)
			if err != nil || summary.Mode != domain.NFOModeReadOnly || summary.Phase != domain.NFOSummaryAborted {
				t.Fatal("historical summary inaccessible", err)
			}
		})
	}
}
func TestNFOWorkerLegacyOffCanReplayButNeverAcquireReadOnlyIntent(t *testing.T) {
	f := newNFOFixture(t)
	j := f.jobFixture.submit(t, "legacy-off")
	l := f.jobFixture.claim(t, "before-downgrade")
	if err := f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f.jobFixture, "down", 36)
	nfoMigrateVersion(t, f.jobFixture, "down", 35)
	nfoMigrateVersion(t, f.jobFixture, "down", 34)
	nfoMigrateVersion(t, f.jobFixture, "down", 33)
	nfoMigrateVersion(t, f.jobFixture, "down", 32)
	nfoMigrateVersion(t, f.jobFixture, "down", 31)
	nfoMigrateVersion(t, f.jobFixture, "down", 30)
	nfoMigrateVersion(t, f.jobFixture, "down", 29)
	nfoMigrateVersion(t, f.jobFixture, "down", 28)
	nfoMigrateVersion(t, f.jobFixture, "down", 27)
	nfoMigrateVersion(t, f.jobFixture, "down", 26)
	nfoMigrateVersion(t, f.jobFixture, "down", 25)
	nfoMigrateVersion(t, f.jobFixture, "down", 24)
	nfoMigrateVersion(t, f.jobFixture, "down", 23)
	nfoMigrateVersion(t, f.jobFixture, "down", 22)
	nfoMigrateVersion(t, f.jobFixture, "down", 21)
	nfoMigrateVersion(t, f.jobFixture, "down", 20)
	nfoMigrateVersion(t, f.jobFixture, "down", 19)
	nfoMigrateVersion(t, f.jobFixture, "down", 18)
	nfoMigrateVersion(t, f.jobFixture, "down", 17)
	nfoMigrateVersion(t, f.jobFixture, "down", 16)
	nfoMigrateVersion(t, f.jobFixture, "down", 15)
	nfoMigrateVersion(t, f.jobFixture, "down", 14)
	nfoMigrateVersion(t, f.jobFixture, "down", 13)
	nfoMigrateVersion(t, f.jobFixture, "down", 12)
	nfoMigrateVersion(t, f.jobFixture, "down", 11)
	nfoMigrateVersion(t, f.jobFixture, "down", 10)
	nfoMigrateVersion(t, f.jobFixture, "down", 9)
	nfoMigrateVersion(t, f.jobFixture, "down", 8)
	nfoMigrateVersion(t, f.jobFixture, "down", 7)
	nfoMigrateVersion(t, f.jobFixture, "down", 6)
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_job_state WHERE job_id=$1::uuid`, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='queued',finished_at=NULL,error_code='' WHERE id=$1::uuid`, j.ID); err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f.jobFixture, "up", SchemaVersion)
	if replay, wasReplay, err := f.s.SubmitJob(f.ctx, f.a, j.LibraryID, "legacy-off", domain.JobPriorityManual, f.policy); err != nil || !wasReplay || replay.ID != j.ID {
		t.Fatal("legacy off replay", err)
	}
	l = f.jobFixture.claim(t, "legacy-worker")
	p, err := f.s.PrepareNFOPhase(f.ctx, l, f.identity)
	if err != nil || !frozenNFOOff(p) {
		t.Fatal("legacy entry acquired new read-only intent", err)
	}
	d := f.directory(t, l)
	e := scanEntry(d, "legacy.jpg", 9)
	e.Kind = "image"
	if err = f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: []domain.InventoryEntry{e}, Done: true}); err != nil {
		t.Fatal(err)
	}
	if p := finishImageInventory(t, f.jobFixture, l, domain.JobSucceeded, ""); p != (domain.ImageProgress{Uncompared: 1}) {
		t.Fatal("unknown legacy root epoch compared", p)
	}
	if imageBaseline(t, f.jobFixture) != "[]" {
		t.Fatal("legacy unknown epoch published baseline")
	}
}
func TestNFOWorkerDownGuardIncludesFrozenOffAndPreservesBCache(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "cache", "a.nfo")
	f.parseHead(t, l, nfoValidSummary())
	f.finish(t, l)
	f.jobFixture.submit(t, "off-active")
	before := nfoSnapshot(t, f)
	nfoMigrationDenied(t, f.jobFixture, "000007_nfo_worker.down.sql")
	if before != nfoSnapshot(t, f) {
		t.Fatal("down guard changed active off job")
	}
	l = f.jobFixture.claim(t, "off-finish")
	if err := f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal(err)
	}
	const retainedSQL = `SELECT jsonb_build_object('cache',(SELECT jsonb_agg(to_jsonb(c)-'observation_id' ORDER BY root_id,relative_path) FROM nfo_cache c),'phase',(SELECT jsonb_agg(to_jsonb(p) ORDER BY job_id) FROM nfo_job_state p),'quota',(SELECT to_jsonb(q) FROM nfo_cache_quota q),'libraries',(SELECT jsonb_agg(to_jsonb(q) ORDER BY library_id) FROM nfo_library_quota q),'policy',(SELECT jsonb_agg(jsonb_build_array(id,nfo_mode,nfo_generation) ORDER BY id) FROM libraries),'baseline',(SELECT jsonb_agg(jsonb_build_array(library_id,root_id,path) ORDER BY library_id,root_id,path) FROM library_inventory_baseline))::text`
	var retained, after string
	if err := f.s.Pool.QueryRow(f.ctx, retainedSQL).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f.jobFixture, "down", 36)
	nfoMigrateVersion(t, f.jobFixture, "down", 35)
	nfoMigrateVersion(t, f.jobFixture, "down", 34)
	nfoMigrateVersion(t, f.jobFixture, "down", 33)
	nfoMigrateVersion(t, f.jobFixture, "down", 32)
	nfoMigrateVersion(t, f.jobFixture, "down", 31)
	nfoMigrateVersion(t, f.jobFixture, "down", 30)
	nfoMigrateVersion(t, f.jobFixture, "down", 29)
	nfoMigrateVersion(t, f.jobFixture, "down", 28)
	nfoMigrateVersion(t, f.jobFixture, "down", 27)
	nfoMigrateVersion(t, f.jobFixture, "down", 26)
	nfoMigrateVersion(t, f.jobFixture, "down", 25)
	nfoMigrateVersion(t, f.jobFixture, "down", 24)
	nfoMigrateVersion(t, f.jobFixture, "down", 23)
	nfoMigrateVersion(t, f.jobFixture, "down", 22)
	nfoMigrateVersion(t, f.jobFixture, "down", 21)
	nfoMigrateVersion(t, f.jobFixture, "down", 20)
	nfoMigrateVersion(t, f.jobFixture, "down", 19)
	nfoMigrateVersion(t, f.jobFixture, "down", 18)
	nfoMigrateVersion(t, f.jobFixture, "down", 17)
	nfoMigrateVersion(t, f.jobFixture, "down", 16)
	nfoMigrateVersion(t, f.jobFixture, "down", 15)
	nfoMigrateVersion(t, f.jobFixture, "down", 14)
	nfoMigrateVersion(t, f.jobFixture, "down", 13)
	nfoMigrateVersion(t, f.jobFixture, "down", 12)
	nfoMigrateVersion(t, f.jobFixture, "down", 11)
	nfoMigrateVersion(t, f.jobFixture, "down", 10)
	nfoMigrateVersion(t, f.jobFixture, "down", 9)
	nfoMigrateVersion(t, f.jobFixture, "down", 8)
	nfoMigrateVersion(t, f.jobFixture, "down", 7)
	nfoMigrateVersion(t, f.jobFixture, "down", 6)
	if err := f.s.Pool.QueryRow(f.ctx, retainedSQL).Scan(&after); err != nil || after != retained {
		t.Fatal("007 downgrade changed B cache/policy/baseline", err)
	}
	nfoMigrateVersion(t, f.jobFixture, "up", SchemaVersion)
	if err := f.s.Pool.QueryRow(f.ctx, retainedSQL).Scan(&after); err != nil || after != retained {
		t.Fatal("007 upgrade changed retained B data", err)
	}
}
func TestNFOFrozenOffRejectsCorruptProgress(t *testing.T) {
	f := newNFOFixture(t)
	j := f.jobFixture.submit(t, "off-shape")
	l := f.jobFixture.claim(t, "off-worker")
	// The normal database constraint already rejects this corruption. Remove
	// that one constraint only in this private schema to exercise read defense.
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_job_state SET processed=1,unavailable=1 WHERE job_id=$1::uuid`, j.ID); err == nil {
		t.Fatal("database admitted corrupt off progress")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE nfo_job_state DROP CONSTRAINT nfo_job_state_check4`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_job_state SET processed=1,unavailable=1 WHERE job_id=$1::uuid`, j.ID); err != nil {
		t.Fatal(err)
	}
	if p, err := f.s.GetNFOJobSummary(f.ctx, f.a, j.ID); !errors.Is(err, domain.ErrDatabase) || p != (domain.NFOJobSummary{}) {
		t.Fatal("corrupt off progress was silently erased", err)
	}
	if _, err := f.s.NextScanDirectory(f.ctx, l); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("corrupt off phase admitted inventory", err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("corrupt off phase allowed success", err)
	}
}
