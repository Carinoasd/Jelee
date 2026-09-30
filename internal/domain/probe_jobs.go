package domain

import "errors"

var (
	ErrProbeDisabled           = errors.New("probe_disabled")
	ErrProbeRuntimeUnavailable = errors.New("probe_runtime_unavailable")
	ErrProbeInputUnavailable   = errors.New("probe_input_unavailable")
	ErrProbeSourceChanged      = errors.New("probe_source_changed")
	ErrProbeFailed             = errors.New("probe_failed")
	ErrProbeMetadataInvalid    = errors.New("probe_metadata_invalid")
	ErrProbeMetadataLimit      = errors.New("probe_metadata_limit")
	ErrProbeOutputLimit        = errors.New("probe_output_limit")
)

// ProbeObservation is internal worker data from the trusted isolated adapter.
// An error must return its zero value; it is never a public job summary.
type ProbeObservation struct {
	Metadata       MediaMetadata
	Stamp          ProbeStamp
	IdentityDigest string
}

// Empty Scope means scan-only. Other scopes use the existing probe constants.
// TargetItemID is set only for item_rebuild; library ownership is checked in DB.
type ProbeIntent struct {
	Scope        string
	TargetItemID string
}

func ValidateProbeIntent(v ProbeIntent) error {
	switch v.Scope {
	case "", ProbeScopeIncremental, ProbeScopeLibraryRebuild:
		if v.TargetItemID != "" {
			return ErrInvalid
		}
	case ProbeScopeItemRebuild:
		if !ValidID(v.TargetItemID) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// ProbeRequest freezes the scope and trusted identity when a job is queued.
// ErrorCode also records a fixed runtime failure before a phase can begin.
type ProbeRequest struct {
	JobID                string
	LibraryID            string
	Intent               ProbeIntent
	Identity             ProbeIdentityRef
	LibraryGeneration    int64
	TargetItemGeneration int64
	ErrorCode            ProbePhaseError
}

// A nil Request means a scan-only job. A nil Phase means the opted-in job has
// not completed its inventory stage. Both are read under the live parent fence.
type ProbeWork struct {
	Request *ProbeRequest
	Phase   *ProbePhase
}

const (
	ProbeSummaryDisabled    = "disabled"
	ProbeSummaryWaitingScan = "waiting_scan"
	ProbeSummaryRunning     = "running"
	ProbeSummaryDone        = "done"
	ProbeSummaryAborted     = "aborted"
	ProbeSummaryCancelled   = "cancelled"
)

// ProbeJobSummary contains bounded public fields. ErrorCode is a fixed phase
// or parent-job code; no raw diagnostics, tool paths, leases or metadata belong
// here. Counts describe committed outcomes, not immutable media validity.
type ProbeJobSummary struct {
	JobID        string `json:"jobId"`
	LibraryID    string `json:"libraryId"`
	Enabled      bool   `json:"enabled"`
	Scope        string `json:"scope,omitempty"`
	TargetItemID string `json:"targetItemId,omitempty"`
	Phase        string `json:"phase"`
	Processed    int64  `json:"processed"`
	Hits         int64  `json:"hits"`
	NegativeHits int64  `json:"negativeHits"`
	Succeeded    int64  `json:"succeeded"`
	Failed       int64  `json:"failed"`
	Changed      int64  `json:"changed"`
	Unavailable  int64  `json:"unavailable"`
	ErrorCode    string `json:"errorCode,omitempty"`
}

// ProbeCapability is a public projection of local runtime health. State and
// Reason are fixed codes chosen by the runtime, never underlying error text.
type ProbeCapability struct {
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
}
