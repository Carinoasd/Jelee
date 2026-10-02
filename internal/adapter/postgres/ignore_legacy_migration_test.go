package postgres

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestLegacyIgnoreMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	var tables int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('job_ignore_legacy_manifests','job_ignore_legacy_proofs','job_ignore_legacy_queries')`).Scan(&tables); err != nil || tables != 3 {
		t.Fatal("legacy schema missing", err)
	}
}
func TestLegacyIgnoreMigrationGuardsAndCleanup(t *testing.T) {
	f := newJobFixture(t)
	job := ignoreSubmit(t, f, "legacy-storage")
	// The old immutable request must not acquire new-family state.
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_legacy_manifests(job_id,inventory_generation) VALUES($1::uuid,1)`, job.ID); err == nil {
		t.Fatal("old intent accepted legacy manifest")
	}
	// Manufacture only a future retained request to test the reserved schema;
	// public admission and current claims must still reject this mode.
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM job_ignore_requests WHERE job_id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_requests(job_id,library_id,mode,case_mode,program_version,proof_version) VALUES($1::uuid,$2::uuid,'jeleeignore-legacy-v1','sensitive','jeleeignore-v1','jeleeignore-proof-v1')`, job.ID, f.registration.Library.ID); err == nil {
		t.Fatal("mixed contract tuple accepted")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_requests(job_id,library_id,mode,case_mode,program_version,proof_version) VALUES($1::uuid,$2::uuid,'jeleeignore-legacy-v1','sensitive','jeleeignore-legacy-v1','jeleeignore-legacy-proof-v1')`, job.ID, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_legacy_manifests(job_id,inventory_generation) VALUES($1::uuid,1)`, job.ID); err != nil {
		t.Fatal(err)
	}
	zero := [32]byte{}
	identity := [32]byte{1}
	digest := [32]byte{2}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_legacy_proofs(job_id,root_id,directory,parent_identity,identity,checked,rule_present,rule_identity,rule_size,rule_modified_nano,rule_sha256) VALUES($1::uuid,$2::uuid,'.',$3,$4,false,false,$3,0,0,$3)`, job.ID, f.registration.RootID, zero[:], identity[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_proofs SET checked=true,rule_present=true,rule_identity=$2,rule_sha256=$3 WHERE job_id=$1::uuid`, job.ID, identity[:], digest[:]); err != nil {
		t.Fatal("unchecked-to-checked denied", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_proofs SET checked=false WHERE job_id=$1::uuid`, job.ID); err == nil {
		t.Fatal("checked evidence reverted")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_legacy_queries(job_id,root_id,directory,selected_directory,proof_version) VALUES($1::uuid,$2::uuid,'.','.','legacy-nearest-source-v1')`, job.ID, f.registration.RootID); err != nil {
		t.Fatal(err)
	}
	nfoMigrationDenied(t, f, "000013_ignore_legacy.down.sql")
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal("retained history cleanup", err)
	}
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	if domain.ValidateIgnoreIntent(domain.IgnoreIntent{Mode: "jeleeignore-legacy-v1", CaseMode: domain.IgnoreCaseSensitive}) != domain.ErrInvalid {
		t.Fatal("reserved mode publicly enabled")
	}
}
