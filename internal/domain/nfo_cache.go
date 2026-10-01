package domain

import (
	"errors"
	"time"
)

var (
	ErrNFODisabled          = errors.New("nfo_disabled")
	ErrNFOInvalidated       = errors.New("nfo_invalidated")
	ErrNFOCacheCapacity     = errors.New("nfo_cache_capacity")
	ErrNFOIdentityMismatch  = errors.New("nfo_identity_mismatch")
	ErrNFOSummaryLimit      = errors.New("nfo_summary_limit")
	ErrNFOSourceChanged     = errors.New("nfo_changed")
	ErrNFOInputUnavailable  = errors.New("nfo_input_unavailable")
	ErrNFOSourceLimit       = errors.New("nfo_too_large")
	ErrNFOReaderUnavailable = errors.New("nfo_reader_unavailable")
)

const (
	NFOModeOff               = "off"
	NFOModeReadOnly          = "read-only"
	NFOParserVersion         = "nfo-validation-v1"
	NFOSummarySchemaVersion  = 1
	NFOFingerprintVersion    = "sha256-full-v1"
	NFODefaultSourceBytes    = 8 << 20
	NFOMaxSourceBytes        = 32 << 20
	NFOSummaryMaxBytes       = 16 << 10
	NFORowAllowanceBytes     = 2048
	NFOIssuesMax             = 64
	NFOIssueTotalMax         = 1000000
	NFOEntriesMax            = 128
	NFOPageMax               = 32
	NFOBatchMax              = 16
	NFOSweepMax              = 128
	NFORequestGlobalMax      = 4096
	NFORequestActorMax       = 64
	NFORequestRetention      = 24 * time.Hour
	NFOPhaseWaiting          = "waiting"
	NFOPhaseRunning          = "running"
	NFOPhaseDone             = "done"
	NFOPhaseAborted          = "aborted"
	NFOStatusValid           = "valid"
	NFOStatusInvalid         = "invalid"
	NFOLookupHit             = "hit"
	NFOLookupNegativeHit     = "negative_hit"
	NFOLookupMiss            = "miss"
	NFOCompletionHit         = "hit"
	NFOCompletionNegativeHit = "negative_hit"
	NFOCompletionParsed      = "parsed"
	NFOCompletionChanged     = "changed"
	NFOCompletionUnavailable = "unavailable"
	NFOCompletionRejected    = "rejected"
)

// Cache limits are established once per database. They are separate from media
// probe quotas; another instance must match the stored policy exactly.
type NFOCachePolicy struct {
	MaxRows         int64
	MaxBytes        int64
	LibraryMaxRows  int64
	LibraryMaxBytes int64
	MaxLibraries    int
	PositiveTTL     time.Duration
	NegativeTTL     time.Duration
}

func DefaultNFOCachePolicy() NFOCachePolicy {
	return NFOCachePolicy{MaxRows: 100000, MaxBytes: 256 << 20, LibraryMaxRows: 50000,
		LibraryMaxBytes: 64 << 20, MaxLibraries: 1024, PositiveTTL: 30 * 24 * time.Hour, NegativeTTL: 15 * time.Minute}
}

// Generation belongs only to NFO policy/root observations, never probe state.
type NFOLibraryPolicy struct {
	LibraryID  string `json:"libraryId"`
	Mode       string `json:"mode"`
	Generation int64  `json:"generation"`
}

// Identity is trusted parser configuration, not executable authority or a
// database-selected parser. Changes to parsing/projection semantics bump version.
type NFOIdentity struct {
	ParserVersion        string
	SummarySchemaVersion int
	FingerprintVersion   string
	MaxSourceBytes       int64
}

func DefaultNFOIdentity() NFOIdentity {
	return NFOIdentity{NFOParserVersion, NFOSummarySchemaVersion, NFOFingerprintVersion, NFODefaultSourceBytes}
}

// A full hash identifies retained bytes, not an atomic filesystem snapshot.
type NFOStamp struct {
	Size               int64
	ModifiedUnixNano   int64
	SHA256             string
	FingerprintVersion string
}

type NFOFailureCode string

