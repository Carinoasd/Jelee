package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const cloneQuotaPreparation = `INSERT INTO nfo_write_preparations SELECT (jsonb_populate_record(NULL::nfo_write_preparations,to_jsonb(p)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key',$2::text))).* FROM nfo_write_preparations p WHERE id=$1::uuid`

func TestNFOQuotaConcurrentSnapshotsCannotExceedActorLimit(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			saved, _, err := service.Prepare(f.ctx, f.a, "quota-source", request)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 30; i++ {
				if _, err := f.s.Pool.Exec(f.ctx, cloneQuotaPreparation, saved.ID, fmt.Sprintf("seed-%d", i)); err != nil {
					t.Fatal("seed below actor capacity", err)
				}
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
				var count int
				if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_preparations`).Scan(&count); err != nil || count != 31 {
					t.Fatal("concurrent snapshots were not established", err)
				}
			}
			if _, err := first.Exec(f.ctx, cloneQuotaPreparation, saved.ID, "first-last-slot"); err != nil {
				t.Fatal("first writer rejected below capacity", err)
			}
			if err := first.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			_, err = second.Exec(f.ctx, cloneQuotaPreparation, saved.ID, "second-last-slot")
			if err == nil {
				err = second.Commit(f.ctx)
			}
			if err == nil {
				t.Fatal("concurrent snapshot exceeded the 32-row actor limit")
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" && pgErr.Code != "40001" {
				t.Fatal("quota refused for unrelated failure", err)
			}
			_ = second.Rollback(f.ctx)
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_preparations`).Scan(&count); err != nil || count != 32 {
				t.Fatal("failed transaction retained excess rows", err)
			}
		})
	}
}

// These SQL fixtures exercise storage quotas, not writer admission. Their
// terminal jobs avoid the active-library uniqueness constraint.
func cloneQuotaJob(t *testing.T, f jobFixture, tx pgx.Tx, base, key string, total int) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(f.ctx, `INSERT INTO jobs SELECT (jsonb_populate_record(NULL::jobs,to_jsonb(j)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key',$2::text))).* FROM jobs j WHERE id=$1::uuid RETURNING id::text`, base, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `INSERT INTO nfo_write_requests SELECT $1::uuid,library_id,generation,$3,intent_digest FROM nfo_write_requests WHERE job_id=$2::uuid`, id, base, total); err != nil {
		t.Fatal(err)
	}
	return id
}

const cloneQuotaEntry = `WITH targets AS MATERIALIZED (SELECT n,gen_random_uuid() AS item FROM generate_series(1,$3::integer) n), recipes AS MATERIALIZED (SELECT e.*,n,item,convert_to((convert_from(request_bytes,'UTF8')::jsonb||jsonb_build_object('itemId',item))::text,'UTF8') AS encoded FROM nfo_write_entries e CROSS JOIN targets WHERE job_id=$2::uuid AND sequence=1)
 INSERT INTO nfo_write_entries SELECT (jsonb_populate_record(NULL::nfo_write_entries,to_jsonb(r)-'n'-'item'-'encoded'||jsonb_build_object('job_id',$1::uuid,'sequence',n,'item_id',item,'request_bytes',encoded,'request_digest',encode(sha256(encoded),'hex')))).* FROM recipes r`

func TestNFOQuotaJobConcurrentSnapshotsAndGlobalRows(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			saved, _, err := service.Prepare(f.ctx, f.a, "quota-job-source", request)
			if err != nil {
				t.Fatal(err)
			}
			j := nfoWriteJobFixture(t, f, saved, "quota-job", domain.JobPriorityManual)
			if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 11; i++ {
				total := 100
				if i == 10 {
					total = 22
				}
				tx, err := f.s.Pool.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				id := cloneQuotaJob(t, f, tx, j.ID, fmt.Sprintf("seed-job-%d", i), total)
				if _, err := tx.Exec(f.ctx, cloneQuotaEntry, id, j.ID, total); err != nil {
					_ = tx.Rollback(f.ctx)
					t.Fatal(err)
				}
				if err := tx.Commit(f.ctx); err != nil {
					t.Fatal(err)
				}
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
				var count int
				if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_entries`).Scan(&count); err != nil || count != 1023 {
					t.Fatal("global row snapshot", err, count)
				}
			}
			id := cloneQuotaJob(t, f, first, j.ID, "first-final-slot", 1)
			if _, err := first.Exec(f.ctx, cloneQuotaEntry, id, j.ID, 1); err != nil {
				t.Fatal(err)
			}
			if err := first.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			err = second.QueryRow(f.ctx, `INSERT INTO jobs SELECT (jsonb_populate_record(NULL::jobs,to_jsonb(j)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key','second-final-slot'))).* FROM jobs j WHERE id=$1::uuid RETURNING id::text`, j.ID).Scan(&id)
			if err == nil {
				_, err = second.Exec(f.ctx, `INSERT INTO nfo_write_requests SELECT $1::uuid,library_id,generation,1,intent_digest FROM nfo_write_requests WHERE job_id=$2::uuid`, id, j.ID)
			}
			if err == nil {
				_, err = second.Exec(f.ctx, cloneQuotaEntry, id, j.ID, 1)
			}
			if err == nil {
				err = second.Commit(f.ctx)
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" && pgErr.Code != "40001" {
				t.Fatal("global quota exceeded or unrelated refusal", err)
			}
			_ = second.Rollback(f.ctx)
			var count, requests int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_entries),(SELECT count(*) FROM nfo_write_requests)`).Scan(&count, &requests); err != nil || count != 1024 || requests != 13 {
				t.Fatal("failed admission retained partial data", err, count, requests)
			}
		})
	}
}

func TestNFOQuotaFenceMigrationAndMissingRow(t *testing.T) {
	t.Run("empty round trip", func(t *testing.T) {
		f := newJobFixture(t)
		before := jobMetricMigrationStorage(t, f)
		jobMetricMigration(t, f, "down", 47)
		jobMetricMigration(t, f, "up", SchemaVersion)
		var count int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_quota_fences`).Scan(&count); err != nil || count != 2 || jobMetricMigrationStorage(t, f) != before {
			t.Fatal("fence round trip changed metrics", err)
		}
	})
	t.Run("retained preparation", func(t *testing.T) {
		f, _, _, request := nfoWritePreparationFixture(t)
		legacyNFOWritePreparationFixture(t, f, request, "retained-fence", 48)
		if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
			t.Fatal("removed retained fence")
		}
		version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
		if err != nil || version != 47 || !dirty || f.s.Ready(f.ctx) == nil {
			t.Fatal("retained refusal lost dirty state", err)
		}
		var count int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_preparations`).Scan(&count); err != nil || count != 1 {
			t.Fatal("lost retained data", err)
		}
	})
	t.Run("missing fence fails closed", func(t *testing.T) {
		f, service, _, request := nfoWritePreparationFixture(t)
		saved, _, err := service.Prepare(f.ctx, f.a, "missing-fence", request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_quota_fences WHERE scope='preparation'`); err != nil {
			t.Fatal(err)
		}
		_, err = f.s.Pool.Exec(f.ctx, cloneQuotaPreparation, saved.ID, "must-refuse")
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "nfo quota fence is missing" {
			t.Fatal("missing fence admitted data", err)
		}
	})
}

