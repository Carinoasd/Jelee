package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func nfoValidSummary() NFOValidationSummary {
	return NFOValidationSummary{SchemaVersion: NFOSummarySchemaVersion, Status: NFOStatusValid, Encoding: "UTF-8", Root: "movie", Entries: 1}
}

func TestNFOSummaryCanonicalSafeProjection(t *testing.T) {
	summary := nfoValidSummary()
	encoded, err := MarshalNFOSummary(summary)
	want := `{"schemaVersion":1,"status":"valid","encoding":"UTF-8","encodingGuessed":false,"root":"movie","entries":1,"failureCode":"","warningCount":0,"errorCount":0,"issueCount":0,"issuesTruncated":false,"issues":[]}`
	if err != nil || string(encoded) != want || summary.Issues != nil {
		t.Fatalf("canonical summary failed: %s, %v", encoded, err)
	}
	// The permitted schema cannot accidentally serialize rich metadata fields.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"title", "plot", "art", "raw", "path", "filename", "rootPath", "lockedFields", "uniqueIDs", "URL"} {
		if _, exists := fields[key]; exists {
			t.Fatalf("unexpected metadata field %s", key)
		}
	}
	summary.IssueCount, summary.WarningCount = 1, 1
	summary.Issues = []NFOIssue{{"warning", "nfo_title_missing", "title", 0}}
	if _, err := MarshalNFOSummary(summary); err != nil {
		t.Fatal("warning was incorrectly considered invalid", err)
	}
	summary.Status, summary.ErrorCount, summary.WarningCount = NFOStatusInvalid, 1, 0
	summary.Issues[0] = NFOIssue{"error", "nfo_unsafe_reference", "art", 0}
	if _, err := MarshalNFOSummary(summary); err != nil {
		t.Fatal("bounded semantic failure was rejected", err)
	}
}

func TestNFOSummaryParseFailuresAreDistinctFromSourceFailures(t *testing.T) {
	for _, code := range []NFOFailureCode{NFOFailureInvalidXML, NFOFailureUnsafeXML, NFOFailureInvalidEncoding, NFOFailureUnsupportedEncoding, NFOFailureTooComplex} {
		summary := NFOValidationSummary{SchemaVersion: 1, Status: NFOStatusInvalid, Encoding: "unknown", Root: "unknown", FailureCode: code}
		if _, err := MarshalNFOSummary(summary); err != nil {
			t.Fatalf("known parser failure %s: %v", code, err)
		}
		for _, change := range []func(*NFOValidationSummary){
			func(s *NFOValidationSummary) { s.Status = NFOStatusValid },
			func(s *NFOValidationSummary) { s.Encoding = "UTF-8" },
			func(s *NFOValidationSummary) { s.Root = "movie" },
			func(s *NFOValidationSummary) { s.Entries = 1 },
			func(s *NFOValidationSummary) { s.EncodingGuessed = true },
			func(s *NFOValidationSummary) {
				s.WarningCount, s.IssueCount = 1, 1
				s.Issues = []NFOIssue{{"warning", "nfo_encoding_guessed", "encoding", -1}}
			},
		} {
			bad := summary
			change(&bad)
			if err := ValidateNFOSummary(bad); !errors.Is(err, ErrInvalid) {
				t.Fatal("partial parser failure observation accepted")
			}
		}
	}
	for _, code := range []NFOFailureCode{NFOFailureTooLarge, "nfo_changed", "nfo_read_failed", "nfo_input_unavailable", "context canceled", "C:/private/path.nfo"} {
		summary := NFOValidationSummary{SchemaVersion: 1, Status: NFOStatusInvalid, Encoding: "unknown", Root: "unknown", FailureCode: code}
		if ValidNFOParseFailureCode(code) || ValidateNFOSummary(summary) == nil {
			t.Fatal("a source/runtime failure could become a negative cache entry")
		}
	}
}

