package postgres

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Both jobs retain the same receipt captured from this fixture's owned media
// and NFO. Stopping the first lease preserves its unresolved journal; it cannot
// make a second job safe to start touching the same physical target.
func TestNFOCommitUnresolvedPhysicalTargetRefusesAnotherJob(t *testing.T) {
	f, firstLease, prepared := nfoCommitFixture(t)
	first, err := f.s.BeginNFOWriteCommit(f.ctx, firstLease, 1)
	if err != nil {
		t.Fatal("record first owned unresolved attempt", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',owner=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE id=$1::uuid`, firstLease.Job.ID); err != nil {
		t.Fatal("stop first owned lease while retaining journal", err)
	}
	secondJob := nfoWriteJobFixture(t, f, prepared, "same-physical-unresolved-second", firstLease.Job.Priority)
	secondLease := nfoWriteLeaseFixture(t, f, secondJob.ID)
	second, err := f.s.BeginNFOWriteCommit(f.ctx, secondLease, 1)
	if err == nil {
		t.Fatal("same physical NFO admitted despite unresolved journal")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unresolved target refused for unrelated reason", err)
	}
	if second.Token != "" {
		t.Fatal("refused attempt returned a journal token")
	}
	_, rawErr := f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner) VALUES($1::uuid,1,$2,$3)`, secondJob.ID, secondLease.Generation, secondLease.Owner)
	var conflict *pgconn.PgError
	if !errors.As(rawErr, &conflict) || conflict.Code != "23505" || conflict.ConstraintName != "nfo_native_target_unresolved" {
		t.Fatal("raw journal refused for unrelated reason", rawErr)
	}
	var claims int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_native_claims WHERE token=$1::uuid`, first.Token).Scan(&claims); err != nil || claims != 2 {
		t.Fatal("first token did not retain both physical claims", err)
	}
	var retained, added int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FILTER(WHERE token=$1::uuid),count(*) FILTER(WHERE job_id=$2::uuid) FROM nfo_write_commit_journal`, first.Token, secondJob.ID).Scan(&retained, &added); err != nil || retained != 1 || added != 0 {
		t.Fatal("refusal changed or added unresolved evidence", err)
	}
}

func TestNFOCommitNativeClaimsReplayAndImmutability(t *testing.T) {
	f, lease, prepared := nfoCommitFixture(t)
	first, err := f.s.BeginNFOWriteCommit(f.ctx, lease, 1)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal("reopen owned claim repository", err)
	}
	defer fresh.Pool.Close()
	again, err := fresh.BeginNFOWriteCommit(f.ctx, lease, 1)
	if err != nil || again != first {
		t.Fatal("same token claim replay changed evidence", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations WHERE id=$1::uuid`, prepared.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.SaveNFOWriteCommitFilePlan(f.ctx, lease, 1, first.Token, commitPlanFixture(prepared)); err != nil {
		t.Fatal("claim lost after preparation cleanup", err)
	}
	for _, query := range []string{
		`DELETE FROM nfo_write_native_claims WHERE token=$1::uuid`,
		`UPDATE nfo_write_native_claims SET identity=set_byte(identity,16,get_byte(identity,16)#1) WHERE token=$1::uuid`,
		`UPDATE nfo_write_native_claims SET token=gen_random_uuid() WHERE token=$1::uuid`,
	} {
		_, err := f.s.Pool.Exec(f.ctx, query, first.Token)
		var failure *pgconn.PgError
		if !errors.As(err, &failure) || failure.Code != "23514" || failure.Message != "nfo native claim is immutable" {
			t.Fatal("retained claim changed or unrelated refusal", err)
		}
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_write_native_claims SET identity=identity WHERE token=$1::uuid`, first.Token); err != nil {
		t.Fatal("owned no-op claim rejected", err)
	}
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		tx, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_native_claims WHERE token=$1::uuid`, first.Token).Scan(&count); err != nil || count != 2 {
			_ = tx.Rollback(f.ctx)
			t.Fatal("claim observations incomplete", err)
		}
		_ = tx.Rollback(f.ctx)
	}
}

