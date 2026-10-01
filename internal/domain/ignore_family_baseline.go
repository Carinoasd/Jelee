package domain

import "strings"

// This is provisional classification of an unseen baseline file. Accepting a
// decision still requires the pending repository page, retained source evidence,
// complete scan coverage, and successful verification of both rule families.
type FamilyIgnoreBaselineDecision struct {
	RootID        string `json:"-"`
	Path          string `json:"-"`
	Outcome       string `json:"-"`
	Family        string `json:"-"`
	Reason        string `json:"-"`
	RuleDirectory string `json:"-"`
	RuleLine      int    `json:"-"`
	MatchedPath   string `json:"-"`
}

func (FamilyIgnoreBaselineDecision) String() string {
	return "family baseline decision (data redacted)"
}
func (FamilyIgnoreBaselineDecision) GoString() string {
	return "family baseline decision (data redacted)"
}

func ValidateFamilyIgnoreBaselineDecision(d FamilyIgnoreBaselineDecision) error {
	if !ValidID(d.RootID) || !ValidNFOObservationPath(d.Path) {
		return ErrInvalid
	}
	if d.Outcome != IgnoreBaselineExcluded {
		if d.Family != "" {
			return ErrInvalid
		}
		return ValidateIgnoreBaselineDecision(IgnoreBaselineDecision{RootID: d.RootID, Path: d.Path, Outcome: d.Outcome, Reason: d.Reason, RuleDirectory: d.RuleDirectory, RuleLine: d.RuleLine, MatchedPath: d.MatchedPath})
	}
	if !ValidNFOObservationPath(d.MatchedPath) || (d.RuleDirectory != "." && !ValidNFOObservationPath(d.RuleDirectory)) ||
		(d.Path != d.MatchedPath && !strings.HasPrefix(d.Path, d.MatchedPath+"/")) {
		return ErrInvalid
	}
	switch d.Family {
	case IgnoreFamilyCustom:
		if d.Reason != IgnoreReasonRule {
			return ErrInvalid
		}
		return ValidateIgnoreBaselineDecision(IgnoreBaselineDecision{RootID: d.RootID, Path: d.Path, Outcome: d.Outcome, RuleDirectory: d.RuleDirectory, RuleLine: d.RuleLine, MatchedPath: d.MatchedPath})
	case IgnoreFamilyLegacy:
		// A legacy lookup includes the matched directory itself. The final
		// candidate is a file, so its own path cannot be a source directory.
		if d.RuleDirectory != "." && !strings.HasPrefix(d.MatchedPath, d.RuleDirectory+"/") &&
			!(d.RuleDirectory == d.MatchedPath && d.MatchedPath != d.Path) {
			return ErrInvalid
		}
		switch d.Reason {
		case IgnoreReasonRule:
			if d.RuleLine < 1 || d.RuleLine > 4096 {
				return ErrInvalid
			}
		case IgnoreReasonBlank, IgnoreReasonInvalid:
			if d.RuleLine != 0 {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
