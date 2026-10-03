package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// These owned SQL recipes isolate byte accounting. Their cloned metadata and
// receipts are not passed to a filesystem writer or claimed as fresh observations.
func nativeQuotaStoragePreparation(t *testing.T, f jobFixture, base, library, item, key string, maximum int, capacity int64, ledger string) string {
	return nativeQuotaStoragePreparationWith(t, f, f.s.Pool, base, library, item, key, maximum, capacity, ledger)
}

type nativeQuotaQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func nativeQuotaStoragePreparationWith(t *testing.T, f jobFixture, queryer nativeQuotaQuery, base, library, item, key string, maximum int, capacity int64, ledger string) string {
	t.Helper()
	var used string
	switch ledger {
	case "preparation_global":
		used = "SELECT COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+COALESCE(octet_length(native_receipt),0)),0) FROM nfo_write_preparations"
	case "preparation_library":
		used = "SELECT COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+COALESCE(octet_length(native_receipt),0)),0) FROM nfo_write_preparations WHERE library_id=$2::uuid"
	case "entry_global":
		used = "SELECT COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+COALESCE(octet_length(native_receipt),0)),0) FROM nfo_write_entries"
	default:
		t.Fatal("invalid owned quota ledger")
	}
	query := `WITH recipe AS MATERIALIZED (SELECT p.*,convert_to((convert_from(request_bytes,'UTF8')::jsonb||jsonb_build_object('itemId',$3::uuid,'maxBytes',$5::integer))::text,'UTF8') AS encoded FROM nfo_write_preparations p WHERE id=$1::uuid),
 sizes AS MATERIALIZED (SELECT CASE WHEN $6::bigint=0 THEN $5::bigint ELSE $6::bigint-(` + used + `)-octet_length(encoded)-octet_length(native_receipt)-$5::bigint END AS replacement_size FROM recipe),
 payload AS MATERIALIZED (SELECT convert_to(rpad('<movie/>',$5::integer,' '),'UTF8') AS original,convert_to(rpad('<movie/>',replacement_size::integer,' '),'UTF8') AS replacement FROM sizes)
 INSERT INTO nfo_write_preparations(actor_id,idempotency_key,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt)
 SELECT actor_id,$4,version,encoded,encode(sha256(encoded),'hex'),$2::uuid,$3::uuid,source_id,(SELECT id FROM library_roots WHERE library_id=$2::uuid),kind,revision,generation,(SELECT nfo_generation FROM library_roots WHERE library_id=$2::uuid),(SELECT path FROM library_roots WHERE library_id=$2::uuid),relative_path,media_path,directory_path,$5::integer,modified_unix_nano,original,encode(sha256(original),'hex'),replacement,encode(sha256(replacement),'hex'),native_receipt FROM recipe CROSS JOIN payload RETURNING id::text`
	var id string
	if err := queryer.QueryRow(f.ctx, query, base, library, item, key, maximum, capacity).Scan(&id); err != nil {
		t.Fatal("create bounded owned native quota recipe", err)
	}
	return id
}

func nativeQuotaExtraItem(t *testing.T, f jobFixture, library string) string {
	t.Helper()
	var id string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items SELECT (jsonb_populate_record(NULL::items,to_jsonb(i)||jsonb_build_object('id',gen_random_uuid()))).* FROM items i WHERE library_id=$1::uuid LIMIT 1 RETURNING id::text`, library).Scan(&id); err != nil {
		t.Fatal("create owned quota item", err)
	}
	return id
}

func nativeQuotaRejectExtraEntry(t *testing.T, f jobFixture, base, job, library string, sequence int, capacity int64, global bool) {
	t.Helper()
	item := nativeQuotaExtraItem(t, f, library)
	preparation := nativeQuotaStoragePreparation(t, f, base, library, item, "native-extra-entry", 64, 0, "preparation_global")
	filter := " WHERE job_id=$1::uuid"
	args := []any{job, preparation}
	if global {
		filter = ""
		args = []any{preparation}
	}
	incoming := "$2::uuid"
	if global {
		incoming = "$1::uuid"
	}
	var withoutNative, native, incomingWithoutNative int64
	query := `SELECT COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),0),COALESCE(sum(octet_length(native_receipt)),0),(SELECT octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes) FROM nfo_write_preparations WHERE id=` + incoming + `) FROM nfo_write_entries` + filter
	if err := f.s.Pool.QueryRow(f.ctx, query, args...).Scan(&withoutNative, &native, &incomingWithoutNative); err != nil || withoutNative+native != capacity || withoutNative+incomingWithoutNative > capacity {
		t.Fatal("entry boundary does not isolate native byte contribution", err)
	}
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	_, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_entries(job_id,sequence,preparation_id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt)
 SELECT $1::uuid,$2,id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt FROM nfo_write_preparations WHERE id=$3::uuid`, job, sequence, preparation)
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "23514" || failure.Message != "nfo write intent capacity reached" {
		t.Fatal("native entry byte contribution not enforced by quota", err)
	}
}

