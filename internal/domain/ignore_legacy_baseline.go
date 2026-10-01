package domain

import "strings"

const LegacyIgnoreBaselineProofVersion = "legacy-baseline-source-v1"

// Source stops at the deepest existing directory. MissingDirectory, when set,
// proves the first absent child under that directory; descendants are not rows.
type LegacyIgnoreBaselineObservation struct {
	Version          string                  `json:"-"`
	LookupDirectory  string                  `json:"-"`
	Source           LegacyIgnoreObservation `json:"-"`
	MissingDirectory IgnoreDirectoryProof    `json:"-"`
}

func (LegacyIgnoreBaselineObservation) String() string {
	return "legacy baseline evidence (data redacted)"
}
func (LegacyIgnoreBaselineObservation) GoString() string {
	return "legacy baseline evidence (data redacted)"
}

func ValidateLegacyIgnoreBaselineObservation(o LegacyIgnoreBaselineObservation) error {
	if o.Version != LegacyIgnoreBaselineProofVersion || o.LookupDirectory != "." && !ValidNFOObservationPath(o.LookupDirectory) || ValidateLegacyIgnoreObservation(o.Source) != nil {
		return ErrInvalid
	}
	if o.MissingDirectory == (IgnoreDirectoryProof{}) {
		if o.LookupDirectory != o.Source.Directory {
			return ErrInvalid
		}
		return nil
	}
	p := o.MissingDirectory
	tail := o.Source.Proofs[len(o.Source.Proofs)-1].IgnoreDirectoryProof
	if !p.MissingDirectory || !IgnoreProofParentMatches(p, tail) || o.LookupDirectory != p.Directory && !strings.HasPrefix(o.LookupDirectory, p.Directory+"/") {
		return ErrInvalid
	}
	return nil
}