func TestNFOCommitUnresolvedMediaRefusesNewNFOIdentity(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	prepared, _, err := service.Prepare(f.ctx, f.a, "media-claim-first", request)
	if err != nil {
		t.Fatal(err)
	}
	firstJob := nfoWriteJobFixture(t, f, prepared, "media-claim-first-job", domain.JobPriorityManual)
	firstLease := nfoWriteLeaseFixture(t, f, firstJob.ID)
	first, err := f.s.BeginNFOWriteCommit(f.ctx, firstLease, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',owner=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE id=$1::uuid`, firstJob.ID); err != nil {
		t.Fatal(err)
	}
	nfoPath := filepath.Join(prepared.Scope.Source.RootPath, prepared.Scope.Source.RelativePath)
	witnessPath := nfoPath + ".owned-unresolved-original"
	if err := os.Rename(nfoPath, witnessPath); err != nil {
		t.Fatal("retain owned original NFO", err)
	}
	if err := os.WriteFile(nfoPath, prepared.Original, 0600); err != nil {
		t.Fatal("create owned replacement NFO", err)
	}
	second, _, err := service.Prepare(f.ctx, f.a, "media-claim-second", request)
	if err != nil || second.NativeObservation.NFOFileIdentity() == prepared.NativeObservation.NFOFileIdentity() || second.NativeObservation.MediaIdentity() != prepared.NativeObservation.MediaIdentity() {
		t.Fatal("replacement did not change only NFO identity", err)
	}
	secondJob := nfoWriteJobFixture(t, f, second, "media-claim-second-job", domain.JobPriorityManual)
	secondLease := nfoWriteLeaseFixture(t, f, secondJob.ID)
	if _, err := f.s.BeginNFOWriteCommit(f.ctx, secondLease, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("same media admitted after NFO identity changed", err)
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_native_claims WHERE token=$1::uuid`, first.Token).Scan(&count); err != nil || count != 2 {
		t.Fatal("replacement cleared retained claims", err)
	}
	if _, err := os.Stat(witnessPath); err != nil {
		t.Fatal("original NFO witness lost", err)
	}
}

func TestNFOCommitNativeClaimsMigrationRetainsFirstObservation(t *testing.T) {
	f, lease, prepared := nfoCommitFixture(t)
	metrics := jobMetricMigrationStorage(t, f)
	jobMetricMigration(t, f, "down", 53)
	if jobMetricMigrationStorage(t, f) != metrics {
		t.Fatal("empty claim downgrade changed durable metrics")
	}
	first, err := f.s.BeginNFOWriteCommit(f.ctx, lease, 1)
	if err != nil {
		t.Fatal("record retained schema53 journal", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',owner=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE id=$1::uuid`, lease.Job.ID); err != nil {
		t.Fatal(err)
	}
	metrics = jobMetricMigrationStorage(t, f)
	jobMetricMigration(t, f, "up", SchemaVersion)
	if jobMetricMigrationStorage(t, f) != metrics {
		t.Fatal("claim migration changed durable job metrics")
	}
	receipt, err := prepared.NativeObservation.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var matches int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_native_claims WHERE token=$1::uuid AND ((kind='media' AND identity=substring($2::bytea FROM 57 FOR 48)) OR (kind='nfo' AND identity=substring($2::bytea FROM 105 FOR 48)))`, first.Token, receipt).Scan(&matches); err != nil || matches != 2 {
		t.Fatal("migration did not copy retained first observation", err)
	}
	nfoMigrationDenied(t, f, "000054_nfo_unresolved_native_claims.down.sql")
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("downgrade removed unresolved claims")
	}
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != SchemaVersion-1 || !dirty {
		t.Fatal("failed downgrade did not expose dirty migration state", err)
	}
	var retained int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_native_claims WHERE token=$1::uuid`, first.Token).Scan(&retained); err != nil || retained != 2 {
		t.Fatal("failed downgrade discarded first claims", err)
	}
}

