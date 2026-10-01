package scan

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

const testRootID = "8173c730-8a2d-4f8f-8984-76c7fe4efc71"

var _ app.InventoryScanner = New()

func writeScanFile(t *testing.T, root, relative string, data []byte) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func collect(t *testing.T, root, relative string) ([]domain.InventoryEntry, []string, int64) {
	t.Helper()
	var entries []domain.InventoryEntry
	var directories []string
	var skipped int64
	done := 0
	err := New().ScanDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: relative}, func(batch domain.ScanBatch) error {
		if len(batch.Entries)+len(batch.Directories) > domain.ScanBatchMaxEntries || done > 0 || batch.Skipped < 0 {
			t.Fatal("batch violated bounded ordered delivery")
		}
		if batch.Done {
			done++
			if len(batch.Entries)+len(batch.Directories) != 0 {
				t.Fatal("final batch should only contain completion counters")
			}
		} else if batch.Skipped != 0 {
			t.Fatal("skipped count emitted before completion")
		}
		for _, entry := range batch.Entries {
			if entry.ID != "" || entry.RootID != testRootID || path.Dir(entry.Path) != relative {
				t.Fatalf("entry identity or direct-child path is invalid: %#v", entry)
			}
		}
		for _, directory := range batch.Directories {
			if path.Dir(directory) != relative {
				t.Fatal("frontier contains a non-direct child")
			}
		}
		entries = append(entries, batch.Entries...)
		directories = append(directories, batch.Directories...)
		skipped += batch.Skipped
		return nil
	})
	if err != nil || done != 1 {
		t.Fatalf("scan error=%v completions=%d", err, done)
	}
	return entries, directories, skipped
}

func TestThousandFilesRemainBoundedAndOriginalBytesUnchanged(t *testing.T) {
	root := t.TempDir()
	payload := []byte("original media bytes\x00\xff\x01")
	for index := 0; index < 1000; index++ {
		writeScanFile(t, root, fmt.Sprintf("video-%04d.MKV", index), payload)
	}
	for index := 0; index < 17; index++ {
		writeScanFile(t, root, fmt.Sprintf("nested-%02d/unvisited.mp4", index), []byte("not recursed"))
	}
	before := sha256.Sum256(payload)
	entries, directories, skipped := collect(t, root, ".")
	if len(entries) != 1000 || len(directories) != 17 || skipped != 0 {
		t.Fatalf("files=%d directories=%d skipped=%d", len(entries), len(directories), skipped)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if seen[entry.Path] || entry.Kind != "video" || entry.Size != int64(len(payload)) {
			t.Fatal("duplicate entry or invalid metadata")
		}
		seen[entry.Path] = true
		name := filepath.Join(root, entry.Path)
		after, err := os.ReadFile(name)
		if err != nil || sha256.Sum256(after) != before {
			t.Fatal("source bytes changed")
		}
		info, err := os.Stat(name)
		if err != nil || info.ModTime().UnixNano() != entry.ModifiedUnixNano {
			t.Fatal("modification time was altered or not captured")
		}
	}
}

func TestUnicodeSpacesMetadataKindsAndImmediateChildren(t *testing.T) {
	root := t.TempDir()
	prefix := "电影 目录"
	cases := map[string]string{"影片 α😀.MKV": "video", "movie.NFO": "nfo", "poster.JPEG": "image", "素材.AVIF": "image", "sidecar.srt": "other", "movie.iso": "other", "no extension": "other"}
	stamp := time.Date(2024, 3, 4, 5, 6, 7, 123456700, time.UTC)
	for name := range cases {
		writeScanFile(t, root, prefix+"/"+name, []byte("unchanged"))
		if err := os.Chtimes(filepath.Join(root, prefix, name), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	writeScanFile(t, root, prefix+"/child/nested.mp4", []byte("not visited"))
	entries, directories, skipped := collect(t, root, prefix)
	if len(entries) != len(cases) || len(directories) != 1 || directories[0] != prefix+"/child" || skipped != 0 {
		t.Fatal("incorrect directory frontier or entry count")
	}
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(entry.Path)))
		if err != nil || entry.Kind != cases[path.Base(entry.Path)] || entry.Size != 9 || entry.ModifiedUnixNano != info.ModTime().UnixNano() {
			t.Fatal("unicode path or file metadata changed")
		}
	}
	collect(t, root, prefix+"/child")
}

