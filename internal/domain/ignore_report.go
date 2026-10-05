package domain

// IgnoreReport lists retained relative paths and rule provenance for a terminal
// job. Baseline decisions and current scan exclusions are separate observations.
type IgnoreReportEntry struct {
	Source        string `json:"source"`
	RootID        string `json:"rootId"`
	Path          string `json:"path"`
	Kind          string `json:"kind,omitempty"`
	Outcome       string `json:"outcome"`
	Family        string `json:"family,omitempty"`
	RuleDirectory string `json:"ruleDirectory,omitempty"`
	RuleLine      int    `json:"ruleLine,omitempty"`
	MatchedPath   string `json:"matchedPath,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

type IgnoreReport struct {
	JobID               string              `json:"jobId"`
	State               string              `json:"state"`
	Enabled             bool                `json:"enabled"`
	ReviewRequired      bool                `json:"reviewRequired"`
	Invalidated         bool                `json:"invalidated"`
	ExcludedFiles       int64               `json:"excludedFiles"`
	ExcludedDirectories int64               `json:"excludedDirectories"`
	Unknown             int64               `json:"unknown"`
	Entries             []IgnoreReportEntry `json:"entries"`
	NextCursor          string              `json:"nextCursor,omitempty"`
}

func ValidateIgnoreReportEntry(e IgnoreReportEntry) error {
	if e.Source == "scan" {
		if e.Outcome != IgnoreBaselineExcluded || (e.Kind != "directory" && e.Kind != "video" && e.Kind != "nfo" && e.Kind != "image" && e.Kind != "other") {
			return ErrInvalid
		}
	} else if e.Source != "baseline" || e.Kind != "" {
		return ErrInvalid
	}
	if e.Family != "" {
		if e.Source == "scan" && e.MatchedPath != e.Path {
			return ErrInvalid
		}
		// A legacy directory lookup may find its own .ignore. Baseline
		// candidates are files and deliberately reject this shape.
		if e.Source == "scan" && e.Kind == "directory" && e.Family == IgnoreFamilyLegacy && e.RuleDirectory == e.Path {
			if !ValidID(e.RootID) || !ValidNFOObservationPath(e.Path) {
				return ErrInvalid
			}
			switch e.Reason {
			case IgnoreReasonRule:
				if e.RuleLine >= 1 && e.RuleLine <= 4096 {
					return nil
				}
			case IgnoreReasonBlank, IgnoreReasonInvalid:
				if e.RuleLine == 0 {
					return nil
				}
			}
			return ErrInvalid
		}
		return ValidateFamilyIgnoreBaselineDecision(FamilyIgnoreBaselineDecision{RootID: e.RootID, Path: e.Path, Outcome: e.Outcome, Family: e.Family, Reason: e.Reason, RuleDirectory: e.RuleDirectory, RuleLine: e.RuleLine, MatchedPath: e.MatchedPath})
	}
	return ValidateIgnoreBaselineDecision(IgnoreBaselineDecision{RootID: e.RootID, Path: e.Path, Outcome: e.Outcome, RuleDirectory: e.RuleDirectory, RuleLine: e.RuleLine, MatchedPath: e.MatchedPath, Reason: e.Reason})
}
