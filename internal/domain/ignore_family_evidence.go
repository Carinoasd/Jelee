package domain

import (
	"path"
	"slices"
	"strings"
)

// FamilyBaselineEvaluation binds a provisional decision to its observed sources.
// Validation establishes relationships, not the truth of filesystem evidence.
type FamilyBaselineEvaluation struct {
	Decision           FamilyIgnoreBaselineDecision      `json:"-"`
	CustomProofs       []IgnoreDirectoryProof            `json:"-"`
	LegacyObservations []LegacyIgnoreBaselineObservation `json:"-"`
}

func (FamilyBaselineEvaluation) String() string { return "family baseline evaluation (data redacted)" }
func (FamilyBaselineEvaluation) GoString() string {
	return "family baseline evaluation (data redacted)"
}

func ValidateFamilyBaselineEvaluation(e FamilyBaselineEvaluation) error {
	d := e.Decision
	if ValidateFamilyIgnoreBaselineDecision(d) != nil || len(e.CustomProofs) > 129 || len(e.LegacyObservations) > 128 {
		return ErrInvalid
	}
	if d.Outcome == IgnoreBaselineUnknown {
		// Unknown carries no accepted partial evidence.
		if len(e.CustomProofs) != 0 || len(e.LegacyObservations) != 0 {
			return ErrInvalid
		}
		return nil
	}
	if len(e.CustomProofs) == 0 {
		return ErrInvalid
	}
	target := d.Path
	if d.Outcome == IgnoreBaselineExcluded {
		target = d.MatchedPath
	}
	parent := path.Dir(target)
	custom := map[string]IgnoreDirectoryProof{}
	identities := map[string]IgnoreDirectoryProof{}
	addIdentity := func(p IgnoreDirectoryProof) bool {
		if old, ok := identities[p.Directory]; ok && (old.RootID != p.RootID || old.Identity != p.Identity || old.ParentIdentity != p.ParentIdentity || old.MissingDirectory != p.MissingDirectory) {
			return false
		}
		identities[p.Directory] = p
		return true
	}
	for i, p := range e.CustomProofs {
		if ValidateIgnoreDirectoryProof(p) != nil || p.RootID != d.RootID || p.Directory != "." && p.Directory != parent && !strings.HasPrefix(parent, p.Directory+"/") {
			return ErrInvalid
		}
		if i == 0 {
			if p.Directory != "." {
				return ErrInvalid
			}
		} else if !IgnoreProofParentMatches(p, e.CustomProofs[i-1]) {
			return ErrInvalid
		}
		custom[p.Directory] = p
		addIdentity(p)
	}
	tail := e.CustomProofs[len(e.CustomProofs)-1]
	if !tail.MissingDirectory && tail.Directory != parent {
		return ErrInvalid
	}
	queries := map[string]LegacyIgnoreBaselineObservation{}
	legacy := map[string]LegacyIgnoreDirectoryProof{}
	for _, o := range e.LegacyObservations {
		if ValidateLegacyIgnoreBaselineObservation(o) != nil || o.Source.Proofs[0].RootID != d.RootID {
			return ErrInvalid
		}
		// Only reachable ancestor lookups (or the final file's parent) belong
		// to this decision. A legacy excluded ancestor can query itself.
		lookup := o.LookupDirectory
		if lookup != "." && lookup != parent && !strings.HasPrefix(parent, lookup+"/") && !(d.Outcome == IgnoreBaselineExcluded && d.Family == IgnoreFamilyLegacy && target != d.Path && lookup == target) {
			return ErrInvalid
		}
		if old, ok := queries[lookup]; ok && (old.Version != o.Version || old.Source.Version != o.Source.Version || old.Source.Directory != o.Source.Directory || old.MissingDirectory != o.MissingDirectory || !slices.Equal(old.Source.Proofs, o.Source.Proofs)) {
			return ErrInvalid
		}
		queries[lookup] = o
		for _, p := range o.Source.Proofs {
			if !addIdentity(p.IgnoreDirectoryProof) {
				return ErrInvalid
			}
			if old, ok := legacy[p.Directory]; ok {
				if !LegacyIgnoreProofsCompatible(old, p) {
					return ErrInvalid
				}
				if old.Checked {
					continue
				}
			}
			legacy[p.Directory] = p
		}
		if p := o.MissingDirectory; p.MissingDirectory && !addIdentity(p) {
			return ErrInvalid
		}
	}
	if d.Outcome == IgnoreBaselineExcluded {
		if d.Family == IgnoreFamilyCustom {
			if p, ok := custom[d.RuleDirectory]; !ok || !p.RulePresent || p.MissingDirectory {
				return ErrInvalid
			}
		} else {
			lookup := parent
			if target != d.Path {
				lookup = target
			}
			o, ok := queries[lookup]
			if !ok {
				return ErrInvalid
			}
			found := false
			for _, p := range o.Source.Proofs {
				if p.Checked && p.RulePresent && p.Directory == d.RuleDirectory {
					found = true
				}
			}
			if !found {
				return ErrInvalid
			}
		}
	}
	return nil
}
