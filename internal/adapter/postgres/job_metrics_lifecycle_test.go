package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func lifecycleMetrics(t *testing.T, f jobFixture) app.JobMetricsSnapshot {
	t.Helper()
	value, err := f.s.JobMetrics(f.ctx)
	if err != nil {
		t.Fatal("read job lifecycle metrics", err)
	}
	return value
}

func lifecycleMetricGroup(t *testing.T, value app.JobMetricsSnapshot, kind, priority string) app.JobMetricGroup {
	t.Helper()
	for _, group := range value.Groups {
		if group.Kind == kind && group.Priority == priority {
			return group
		}
	}
	t.Fatal("job metrics group is missing", kind, priority)
	return app.JobMetricGroup{}
}

type lifecycleMetricDelta struct {
	succeeded, failed, cancelled int64
	wait, duration               uint64
}

func assertLifecycleMetricDelta(t *testing.T, before, after app.JobMetricsSnapshot, kind, priority string, want lifecycleMetricDelta) {
	t.Helper()
	if !before.StartedAt.Equal(after.StartedAt) {
		t.Fatal("job metrics epoch changed during a lifecycle transition")
	}
	for _, old := range before.Groups {
		got := lifecycleMetricGroup(t, after, old.Kind, old.Priority)
		delta := lifecycleMetricDelta{}
		if old.Kind == kind && old.Priority == priority {
			delta = want
		}
		if got.Succeeded != old.Succeeded+delta.succeeded || got.Failed != old.Failed+delta.failed || got.Cancelled != old.Cancelled+delta.cancelled || got.Wait.Count != old.Wait.Count+delta.wait || got.Duration.Count != old.Duration.Count+delta.duration {
			t.Fatalf("unexpected lifecycle totals for %s/%s: before=%+v after=%+v delta=%+v", old.Kind, old.Priority, old, got, delta)
		}
		for _, hist := range []app.JobMetricHistogram{got.Wait, got.Duration} {
			var count uint64
			for _, bucket := range hist.BucketCounts {
				count += bucket
			}
			if count != hist.Count || hist.SumSeconds < 0 || math.IsNaN(hist.SumSeconds) || math.IsInf(hist.SumSeconds, 0) {
				t.Fatal("lifecycle histogram lost an observation", hist)
			}
		}
		if delta.wait == 0 && got.Wait != old.Wait || delta.duration == 0 && got.Duration != old.Duration {
			t.Fatal("transition without a sample changed a histogram")
		}
		if got.Wait.SumSeconds < old.Wait.SumSeconds || got.Duration.SumSeconds < old.Duration.SumSeconds {
			t.Fatal("lifecycle cumulative time decreased")
		}
	}
}

func assertLifecycleMetricsUnchanged(t *testing.T, before, after app.JobMetricsSnapshot) {
	t.Helper()
	assertLifecycleMetricDelta(t, before, after, "", "", lifecycleMetricDelta{})
}

