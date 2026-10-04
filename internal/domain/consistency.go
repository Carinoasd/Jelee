package domain

import "time"

// Data consistency checker (G50.3). A run compares the catalog, the last
// accepted inventory baseline and derived tables, and reports where they
// disagree. docs/consistency.md is the contract for the report document.

// JobConsistencyCheck runs the checker for one library under a job lease.
const JobConsistencyCheck = "consistency_check"

// ConsistencyReportSchema names the version of the report document. A
// change that removes or renames a member needs a new version.
const ConsistencyReportSchema = "jelee-consistency-report/v1"

// Check identifiers, in report order.
const (
	ConsistencyOrphanItem    = "orphan_item"
	ConsistencyOrphanFile    = "orphan_file"
	ConsistencyVersionCount  = "version_count"
	ConsistencyWatchStats    = "watch_stats_drift"
	ConsistencyImageFile     = "image_file"
	ConsistencyImageVariant  = "image_variant_index"
	ConsistencyNFOState      = "nfo_state"
	ConsistencySidecarFile   = "sidecar_file"
	ConsistencyProbeCache    = "probe_cache_stale"
	ConsistencyConstraints   = "constraint_state"
	consistencyCheckCount    = 10
	consistencyLibraryChecks = 8

	// ConsistencyLibraryCheckCount and ConsistencyGlobalCheckCount size one
	// run: the first runs once per library, the second once per run.
	ConsistencyLibraryCheckCount = consistencyLibraryChecks
	ConsistencyGlobalCheckCount  = consistencyCheckCount - consistencyLibraryChecks
)

// ConsistencyCheckSet is the fixed, ordered list of checks. Its length is a
// compile-time constant so metric series stay bounded.
type ConsistencyCheckSet = [consistencyCheckCount]string

// ConsistencyCheckCounts holds one count per check, indexed like
// ConsistencyChecks.
type ConsistencyCheckCounts = [consistencyCheckCount]int64

// ConsistencyChecks returns every check in report order.
func ConsistencyChecks() ConsistencyCheckSet {
	return ConsistencyCheckSet{ConsistencyOrphanItem, ConsistencyOrphanFile, ConsistencyVersionCount, ConsistencyWatchStats, ConsistencyImageFile, ConsistencyImageVariant, ConsistencyNFOState, ConsistencySidecarFile, ConsistencyProbeCache, ConsistencyConstraints}
}

// ConsistencyLibraryChecks are the checks run once per library, in order.
func ConsistencyLibraryChecks() [consistencyLibraryChecks]string {
	return [consistencyLibraryChecks]string{ConsistencyOrphanItem, ConsistencyOrphanFile, ConsistencyVersionCount, ConsistencyWatchStats, ConsistencyImageFile, ConsistencyNFOState, ConsistencySidecarFile, ConsistencyProbeCache}
}

// ConsistencyGlobalChecks are the database-wide checks, in order.
func ConsistencyGlobalChecks() [ConsistencyGlobalCheckCount]string {
	return [ConsistencyGlobalCheckCount]string{ConsistencyImageVariant, ConsistencyConstraints}
}

// ValidConsistencyCheck reports whether name is a known check.
func ValidConsistencyCheck(name string) bool {
	for _, c := range ConsistencyChecks() {
		if c == name {
			return true
		}
	}
	return false
}

// Check result statuses.
const (
	ConsistencyStatusOK         = "ok"
	ConsistencyStatusFindings   = "findings"
	ConsistencyStatusSkipped    = "skipped"
	ConsistencyStatusIncomplete = "incomplete"
)

// Reasons a check was skipped or is incomplete.
const (
	ConsistencyReasonNoBaseline      = "no_baseline"
	ConsistencyReasonBaselineChanged = "baseline_changed"
	ConsistencyReasonStoreDisabled   = "image_store_unavailable"
	ConsistencyReasonCancelled       = "cancelled"
	ConsistencyReasonUnverified      = "verification_budget_exhausted"
	ConsistencyReasonFailed          = "check_failed"
)

// Run origins, modes and states.
const (
	ConsistencyOriginCLI    = "cli"
	ConsistencyOriginJob    = "job"
	ConsistencyModeReport   = "report"
	ConsistencyModeFix      = "fix"
	ConsistencyRunRunning   = "running"
	ConsistencyRunCompleted = "completed"
	ConsistencyRunPartial   = "partial"
	ConsistencyRunCancelled = "cancelled"
	ConsistencyRunFailed    = "failed"
)

// Bounds. A run reads every page of a library, but keeps only a bounded
// sample of findings, stats a bounded number of files and samples a bounded
// number of statistics rows, so its memory does not grow with the library.
const (
	ConsistencyPageSize            = 500
	ConsistencySamplesPerCheck     = 50
	ConsistencyDefaultStatBudget   = 1000
	ConsistencyMaxStatBudget       = 100000
	ConsistencyDefaultWatchSample  = 500
	ConsistencyMaxWatchSample      = 10000
	ConsistencyDefaultVariantProbe = 1000
	ConsistencyRunRetention        = 50
)

