package domain

import (
	"math"
	"time"
)

// ETA bases. Only JobETAThroughput comes with etaSeconds; every other basis
// names why no estimate is given instead of a guessed number (G13.3).
const (
	JobETAThroughput          = "throughput"           // remaining work divided by the recent throughput
	JobETATotalUnknown        = "total_unknown"        // the kind has no total yet, or never has one
	JobETAInsufficientSamples = "insufficient_samples" // too few checkpoints to trust a throughput
	JobETANotStarted          = "not_started"          // queued and never claimed
	JobETAPaused              = "paused"               // started, then returned to the queue (pause, yield or recovery)
	JobETACancelling          = "cancelling"           // cancellation requested; the remaining work will not run
	JobETAStalled             = "stalled"              // no progress sample for JobETAStallAfter
	JobETATotalReached        = "total_reached"        // done reached the total; later phases or a grown work list are not estimated
	JobETATooSlow             = "too_slow"             // the estimate would exceed JobETAMax
	JobETAFinished            = "finished"             // the job is terminal
)

const (
	// JobETAMinSamples is the number of throughput samples an estimate needs.
	JobETAMinSamples = 3
	// JobETAStallAfter is the age of the last sample beyond which the
	// throughput no longer describes the job.
	JobETAStallAfter = 2 * time.Minute
	// JobETAMax bounds a published estimate; a slower job reports too_slow.
	JobETAMax = 30 * 24 * time.Hour
)

// JobProgress is the public progress view of a job. Done is in the kind's
// done unit (JobProgressDone). Total, Percent and ETASeconds are null while
// unknown; ETABasis always says which rule produced or withheld the ETA.
type JobProgress struct {
	Done         int64      `json:"done"`
	Total        *int64     `json:"total"`
	TotalKnownAt *time.Time `json:"totalKnownAt"`
	Percent      *float64   `json:"percent"`
	ETASeconds   *int64     `json:"etaSeconds"`
	ETABasis     string     `json:"etaBasis"`
}

// JobRate is the stored estimate input of a job, never public JSON as such.
// SinceSample is the age of the last progress mark measured by the database
// clock; nil when the job is not running or has no mark yet.
type JobRate struct {
	Total        *int64
	TotalKnownAt *time.Time
	EMA          *float64
	Samples      int
	SinceSample  *time.Duration
}

// JobProgressDone is the kind's done unit: completed directories for an
// inventory scan, whose files are discovered rather than worked through, and
// processed items (files plus skipped) for every other kind. The database
// trigger jobs_progress_rate samples the same unit.
func JobProgressDone(j Job) int64 {
	if j.Kind == "inventory_scan" {
		return j.Directories
	}
	if j.Files > math.MaxInt64-j.Skipped {
		return math.MaxInt64
	}
	return j.Files + j.Skipped
}

// ProjectJobProgress derives the public progress of j from its stored rate.
// It never invents a total or a throughput: an ETA needs a running job with a
// known total, enough fresh samples and positive throughput.
func ProjectJobProgress(j Job, r JobRate) JobProgress {
	p := JobProgress{Done: JobProgressDone(j), ETABasis: JobETAThroughput}
	if r.Total != nil && *r.Total >= 0 {
		total := *r.Total
		p.Total = &total
		if r.TotalKnownAt != nil {
			at := r.TotalKnownAt.UTC()
			p.TotalKnownAt = &at
		}
		percent := 100.0
		if total > 0 {
			percent = math.Min(100, math.Floor(float64(p.Done)/float64(total)*1000)/10)
		}
		p.Percent = &percent
	}
	switch {
	case j.State == JobSucceeded || j.State == JobFailed || j.State == JobCancelled:
		p.ETABasis = JobETAFinished
	case j.CancelRequested:
		p.ETABasis = JobETACancelling
	case j.State == JobQueued && j.StartedAt == nil:
		p.ETABasis = JobETANotStarted
	case j.State != JobRunning:
		p.ETABasis = JobETAPaused
	case p.Total == nil:
		p.ETABasis = JobETATotalUnknown
	case p.Done >= *p.Total:
		p.ETABasis = JobETATotalReached
	case r.Samples < JobETAMinSamples || r.EMA == nil || !(*r.EMA > 0) || math.IsInf(*r.EMA, 0):
		p.ETABasis = JobETAInsufficientSamples
	case r.SinceSample == nil || *r.SinceSample > JobETAStallAfter:
		p.ETABasis = JobETAStalled
	default:
		seconds := math.Ceil(float64(*p.Total-p.Done) / *r.EMA)
		if seconds > JobETAMax.Seconds() {
			p.ETABasis = JobETATooSlow
			break
		}
		eta := int64(math.Max(seconds, 1))
		p.ETASeconds = &eta
	}
	return p
}

// ValidJobProgress checks a decoded public progress object.
func ValidJobProgress(p JobProgress) bool {
	if p.Done < 0 || (p.Total == nil) != (p.Percent == nil) || p.Total != nil && *p.Total < 0 || p.TotalKnownAt != nil && (p.Total == nil || p.TotalKnownAt.IsZero()) {
		return false
	}
	if p.Percent != nil && (math.IsNaN(*p.Percent) || *p.Percent < 0 || *p.Percent > 100) {
		return false
	}
	switch p.ETABasis {
	case JobETAThroughput:
		return p.ETASeconds != nil && *p.ETASeconds >= 1 && *p.ETASeconds <= int64(JobETAMax.Seconds()) && p.Total != nil
	case JobETATotalUnknown, JobETAInsufficientSamples, JobETANotStarted, JobETAPaused, JobETACancelling, JobETAStalled, JobETATotalReached, JobETATooSlow, JobETAFinished:
		return p.ETASeconds == nil
	default:
		return false
	}
}
