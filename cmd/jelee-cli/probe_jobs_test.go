package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func validCLIProbeSummary() map[string]any {
	return map[string]any{"jobId": jobsTestID, "libraryId": jobsTestID, "enabled": true, "scope": "incremental", "phase": "done", "processed": 6, "hits": 1, "negativeHits": 1, "succeeded": 1, "failed": 1, "changed": 1, "unavailable": 1}
}

func TestProbeCLIUsesFixedAuthenticatedRoutesAndOptInBody(t *testing.T) {
	for _, tc := range []struct {
		command, method, path string
		extra                 []string
		body                  map[string]any
	}{
		{"scan", "POST", "/api/v1/libraries/" + jobsTestID + "/scan", []string{"--probe"}, map[string]any{"priority": "manual", "probe": true}},
		{"scan", "POST", "/api/v1/libraries/" + jobsTestID + "/scan", nil, map[string]any{"priority": "manual"}},
		{"scan", "POST", "/api/v1/libraries/" + jobsTestID + "/scan", []string{"--ignore", "jeleeignore", "--ignore-case", "sensitive"}, map[string]any{"priority": "manual", "ignore": map[string]any{"mode": "jeleeignore", "caseMode": "sensitive"}}},
		{"scan", "POST", "/api/v1/libraries/" + jobsTestID + "/scan", []string{"--ignore", "jeleeignore-legacy-v1", "--ignore-case", "sensitive"}, map[string]any{"priority": "manual", "ignore": map[string]any{"mode": "jeleeignore-legacy-v1", "caseMode": "sensitive"}}},
		{"scan", "POST", "/api/v1/libraries/" + jobsTestID + "/scan", []string{"--nfo", "--probe", "--ignore", "jeleeignore-legacy-v1", "--ignore-case", "ascii-insensitive"}, map[string]any{"priority": "manual", "nfo": true, "probe": true, "ignore": map[string]any{"mode": "jeleeignore-legacy-v1", "caseMode": "ascii-insensitive"}}}, {"probe-rebuild-library", "POST", "/api/v1/libraries/" + jobsTestID + "/probe/rebuild", []string{"--priority", "background"}, map[string]any{"priority": "background"}},
		{"probe-rebuild-item", "POST", "/api/v1/items/" + jobsTestID + "/probe/rebuild", nil, map[string]any{"priority": "manual"}},
		{"probe", "GET", "/api/v1/jobs/" + jobsTestID + "/probe", nil, nil},
	} {
		t.Run(tc.command+strings.Join(tc.extra, ""), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 43) {
					t.Error("incorrect request or token source")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if tc.method == "POST" {
					var decoded map[string]any
					if json.Unmarshal(body, &decoded) != nil || !reflect.DeepEqual(decoded, tc.body) || r.Header.Get("Idempotency-Key") != "probe-1" {
						t.Errorf("incorrect bounded enqueue body: %s", body)
					}
					w.WriteHeader(202)
					_, _ = w.Write(jobsCLIJSON(t, validJobsCLIJob()))
				} else {
					if len(body) != 0 || r.Header.Get("Idempotency-Key") != "" {
						t.Error("summary carried mutation data")
					}
					_, _ = w.Write(jobsCLIJSON(t, validCLIProbeSummary()))
				}
			}))
			defer server.Close()
			args := []string{tc.command, "--id", jobsTestID, "--url", server.URL, "--token-stdin"}
			if tc.method == "POST" {
				args = append(args, "--key", "probe-1")
			}
			args = append(args, tc.extra...)
			var out, errs bytes.Buffer
			if status := runJobsCLI(context.Background(), args, strings.NewReader(strings.Repeat("a", 43)+"\n"), &out, &errs); status != 0 || calls != 1 || errs.Len() != 0 || strings.Contains(out.String(), strings.Repeat("a", 43)) {
				t.Fatalf("status %d, calls %d, errors %s", status, calls, &errs)
			}
		})
	}
}

type unreadProbeToken struct{ t *testing.T }

func (r unreadProbeToken) Read([]byte) (int, error) {
	r.t.Fatal("invalid CLI options consumed credentials")
	return 0, io.EOF
}