func TestNFOSummaryCountsBoundsAndExplicitTruncation(t *testing.T) {
	summary := nfoValidSummary()
	summary.Status, summary.ErrorCount, summary.IssueCount = NFOStatusInvalid, NFOIssuesMax+1, NFOIssuesMax+1
	summary.IssuesTruncated = true
	for range NFOIssuesMax {
		summary.Issues = append(summary.Issues, NFOIssue{"error", "nfo_invalid_integer", "year", 0})
	}
	encoded, err := MarshalNFOSummary(summary)
	if err != nil || len(encoded) > NFOSummaryMaxBytes {
		t.Fatal("bounded prefix with preserved total rejected", err)
	}
	for _, change := range []func(*NFOValidationSummary){
		func(s *NFOValidationSummary) { s.SchemaVersion = 2 },
		func(s *NFOValidationSummary) { s.Status = "C:/private/path.nfo" },
		func(s *NFOValidationSummary) { s.Encoding = "PRIVATE" },
		func(s *NFOValidationSummary) { s.Root = "<private-user-tag>" },
		func(s *NFOValidationSummary) { s.Entries = -1 },
		func(s *NFOValidationSummary) { s.Entries = NFOEntriesMax + 1 },
		func(s *NFOValidationSummary) { s.WarningCount = -1 },
		func(s *NFOValidationSummary) { s.ErrorCount = math.MaxInt64 },
		func(s *NFOValidationSummary) { s.IssueCount = NFOIssueTotalMax + 1 },
		func(s *NFOValidationSummary) { s.IssueCount++ },
		func(s *NFOValidationSummary) { s.Issues = s.Issues[:NFOIssuesMax-1] },
		func(s *NFOValidationSummary) { s.IssuesTruncated = false },
		func(s *NFOValidationSummary) { s.Status = NFOStatusValid },
		func(s *NFOValidationSummary) { s.Encoding = "unknown" },
		func(s *NFOValidationSummary) { s.Entries = 0 },
	} {
		bad := summary
		change(&bad)
		if encoded, err := MarshalNFOSummary(bad); err == nil || encoded != nil {
			t.Fatal("invalid summary returned persisted bytes")
		}
	}
	summary.Issues = append(summary.Issues, summary.Issues[0])
	if encoded, err := MarshalNFOSummary(summary); !errors.Is(err, ErrNFOSummaryLimit) || encoded != nil {
		t.Fatal("issue list bound was not enforced before serialization")
	}
	base := nfoValidSummary()
	base.IssuesTruncated = true
	if ValidateNFOSummary(base) == nil {
		t.Fatal("empty result claimed truncation")
	}
}

func TestNFOEncodingGuessAndObservedIssueTotals(t *testing.T) {
	for _, encoding := range []string{"GBK", "UTF-16LE", "UTF-16BE"} {
		summary := nfoValidSummary()
		summary.Encoding, summary.EncodingGuessed = encoding, true
		summary.IssueCount, summary.WarningCount = 1, 1
		summary.Issues = []NFOIssue{{"warning", "nfo_encoding_guessed", "encoding", -1}}
		if ValidateNFOSummary(summary) != nil {
			t.Fatal("known encoding guess rejected")
		}
		for _, change := range []func(*NFOValidationSummary){
			func(s *NFOValidationSummary) { s.Encoding = "UTF-8" },
			func(s *NFOValidationSummary) { s.EncodingGuessed = false },
			func(s *NFOValidationSummary) { s.WarningCount, s.ErrorCount, s.Status = 0, 1, NFOStatusInvalid },
			func(s *NFOValidationSummary) { s.Issues[0].Entry = 0 },
			func(s *NFOValidationSummary) { s.Issues[0].Field = "https://private.invalid/image" },
			func(s *NFOValidationSummary) { s.Issues[0].Code = "unrecognized" },
			func(s *NFOValidationSummary) {
				s.IssueCount, s.WarningCount = 2, 2
				s.Issues = append(s.Issues, s.Issues[0])
			},
		} {
			bad := summary
			bad.Issues = append([]NFOIssue(nil), summary.Issues...)
			change(&bad)
			if ValidateNFOSummary(bad) == nil {
				t.Fatal("unproven guess, inconsistent count or unsafe issue accepted")
			}
		}
	}
	summary := nfoValidSummary()
	summary.IssueCount, summary.ErrorCount, summary.Status = 1, 1, NFOStatusInvalid
	summary.Issues = []NFOIssue{{"error", "nfo_invalid_integer", "year", 1}}
	if ValidateNFOSummary(summary) == nil {
		t.Fatal("issue referred to a nonexistent entry")
	}
}

