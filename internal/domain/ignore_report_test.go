package domain

import "testing"

func TestIgnoreReportFamilyProvenance(t *testing.T) {
	base := IgnoreReportEntry{Source: "scan", RootID: ignoreTestJobID, Path: "hidden", Kind: "directory", Outcome: IgnoreBaselineExcluded, Family: IgnoreFamilyLegacy, Reason: IgnoreReasonBlank, RuleDirectory: "hidden", MatchedPath: "hidden"}
	for _, reason := range []string{IgnoreReasonBlank, IgnoreReasonInvalid, IgnoreReasonRule} {
		e := base
		e.Reason = reason
		if reason == IgnoreReasonRule {
			e.RuleLine = 1
		}
		if err := ValidateIgnoreReportEntry(e); err != nil {
			t.Fatal("own-directory source rejected", err)
		}
	}
	for _, change := range []func(*IgnoreReportEntry){
		func(e *IgnoreReportEntry) { e.Source = "baseline"; e.Kind = "" },
		func(e *IgnoreReportEntry) { e.Kind = "video" },
		func(e *IgnoreReportEntry) { e.Family = "other" },
		func(e *IgnoreReportEntry) { e.RuleLine = 1 },
		func(e *IgnoreReportEntry) { e.MatchedPath = "other" },
		func(e *IgnoreReportEntry) { e.Path = "../hidden" },
		func(e *IgnoreReportEntry) { e.Outcome = IgnoreBaselineUnknown },
	} {
		e := base
		change(&e)
		if ValidateIgnoreReportEntry(e) == nil {
			t.Fatal("invalid provenance accepted")
		}
	}
	e := base
	e.Source = "baseline"
	e.Kind = ""
	e.Path = "hidden/movie.mkv"
	if ValidateIgnoreReportEntry(e) != nil {
		t.Fatal("ancestor exclusion rejected")
	}
	e = IgnoreReportEntry{Source: "baseline", RootID: ignoreTestJobID, Path: "movie.mkv", Outcome: IgnoreBaselineUnknown, Reason: IgnoreUnknownSource}
	if ValidateIgnoreReportEntry(e) != nil {
		t.Fatal("unknown rejected")
	}
	e.Family = IgnoreFamilyLegacy
	if ValidateIgnoreReportEntry(e) == nil {
		t.Fatal("unknown attributed to a rule family")
	}
}
