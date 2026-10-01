package domain

// IgnoreReport lists retained relative paths and rule provenance for a terminal
// job. Baseline decisions and current scan exclusions are separate observations.
type IgnoreReportEntry struct {
	Source        string `json:"source"`
	RootID        string `json:"rootId"`
	Path          string `json:"path"`
	Kind          string `json:"kind,omitempty"`
	Outcome       string `json:"outcome"`
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
	return ValidateIgnoreBaselineDecision(IgnoreBaselineDecision{RootID: e.RootID, Path: e.Path, Outcome: e.Outcome, RuleDirectory: e.RuleDirectory, RuleLine: e.RuleLine, MatchedPath: e.MatchedPath, Reason: e.Reason})
}
