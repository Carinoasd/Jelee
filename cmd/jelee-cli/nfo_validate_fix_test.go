package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type nfoFixCall struct {
	method, path, key, body string
}

// nfoFixServer plays the service: the validate job is queued, then running,
// then in state final; the export POST answers exportStatus.
func nfoFixServer(t *testing.T, final string, exportStatus int) (*httptest.Server, *[]nfoFixCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []nfoFixCall
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, nfoFixCall{r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"), string(raw)})
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 43) {
			t.Error("missing bearer token")
		}
		job := validJobsCLIJob()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/nfo/validate"):
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/jobs/"+jobsTestID:
			polls++
			job["state"] = "running"
			if polls >= 2 {
				job["state"] = final
				if final == "failed" {
					job["errorCode"] = "scan_failed"
				}
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/nfo/export"):
			if exportStatus != http.StatusAccepted {
				w.WriteHeader(exportStatus)
				_, _ = w.Write([]byte(`{"error":{"code":"nfo_write_disabled","message":"private"}}`))
				return
			}
			job = nfoExportJobData()
			job["id"], job["libraryId"] = nfoWriteTestID2, jobsTestID
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": job})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestNFOValidateFixSubmitsValidationThenTheFixExport(t *testing.T) {
	old := nfoFixPollInterval
	nfoFixPollInterval = time.Millisecond
	t.Cleanup(func() { nfoFixPollInterval = old })
	server, calls := nfoFixServer(t, "succeeded", http.StatusAccepted)
	var out, diagnostics bytes.Buffer
	args := []string{"validate", "--library", jobsTestID, "--fix", "--key", "run-1", "--priority", "background", "--url", server.URL, "--token-stdin"}
	if code := runNFOCLI(context.Background(), args, strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics); code != 0 {
		t.Fatal(code, out.String(), diagnostics.String())
	}
	var printed struct {
		Data struct {
			Validate map[string]any `json:"validate"`
			Fix      map[string]any `json:"fix"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil || printed.Data.Validate["id"] != jobsTestID || printed.Data.Validate["state"] != "succeeded" || printed.Data.Fix["id"] != nfoWriteTestID2 || printed.Data.Fix["kind"] != "nfo_export" {
		t.Fatalf("output %s %v", out.String(), err)
	}
	want := []nfoFixCall{
		{"POST", "/api/v1/libraries/" + jobsTestID + "/nfo/validate", "run-1", `{"priority":"background"}`},
		{"GET", "/api/v1/jobs/" + jobsTestID, "", ""},
		{"GET", "/api/v1/jobs/" + jobsTestID, "", ""},
		{"POST", "/api/v1/libraries/" + jobsTestID + "/nfo/export", "run-1-fix", `{"mode":"fix","priority":"background"}`},
	}
	if len(*calls) != len(want) {
		t.Fatalf("calls %+v", *calls)
	}
	for i, call := range want {
		if (*calls)[i] != call {
			t.Fatalf("call %d: %+v, want %+v", i, (*calls)[i], call)
		}
	}
}

func TestNFOValidateFixDoesNotRepairAfterAValidationThatDidNotSucceed(t *testing.T) {
	old := nfoFixPollInterval
	nfoFixPollInterval = time.Millisecond
	t.Cleanup(func() { nfoFixPollInterval = old })
	for _, final := range []string{"failed", "cancelled"} {
		server, calls := nfoFixServer(t, final, http.StatusAccepted)
		var out, diagnostics bytes.Buffer
		args := []string{"validate", "--library", jobsTestID, "--fix", "--key", "run-1", "--url", server.URL, "--token-stdin"}
		if code := runNFOCLI(context.Background(), args, strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics); code != 1 || out.Len() != 0 || !strings.Contains(diagnostics.String(), "nfo_validate_not_succeeded (job "+jobsTestID+" "+final+")") {
			t.Fatal(final, code, out.String(), diagnostics.String())
		}
		for _, call := range *calls {
			if strings.HasSuffix(call.path, "/nfo/export") {
				t.Fatalf("%s: the fix was submitted after a validation that %s", final, final)
			}
		}
	}
}

func TestNFOValidateFixReportsARejectedFixAndTheValidationJob(t *testing.T) {
	old := nfoFixPollInterval
	nfoFixPollInterval = time.Millisecond
	t.Cleanup(func() { nfoFixPollInterval = old })
	server, _ := nfoFixServer(t, "succeeded", http.StatusConflict)
	var out, diagnostics bytes.Buffer
	args := []string{"validate", "--library", jobsTestID, "--fix", "--key", "run-1", "--url", server.URL, "--token-stdin"}
	if code := runNFOCLI(context.Background(), args, strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics); code != 1 || out.Len() != 0 ||
		!strings.Contains(diagnostics.String(), "nfo_request_rejected (HTTP 409 nfo_write_disabled)") || !strings.Contains(diagnostics.String(), "nfo_fix_not_submitted (validate job "+jobsTestID+")") || strings.Contains(diagnostics.String(), "private") {
		t.Fatal(code, out.String(), diagnostics.String())
	}
}

func TestNFOValidateFixStopsWhenCancelledWhileWaiting(t *testing.T) {
	old := nfoFixPollInterval
	nfoFixPollInterval = time.Hour
	t.Cleanup(func() { nfoFixPollInterval = old })
	server, _ := nfoFixServer(t, "succeeded", http.StatusAccepted)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var out, diagnostics bytes.Buffer
	args := []string{"validate", "--library", jobsTestID, "--fix", "--key", "run-1", "--url", server.URL, "--token-stdin"}
	if code := runNFOCLI(ctx, args, strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics); code == 0 || out.Len() != 0 {
		t.Fatal(code, out.String(), diagnostics.String())
	}
}

func TestNFOValidateFixRejectsInvalidOptionsBeforeReadingCredentials(t *testing.T) {
	long := strings.Repeat("k", 125)
	for _, args := range [][]string{
		{"validate", "--root", "/r", "--file", "a.nfo", "--fix"},
		{"validate", "--library", jobsTestID, "--fix", "--token-stdin"},
		{"validate", "--library", jobsTestID, "--fix", "--key", long, "--token-stdin"},
		{"validate", "--library", jobsTestID, "--fix=maybe", "--key", "k", "--token-stdin"},
		{"policy-get", "--library", jobsTestID, "--fix", "--token-stdin"},
	} {
		var out, diagnostics bytes.Buffer
		if code := runNFOCLI(context.Background(), args, unreadProbeToken{t}, &out, &diagnostics); code != 2 || !strings.HasPrefix(diagnostics.String(), "usage:") {
			t.Fatalf("%q: %d %s", args, code, diagnostics.String())
		}
	}
}
