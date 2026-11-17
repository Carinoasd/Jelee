package domain

import (
	"math"
	"testing"
	"time"
)

func progressRate(total int64, ema float64, samples int, since time.Duration) JobRate {
	at := time.Date(2026, 10, 30, 1, 2, 3, 0, time.FixedZone("x", 3600))
	return JobRate{Total: &total, TotalKnownAt: &at, EMA: &ema, Samples: samples, SinceSample: &since}
}

func runningJob(kind string, files, skipped, directories int64) Job {
	started := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	return Job{Kind: kind, State: JobRunning, StartedAt: &started, Files: files, Skipped: skipped, Directories: directories}
}

func TestJobProgressDoneUnitPerKind(t *testing.T) {
	if got := JobProgressDone(runningJob("inventory_scan", 900, 5, 12)); got != 12 {
		t.Fatal("scan done must count completed directories, not discovered files:", got)
	}
	if got := JobProgressDone(runningJob(JobNFOApply, 30, 4, 0)); got != 34 {
		t.Fatal("item kinds count files plus skipped:", got)
	}
	if got := JobProgressDone(runningJob(JobImageRefresh, math.MaxInt64, 1, 0)); got != math.MaxInt64 {
		t.Fatal("done must saturate, not overflow:", got)
	}
}

func TestProjectJobProgressThroughputETA(t *testing.T) {
	p := ProjectJobProgress(runningJob(JobNFOApply, 30, 10, 0), progressRate(100, 2, JobETAMinSamples, 3*time.Second))
	if p.ETABasis != JobETAThroughput || p.ETASeconds == nil || *p.ETASeconds != 30 || p.Done != 40 || *p.Total != 100 || *p.Percent != 40 {
		t.Fatalf("throughput estimate: %+v", p)
	}
	if p.TotalKnownAt == nil || p.TotalKnownAt.Location() != time.UTC {
		t.Fatal("total time must be published in UTC")
	}
	if !ValidJobProgress(p) {
		t.Fatal("projected progress must validate")
	}
	// A fraction of a second still rounds up to a whole second.
	p = ProjectJobProgress(runningJob(JobNFOApply, 99, 0, 0), progressRate(100, 50, 9, 0))
	if p.ETASeconds == nil || *p.ETASeconds != 1 {
		t.Fatalf("sub-second remainder: %+v", p)
	}
	// One decimal, never rounded up to a finished-looking 100.
	p = ProjectJobProgress(runningJob(JobNFOApply, 9999, 0, 0), progressRate(10000, 50, 9, 0))
	if *p.Percent != 99.9 {
		t.Fatal("percent must floor to one decimal:", *p.Percent)
	}
}