func TestNFOWriteNativeReceiptPreparationGlobalExactBytes(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "native-global-base", request)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		library, item := quotaPreparationScope(t, f, saved.ID, fmt.Sprintf("native-global-%d", i))
		capacity := int64(0)
		if i == 3 {
			capacity = 268435456
		}
		nativeQuotaStoragePreparation(t, f, saved.ID, library, item, fmt.Sprintf("native-global-large-%d", i), 33554432, capacity, "preparation_global")
	}
	var withoutNative, native, incoming int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),sum(octet_length(native_receipt)),(SELECT octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes) FROM nfo_write_preparations WHERE id=$1::uuid) FROM nfo_write_preparations`, saved.ID).Scan(&withoutNative, &native, &incoming); err != nil || withoutNative+native != 268435456 || withoutNative+incoming > 268435456 {
		t.Fatal("global prep boundary does not isolate native bytes", err)
	}
	if _, _, err := f.s.SaveNFOWritePreparation(f.ctx, f.a, "native-global-extra", saved); err != domain.ErrNFOCacheCapacity {
		t.Fatal("application global native byte quota omitted", err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_preparations SELECT (jsonb_populate_record(NULL::nfo_write_preparations,to_jsonb(p)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key','native-global-direct-extra'))).* FROM nfo_write_preparations p WHERE id=$1::uuid`, saved.ID)
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "23514" || failure.Message != "nfo write preparation capacity reached" {
		t.Fatal("SQL global native byte quota omitted", err)
	}
}