const (
	NFOFailureInvalidXML          NFOFailureCode = "nfo_invalid_xml"
	NFOFailureUnsafeXML           NFOFailureCode = "nfo_unsafe_xml"
	NFOFailureInvalidEncoding     NFOFailureCode = "nfo_invalid_encoding"
	NFOFailureUnsupportedEncoding NFOFailureCode = "nfo_unsupported_encoding"
	NFOFailureTooComplex          NFOFailureCode = "nfo_too_complex"
	NFOFailureTooLarge            NFOFailureCode = "nfo_too_large"
)

// Every string is a fixed whitelist value. There is no arbitrary message field.
type NFOIssue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Field    string `json:"field"`
	Entry    int    `json:"entry"`
}

// This contains validation results only. It cannot supply Catalog metadata or
// field-lock merging. Counts cover all issues; Issues is a bounded source-order
// prefix. A parser failure has unknown root/encoding, zero entries and no issues.
type NFOValidationSummary struct {
	SchemaVersion   int            `json:"schemaVersion"`
	Status          string         `json:"status"`
	Encoding        string         `json:"encoding"`
	EncodingGuessed bool           `json:"encodingGuessed"`
	Root            string         `json:"root"`
	Entries         int            `json:"entries"`
	FailureCode     NFOFailureCode `json:"failureCode"`
	WarningCount    int64          `json:"warningCount"`
	ErrorCount      int64          `json:"errorCount"`
	IssueCount      int64          `json:"issueCount"`
	IssuesTruncated bool           `json:"issuesTruncated"`
	Issues          []NFOIssue     `json:"issues"`
}

type NFOPageToken struct {
	Revision int64
	AfterID  string
}

// Processed = Hits + NegativeHits + Parsed + Changed + Unavailable + Rejected.
// Valid + Invalid = Hits + NegativeHits + Parsed. Warnings do not imply invalid.
type NFOProgress struct {
	Processed    int64
	Hits         int64
	NegativeHits int64
	Parsed       int64
	Valid        int64
	Invalid      int64
	WarningFiles int64
	Changed      int64
	Unavailable  int64
	Rejected     int64
}

type NFOPhaseError string

const (
	NFOPhaseDisabled         NFOPhaseError = "nfo_disabled"
	NFOPhaseInvalidated      NFOPhaseError = "nfo_invalidated"
	NFOPhaseCapacity         NFOPhaseError = "nfo_cache_capacity"
	NFOPhaseIdentityMismatch NFOPhaseError = "nfo_identity_mismatch"
	NFOPhaseUnavailable      NFOPhaseError = "nfo_unavailable"
	NFOPhaseCancelled        NFOPhaseError = "nfo_cancelled"
)

type NFOPhase struct {
	JobID             string
	LibraryID         string
	Mode              string
	State             string
	Identity          NFOIdentity
	IdentityDigest    string
	LibraryGeneration int64
	Token             NFOPageToken
	Progress          NFOProgress
	ErrorCode         NFOPhaseError
}

// Filesystem paths are private resolver data, never HTTP inputs or summaries.
type NFOSource struct {
	RootPath     string `json:"-"`
	RelativePath string `json:"-"`
}

func (NFOSource) String() string   { return "nfo source (path redacted)" }
func (NFOSource) GoString() string { return "nfo source (path redacted)" }

type NFOEntry struct {
	Inventory InventoryEntry `json:"-"`
	Source    NFOSource      `json:"-"`
}

func (NFOEntry) String() string   { return "nfo entry (path redacted)" }
func (NFOEntry) GoString() string { return "nfo entry (path redacted)" }

type NFOPage struct {
	Token   NFOPageToken
	Entries []NFOEntry
}

type NFOCandidate struct {
	InventoryID string
	Stamp       NFOStamp
}

// Lookup omits summary bodies. Commit rechecks the current key, status and TTL.
type NFOLookup struct {
	InventoryID string
	Kind        string
}

// A batch is a consecutive hit prefix, or one fresh parsed/changed/unavailable/
// rejected entry. Only parsed stores Summary. Changed/unavailable/rejected have
// a zero Stamp and cannot become negative cache entries; rejected is too-large.
type NFOCompletion struct {
	Candidate   NFOCandidate
	Kind        string
	Summary     *NFOValidationSummary
	FailureCode NFOFailureCode
}

type NFOSweepResult struct {
	Deleted         int64
	FreedBytes      int64
	DeletedRequests int64
}
