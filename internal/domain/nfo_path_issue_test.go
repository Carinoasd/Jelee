package domain

import (
	"strings"
	"testing"
)

func TestNFOPathCodesAreAFixedWhitelist(t *testing.T) {
	seen := map[string]bool{}
	for _, code := range NFOPathCodes {
		if !ValidNFOPathCode(code) || !strings.HasPrefix(code, "nfo_path_") || seen[code] {
			t.Fatalf("path code %q is not a distinct nfo_path_ whitelist value", code)
		}
		seen[code] = true
		issue := NFOIssue{Severity: "error", Code: code, Field: NFOPathField, Entry: -1}
		if !ValidNFOIssue(issue) {
			t.Fatalf("path issue %q rejected", code)
		}
		// A path issue is always the document-level error on the path field.
		for name, change := range map[string]func(*NFOIssue){
			"warning": func(v *NFOIssue) { v.Severity = "warning" },
			"field":   func(v *NFOIssue) { v.Field = "title" },
			"entry":   func(v *NFOIssue) { v.Entry = 0 },
		} {
			bad := issue
			change(&bad)
			if ValidNFOIssue(bad) {
				t.Fatalf("path issue %q accepted with a wrong %s", code, name)
			}
		}
	}
	for _, code := range []string{"", "nfo_path", "nfo_path_other", "NFO_PATH_SYMLINK", "nfo_invalid_xml"} {
		if ValidNFOPathCode(code) {
			t.Fatalf("%q accepted as a path code", code)
		}
	}
	if len(NFOPathCodes) != 7 {
		t.Fatalf("the path vocabulary changed: %v", NFOPathCodes)
	}
}

func TestNFOPathSummaryIsTheOnlySummaryWithoutEntriesOrFailure(t *testing.T) {
	for _, code := range NFOPathCodes {
		summary, ok := NewNFOPathSummary(code)
		if !ok || !IsNFOPathSummary(summary) || ValidateNFOSummary(summary) != nil {
			t.Fatalf("path summary %q rejected", code)
		}
		encoded, err := MarshalNFOSummary(summary)
		if err != nil || !strings.Contains(string(encoded), `"entries":0`) || !strings.Contains(string(encoded), code) {
			t.Fatalf("path summary %q does not marshal: %s %v", code, encoded, err)
		}
		for name, change := range map[string]func(*NFOValidationSummary){
			"valid":    func(v *NFOValidationSummary) { v.Status = NFOStatusValid },
			"entries":  func(v *NFOValidationSummary) { v.Entries = 1 },
			"encoding": func(v *NFOValidationSummary) { v.Encoding = "UTF-8" },
			"root":     func(v *NFOValidationSummary) { v.Root = "movie" },
			"failure":  func(v *NFOValidationSummary) { v.FailureCode = NFOFailureInvalidXML },
			"warning":  func(v *NFOValidationSummary) { v.WarningCount, v.IssueCount = 1, 2 },
			"two issues": func(v *NFOValidationSummary) {
				v.Issues = append(v.Issues, v.Issues[0])
				v.ErrorCount, v.IssueCount = 2, 2
			},
			"guessed": func(v *NFOValidationSummary) { v.EncodingGuessed = true },
		} {
			bad, _ := NewNFOPathSummary(code)
			bad.Issues = append([]NFOIssue(nil), bad.Issues...)
			change(&bad)
			if ValidateNFOSummary(bad) == nil {
				t.Fatalf("path summary %q accepted with a wrong %s", code, name)
			}
		}
	}
	if _, ok := NewNFOPathSummary("nfo_invalid_xml"); ok {
		t.Fatal("a summary was made of a code that is not a path code")
	}
	// A path issue never appears among the issues of a document that was read.
	read := nfoValidSummary()
	read.Status, read.ErrorCount, read.IssueCount = NFOStatusInvalid, 1, 1
	read.Issues = []NFOIssue{{Severity: "error", Code: NFOPathSymlink, Field: NFOPathField, Entry: -1}}
	read.Entries = 1
	if ValidateNFOSummary(read) == nil {
		t.Fatal("a path issue was accepted in a summary of a file that was read")
	}
	read.Issues = []NFOIssue{{Severity: "warning", Code: NFOPathSymlink, Field: NFOPathField, Entry: -1}}
	read.Status, read.ErrorCount, read.WarningCount = NFOStatusValid, 0, 1
	if ValidateNFOSummary(read) == nil {
		t.Fatal("a path warning was accepted")
	}
}

