package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/jackc/pgx/v5/pgconn"
)

// Only a private SQL fixture can create these jobs until admission and commit
// recovery are connected. No runtime capability or read-write mode is enabled.
func nfoWriteJobFixture(t *testing.T, f jobFixture, prepared domain.NFOWritePreparation, key, priority string) domain.Job {
	t.Helper()
	tx, err := f.s.authorizedJobs(f.ctx, f.a)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	p := f.policy
	j, err := scanJob(tx.QueryRow(f.ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1::uuid,$2::uuid,$3,'nfo_write',$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+jobColumns, prepared.Scope.LibraryID, f.a.UserID, key, priority, p.QueueLimit, p.HistoryLimit, p.MaxEntries, p.MaxDirectories, p.MaxAttempts, p.MissingCountLimit, p.MissingPercentLimit))
	if err != nil {
		t.Fatal("create private write job", err)
	}
	if err := copyNFOWriteIntents(f.ctx, tx, j.ID, []string{prepared.ID}); err != nil {
		t.Fatal("copy job-owned intent", err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal("publish complete private fixture", err)
	}
	return j
}

func nfoWriteLeaseFixture(t *testing.T, f jobFixture, id string) domain.JobLease {
	t.Helper()
	l, err := scanLease(f.s.Pool.QueryRow(f.ctx, `UPDATE jobs SET state='running',owner='nfo-write-fixture',generation=generation+1,attempts=attempts+1,lease_until=clock_timestamp()+interval '1 minute',started_at=COALESCE(started_at,clock_timestamp()) WHERE id=$1::uuid RETURNING `+leaseColumns, id))
	if err != nil {
		t.Fatal("claim private fixture", err)
	}
	return l
}

func TestNFOWriteJobsOwnIntentAfterPreparationCleanupAndPoolReopen(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "job-owned", request)
	if err != nil {
		t.Fatal(err)
	}
	j := nfoWriteJobFixture(t, f, saved, "owned-job", domain.JobPriorityManual)
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations WHERE id=$1::uuid`, saved.ID); err != nil {
		t.Fatal("clean up preparation fixture", err)
	}
	fresh, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Pool.Close()
	l := nfoWriteLeaseFixture(t, f, j.ID)
	task, err := fresh.GetNFOWriteTask(f.ctx, l, 1)
	if err != nil || task.Sequence != 1 || task.JobID != j.ID || task.Preparation.ID != saved.ID || task.Preparation.Scope != saved.Scope || task.Preparation.Stamp != saved.Stamp || !bytes.Equal(task.Preparation.Original, saved.Original) || !bytes.Equal(task.Preparation.Replacement, saved.Replacement) {
		t.Fatal("job intent depended on preparation retention", err)
	}
	encoded, _ := json.Marshal(task)
	if string(encoded) != "{}" || strings.Contains(fmt.Sprintf("%+v %#v", task, task), saved.Scope.Source.RootPath) {
		t.Fatal("private task leaked")
	}
	task.Preparation.Replacement[0] = 0
	task.Preparation.Request.Edits[0].Value = "caller"
	again, err := fresh.GetNFOWriteTask(f.ctx, l, 1)
	if err != nil || !bytes.Equal(again.Preparation.Replacement, saved.Replacement) || again.Preparation.Request.Edits[0].Value != request.Edits[0].Value {
		t.Fatal("caller changed immutable job data")
	}
	// Isolated adapter verification consumes job-owned bytes after preparation
	// cleanup. There is still no runtime writer or durable commit journal.
	b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	w, _ := nfo.NewWriterWithBudget(b)
	source, err := nfo.ReadSource(f.ctx, saved.Scope.Source.RootPath, saved.Scope.Source.RelativePath, saved.Request.MaxBytes)
	if err != nil || w.ReplacePrepared(f.ctx, source, again.Preparation) != nil {
		t.Fatal("job-owned recipe cannot reconstruct fixed output")
	}
	after, _ := os.ReadFile(filepath.Join(saved.Scope.Source.RootPath, saved.Scope.Source.RelativePath))
	if !bytes.Equal(after, saved.Replacement) || b.Stats() != (resources.Stats{}) {
		t.Fatal("job-owned output regenerated UUID or leaked permit")
	}
}

func TestNFOWriteJobsFenceObservationAndExcludeOldWorker(t *testing.T) {
	for _, scenario := range []string{"owner", "generation", "expired", "cancelled", "disabled actor", "generic finish"} {
		t.Run(scenario, func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			saved, _, err := service.Prepare(f.ctx, f.a, "fenced", request)
			if err != nil {
				t.Fatal(err)
			}
			j := nfoWriteJobFixture(t, f, saved, "fenced-job", domain.JobPriorityManual)
			if _, err := f.s.ClaimJobWithCapabilities(f.ctx, "old-worker", false, time.Minute, domain.ScanCapabilities{Probe: true, NFO: true, Ignore: true, FamilyIgnore: true, CatalogImport: true}); err != domain.ErrNotFound {
				t.Fatal("old worker claimed unsupported write job", err)
			}
			l := nfoWriteLeaseFixture(t, f, j.ID)
			want := domain.ErrJobLeaseLost
			switch scenario {
			case "owner":
				l.Owner = "other-owner"
			case "generation":
				l.Generation++
			case "expired":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, j.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.s.ClaimJob(f.ctx, "old-recovery", false, time.Minute); err != domain.ErrNotFound {
					t.Fatal("old worker recovered write job", err)
				}
				var state, owner string
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT state,owner FROM jobs WHERE id=$1::uuid`, j.ID).Scan(&state, &owner); err != nil || state != "running" || owner != l.Owner {
					t.Fatal("unsupported recovery mutated lease")
				}
			case "cancelled":
				if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
					t.Fatal(err)
				}
				want = context.Canceled
			case "disabled actor":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID); err != nil {
					t.Fatal(err)
				}
				want = domain.ErrForbidden
			case "generic finish":
				l.Job.Kind = "inventory_scan"
				if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != domain.ErrInvalid {
					t.Fatal("generic finish published a write job", err)
				}
				return
			}
			if task, err := f.s.GetNFOWriteTask(f.ctx, l, 1); err != want || task.JobID != "" || len(task.Preparation.Replacement) != 0 {
				t.Fatal("stale or unauthorized observation returned private data", err)
			}
		})
	}
}

