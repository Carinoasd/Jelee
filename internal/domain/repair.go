package domain

import (
	"errors"
	"time"
)

// Self-healing repair actions (G50.4). Every action has a dry run that lists
// the affected objects and their number without writing anything, and an
// execution that applies the same plan, records a repair run and audits it.
// Rerunning an action after it succeeded affects nothing. docs/repair.md
// describes each action.

// ErrRepairUnavailable reports an action this process cannot run: the image
// store is not configured, or the action needs the running server (its
// image store or scan pipeline) and was asked of the command line.
var ErrRepairUnavailable = errors.New("repair action is unavailable here")

// Repair actions.
const (
	// RepairItems queues catalog synchronisation for baseline videos that
	// have no item (rebuild items).
	RepairItems = "items"
	// RepairImageVariants clears the live image variant generation so every
	// variant is rendered again on its next request.
	RepairImageVariants = "image-variants"
	// RepairCaches drops probe cache rows whose file changed since it was
	// probed, so the next probe rebuilds them.
	RepairCaches = "caches"
	// RepairStats recounts every daily statistics row from its sessions.
	RepairStats = "stats"
	// RepairOrphans removes derived records whose file is gone: probe cache
	// rows of deleted files and image variant index rows without a file.
	RepairOrphans = "orphans"
	// RepairNFO queues NFO revalidation for libraries with stale NFO
	// observations.
	RepairNFO = "nfo"
	// RepairCounts clears "last version" references that point at another
	// item's version, which make version counts wrong.
	RepairCounts = "counts"
)

// RepairActions lists the actions in documentation order.
func RepairActions() []string {
	return []string{RepairItems, RepairImageVariants, RepairCaches, RepairStats, RepairOrphans, RepairNFO, RepairCounts}
}

// ValidRepairAction reports whether name is a known action.
func ValidRepairAction(name string) bool {
	for _, a := range RepairActions() {
		if a == name {
			return true
		}
	}
	return false
}

// RepairRevertible reports whether an executed run can be reverted. Only the
// journaled reference and counter repairs can; caches, indexes and queued
// jobs are rebuilt rather than restored.
func RepairRevertible(action string) bool {
	return action == RepairStats || action == RepairCounts
}

// Repair target kinds: what one affected object is.
const (
	RepairTargetCatalogVideo   = "uncataloged_video"
	RepairTargetImageVariant   = "image_variant"
	RepairTargetProbeStale     = "probe_cache_stale"
	RepairTargetProbeOrphan    = "probe_cache_orphan"
	RepairTargetVariantOrphan  = "variant_index_orphan"
	RepairTargetDailyCounters  = "daily_counters"
	RepairTargetNFOObservation = "nfo_observation"
	RepairTargetUserData       = "user_data_reference"
	RepairTargetSession        = "session_reference"
)

// Origins and states of a repair run.
const (
	RepairOriginCLI = "cli"
	RepairOriginAPI = "api"

	RepairStatePlanned   = "planned"
	RepairStateCompleted = "completed"
	RepairStatePartial   = "partial"
	RepairStateFailed    = "failed"

	// RepairResultSchema names the result document; adding a field keeps
	// the name, removing or renaming one changes it.
	RepairResultSchema = "jelee-repair-result/v1"
	// RepairSamples bounds the sample objects of one result.
	RepairSamples = 20
	// RepairDefaultStatBudget bounds file probes of one orphan run.
	RepairDefaultStatBudget = 1000
	// RepairRunRetention bounds kept runs without a journal.
	RepairRunRetention = 100
)

// Reasons a result reports instead of, or next to, its counts.
const (
	// RepairReasonNoBaseline: the library was never scanned; actions that
	// compare with the baseline have nothing to compare.
	RepairReasonNoBaseline = "no_baseline"
	// RepairReasonUnconfirmed: some candidates could not be confirmed
	// within the probe budget and were left alone.
	RepairReasonUnconfirmed = "unconfirmed"
	// RepairReasonStoreDisabled: the image store is not configured.
	RepairReasonStoreDisabled = "image_store_unavailable"
	// RepairReasonFailed: a step failed; the counts show what was done.
	RepairReasonFailed = "repair_failed"
)

// RepairTargetCount counts one kind of affected object.
type RepairTargetCount struct {
	Kind string `json:"kind"`
	// Planned objects were found; Applied ones were repaired and Skipped
	// ones had changed since they were read, or were not confirmed.
	Planned int64 `json:"planned"`
	Applied int64 `json:"applied"`
	Skipped int64 `json:"skipped"`
}

// RepairSample names one affected object by identifiers. Path is rendered
// through the logging path mode and is never absolute.
type RepairSample struct {
	Kind      string `json:"kind"`
	LibraryID string `json:"libraryId,omitempty"`
	ItemID    string `json:"itemId,omitempty"`
	SourceID  string `json:"sourceId,omitempty"`
	UserID    string `json:"userId,omitempty"`
	Day       string `json:"day,omitempty"`
	Object    string `json:"object,omitempty"`
	Path      string `json:"path,omitempty"`
}

// RepairJob is a job an execution queued, or found already queued.
type RepairJob struct {
	LibraryID string `json:"libraryId"`
	JobID     string `json:"jobId"`
	// Replayed is true when the library already had the job active; the
	// run queued nothing new.
	Replayed bool `json:"replayed"`
}

// RepairResult is the stable result document of a dry run or an execution.
type RepairResult struct {
	Schema    string `json:"schema"`
	Action    string `json:"action"`
	RunID     string `json:"runId,omitempty"`
	Origin    string `json:"origin"`
	LibraryID string `json:"libraryId,omitempty"`
	DryRun    bool   `json:"dryRun"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	// Revertible tells whether "repair revert" can undo this run.
	Revertible bool                `json:"revertible"`
	Planned    int64               `json:"planned"`
	Applied    int64               `json:"applied"`
	Skipped    int64               `json:"skipped"`
	Targets    []RepairTargetCount `json:"targets"`
	Jobs       []RepairJob         `json:"jobs,omitempty"`
	// Info carries counts that are not affected objects, such as baseline
	// videos awaiting review or candidates the baseline knows are present.
	Info             map[string]int64 `json:"info,omitempty"`
	Samples          []RepairSample   `json:"samples"`
	SamplesTruncated bool             `json:"samplesTruncated"`
	StartedAt        time.Time        `json:"startedAt"`
	FinishedAt       time.Time        `json:"finishedAt"`
}

// RepairActiveJob is the active job of a library as a repair sees it: NFO
// is true for a scan that validates NFO files.
type RepairActiveJob struct {
	ID   string
	Kind string
	NFO  bool
}

// RepairRun is the persisted identity of an execution.
type RepairRun struct {
	ID        string
	Action    string
	LibraryID string
	Origin    string
	Actor     Actor
	StartedAt time.Time
}

// RepairRevertResult counts a revert.
type RepairRevertResult struct {
	RunID    string `json:"runId"`
	Reverted int64  `json:"reverted"`
	// Skipped entries no longer hold the repaired value, or were already
	// reverted; they are left as they are.
	Skipped int64 `json:"skipped"`
}
