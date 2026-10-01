package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestFamilyBaselineAncestorProvenance(t *testing.T) {
	base := FamilyIgnoreBaselineDecision{RootID: ignoreTestJobID, Path: "films/hidden/movie.mkv", Outcome: IgnoreBaselineExcluded, Family: IgnoreFamilyLegacy, Reason: IgnoreReasonBlank, RuleDirectory: "films/hidden", MatchedPath: "films/hidden"}
	for _, reason := range []string{IgnoreReasonBlank, IgnoreReasonInvalid, IgnoreReasonRule} {
		d := base
		d.Reason = reason
		if reason == IgnoreReasonRule {
			d.RuleLine = 4
		}
		if ValidateFamilyIgnoreBaselineDecision(d) != nil {
			t.Fatal("valid legacy ancestor rejected")
		}
		d.Family = IgnoreFamilyCustom
		d.Reason = IgnoreReasonRule
		d.RuleLine = 4
		if ValidateFamilyIgnoreBaselineDecision(d) != ErrInvalid {
			t.Fatal("custom self-directory source accepted")
		}
		d.RuleDirectory = "films"
		if ValidateFamilyIgnoreBaselineDecision(d) != nil {
			t.Fatal("custom parent source rejected")
		}
	}
	for name, mutate := range map[string]func(*FamilyIgnoreBaselineDecision){
		"file_source":             func(d *FamilyIgnoreBaselineDecision) { d.RuleDirectory = d.Path; d.MatchedPath = d.Path },
		"sibling":                 func(d *FamilyIgnoreBaselineDecision) { d.RuleDirectory = "films/hid" },
		"nonancestor":             func(d *FamilyIgnoreBaselineDecision) { d.MatchedPath = "films/other" },
		"blank_line":              func(d *FamilyIgnoreBaselineDecision) { d.RuleLine = 1 },
		"rule_without_line":       func(d *FamilyIgnoreBaselineDecision) { d.Reason = IgnoreReasonRule },
		"foreign_family":          func(d *FamilyIgnoreBaselineDecision) { d.Family = "unknown" },
		"raw_reason":              func(d *FamilyIgnoreBaselineDecision) { d.Reason = "private error" },
		"missing_with_provenance": func(d *FamilyIgnoreBaselineDecision) { d.Outcome = IgnoreBaselineMissing },
	} {
		t.Run(name, func(t *testing.T) {
			d := base
			mutate(&d)
			if ValidateFamilyIgnoreBaselineDecision(d) != ErrInvalid {
				t.Fatal("invalid decision accepted")
			}
		})
	}
	encoded, err := json.Marshal(base)
	if err != nil || string(encoded) != "{}" || strings.Contains(fmt.Sprintf("%v %#v", base, base), "films") {
		t.Fatal("private decision leaked")
	}
}

func TestFamilyBaselineUnknownAndMissing(t *testing.T) {
	d := FamilyIgnoreBaselineDecision{RootID: ignoreTestJobID, Path: "movie.mkv", Outcome: IgnoreBaselineMissing}
	if ValidateFamilyIgnoreBaselineDecision(d) != nil {
		t.Fatal("missing rejected")
	}
	d.Outcome = IgnoreBaselineUnknown
	for _, reason := range []string{IgnoreUnknownSource, IgnoreChangedSource, IgnoreUnknownCoverage} {
		d.Reason = reason
		if ValidateFamilyIgnoreBaselineDecision(d) != nil {
			t.Fatal("unknown rejected")
		}
	}
	d.Family = IgnoreFamilyLegacy
	if ValidateFamilyIgnoreBaselineDecision(d) != ErrInvalid {
		t.Fatal("unknown carried unproved family")
	}
}
