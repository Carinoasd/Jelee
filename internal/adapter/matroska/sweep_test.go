//go:build !windows

package matroska

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const (
	sweepLive   = "00000000-0000-4000-8000-000000000001"
	sweepOrphan = "00000000-0000-4000-8000-000000000002"
	sweepFresh  = "0123456789abcdef0123456789abcdef"
	sweepIdle   = "fedcba9876543210fedcba9876543210"
)

func sweepEntry(t *testing.T, root, source, revision string, bytes int, used time.Time) {
	t.Helper()
	directory := filepath.Join(root, source, revision)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "t1.srt"), make([]byte, bytes), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(entry{Version: entryVersion, Subtitles: []entryTrack{{ID: 1, Format: "srt", File: "t1.srt"}}, Attachments: []entryFont{}, Bytes: int64(bytes)})
	name := filepath.Join(directory, entryFile)
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, used, used); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestSweeperRemovesOnlyOwnStaleEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sweepEntry(t, root, sweepLive, sweepFresh, 100, now)
	sweepEntry(t, root, sweepLive, sweepIdle, 200, now.Add(-40*24*time.Hour))
	sweepEntry(t, root, sweepOrphan, sweepFresh, 300, now)
	staleStage := filepath.Join(root, sweepLive, stagingPrefix+"old")
	freshStage := filepath.Join(root, sweepLive, stagingPrefix+"new")
	for _, dir := range []string{staleStage, freshStage} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(staleStage, old, old); err != nil {
		t.Fatal(err)
	}
	// Names this package never writes stay, inside a live source and at
	// the cache root.
	foreign := filepath.Join(root, sweepLive, "notes.txt")
	stranger := filepath.Join(root, "not-a-source")
	for _, name := range []string{foreign, stranger} {
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A link in the revision name space is removed as a link; its target
	// outside the cache is never entered.
	if err := os.Symlink(outside, filepath.Join(root, sweepLive, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")); err != nil {
		t.Fatal(err)
	}

	s, err := NewSweeper(Config{CacheRoot: root, MaxCacheBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if n, err := s.CountSources(ctx); err != nil || n != 2 {
		t.Fatal("count", n, err)
	}
	if names, err := s.ListSources(ctx, "", 1); err != nil || !slices.Equal(names, []string{sweepLive}) {
		t.Fatal("first page", names, err)
	}
	if names, err := s.ListSources(ctx, sweepLive, 10); err != nil || !slices.Equal(names, []string{sweepOrphan}) {
		t.Fatal("second page", names, err)
	}
	if names, err := s.ListSources(ctx, sweepOrphan, 10); err != nil || len(names) != 0 {
		t.Fatal("last page", names, err)
	}
	if _, err := s.ListSources(ctx, "../x", 10); !errors.Is(err, ErrInvalid) {
		t.Fatal("cursor outside the name space accepted", err)
	}

	live, err := s.SweepSource(ctx, sweepLive, true, now.Add(-30*24*time.Hour))
	if err != nil || live.Revisions != 3 || live.Orphaned != 0 || live.Failed != 0 || live.Bytes < 200 {
		t.Fatalf("live sweep %+v %v", live, err)
	}
	if !exists(filepath.Join(root, sweepLive, sweepFresh)) || exists(filepath.Join(root, sweepLive, sweepIdle)) || exists(staleStage) || !exists(freshStage) || exists(filepath.Join(root, sweepLive, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")) {
		t.Fatal("live source revisions")
	}
	if !exists(foreign) || !exists(stranger) || !exists(filepath.Join(outside, "keep")) {
		t.Fatal("a name this package does not write was removed")
	}
	orphan, err := s.SweepSource(ctx, sweepOrphan, false, now)
	if err != nil || orphan.Orphaned != 1 || orphan.Bytes < 300 || exists(filepath.Join(root, sweepOrphan)) {
		t.Fatalf("orphan sweep %+v %v", orphan, err)
	}
	if again, err := s.SweepSource(ctx, sweepOrphan, false, now); err != nil || again.Orphaned != 1 || again.Bytes != 0 {
		t.Fatalf("repeated orphan sweep %+v %v", again, err)
	}

	// Capacity: three entries of 400 KiB in a 1 MiB cache lose the least
	// recently used one.
	for i, revision := range []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"} {
		sweepEntry(t, root, sweepLive, revision, 400<<10, now.Add(time.Duration(i-10)*time.Minute))
	}
	capacity, err := s.EnforceCapacity(ctx)
	if err != nil || capacity.Capacity != 1 || capacity.Bytes != 400<<10 || exists(filepath.Join(root, sweepLive, "11111111111111111111111111111111")) || !exists(filepath.Join(root, sweepLive, "33333333333333333333333333333333")) {
		t.Fatalf("capacity %+v %v", capacity, err)
	}
	if again, err := s.EnforceCapacity(ctx); err != nil || again.Capacity != 0 {
		t.Fatalf("second capacity %+v %v", again, err)
	}
}

func TestSweeperRefusesUnsafeRoots(t *testing.T) {
	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Config{{CacheRoot: open}, {CacheRoot: "relative"}, {CacheRoot: filepath.Join(open, "missing")}, {CacheRoot: open + "/."}} {
		if _, err := NewSweeper(c); !errors.Is(err, ErrUnavailable) {
			t.Fatal("unsafe root accepted", c.CacheRoot, err)
		}
	}
	private := t.TempDir()
	if err := os.Chmod(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSweeper(Config{CacheRoot: private, MaxCacheBytes: 1}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("tiny bound accepted", err)
	}
	s, err := NewSweeper(Config{CacheRoot: private})
	if err != nil || s.config.MaxCacheBytes != DefaultMaxCacheBytes {
		t.Fatal("default bound", err)
	}
	if err := os.Remove(private); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CountSources(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("vanished root", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.SweepSource(ctx, sweepLive, true, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled sweep", err)
	}
}