func TestValidateRootAndInvalidPathErrorsContainNoPaths(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, root, "ordinary.mp4", []byte("unchanged"))
	if err := ValidateRoot(context.Background(), root+string(os.PathSeparator)); err != nil {
		t.Fatal(err)
	}
	for _, absolute := range []string{"", "relative", root + "\x00private", filepath.Join(root, "missing-private"), filepath.Join(root, "ordinary.mp4")} {
		err := ValidateRoot(context.Background(), absolute)
		if err != domain.ErrScanUnavailable {
			t.Fatalf("unsafe validation error: %v", err)
		}
	}
	for _, relative := range []string{"", "..", "../private", "/absolute", "a/../b", "a//b", "a/./b", "a/", "a\\b", "C:private", "a:b", "private\x00", "private\nname", "private\tname", "private\u0085name", string([]byte{0xff}), "missing-private", "ordinary.mp4"} {
		called := false
		err := New().ScanDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: relative}, func(domain.ScanBatch) error { called = true; return nil })
		if err != domain.ErrScanUnavailable || called || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "private") {
			t.Fatalf("invalid path accepted or disclosed: err=%v callback=%v", err, called)
		}
	}
	if err := New().ScanDirectory(context.Background(), domain.ScanDirectory{RootID: "invalid", RootPath: root, Path: "."}, func(domain.ScanBatch) error { return nil }); err != domain.ErrScanUnavailable {
		t.Fatal("invalid root identity accepted")
	}
	if err := New().ScanDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, nil); err != domain.ErrScanUnavailable {
		t.Fatal("nil callback accepted")
	}
	if err := ValidateRoot(nil, root); err != domain.ErrScanUnavailable {
		t.Fatal("nil context accepted")
	}
	if err := New().ScanDirectory(nil, domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, func(domain.ScanBatch) error { return nil }); err != domain.ErrScanUnavailable {
		t.Fatal("nil scan context accepted")
	}
}

func TestPathByteAndDepthLimitsOnRealDirectories(t *testing.T) {
	root := t.TempDir()
	prefix := strings.Join([]string{strings.Repeat("a", 200), strings.Repeat("b", 200), strings.Repeat("c", 200), strings.Repeat("d", 200)}, "/")
	name := strings.Repeat("e", domain.ScanPathMaxBytes-len(prefix)-1)
	writeScanFile(t, root, prefix+"/"+name, []byte("exact boundary"))
	entries, _, _ := collect(t, root, prefix)
	if len(entries) != 1 || len(entries[0].Path) != domain.ScanPathMaxBytes {
		t.Fatal("exact byte boundary was not accepted")
	}
	writeScanFile(t, root, prefix+"/"+name+"f", []byte("over boundary"))
	done := false
	err := New().ScanDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: prefix}, func(batch domain.ScanBatch) error { done = done || batch.Done; return nil })
	if err != domain.ErrScanLimit || done {
		t.Fatalf("overlong entry completed a directory: %v", err)
	}
	if err := New().ScanDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: strings.Repeat("x", 1025)}, func(domain.ScanBatch) error { return nil }); err != domain.ErrScanLimit {
		t.Fatal("overlong scan path accepted")
	}
	depthRoot := t.TempDir()
	deep := strings.TrimSuffix(strings.Repeat("d/", MaxDepth), "/")
	if err := os.MkdirAll(filepath.Join(depthRoot, filepath.FromSlash(deep)), 0700); err != nil {
		t.Fatal(err)
	}
	collect(t, depthRoot, deep)
	writeScanFile(t, depthRoot, deep+"/extra.mp4", []byte("depth overflow"))
	err = New().ScanDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: depthRoot, Path: deep}, func(domain.ScanBatch) error { return nil })
	if err != domain.ErrScanLimit {
		t.Fatal("entry depth overflow accepted")
	}
	if err := New().ScanDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: depthRoot, Path: deep + "/extra"}, func(domain.ScanBatch) error { return nil }); err != domain.ErrScanLimit {
		t.Fatal("scan depth overflow accepted")
	}
}

