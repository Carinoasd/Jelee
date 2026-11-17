package postgres

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// progressPace keeps checkpoints apart by more than the trigger's 50 ms
// accumulation window, so each one is a throughput sample.
const progressPace = 80 * time.Millisecond

type progressRateRow struct {
	samples int
	ema     *float64
	mark    *time.Time
}

func readProgressRate(t *testing.T, f jobFixture, job string) progressRateRow {
	t.Helper()
	var r progressRateRow
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT rate_samples,rate_ema,rate_mark_at FROM jobs WHERE id=$1::uuid`, job).Scan(&r.samples, &r.ema, &r.mark); err != nil {
		t.Fatal(err)
	}
	return r
}

func progressOf(t *testing.T, f jobFixture, job string) domain.JobProgress {
	t.Helper()
	j := f.get(t, job)
	if j.Progress == nil || !domain.ValidJobProgress(*j.Progress) {
		t.Fatalf("job %s has no valid progress: %+v", job, j.Progress)
	}
	return *j.Progress
}

func TestJobProgressNFOApplyTotalAndETAFollowCheckpoints(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='read-only' WHERE id=$1::uuid`, library); err != nil {
		t.Fatal(err)
	}
	items := make([]string, 20)
	for i := range items {
		items[i] = metadataItem(t, f)
	}
	slices.Sort(items)
	job, _, err := f.s.SubmitNFOApplyJob(f.ctx, f.a, library, "progress-apply", domain.JobPriorityManual, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	if p := job.Progress; p == nil || p.Total == nil || *p.Total != 20 || p.TotalKnownAt == nil || *p.Percent != 0 || p.ETABasis != domain.JobETANotStarted {
		t.Fatalf("submission must count the library items: %+v", p)
	}
	lease, err := f.s.ClaimJobWithCapabilities(f.ctx, "progress-worker", false, time.Minute, domain.ScanCapabilities{NFOApply: true})
	if err != nil || lease.Job.ID != job.ID {
		t.Fatal("claim", err)
	}
	if p := progressOf(t, f, job.ID); p.ETABasis != domain.JobETAInsufficientSamples || p.ETASeconds != nil {
		t.Fatalf("a fresh claim has no throughput yet: %+v", p)
	}
	record := func(batch int) {
		t.Helper()
		time.Sleep(progressPace)
		if err := f.s.RecordNFOApplyBatch(f.ctx, lease, items[batch*2+1], domain.NFOApplyCounts{Applied: 1, Skipped: 1}); err != nil {
			t.Fatal("record", batch, err)
		}
	}
	for batch := range domain.JobETAMinSamples - 1 {
		record(batch)
		if p := progressOf(t, f, job.ID); p.ETASeconds != nil || p.ETABasis != domain.JobETAInsufficientSamples {
			t.Fatalf("batch %d gave an ETA before %d samples: %+v", batch, domain.JobETAMinSamples, p)
		}
	}
	record(domain.JobETAMinSamples - 1)
	p := progressOf(t, f, job.ID)
	done := int64(domain.JobETAMinSamples * 2)
	if p.ETABasis != domain.JobETAThroughput || p.ETASeconds == nil || p.Done != done || *p.Percent != float64(done)*100/20 {
		t.Fatalf("enough samples must give an ETA: %+v", p)
	}
	// About 25 items/s with 14 left; allow a loaded machine a wide margin.
	if *p.ETASeconds < 1 || *p.ETASeconds > 60 {
		t.Fatalf("ETA out of range for the observed pace: %d", *p.ETASeconds)
	}
	before := readProgressRate(t, f, job.ID)
	if before.samples != domain.JobETAMinSamples || before.ema == nil || before.mark == nil {
		t.Fatalf("stored rate: %+v", before)
	}

	// A pause withholds the ETA, and the queue time never becomes a sample.
	if err = f.s.PauseJob(f.ctx, lease); err != nil {
		t.Fatal(err)
	}
	if p = progressOf(t, f, job.ID); p.ETABasis != domain.JobETAPaused || p.ETASeconds != nil || *p.Total != 20 {
		t.Fatalf("paused job: %+v", p)
	}
	if r := readProgressRate(t, f, job.ID); r.mark != nil || r.samples != before.samples {
		t.Fatalf("pause kept the mark: %+v", r)
	}
	time.Sleep(2 * time.Second)
	if lease, err = f.s.ClaimJobWithCapabilities(f.ctx, "progress-worker", false, time.Minute, domain.ScanCapabilities{NFOApply: true}); err != nil || lease.Job.ID != job.ID {
		t.Fatal("reclaim", err)
	}
	record(domain.JobETAMinSamples)
	after := readProgressRate(t, f, job.ID)
	// Recover the new sample from the 0.3/0.7 average. Two seconds of queue
	// time would make it about 1 item/s; a pace-only sample is far above.
	sample := (*after.ema - 0.7**before.ema) / 0.3
	if after.samples != before.samples+1 || sample < 4 {
		t.Fatalf("pause leaked into throughput: before=%v after=%v sample=%v", *before.ema, *after.ema, sample)
	}

	// Cancellation and the terminal state withhold it as well.
	if _, err = f.s.CancelJob(f.ctx, f.a, job.ID); err != nil {
		t.Fatal(err)
	}
	if p = progressOf(t, f, job.ID); p.ETABasis != domain.JobETACancelling || p.ETASeconds != nil {
		t.Fatalf("cancelling job: %+v", p)
	}
	if err = f.s.FinishNFOApply(f.ctx, lease, domain.JobCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if p = progressOf(t, f, job.ID); p.ETABasis != domain.JobETAFinished || *p.Total != 20 {
		t.Fatalf("finished job: %+v", p)
	}
	if r := readProgressRate(t, f, job.ID); r.mark != nil {
		t.Fatal("a terminal job kept its mark")
	}
}

func TestJobProgressImageRefreshCountsCandidatesAndEstimates(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	for i := range 8 {
		itemImageUpsert(t, f, itemImageRemote(metadataItem(t, f), domain.ImageSourceRemote, fmt.Sprintf("https://art.example/p-%d.png", i)))
	}
	// A locked row is not a candidate and must not inflate the total.
	locked := itemImageRemote(metadataItem(t, f), domain.ImageSourceRemote, "https://art.example/locked.png")
	itemImageUpsert(t, f, locked)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE item_images SET locked=true WHERE remote_url='https://art.example/locked.png'`); err != nil {
		t.Fatal(err)
	}
	job, _, err := f.s.SubmitImageRefreshJob(f.ctx, f.a, library, "progress-refresh", domain.JobPriorityManual, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	if job.Progress == nil || job.Progress.Total == nil || *job.Progress.Total != 8 {
		t.Fatalf("refresh total must be the candidate count: %+v", job.Progress)
	}
	lease, err := f.s.ClaimJobWithCapabilities(f.ctx, "refresh-worker", false, time.Minute, domain.ScanCapabilities{ImageRefresh: true})
	if err != nil || lease.Job.ID != job.ID {
		t.Fatal("claim", err)
	}
	var p domain.JobProgress
	for round := range 4 {
		time.Sleep(progressPace)
		batch, err := f.s.NextImageRefreshBatch(f.ctx, lease, 2)
		if err != nil || len(batch) != 2 {
			t.Fatal("batch", round, len(batch), err)
		}
		results := make([]domain.ImageRefreshResult, len(batch))
		for i, c := range batch {
			results[i] = domain.ImageRefreshResult{Candidate: c, Deferred: true}
		}
		if err = f.s.RecordImageRefresh(f.ctx, lease, results); err != nil {
			t.Fatal("record", round, err)
		}
		p = progressOf(t, f, job.ID)
		if p.Done != int64(2*(round+1)) || *p.Percent != float64(25*(round+1)) {
			t.Fatalf("round %d progress: %+v", round, p)
		}
		if round == domain.JobETAMinSamples-1 {
			if p.ETABasis != domain.JobETAThroughput || p.ETASeconds == nil {
				t.Fatalf("round %d: expected an ETA: %+v", round, p)
			}
		} else if round < domain.JobETAMinSamples-1 && p.ETASeconds != nil {
			t.Fatalf("round %d: ETA before enough samples: %+v", round, p)
		}
	}
	if p.ETABasis != domain.JobETATotalReached || p.ETASeconds != nil {
		t.Fatalf("all candidates done must withhold a remaining ETA: %+v", p)
	}
	listed, err := f.s.ListJobs(f.ctx, f.a, "", 10, "")
	if err != nil || len(listed) != 1 || listed[0].Progress == nil || *listed[0].Progress.Total != 8 || listed[0].Progress.Done != 8 {
		t.Fatal("list must carry the same progress", err)
	}
}

func TestJobProgressScanTotalUnknownUntilEnumerationCompletes(t *testing.T) {
	f := newJobFixture(t)
	job := f.submit(t, "progress-scan")
	if p := job.Progress; p == nil || p.Total != nil || p.Percent != nil || p.ETABasis != domain.JobETANotStarted {
		t.Fatalf("a queued scan has no total: %+v", p)
	}
	l := f.claim(t, "scan-worker")
	root := f.directory(t, l)
	children := []string{"a", "b", "c", "d"}
	if err := f.s.SaveScanBatch(f.ctx, l, root, domain.ScanBatch{Directories: children, Done: true}); err != nil {
		t.Fatal(err)
	}
	for i := range children {
		time.Sleep(progressPace)
		d := f.directory(t, l)
		if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "v.mkv", 7)}, Done: true}); err != nil {
			t.Fatal(err)
		}
		p := progressOf(t, f, job.ID)
		if p.Done != int64(i+2) {
			t.Fatalf("scan done counts directories: %+v", p)
		}
		if i < len(children)-1 {
			// Enough samples, but the directory tree is still being discovered.
			if p.Total != nil || p.ETASeconds != nil || p.ETABasis != domain.JobETATotalUnknown {
				t.Fatalf("scan published a total before enumeration completed: %+v", p)
			}
		} else if p.Total == nil || *p.Total != 5 || *p.Percent != 100 || p.ETABasis != domain.JobETATotalReached || p.TotalKnownAt == nil {
			t.Fatalf("completed enumeration must fix the total: %+v", p)
		}
	}
	if r := readProgressRate(t, f, job.ID); r.samples < domain.JobETAMinSamples {
		t.Fatalf("scan checkpoints were not sampled: %+v", r)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if p := progressOf(t, f, job.ID); p.ETABasis != domain.JobETAFinished || p.Total == nil || *p.Total != 5 {
		t.Fatalf("finished scan: %+v", p)
	}
}

func TestJobProgressMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='read-only' WHERE id=$1::uuid`, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	metadataItem(t, f)
	job, _, err := f.s.SubmitNFOApplyJob(f.ctx, f.a, f.registration.Library.ID, "progress-migrate", domain.JobPriorityManual, f.policy)
	if err != nil || job.Progress == nil || job.Progress.Total == nil {
		t.Fatal("submit", err)
	}
	want := migrationVersion(t, "job_progress_eta")
	jobMetricMigration(t, f, "down", previousMigration(t, want))
	var columns int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='jobs' AND column_name IN ('progress_total','progress_total_at','rate_ema','rate_samples','rate_mark_at','rate_mark_done')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatal("downgrade kept progress columns", columns, err)
	}
	var trigger bool
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT to_regprocedure('jobs_progress_rate()') IS NOT NULL`).Scan(&trigger); err != nil || trigger {
		t.Fatal("downgrade kept the rate trigger", err)
	}
	// The retained job survives the downgrade; it simply has no estimate after.
	jobMetricMigration(t, f, "up", SchemaVersion)
	p := progressOf(t, f, job.ID)
	if p.Total != nil || p.ETABasis != domain.JobETANotStarted {
		t.Fatalf("upgraded job must report an unknown total: %+v", p)
	}
	for _, bad := range []string{
		`UPDATE jobs SET progress_total=1 WHERE id=$1::uuid`,
		`UPDATE jobs SET progress_total=-1,progress_total_at=now() WHERE id=$1::uuid`,
		`UPDATE jobs SET rate_samples=2 WHERE id=$1::uuid`,
		`UPDATE jobs SET rate_ema='Infinity',rate_samples=1 WHERE id=$1::uuid`,
	} {
		if _, err = f.s.Pool.Exec(f.ctx, bad, job.ID); itemImageSQLState(err) != "23514" {
			t.Fatalf("%s accepted: %v", bad, err)
		}
	}
}
