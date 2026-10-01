package postgres

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func nfoDecodeValidSummary() domain.NFOValidationSummary {
	return domain.NFOValidationSummary{SchemaVersion: domain.NFOSummarySchemaVersion,
		Status: domain.NFOStatusValid, Encoding: "UTF-8", Root: "movie", Entries: 1,
		Issues: []domain.NFOIssue{}}
}

func nfoDecodeJSON(t *testing.T, summary domain.NFOValidationSummary) []byte {
	t.Helper()
	raw, err := domain.MarshalNFOSummary(summary)
	if err != nil {
		t.Fatalf("invalid test fixture: %v", err)
	}
	return raw
}

func nfoDecodeMutate(t *testing.T, raw []byte, edit func(map[string]json.RawMessage)) []byte {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal("invalid JSON test fixture")
	}
	edit(object)
	result, err := json.Marshal(object)
	if err != nil {
		t.Fatal("invalid JSON mutation")
	}
	return result
}

func TestNFOSummaryDecodeAccepted(t *testing.T) {
	valid := nfoDecodeValidSummary()
	warning := nfoDecodeValidSummary()
	warning.WarningCount, warning.IssueCount = 1, 1
	warning.Issues = []domain.NFOIssue{{Severity: "warning", Code: "nfo_title_missing", Field: "title", Entry: 0}}
	semantic := nfoDecodeValidSummary()
	semantic.Status, semantic.ErrorCount, semantic.IssueCount = domain.NFOStatusInvalid, 1, 1
	semantic.Issues = []domain.NFOIssue{{Severity: "error", Code: "nfo_invalid_integer", Field: "year", Entry: 0}}
	guessed := nfoDecodeValidSummary()
	guessed.Encoding, guessed.EncodingGuessed, guessed.WarningCount, guessed.IssueCount = "GBK", true, 1, 1
	guessed.Issues = []domain.NFOIssue{{Severity: "warning", Code: "nfo_encoding_guessed", Field: "encoding", Entry: -1}}
	truncated := nfoDecodeValidSummary()
	truncated.Status, truncated.WarningCount, truncated.ErrorCount = domain.NFOStatusInvalid, domain.NFOIssuesMax, 1
	truncated.IssueCount, truncated.IssuesTruncated = domain.NFOIssuesMax+1, true
	truncated.Issues = make([]domain.NFOIssue, domain.NFOIssuesMax)
	for i := range truncated.Issues {
		truncated.Issues[i] = warning.Issues[0]
	}
	cases := []struct {
		name string
		want domain.NFOValidationSummary
	}{{"valid", valid}, {"warning", warning}, {"semantic_error", semantic}, {"encoding_guessed", guessed}, {"error_beyond_retained_prefix", truncated}}
	for _, code := range []domain.NFOFailureCode{domain.NFOFailureInvalidXML, domain.NFOFailureUnsafeXML,
		domain.NFOFailureInvalidEncoding, domain.NFOFailureUnsupportedEncoding, domain.NFOFailureTooComplex} {
		cases = append(cases, struct {
			name string
			want domain.NFOValidationSummary
		}{string(code), domain.NFOValidationSummary{SchemaVersion: domain.NFOSummarySchemaVersion,
			Status: domain.NFOStatusInvalid, Encoding: "unknown", Root: "unknown", FailureCode: code,
			Issues: []domain.NFOIssue{}}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := nfoDecodeJSON(t, tc.want)
			// PostgreSQL's JSONB text has whitespace and a different key order.
			reordered := nfoDecodeMutate(t, raw, func(map[string]json.RawMessage) {})
			var formatted bytes.Buffer
			if err := json.Indent(&formatted, reordered, "", " "); err != nil {
				t.Fatal("cannot format fixture")
			}
			for _, input := range [][]byte{raw, formatted.Bytes()} {
				original := bytes.Clone(input)
				got, err := decodeNFOSummary(input)
				if err != nil || !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("summary did not round trip: error=%v", err)
				}
				if !bytes.Equal(input, original) {
					t.Fatal("decoder changed the persisted bytes")
				}
			}
		})
	}
}

