package postgres

import (
	"bytes"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNFOWriteNativeReceiptSQLCanonicalParity(t *testing.T) {
	f := newJobFixture(t)
	for _, platform := range []byte{1, 2} {
		for _, count := range []int{1, 128} {
			record := func(kind, tag byte) [48]byte {
				var v [48]byte
				v[0], v[1], v[2], v[16] = 1, platform, kind, tag
				return v
			}
			for _, mediaKind := range []byte{1, 2} {
				ancestors := make([][48]byte, count)
				for i := range ancestors {
					ancestors[i] = record(2, byte(i+4))
				}
				receipt, err := domain.NewNFONativePreparationReceipt(record(2, 1), record(mediaKind, 2), record(1, 3), ancestors)
				if err != nil {
					t.Fatal("build synthetic canonical records")
				}
				body, _ := receipt.MarshalBinary()
				check := func(data []byte) {
					_, parseErr := domain.ParseNFONativePreparationReceipt(data)
					var valid bool
					if err := f.s.Pool.QueryRow(f.ctx, `SELECT nfo_write_native_receipt_valid($1::bytea)`, data).Scan(&valid); err != nil || valid != (parseErr == nil) {
						t.Fatal("SQL receipt shape differs from domain codec")
					}
				}
				check(body)
				check([]byte{})
				check(body[:199])
				check(append(bytes.Clone(body), 0))
				check(make([]byte, 6297))
				for _, index := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 32, 48, 52, 56, 57, 58, 104, 105, 106, 152, 153, 154, 155} {
					mutant := bytes.Clone(body)
					mutant[index] ^= 0xff
					check(mutant)
				}
			}
		}
	}
}

func TestNFOWriteNativeReceiptRefusesMissingSaveAndRetainedDowngrade(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "native-guards", request)
	if err != nil {
		t.Fatal("save native guard fixture")
	}
	missing := domain.CloneNFOWritePreparation(saved)
	missing.NativeObservation = domain.NFONativePreparationReceipt{}
	if _, _, err := f.s.SaveNFOWritePreparation(f.ctx, f.a, "missing-native", missing); err != domain.ErrInvalid {
		t.Fatal("new save accepted missing native receipt")
	}
	for _, value := range []any{nil, []byte{1}, make([]byte, 6297)} {
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_write_preparations SET native_receipt=$2::bytea WHERE id=$1::uuid`, saved.ID, value); err == nil {
			t.Fatal("immutable receipt was replaced")
		}
	}
	nfoMigrationDenied(t, f, "000053_nfo_write_native_receipt.down.sql")
}

func TestNFOWriteNativeReceiptPreparationPersistence(t *testing.T) {
	f, _, scope, request := nfoWritePreparationFixture(t)
	b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	preparer, _ := nfo.NewWritePreparer(b)
	observed, err := preparer.PrepareNFOWrite(f.ctx, scope, request)
	if err != nil || observed.NativeObservation.Empty() {
		t.Fatal("observe owned native preparation")
	}
	saved, replay, err := f.s.SaveNFOWritePreparation(f.ctx, f.a, "native-persist", observed)
	if err != nil || replay || !saved.NativeObservation.Equal(observed.NativeObservation) {
		t.Fatal("native receipt lost while saving preparation")
	}
	digest, _ := domain.NFOWriteRequestDigest(request)
	fresh, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal("reopen native preparation repository")
	}
	defer fresh.Pool.Close()
	again, err := fresh.FindNFOWritePreparation(f.ctx, f.a, "native-persist", digest)
	if err != nil || !again.NativeObservation.Equal(observed.NativeObservation) {
		t.Fatal("native receipt changed after repository reopen")
	}
	ids := again.NativeObservation.AncestorIdentities()
	ids[0][8] ^= 1
	again, err = fresh.FindNFOWritePreparation(f.ctx, f.a, "native-persist", digest)
	if err != nil || !again.NativeObservation.Equal(observed.NativeObservation) {
		t.Fatal("caller changed retained native receipt")
	}
}

func TestNFOWriteNativeReceiptJobCopySurvivesPreparationCleanup(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "native-copy", request)
	if err != nil {
		t.Fatal("save native copy fixture")
	}
	j := nfoWriteJobFixture(t, f, saved, "native-owned-job", domain.JobPriorityManual)
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations WHERE id=$1::uuid`, saved.ID); err != nil {
		t.Fatal("delete expired preparation fixture")
	}
	lease := nfoWriteLeaseFixture(t, f, j.ID)
	fresh, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal("reopen native job repository")
	}
	defer fresh.Pool.Close()
	task, err := fresh.GetNFOWriteTask(f.ctx, lease, 1)
	if err != nil || task.Preparation.NativeObservation.Empty() || !task.Preparation.NativeObservation.Equal(saved.NativeObservation) {
		t.Fatal("job-owned native receipt missing after preparation cleanup")
	}
}