func TestVisibleSymlinksSkippedAndExplicitTraversalRejected(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeScanFile(t, root, "inside.mp4", []byte("inside"))
	writeScanFile(t, root, "real/nested.mp4", []byte("inside"))
	writeScanFile(t, outside, "secret.mp4", []byte("outside"))
	if err := os.Symlink(filepath.Join(outside, "secret.mp4"), filepath.Join(root, "external-file")); err != nil {
		t.Skipf("filesystem cannot create symlinks: %v", err)
	}
	for name, target := range map[string]string{"internal-file": "inside.mp4", "internal-directory": "real", "external-directory": outside, "broken": "does-not-exist"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	entries, directories, skipped := collect(t, root, ".")
	if len(entries) != 1 || entries[0].Path != "inside.mp4" || len(directories) != 1 || directories[0] != "real" || skipped != 5 {
		t.Fatalf("link handling incorrect: files=%d dirs=%d skipped=%d", len(entries), len(directories), skipped)
	}
	for _, relative := range []string{"internal-directory", "internal-directory/nested", "external-directory", "external-file", "broken"} {
		if err := New().ScanDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: relative}, func(domain.ScanBatch) error { t.Fatal("link path emitted metadata"); return nil }); err != domain.ErrScanUnavailable {
			t.Fatalf("link directory accepted: %v", err)
		}
	}
	linkRoot := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, linkRoot); err != nil {
		t.Fatal(err)
	}
	for _, absolute := range []string{linkRoot, linkRoot + string(os.PathSeparator)} {
		if err := ValidateRoot(context.Background(), absolute); err != domain.ErrScanUnavailable {
			t.Fatal("configured leaf symlink accepted")
		}
	}
}

func TestHardLinkMetadataIsReadOnlyAndIsNotClaimedAsExcluded(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	data := []byte("hard link source remains unchanged")
	writeScanFile(t, outside, "original.mp4", data)
	if err := os.Link(filepath.Join(outside, "original.mp4"), filepath.Join(root, "linked.mp4")); err != nil {
		t.Skipf("filesystem cannot create hardlinks: %v", err)
	}
	entries, _, skipped := collect(t, root, ".")
	if len(entries) != 1 || entries[0].Path != "linked.mp4" || skipped != 0 {
		t.Fatal("hardlink should be observed as regular metadata")
	}
	after, err := os.ReadFile(filepath.Join(outside, "original.mp4"))
	if err != nil || sha256.Sum256(after) != sha256.Sum256(data) {
		t.Fatal("hardlink source content changed")
	}
}

type scanTestInfo struct {
	name string
	mode fs.FileMode
	size int64
	time time.Time
}

func (i scanTestInfo) Name() string      { return i.name }
func (i scanTestInfo) Size() int64       { return i.size }
func (i scanTestInfo) Mode() fs.FileMode { return i.mode }
func (i scanTestInfo) ModTime() time.Time {
	if i.time.IsZero() {
		return time.Unix(0, 0)
	}
	return i.time
}
func (i scanTestInfo) IsDir() bool { return i.mode.IsDir() }
func (i scanTestInfo) Sys() any    { return nil }

type scanReadStub struct {
	children []os.DirEntry
	err      error
	calls    int
	maximum  int
}

func (s *scanReadStub) ReadDir(count int) ([]os.DirEntry, error) {
	s.calls++
	if count > s.maximum {
		s.maximum = count
	}
	if len(s.children) == 0 {
		return nil, s.err
	}
	n := min(count, len(s.children))
	entries := s.children[:n]
	s.children = s.children[n:]
	return entries, nil
}
func (*scanReadStub) Close() error { return nil }

func TestReadDirBoundAndCallbackErrorPropagation(t *testing.T) {
	for _, callbackError := range []error{domain.ErrDatabase, domain.ErrJobLeaseLost, domain.ErrScanLimit, domain.ErrConflict, context.Canceled, errors.New("private worker error must be mapped by runner")} {
		reader := &scanReadStub{err: io.EOF}
		for index := 0; index < 1000; index++ {
			reader.children = append(reader.children, fs.FileInfoToDirEntry(scanTestInfo{name: fmt.Sprintf("file-%04d", index)}))
		}
		emissions := 0
		err := enumerate(context.Background(), reader, domain.ScanDirectory{RootID: testRootID, RootPath: "unused-private-root", Path: "."}, func(batch domain.ScanBatch) error {
			emissions++
			if len(batch.Entries)+len(batch.Directories) != 128 || batch.Done {
				t.Fatal("first callback not bounded")
			}
			return callbackError
		})
		if err != callbackError || emissions != 1 || reader.calls != 1 || reader.maximum != 128 {
			t.Fatal("callback failure continued reading or lost original worker error")
		}
	}
}