func TestProjectJobProgressWithholdsETA(t *testing.T) {
	queued := Job{Kind: JobNFOApply, State: JobQueued}
	started := time.Now()
	paused := Job{Kind: JobNFOApply, State: JobQueued, StartedAt: &started, Files: 40}
	cancelling := runningJob(JobNFOApply, 40, 0, 0)
	cancelling.CancelRequested = true
	finished := runningJob(JobNFOApply, 100, 0, 0)
	finished.State = JobSucceeded
	good := progressRate(100, 2, 5, time.Second)
	unknown := good
	unknown.Total, unknown.TotalKnownAt = nil, nil
	for _, tc := range []struct {
		name  string
		job   Job
		rate  JobRate
		basis string
	}{
		{"total unknown", runningJob("inventory_scan", 50, 0, 3), unknown, JobETATotalUnknown},
		{"no samples", runningJob(JobNFOApply, 40, 0, 0), JobRate{Total: good.Total, TotalKnownAt: good.TotalKnownAt}, JobETAInsufficientSamples},
		{"too few samples", runningJob(JobNFOApply, 40, 0, 0), progressRate(100, 2, JobETAMinSamples-1, time.Second), JobETAInsufficientSamples},
		{"zero throughput", runningJob(JobNFOApply, 40, 0, 0), progressRate(100, 0, 9, time.Second), JobETAInsufficientSamples},
		{"infinite throughput", runningJob(JobNFOApply, 40, 0, 0), progressRate(100, math.Inf(1), 9, time.Second), JobETAInsufficientSamples},
		{"not started", queued, good, JobETANotStarted},
		{"paused", paused, good, JobETAPaused},
		{"cancelling", cancelling, good, JobETACancelling},
		{"finished", finished, good, JobETAFinished},
		{"stalled", runningJob(JobNFOApply, 40, 0, 0), progressRate(100, 2, 5, JobETAStallAfter+time.Second), JobETAStalled},
		{"no mark", runningJob(JobNFOApply, 40, 0, 0), JobRate{Total: good.Total, TotalKnownAt: good.TotalKnownAt, EMA: good.EMA, Samples: 5}, JobETAStalled},
		{"total reached", runningJob(JobNFOApply, 90, 20, 0), good, JobETATotalReached},
		{"too slow", runningJob(JobNFOApply, 0, 0, 0), progressRate(100, 1e-6, 5, time.Second), JobETATooSlow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ProjectJobProgress(tc.job, tc.rate)
			if p.ETABasis != tc.basis || p.ETASeconds != nil || !ValidJobProgress(p) {
				t.Fatalf("got %+v, want basis %s without ETA", p, tc.basis)
			}
		})
	}
	// Without a total there is no percent either; a known total of zero is complete.
	if p := ProjectJobProgress(runningJob("inventory_scan", 50, 0, 3), unknown); p.Percent != nil || p.Total != nil {
		t.Fatalf("unknown total produced numbers: %+v", p)
	}
	if p := ProjectJobProgress(finished, progressRate(0, 0, 0, 0)); *p.Percent != 100 {
		t.Fatalf("empty work list: %+v", p)
	}
	if p := ProjectJobProgress(runningJob(JobNFOApply, 150, 0, 0), good); *p.Percent != 100 {
		t.Fatalf("percent above a stale estimate: %+v", p)
	}
}

func TestValidJobProgressRejectsInconsistentValues(t *testing.T) {
	total, eta, percent, negative, over := int64(10), int64(5), 50.0, int64(-1), 101.0
	zero := time.Time{}
	nan := math.NaN()
	tooLong := int64(JobETAMax.Seconds()) + 1
	for name, p := range map[string]JobProgress{
		"eta without total":   {Done: 1, ETASeconds: &eta, ETABasis: JobETAThroughput},
		"throughput no eta":   {Done: 1, Total: &total, Percent: &percent, ETABasis: JobETAThroughput},
		"eta on other basis":  {Done: 1, Total: &total, Percent: &percent, ETASeconds: &eta, ETABasis: JobETAPaused},
		"percent no total":    {Done: 1, Percent: &percent, ETABasis: JobETATotalUnknown},
		"total no percent":    {Done: 1, Total: &total, ETABasis: JobETAStalled},
		"negative total":      {Done: 1, Total: &negative, Percent: &percent, ETABasis: JobETAStalled},
		"percent over":        {Done: 1, Total: &total, Percent: &over, ETABasis: JobETAStalled},
		"percent nan":         {Done: 1, Total: &total, Percent: &nan, ETABasis: JobETAStalled},
		"negative done":       {Done: -1, ETABasis: JobETATotalUnknown},
		"unknown basis":       {Done: 1, ETABasis: "guess"},
		"zero known time":     {Done: 1, Total: &total, TotalKnownAt: &zero, Percent: &percent, ETABasis: JobETAStalled},
		"known time no total": {Done: 1, TotalKnownAt: &zero, ETABasis: JobETATotalUnknown},
		"eta beyond bound":    {Done: 1, Total: &total, Percent: &percent, ETASeconds: &tooLong, ETABasis: JobETAThroughput},
	} {
		if ValidJobProgress(p) {
			t.Error(name, "accepted")
		}
	}
}
