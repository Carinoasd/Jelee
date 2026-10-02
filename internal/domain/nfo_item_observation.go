package domain

import "time"

const (
	NFOItemObservedValid   = "valid"
	NFOItemObservedMissing = "missing"
	NFOItemObservedInvalid = "nfo_invalid"
)

// Internal observation data; selection paths must not be serialized into an
// item API or audit. Missing observations have neither file stamps nor fields.
type NFOItemObservationState struct {
	Status    string
	Selection NFOItemSelection
	Identity  NFOIdentity
	Stamp     NFOStamp
	ReadAt    time.Time
}

func (NFOItemObservationState) String() string   { return "nfo item observation state (data redacted)" }
func (NFOItemObservationState) GoString() string { return "nfo item observation state (data redacted)" }

func ValidNFOItemObservationState(scope NFOItemScope, v NFOItemObservationState) bool {
	if !ValidNFOItemScope(scope) || ValidateNFOIdentity(v.Identity) != nil || v.ReadAt.IsZero() || v.ReadAt.Year() < 1 || v.ReadAt.Year() > 9999 || !probeHex(v.Selection.CandidateDigest, 64) {
		return false
	}
	switch v.Status {
	case NFOItemObservedValid:
		return ValidNFOItemSelection(scope, v.Selection) && v.Identity == v.Selection.Fields.Identity && v.Stamp == v.Selection.Fields.Stamp && v.ReadAt.Equal(v.Selection.Fields.ReadAt)
	case NFOItemObservedMissing:
		return v.Selection.RelativePath == "" && v.Selection.CandidateDigest == NFOCandidateDigest([]string{}) && v.Stamp == (NFOStamp{}) && emptyNFOItemFields(v.Selection.Fields)
	case NFOItemObservedInvalid:
		return AllowedNFOItemPath(scope, v.Selection.RelativePath) && ValidateNFOStamp(v.Stamp) == nil && v.Stamp.Size <= v.Identity.MaxSourceBytes && emptyNFOItemFields(v.Selection.Fields)
	}
	return false
}

func emptyNFOItemFields(v NFOItemFields) bool {
	return v.Version == "" && v.Kind == "" && v.Identity == (NFOIdentity{}) && v.Stamp == (NFOStamp{}) && v.ReadAt.IsZero() && !v.LockData && len(v.Fields) == 0 && len(v.LockedFields) == 0
}
