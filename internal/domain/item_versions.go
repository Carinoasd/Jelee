package domain

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Manual version decisions (G20.3, G20.5). An administrator splits a
// version off into a new item, merges one item into another, chooses the
// main version or lifts an exclusion. Every decision is audited and kept as
// an operation that can be undone while its window lasts, newest first.

// Operation kinds.
const (
	VersionOpSplit     = "split"
	VersionOpMerge     = "merge"
	VersionOpPrimary   = "primary"
	VersionOpUnexclude = "unexclude"
)

// VersionUndoWindow is how long an operation stays undoable.
const VersionUndoWindow = 30 * 24 * time.Hour

// VersionOperationsPage bounds the operation history of one item.
const VersionOperationsPage = 50

// Undo blocking reasons.
const (
	VersionUndoBlockedUndone  = "undone"
	VersionUndoBlockedExpired = "expired"
	// VersionUndoBlockedLater: a later split or merge involves the same
	// items and must be undone first.
	VersionUndoBlockedLater = "later_operation"
)

var (
	// ErrVersionIdentityConflict refuses a merge of items whose external
	// IDs (NFO, provider) or episode numbers disagree (G20.5).
	ErrVersionIdentityConflict = errors.New("items have conflicting identities")
	// ErrVersionMergeIncompatible refuses a merge of items of another kind,
	// library or series, of a container item, of an item with children, or a
	// split of the only version.
	ErrVersionMergeIncompatible = errors.New("items cannot be combined")
	// ErrVersionUndoUnavailable refuses an undo that expired, was done
	// already or must wait for a later operation to be undone.
	ErrVersionUndoUnavailable = errors.New("version operation cannot be undone")
	// ErrVersionItemBusy refuses a change while a client plays the item or
	// a job works on it.
	ErrVersionItemBusy = errors.New("item is in use")
)

// VersionTitleMax bounds the title of an item created by a split.
const VersionTitleMax = 1024

// SplitVersionInput moves one version into a new item. Exclude records that
// the file is not a version of the original item, so synchronisation never
// groups it there again. Title names the new item; empty keeps the
// original's title.
type SplitVersionInput struct {
	SourceID string `json:"sourceId"`
	Exclude  bool   `json:"exclude"`
	Title    string `json:"title"`
}

// Valid checks the request shape.
func (in SplitVersionInput) Valid() bool {
	return ValidID(in.SourceID) && utf8.ValidString(in.Title) && len(in.Title) <= VersionTitleMax &&
		(in.Title == "" || strings.TrimSpace(in.Title) != "")
}

// VersionOperation is one recorded decision. Undoable and UndoBlocked are
// computed when it is read.
type VersionOperation struct {
	ID          string     `json:"id"`
	LibraryID   string     `json:"libraryId"`
	Kind        string     `json:"kind"`
	ItemID      string     `json:"itemId"`
	OtherItemID string     `json:"otherItemId,omitempty"`
	SourceIDs   []string   `json:"sourceIds"`
	ActorID     string     `json:"actorId,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UndoUntil   time.Time  `json:"undoUntil"`
	UndoneAt    *time.Time `json:"undoneAt,omitempty"`
	Undoable    bool       `json:"undoable"`
	UndoBlocked string     `json:"undoBlocked,omitempty"`
}

// VersionExclusion is a file that may not be grouped into the item again.
// Only the file name leaves storage.
type VersionExclusion struct {
	ID          string    `json:"id"`
	ItemID      string    `json:"itemId"`
	FileName    string    `json:"fileName"`
	OperationID string    `json:"operationId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// VersionOverview is the administrator view of one item's versions.
type VersionOverview struct {
	ItemID          string             `json:"itemId"`
	PrimarySourceID string             `json:"primarySourceId,omitempty"`
	Exclusions      []VersionExclusion `json:"exclusions"`
	Operations      []VersionOperation `json:"operations"`
}

// VersionMergeKind reports whether items of kind can be merged or split:
// only playable leaves hold versions.
func VersionMergeKind(kind string) bool { return ValidVideoItemKind(kind) }

// ExternalIDConflicts compares two uniqueIds documents ([{type,value}]) and
// returns the provider types both name with different values. Types compare
// case-insensitively, values after trimming; an unreadable document has no
// IDs.
func ExternalIDConflicts(a, b []byte) []string {
	left, right := externalIDs(a), externalIDs(b)
	var out []string
	for kind, values := range left {
		other, ok := right[kind]
		if !ok {
			continue
		}
		shared := false
		for v := range values {
			if other[v] {
				shared = true
			}
		}
		if !shared {
			out = append(out, kind)
		}
	}
	slices.Sort(out)
	return out
}

func externalIDs(doc []byte) map[string]map[string]bool {
	var entries []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	out := map[string]map[string]bool{}
	if len(doc) == 0 || json.Unmarshal(doc, &entries) != nil {
		return out
	}
	for _, e := range entries {
		kind, value := strings.ToLower(strings.TrimSpace(e.Type)), strings.TrimSpace(e.Value)
		if kind == "" || value == "" {
			continue
		}
		if out[kind] == nil {
			out[kind] = map[string]bool{}
		}
		out[kind][strings.ToLower(value)] = true
	}
	return out
}

// VersionUndoState decides whether an operation can be undone now. later
// reports a newer, not undone split or merge involving the same items.
func VersionUndoState(op VersionOperation, now time.Time, later bool) (bool, string) {
	switch {
	case op.UndoneAt != nil:
		return false, VersionUndoBlockedUndone
	case !now.Before(op.UndoUntil):
		return false, VersionUndoBlockedExpired
	case later && (op.Kind == VersionOpSplit || op.Kind == VersionOpMerge):
		return false, VersionUndoBlockedLater
	}
	return true, ""
}
