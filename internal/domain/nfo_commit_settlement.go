package domain

import (
	"errors"
	"time"
)

// NFOWriteCommitSettlementPhase is the highest durable settlement phase of a
// token. Zero means no settlement record: the target was never renamed.
type NFOWriteCommitSettlementPhase uint8

const (
	// NFOWriteCommitBackedUp is saved before the target Rename may happen.
	NFOWriteCommitBackedUp NFOWriteCommitSettlementPhase = 1
	// NFOWriteCommitReplaced is terminal: the prepared output is the target.
	NFOWriteCommitReplaced NFOWriteCommitSettlementPhase = 2
	// NFOWriteCommitRolledBack is terminal: the target holds the prepared
	// original, either never replaced or restored from the rollback object.
	NFOWriteCommitRolledBack NFOWriteCommitSettlementPhase = 3
)

func (p NFOWriteCommitSettlementPhase) Valid() bool {
	return p >= NFOWriteCommitBackedUp && p <= NFOWriteCommitRolledBack
}

func (p NFOWriteCommitSettlementPhase) Terminal() bool {
	return p == NFOWriteCommitReplaced || p == NFOWriteCommitRolledBack
}

// NFOWriteCommitSettlement summarizes immutable settlement records. Times are
// the first durable observation of each phase and are never rewritten.
type NFOWriteCommitSettlement struct {
	Phase      NFOWriteCommitSettlementPhase `json:"-"`
	Attempt    uint8                         `json:"-"`
	BackedUpAt time.Time                     `json:"-"`
	SettledAt  time.Time                     `json:"-"`
	// ReadyRecorded names the selected ready namespace, legacy (attempt 0) or
	// one new attempt, read without the catalog fence so rollback stays possible.
	ReadyRecorded bool                     `json:"-"`
	ReadyAttempt  uint8                    `json:"-"`
	Ready         NFOWriteCommitFilesReady `json:"-"`
}

func (NFOWriteCommitSettlement) String() string   { return "nfo commit settlement (redacted)" }
func (NFOWriteCommitSettlement) GoString() string { return "nfo commit settlement (redacted)" }

// NFOWriteCommitEntryState is a bounded observation of one job entry for the
// worker. It grants no filesystem authority and carries no private paths.
type NFOWriteCommitEntryState struct {
	Sequence      int                           `json:"-"`
	Token         string                        `json:"-"`
	PlanRecorded  bool                          `json:"-"`
	ReadyRecorded bool                          `json:"-"`
	Settlement    NFOWriteCommitSettlementPhase `json:"-"`
}

func (NFOWriteCommitEntryState) String() string   { return "nfo commit entry (redacted)" }
func (NFOWriteCommitEntryState) GoString() string { return "nfo commit entry (redacted)" }

// ErrNFOWriteRejected marks an entry that cannot complete as prepared: the
// target or its scope changed, the document is unusable, or the bounded stage
// attempts are spent. The worker concludes it without replacement.
var ErrNFOWriteRejected = errors.New("nfo write entry rejected")
