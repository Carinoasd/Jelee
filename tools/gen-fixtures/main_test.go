//go:build jelee_fixture_tools

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixturePathAndChecksumBoundary(t *testing.T) {
	for _, path := range []string{"", "../a", "/a", "a/../b", "a//b", "a\\b", "C:a", "a\x00b"} {
		if relativePath(path) {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile("tool", []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("original"))
	if err := verifyFile(root, "tool", hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ path, hash string }{{"tool", strings.Repeat("0", 64)}, {"tool", "invalid"}, {"missing", hex.EncodeToString(hash[:])}, {"../outside", hex.EncodeToString(hash[:])}} {
		if err := verifyFile(root, item.path, item.hash); err == nil {
			t.Fatalf("accepted bad tool %q", item.path)
		}
	}
}

func TestFixtureWriteNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.nfo")
	before := []byte("original user file")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(path, []byte("replacement")); err == nil {
		t.Fatal("overwrote existing file")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source changed")
	}
	if err := makeImage(path); err == nil {
		t.Fatal("image replaced existing file")
	}
}

func TestFixtureManifestFailureIsExplicit(t *testing.T) {
	project := t.TempDir()
	if _, _, err := loadTool(project); err == nil || err.Error() != "fixture_manifest_missing" {
		t.Fatalf("missing manifest: %v", err)
	}
	if err := os.Mkdir(filepath.Join(project, "tools"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "tools", "manifest.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadTool(project); err == nil || err.Error() != "fixture_manifest_invalid" {
		t.Fatalf("bad manifest: %v", err)
	}
}

func fixtureProject(t *testing.T) string {
	t.Helper()
	if os.Getenv("JELEE_REQUIRE_MEDIA_TOOL_TESTS") != "true" {
		t.Skip("real media tools disabled; use JELEE_REQUIRE_MEDIA_TOOL_TESTS=true with bootstrap-media")
	}
	project, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadTool(project); err != nil {
		t.Fatalf("required pinned media tools unavailable: %v", err)
	}
	return project
}

func TestGenerateRealFixturesAndCancellation(t *testing.T) {
	project := fixtureProject(t)
	parent := filepath.Join(project, ".testfixtures")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel, err := os.CreateTemp(parent, "source-preservation-")
	if err != nil {
		t.Fatal(err)
	}
	sentinelName := sentinel.Name()
	t.Cleanup(func() { os.Remove(sentinelName) })
	if _, err := sentinel.WriteString("original content"); err != nil {
		t.Fatal(err)
	}
	sentinel.Close()
	before, err := os.ReadFile(sentinelName)
	if err != nil {
		t.Fatal(err)
	}
	output, err := generate(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(output) })
	raw, err := os.ReadFile(filepath.Join(output, "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest fixtureManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || len(manifest.Files) != 19 || manifest.ToolVersion == "" {
		t.Fatalf("bad fixture manifest: %#v", manifest)
	}
	for _, entry := range manifest.Files {
		data, err := os.ReadFile(filepath.Join(output, entry.Name))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if int64(len(data)) != entry.Bytes || hex.EncodeToString(hash[:]) != entry.SHA256 {
			t.Fatalf("hash mismatch: %s", entry.Name)
		}
	}
	after, err := os.ReadFile(sentinelName)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing source changed")
	}
	entriesBefore, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := generate(ctx, project); err == nil {
		t.Fatal("cancelled generation succeeded")
	}
	entriesAfter, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesBefore) != len(entriesAfter) {
		t.Fatal("failed generation leaked an output directory")
	}
	t.Logf("PASS: 16 small original fixtures, fixed arguments, checksums, source preservation and cancelled cleanup (%s)", manifest.Platform)
}