func deleteJobMetricsFixtureLibrary(t *testing.T, f jobFixture) {
	t.Helper()
	// Jobs own directory checkpoints; accepted snapshots outlive those jobs.
	// Both retain root foreign keys, so remove them before the library's roots.
	for _, query := range []string{
		`DELETE FROM jobs WHERE library_id=$1::uuid`,
		`DELETE FROM library_inventory_baseline_data WHERE library_id=$1::uuid`,
		`DELETE FROM libraries WHERE id=$1::uuid`,
	} {
		if _, err := f.s.Pool.Exec(f.ctx, query, f.registration.Library.ID); err != nil {
			t.Fatal("delete metrics fixture library dependencies", err)
		}
	}
	var remaining int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT
 (SELECT count(*) FROM jobs WHERE library_id=$1::uuid)+
 (SELECT count(*) FROM library_inventory_baseline_data WHERE library_id=$1::uuid)+
 (SELECT count(*) FROM library_roots WHERE library_id=$1::uuid)+
 (SELECT count(*) FROM libraries WHERE id=$1::uuid)`, f.registration.Library.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("metrics fixture library rows survived deletion", remaining, err)
	}
}

func TestJobMetricsPublicClaimCheckpointReleaseAndRetry(t *testing.T) {
	f := newJobFixture(t)
	before := lifecycleMetrics(t, f)
	job := f.submit(t, "metrics-first")
	queued := lifecycleMetrics(t, f)
	assertLifecycleMetricsUnchanged(t, before, queued)
	if group := lifecycleMetricGroup(t, queued, "inventory_scan", domain.JobPriorityManual); group.Queued != 1 || group.Running != 0 {
		t.Fatal("submitted job is not queued in the snapshot")
	}
	first := f.claim(t, "metrics-first-owner")
	claimed := lifecycleMetrics(t, f)
	assertLifecycleMetricDelta(t, before, claimed, "inventory_scan", domain.JobPriorityManual, lifecycleMetricDelta{wait: 1})
	if first.Job.StartedAt == nil {
		t.Fatal("first claim did not retain its start timestamp")
	}
	wait := lifecycleMetricGroup(t, claimed, "inventory_scan", domain.JobPriorityManual).Wait.SumSeconds
	if math.Abs(wait-first.Job.StartedAt.Sub(job.CreatedAt).Seconds()) > 0.000001 {
		t.Fatal("wait did not measure initial submission to first start", wait)
	}
	directory := f.directory(t, first)
	if cancelled, err := f.s.HeartbeatJob(f.ctx, first, time.Minute); err != nil || cancelled {
		t.Fatal("heartbeat", err)
	}
	if err := f.s.SaveScanBatch(f.ctx, first, directory, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(directory, "kept.mkv", 7)}, Done: true}); err != nil {
		t.Fatal("checkpoint", err)
	}
	assertLifecycleMetricsUnchanged(t, claimed, lifecycleMetrics(t, f))
	if err := f.s.ReleaseJob(f.ctx, first); err != nil {
		t.Fatal("release", err)
	}
	released := lifecycleMetrics(t, f)
	assertLifecycleMetricsUnchanged(t, claimed, released)
	if group := lifecycleMetricGroup(t, released, "inventory_scan", domain.JobPriorityManual); group.Queued != 1 || group.Running != 0 {
		t.Fatal("released job did not return to queued metrics")
	}
	second := f.claim(t, "metrics-second-owner")
	if second.Job.ID != first.Job.ID || second.Job.Attempts != 2 || second.Job.StartedAt == nil || !second.Job.StartedAt.Equal(*first.Job.StartedAt) {
		t.Fatal("reclaim did not retain the original start")
	}
	assertLifecycleMetricsUnchanged(t, claimed, lifecycleMetrics(t, f))
	if err := f.s.FinishJob(f.ctx, second, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal("finish failed job", err)
	}
	failed := lifecycleMetrics(t, f)
	assertLifecycleMetricDelta(t, claimed, failed, "inventory_scan", domain.JobPriorityManual, lifecycleMetricDelta{failed: 1, duration: 1})
	terminal := f.get(t, job.ID)
	duration := lifecycleMetricGroup(t, failed, "inventory_scan", domain.JobPriorityManual).Duration.SumSeconds
	if terminal.FinishedAt == nil || math.Abs(duration-terminal.FinishedAt.Sub(*first.Job.StartedAt).Seconds()) > 0.000001 {
		t.Fatal("duration did not use first start through final completion", duration)
	}
	if err := f.s.FinishJob(f.ctx, second, domain.JobFailed, "scan_io"); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("duplicate finish accepted", err)
	}
	retry, replay, err := f.s.RetryJob(f.ctx, f.a, job.ID, "metrics-retry", f.policy)
	if err != nil || replay || retry.ID == job.ID {
		t.Fatal("retry admission", err)
	}
	again, replay, err := f.s.RetryJob(f.ctx, f.a, job.ID, "metrics-retry", f.policy)
	if err != nil || !replay || again.ID != retry.ID {
		t.Fatal("retry idempotency", err)
	}
	assertLifecycleMetricsUnchanged(t, failed, lifecycleMetrics(t, f))
	lease := f.claim(t, "metrics-retry-owner")
	directory = f.directory(t, lease)
	if err := f.s.SaveScanBatch(f.ctx, lease, directory, domain.ScanBatch{Done: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishJob(f.ctx, lease, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	assertLifecycleMetricDelta(t, failed, lifecycleMetrics(t, f), "inventory_scan", domain.JobPriorityManual, lifecycleMetricDelta{succeeded: 1, wait: 1, duration: 1})
}

func TestJobMetricsCancellationAndReleaseOutcomes(t *testing.T) {
	f := newJobFixture(t)
	for _, mode := range []string{"queued", "requeued", "release-cancel", "finish-cancel", "release-exhausted"} {
		t.Run(mode, func(t *testing.T) {
			before := lifecycleMetrics(t, f)
			f.policy = jobTestPolicy()
			if mode == "release-exhausted" {
				f.policy.MaxAttempts = 1
			}
			job := f.submit(t, "metrics-"+mode)
			want := lifecycleMetricDelta{cancelled: 1}
			var lease domain.JobLease
			if mode != "queued" {
				lease = f.claim(t, "metrics-"+mode)
				want.wait, want.duration = 1, 1
			}
			if mode == "release-exhausted" {
				if err := f.s.ReleaseJob(f.ctx, lease); err != nil {
					t.Fatal(err)
				}
				want.failed, want.cancelled = 1, 0
			} else {
				if mode == "requeued" {
					if err := f.s.ReleaseJob(f.ctx, lease); err != nil {
						t.Fatal(err)
					}
				}
				preCancel := lifecycleMetrics(t, f)
				if _, err := f.s.CancelJob(f.ctx, f.a, job.ID); err != nil {
					t.Fatal(err)
				}
				if mode == "release-cancel" || mode == "finish-cancel" {
					assertLifecycleMetricsUnchanged(t, preCancel, lifecycleMetrics(t, f))
					if flag, err := f.s.HeartbeatJob(f.ctx, lease, time.Minute); err != nil || !flag {
						t.Fatal("running cancellation flag was not retained", err)
					}
					var err error
					if mode == "release-cancel" {
						err = f.s.ReleaseJob(f.ctx, lease)
					} else {
						err = f.s.FinishJob(f.ctx, lease, domain.JobCancelled, "")
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			after := lifecycleMetrics(t, f)
			assertLifecycleMetricDelta(t, before, after, "inventory_scan", domain.JobPriorityManual, want)
			if _, err := f.s.CancelJob(f.ctx, f.a, job.ID); err != nil {
				t.Fatal("terminal cancellation replay", err)
			}
			assertLifecycleMetricsUnchanged(t, after, lifecycleMetrics(t, f))
		})
	}
}

func TestJobMetricsRecoveryAndOldOwnerFences(t *testing.T) {
	for _, mode := range []string{"requeue", "cancelled", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			f := newJobFixture(t)
			if mode == "exhausted" {
				f.policy.MaxAttempts = 1
			}
			job := f.submit(t, "metrics-recovery")
			old := f.claim(t, "metrics-old-owner")
			directory := f.directory(t, old)
			if mode == "cancelled" {
				if _, err := f.s.CancelJob(f.ctx, f.a, job.ID); err != nil {
					t.Fatal(err)
				}
			}
			before := lifecycleMetrics(t, f)
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, job.ID); err != nil {
				t.Fatal(err)
			}
			expired := lifecycleMetrics(t, f)
			assertLifecycleMetricsUnchanged(t, before, expired)
			if group := lifecycleMetricGroup(t, expired, "inventory_scan", domain.JobPriorityManual); group.Running != 0 || group.ExpiredRunning != 1 {
				t.Fatal("expired lease was reported as running")
			}
			current, err := f.s.ClaimJobWithCapabilities(f.ctx, "metrics-recovery-owner", false, time.Minute, domain.ScanCapabilities{})
			want := lifecycleMetricDelta{}
			if mode == "requeue" {
				if err != nil || current.Job.ID != job.ID || current.Job.Attempts != 2 || current.Generation <= old.Generation {
					t.Fatal("recovery did not reclaim once", err)
				}
			} else {
				if !errors.Is(err, domain.ErrNotFound) {
					t.Fatal("terminal recovery was claimed", err)
				}
				want.duration = 1
				if mode == "cancelled" {
					want.cancelled = 1
				} else {
					want.failed = 1
				}
			}
			after := lifecycleMetrics(t, f)
			assertLifecycleMetricDelta(t, before, after, "inventory_scan", domain.JobPriorityManual, want)
			for _, stale := range []func() error{
				func() error { _, err := f.s.HeartbeatJob(f.ctx, old, time.Minute); return err },
				func() error { return f.s.SaveScanBatch(f.ctx, old, directory, domain.ScanBatch{Done: true}) },
				func() error { return f.s.FinishJob(f.ctx, old, domain.JobFailed, "scan_io") },
				func() error { return f.s.ReleaseJob(f.ctx, old) },
			} {
				if err := stale(); !errors.Is(err, domain.ErrJobLeaseLost) {
					t.Fatal("stale owner bypassed recovery fence", err)
				}
			}
			if _, err := f.s.ClaimJobWithCapabilities(f.ctx, "metrics-later-poll", false, time.Minute, domain.ScanCapabilities{}); !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("repeated recovery found another job", err)
			}
			assertLifecycleMetricsUnchanged(t, after, lifecycleMetrics(t, f))
		})
	}
}

func TestJobMetricsHistoryTrimDoesNotResetTotals(t *testing.T) {
	f := newJobFixture(t)
	f.policy.HistoryLimit = 1
	for i := 0; i < 5; i++ {
		before := lifecycleMetrics(t, f)
		f.complete(t, fmt.Sprintf("metrics-history-%d", i), []string{"kept.mkv"}, 0)
		assertLifecycleMetricDelta(t, before, lifecycleMetrics(t, f), "inventory_scan", domain.JobPriorityManual, lifecycleMetricDelta{succeeded: 1, wait: 1, duration: 1})
		var retained int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM jobs`).Scan(&retained); err != nil || retained != 1 {
			t.Fatal("test did not exercise real history trimming", retained, err)
		}
	}
	before := lifecycleMetrics(t, f)
	deleteJobMetricsFixtureLibrary(t, f)
	assertLifecycleMetricsUnchanged(t, before, lifecycleMetrics(t, f))
}

