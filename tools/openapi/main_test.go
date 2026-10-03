package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const committed = "../../" + defaultOutput

// TestCommittedOpenAPIIsCurrent is the staleness gate for G08.5/G49.3.
func TestCommittedOpenAPIIsCurrent(t *testing.T) {
	want, err := render()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("read %s: %v; run `go run ./tools/openapi` from the repository root", defaultOutput, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; run `go run ./tools/openapi` (make openapi) from the repository root and commit the result", defaultOutput)
	}
}

func TestOpenAPIRenderIsStableAndCheckModeDetectsDrift(t *testing.T) {
	first, err := render()
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		again, err := render()
		if err != nil || !bytes.Equal(first, again) {
			t.Fatal("specification rendering is not deterministic")
		}
	}
	var document map[string]any
	if err := json.Unmarshal(first, &document); err != nil || document["openapi"] != "3.1.0" || first[len(first)-1] != '\n' {
		t.Fatal("rendered specification is not a newline-terminated OpenAPI 3.1 document")
	}
	path := filepath.Join(t.TempDir(), "openapi.json")
	if code := run([]string{"-check", "-o", path}); code != 1 {
		t.Fatalf("missing output accepted: %d", code)
	}
	if code := run([]string{"-o", path}); code != 0 {
		t.Fatalf("write failed: %d", code)
	}
	if code := run([]string{"-check", "-o", path}); code != 0 {
		t.Fatalf("fresh output rejected: %d", code)
	}
	if err := os.WriteFile(path, append(first[:len(first)-1:len(first)-1], ' ', '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-check", "-o", path}); code != 1 {
		t.Fatalf("stale output accepted: %d", code)
	}
}
