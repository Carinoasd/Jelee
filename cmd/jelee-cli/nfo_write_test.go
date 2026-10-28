package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const nfoWriteTestID2 = "22222222-2222-4222-8222-222222222222"

var nfoWriteTestDigest = strings.Repeat("ab", 32)

func nfoWritePreparationData() map[string]any {
	return map[string]any{"id": nfoWriteTestID2, "itemId": jobsTestID, "libraryId": jobsTestID, "revision": 4, "fields": []string{"title", "plot"},
		"originalBytes": 33, "replacementBytes": 35, "originalSha256": nfoWriteTestDigest, "replacementSha256": nfoWriteTestDigest,
		"createdAt": "2026-10-01T01:02:03Z", "expiresAt": "2026-10-02T01:02:03Z"}
}

func nfoWriteJobData() map[string]any {
	job := validJobsCLIJob()
	job["kind"] = "nfo_write"
	return job
}

func TestNFOWriteCLIBuildsFixedRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, key string
		args                    []string
		body                    map[string]any
		status                  int
		data                    map[string]any
		replay                  bool
		want                    string
	}{
		{"enable", "PUT", "/api/v1/libraries/" + jobsTestID + "/nfo/write-policy", "", []string{"write-policy", "--library", jobsTestID, "--enable"},
			map[string]any{"enabled": true}, 200, map[string]any{"libraryId": jobsTestID, "mode": "read-only", "generation": 2, "writeEnabled": true}, false, "NFO writes enabled"},
		{"disable", "PUT", "/api/v1/libraries/" + jobsTestID + "/nfo/write-policy", "", []string{"write-policy", "--library", jobsTestID, "--disable"},
			map[string]any{"enabled": false}, 200, map[string]any{"libraryId": jobsTestID, "mode": "read-only", "generation": 2, "writeEnabled": false}, false, "NFO writes disabled"},
		{"prepare", "POST", "/api/v1/items/" + jobsTestID + "/nfo/write-preparations", "k-1", []string{"write", "prepare", "--item", jobsTestID, "--revision", "4", "--set", "title=A=B", "--set", "plot=", "--key", "k-1"},
			map[string]any{"revision": float64(4), "edits": []any{map[string]any{"field": "title", "value": "A=B"}, map[string]any{"field": "plot", "value": ""}}, "backups": float64(3)}, 201, nfoWritePreparationData(), false, "prepared " + nfoWriteTestID2},
		{"prepare options", "POST", "/api/v1/items/" + jobsTestID + "/nfo/write-preparations", "k-2", []string{"write", "prepare", "--item", jobsTestID, "--revision", "4", "--set", "title=A", "--set", "plot=B", "--backups", "0", "--bom", "omit", "--key", "k-2"},
			map[string]any{"revision": float64(4), "edits": []any{map[string]any{"field": "title", "value": "A"}, map[string]any{"field": "plot", "value": "B"}}, "backups": float64(0), "bom": "omit"}, 200, nfoWritePreparationData(), true, "(replayed)"},
		{"submit", "POST", "/api/v1/libraries/" + jobsTestID + "/nfo/writes", "k-3", []string{"write", "submit", "--library", jobsTestID, "--preparation", nfoWriteTestID2, "--preparation", jobsTestID, "--priority", "background", "--key", "k-3"},
			map[string]any{"preparations": []any{nfoWriteTestID2, jobsTestID}, "priority": "background"}, 202, nfoWriteJobData(), false, "job " + jobsTestID + " queued"},
	} {
		for _, asJSON := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					body, _ := io.ReadAll(r.Body)
					var decoded map[string]any
					if r.Method != tc.method || r.URL.Path != tc.path || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 43) ||
						r.Header.Get("Idempotency-Key") != tc.key || r.Header.Get("Content-Type") != "application/json" ||
						json.Unmarshal(body, &decoded) != nil || !reflect.DeepEqual(decoded, tc.body) {
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
					if json.Unmarshal(out.Bytes(), &printed) != nil || len(printed.Data) == 0 || (tc.name != "enable" && tc.name != "disable") && (printed.Replayed == nil || *printed.Replayed != tc.replay) {
						t.Fatalf("json output: %s", out.String())
					}
				} else if !strings.Contains(out.String(), tc.want) || strings.Count(out.String(), "\n") < 1 {
					t.Fatalf("summary: %s", out.String())
				}
			})
		}
	}
}

