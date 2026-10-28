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
)

func nfoExportJobData() map[string]any {
	job := validJobsCLIJob()
	job["kind"], job["libraryId"] = "nfo_export", nfoWriteTestID2
	return job
}

func nfoExportStateData(cursor any) map[string]any {
	return map[string]any{"jobId": jobsTestID, "libraryId": nfoWriteTestID2, "cursorItemId": cursor, "childJobId": nil, "chunks": 6, "written": 5, "unchanged": 4, "skippedMissing": 3, "skippedInvalid": 2, "skippedOther": 1, "failed": 7, "updatedAt": "2026-10-01T01:02:03Z"}
}

func TestNFOExportCLIBuildsFixedRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, key string
		args                    []string
		body                    map[string]any
		status                  int
		data                    map[string]any
		replay                  bool
		want                    string
	}{
		{"export", "POST", "/api/v1/libraries/" + nfoWriteTestID2 + "/nfo/export", "k-1", []string{"export", "--library", nfoWriteTestID2, "--key", "k-1"},
			map[string]any{"priority": "manual"}, 202, nfoExportJobData(), false, "job " + jobsTestID + " queued"},
		{"export background replay", "POST", "/api/v1/libraries/" + nfoWriteTestID2 + "/nfo/export", "k-2", []string{"export", "--library", nfoWriteTestID2, "--priority", "background", "--key", "k-2"},
			map[string]any{"priority": "background"}, 200, nfoExportJobData(), true, "(replayed)"},
		{"status", "GET", "/api/v1/jobs/" + jobsTestID + "/nfo-export", "", []string{"export-status", "--job", jobsTestID},
			nil, 200, nfoExportStateData(jobsTestID), false, "written 5 unchanged 4 skipped-missing 3 skipped-invalid 2 skipped-other 1 failed 7"},
		{"status without cursor", "GET", "/api/v1/jobs/" + jobsTestID + "/nfo-export", "", []string{"export-status", "--job", jobsTestID},
			nil, 200, nfoExportStateData(nil), false, "cursor none"},
	} {
		for _, asJSON := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					body, _ := io.ReadAll(r.Body)
					var decoded map[string]any
					if tc.body != nil {
						_ = json.Unmarshal(body, &decoded)
					}
					if r.Method != tc.method || r.URL.Path != tc.path || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 43) ||
						r.Header.Get("Idempotency-Key") != tc.key || !reflect.DeepEqual(decoded, tc.body) || (tc.body == nil) != (len(body) == 0) {
						t.Errorf("wrong request: %s %s %s", r.Method, r.URL, body)
					}
					if tc.replay {
						w.Header().Set("Idempotency-Replayed", "true")
					}
					w.WriteHeader(tc.status)
					data := map[string]any{}
					for k, v := range tc.data {
						data[k] = v
					}
					data["rawXML"] = "private-payload"
					_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "token": "private-payload"})
				}))
				defer server.Close()
				args := append(append([]string{}, tc.args...), "--url", server.URL, "--token-stdin")
				if asJSON {
					args = append(args, "--json")
				}
				var out, diagnostics bytes.Buffer
				status := runNFOCLI(context.Background(), args, strings.NewReader(strings.Repeat("a", 43)+"\n"), &out, &diagnostics)
				if status != 0 || calls != 1 || diagnostics.Len() != 0 || strings.Contains(out.String(), "private-payload") || strings.Contains(out.String(), strings.Repeat("a", 43)) {
					t.Fatalf("status=%d calls=%d out=%s err=%s", status, calls, out.String(), diagnostics.String())
				}
				if asJSON {
					var printed struct {
						Data     map[string]any `json:"data"`
						Replayed *bool          `json:"replayed"`
					}
					if json.Unmarshal(out.Bytes(), &printed) != nil || len(printed.Data) == 0 || (tc.method == "POST") != (printed.Replayed != nil) || printed.Replayed != nil && *printed.Replayed != tc.replay {
						t.Fatalf("json output: %s", out.String())
					}
					if tc.method == "GET" {
						if _, ok := printed.Data["cursorItemId"]; !ok || len(printed.Data) != 12 {
							t.Fatalf("status fields: %s", out.String())
						}
					}
				} else if !strings.Contains(out.String(), tc.want) {
					t.Fatalf("summary: %s", out.String())
				}
			})
		}
	}
}

