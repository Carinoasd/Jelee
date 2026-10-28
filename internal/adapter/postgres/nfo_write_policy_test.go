package postgres

import (
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func nfoWritePolicyAudits(t *testing.T, f jobFixture) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='nfo.write_policy_changed'`).Scan(&n); err != nil {
		t.Fatal("count write policy audits", err)
	}
	return n
}

func TestNFOWritePolicySetAndRevoke(t *testing.T) {
	f := newJobFixture(t)
	lib := f.registration.Library.ID
	before := nfoPolicyRead(t, f)
	if before.Mode != domain.NFOModeOff || before.WriteEnabled {
		t.Fatal("new library does not start with reading and writing off")
	}
	if _, err := f.s.SetNFOWritePolicy(f.ctx, f.a, lib, true); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("write permission enabled while reading is off", err)
	}
	if _, err := f.s.SetNFOWritePolicy(f.ctx, f.a, lib, false); err != nil || nfoWritePolicyAudits(t, f) != 0 {
		t.Fatal("disabling an already disabled permission was not a silent no-op", err)
	}
	if _, err := f.s.SetNFOWritePolicy(f.ctx, f.a, "bad", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("malformed library accepted", err)
	}
	read, replay, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, lib, "read-only-key", before.Generation, domain.NFOModeReadOnly)
	if err != nil || replay || read.WriteEnabled || read.Mode != domain.NFOModeReadOnly {
		t.Fatal("enable reading failed", err)
	}
	on, err := f.s.SetNFOWritePolicy(f.ctx, f.a, lib, true)
	if err != nil || !on.WriteEnabled || on.Mode != domain.NFOModeReadOnly || on.Generation != read.Generation {
		t.Fatal("write permission did not enable without touching the generation", err)
	}
	again, err := f.s.SetNFOWritePolicy(f.ctx, f.a, lib, true)
	if err != nil || again != on || nfoWritePolicyAudits(t, f) != 1 {
		t.Fatal("same state was not a no-op", err)
	}
	if got := nfoPolicyRead(t, f); got != on {
		t.Fatal("stored policy differs from the returned one")
	}
	off, err := f.s.SetNFOWritePolicy(f.ctx, f.a, lib, false)
	if err != nil || off.WriteEnabled || off.Generation != read.Generation || nfoWritePolicyAudits(t, f) != 2 {
		t.Fatal("write permission did not disable", err)
	}
	if _, err = f.s.SetNFOWritePolicy(f.ctx, f.a, lib, true); err != nil {
		t.Fatal(err)
	}
	cleared, _, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, lib, "off-key", read.Generation, domain.NFOModeOff)
	if err != nil || cleared.WriteEnabled || cleared.Mode != domain.NFOModeOff || cleared.Generation != read.Generation+1 {
		t.Fatal("turning reading off kept the write permission", err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_write_enabled=true WHERE id=$1::uuid`, lib)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatal("check constraint did not tie writing to reading", err)
	}
}

func TestNFOWriteJobAdmissionNeedsWritePermission(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	p, _, err := service.Prepare(f.ctx, f.a, "policy-source", request)
	if err != nil {
		t.Fatal(err)
	}
	lib := f.registration.Library.ID
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_write_enabled=false WHERE id=$1::uuid`, lib); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitNFOWriteJob(f.ctx, f.a, lib, "policy-job", domain.JobPriorityManual, []string{p.ID}, f.policy); !errors.Is(err, domain.ErrNFOWriteDisabled) {
		t.Fatal("write job admitted without the write permission", err)
	}
	var jobs int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM jobs WHERE kind='nfo_write'`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("refused admission left a job", err)
	}
	if _, err = f.s.SetNFOWritePolicy(f.ctx, f.a, lib, true); err != nil {
		t.Fatal(err)
	}
	job, replay, err := f.s.SubmitNFOWriteJob(f.ctx, f.a, lib, "policy-job", domain.JobPriorityManual, []string{p.ID}, f.policy)
	if err != nil || replay || job.Kind != domain.JobNFOWrite {
		t.Fatal("write job refused with the write permission", err)
	}
}

func TestNFOWriteRevokedMidJobStopsCommitButNotRollback(t *testing.T) {
	f, l, _ := nfoCommitFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_write_enabled=false`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1); err == nil {
		t.Fatal("commit began after the write permission was revoked")
	}

	f, l, p, token := settlementFixture(t)
	settlementReadyFixture(t, f, l, p, token)
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitBackedUp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_write_enabled=false`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitReplaced); err == nil {
		t.Fatal("replacement settled after the write permission was revoked")
	}
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitRolledBack); err != nil {
		t.Fatal("rollback depended on the write permission", err)
	}

	f, l, p = nfoCommitFixture(t)
	r, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_write_enabled=false`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, r.Token, commitPlanFixture(p)); err == nil {
		t.Fatal("file plan saved after the write permission was revoked")
	}
}

func TestNFOWritePolicyMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	jobMetricMigration(t, f, "down", downgradeAboveMigration(t, f, "nfo_write_policy")-1)
	var removed, restored bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='libraries'::regclass AND attname='nfo_write_enabled' AND NOT attisdropped)`).Scan(&removed); err != nil || !removed {
		t.Fatal("empty downgrade left the write column", err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT position('nfo_write_enabled' IN prosrc)=0 AND to_regprocedure('check_nfo_commit_catalog_v86(text,uuid,integer)') IS NULL FROM pg_proc WHERE oid='check_nfo_commit_catalog(text,uuid,integer)'::regprocedure`).Scan(&restored); err != nil || !restored {
		t.Fatal("downgrade did not restore the catalog guard", err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("round-trip schema is not ready", err)
	}
}
