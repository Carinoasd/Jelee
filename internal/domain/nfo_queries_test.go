package domain

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func nfoQueryObservation() NFOObservation {
	observed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return NFOObservation{ID: nfoJobsTestID, RootID: nfoJobsLibraryID, Path: "Series/episode.nfo",
		Status: NFOStatusValid, Entries: 1, ObservedAt: observed, ExpiresAt: observed.Add(time.Hour)}
}

func TestNFOObservationPathBoundary(t *testing.T) {
	for _, value := range []string{"movie.nfo", ".hidden/movie.nfo", `系列/片名 ["特別版"].nfo`, strings.Repeat("x", ScanPathMaxBytes), strings.Repeat("x/", 127) + "x"} {
		if !ValidNFOObservationPath(value) {
			t.Fatal("valid relative filename rejected")
		}
	}
	for _, value := range []string{"", ".", "..", "../secret", "a/../secret", "a/./b", "/absolute", "a//b", "a/", `C:\secret.nfo`, `a\b`, "http:private", "a\nsecret", "a\x00b", "a\u0085b", string([]byte{0xff}), strings.Repeat("x", ScanPathMaxBytes+1), strings.Repeat("x/", 128) + "x", strings.Repeat("片", 342)} {
		if ValidNFOObservationPath(value) {
			t.Fatal("unsafe or unbounded relative path accepted")
		}
	}
}

func TestNFOObservationValidationSummarySemantics(t *testing.T) {
	valid := nfoQueryObservation()
	warning := valid
	warning.WarningCount, warning.IssueCount = 1, 1
	semantic := valid
	semantic.Status, semantic.ErrorCount, semantic.IssueCount = NFOStatusInvalid, 1, 1
	truncated := semantic
	truncated.WarningCount, truncated.IssueCount, truncated.IssuesTruncated = 64, 65, true
	for _, value := range []NFOObservation{valid, warning, semantic, truncated} {
		if ValidateNFOObservation(value) != nil {
			t.Fatal("safe observation summary rejected")
		}
	}
	for _, code := range []NFOFailureCode{NFOFailureInvalidXML, NFOFailureUnsafeXML, NFOFailureInvalidEncoding, NFOFailureUnsupportedEncoding, NFOFailureTooComplex} {
		value := valid
		value.Status, value.Entries, value.FailureCode = NFOStatusInvalid, 0, code
		if ValidateNFOObservation(value) != nil {
			t.Fatal("fixed invalid parse observation rejected")
		}
	}
	for name, change := range map[string]func(*NFOObservation){
		"id":               func(v *NFOObservation) { v.ID = "bad" },
		"root":             func(v *NFOObservation) { v.RootID = "bad" },
		"path":             func(v *NFOObservation) { v.Path = "../private" },
		"status":           func(v *NFOObservation) { v.Status = "private-status" },
		"entries_low":      func(v *NFOObservation) { v.Entries = -1 },
		"entries_high":     func(v *NFOObservation) { v.Entries = 129 },
		"entries_zero":     func(v *NFOObservation) { v.Entries = 0 },
		"warnings_low":     func(v *NFOObservation) { v.WarningCount = -1 },
		"warnings_high":    func(v *NFOObservation) { v.WarningCount = NFOIssueTotalMax + 1 },
		"errors_low":       func(v *NFOObservation) { v.ErrorCount = -1 },
		"errors_overflow":  func(v *NFOObservation) { v.ErrorCount = 1<<63 - 1 },
		"count_low":        func(v *NFOObservation) { v.IssueCount = -1 },
		"count_high":       func(v *NFOObservation) { v.IssueCount = NFOIssueTotalMax + 1 },
		"count_sum":        func(v *NFOObservation) { v.IssueCount = 1 },
		"truncated":        func(v *NFOObservation) { v.IssuesTruncated = true },
		"status_semantics": func(v *NFOObservation) { v.Status = NFOStatusInvalid },
		"failure_unknown":  func(v *NFOObservation) { v.FailureCode = "private-error" },
		"failure_read":     func(v *NFOObservation) { v.FailureCode = "nfo_changed" },
		"failure_status":   func(v *NFOObservation) { v.FailureCode, v.Entries = NFOFailureInvalidXML, 0 },
		"failure_entries":  func(v *NFOObservation) { v.FailureCode, v.Status = NFOFailureInvalidXML, NFOStatusInvalid },
		"failure_issues": func(v *NFOObservation) {
			v.FailureCode, v.Status, v.Entries, v.WarningCount, v.IssueCount = NFOFailureInvalidXML, NFOStatusInvalid, 0, 1, 1
		},
		"observed_zero":    func(v *NFOObservation) { v.ObservedAt = time.Time{} },
		"expiry_zero":      func(v *NFOObservation) { v.ExpiresAt = time.Time{} },
		"expiry_not_after": func(v *NFOObservation) { v.ExpiresAt = v.ObservedAt },
		"timestamp_low":    func(v *NFOObservation) { v.ObservedAt = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC) },
		"timestamp_high":   func(v *NFOObservation) { v.ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
	} {
		t.Run(name, func(t *testing.T) {
			value := valid
			change(&value)
			if ValidateNFOObservation(value) != ErrInvalid {
				t.Fatal("invalid observation accepted")
			}
		})
	}
}