func TestNFOExportCLIRejectsBadArgumentsBeforeCredentials(t *testing.T) {
	export := []string{"export", "--library", nfoWriteTestID2, "--key", "k", "--token-stdin"}
	status := []string{"export-status", "--job", jobsTestID, "--token-stdin"}
	without := func(args []string, drop string) []string {
		var out []string
		for i := 0; i < len(args); i++ {
			if args[i] == drop {
				if drop != "--token-stdin" {
					i++
				}
				continue
			}
			out = append(out, args[i])
		}
		return out
	}
	for _, args := range [][]string{
		{"export"}, {"export-status"},
		without(export, "--library"), without(export, "--key"), without(export, "--token-stdin"),
		append(append([]string{}, export...), "--priority", "urgent"),
		append(append([]string{}, export...), "--job", jobsTestID),
		append(append([]string{}, export...), "extra"),
		append(without(export, "--key"), "--key", "bad key"),
		append(without(export, "--library"), "--library", "x"),
		append(append([]string{}, export...), "--url", "http://example.com/path"),
		without(status, "--job"), without(status, "--token-stdin"),
		append(without(status, "--job"), "--job", "x"),
		append(append([]string{}, status...), "--key", "k"),
		append(append([]string{}, status...), "--library", nfoWriteTestID2),
	} {
		var out, diagnostics bytes.Buffer
		if code := runNFOCLI(context.Background(), args, unreadProbeToken{t}, &out, &diagnostics); code != 2 || out.Len() != 0 || !strings.HasPrefix(diagnostics.String(), "usage:") {
			t.Fatalf("%q: status=%d %s", args, code, diagnostics.String())
		}
	}
}

func TestNFOExportCLIReportsStableErrorCodes(t *testing.T) {
	jobPayload := func(mutate func(map[string]any)) string {
		data := nfoExportJobData()
		mutate(data)
		raw, _ := json.Marshal(map[string]any{"data": data})
		return string(raw)
	}
	bad := func(mutate func(map[string]any)) map[string]any {
		state := nfoExportStateData(nil)
		mutate(state)
		return state
	}
	for _, tc := range []struct {
		args    []string
		status  int
		payload string
		want    string
	}{
		{[]string{"export", "--library", nfoWriteTestID2, "--key", "k"}, 409, `{"error":{"code":"conflict","message":"private /root path"}}`, "nfo_request_rejected (HTTP 409 conflict)"},
		{[]string{"export", "--library", nfoWriteTestID2, "--key", "k"}, 409, `{"error":{"code":"job_busy"}}`, "nfo_request_rejected (HTTP 409 job_busy)"},
		{[]string{"export", "--library", nfoWriteTestID2, "--key", "k"}, 502, `<html>private</html>`, "nfo_request_rejected (HTTP 502)"},
		{[]string{"export", "--library", nfoWriteTestID2, "--key", "k"}, 202, `{"data":{}}`, "nfo_response_invalid"},
		{[]string{"export", "--library", nfoWriteTestID2, "--key", "k"}, 202, jobPayload(func(m map[string]any) { m["kind"] = "nfo_write" }), "nfo_response_invalid"},
		{[]string{"export", "--library", nfoWriteTestID2, "--key", "k"}, 202, jobPayload(func(m map[string]any) { m["libraryId"] = jobsTestID }), "nfo_response_invalid"},
		{[]string{"export", "--library", nfoWriteTestID2, "--key", "k"}, 201, jobPayload(func(map[string]any) {}), "nfo_request_rejected (HTTP 201)"},
		{[]string{"export-status", "--job", jobsTestID}, 404, `{"error":{"code":"not_found"}}`, "nfo_request_rejected (HTTP 404 not_found)"},
		{[]string{"export-status", "--job", jobsTestID}, 200, `{"data":{"jobId":"` + jobsTestID + `"}}`, "nfo_response_invalid"},
		{[]string{"export-status", "--job", jobsTestID}, 200, `{"data":` + mustJSON(bad(func(m map[string]any) { m["jobId"] = nfoWriteTestID2 })) + `}`, "nfo_response_invalid"},
		{[]string{"export-status", "--job", jobsTestID}, 200, `{"data":` + mustJSON(bad(func(m map[string]any) { m["written"] = -1 })) + `}`, "nfo_response_invalid"},
		{[]string{"export-status", "--job", jobsTestID}, 200, `{"data":` + mustJSON(bad(func(m map[string]any) { m["cursorItemId"] = "x" })) + `}`, "nfo_response_invalid"},
		{[]string{"export-status", "--job", jobsTestID}, 200, `{"data":` + mustJSON(bad(func(m map[string]any) { delete(m, "childJobId") })) + `}`, "nfo_response_invalid"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.payload)
		}))
		var out, diagnostics bytes.Buffer
		args := append(append([]string{}, tc.args...), "--url", server.URL, "--token-stdin")
		code := runNFOCLI(context.Background(), args, strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics)
		server.Close()
		if code != 1 || out.Len() != 0 || strings.TrimSpace(diagnostics.String()) != tc.want || strings.Contains(diagnostics.String(), "private") {
			t.Fatalf("%v %d %s: status=%d %q", tc.args, tc.status, tc.payload, code, diagnostics.String())
		}
	}
}
