package domain

import (
	"errors"
	"time"
)

var (
	ErrProbeBusy             = errors.New("probe_busy")
	ErrProbeCacheCapacity    = errors.New("probe_cache_capacity")
	ErrProbeLeaseLost        = errors.New("probe_lease_lost")
	ErrProbeInvalidated      = errors.New("probe_invalidated")
	ErrProbeIdentityMismatch = errors.New("probe_identity_mismatch")
)

const (
	ProbeFingerprintVersion    = "edge-sha256-v1"
	ProbeParserVersion         = "media-metadata-v2"
	ProbeMetadataSchemaVersion = 1
	ProbeMetadataMaxBytes      = 128 << 10
	ProbeRowAllowanceBytes     = 2048
	ProbePageMax               = 32
	ProbeBatchMax              = 16
	ProbeSweepMax              = 128
	ProbeScopeIncremental      = "incremental"
	ProbeScopeLibraryRebuild   = "library_rebuild"
	ProbeScopeItemRebuild      = "item_rebuild"
	ProbePhaseRunning          = "running"
	ProbePhaseDone             = "done"
	ProbePhaseAborted          = "aborted"
	ProbeLookupHit             = "hit"
	ProbeLookupNegativeHit     = "negative_hit"
	ProbeLookupMiss            = "miss"
	ProbeLookupBusy            = "busy"
	ProbeCompletionHit         = "hit"
	ProbeCompletionNegativeHit = "negative_hit"
	ProbeCompletionSucceeded   = "succeeded"
	ProbeCompletionFailed      = "failed"
	ProbeCompletionChanged     = "changed"
	ProbeCompletionUnavailable = "unavailable"
)

// ProbeCachePolicy is established once per database, not supplied on each
// acquire. A second instance must match every field of the stored policy.
type ProbeCachePolicy struct {
	MaxRows         int64
	MaxBytes        int64
	LibraryMaxRows  int64
	LibraryMaxBytes int64
	MaxLibraries    int
	MaxToolVersions int
	MaxLeases       int
	PositiveTTL     time.Duration
	NegativeTTL     time.Duration
	LeaseDuration   time.Duration
}

func DefaultProbeCachePolicy() ProbeCachePolicy {
	return ProbeCachePolicy{MaxRows: 100000, MaxBytes: 1 << 30, LibraryMaxRows: 50000, LibraryMaxBytes: 256 << 20, MaxLibraries: 1024, MaxToolVersions: 16, MaxLeases: 2, PositiveTTL: 30 * 24 * time.Hour, NegativeTTL: 15 * time.Minute, LeaseDuration: 30 * time.Second}
}

// ProbeIdentity is a record of trusted registration, never authority to execute
// a database-selected binary. Digests are canonical lowercase SHA256 hex.
type ProbeIdentity struct {
	Platform              string
	VendorVersion         string
	UpstreamVersion       string
	SourceRevision        string
	ExecutableSHA256      string
	RuntimeSHA256         string
	ParserVersion         string
	MetadataSchemaVersion int
	ArgumentsSHA256       string
	SandboxVersion        string
	FingerprintVersion    string
}

type ProbeIdentityRef struct{ ID, Digest string }

// ProbeStamp describes a bounded observation, not an immutable content snapshot.
type ProbeStamp struct {
	Size               int64
	ModifiedUnixNano   int64
	Fingerprint        string
	FingerprintVersion string
}

// ProbeSource is private resolver data; no local path may be serialized.
type ProbeSource struct {
	RootPath     string `json:"-"`
	RelativePath string `json:"-"`
}

func (ProbeSource) String() string   { return "probe source (path redacted)" }
func (ProbeSource) GoString() string { return "probe source (path redacted)" }

type ProbePhaseStart struct {
	Identity     ProbeIdentityRef
	Scope        string
	TargetItemID string
}

type ProbePageToken struct {
	Revision int64
	AfterID  string
}

type ProbeProgress struct {
	Processed    int64
	Hits         int64
	NegativeHits int64
	Succeeded    int64
	Failed       int64
	Changed      int64
	Unavailable  int64
}

type ProbePhaseError string

const (
	ProbePhaseRuntimeUnavailable ProbePhaseError = "probe_runtime_unavailable"
	ProbePhaseCapacity           ProbePhaseError = "probe_cache_capacity"
	ProbePhaseInvalidated        ProbePhaseError = "probe_invalidated"
	ProbePhaseIdentityMismatch   ProbePhaseError = "probe_identity_mismatch"
)

type ProbePhase struct {
	JobID                string
	LibraryID            string
	State                string
	Start                ProbePhaseStart
	LibraryGeneration    int64
	TargetItemGeneration int64
	Token                ProbePageToken
	Progress             ProbeProgress
	ErrorCode            ProbePhaseError
}

type ProbeEntry struct {
	Inventory InventoryEntry
	Source    ProbeSource
}
type ProbePage struct {
	Token   ProbePageToken
	Entries []ProbeEntry
}
type ProbeCandidate struct {
	InventoryID string
	Stamp       ProbeStamp
}

// Lookup carries no metadata body: a worker only needs to know whether it can
// checkpoint a hit. Commit rechecks the full key and TTL under the same fence.
type ProbeLookup struct {
	InventoryID string
	Kind        string
}

// All fields are internal worker data. Generation comes from a NO CYCLE DB
// sequence so eviction/reinsertion cannot revive an old lease (ABA).
type ProbeLease struct {
	InventoryID       string
	RootID            string
	Path              string
	LibraryID         string
	ItemID            string
	LibraryGeneration int64
	RootGeneration    int64
	ItemGeneration    int64
	Identity          ProbeIdentityRef
	Stamp             ProbeStamp
	Owner             string
	Generation        int64
	JobID             string
	JobGeneration     int64
	ExpiresAt         time.Time
}

type ProbeFailureCode string

const (
	ProbeFailureMedia           ProbeFailureCode = "probe_failed"
	ProbeFailureMetadataInvalid ProbeFailureCode = "probe_metadata_invalid"
	ProbeFailureMetadataLimit   ProbeFailureCode = "probe_metadata_limit"
	ProbeFailureOutputLimit     ProbeFailureCode = "probe_output_limit"
	ProbeFailureTimeout         ProbeFailureCode = "probe_timeout"
)

// Completion is either a hit prefix (up to 16), or one terminal miss. Success
// requires Lease and Metadata; failed requires Lease and a media FailureCode.
// Runtime errors, cancellation and database failures are never media failures.
type ProbeCompletion struct {
	Candidate   ProbeCandidate
	Kind        string
	Lease       *ProbeLease
	Metadata    *MediaMetadata
	FailureCode ProbeFailureCode
}

type ProbeSweepResult struct {
	Deleted        int64
	ReleasedLeases int64
	FreedBytes     int64
}