func TestNFOCommitHistoricalUnknownJournalRefusesFreshTarget(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "unknown-history-source", request)
	if err != nil {
		t.Fatal(err)
	}
	job := nfoWriteJobFixture(t, f, saved, "unknown-history-job", domain.JobPriorityManual)
	nfoNativeReceiptLegacyAt52(t, f)
	lease := nfoWriteLeaseFixture(t, f, job.ID)
	first, err := persistNFOWriteCommitFixture(f.ctx, f.s, lease, 1)
	if err != nil {
		t.Fatal("retain actual pre53 journal", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',owner=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	fresh, _, err := service.Prepare(f.ctx, f.a, "unknown-history-fresh", request)
	if err != nil || fresh.NativeObservation.Empty() {
		t.Fatal("fresh preparation lacks actual native receipt", err)
	}
	nextJob := nfoWriteJobFixture(t, f, fresh, "unknown-history-fresh-job", domain.JobPriorityManual)
	nextLease := nfoWriteLeaseFixture(t, f, nextJob.ID)
	if _, err := f.s.BeginNFOWriteCommit(f.ctx, nextLease, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unknown historical physical target admitted fresh attempt", err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner) VALUES($1::uuid,1,$2,$3)`, nextJob.ID, nextLease.Generation, nextLease.Owner)
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "23505" || failure.ConstraintName != "nfo_native_target_unresolved" || failure.Message != "nfo historical physical observation unresolved" {
		t.Fatal("historical refusal came from unrelated guard", err)
	}
	var unknown, claims, journals int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_entries WHERE job_id=$1::uuid AND native_receipt IS NULL),(SELECT count(*) FROM nfo_write_native_claims),(SELECT count(*) FROM nfo_write_commit_journal WHERE token=$2::uuid)`, job.ID, first.Token).Scan(&unknown, &claims, &journals); err != nil || unknown != 1 || claims != 0 || journals != 1 {
		t.Fatal("upgrade inferred history or discarded unknown journal", err)
	}
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("unknown unresolved journal allowed downgrade")
	}
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal WHERE token=$1::uuid`, first.Token).Scan(&journals); err != nil || journals != 1 {
		t.Fatal("failed downgrade discarded historical journal", err)
	}
}