func TestSkippedCountsOnlyFinalBatchAndMetadataLimits(t *testing.T) {
	reader := &scanReadStub{err: io.EOF}
	for index := 0; index < 300; index++ {
		reader.children = append(reader.children, fs.FileInfoToDirEntry(scanTestInfo{name: fmt.Sprintf("link-%03d", index), mode: os.ModeSymlink}))
	}
	for _, info := range []scanTestInfo{{name: "socket", mode: os.ModeSocket}, {name: "fifo", mode: os.ModeNamedPipe}, {name: "device", mode: os.ModeDevice}, {name: "irregular-directory", mode: os.ModeDir | os.ModeIrregular}, {name: "bad\\name"}, {name: "bad:name"}, {name: "\xff"}, {name: "bad\nname"}, {name: "bad\tname"}, {name: "bad\u0085name"}} {
		reader.children = append(reader.children, fs.FileInfoToDirEntry(info))
	}
	emissions := 0
	if err := enumerate(context.Background(), reader, domain.ScanDirectory{RootID: testRootID, RootPath: "unused", Path: "."}, func(batch domain.ScanBatch) error {
		emissions++
		if !batch.Done || batch.Skipped != 310 || len(batch.Entries)+len(batch.Directories) != 0 {
			t.Fatal("skipped-only batches must not leak intermediate counts")
		}
		return nil
	}); err != nil || emissions != 1 || reader.maximum != 128 {
		t.Fatalf("skip enumeration failed: %v", err)
	}
	for _, info := range []scanTestInfo{{name: "negative", size: -1}, {name: "unrepresentable-time", time: time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)}} {
		reader := &scanReadStub{err: io.EOF, children: []os.DirEntry{fs.FileInfoToDirEntry(info)}}
		if err := enumerate(context.Background(), reader, domain.ScanDirectory{RootID: testRootID, RootPath: "unused", Path: "."}, func(domain.ScanBatch) error { t.Fatal("invalid metadata emitted"); return nil }); err != domain.ErrScanLimit {
			t.Fatal("metadata overflow not rejected")
		}
	}
}

type blockingDirectory struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (d *blockingDirectory) ReadDir(int) ([]os.DirEntry, error) {
	close(d.entered)
	<-d.closed
	return nil, errors.New("private blocked filesystem path")
}
func (d *blockingDirectory) Close() error { d.once.Do(func() { close(d.closed) }); return nil }

func TestCancellationClosesBlockedDirectoryWithoutDetachedReader(t *testing.T) {
	reader := &blockingDirectory{entered: make(chan struct{}), closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- enumerate(ctx, reader, domain.ScanDirectory{RootID: testRootID, RootPath: "unused", Path: "."}, func(domain.ScanBatch) error { return errors.New("callback should not run") })
	}()
	<-reader.entered
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("cancel error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked reader did not finish after close")
	}
}

func TestCancellationAtCallbacksAndReadFailuresDoNotEmitDone(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, root, "file.mp4", []byte("unchanged"))
	ctx, cancel := context.WithCancel(context.Background())
	emissions := 0
	err := New().ScanDirectory(ctx, domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, func(batch domain.ScanBatch) error {
		emissions++
		if batch.Done {
			t.Fatal("completed cancelled directory")
		}
		cancel()
		return nil
	})
	if err != context.Canceled || emissions != 1 {
		t.Fatalf("callback cancellation: %v", err)
	}
	if err := ValidateRoot(ctx, root); err != context.Canceled {
		t.Fatal("root validation ignored cancellation")
	}
	if err := New().ScanDirectory(ctx, domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, func(domain.ScanBatch) error { t.Fatal("cancelled scan emitted"); return nil }); err != context.Canceled {
		t.Fatal("scan ignored pre-cancel")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if err := enumerate(ctx, &scanReadStub{err: io.EOF}, domain.ScanDirectory{RootID: testRootID, RootPath: "unused", Path: "."}, func(batch domain.ScanBatch) error {
		if !batch.Done {
			t.Fatal("empty directory emitted data")
		}
		cancel()
		return nil
	}); err != context.Canceled {
		t.Fatal("final callback cancellation ignored")
	}
	reader := &scanReadStub{err: errors.New("private absolute filesystem path")}
	if err := enumerate(context.Background(), reader, domain.ScanDirectory{RootID: testRootID, RootPath: "unused", Path: "."}, func(domain.ScanBatch) error { t.Fatal("read failure emitted done"); return nil }); err != domain.ErrScanIO {
		t.Fatal("read failure was not sanitized")
	}
}
