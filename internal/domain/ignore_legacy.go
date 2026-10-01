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

// RestoreLegacyIgnoreObservation projects a merged ledger chain back to its
// retained query boundary. A source checked for another query stays shadowed
// here. The supplied chain must be complete, ordered and bounded; restoration
// neither fills missing evidence nor revalidates the live filesystem.
func RestoreLegacyIgnoreObservation(directory string, selected *string, proofs []LegacyIgnoreDirectoryProof) (LegacyIgnoreObservation, error) {
	if len(proofs) < 1 || len(proofs) > 129 {
		return LegacyIgnoreObservation{}, ErrInvalid
	}
	start := 0
	if selected != nil {
		start = -1
		for i, p := range proofs {
			if p.Directory == *selected {
				start = i
				break
			}
		}
		if start < 0 || !proofs[start].Checked || !proofs[start].RulePresent {
			return LegacyIgnoreObservation{}, ErrInvalid
		}
	}
	result := LegacyIgnoreObservation{Version: LegacyIgnoreProofVersion, Directory: directory, Proofs: make([]LegacyIgnoreDirectoryProof, len(proofs))}
	for i, p := range proofs {
		if !LegacyIgnoreProofsCompatible(p, p) {
			return LegacyIgnoreObservation{}, ErrInvalid
		}
		if i < start {
			p.Checked = false
			p.RulePresent = false
			p.RuleIdentity = [32]byte{}
			p.RuleSize = 0
			p.RuleModifiedNano = 0
			p.RuleSHA256 = [32]byte{}
		} else if !p.Checked || p.RulePresent != (selected != nil && i == start) {
			return LegacyIgnoreObservation{}, ErrInvalid
		}
		result.Proofs[i] = p
	}
	if err := ValidateLegacyIgnoreObservation(result); err != nil {
		return LegacyIgnoreObservation{}, err
	}
	return result, nil
}