func TestJobMetricsTwoStoresCountOnlyCommittedTransitions(t *testing.T) {
	f := newJobFixture(t)
	other, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal("open second metrics store")
	}
	t.Cleanup(other.Pool.Close)
	f.submit(t, "metrics-competing-claim")
	before := lifecycleMetrics(t, f)
	type claimResult struct {
		lease domain.JobLease
		err   error
	}
	claims := make(chan claimResult, 2)
	start := make(chan struct{})
	for i, store := range []*Store{f.s, other} {
		go func() {
			<-start
			lease, err := store.ClaimJob(f.ctx, fmt.Sprintf("metrics-competing-%d", i), false, time.Minute)
			claims <- claimResult{lease, err}
		}()
	}
	close(start)
	var winner domain.JobLease
	won, missed := 0, 0
	for range 2 {
		result := <-claims
		if result.err == nil {
			winner, won = result.lease, won+1
		} else if errors.Is(result.err, domain.ErrNotFound) {
			missed++
		} else {
			t.Fatal("competing claim", result.err)
		}
	}
	if won != 1 || missed != 1 {
		t.Fatal("more than one store claimed the same job", won, missed)
	}
	claimed := lifecycleMetrics(t, f)
	assertLifecycleMetricDelta(t, before, claimed, "inventory_scan", domain.JobPriorityManual, lifecycleMetricDelta{wait: 1})
	finishes := make(chan error, 2)
	start = make(chan struct{})
	for _, store := range []*Store{f.s, other} {
		go func() { <-start; finishes <- store.FinishJob(f.ctx, winner, domain.JobFailed, "scan_io") }()
	}
	close(start)
	won, missed = 0, 0
	for range 2 {
		err := <-finishes
		if err == nil {
			won++
		} else if errors.Is(err, domain.ErrJobLeaseLost) {
			missed++
		} else {
			t.Fatal("competing finish", err)
		}
	}
	if won != 1 || missed != 1 {
		t.Fatal("more than one terminal transition committed", won, missed)
	}
	after := lifecycleMetrics(t, f)
	assertLifecycleMetricDelta(t, claimed, after, "inventory_scan", domain.JobPriorityManual, lifecycleMetricDelta{failed: 1, duration: 1})
	second, err := other.JobMetrics(f.ctx)
	if err != nil || second.Groups != after.Groups || !second.StartedAt.Equal(after.StartedAt) {
		t.Fatal("stores do not share committed metrics", err)
	}
}

