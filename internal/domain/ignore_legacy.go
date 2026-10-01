package domain

const LegacyIgnoreProofVersion = "legacy-nearest-source-v1"

// LegacyIgnoreDirectoryProof carries directory identity even when its fixed
// rule leaf was shadowed and not read. Checked=false never proves absence.
type LegacyIgnoreDirectoryProof struct {
	IgnoreDirectoryProof `json:"-"`
	Checked              bool `json:"-"`
}

func (LegacyIgnoreDirectoryProof) String() string   { return "legacy ignore proof (data redacted)" }
func (LegacyIgnoreDirectoryProof) GoString() string { return "legacy ignore proof (data redacted)" }

// LegacyIgnoreObservation is one complete nearest-source query. Queries must
// retain their boundary: flattening them into one unchecked/checked row per
// directory would confuse a shadowed ancestor with a verified absence.
// It does not enable a job mode or grant an execution capability.
type LegacyIgnoreObservation struct {
	Version   string                       `json:"-"`
	Directory string                       `json:"-"`
	Proofs    []LegacyIgnoreDirectoryProof `json:"-"`
}

func (LegacyIgnoreObservation) String() string   { return "legacy ignore observation (data redacted)" }
func (LegacyIgnoreObservation) GoString() string { return "legacy ignore observation (data redacted)" }

func ValidateLegacyIgnoreObservation(o LegacyIgnoreObservation) error {
	if o.Version != LegacyIgnoreProofVersion || len(o.Proofs) < 1 || len(o.Proofs) > 129 {
		return ErrInvalid
	}
	if o.Directory != o.Proofs[len(o.Proofs)-1].Directory {
		return ErrInvalid
	}
	checked, selected := false, false
	for i, p := range o.Proofs {
		if ValidateIgnoreDirectoryProof(p.IgnoreDirectoryProof) != nil || p.MissingDirectory {
			return ErrInvalid
		}
		if i == 0 {
			if p.Directory != "." {
				return ErrInvalid
			}
		} else if !IgnoreProofParentMatches(p.IgnoreDirectoryProof, o.Proofs[i-1].IgnoreDirectoryProof) {
			return ErrInvalid
		}
		if !p.Checked {
			if checked || p.RulePresent {
				return ErrInvalid
			}
			continue
		}
		if p.RulePresent {
			// The first checked source is the nearest existing one. Every later
			// (deeper) checked leaf must be absent; two selected sources are invalid.
			if checked || selected {
				return ErrInvalid
			}
			selected = true
		}
		checked = true
	}
	// No source means the search reached the root and checked every leaf.
	if !checked || !selected && !o.Proofs[0].Checked {
		return ErrInvalid
	}
	return nil
}

// LegacyIgnoreProofsCompatible checks repeated observations of one directory.
// An unchecked leaf may later be checked, but an observed absence or source
// may not change within the same snapshot. Directory identity always matches.
func LegacyIgnoreProofsCompatible(a, b LegacyIgnoreDirectoryProof) bool {
	if ValidateIgnoreDirectoryProof(a.IgnoreDirectoryProof) != nil || ValidateIgnoreDirectoryProof(b.IgnoreDirectoryProof) != nil || a.MissingDirectory || b.MissingDirectory || !a.Checked && a.RulePresent || !b.Checked && b.RulePresent {
		return false
	}
	if a.RootID != b.RootID || a.Directory != b.Directory || a.ParentIdentity != b.ParentIdentity || a.Identity != b.Identity {
		return false
	}
	if a.Checked && b.Checked {
		return a.IgnoreDirectoryProof == b.IgnoreDirectoryProof
	}
	return true
}