func TestNFOQuotaJobGlobalBytesRollBackAdmission(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "global-bytes-source", request)
	if err != nil {
		t.Fatal(err)
	}
	j := nfoWriteJobFixture(t, f, saved, "global-bytes-base", domain.JobPriorityManual)
	if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
		t.Fatal(err)
	}
	// Logical byte sizes are measured before TOAST compression. Every individual
	// job is below 128 MiB, so only the global 512 MiB guard can refuse job eight.
	query := `WITH large AS MATERIALIZED (SELECT convert_to(rpad('<movie/>',33554432,' '),'UTF8') AS value), target AS MATERIALIZED (SELECT gen_random_uuid() AS item), recipe AS MATERIALIZED (SELECT e.*,target.item,convert_to((convert_from(request_bytes,'UTF8')::jsonb||jsonb_build_object('itemId',target.item,'maxBytes',33554432))::text,'UTF8') AS encoded FROM nfo_write_entries e CROSS JOIN target WHERE job_id=$2::uuid AND sequence=1)
 INSERT INTO nfo_write_entries(job_id,sequence,preparation_id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256)
 SELECT $1::uuid,1,gen_random_uuid(),version,encoded,encode(sha256(encoded),'hex'),library_id,item,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,33554432,modified_unix_nano,value,encode(sha256(value),'hex'),value,encode(sha256(value),'hex') FROM recipe CROSS JOIN large`
	for i := 0; i < 8; i++ {
		tx, err := f.s.Pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		id := cloneQuotaJob(t, f, tx, j.ID, fmt.Sprintf("global-bytes-%d", i), 1)
		_, err = tx.Exec(f.ctx, query, id, j.ID)
		if i < 7 {
			if err != nil {
				_ = tx.Rollback(f.ctx)
				t.Fatal("rejected below global capacity", err)
			}
			if err := tx.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "nfo write intent capacity reached" {
			_ = tx.Rollback(f.ctx)
			t.Fatal("global bytes guard refused for unrelated reason", err)
		}
		_ = tx.Rollback(f.ctx)
		var entries, requests, jobs int
		var size int64
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*),sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),(SELECT count(*) FROM nfo_write_requests),(SELECT count(*) FROM jobs WHERE kind='nfo_write') FROM nfo_write_entries`).Scan(&entries, &size, &requests, &jobs); err != nil || entries != 8 || requests != 8 || jobs != 8 || size <= 7*67108864 || size > 536870912 {
			t.Fatal("excess admission retained partial data", err, entries, requests, jobs, size)
		}
	}
}

func TestNFOQuotaFenceRetainedJobAndMissingIntentFence(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			var saved domain.NFOWritePreparation
			if !missing {
				saved = legacyNFOWritePreparationFixture(t, f, request, "intent-fence-source", 48)
			} else {
				var err error
				saved, _, err = service.Prepare(f.ctx, f.a, "intent-fence-source", request)
				if err != nil {
					t.Fatal(err)
				}
			}
			j := nfoWriteJobFixture(t, f, saved, "intent-fence-base", domain.JobPriorityManual)
			if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations`); err != nil {
				t.Fatal(err)
			}
			if !missing {
				if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
					t.Fatal("removed fence with retained job bytes")
				}
				version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
				if err != nil || version != 47 || !dirty || f.s.Ready(f.ctx) == nil {
					t.Fatal("retained intent refusal lost dirty state", err)
				}
			} else {
				if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_quota_fences WHERE scope='job_intent'`); err != nil {
					t.Fatal(err)
				}
				tx, err := f.s.Pool.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(f.ctx)
				id := cloneQuotaJob(t, f, tx, j.ID, "missing-intent-fence", 1)
				_, err = tx.Exec(f.ctx, cloneQuotaEntry, id, j.ID, 1)
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "nfo quota fence is missing" {
					t.Fatal("missing intent fence admitted data", err)
				}
				_ = tx.Rollback(f.ctx)
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_entries`).Scan(&count); err != nil || count != 1 {
				t.Fatal("lost retained intent", err)
			}
		})
	}
}