func TestJobMetricsTransactionAndSavepointRollback(t *testing.T) {
	for _, savepoint := range []bool{false, true} {
		t.Run(fmt.Sprint(savepoint), func(t *testing.T) {
			f := newJobFixture(t)
			job := f.submit(t, "metrics-rollback")
			before := lifecycleMetrics(t, f)
			outer, err := f.s.Pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer outer.Rollback(context.Background())
			tx := outer
			if savepoint {
				tx, err = outer.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = tx.Exec(f.ctx, `UPDATE jobs SET state='running',owner='metrics-rollback',generation=generation+1,attempts=attempts+1,lease_until=clock_timestamp()+interval '1 minute',started_at=clock_timestamp() WHERE id=$1::uuid`, job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(f.ctx, `UPDATE jobs SET state='failed',owner=NULL,lease_until=NULL,finished_at=clock_timestamp(),error_code='scan_io' WHERE id=$1::uuid`, job.ID); err != nil {
				t.Fatal(err)
			}
			var wait, duration, failures int64
			if err = tx.QueryRow(f.ctx, `SELECT wait_count,duration_count,failed_total FROM job_metric_totals WHERE kind='inventory_scan' AND priority='manual'`).Scan(&wait, &duration, &failures); err != nil || wait != 1 || duration != 1 || failures != 1 {
				t.Fatal("rollback fixture did not produce provisional metrics", wait, duration, failures, err)
			}
			if err = tx.Rollback(f.ctx); err != nil {
				t.Fatal(err)
			}
			if savepoint {
				if err = outer.Commit(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			assertLifecycleMetricsUnchanged(t, before, lifecycleMetrics(t, f))
			if f.get(t, job.ID).State != domain.JobQueued {
				t.Fatal("rolled back transition changed the job")
			}
		})
	}
}

// Publication fixtures retain each mode's real coverage, comparison, seal, and
// catalog-entry prerequisites. Only the later fault injection is synthetic.
func lifecyclePublication(t *testing.T, mode string) (jobFixture, domain.JobLease, func() error) {
	t.Helper()
	switch mode {
	case "custom":
		f, lease, _ := baselineComparisonFixture(t, 2, 1)
		classifyForPublication(t, f, lease, func(*domain.IgnoreBaselineDecision) {}, true)
		return f, lease, func() error { return f.s.FinishIgnoreJob(f.ctx, lease) }
	case "family", "family-review":
		f, lease := familyComparisonFixture(t)
		if mode == "family-review" {
			classifyFamilyForPublication(t, f, lease, func(e *domain.FamilyBaselineEvaluation) {
				*e = domain.FamilyBaselineEvaluation{Decision: domain.FamilyIgnoreBaselineDecision{RootID: e.Decision.RootID, Path: e.Decision.Path, Outcome: domain.IgnoreBaselineUnknown, Reason: domain.IgnoreUnknownSource}}
			}, false)
		} else {
			classifyFamilyForPublication(t, f, lease, familyExcluded, true)
		}
		return f, lease, func() error { return f.s.FinishFamilyIgnoreJob(f.ctx, lease) }
	case "catalog":
		f, source, items := catalogImportFixture(t, 1)
		if _, _, err := f.s.SubmitCatalogImport(f.ctx, f.a, source.ID, "metrics-catalog", domain.JobPriorityManual, items, f.policy); err != nil {
			t.Fatal(err)
		}
		lease, err := f.s.ClaimJobWithCapabilities(f.ctx, "metrics-catalog-owner", false, time.Minute, domain.ScanCapabilities{CatalogImport: true})
		if err != nil {
			t.Fatal(err)
		}
		task, err := f.s.NextCatalogImport(f.ctx, lease)
		if err != nil {
			t.Fatal(err)
		}
		if err = scan.New().VerifyInventoryImport(f.ctx, task.Source); err != nil {
			t.Fatal(err)
		}
		if err = f.s.CommitCatalogImport(f.ctx, lease, task); err != nil {
			t.Fatal(err)
		}
		return f, lease, func() error { return f.s.FinishCatalogImport(f.ctx, lease, domain.JobSucceeded, "") }
	default:
		f := newJobFixture(t)
		f.submit(t, "metrics-inventory")
		lease := f.claim(t, "metrics-inventory-owner")
		directory := f.directory(t, lease)
		if err := f.s.SaveScanBatch(f.ctx, lease, directory, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(directory, "kept.mkv", 7)}, Done: true}); err != nil {
			t.Fatal(err)
		}
		return f, lease, func() error { return f.s.FinishJob(f.ctx, lease, domain.JobSucceeded, "") }
	}
}

func TestJobMetricsAllSuccessfulPublicationPaths(t *testing.T) {
	for _, mode := range []string{"inventory", "custom", "family", "family-review", "catalog"} {
		t.Run(mode, func(t *testing.T) {
			f, lease, finish := lifecyclePublication(t, mode)
			before := lifecycleMetrics(t, f)
			if err := finish(); err != nil {
				t.Fatal("publish", err)
			}
			kind := "inventory_scan"
			if mode == "catalog" {
				kind = domain.JobCatalogImport
			}
			assertLifecycleMetricDelta(t, before, lifecycleMetrics(t, f), kind, domain.JobPriorityManual, lifecycleMetricDelta{succeeded: 1, duration: 1})
			job := f.get(t, lease.Job.ID)
			if job.State != domain.JobSucceeded || job.ReviewRequired != (mode == "family-review") {
				t.Fatal("success fixture did not publish expected state", job.State, job.ReviewRequired)
			}
		})
	}
}

func TestJobMetricsLateFinishFailuresRollbackIncrements(t *testing.T) {
	for _, mode := range []string{"inventory", "custom", "family", "catalog"} {
		for _, failure := range []string{"audit", "guard", "seal"} {
			if mode == "catalog" && failure != "audit" || mode == "inventory" && failure == "seal" {
				continue // Only ignore publication has a final seal guard.
			}
			t.Run(mode+"-"+failure, func(t *testing.T) {
				f, lease, finish := lifecyclePublication(t, mode)
				before := lifecycleMetrics(t, f)
				// Sequences survive rollback and prove an outcome increment was
				// reached, not merely that an early validation rejected the call.
				if _, err := f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE metrics_increment_entered;
CREATE FUNCTION mark_metrics_increment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval(format('%I.metrics_increment_entered',TG_TABLE_SCHEMA)::regclass); RETURN NEW; END $$;
CREATE TRIGGER mark_metrics_increment AFTER UPDATE ON job_metric_totals FOR EACH ROW WHEN(NEW.succeeded_total>OLD.succeeded_total) EXECUTE FUNCTION mark_metrics_increment()`); err != nil {
					t.Fatal(err)
				}
				var ddl string
				want := domain.ErrDatabase
				if failure == "audit" {
					ddl = `CREATE FUNCTION reject_metrics_finish_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture finish audit failure'; END $$; CREATE TRIGGER reject_metrics_finish_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_metrics_finish_audit()`
				} else if failure == "seal" {
					want = domain.ErrInventoryInvalidated
					ddl = `CREATE FUNCTION invalidate_metrics_seal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE job_ignore_verifications SET sealed_until=clock_timestamp()-interval '1 second' WHERE job_id=NEW.id; RETURN NEW; END $$; CREATE TRIGGER invalidate_metrics_seal AFTER UPDATE OF state ON jobs FOR EACH ROW WHEN(OLD.state='running' AND NEW.state='succeeded') EXECUTE FUNCTION invalidate_metrics_seal()`
				} else {
					want = domain.ErrInventoryInvalidated
					ddl = `CREATE FUNCTION invalidate_metrics_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE libraries SET inventory_generation=inventory_generation+1 WHERE id=NEW.library_id; RETURN NEW; END $$; CREATE TRIGGER invalidate_metrics_finish AFTER UPDATE OF state ON jobs FOR EACH ROW WHEN(OLD.state='running' AND NEW.state='succeeded') EXECUTE FUNCTION invalidate_metrics_finish()`
				}
				if _, err := f.s.Pool.Exec(f.ctx, ddl); err != nil {
					t.Fatal(err)
				}
				if err := finish(); !errors.Is(err, want) {
					t.Fatal("late publication failure was not rejected", failure, err)
				}
				var incremented bool
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM metrics_increment_entered`).Scan(&incremented); err != nil || !incremented {
					t.Fatal("failure occurred before the provisional metrics increment", incremented, err)
				}
				assertLifecycleMetricsUnchanged(t, before, lifecycleMetrics(t, f))
				if f.get(t, lease.Job.ID).State != domain.JobRunning {
					t.Fatal("failed finish retained a terminal state")
				}
			})
		}
	}
}
