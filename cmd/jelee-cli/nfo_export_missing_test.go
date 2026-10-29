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

func TestNFOExportCLIMissingSendsTheMode(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nfoExportJobData()})
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	args := []string{"export", "--library", nfoWriteTestID2, "--missing", "--key", "k", "--url", server.URL, "--token-stdin"}
	if code := runNFOCLI(context.Background(), args, strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics); code != 0 || diagnostics.Len() != 0 {
		t.Fatal(code, diagnostics.String())
	}
	if want := map[string]any{"priority": "manual", "mode": "missing"}; !reflect.DeepEqual(body, want) {
		t.Fatalf("body %v, want %v", body, want)
	}
	// --missing takes no value and belongs to export only.
	for _, bad := range [][]string{
		{"export", "--library", nfoWriteTestID2, "--missing=maybe", "--key", "k", "--token-stdin"},
		{"export-status", "--job", jobsTestID, "--missing", "--token-stdin"},
	} {
		var o, d bytes.Buffer
		if code := runNFOCLI(context.Background(), bad, unreadProbeToken{t}, &o, &d); code != 2 || !strings.HasPrefix(d.String(), "usage:") {
			t.Fatalf("%q: status=%d %s", bad, code, d.String())
		}
	}
}

func TestNFOExportCLIStatusShowsTheMissingCounter(t *testing.T) {
	state := nfoExportStateData(nil)
	state["mode"], state["skippedExisting"] = "missing", 9
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": state})
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	args := []string{"export-status", "--job", jobsTestID, "--url", server.URL, "--token-stdin"}
	if code := runNFOCLI(context.Background(), args, strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics); code != 0 || !strings.Contains(out.String(), "mode missing\nskipped-existing 9\n") {
		t.Fatal(code, out.String(), diagnostics.String())
	}
	state["mode"] = "other"
	out.Reset()
	if code := runNFOCLI(context.Background(), args, strings.NewReader(strings.Repeat("a", 43)), &out, &diagnostics); code != 1 || !strings.Contains(diagnostics.String(), "nfo_response_invalid") {
		t.Fatal("an unknown mode was accepted", code, diagnostics.String())
	}
}