func TestNFOIssueVocabularyMatchesFixedParser(t *testing.T) {
	type vocabulary struct {
		severity, code string
		fields         []string
	}
	for _, row := range []vocabulary{
		{"warning", "nfo_unknown_root", []string{"root"}},
		{"warning", "nfo_wrapper_fields_ignored", []string{"root"}},
		{"warning", "nfo_kind_unknown", []string{"root"}},
		{"warning", "nfo_title_missing", []string{"title"}},
		{"warning", "nfo_season_missing", []string{"season"}},
		{"warning", "nfo_episode_missing", []string{"episode"}},
		{"warning", "nfo_conflicting_id", []string{"uniqueid"}},
		{"error", "nfo_invalid_integer", []string{"year", "season", "seasonnumber", "episode", "displayseason", "displayepisode", "runtime", "actor", "thumb", "poster", "banner", "clearart", "clearlogo", "landscape", "ratings"}},
		{"error", "nfo_invalid_number", []string{"rating", "communityrating", "userrating", "ratings"}},
		{"error", "nfo_invalid_boolean", []string{"uniqueid", "ratings", "lockdata"}},
		{"error", "nfo_person_name_missing", []string{"actor"}},
		{"error", "nfo_id_incomplete", []string{"uniqueid", "imdbid", "tmdbid", "tvdbid", "id"}},
		{"error", "nfo_invalid_rating_scale", []string{"ratings"}},
		{"error", "nfo_rating_value_missing", []string{"ratings"}},
		{"error", "nfo_invalid_date", []string{"premiered", "aired", "dateadded"}},
		{"error", "nfo_unsafe_reference", []string{"art", "art.preview", "actor.thumb"}},
	} {
		for _, field := range row.fields {
			issue := NFOIssue{row.severity, row.code, field, 0}
			if !ValidNFOIssue(issue) {
				t.Fatalf("parser issue refused: %+v", issue)
			}
			issue.Field = "private/path"
			if ValidNFOIssue(issue) {
				t.Fatal("unknown field accepted")
			}
		}
	}
	for _, issue := range []NFOIssue{
		{"warning", "unknown", "root", 0}, {"error", "unknown", "root", 0}, {"fatal", "nfo_title_missing", "title", 0},
		{"warning", "nfo_title_missing", "title", -1}, {"warning", "nfo_title_missing", "title", -2}, {"warning", "nfo_title_missing", "title", 128},
	} {
		if ValidNFOIssue(issue) {
			t.Fatal("invalid issue accepted")
		}
	}
	for _, root := range []string{"movie", "tvshow", "season", "episode", "episodedetails", "wrapper", "unknown"} {
		if !ValidNFORoot(root) {
			t.Fatal("known root refused")
		}
	}
}

func TestNFOPrivatePathsDoNotSerializeOrFormat(t *testing.T) {
	secret := "private-parent/source.xml"
	source := NFOSource{RootPath: secret, RelativePath: secret}
	entry := NFOEntry{Inventory: InventoryEntry{Path: secret}, Source: source}
	page := NFOPage{Entries: []NFOEntry{entry}}
	for _, value := range []any{source, &source, entry, &entry, page} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, output := range []string{string(encoded), fmt.Sprintf("%v", value), fmt.Sprintf("%+v", value), fmt.Sprintf("%#v", value)} {
			if strings.Contains(output, "private-parent") || strings.Contains(output, "source.xml") {
				t.Fatal("private source path escaped its projection")
			}
		}
	}
}