// Only owned historical fixtures may erase these synthetic observations to
// model pre53 storage. Never discard an unresolved journal to enable downgrade.
func nfoNativeReceiptLegacyAt52(t *testing.T, f jobFixture) {
	t.Helper()
	var journals int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal`).Scan(&journals); err != nil || journals != 0 {
		t.Fatal("historical fixture cannot discard journal evidence")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE nfo_write_preparations DISABLE TRIGGER ALL;
ALTER TABLE nfo_write_entries DISABLE TRIGGER ALL;
UPDATE nfo_write_preparations SET native_receipt=NULL;
UPDATE nfo_write_entries SET native_receipt=NULL;
ALTER TABLE nfo_write_entries ENABLE TRIGGER ALL;
ALTER TABLE nfo_write_preparations ENABLE TRIGGER ALL;`); err != nil {
		t.Fatal("create owned historical missing receipt")
	}
	jobMetricMigration(t, f, "down", 52)
}

func TestNFOWriteNativeReceiptHistoricalNULLIsNotBackfilled(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "native-history", request)
	if err != nil {
		t.Fatal("prepare owned historical fixture")
	}
	job := nfoWriteJobFixture(t, f, saved, "native-history-job", domain.JobPriorityManual)
	nfoNativeReceiptLegacyAt52(t, f)
	digest, _ := domain.NFOWriteRequestDigest(request)
	history, err := f.s.FindNFOWritePreparation(f.ctx, f.a, "native-history", digest)
	if err != nil || !history.NativeObservation.Empty() || history.Scope.RootGeneration < 1 {
		t.Fatal("historical observation gained today's receipt")
	}
	jobMetricMigration(t, f, "up", 53)
	history, err = f.s.FindNFOWritePreparation(f.ctx, f.a, "native-history", digest)
	if err != nil || !history.NativeObservation.Empty() {
		t.Fatal("upgrade backfilled native receipt")
	}
	lease := nfoWriteLeaseFixture(t, f, job.ID)
	task, err := f.s.GetNFOWriteTask(f.ctx, lease, 1)
	if err != nil || !task.Preparation.NativeObservation.Empty() {
		t.Fatal("historical owned task gained native receipt")
	}
	if _, err := f.s.BeginNFOWriteCommit(f.ctx, lease, 1); err == nil {
		t.Fatal("historical missing receipt authorized commit begin")
	}
}

