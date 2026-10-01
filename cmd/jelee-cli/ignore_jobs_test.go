package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func validCLIIgnoreReport() map[string]any {
	return map[string]any{"jobId": jobsTestID, "state": "succeeded", "enabled": true, "reviewRequired": false, "invalidated": false, "excludedFiles": 1, "excludedDirectories": 0, "unknown": 0,
		"entries": []any{map[string]any{"source": "scan", "rootId": jobsTestID, "path": "hidden.mkv", "kind": "video", "outcome": "excluded", "ruleDirectory": ".", "ruleLine": 2, "matchedPath": "hidden.mkv"}}}
}

func TestIgnoreCLIReportRouteAndPublicOutput(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/api/v1/jobs/"+jobsTestID+"/ignore" || r.URL.Query().Get("cursor") != "opaque_cursor-1" || r.URL.Query().Get("limit") != "1" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 43) || r.Header.Get("Idempotency-Key") != "" {
			t.Error("incorrect report request")
		}
		report := validCLIIgnoreReport()
		report["privateRoot"] = "/private/media"
		report["entries"].([]any)[0].(map[string]any)["Path"] = "/private/alias"
		report["nextCursor"] = "next_cursor"
		_, _ = w.Write(jobsCLIJSON(t, report))
	}))
	defer server.Close()
	var out, errs bytes.Buffer
	status := runJobsCLI(context.Background(), []string{"ignore", "--id", jobsTestID, "--url", server.URL, "--token-stdin", "--limit", "1", "--cursor", "opaque_cursor-1"}, strings.NewReader(strings.Repeat("a", 43)), &out, &errs)
	if status != 0 || calls != 1 || errs.Len() != 0 || strings.Contains(out.String(), "private") || !strings.Contains(out.String(), "hidden.mkv") || !strings.Contains(out.String(), "next_cursor") {
		t.Fatal("incorrect public report output", status, calls, &out, &errs)
	}
}

func TestIgnoreCLIReportRejectsInvalidResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"running", func(r map[string]any) { r["state"] = "running" }},
		{"negative", func(r map[string]any) { r["unknown"] = -1 }},
		{"null entries", func(r map[string]any) { r["entries"] = nil }},
		{"oversize page", func(r map[string]any) { r["entries"] = append(r["entries"].([]any), r["entries"].([]any)[0]) }},
		{"absolute path", func(r map[string]any) { r["entries"].([]any)[0].(map[string]any)["path"] = "/private/media" }},
		{"invalid provenance", func(r map[string]any) { r["entries"].([]any)[0].(map[string]any)["ruleLine"] = 0 }},
		{"invalid reason", func(r map[string]any) { r["entries"].([]any)[0].(map[string]any)["reason"] = "private error" }},
		{"null optional", func(r map[string]any) { r["nextCursor"] = nil }},
		{"cursor control", func(r map[string]any) { r["nextCursor"] = "bad\nvalue" }},
		{"cursor size", func(r map[string]any) { r["nextCursor"] = strings.Repeat("a", 4097) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := validCLIIgnoreReport()
			tc.mutate(report)
			if _, ok := decodeJobsCLIResponse(jobsCLIJSON(t, report), "ignore", 1); ok {
				t.Fatal("invalid report accepted")
			}
		})
	}
	raw, _ := json.Marshal(validCLIIgnoreReport())
	duplicate := strings.Replace(string(raw), `"path":"hidden.mkv"`, `"path":"hidden.mkv","path":"other.mkv"`, 1)
	if _, ok := decodeCLIIgnoreReport([]byte(duplicate), 1); ok {
		t.Fatal("duplicate nested field accepted")
	}
}

func TestIgnoreCLIReportRejectsFlagsBeforeCredentials(t *testing.T) {
	for _, extra := range [][]string{{"--limit", "0"}, {"--limit", "101"}, {"--cursor", "bad/value"}, {"--cursor", strings.Repeat("a", 4097)}, {"--state", "succeeded"}} {
		var out, errs bytes.Buffer
		args := append([]string{"ignore", "--id", jobsTestID, "--token-stdin"}, extra...)
		if status := runJobsCLI(context.Background(), args, unreadProbeToken{t}, &out, &errs); status != 2 || out.Len() != 0 {
			t.Fatal("invalid report flags accepted", status)
		}
	}
}