func TestNFOWriteNativeReceiptJobExactBytes(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "native-job-byte-base", request)
	if err != nil {
		t.Fatal(err)
	}
	library, item := quotaPreparationScope(t, f, saved.ID, "native-job-byte-library")
	first := nativeQuotaStoragePreparation(t, f, saved.ID, library, item, "native-job-byte-first", 33554432, 0, "preparation_library")
	secondItem := nativeQuotaExtraItem(t, f, library)
	second := nativeQuotaStoragePreparation(t, f, saved.ID, library, secondItem, "native-job-byte-last", 33554432, 134217728, "preparation_library")
	job := nfoWriteJobFixture(t, f, domain.NFOWritePreparation{ID: first, Scope: domain.NFOItemScope{LibraryID: library}}, "native-job-byte", domain.JobPriorityManual, second)
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations WHERE id=ANY($1::uuid[])`, []string{first, second}); err != nil {
		t.Fatal("cleanup copied owned quota preparations", err)
	}
	nativeQuotaRejectExtraEntry(t, f, saved.ID, job.ID, library, 3, 134217728, false)
}

// Each isolation level constructs its own proposed three-entry batch. This
// storage-only fixture releases copied preparations inside the same transaction
// to isolate the job limit from the equal-sized library preparation limit.
// It never commits an incomplete batch or appends to an already committed job.
func TestNFOWriteNativeReceiptJobExactBytesBatchRollbackIsolation(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			saved, _, err := service.Prepare(f.ctx, f.a, "native-job-isolation-base", request)
			if err != nil {
				t.Fatal(err)
			}
			library, item := quotaPreparationScope(t, f, saved.ID, "native-job-isolation-library")
			first := nativeQuotaStoragePreparation(t, f, saved.ID, library, item, "native-job-isolation-first", 33554432, 0, "preparation_library")
			secondItem := nativeQuotaExtraItem(t, f, library)
			second := nativeQuotaStoragePreparation(t, f, saved.ID, library, secondItem, "native-job-isolation-second", 33554432, 134217728, "preparation_library")
			base := nfoWriteJobFixture(t, f, domain.NFOWritePreparation{ID: first, Scope: domain.NFOItemScope{LibraryID: library}}, "native-job-isolation-owned", domain.JobPriorityManual, second)
			if _, err := f.s.CancelJob(f.ctx, f.a, base.ID); err != nil {
				t.Fatal(err)
			}
			extraItem := nativeQuotaExtraItem(t, f, library)
			tx, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			job := cloneQuotaJob(t, f, tx, base.ID, "native-job-isolation-proposed", 3)
			copyEntry := func(id string, sequence int) error {
				_, err := tx.Exec(f.ctx, `INSERT INTO nfo_write_entries(job_id,sequence,preparation_id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt)
 SELECT $1::uuid,$2,id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt FROM nfo_write_preparations WHERE id=$3::uuid`, job, sequence, id)
				return err
			}
			if err := copyEntry(first, 1); err != nil {
				t.Fatal("first owned entry refused", err)
			}
			if err := copyEntry(second, 2); err != nil {
				t.Fatal("exact inclusive job byte capacity refused", err)
			}
			var used, without int64
			if err := tx.QueryRow(f.ctx, `SELECT sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+octet_length(native_receipt)),sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)) FROM nfo_write_entries WHERE job_id=$1::uuid`, job).Scan(&used, &without); err != nil || used != 134217728 {
				t.Fatal("job not at exact native capacity", err)
			}
			if _, err := tx.Exec(f.ctx, `DELETE FROM nfo_write_preparations WHERE id=ANY($1::uuid[])`, []string{first, second}); err != nil {
				t.Fatal(err)
			}
			extra := nativeQuotaStoragePreparationWith(t, f, tx, saved.ID, library, extraItem, "native-job-isolation-extra", 64, 0, "preparation_global")
			var incomingWithout int64
			if err := tx.QueryRow(f.ctx, `SELECT octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes) FROM nfo_write_preparations WHERE id=$1::uuid`, extra).Scan(&incomingWithout); err != nil || without+incomingWithout > 134217728 {
				t.Fatal("job rejection not isolated to native bytes", err)
			}
			err = copyEntry(extra, 3)
			if err == nil {
				if commitErr := tx.Commit(f.ctx); commitErr != nil {
					t.Fatal("batch refused by unrelated commit guard", commitErr)
				}
				t.Fatal("native job bytes admitted an oversized complete batch")
			}
			var failure *pgconn.PgError
			if !errors.As(err, &failure) || failure.Code != "23514" || failure.Message != "nfo write intent capacity reached" {
				t.Fatal("job byte limit refused for unrelated reason", err)
			}
			_ = tx.Rollback(f.ctx)
			var jobs, requests, entries, preparations int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM jobs WHERE id=$1::uuid),(SELECT count(*) FROM nfo_write_requests WHERE job_id=$1::uuid),(SELECT count(*) FROM nfo_write_entries WHERE job_id=$1::uuid),(SELECT count(*) FROM nfo_write_preparations WHERE id=ANY($2::uuid[]))`, job, []string{first, second}).Scan(&jobs, &requests, &entries, &preparations); err != nil || jobs != 0 || requests != 0 || entries != 0 || preparations != 2 {
				t.Fatal("oversized job transaction did not fully roll back", err, jobs, requests, entries, preparations)
			}
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_preparations WHERE idempotency_key='native-job-isolation-extra'`).Scan(&preparations); err != nil || preparations != 0 {
				t.Fatal("extra preparation survived rollback", err)
			}
		})
	}
}

func TestNFOWriteNativeReceiptEntryGlobalExactBytes(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "native-entry-global-base", request)
	if err != nil {
		t.Fatal(err)
	}
	var firstJob domain.Job
	for i := 0; i < 8; i++ {
		library, item := quotaPreparationScope(t, f, saved.ID, fmt.Sprintf("native-entry-global-%d", i))
		capacity := int64(0)
		if i == 7 {
			capacity = 536870912
		}
		id := nativeQuotaStoragePreparation(t, f, saved.ID, library, item, fmt.Sprintf("native-entry-large-%d", i), 33554432, capacity, "entry_global")
		job := nfoWriteJobFixture(t, f, domain.NFOWritePreparation{ID: id, Scope: domain.NFOItemScope{LibraryID: library}}, fmt.Sprintf("native-entry-global-job-%d", i), domain.JobPriorityManual)
		if i == 0 {
			firstJob = job
		}
		if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations WHERE id=$1::uuid`, id); err != nil {
			t.Fatal("cleanup copied owned quota preparation", err)
		}
	}
	nativeQuotaRejectExtraEntry(t, f, saved.ID, firstJob.ID, firstJob.LibraryID, 2, 536870912, true)
}