func TestNFOCommitNativeClaimsMigrationRefusesConflictingHistory(t *testing.T) {
	f, firstLease, prepared := nfoCommitFixture(t)
	jobMetricMigration(t, f, "down", 53)
	first, err := f.s.BeginNFOWriteCommit(f.ctx, firstLease, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',owner=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE id=$1::uuid`, firstLease.Job.ID); err != nil {
		t.Fatal(err)
	}
	secondJob := nfoWriteJobFixture(t, f, prepared, "migration-conflicting-history", domain.JobPriorityManual)
	secondLease := nfoWriteLeaseFixture(t, f, secondJob.ID)
	second, err := f.s.BeginNFOWriteCommit(f.ctx, secondLease, 1)
	if err != nil || second.Token == first.Token {
		t.Fatal("schema53 conflict fixture did not retain distinct tokens", err)
	}
	sql, err := migrationFiles.ReadFile("migrations/000054_nfo_unresolved_native_claims.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := f.s.Pool.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, rawErr := conn.Exec(f.ctx, string(sql))
	_, rollbackErr := conn.Exec(f.ctx, "ROLLBACK")
	conn.Release()
	if rollbackErr != nil {
		t.Fatal("rollback owned rejected migration", rollbackErr)
	}
	var conflict *pgconn.PgError
	if !errors.As(rawErr, &conflict) || conflict.Code != "23505" || conflict.ConstraintName != "nfo_native_target_unresolved" {
		t.Fatal("historical conflict refused for unrelated cause", rawErr)
	}
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "up"); err == nil {
		t.Fatal("migration arbitrarily accepted conflicting retained journals")
	}
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != 54 || !dirty {
		t.Fatal("rejected upgrade lost dirty state", err)
	}
	receipt, err := prepared.NativeObservation.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var retained int
	var absent bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_commit_journal j JOIN nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence WHERE j.token IN ($1::uuid,$2::uuid) AND e.native_receipt=$3::bytea),to_regclass('nfo_write_native_claims') IS NULL`, first.Token, second.Token, receipt).Scan(&retained, &absent); err != nil || retained != 2 || !absent {
		t.Fatal("rejected migration left partial claims or discarded first proof", err)
	}
}

// Both roots and every file are owned by this fixture. Capture fresh receipts
// from real hardlinks or independent copies; no borrowed SQL identity is used.
func nfoNativeAliasPreparation(t *testing.T, f jobFixture, service *app.NFOWritePreparations, base domain.NFOWritePreparation, linkNFO, linkMedia bool) domain.NFOWritePreparation {
	t.Helper()
	library, item := quotaPreparationScope(t, f, base.ID, "owned-native-alias")
	var root, rootPath string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text,path FROM library_roots WHERE library_id=$1::uuid`, library).Scan(&root, &rootPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rootPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska')`, item, library, root, base.Scope.MediaPath); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		relative string
		link     bool
	}{{base.Scope.Source.RelativePath, linkNFO}, {base.Scope.MediaPath, linkMedia}} {
		original := filepath.Join(base.Scope.Source.RootPath, filepath.FromSlash(file.relative))
		alias := filepath.Join(rootPath, filepath.FromSlash(file.relative))
		if err := os.MkdirAll(filepath.Dir(alias), 0700); err != nil {
			t.Fatal(err)
		}
		if file.link {
			if err := os.Link(original, alias); err != nil {
				t.Fatal("create owned native hardlink", err)
			}
			continue
		}
		bytes, err := os.ReadFile(original)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(original)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(alias, bytes, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(alias, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	request := base.Request
	request.ItemID = item
	request.Revision = 1
	prepared, _, err := service.Prepare(f.ctx, f.a, "owned-native-alias-preparation", request)
	if err != nil {
		t.Fatal("capture fresh owned alias observation", err)
	}
	if (prepared.NativeObservation.NFOFileIdentity() == base.NativeObservation.NFOFileIdentity()) != linkNFO || (prepared.NativeObservation.MediaIdentity() == base.NativeObservation.MediaIdentity()) != linkMedia {
		t.Fatal("real alias identities do not match fixture")
	}
	return prepared
}

func TestNFOCommitNativeClaimsAcrossLibrariesAndIndependentCopies(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		nfo, media bool
	}{{"both_hardlinks", true, true}, {"nfo_hardlink", true, false}, {"media_hardlink", false, true}, {"independent_same_bytes_mtime", false, false}} {
		t.Run(scenario.name, func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			base, _, err := service.Prepare(f.ctx, f.a, "native-alias-base", request)
			if err != nil {
				t.Fatal(err)
			}
			alias := nfoNativeAliasPreparation(t, f, service, base, scenario.nfo, scenario.media)
			firstJob := nfoWriteJobFixture(t, f, base, "native-alias-first", domain.JobPriorityManual)
			secondJob := nfoWriteJobFixture(t, f, alias, "native-alias-second", domain.JobPriorityManual)
			firstLease := nfoWriteLeaseFixture(t, f, firstJob.ID)
			secondLease := nfoWriteLeaseFixture(t, f, secondJob.ID)
			first, err := f.s.BeginNFOWriteCommit(f.ctx, firstLease, 1)
			if err != nil {
				t.Fatal(err)
			}
			second, err := f.s.BeginNFOWriteCommit(f.ctx, secondLease, 1)
			expected := 2
			if scenario.nfo || scenario.media {
				if !errors.Is(err, domain.ErrConflict) || second.Token != "" {
					t.Fatal("cross-library native alias admitted or unrelated refusal", err)
				}
				_, rawErr := f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner) VALUES($1::uuid,1,$2,$3)`, secondJob.ID, secondLease.Generation, secondLease.Owner)
				var failure *pgconn.PgError
				if !errors.As(rawErr, &failure) || failure.Code != "23505" || failure.ConstraintName != "nfo_native_target_unresolved" {
					t.Fatal("alias conflict not enforced by native claim", rawErr)
				}
			} else {
				if err != nil || second.Token == "" || second.Token == first.Token {
					t.Fatal("independent physical copies confused with aliases", err)
				}
				expected = 4
			}
			var claims int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_native_claims`).Scan(&claims); err != nil || claims != expected {
				t.Fatal("alias admission left partial claims", err)
			}
		})
	}
}