func TestProbeCLIRejectsInvalidOptionsBeforeReadingCredentials(t *testing.T) {
	for _, args := range [][]string{
		{"probe", "--token-stdin"}, {"probe", "--id", "bad", "--token-stdin"},
		{"probe", "--id", jobsTestID, "--probe", "--token-stdin"}, {"probe", "--id", jobsTestID, "--key", "arbitrary", "--token-stdin"},
		{"probe-rebuild-library", "--id", jobsTestID, "--token-stdin"},
		{"probe-rebuild-item", "--key", "k", "--token-stdin"},
		{"probe-rebuild-item", "--id", jobsTestID, "--key", "k", "--probe", "--token-stdin"},
		{"probe-rebuild-library", "--id", jobsTestID, "--key", "bad key", "--token-stdin"},
		{"probe-rebuild-library", "--id", jobsTestID, "--key", "k", "--priority", "urgent", "--token-stdin"},
		{"scan", "--id", jobsTestID, "--key", "k", "--probe=not-bool", "--token-stdin"},
		{"probe", "--id", jobsTestID}, {"probe", "--id", jobsTestID, "--token-stdin", "extra"},
		{"probe", "--id", jobsTestID, "--url", "http://example.com", "--token-stdin"},
	} {
		var out, errs bytes.Buffer
		if status := runJobsCLI(context.Background(), args, unreadProbeToken{t}, &out, &errs); status != 2 || out.Len() != 0 || !strings.HasPrefix(errs.String(), "usage:") {
			t.Fatalf("invalid args accepted: %v => %d", args, status)
		}
	}
}

func TestProbeCLISummaryRequiresEveryPublicFieldAndExactTypes(t *testing.T) {
	for name := range validCLIProbeSummary() {
		for _, remove := range []bool{true, false} {
			value := validCLIProbeSummary()
			if remove {
				delete(value, name)
			} else {
				value[name] = nil
			}
			rejectJobsCLIReply(t, "probe", jobsCLIJSON(t, value))
		}
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"jobId", "private-path"}, {"libraryId", "not-uuid"}, {"enabled", "true"}, {"scope", "unknown"}, {"scope", ""}, {"phase", "succeeded"},
		{"processed", -1}, {"processed", 500001}, {"processed", 5}, {"processed", "6"}, {"hits", -1}, {"negativeHits", -1}, {"succeeded", -1}, {"failed", -1}, {"changed", -1}, {"unavailable", -1},
		{"hits", 500001}, {"hits", 9223372036854775807}, {"hits", 0.5}, {"targetItemId", nil}, {"targetItemId", "private-path"}, {"errorCode", nil}, {"errorCode", "private failure"},
	} {
		value := validCLIProbeSummary()
		value[tc.name] = tc.value
		rejectJobsCLIReply(t, "probe", jobsCLIJSON(t, value))
	}
	for _, raw := range [][]byte{[]byte(`{}`), []byte(`{"data":null}`), []byte(`{"data":[]}`), []byte(`{"data":{},"data":{}}`)} {
		rejectJobsCLIReply(t, "probe", raw)
	}
	value := validCLIProbeSummary()
	encoded := jobsCLIJSON(t, value)
	duplicate := bytes.Replace(encoded, []byte(`"hits":1`), []byte(`"hits":1,"hits":1`), 1)
	rejectJobsCLIReply(t, "probe", duplicate)
}