func TestNFOWriteJobsImmutableBatchAndMetrics(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "metrics", request)
	if err != nil {
		t.Fatal(err)
	}
	for _, priority := range []string{domain.JobPriorityBackground, domain.JobPriorityManual} {
		j := nfoWriteJobFixture(t, f, saved, "metric-"+priority, priority)
		for _, statement := range []string{
			`UPDATE nfo_write_entries SET replacement_bytes=original_bytes WHERE job_id=$1::uuid`,
			`UPDATE nfo_write_requests SET total=2 WHERE job_id=$1::uuid`,
			`DELETE FROM nfo_write_entries WHERE job_id=$1::uuid`,
			`UPDATE jobs SET kind='inventory_scan' WHERE id=$1::uuid`,
		} {
			_, err := f.s.Pool.Exec(f.ctx, statement, j.ID)
			jobMetricMigrationRejected(t, err)
		}
		if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := f.s.JobMetrics(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range snapshot.Groups {
		if g.Kind == domain.JobNFOWrite {
			if g.Cancelled != 1 || g.Wait.Count != 0 || g.Duration.Count != 0 {
				t.Fatal("write cancellation counter or never-started histogram differs", g)
			}
		} else if g.Cancelled != 0 {
			t.Fatal("write transitions changed other kinds", g)
		}
	}
}

func TestNFOWriteJobsMigrationPreservesEpochAndRefusesRetainedData(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newJobFixture(t)
		jobMetricMigration(t, f, "down", 46)
		before := jobMetricMigrationStorage(t, f)
		jobMetricMigration(t, f, "up", SchemaVersion)
		jobMetricMigration(t, f, "down", 46)
		if jobMetricMigrationStorage(t, f) != before {
			t.Fatal("round trip reset existing metric epoch or totals")
		}
		jobMetricMigration(t, f, "up", SchemaVersion)
	})
	t.Run("retained", func(t *testing.T) {
		f, service, _, request := nfoWritePreparationFixture(t)
		jobMetricMigration(t, f, "down", 47)
		saved, _, err := service.Prepare(f.ctx, f.a, "retained-job", request)
		if err != nil {
			t.Fatal(err)
		}
		j := nfoWriteJobFixture(t, f, saved, "retained-job", domain.JobPriorityManual)
		if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
			t.Fatal("downgrade discarded job intent")
		}
		version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
		if err != nil || version != 46 || !dirty || f.s.Ready(f.ctx) == nil {
			t.Fatal("retained refusal lost dirty schema state")
		}
		l := nfoWriteLeaseFixture(t, f, j.ID)
		task, err := f.s.GetNFOWriteTask(f.ctx, l, 1)
		if err != nil || !bytes.Equal(task.Preparation.Replacement, saved.Replacement) {
			t.Fatal("refused downgrade lost bytes")
		}
	})
	t.Run("metrics only", func(t *testing.T) {
		f, service, _, request := nfoWritePreparationFixture(t)
		jobMetricMigration(t, f, "down", 47)
		saved, _, err := service.Prepare(f.ctx, f.a, "retained-metrics", request)
		if err != nil {
			t.Fatal(err)
		}
		j := nfoWriteJobFixture(t, f, saved, "metric-job", domain.JobPriorityManual)
		if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, j.ID); err != nil {
			t.Fatal(err)
		}
		before := jobMetricMigrationStorage(t, f)
		if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil || jobMetricMigrationStorage(t, f) != before {
			t.Fatal("downgrade discarded counters after history cleanup")
		}
	})
}