func TestNFOWriteNativeReceiptPreparationExactBytesStaleSnapshots(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			saved, _, err := service.Prepare(f.ctx, f.a, "native-snapshot-base", request)
			if err != nil {
				t.Fatal(err)
			}
			var incoming int64
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+octet_length(native_receipt) FROM nfo_write_preparations WHERE id=$1::uuid`, saved.ID).Scan(&incoming); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 4; i++ {
				library, item := quotaPreparationScope(t, f, saved.ID, fmt.Sprintf("native-snapshot-library-%d", i))
				capacity := int64(0)
				if i == 3 {
					capacity = 268435456 - incoming
				}
				nativeQuotaStoragePreparation(t, f, saved.ID, library, item, fmt.Sprintf("native-snapshot-large-%d", i), 33554432, capacity, "preparation_global")
			}
			first, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer first.Rollback(f.ctx)
			second, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer second.Rollback(f.ctx)
			for _, tx := range []pgx.Tx{first, second} {
				var used int64
				if err := tx.QueryRow(f.ctx, `SELECT sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+octet_length(native_receipt)) FROM nfo_write_preparations`).Scan(&used); err != nil || used+incoming != 268435456 {
					t.Fatal("native snapshots do not start at the same last slot", err)
				}
			}
			if _, err := first.Exec(f.ctx, cloneQuotaPreparation, saved.ID, "native-snapshot-first"); err != nil {
				t.Fatal("native exact last slot refused", err)
			}
			if err := first.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			var used, native, incomingWithoutNative int64
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),sum(octet_length(native_receipt)),(SELECT octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes) FROM nfo_write_preparations WHERE id=$1::uuid) FROM nfo_write_preparations`, saved.ID).Scan(&used, &native, &incomingWithoutNative); err != nil || used+native != 268435456 || used+incomingWithoutNative > 268435456 {
				t.Fatal("stale snapshot boundary does not isolate native contribution", err)
			}
			_, err = second.Exec(f.ctx, cloneQuotaPreparation, saved.ID, "native-snapshot-second")
			var failure *pgconn.PgError
			if !errors.As(err, &failure) || failure.Code != "40001" && (failure.Code != "23514" || failure.Message != "nfo write preparation capacity reached") {
				t.Fatal("stale native snapshot admitted or refused for another reason", err)
			}
			if isolation == pgx.ReadCommitted && failure.Code != "23514" {
				t.Fatal("read committed native refusal must come from capacity")
			}
			if err := second.Rollback(f.ctx); err != nil {
				t.Fatal(err)
			}
			var count, partial int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE idempotency_key='native-snapshot-second') FROM nfo_write_preparations`).Scan(&count, &partial); err != nil || count != 6 || partial != 0 {
				t.Fatal("stale native refusal retained extra data", err)
			}
		})
	}
}

// Source preparations exist before either snapshot. Each
// transaction creates its own complete job batch; no preparation insert can
// mask the entry fence. Loaned receipt metadata proves storage accounting only.
func TestNFOWriteNativeReceiptEntryExactBytesStaleSnapshots(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			saved, _, err := service.Prepare(f.ctx, f.a, "native-entry-snapshot-base", request)
			if err != nil {
				t.Fatal(err)
			}
			base := nfoWriteJobFixture(t, f, saved, "native-entry-snapshot-base-job", domain.JobPriorityManual)
			if _, err := f.s.CancelJob(f.ctx, f.a, base.ID); err != nil {
				t.Fatal(err)
			}
			candidateIDs := make([]string, 2)
			for i := range candidateIDs {
				item := nativeQuotaExtraItem(t, f, saved.Scope.LibraryID)
				candidateIDs[i] = nativeQuotaStoragePreparation(t, f, saved.ID, saved.Scope.LibraryID, item, fmt.Sprintf("native-entry-snapshot-small-%d", i), 64, 0, "preparation_global")
			}
			var incoming, incomingWithout int64
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+octet_length(native_receipt),octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes) FROM nfo_write_preparations WHERE id=$1::uuid`, candidateIDs[0]).Scan(&incoming, &incomingWithout); err != nil {
				t.Fatal(err)
			}
			var otherIncoming int64
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+octet_length(native_receipt) FROM nfo_write_preparations WHERE id=$1::uuid`, candidateIDs[1]).Scan(&otherIncoming); err != nil || otherIncoming != incoming {
				t.Fatal("candidate byte sizes differ", err)
			}
			const capacity int64 = 536870912
			for i := 0; i < 8; i++ {
				library, item := quotaPreparationScope(t, f, saved.ID, fmt.Sprintf("native-entry-snapshot-library-%d", i))
				exact := int64(0)
				if i == 7 {
					exact = capacity - incoming
				}
				id := nativeQuotaStoragePreparation(t, f, saved.ID, library, item, fmt.Sprintf("native-entry-snapshot-large-%d", i), 33554432, exact, "entry_global")
				nfoWriteJobFixture(t, f, domain.NFOWritePreparation{ID: id, Scope: domain.NFOItemScope{LibraryID: library}}, fmt.Sprintf("native-entry-snapshot-seed-job-%d", i), domain.JobPriorityManual)
				if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations WHERE id=$1::uuid`, id); err != nil {
					t.Fatal(err)
				}
			}
			var bytes, without int64
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+octet_length(native_receipt)),sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)) FROM nfo_write_entries`).Scan(&bytes, &without); err != nil || bytes != capacity-incoming || without+2*incomingWithout > capacity {
				t.Fatal("native byte boundary not isolated", err)
			}
			first, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer first.Rollback(f.ctx)
			second, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer second.Rollback(f.ctx)
			for _, tx := range []pgx.Tx{first, second} {
				var observed int64
				if err := tx.QueryRow(f.ctx, `SELECT sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+octet_length(native_receipt)) FROM nfo_write_entries`).Scan(&observed); err != nil || observed != capacity-incoming {
					t.Fatal("last-slot snapshots differ", err)
				}
			}
			admissionStage := ""
			admit := func(tx pgx.Tx, preparation, key string) error {
				admissionStage = "job"
				var job string
				err := tx.QueryRow(f.ctx, `INSERT INTO jobs SELECT (jsonb_populate_record(NULL::jobs,to_jsonb(j)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key',$2::text))).* FROM jobs j WHERE id=$1::uuid RETURNING id::text`, base.ID, key).Scan(&job)
				if err != nil {
					return err
				}
				admissionStage = "request"
				_, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_requests(job_id,library_id,generation,total,intent_digest) SELECT j.id,j.library_id,p.generation,1,sha256(convert_to(format('{"Priority":%s,"Preparations":[%s]}',to_json(j.priority)::text,to_json(p.id::text)::text),'UTF8')) FROM jobs j JOIN nfo_write_preparations p ON p.id=$2::uuid AND p.actor_id=j.actor_id AND p.library_id=j.library_id WHERE j.id=$1::uuid`, job, preparation)
				if err != nil {
					return err
				}
				admissionStage = "entry"
				tag, err := tx.Exec(f.ctx, `INSERT INTO nfo_write_entries(job_id,sequence,preparation_id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt)
 SELECT $1::uuid,1,id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt FROM nfo_write_preparations WHERE id=$2::uuid`, job, preparation)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return fmt.Errorf("owned source preparation missing")
				}
				return nil
			}
			if err := admit(first, candidateIDs[0], "native-entry-first-last-slot"); err != nil {
				t.Fatal("first complete batch refused", err)
			}
			if err := first.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			err = admit(second, candidateIDs[1], "native-entry-second-last-slot")
			if err == nil {
				err = second.Commit(f.ctx)
			}
			if err == nil {
				t.Fatal("stale native byte snapshot admitted a second complete batch")
			}
			var failure *pgconn.PgError
			if !errors.As(err, &failure) {
				t.Fatal("unrelated second admission error", admissionStage, err)
			}
			serialAdmissionAbort := isolation == pgx.Serializable && failure.Code == "40001"
			entryRefusal := admissionStage == "entry" && ((failure.Code == "23514" && failure.Message == "nfo write intent capacity reached") || (isolation == pgx.RepeatableRead && failure.Code == "40001"))
			if !serialAdmissionAbort && !entryRefusal {
				t.Fatal("unrelated second admission error", admissionStage, err)
			}
			t.Logf("second complete admission refused: isolation=%s stage=%s sqlstate=%s; no claim of entry-fence causality for an earlier serializable abort", isolation, admissionStage, failure.Code)
			_ = second.Rollback(f.ctx)
			var jobs, requests, entries int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM jobs WHERE kind='nfo_write'),(SELECT count(*) FROM nfo_write_requests),(SELECT count(*) FROM nfo_write_entries)`).Scan(&jobs, &requests, &entries); err != nil || jobs != 10 || requests != 10 || entries != 10 {
				t.Fatal("partial or excess batch retained", err, jobs, requests, entries)
			}
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+octet_length(native_receipt)) FROM nfo_write_entries`).Scan(&bytes); err != nil || bytes != capacity {
				t.Fatal("first batch did not fill exact byte capacity", err)
			}
		})
	}
}
