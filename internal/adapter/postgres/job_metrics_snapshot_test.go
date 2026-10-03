package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
)

func metricsSnapshot(t *testing.T, f jobFixture) app.JobMetricsSnapshot {
	t.Helper()
	s, err := f.s.JobMetrics(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJobMetricsIntegrationSnapshotIsReadOnlyAndBounded(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "snapshot-queued")
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET created_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := metricsSnapshot(t, f)
	g := snapshot.Groups[3]
	if snapshot.StartedAt.IsZero() || snapshot.ObservedAt.Before(snapshot.StartedAt) ||
		g.Kind != "inventory_scan" || g.Priority != "manual" || g.Queued != 1 || g.Running != 0 || g.ExpiredRunning != 0 ||
		g.OldestQueuedAgeSeconds < 60 || g.OldestQueuedAgeSeconds > 65 || g.Wait.Count != 0 {
		t.Fatalf("queued snapshot: %+v", snapshot)
	}
	f.claim(t, "snapshot-worker")
	tx, err := f.s.jobTransaction(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	// The uncommitted writer and advisory lock must not block observability.
	if _, err := tx.Exec(f.ctx, `UPDATE jobs SET files=files+1 WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, j.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, time.Second)
	observed, err := f.s.JobMetrics(ctx)
	cancel()
	if err != nil || observed.Groups[3].Running != 1 || observed.Groups[3].Queued != 0 || observed.Groups[3].Wait.Count != 1 {
		t.Fatalf("snapshot waited for the job writer or modified queue: %+v %v", observed, err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, j.ID).Scan(&after); err != nil || before != after {
		t.Fatal("metrics read changed committed job/lease", err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	expired := metricsSnapshot(t, f).Groups[3]
	if expired.Running != 0 || expired.ExpiredRunning != 1 || expired.Failed != 0 || expired.Queued != 0 || expired.Wait.Count != 1 {
		t.Fatal("scrape recovered an expired job", expired)
	}
	ctx, cancel = context.WithCancel(f.ctx)
	cancel()
	if got, err := f.s.JobMetrics(ctx); !errors.Is(err, context.Canceled) || got != (app.JobMetricsSnapshot{}) {
		t.Fatal("cancelled query returned data", err)
	}
	// Exhaust the pool to cover cancellation while acquiring, before SQL starts.
	first, err := f.s.Pool.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	for i := int32(1); i < f.s.Pool.Config().MaxConns; i++ {
		c, err := f.s.Pool.Acquire(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Release()
	}
	ctx, cancel = context.WithTimeout(f.ctx, 50*time.Millisecond)
	defer cancel()
	if got, err := f.s.JobMetrics(ctx); !errors.Is(err, context.DeadlineExceeded) || got != (app.JobMetricsSnapshot{}) {
		t.Fatal("pool acquisition deadline returned data", err)
	}
	started := time.Now()
	if got, err := f.s.JobMetrics(f.ctx); !errors.Is(err, context.DeadlineExceeded) || got != (app.JobMetricsSnapshot{}) || time.Since(started) > 4*time.Second {
		t.Fatal("query without a short caller deadline exceeded its acquisition budget", err)
	}
}

func TestJobMetricsIntegrationRejectsMissingOrInconsistentStorage(t *testing.T) {
	for _, query := range []string{
		`DELETE FROM job_metric_epoch`,
		`DELETE FROM job_metric_buckets WHERE kind='catalog_import' AND priority='manual'; DELETE FROM job_metric_totals WHERE kind='catalog_import' AND priority='manual'`,
		`DELETE FROM job_metric_buckets WHERE kind='inventory_scan' AND priority='manual' AND measure='wait' AND bucket_index=12`,
		`UPDATE job_metric_buckets SET bucket_count=1 WHERE kind='inventory_scan' AND priority='manual' AND measure='wait' AND bucket_index=0`,
	} {
		t.Run(fmt.Sprint(len(query))+query[:6], func(t *testing.T) {
			f := newJobFixture(t)
			if _, err := f.s.Pool.Exec(f.ctx, query); err != nil {
				t.Fatal(err)
			}
			got, err := f.s.JobMetrics(f.ctx)
			if err == nil || err.Error() != "job metrics unavailable" || got != (app.JobMetricsSnapshot{}) {
				t.Fatal("incomplete data accepted or SQL detail leaked", got, err)
			}
		})
	}
}

func TestJobMetricsIntegrationHistogramBoundaries(t *testing.T) {
	f := newJobFixture(t)
	waitBounds, durationBounds := app.JobWaitBoundsSeconds(), app.JobDurationBoundsSeconds()
	waitValues, durationValues := []int64{-1, 0, 86400000001}, []int64{-1, 0, 86400000001}
	for i := range waitBounds {
		w, d := int64(waitBounds[i]*1e6), int64(durationBounds[i]*1e6)
		waitValues = append(waitValues, w-1, w, w+1)
		durationValues = append(durationValues, d-1, d, d+1)
	}
	var expectedWait, expectedDuration app.JobMetricHistogram
	accumulate := func(h *app.JobMetricHistogram, value int64, bounds [12]float64) {
		if value < 0 {
			value = 0
		}
		h.Count++
		h.SumSeconds += float64(value) / 1e6
		index := len(bounds)
		for i, bound := range bounds {
			if value <= int64(bound*1e6) {
				index = i
				break
			}
		}
		h.BucketCounts[index]++
	}
	for i, w := range waitValues {
		d := durationValues[i]
		j := f.submit(t, fmt.Sprintf("bucket-%d", i))
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='running',owner='bucket-fixture',lease_until=clock_timestamp()+interval '1 minute',started_at=created_at+$2*interval '1 microsecond',attempts=1 WHERE id=$1`, j.ID, w); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='succeeded',owner=NULL,lease_until=NULL,finished_at=started_at+$2*interval '1 microsecond' WHERE id=$1`, j.ID, d); err != nil {
			t.Fatal(err)
		}
		accumulate(&expectedWait, w, waitBounds)
		accumulate(&expectedDuration, d, durationBounds)
	}
	g := metricsSnapshot(t, f).Groups[3]
	assertHistogram := func(name string, got, want app.JobMetricHistogram) {
		t.Helper()
		if got.Count != want.Count || got.BucketCounts != want.BucketCounts || math.Abs(got.SumSeconds-want.SumSeconds) > 1e-8 {
			t.Fatalf("%s: got %+v want %+v", name, got, want)
		}
	}
	assertHistogram("wait", g.Wait, expectedWait)
	assertHistogram("duration", g.Duration, expectedDuration)
	if g.Succeeded != int64(len(waitValues)) {
		t.Fatal("terminal samples disagree", g)
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM job_metric_epoch)+(SELECT count(*) FROM job_metric_totals)+(SELECT count(*) FROM job_metric_buckets)`).Scan(&count); err != nil || count != 163 {
		t.Fatal("metric storage grew with job history", count, err)
	}
	// Independently re-read; an observation must not add histogram samples.
	if again := metricsSnapshot(t, f).Groups; !reflect.DeepEqual(again, metricsSnapshot(t, f).Groups) {
		t.Fatal("repeated reads changed cumulative samples")
	}
}

func TestJobMetricsIntegrationActiveQueryPlan(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "history-source")
	if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
		t.Fatal(err)
	}
	// Retained terminal jobs model an older database. INSERT is deliberately not
	// a transition observation and must not fabricate historical metrics.
	_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO jobs SELECT (jsonb_populate_record(NULL::jobs,
	 to_jsonb(j)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key','history-'||n))).*
	 FROM jobs j CROSS JOIN generate_series(1,10000) n WHERE j.id=$1`, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `ANALYZE jobs`); err != nil {
		t.Fatal(err)
	}
	rows, err := f.s.Pool.Query(f.ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) "+jobMetricsSQL)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	if strings.Contains(plan, "Seq Scan on jobs") || strings.Contains(plan, "job_inventory") || strings.Contains(plan, "library_inventory_baseline") || !strings.Contains(plan, "jobs_active_library_idx") {
		t.Fatalf("metrics scanned retained history/inventory: %s", plan)
	}
	if got := metricsSnapshot(t, f).Groups[3]; got.Cancelled != 1 || got.Queued != 0 || got.Running != 0 {
		t.Fatal("retained rows altered durable counters", got)
	}
}

func TestJobMetricsIntegrationKindsAndPriorities(t *testing.T) {
	f := newJobFixture(t)
	for _, kind := range []string{"catalog_import", "inventory_scan"} {
		for _, priority := range []string{"background", "manual"} {
			j := f.submit(t, kind+priority)
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET kind=$2,priority=$3 WHERE id=$1`, j.ID, kind, priority); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, g := range metricsSnapshot(t, f).Groups {
		if g.Kind == "nfo_write" {
			if g.Cancelled != 0 || g.Duration.Count != 0 || g.Wait.Count != 0 {
				t.Fatal("legacy jobs changed write metrics", g)
			}
			continue
		}
		if g.Cancelled != 1 || g.Duration.Count != 0 || g.Wait.Count != 0 {
			t.Fatal("fixed dimension missed a group", g)
		}
	}
}
