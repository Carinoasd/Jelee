package domain

// NFORequest freezes intent and the trusted identity at enqueue. The optional
// error records a failure before phase execution. No field is a public DTO.
type NFORequest struct {
	JobID             string        `json:"-"`
	LibraryID         string        `json:"-"`
	Requested         bool          `json:"-"`
	Mode              string        `json:"-"`
	Identity          NFOIdentity   `json:"-"`
	IdentityDigest    string        `json:"-"`
	LibraryGeneration int64         `json:"-"`
	ErrorCode         NFOPhaseError `json:"-"`
}

func (NFORequest) String() string   { return "nfo request (data redacted)" }
func (NFORequest) GoString() string { return "nfo request (data redacted)" }

// A nil request with no read-only phase is legacy off. A retained read-only
// phase without its request is never silently converted to off by execution.
type NFOWork struct {
	Request *NFORequest `json:"-"`
	Phase   *NFOPhase   `json:"-"`
}

func (NFOWork) String() string   { return "nfo work (data redacted)" }
func (NFOWork) GoString() string { return "nfo work (data redacted)" }

const (
	NFOSummaryDisabled    = "disabled"
	NFOSummaryWaitingScan = "waiting_scan"
	NFOSummaryRunning     = "running"
	NFOSummaryDone        = "done"
	NFOSummaryAborted     = "aborted"
	NFOSummaryCancelled   = "cancelled"
)

// NFOJobSummary reports committed historical counts, never historical per-file
// issues. Mode also preserves B phases that predate explicit enqueue intent.
type NFOJobSummary struct {
	JobID        string `json:"jobId"`
	LibraryID    string `json:"libraryId"`
	Mode         string `json:"mode"`
	Phase        string `json:"phase"`
	Processed    int64  `json:"processed"`
	Hits         int64  `json:"hits"`
	NegativeHits int64  `json:"negativeHits"`
	Parsed       int64  `json:"parsed"`
	Valid        int64  `json:"valid"`
	Invalid      int64  `json:"invalid"`
	WarningFiles int64  `json:"warningFiles"`
	Changed      int64  `json:"changed"`
	Unavailable  int64  `json:"unavailable"`
	Rejected     int64  `json:"rejected"`
	ErrorCode    string `json:"errorCode,omitempty"`
}