// Finding codes. Codes are stable report values.
const (
	ConsistencySourceMissing          = "source_missing"
	ConsistencySourceUnconfirmed      = "source_missing_unconfirmed"
	ConsistencyVideoNotCataloged      = "video_not_cataloged"
	ConsistencyVideoWithoutSource     = "video_item_without_source"
	ConsistencyContainerWithSource    = "container_item_with_source"
	ConsistencyUserDataForeignSource  = "user_data_foreign_source"
	ConsistencySessionForeignSource   = "session_foreign_source"
	ConsistencyDailyCounterDrift      = "daily_counter_drift"
	ConsistencyDailyRowMissing        = "daily_row_missing"
	ConsistencyImageSourceMissing     = "image_source_missing"
	ConsistencyImageSourceUnconfirmed = "image_source_missing_unconfirmed"
	ConsistencyImageSourceChanged     = "image_source_changed"
	ConsistencyVariantFileMissing     = "variant_file_missing"
	ConsistencyNFOMissing             = "nfo_missing_but_observed"
	ConsistencyNFOAppeared            = "nfo_present_but_observed_missing"
	ConsistencyNFOChanged             = "nfo_changed_since_observed"
	ConsistencyNFOForeignSource       = "observation_foreign_source"
	ConsistencySidecarMissing         = "sidecar_missing"
	ConsistencySidecarUnconfirmed     = "sidecar_missing_unconfirmed"
	ConsistencySidecarChanged         = "sidecar_changed"
	ConsistencyProbeCacheChanged      = "probe_cache_stale"
	ConsistencyProbeCacheOrphan       = "probe_cache_orphan"
	ConsistencyConstraintNotValid     = "constraint_not_validated"
	ConsistencyIndexInvalid           = "index_invalid"
)

// Informational counters a check may add to its result.
const (
	ConsistencyInfoBaselineStale = "baselineStale"
	ConsistencyInfoUnconfirmed   = "unconfirmed"
	ConsistencyInfoPending       = "pendingReview"
	ConsistencyInfoUnverified    = "unverified"
	ConsistencyInfoSampled       = "sampled"
)

// ConsistencyConfirm tells the runner what a filesystem probe decides for a
// candidate.
type ConsistencyConfirm uint8

const (
	// ConsistencyConfirmNone means the database alone decided the finding.
	ConsistencyConfirmNone ConsistencyConfirm = iota
	// ConsistencyConfirmAbsent means the baseline lacks the file; a probe
	// that finds it clears the finding (the baseline is stale).
	ConsistencyConfirmAbsent
	// ConsistencyConfirmVariant means only a probe that finds the stored
	// image variant missing makes a finding.
	ConsistencyConfirmVariant
)

// ConsistencyFinding is one disagreement. Members are identifiers and
// root-relative paths only; Path is rendered through the logging path mode
// and is never absolute.
type ConsistencyFinding struct {
	Code      string `json:"code"`
	LibraryID string `json:"libraryId,omitempty"`
	ItemID    string `json:"itemId,omitempty"`
	SourceID  string `json:"sourceId,omitempty"`
	RootID    string `json:"rootId,omitempty"`
	Path      string `json:"path,omitempty"`
	UserID    string `json:"userId,omitempty"`
	Day       string `json:"day,omitempty"`
	// Object names a database object or record that is neither an item nor
	// a source: a constraint, an index, a session or an image record.
	Object   string           `json:"object,omitempty"`
	Fixable  bool             `json:"fixable"`
	Expected map[string]int64 `json:"expected,omitempty"`
	Actual   map[string]int64 `json:"actual,omitempty"`

	// RelativePath is the private root-relative path to probe and render.
	RelativePath string `json:"-"`
	// Confirm selects the filesystem probe for this candidate.
	Confirm ConsistencyConfirm `json:"-"`
}

// ConsistencyPage is one bounded page of a check. Candidates may still be
// cleared by a probe; Examined counts records read; Next is empty after the
// last page.
type ConsistencyPage struct {
	Candidates []ConsistencyFinding
	Examined   int64
	Info       map[string]int64
	Next       string
}

// ConsistencyRoot is a library root as the checker sees it. Path is private
// and must not be copied into a report.
type ConsistencyRoot struct {
	ID   string
	Path string
}

// ConsistencyLibrary is a library and its accepted baseline at the start of
// a run. Snapshot identifies the baseline rows that every page reads.
type ConsistencyLibrary struct {
	ID       string
	Roots    []ConsistencyRoot
	Baseline bool
	Snapshot int64
	Revision int64
	Entries  int64
}