func TestNFOObservationPageCursorAndOrder(t *testing.T) {
	items := make([]NFOObservation, NFOObservationPageMax)
	for i := range items {
		items[i] = nfoQueryObservation()
		items[i].ID = fmt.Sprintf("11111111-1111-4111-8111-%012d", i+1)
	}
	full := NFOObservationPage{Items: items, NextCursor: items[len(items)-1].ID}
	if ValidateNFOObservationPage(full) != nil || ValidateNFOObservationPage(NFOObservationPage{Items: []NFOObservation{}}) != nil {
		t.Fatal("valid bounded current page rejected")
	}
	for name, page := range map[string]NFOObservationPage{
		"null":         {},
		"oversize":     {Items: append(append([]NFOObservation{}, items...), nfoQueryObservation())},
		"empty_cursor": {Items: []NFOObservation{}, NextCursor: nfoJobsTestID},
		"wrong_cursor": {Items: items, NextCursor: nfoJobsLibraryID},
		"duplicate":    {Items: []NFOObservation{items[0], items[0]}},
		"reverse":      {Items: []NFOObservation{items[1], items[0]}},
		"bad_item":     {Items: []NFOObservation{{ID: nfoJobsTestID}}},
	} {
		t.Run(name, func(t *testing.T) {
			if ValidateNFOObservationPage(page) != ErrInvalid {
				t.Fatal("unsafe or nonprogressing observation page accepted")
			}
		})
	}
}

func nfoQueryIssuePage() NFOIssuesPage {
	return NFOIssuesPage{ObservationID: nfoJobsTestID, Entries: 1, IssueCount: 1,
		Issues: []NFOIssue{{Severity: "warning", Code: "nfo_title_missing", Field: "title", Entry: 0}}}
}

func TestNFOIssuesPageRetainedPrefixPagination(t *testing.T) {
	base := nfoQueryIssuePage()
	page := base
	page.IssueCount, page.IssuesTruncated = 65, true
	page.Issues = make([]NFOIssue, NFOIssuesPageMax)
	for i := range page.Issues {
		page.Issues[i] = base.Issues[0]
	}
	next := 32
	page.NextOffset = &next
	if ValidateNFOIssuesPage(page) != nil {
		t.Fatal("first retained prefix page rejected")
	}
	page.Offset, page.NextOffset = 32, nil
	if ValidateNFOIssuesPage(page) != nil {
		t.Fatal("last retained prefix page rejected because original count exceeds 64")
	}
	page.Offset, page.Issues = 64, []NFOIssue{}
	if ValidateNFOIssuesPage(page) != nil {
		t.Fatal("empty endpoint at retained prefix boundary rejected")
	}
	empty := base
	empty.IssueCount, empty.Issues = 0, []NFOIssue{}
	if ValidateNFOIssuesPage(empty) != nil {
		t.Fatal("valid file with no issues rejected")
	}
	for _, code := range []NFOFailureCode{NFOFailureInvalidXML, NFOFailureUnsafeXML, NFOFailureInvalidEncoding, NFOFailureUnsupportedEncoding, NFOFailureTooComplex} {
		failure := empty
		failure.Entries, failure.FailureCode = 0, code
		if ValidateNFOIssuesPage(failure) != nil {
			t.Fatal("parse failure reason without issues rejected")
		}
	}
	for name, change := range map[string]func(*NFOIssuesPage){
		"id":                  func(v *NFOIssuesPage) { v.ObservationID = "bad" },
		"entries_low":         func(v *NFOIssuesPage) { v.Entries = -1 },
		"entries_high":        func(v *NFOIssuesPage) { v.Entries = 129 },
		"entries_zero":        func(v *NFOIssuesPage) { v.Entries = 0 },
		"count_low":           func(v *NFOIssuesPage) { v.IssueCount = -1 },
		"count_high":          func(v *NFOIssuesPage) { v.IssueCount = NFOIssueTotalMax + 1 },
		"truncate":            func(v *NFOIssuesPage) { v.IssuesTruncated = true },
		"offset_low":          func(v *NFOIssuesPage) { v.Offset = -1 },
		"offset_high":         func(v *NFOIssuesPage) { v.Offset = 65 },
		"offset_past_end":     func(v *NFOIssuesPage) { v.Offset = 1 },
		"null":                func(v *NFOIssuesPage) { v.Issues = nil },
		"oversize":            func(v *NFOIssuesPage) { v.IssueCount, v.Issues = 33, make([]NFOIssue, 33) },
		"omitted_next":        func(v *NFOIssuesPage) { v.IssueCount = 2 },
		"next_no_progress":    func(v *NFOIssuesPage) { n := 0; v.NextOffset = &n },
		"next_skips":          func(v *NFOIssuesPage) { n := 2; v.IssueCount, v.NextOffset = 3, &n },
		"next_at_end":         func(v *NFOIssuesPage) { n := 1; v.NextOffset = &n },
		"empty_nonterminal":   func(v *NFOIssuesPage) { n := 0; v.Issues, v.NextOffset = []NFOIssue{}, &n },
		"failure_unknown":     func(v *NFOIssuesPage) { v.FailureCode = "private-error" },
		"failure_entries":     func(v *NFOIssuesPage) { v.FailureCode = NFOFailureInvalidXML },
		"failure_issues":      func(v *NFOIssuesPage) { v.FailureCode, v.Entries = NFOFailureInvalidXML, 0 },
		"issue_unknown":       func(v *NFOIssuesPage) { v.Issues[0].Code = "private-code" },
		"issue_foreign_entry": func(v *NFOIssuesPage) { v.Issues[0].Entry = 1 },
		"issue_duplicate_guess": func(v *NFOIssuesPage) {
			v.IssueCount = 2
			v.Issues = []NFOIssue{{Severity: "warning", Code: "nfo_encoding_guessed", Field: "encoding", Entry: -1}, {Severity: "warning", Code: "nfo_encoding_guessed", Field: "encoding", Entry: -1}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := nfoQueryIssuePage()
			change(&value)
			if ValidateNFOIssuesPage(value) != ErrInvalid {
				t.Fatal("unsafe or nonprogressing issue page accepted")
			}
		})
	}
}