func TestNFOIDMissingIsNotAnIssueCode(t *testing.T) {
	// An entry without an identifier is normal; the writer adds one on any
	// rewrite, so the code is not part of the issue whitelist.
	for _, severity := range []string{"warning", "error"} {
		if ValidNFOIssue(NFOIssue{Severity: severity, Code: "nfo_id_missing", Field: "uniqueid", Entry: 0}) {
			t.Fatalf("nfo_id_missing accepted as a %s", severity)
		}
	}
}

func TestNFOPathObservationAndIssuePage(t *testing.T) {
	observation := nfoQueryObservation()
	observation.Status, observation.Entries, observation.ErrorCount, observation.IssueCount = NFOStatusInvalid, 0, 1, 1
	if ValidateNFOObservation(observation) != nil {
		t.Fatal("the observation of an illegal path was rejected")
	}
	for name, change := range map[string]func(*NFOObservation){
		"valid":    func(v *NFOObservation) { v.Status = NFOStatusValid },
		"warning":  func(v *NFOObservation) { v.WarningCount, v.IssueCount = 1, 2 },
		"no error": func(v *NFOObservation) { v.ErrorCount, v.IssueCount = 0, 0 },
	} {
		bad := observation
		change(&bad)
		if ValidateNFOObservation(bad) == nil {
			t.Fatalf("an entry-less observation with %s was accepted", name)
		}
	}
	issue := NFOIssue{Severity: "error", Code: NFOPathCaseCollision, Field: NFOPathField, Entry: -1}
	page := NFOIssuesPage{ObservationID: nfoJobsTestID, IssueCount: 1, Issues: []NFOIssue{issue}}
	if ValidateNFOIssuesPage(page) != nil {
		t.Fatal("the issue page of an illegal path was rejected")
	}
	empty := page
	empty.Offset, empty.Issues = 1, []NFOIssue{}
	if ValidateNFOIssuesPage(empty) != nil {
		t.Fatal("the page after the only path issue was rejected")
	}
	other := page
	other.Issues = []NFOIssue{{Severity: "warning", Code: "nfo_title_missing", Field: "title", Entry: 0}}
	if ValidateNFOIssuesPage(other) == nil {
		t.Fatal("an entry-less page of an ordinary issue was accepted")
	}
	two := page
	two.IssueCount = 2
	if ValidateNFOIssuesPage(two) == nil {
		t.Fatal("an entry-less page of two issues was accepted")
	}
	withEntries := page
	withEntries.Entries = 1
	if ValidateNFOIssuesPage(withEntries) == nil {
		t.Fatal("a path issue was accepted on a page of a file that was read")
	}
}

func TestNFOExportFixModeAndCounters(t *testing.T) {
	if !ValidNFOExportMode(NFOExportFix) || ValidNFOExportMode("repair") || ValidNFOExportMode("") {
		t.Fatal("the export modes are all, missing and fix")
	}
	a := NFOExportCounts{Written: 1, Fixed: 2, Unfixable: 3, Unchanged: 4}
	b := NFOExportCounts{Fixed: 5, Unfixable: 6, Failed: 7}
	sum := a.Add(b)
	if sum.Fixed != 7 || sum.Unfixable != 9 || sum.Written != 1 || sum.Unchanged != 4 || sum.Failed != 7 || !sum.Valid() {
		t.Fatalf("counters add up wrong: %+v", sum)
	}
	if (NFOExportCounts{Fixed: -1}).Valid() || (NFOExportCounts{Unfixable: -1}).Valid() {
		t.Fatal("a negative fix counter was valid")
	}
}

func TestNFOItemValidationHasIssueCodes(t *testing.T) {
	v := NFOItemValidation{Status: NFOItemValidationChecked, IssueCodes: []string{"nfo_season_missing", "nfo_title_missing"}}
	if !v.Has("nfo_season_missing") || v.Has("nfo_invalid_date") || (NFOItemValidation{}).Has("nfo_season_missing") {
		t.Fatal("Has misreports the issue codes")
	}
	if strings.Contains(v.String(), "nfo_season_missing") || strings.Contains(v.GoString(), "nfo_season_missing") {
		t.Fatal("a validation prints its content")
	}
}