func TestNFOWriteCLIRejectsBadArgumentsBeforeCredentials(t *testing.T) {
	prep := []string{"write", "prepare", "--item", jobsTestID, "--revision", "1", "--set", "title=x", "--key", "k", "--token-stdin"}
	sub := []string{"write", "submit", "--library", jobsTestID, "--preparation", nfoWriteTestID2, "--key", "k", "--token-stdin"}
	without := func(args []string, drop ...string) []string {
		var out []string
		for i := 0; i < len(args); i++ {
			skip := false
			for _, d := range drop {
				if args[i] == d {
					skip = true
					if args[i] != "--token-stdin" {
						i++
					}
				}
			}
			if !skip {
				out = append(out, args[i])
			}
		}
		return out
	}
	many := []string{"write", "submit", "--library", jobsTestID, "--key", "k", "--token-stdin"}
	for i := 0; i < 101; i++ {
		many = append(many, "--preparation", fmt.Sprintf("%08x-1111-4111-8111-111111111111", i+1))
	}
	for _, args := range [][]string{
		{}, {"write"}, {"write", "apply"}, {"write-policy"},
		{"write-policy", "--library", jobsTestID, "--token-stdin"},
		{"write-policy", "--library", jobsTestID, "--enable", "--disable", "--token-stdin"},
		{"write-policy", "--library", "x", "--enable", "--token-stdin"},
		{"write-policy", "--library", jobsTestID, "--enable"},
		{"write-policy", "--library", jobsTestID, "--enable", "--token-stdin", "--url", "http://example.com"},
		{"write-policy", "--library", jobsTestID, "--enable", "--token-stdin", "extra"},
		without(prep, "--item"), without(prep, "--revision"), without(prep, "--set"), without(prep, "--key"), without(prep, "--token-stdin"),
		append(without(prep, "--revision"), "--revision", "0"),
		append(append([]string{}, prep...), "--set", "title=again"),
		append(append([]string{}, prep...), "--set", "uniqueid=1"),
		append(append([]string{}, prep...), "--set", "novalue"),
		append(append([]string{}, prep...), "--backups", "17"),
		append(append([]string{}, prep...), "--bom", "always"),
		append(append([]string{}, prep...), "--create-missing"),
		append(without(prep, "--key"), "--key", "bad key"),
		without(sub, "--library"), without(sub, "--preparation"), without(sub, "--key"),
		append(append([]string{}, sub...), "--preparation", nfoWriteTestID2),
		append(append([]string{}, sub...), "--preparation", "not-a-uuid"),
		append(append([]string{}, sub...), "--priority", "urgent"),
		many,
	} {
		var out, diagnostics bytes.Buffer
		if status := runNFOCLI(context.Background(), args, unreadProbeToken{t}, &out, &diagnostics); status != 2 || out.Len() != 0 || !strings.HasPrefix(diagnostics.String(), "usage:") {
			t.Fatalf("%q: status=%d %s", args, status, diagnostics.String())
		}
	}
}

func TestNFOWriteCLIReportsStableErrorCodes(t *testing.T) {
	args := func(url string) []string {
		return []string{"write", "submit", "--library", jobsTestID, "--preparation", nfoWriteTestID2, "--key", "k", "--url", url, "--token-stdin"}
	}
	for _, tc := range []struct {
		status  int
		payload string
		want    string
	}{
		{409, `{"error":{"code":"nfo_write_disabled","message":"private /root path"}}`, "nfo_request_rejected (HTTP 409 nfo_write_disabled)"},
		{403, `{"error":{"code":"forbidden"}}`, "nfo_request_rejected (HTTP 403 forbidden)"},
		{502, `<html>private</html>`, "nfo_request_rejected (HTTP 502)"},
		{500, `{"error":{"code":"Bad Code /private"}}`, "nfo_request_rejected (HTTP 500)"},
		{202, `{"data":{}}`, "nfo_response_invalid"},
		{202, `not json`, "nfo_response_invalid"},
		{200, `{"data":{"id":"` + jobsTestID + `","libraryId":"` + jobsTestID + `","kind":"inventory_scan","state":"queued","priority":"manual","attempts":0,"cancelRequested":false,"files":0,"directories":0,"skipped":0,"bytes":0,"missing":0,"reviewRequired":false,"createdAt":"2026-10-01T01:02:03Z"}}`, "nfo_response_invalid"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.payload)
		}))
		var out, diagnostics bytes.Buffer
		status := runNFOCLI(context.Background(), args(server.URL), strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics)
		server.Close()
		if status != 1 || out.Len() != 0 || strings.TrimSpace(diagnostics.String()) != tc.want || strings.Contains(diagnostics.String(), "private") {
			t.Fatalf("%d %s: status=%d %q", tc.status, tc.payload, status, diagnostics.String())
		}
	}
	// A prepare response for another item or other fields is not trusted.
	data := nfoWritePreparationData()
	data["fields"] = []string{"plot"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	status := runNFOCLI(context.Background(), []string{"write", "prepare", "--item", jobsTestID, "--revision", "4", "--set", "title=x", "--key", "k", "--url", server.URL, "--token-stdin"}, strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics)
	if status != 1 || out.Len() != 0 || strings.TrimSpace(diagnostics.String()) != "nfo_response_invalid" {
		t.Fatalf("mismatched response accepted: %d %q", status, diagnostics.String())
	}
}
