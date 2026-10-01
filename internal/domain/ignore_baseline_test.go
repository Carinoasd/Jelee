package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestIgnoreBaselineDecisionProvenance(t *testing.T) {
	base := IgnoreBaselineDecision{RootID: ignoreTestJobID, Path: "movies_%/blocked/file.mkv", Outcome: IgnoreBaselineExcluded, RuleDirectory: "movies_%", RuleLine: 2, MatchedPath: "movies_%/blocked"}
	if ValidateIgnoreBaselineDecision(base) != nil {
		t.Fatal("valid inherited exclusion rejected")
	}
	for name, change := range map[string]func(*IgnoreBaselineDecision){
		"sibling_prefix":      func(d *IgnoreBaselineDecision) { d.MatchedPath = "movies_%/block" },
		"different_root_path": func(d *IgnoreBaselineDecision) { d.RuleDirectory = "other" },
		"self_rule":           func(d *IgnoreBaselineDecision) { d.RuleDirectory = d.MatchedPath },
		"descendant_rule":     func(d *IgnoreBaselineDecision) { d.RuleDirectory = d.Path },
		"no_provenance":       func(d *IgnoreBaselineDecision) { d.RuleDirectory = "" },
		"line_zero":           func(d *IgnoreBaselineDecision) { d.RuleLine = 0 },
		"line_limit":          func(d *IgnoreBaselineDecision) { d.RuleLine = 4097 },
		"path_escape":         func(d *IgnoreBaselineDecision) { d.Path = "../file" },
		"root_invalid":        func(d *IgnoreBaselineDecision) { d.RootID = "root" },
		"raw_reason":          func(d *IgnoreBaselineDecision) { d.Reason = "OS error at private path" },
		"missing_with_rule":   func(d *IgnoreBaselineDecision) { d.Outcome = IgnoreBaselineMissing },
	} {
		t.Run(name, func(t *testing.T) {
			d := base
			change(&d)
			if ValidateIgnoreBaselineDecision(d) != ErrInvalid {
				t.Fatal("invalid provenance accepted")
			}
		})
	}
	for _, reason := range []string{IgnoreUnknownSource, IgnoreChangedSource, IgnoreUnknownCoverage} {
		d := IgnoreBaselineDecision{RootID: ignoreTestJobID, Path: "unseen.mkv", Outcome: IgnoreBaselineUnknown, Reason: reason}
		if ValidateIgnoreBaselineDecision(d) != nil {
			t.Fatal("fixed unknown reason rejected")
		}
		d.Reason = "permission denied: private-path"
		if ValidateIgnoreBaselineDecision(d) != ErrInvalid {
			t.Fatal("raw OS reason accepted")
		}
	}
	d := IgnoreBaselineDecision{RootID: ignoreTestJobID, Path: "unseen.mkv", Outcome: IgnoreBaselineMissing}
	if ValidateIgnoreBaselineDecision(d) != nil {
		t.Fatal("missing decision rejected")
	}
	d.Outcome = IgnoreBaselineUnknown
	if ValidateIgnoreBaselineDecision(d) != ErrInvalid {
		t.Fatal("unknown without reason accepted")
	}
}

func TestIgnoreComparisonExclusionsDoNotDiluteMissing(t *testing.T) {
	// 900 excluded old paths must not turn 10/100 missing into 10/1000.
	c := IgnoreComparisonCounts{Observed: 90, Missing: 10, Excluded: 900}
	got, err := CompareIgnoreBaseline(c, true, true, 100, 10)
	if err != nil || got.Missing != 10 || got.Denominator != 100 || !got.ReviewRequired || !got.ComparisonComplete {
		t.Fatal("exclusions diluted threshold", got, err)
	}
	c.Excluded = 0
	without, err := CompareIgnoreBaseline(c, true, true, 100, 10)
	if err != nil || without != got {
		t.Fatal("excluded count changed comparable result", err)
	}
	got, err = CompareIgnoreBaseline(c, true, true, 100, 11)
	if err != nil || got.ReviewRequired {
		t.Fatal("percentage boundary rounded incorrectly", got, err)
	}
	got, err = CompareIgnoreBaseline(c, true, true, 10, 100)
	if err != nil || !got.ReviewRequired {
		t.Fatal("absolute count threshold bypassed")
	}
}

func TestIgnoreComparisonCurrentUncertaintyBlocksScopeReset(t *testing.T) {
	for _, comparable := range []bool{false, true} {
		for _, counts := range []IgnoreComparisonCounts{{Missing: 10}, {Observed: 10, Unknown: 1}} {
			got, err := CompareIgnoreBaseline(counts, false, comparable, 100, 10)
			if err != nil || got.Missing != 0 || got.ComparisonComplete || !got.ReviewRequired {
				t.Fatal("incomplete observation accepted", got, err)
			}
		}
		got, err := CompareIgnoreBaseline(IgnoreComparisonCounts{Missing: 10, Unknown: 1}, true, comparable, 100, 10)
		if err != nil || got.Missing != 0 || got.ComparisonComplete || !got.ReviewRequired {
			t.Fatal("unknown current evidence accepted", got, err)
		}
	}
	got, err := CompareIgnoreBaseline(IgnoreComparisonCounts{Missing: 500000}, true, false, 1, 1)
	if err != nil || got != (IgnoreComparisonResult{}) {
		t.Fatal("complete scope reset claimed missing or permanent review", got, err)
	}
	got, err = CompareIgnoreBaseline(IgnoreComparisonCounts{Excluded: 500000}, true, true, 1, 1)
	if err != nil || got.Missing != 0 || got.Denominator != 0 || got.ReviewRequired || !got.ComparisonComplete {
		t.Fatal("all excluded scope divided by zero", got, err)
	}
}

func TestIgnoreComparisonBoundsAndDecisionRedaction(t *testing.T) {
	for _, c := range []IgnoreComparisonCounts{{Observed: -1}, {Missing: math.MaxInt64}, {Excluded: 500001}, {Unknown: -1}} {
		if _, err := CompareIgnoreBaseline(c, true, true, 1, 1); err != ErrInvalid {
			t.Fatal("unsafe count accepted", err)
		}
	}
	if _, err := CompareIgnoreBaseline(IgnoreComparisonCounts{Observed: 500000, Missing: 1}, true, true, 1, 1); err != ErrScanLimit {
		t.Fatal("combined baseline bound ignored")
	}
	for _, limits := range [][2]int{{0, 1}, {500001, 1}, {1, 0}, {1, 101}} {
		if _, err := CompareIgnoreBaseline(IgnoreComparisonCounts{}, true, true, limits[0], limits[1]); err != ErrInvalid {
			t.Fatal("invalid threshold accepted")
		}
	}
	d := IgnoreBaselineDecision{Path: "private-path", Reason: "private-path"}
	data, err := json.Marshal(d)
	if err != nil || string(data) != "{}" {
		t.Fatal("private decision JSON leak")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, d), "private-path") {
			t.Fatal("private decision format leak")
		}
	}
}