func TestNFOWriteJobsRejectOrphanAndPartialBatch(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "complete-base", request)
	if err != nil {
		t.Fatal(err)
	}
	j := nfoWriteJobFixture(t, f, saved, "complete-base", domain.JobPriorityManual)
	if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
		t.Fatal(err)
	}
	for _, partial := range []bool{false, true} {
		tx, err := f.s.Pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		err = tx.QueryRow(f.ctx, `INSERT INTO jobs SELECT (jsonb_populate_record(NULL::jobs,to_jsonb(j)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key','incomplete'))).* FROM jobs j WHERE id=$1::uuid RETURNING id::text`, j.ID).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		if partial {
			if _, err := tx.Exec(f.ctx, `INSERT INTO nfo_write_requests SELECT $1::uuid,library_id,generation,2,intent_digest FROM nfo_write_requests WHERE job_id=$2::uuid`, id, j.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(f.ctx, `INSERT INTO nfo_write_entries SELECT (jsonb_populate_record(NULL::nfo_write_entries,to_jsonb(e)||jsonb_build_object('job_id',$1::uuid))).* FROM nfo_write_entries e WHERE job_id=$2::uuid`, id, j.ID); err != nil {
				t.Fatal(err)
			}
		}
		jobMetricMigrationRejected(t, tx.Commit(f.ctx))
		_ = tx.Rollback(f.ctx)
		var count int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM jobs WHERE id=$1::uuid`, id).Scan(&count); err != nil || count != 0 {
			t.Fatal("incomplete batch published a partial job")
		}
	}
}

func TestNFOWriteJobsByteCapacityRollsBackWholeBatch(t *testing.T) {
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "capacity-base", request)
	if err != nil {
		t.Fatal(err)
	}
	j := nfoWriteJobFixture(t, f, saved, "capacity-base", domain.JobPriorityManual)
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	query := `WITH large AS MATERIALIZED (SELECT convert_to(rpad('<movie/>',33554432,' '),'UTF8') AS value), target AS MATERIALIZED (SELECT gen_random_uuid() AS item), recipe AS MATERIALIZED (SELECT e.*,target.item,convert_to((convert_from(request_bytes,'UTF8')::jsonb||jsonb_build_object('itemId',target.item,'maxBytes',33554432))::text,'UTF8') AS encoded FROM nfo_write_entries e CROSS JOIN target WHERE job_id=$1::uuid AND sequence=1)
 INSERT INTO nfo_write_entries(job_id,sequence,preparation_id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256)
 SELECT job_id,$2,gen_random_uuid(),version,encoded,encode(sha256(encoded),'hex'),library_id,item,source_id,root_id,kind,revision,generation,root_path,relative_path,media_path,directory_path,33554432,modified_unix_nano,value,encode(sha256(value),'hex'),value,encode(sha256(value),'hex') FROM recipe CROSS JOIN large`
	if _, err := tx.Exec(f.ctx, query, j.ID, 2); err != nil {
		t.Fatal("capacity rejected below 128MiB", err)
	}
	_, err = tx.Exec(f.ctx, query, j.ID, 3)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "nfo write intent capacity reached" {
		t.Fatal("capacity refused for an unrelated constraint", err)
	}
	_ = tx.Rollback(f.ctx)
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_entries WHERE job_id=$1::uuid`, j.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("capacity refusal retained provisional bytes")
	}
}