func TestProbeCLISummaryDropsPrivateAndCaseAliasFields(t *testing.T) {
	value := validCLIProbeSummary()
	value["rootPath"], value["toolIdentity"], value["metadata"], value["leaseOwner"], value["Processed"] = "private-root", "private-tool", map[string]any{"filename": "private-file"}, "private-owner", 999999
	exit, out, errs := jobsCLIReply(t, "probe", jobsCLIJSON(t, value), 200)
	if exit != 0 || errs != "" || strings.Contains(out, "private") || strings.Contains(out, "999999") {
		t.Fatalf("public response projection failed: %d %s %s", exit, out, errs)
	}
	var result struct {
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal([]byte(out), &result) != nil {
		t.Fatal("output is invalid JSON")
	}
	if result.Data["processed"] != float64(6) || len(result.Data) != 12 {
		t.Fatalf("unexpected summary keys: %v", result)
	}
}

func TestProbeCLISummaryValidStatesScopesAndFixedErrors(t *testing.T) {
	for _, phase := range []string{domain.ProbeSummaryWaitingScan, domain.ProbeSummaryRunning, domain.ProbeSummaryDone, domain.ProbeSummaryCancelled} {
		value := validCLIProbeSummary()
		value["phase"] = phase
		if exit, _, errs := jobsCLIReply(t, "probe", jobsCLIJSON(t, value), 200); exit != 0 || errs != "" {
			t.Fatalf("valid phase rejected: %s", phase)
		}
	}
	for _, code := range []string{string(domain.ProbePhaseRuntimeUnavailable), string(domain.ProbePhaseCapacity), string(domain.ProbePhaseInvalidated), string(domain.ProbePhaseIdentityMismatch), "scan_unavailable", "scan_io", "scan_limit", "scan_failed", "job_timeout", "job_attempts_exhausted"} {
		value := validCLIProbeSummary()
		value["phase"], value["errorCode"] = domain.ProbeSummaryAborted, code
		if exit, out, errs := jobsCLIReply(t, "probe", jobsCLIJSON(t, value), 200); exit != 0 || errs != "" || !strings.Contains(out, code) {
			t.Fatalf("fixed abort code rejected: %s", code)
		}
	}
	value := validCLIProbeSummary()
	value["phase"] = domain.ProbeSummaryAborted
	rejectJobsCLIReply(t, "probe", jobsCLIJSON(t, value))
	value["errorCode"] = "private-path"
	rejectJobsCLIReply(t, "probe", jobsCLIJSON(t, value))
	for _, scope := range []string{domain.ProbeScopeLibraryRebuild, domain.ProbeScopeItemRebuild} {
		value := validCLIProbeSummary()
		value["scope"] = scope
		if scope == domain.ProbeScopeItemRebuild {
			value["targetItemId"] = jobsTestID
		} else {
			value["processed"], value["hits"], value["negativeHits"], value["succeeded"], value["failed"], value["changed"], value["unavailable"] = 500000, 500000, 0, 0, 0, 0, 0
		}
		if exit, _, errs := jobsCLIReply(t, "probe", jobsCLIJSON(t, value), 200); exit != 0 || errs != "" {
			t.Fatalf("valid scope/boundary rejected: %s", scope)
		}
	}
	disabled := map[string]any{"jobId": jobsTestID, "libraryId": jobsTestID, "enabled": false, "phase": "disabled", "processed": 0, "hits": 0, "negativeHits": 0, "succeeded": 0, "failed": 0, "changed": 0, "unavailable": 0}
	if exit, _, errs := jobsCLIReply(t, "probe", jobsCLIJSON(t, disabled), 200); exit != 0 || errs != "" {
		t.Fatal("valid scan-only summary rejected")
	}
	for key, value := range map[string]any{"scope": "incremental", "targetItemId": jobsTestID, "phase": "done", "errorCode": "scan_failed", "processed": 1} {
		copy := make(map[string]any, len(disabled))
		for k, v := range disabled {
			copy[k] = v
		}
		copy[key] = value
		rejectJobsCLIReply(t, "probe", jobsCLIJSON(t, copy))
	}
}

func TestProbeCLIServiceErrorsNeverExposeRawResponse(t *testing.T) {
	for _, command := range []string{"probe", "probe-rebuild-library", "probe-rebuild-item"} {
		for _, status := range []int{401, 403, 409, 503} {
			exit, out, errs := jobsCLIReply(t, command, []byte(`{"error":{"code":"private-path token=secret"}}`), status)
			if exit != 1 || out != "" || !strings.HasPrefix(errs, "jobs_request_rejected (HTTP ") || strings.Contains(errs, "secret") {
				t.Fatal("service failure leaked content")
			}
		}
	}
}

func TestIgnoreCLIRejectsIncompleteIntentBeforeCredentials(t *testing.T) {
	for _, extra := range [][]string{{"--ignore", "jeleeignore"}, {"--ignore-case", "sensitive"}, {"--ignore", "legacy", "--ignore-case", "sensitive"}, {"--ignore", "jeleeignore", "--ignore-case", "fold"}} {
		args := append([]string{"scan", "--id", jobsTestID, "--key", "one", "--token-stdin"}, extra...)
		var out, errs bytes.Buffer
		if status := runJobsCLI(context.Background(), args, unreadProbeToken{t}, &out, &errs); status != 2 || out.Len() != 0 {
			t.Fatal("invalid ignore intent accepted", status)
		}
	}
}