func TestNFOCommitNativeClaimsRepeatableReadStaleSnapshot(t *testing.T) {
	nfoNativeClaimsSnapshot(t, pgx.RepeatableRead)
}

func TestNFOCommitNativeClaimsSnapshotIsolation(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) { nfoNativeClaimsSnapshot(t, isolation) })
	}
}

func nfoNativeClaimsSnapshot(t *testing.T, isolation pgx.TxIsoLevel) {
	nfoNativeClaimsSnapshotRoles(t, isolation, "both")
}

func TestNFOCommitNativeClaimsCrossRoleSnapshotIsolation(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		for _, direction := range []string{"nfo_aliases_media", "media_aliases_nfo"} {
			t.Run(string(isolation)+"/"+direction, func(t *testing.T) { nfoNativeClaimsSnapshotRoles(t, isolation, direction) })
		}
	}
}

func nfoNativeClaimsSnapshotRoles(t *testing.T, isolation pgx.TxIsoLevel, direction string) {
	t.Helper()
	f, service, base := nfoCrossRoleBase(t, false)
	both := direction == "both"
	alias := nfoNativeAliasPreparation(t, f, service, base, both, both)
	if !both {
		relative, originalRelative := alias.Scope.Source.RelativePath, base.Scope.MediaPath
		if direction == "media_aliases_nfo" {
			relative, originalRelative = alias.Scope.MediaPath, base.Scope.Source.RelativePath
		}
		target := filepath.Join(alias.Scope.Source.RootPath, relative)
		original := filepath.Join(base.Scope.Source.RootPath, originalRelative)
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(original, target); err != nil {
			t.Fatal(err)
		}
		observed, _, err := service.Prepare(f.ctx, f.a, "native-stale-cross-role", alias.Request)
		if err != nil {
			t.Fatal(err)
		}
		if direction == "nfo_aliases_media" && observed.NativeObservation.NFOFileIdentity() != base.NativeObservation.MediaIdentity() {
			t.Fatal("stale NFO-to-media native observation missing")
		}
		if direction == "media_aliases_nfo" && observed.NativeObservation.MediaIdentity() != base.NativeObservation.NFOFileIdentity() {
			t.Fatal("stale media-to-NFO native observation missing")
		}
		alias = observed
	}
	firstJob := nfoWriteJobFixture(t, f, base, "native-stale-first", domain.JobPriorityManual)
	secondJob := nfoWriteJobFixture(t, f, alias, "native-stale-second", domain.JobPriorityManual)
	firstLease := nfoWriteLeaseFixture(t, f, firstJob.ID)
	secondLease := nfoWriteLeaseFixture(t, f, secondJob.ID)
	// Causal control may remove only the physical-identity primary key here.
	firstTX, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
	if err != nil {
		t.Fatal(err)
	}
	defer firstTX.Rollback(f.ctx)
	secondTX, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
	if err != nil {
		t.Fatal(err)
	}
	defer secondTX.Rollback(f.ctx)
	for _, tx := range []pgx.Tx{firstTX, secondTX} {
		var count int
		if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_native_claims`).Scan(&count); err != nil || count != 0 {
			t.Fatal("both old snapshots must begin without claims", err)
		}
	}
	insert := `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner) VALUES($1::uuid,1,$2,$3)`
	if _, err := firstTX.Exec(f.ctx, insert, firstJob.ID, firstLease.Generation, firstLease.Owner); err != nil {
		t.Fatal("first stale-snapshot journal rejected", err)
	}
	if err := firstTX.Commit(f.ctx); err != nil {
		t.Fatal("first native claims failed to commit", err)
	}
	_, err = secondTX.Exec(f.ctx, insert, secondJob.ID, secondLease.Generation, secondLease.Owner)
	if err == nil {
		if err := secondTX.Commit(f.ctx); err != nil {
			t.Fatal("second admission refused only at unrelated commit guard", err)
		}
		t.Fatal("stale native claim snapshot admitted conflicting complete journal")
	}
	var conflict *pgconn.PgError
	if !errors.As(err, &conflict) || !((conflict.Code == "23505" && conflict.ConstraintName == "nfo_native_target_unresolved") || (isolation == pgx.Serializable && conflict.Code == "40001")) {
		t.Fatal("stale snapshot refused by unrelated catalog or lease guard", err)
	}
	if err := secondTX.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	var claims, journals int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_native_claims),(SELECT count(*) FROM nfo_write_commit_journal)`).Scan(&claims, &journals); err != nil || claims != 2 || journals != 1 {
		t.Fatal("stale refusal retained partial second attempt", err)
	}
}