// ConsistencyScope is what a page query needs besides its cursor.
type ConsistencyScope struct {
	Library ConsistencyLibrary
	// WatchSample bounds the statistics rows recomputed per direction.
	WatchSample int
	// SessionCutoff is the first statistics day whose sessions are all still
	// stored; zero when playback history is kept without limit.
	SessionCutoff time.Time
	// DailyCutoff is the first day whose daily rows are still stored; zero
	// when statistics are kept without limit.
	DailyCutoff time.Time
	// VariantSample bounds the image variant index rows probed.
	VariantSample int
}

// ConsistencyBaseline describes the baseline a library was checked against.
type ConsistencyBaseline struct {
	Available bool  `json:"available"`
	Revision  int64 `json:"revision"`
	Entries   int64 `json:"entries"`
}

// ConsistencyCheckResult is the outcome of one check for one library or the
// whole database.
type ConsistencyCheckResult struct {
	Check            string               `json:"check"`
	Status           string               `json:"status"`
	Reason           string               `json:"reason,omitempty"`
	Examined         int64                `json:"examined"`
	Findings         int64                `json:"findings"`
	Fixable          int64                `json:"fixable"`
	Fixed            int64                `json:"fixed"`
	Info             map[string]int64     `json:"info,omitempty"`
	Samples          []ConsistencyFinding `json:"samples"`
	SamplesTruncated bool                 `json:"samplesTruncated"`
}

// ConsistencyLibraryReport groups the checks of one library.
type ConsistencyLibraryReport struct {
	LibraryID string                   `json:"libraryId"`
	Baseline  ConsistencyBaseline      `json:"baseline"`
	Checks    []ConsistencyCheckResult `json:"checks"`
}

// ConsistencyTotals sums a run.
type ConsistencyTotals struct {
	Findings int64 `json:"findings"`
	Fixable  int64 `json:"fixable"`
	Fixed    int64 `json:"fixed"`
}

// ConsistencyLimits records the bounds a run used.
type ConsistencyLimits struct {
	StatBudget      int `json:"statBudget"`
	StatsUsed       int `json:"statsUsed"`
	WatchSample     int `json:"watchSample"`
	VariantSample   int `json:"variantSample"`
	SamplesPerCheck int `json:"samplesPerCheck"`
	PageSize        int `json:"pageSize"`
}

// ConsistencyReport is the stable report document.
type ConsistencyReport struct {
	Schema     string                     `json:"schema"`
	RunID      string                     `json:"runId"`
	Origin     string                     `json:"origin"`
	JobID      string                     `json:"jobId,omitempty"`
	LibraryID  string                     `json:"libraryId,omitempty"`
	Mode       string                     `json:"mode"`
	State      string                     `json:"state"`
	StartedAt  time.Time                  `json:"startedAt"`
	FinishedAt time.Time                  `json:"finishedAt"`
	Libraries  []ConsistencyLibraryReport `json:"libraries"`
	Global     []ConsistencyCheckResult   `json:"global"`
	Totals     ConsistencyTotals          `json:"totals"`
	Limits     ConsistencyLimits          `json:"limits"`
}

// ConsistencyRun is the persisted identity of a run.
type ConsistencyRun struct {
	ID        string
	JobID     string
	LibraryID string
	Origin    string
	Mode      string
	StartedAt time.Time
}

// ConsistencyFixResult counts one repair batch.
type ConsistencyFixResult struct {
	Applied int64
	// Skipped repairs found the row changed since it was read.
	Skipped int64
}

// ConsistencyRevertResult counts a revert.
type ConsistencyRevertResult struct {
	RunID    string `json:"runId"`
	Reverted int64  `json:"reverted"`
	// Skipped entries no longer hold the repaired value, or were already
	// reverted; they are left as they are.
	Skipped int64 `json:"skipped"`
}

// ConsistencyUnconfirmedCode is the code of a finding the baseline reports
// but no filesystem probe confirmed, because the run's probe budget ran out
// or the probe failed. Other codes are returned unchanged.
func ConsistencyUnconfirmedCode(code string) string {
	switch code {
	case ConsistencySourceMissing:
		return ConsistencySourceUnconfirmed
	case ConsistencyImageSourceMissing:
		return ConsistencyImageSourceUnconfirmed
	case ConsistencySidecarMissing:
		return ConsistencySidecarUnconfirmed
	}
	return code
}

// ConsistencyBaselineCheck reports whether a check compares against the
// inventory baseline, so a baseline replaced during the run voids it.
func ConsistencyBaselineCheck(check string) bool {
	switch check {
	case ConsistencyOrphanItem, ConsistencyOrphanFile, ConsistencyImageFile, ConsistencyNFOState, ConsistencySidecarFile, ConsistencyProbeCache:
		return true
	}
	return false
}