func TestNFOSummaryDecodeRejected(t *testing.T) {
	const privateMarker = "private-payload-marker"
	base := nfoDecodeValidSummary()
	base.WarningCount, base.IssueCount = 1, 1
	base.Issues = []domain.NFOIssue{{Severity: "warning", Code: "nfo_title_missing", Field: "title", Entry: 0}}
	raw := nfoDecodeJSON(t, base)
	topKeys := []string{"schemaVersion", "status", "encoding", "encodingGuessed", "root", "entries", "failureCode",
		"warningCount", "errorCount", "issueCount", "issuesTruncated", "issues"}
	cases := map[string][]byte{
		"empty": nil, "null": []byte("null"), "array": []byte("[]"), "incomplete": []byte("{"),
		"trailing_object": append(bytes.Clone(raw), []byte(" {}")...),
		"over_size_bound": []byte(strings.Repeat(" ", domain.NFOSummaryMaxBytes+1)),
	}
	for _, key := range topKeys {
		cases["missing_"+key] = nfoDecodeMutate(t, raw, func(object map[string]json.RawMessage) { delete(object, key) })
		cases["null_"+key] = nfoDecodeMutate(t, raw, func(object map[string]json.RawMessage) { object[key] = json.RawMessage("null") })
	}
	cases["extra_top_field"] = nfoDecodeMutate(t, raw, func(object map[string]json.RawMessage) {
		object["rawXML"] = json.RawMessage(`"` + privateMarker + `"`)
	})
	wrongTypes := map[string]string{
		"schemaVersion": `"1"`, "status": "1", "encoding": "[]", "encodingGuessed": `"false"`, "root": "{}",
		"entries": "1.5", "failureCode": "false", "warningCount": `"1"`, "errorCount": "true",
		"issueCount": "1.0", "issuesTruncated": "0", "issues": "{}",
	}
	for key, value := range wrongTypes {
		cases["type_"+key] = nfoDecodeMutate(t, raw, func(object map[string]json.RawMessage) { object[key] = json.RawMessage(value) })
	}
	invalidValues := map[string]struct{ key, value string }{
		"old_schema": {"schemaVersion", "2"}, "unknown_status": {"status", `"` + privateMarker + `"`},
		"unknown_encoding": {"encoding", `"Latin-1"`}, "unknown_root": {"root", `"` + privateMarker + `"`},
		"negative_entries": {"entries", "-1"}, "too_many_entries": {"entries", "129"},
		"unknown_failure":              {"failureCode", `"` + privateMarker + `"`},
		"source_failure_not_cacheable": {"failureCode", `"nfo_changed"`},
		"negative_count":               {"errorCount", "-1"}, "overflow_count": {"errorCount", "9223372036854775808"},
		"count_bound": {"issueCount", "1000001"}, "count_mismatch": {"issueCount", "2"},
		"status_mismatch": {"status", `"invalid"`}, "truncation_mismatch": {"issuesTruncated", "true"},
		"guess_mismatch": {"encodingGuessed", "true"}, "issues_null_entry": {"issues", "[null]"},
		"issues_scalar_entry": {"issues", "[1]"}, "issues_missing_entry": {"issues", "[]"},
	}
	for name, invalid := range invalidValues {
		cases[name] = nfoDecodeMutate(t, raw, func(object map[string]json.RawMessage) { object[invalid.key] = json.RawMessage(invalid.value) })
	}
	mutateIssue := func(edit func(map[string]json.RawMessage)) []byte {
		return nfoDecodeMutate(t, raw, func(object map[string]json.RawMessage) {
			var issues []map[string]json.RawMessage
			if err := json.Unmarshal(object["issues"], &issues); err != nil {
				t.Fatal("invalid issue fixture")
			}
			edit(issues[0])
			encoded, err := json.Marshal(issues)
			if err != nil {
				t.Fatal("invalid issue mutation")
			}
			object["issues"] = encoded
		})
	}
	for _, key := range []string{"severity", "code", "field", "entry"} {
		cases["issue_missing_"+key] = mutateIssue(func(issue map[string]json.RawMessage) { delete(issue, key) })
		cases["issue_null_"+key] = mutateIssue(func(issue map[string]json.RawMessage) { issue[key] = json.RawMessage("null") })
	}
	cases["issue_extra_field"] = mutateIssue(func(issue map[string]json.RawMessage) { issue["message"] = json.RawMessage(`"` + privateMarker + `"`) })
	for name, invalid := range map[string]struct{ key, value string }{
		"severity": {"severity", `"notice"`}, "code": {"code", `"` + privateMarker + `"`},
		"field": {"field", `"` + privateMarker + `"`}, "negative_entry": {"entry", "-1"},
		"foreign_entry": {"entry", "1"}, "entry_float": {"entry", "0.5"},
		"entry_overflow": {"entry", "9223372036854775808"}, "entry_string": {"entry", `"0"`},
	} {
		cases["issue_invalid_"+name] = mutateIssue(func(issue map[string]json.RawMessage) { issue[invalid.key] = json.RawMessage(invalid.value) })
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			original := bytes.Clone(input)
			got, err := decodeNFOSummary(input)
			if err != domain.ErrDatabase || !reflect.DeepEqual(got, domain.NFOValidationSummary{}) {
				t.Fatal("invalid persisted data must return only a zero summary and ErrDatabase")
			}
			if strings.Contains(err.Error(), privateMarker) {
				t.Fatal("decoder disclosed private payload")
			}
			if !bytes.Equal(input, original) {
				t.Fatal("decoder changed invalid persisted bytes")
			}
		})
	}
}
