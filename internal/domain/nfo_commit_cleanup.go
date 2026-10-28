package domain

import "time"

// NFOWriteCommitCleanupLease is one cleaner's bounded hold on a resolved
// token. An expired lease passes to the next owner with the next epoch. It
// grants removal of the token's own retained objects only.
type NFOWriteCommitCleanupLease struct {
	Token     string    `json:"-"`
	Owner     string    `json:"-"`
	Epoch     int64     `json:"-"`
	ExpiresAt time.Time `json:"-"`
}

// String redacts the lease.
func (NFOWriteCommitCleanupLease) String() string { return "nfo commit cleanup lease (redacted)" }

// GoString redacts the lease.
func (NFOWriteCommitCleanupLease) GoString() string { return "nfo commit cleanup lease (redacted)" }

// NFOWriteCommitCleanupOutcome is the recorded result of one token's cleanup.
// Every outcome releases the token's attempt reservation.
type NFOWriteCommitCleanupOutcome uint8

const (
	// NFOWriteCommitCleanupRemoved means no retained object of the token remains.
	NFOWriteCommitCleanupRemoved NFOWriteCommitCleanupOutcome = 1
	// NFOWriteCommitCleanupRetained means objects whose identity differed from
	// the durable record were kept and counted; they need a person.
	NFOWriteCommitCleanupRetained NFOWriteCommitCleanupOutcome = 2
	// NFOWriteCommitCleanupParentGone means the planned parent directory no
	// longer exists at its path or is another directory; nothing was removed.
	NFOWriteCommitCleanupParentGone NFOWriteCommitCleanupOutcome = 3
)

// NFOWriteCommitCleanupMaxRetained bounds the retained count of one token:
// five names and one evicted backup per attempt namespace, with headroom.
const NFOWriteCommitCleanupMaxRetained = 64

// Valid reports whether o is a recordable outcome.
func (o NFOWriteCommitCleanupOutcome) Valid() bool {
	return o >= NFOWriteCommitCleanupRemoved && o <= NFOWriteCommitCleanupParentGone
}

// NFOWriteCommitCleanupNamespace holds the identities recorded for one attempt
// namespace of a token. A zero identity was never recorded, so an object found
// under that name cannot be proven to belong to the token.
type NFOWriteCommitCleanupNamespace struct {
	Attempt          uint8    `json:"-"`
	OutputIdentity   [48]byte `json:"-"`
	RollbackIdentity [48]byte `json:"-"`
}

// NFOWriteCommitCleanupInput is the durable state a cleaner checks every
// filesystem object against. It is read under the cleanup lease and grants no
// authority beyond the token's own names.
type NFOWriteCommitCleanupInput struct {
	Lease        NFOWriteCommitCleanupLease       `json:"-"`
	RootPath     string                           `json:"-"`
	RelativePath string                           `json:"-"`
	Plan         NFOWriteCommitFilePlan           `json:"-"`
	Namespaces   []NFOWriteCommitCleanupNamespace `json:"-"`
	// EvictedIdentities are the original targets of earlier plans for the same
	// parent and file name. Backup rotation links only such originals, so an
	// evicted backup is removed only when it is one of them.
	EvictedIdentities [][48]byte `json:"-"`
}

// String redacts the input.
func (NFOWriteCommitCleanupInput) String() string { return "nfo commit cleanup input (redacted)" }

// GoString redacts the input.
func (NFOWriteCommitCleanupInput) GoString() string { return "nfo commit cleanup input (redacted)" }