func TestNFOCommitNativeClaimsDeferredPairCompleteness(t *testing.T) {
	for _, members := range []int{0, 1, 2} {
		t.Run([]string{"missing_both", "missing_nfo", "complete_pair"}[members], func(t *testing.T) {
			f, lease, _ := nfoCommitFixture(t)
			tx, err := f.s.Pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			// This owned transaction models a missing after-insert producer. Immediate
			// catalog and claim guards, plus every deferred guard, remain enabled.
			if _, err := tx.Exec(f.ctx, `ALTER TABLE nfo_write_commit_journal DISABLE TRIGGER record_nfo_native_claims`); err != nil {
				t.Fatal(err)
			}
			var token string
			if err := tx.QueryRow(f.ctx, `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner) VALUES($1::uuid,1,$2,$3) RETURNING token::text`, lease.Job.ID, lease.Generation, lease.Owner).Scan(&token); err != nil {
				t.Fatal("immediate journal stage refused before deferred boundary", err)
			}
			if members >= 1 {
				if _, err := tx.Exec(f.ctx, `INSERT INTO nfo_write_native_claims(kind,identity,token) SELECT 'media',substring(native_receipt FROM 57 FOR 48),$1::uuid FROM nfo_write_entries WHERE job_id=$2::uuid AND sequence=1`, token, lease.Job.ID); err != nil {
					t.Fatal(err)
				}
			}
			if members == 2 {
				if _, err := tx.Exec(f.ctx, `INSERT INTO nfo_write_native_claims(kind,identity,token) SELECT 'nfo',substring(native_receipt FROM 105 FOR 48),$1::uuid FROM nfo_write_entries WHERE job_id=$2::uuid AND sequence=1`, token, lease.Job.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
					t.Fatal("verify complete manual pair before restoring producer", err)
				}
				if _, err := tx.Exec(f.ctx, `ALTER TABLE nfo_write_commit_journal ENABLE TRIGGER record_nfo_native_claims`); err != nil {
					t.Fatal(err)
				}
			}
			err = tx.Commit(f.ctx)
			var expectedClaims, expectedJournals int
			if members == 2 {
				if err != nil {
					t.Fatal("complete manual pair rejected at deferred boundary", err)
				}
				expectedClaims, expectedJournals = 2, 1
			} else {
				var failure *pgconn.PgError
				if !errors.As(err, &failure) || failure.Code != "23514" || failure.Message != "nfo physical claims missing" {
					t.Fatal("incomplete pair refused by unrelated boundary", err)
				}
			}
			var claims, journals int
			var enabled bool
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_native_claims),(SELECT count(*) FROM nfo_write_commit_journal),(SELECT tgenabled='O' FROM pg_trigger WHERE tgrelid='nfo_write_commit_journal'::regclass AND tgname='record_nfo_native_claims')`).Scan(&claims, &journals, &enabled); err != nil || claims != expectedClaims || journals != expectedJournals || !enabled {
				t.Fatal("pair boundary retained partial rows or disabled producer", err)
			}
			if members != 2 {
				if _, err := f.s.BeginNFOWriteCommit(f.ctx, lease, 1); err != nil {
					t.Fatal("rollback did not restore ordinary native pair producer", err)
				}
			}
		})
	}
}

func nfoCrossRoleBase(t *testing.T, sameJob bool) (jobFixture, *app.NFOWritePreparations, domain.NFOWritePreparation) {
	t.Helper()
	f, service, scope, request := nfoWritePreparationFixture(t)
	nfo := filepath.Join(scope.Source.RootPath, scope.Source.RelativePath)
	media := filepath.Join(scope.Source.RootPath, scope.MediaPath)
	if sameJob {
		if err := os.Remove(media); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(nfo, media); err != nil {
			t.Fatal(err)
		}
	} else {
		payload, err := os.ReadFile(nfo)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(media, payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	base, _, err := service.Prepare(f.ctx, f.a, "cross-role-matrix-base", request)
	if sameJob {
		if !errors.Is(err, domain.ErrInvalid) || base.ID != "" {
			t.Fatal("same-job alias not rejected by preparation validation", err)
		}
		return f, service, base
	}
	if err != nil {
		t.Fatal(err)
	}
	return f, service, base
}

func TestNFOCommitNativeClaimsCrossRoleMatrix(t *testing.T) {
	for _, direction := range []string{"nfo_aliases_media", "media_aliases_nfo", "same_job_alias"} {
		t.Run(direction, func(t *testing.T) {
			sameJob := direction == "same_job_alias"
			f, service, base := nfoCrossRoleBase(t, sameJob)
			if sameJob {
				var claims, journals, preparations int
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_native_claims),(SELECT count(*) FROM nfo_write_commit_journal),(SELECT count(*) FROM nfo_write_preparations)`).Scan(&claims, &journals, &preparations); err != nil || claims != 0 || journals != 0 || preparations != 0 {
					t.Fatal("same-job preparation refusal retained partial evidence", err)
				}
				return
			}
			firstJob := nfoWriteJobFixture(t, f, base, "cross-role-matrix-first", domain.JobPriorityManual)
			firstLease := nfoWriteLeaseFixture(t, f, firstJob.ID)
			alias := nfoNativeAliasPreparation(t, f, service, base, false, false)
			relative, originalRelative := alias.Scope.Source.RelativePath, base.Scope.MediaPath
			if direction == "media_aliases_nfo" {
				relative, originalRelative = alias.Scope.MediaPath, base.Scope.Source.RelativePath
			}
			target := filepath.Join(alias.Scope.Source.RootPath, relative)
			original := filepath.Join(base.Scope.Source.RootPath, originalRelative)
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(original, target); err != nil {
				t.Fatal(err)
			}
			cross, _, err := service.Prepare(f.ctx, f.a, "cross-role-matrix-alias", alias.Request)
			if err != nil {
				t.Fatal(err)
			}
			if direction == "nfo_aliases_media" && cross.NativeObservation.NFOFileIdentity() != base.NativeObservation.MediaIdentity() {
				t.Fatal("NFO-to-media actual observation missing")
			}
			if direction == "media_aliases_nfo" && cross.NativeObservation.MediaIdentity() != base.NativeObservation.NFOFileIdentity() {
				t.Fatal("media-to-NFO actual observation missing")
			}
			secondJob := nfoWriteJobFixture(t, f, cross, "cross-role-matrix-second", domain.JobPriorityManual)
			secondLease := nfoWriteLeaseFixture(t, f, secondJob.ID)
			if _, err := f.s.BeginNFOWriteCommit(f.ctx, firstLease, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.BeginNFOWriteCommit(f.ctx, secondLease, 1); err == nil {
				t.Fatal("cross-role physical inode admitted conflicting unresolved journal")
			} else if !errors.Is(err, domain.ErrConflict) {
				t.Fatal("cross-role conflict refused by unrelated guard", err)
			}
			_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner) VALUES($1::uuid,1,$2,$3)`, secondJob.ID, secondLease.Generation, secondLease.Owner)
			var failure *pgconn.PgError
			if !errors.As(err, &failure) || failure.Code != "23505" || failure.ConstraintName != "nfo_native_target_unresolved" {
				t.Fatal("cross-role conflict did not hit physical identity constraint", err)
			}
			var claims, journals int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_native_claims),(SELECT count(*) FROM nfo_write_commit_journal)`).Scan(&claims, &journals); err != nil || claims != 2 || journals != 1 {
				t.Fatal("cross-role refusal left partial attempt", err)
			}
		})
	}
}
func TestNFOCommitNativeClaimsCrossRoleHistoryMigration(t *testing.T) {
	for _, direction := range []string{"nfo_aliases_media", "media_aliases_nfo"} {
		t.Run(direction, func(t *testing.T) {
			f, service, base := nfoCrossRoleBase(t, false)
			jobMetricMigration(t, f, "down", 53)
			alias := nfoNativeAliasPreparation(t, f, service, base, false, false)
			relative, originalRelative := alias.Scope.Source.RelativePath, base.Scope.MediaPath
			if direction == "media_aliases_nfo" {
				relative, originalRelative = alias.Scope.MediaPath, base.Scope.Source.RelativePath
			}
			target := filepath.Join(alias.Scope.Source.RootPath, relative)
			original := filepath.Join(base.Scope.Source.RootPath, originalRelative)
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(original, target); err != nil {
				t.Fatal(err)
			}
			cross, _, err := service.Prepare(f.ctx, f.a, "cross-role-history-alias", alias.Request)
			if err != nil {
				t.Fatal(err)
			}
			if direction == "nfo_aliases_media" && cross.NativeObservation.NFOFileIdentity() != base.NativeObservation.MediaIdentity() {
				t.Fatal("history NFO-to-media actual observation missing")
			}
			if direction == "media_aliases_nfo" && cross.NativeObservation.MediaIdentity() != base.NativeObservation.NFOFileIdentity() {
				t.Fatal("history media-to-NFO actual observation missing")
			}
			firstJob := nfoWriteJobFixture(t, f, base, "cross-role-history-first", domain.JobPriorityManual)
			secondJob := nfoWriteJobFixture(t, f, cross, "cross-role-history-second", domain.JobPriorityManual)
			firstLease := nfoWriteLeaseFixture(t, f, firstJob.ID)
			secondLease := nfoWriteLeaseFixture(t, f, secondJob.ID)
			first, err := f.s.BeginNFOWriteCommit(f.ctx, firstLease, 1)
			if err != nil {
				t.Fatal(err)
			}
			second, err := f.s.BeginNFOWriteCommit(f.ctx, secondLease, 1)
			if err != nil || first.Token == second.Token {
				t.Fatal("schema53 cross-role history fixture failed", err)
			}
			metrics := jobMetricMigrationStorage(t, f)
			sql, err := migrationFiles.ReadFile("migrations/000054_nfo_unresolved_native_claims.up.sql")
			if err != nil {
				t.Fatal(err)
			}
			conn, err := f.s.Pool.Acquire(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, rawErr := conn.Exec(f.ctx, string(sql))
			_, rollbackErr := conn.Exec(f.ctx, "ROLLBACK")
			conn.Release()
			if rollbackErr != nil {
				t.Fatal("rollback rejected cross-role upgrade", rollbackErr)
			}
			var failure *pgconn.PgError
			if !errors.As(rawErr, &failure) || failure.Code != "23505" || failure.ConstraintName != "nfo_native_target_unresolved" {
				t.Fatal("cross-role migration refused by unrelated boundary", rawErr)
			}
			if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "up"); err == nil {
				t.Fatal("cross-role conflicting history was accepted by migration runner")
			}
			var retained int
			var absent bool
			firstReceipt, err := base.NativeObservation.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			secondReceipt, err := cross.NativeObservation.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_commit_journal j JOIN nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence WHERE (j.token=$1::uuid AND e.native_receipt=$3::bytea) OR (j.token=$2::uuid AND e.native_receipt=$4::bytea)),to_regclass('nfo_write_native_claims') IS NULL`, first.Token, second.Token, firstReceipt, secondReceipt).Scan(&retained, &absent); err != nil || retained != 2 || !absent {
				t.Fatal("cross-role rejected upgrade left partial claims or discarded first proof", err)
			}
			if jobMetricMigrationStorage(t, f) != metrics {
				t.Fatal("rejected cross-role migration changed durable metrics")
			}
			version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
			if err != nil || version != 54 || !dirty {
				t.Fatal("failed cross-role upgrade did not expose dirty migration state", err)
			}
		})
	}
}