func TestNFOWriteNativeReceiptEntryCannotCopyDifferentObservation(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "native-source-copy", request)
	if err != nil {
		t.Fatal("save native source copy fixture")
	}
	job := nfoWriteJobFixture(t, f, saved, "native-source-copy-job", domain.JobPriorityManual)
	mutant, _ := saved.NativeObservation.MarshalBinary()
	mutant[24] ^= 1
	if _, err := domain.ParseNFONativePreparationReceipt(mutant); err != nil {
		t.Fatal("copy control must retain valid canonical shape")
	}
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal("begin native copy control")
	}
	defer tx.Rollback(f.ctx)
	// Remove the owned row only inside this rolled-back control transaction,
	// so neither unique(job,item) nor the deferred job total explains refusal.
	if _, err = tx.Exec(f.ctx, `DELETE FROM nfo_write_entries WHERE job_id=$1::uuid`, job.ID); err != nil {
		t.Fatal("prepare isolated native copy control")
	}
	if _, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_entries(job_id,sequence,preparation_id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt)
 SELECT $1::uuid,1,id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,$3::bytea FROM nfo_write_preparations WHERE id=$2::uuid`, job.ID, saved.ID, mutant); err == nil {
		t.Fatal("new job entry copied a different first native observation")
	}
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "23514" || failure.Message != "nfo write native receipt differs from preparation" {
		t.Fatal("native copy refusal came from another invariant")
	}
}

func TestNFOWriteNativeReceiptBytesCountAtLibraryCapacityBoundary(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "native-byte-base", request)
	if err != nil {
		t.Fatal("save native quota fixture")
	}
	// Owned SQL payloads test storage accounting, not native write authority.
	query := `WITH recipe AS MATERIALIZED (SELECT p.*,convert_to((convert_from(request_bytes,'UTF8')::jsonb||jsonb_build_object('maxBytes',33554432))::text,'UTF8') AS encoded FROM nfo_write_preparations p WHERE id=$1::uuid),
 capacity AS MATERIALIZED (SELECT COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+COALESCE(octet_length(native_receipt),0)),0) AS used FROM nfo_write_preparations),
 sizes AS MATERIALIZED (SELECT CASE WHEN $3::boolean THEN (134217728-used-octet_length(encoded)-octet_length(native_receipt)-33554432)::integer ELSE 33554432 END AS replacement_size FROM recipe CROSS JOIN capacity),
 payload AS MATERIALIZED (SELECT convert_to(rpad('<movie/>',33554432,' '),'UTF8') AS original,convert_to(rpad('<movie/>',replacement_size,' '),'UTF8') AS replacement FROM sizes)
 INSERT INTO nfo_write_preparations(id,actor_id,idempotency_key,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt)
 SELECT gen_random_uuid(),actor_id,$2,version,encoded,encode(sha256(encoded),'hex'),library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,33554432,modified_unix_nano,original,encode(sha256(original),'hex'),replacement,encode(sha256(replacement),'hex'),native_receipt FROM recipe CROSS JOIN payload`
	if _, err := f.s.Pool.Exec(f.ctx, query, saved.ID, "native-large-first", false); err != nil {
		t.Fatal("first large storage row rejected")
	}
	if _, err := f.s.Pool.Exec(f.ctx, query, saved.ID, "native-exact-limit", true); err != nil {
		t.Fatal("exact library capacity with receipt bytes rejected")
	}
	var usedWithoutNative, nativeBytes, incomingWithoutNative int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),sum(octet_length(native_receipt)),(SELECT octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes) FROM nfo_write_preparations WHERE id=$1::uuid) FROM nfo_write_preparations`, saved.ID).Scan(&usedWithoutNative, &nativeBytes, &incomingWithoutNative); err != nil || usedWithoutNative+nativeBytes != 134217728 || usedWithoutNative+incomingWithoutNative > 134217728 {
		t.Fatal("quota control does not isolate native receipt byte accounting")
	}
	if _, _, err := f.s.SaveNFOWritePreparation(f.ctx, f.a, "native-over-limit", saved); err != domain.ErrNFOCacheCapacity {
		t.Fatal("application quota omitted native receipt bytes")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_preparations SELECT (jsonb_populate_record(NULL::nfo_write_preparations,to_jsonb(p)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key','native-direct-over'))).* FROM nfo_write_preparations p WHERE id=$1::uuid`, saved.ID); err == nil {
		t.Fatal("SQL quota omitted native receipt bytes")
	}
}

func TestNFOWriteNativeReceiptPlanMustMatchFirstParentAndTarget(t *testing.T) {
	for _, field := range []string{"parent", "target"} {
		t.Run(field, func(t *testing.T) {
			f, lease, prepared := nfoCommitFixture(t)
			record, err := f.s.BeginNFOWriteCommit(f.ctx, lease, 1)
			if err != nil {
				t.Fatal("begin owned native plan fixture")
			}
			plan := commitPlanFixture(prepared)
			if field == "parent" {
				plan.ParentIdentity[16] ^= 1
			} else {
				plan.TargetIdentity[16] ^= 1
			}
			if !domain.ValidNFONativeIdentity(plan.ParentIdentity, 2) || !domain.ValidNFONativeIdentity(plan.TargetIdentity, 1) {
				t.Fatal("plan control must remain canonical")
			}
			if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, lease, 1, record.Token, plan); err == nil {
				t.Fatal("canonical plan replaced the first physical observation")
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_file_plans WHERE token=$1::uuid`, record.Token).Scan(&count); err != nil || count != 0 {
				t.Fatal("rejected plan retained new file evidence")
			}
		})
	}
}

func TestNFOWriteNativeReceiptPlanSQLBinding(t *testing.T) {
	const insert = `INSERT INTO nfo_write_commit_file_plans(token,version,target_name,parent_identity,target_identity) VALUES($1::uuid,$2,$3,$4,$5)`
	for _, phase := range []string{"before", "deferred", "positive_replay"} {
		for _, field := range []string{"parent", "target"} {
			t.Run(phase+"/"+field, func(t *testing.T) {
				f, lease, prepared := nfoCommitFixture(t)
				record, err := f.s.BeginNFOWriteCommit(f.ctx, lease, 1)
				if err != nil {
					t.Fatal("begin owned SQL native plan fixture")
				}
				plan := commitPlanFixture(prepared)
				tx, err := f.s.Pool.Begin(f.ctx)
				if err != nil {
					t.Fatal("begin SQL native plan transaction")
				}
				defer tx.Rollback(f.ctx)
				if phase == "deferred" {
					// Only this rolled-back owned transaction bypasses the immediate
					// guard, to isolate the deferred check without altering production.
					if _, err := tx.Exec(f.ctx, `ALTER TABLE nfo_write_commit_file_plans DISABLE TRIGGER guard_nfo_commit_plan_native`); err != nil {
						t.Fatal("isolate deferred native plan guard")
					}
				}
				if phase != "positive_replay" {
					if field == "parent" {
						plan.ParentIdentity[16] ^= 1
					} else {
						plan.TargetIdentity[16] ^= 1
					}
				}
				_, err = tx.Exec(f.ctx, insert, record.Token, plan.Version, plan.TargetName, plan.ParentIdentity[:], plan.TargetIdentity[:])
				if phase == "deferred" {
					if err != nil {
						t.Fatal("deferred control rejected before constraint flush")
					}
					_, err = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
				}
				if phase == "positive_replay" {
					if err != nil {
						t.Fatal("first observation SQL plan rejected")
					}
					if _, err := tx.Exec(f.ctx, `UPDATE nfo_write_commit_file_plans SET token=token WHERE token=$1::uuid`, record.Token); err != nil {
						t.Fatal("first observation SQL replay rejected")
					}
					if err := tx.Commit(f.ctx); err != nil {
						t.Fatal("first observation deferred commit rejected")
					}
					return
				}
				var failure *pgconn.PgError
				if !errors.As(err, &failure) || failure.Code != "23514" || failure.Message != "nfo commit plan differs from first native observation" {
					t.Fatal("SQL native plan refusal came from another invariant")
				}
				if err := tx.Rollback(f.ctx); err != nil {
					t.Fatal("rollback rejected SQL native plan")
				}
				var count int
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_file_plans WHERE token=$1::uuid`, record.Token).Scan(&count); err != nil || count != 0 {
					t.Fatal("rejected SQL plan retained evidence")
				}
			})
		}
	}
}
