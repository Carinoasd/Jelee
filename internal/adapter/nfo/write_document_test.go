//go:build windows || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeDocumentFixture(t *testing.T) (string, *os.Root, *Document, *Document) {
	t.Helper()
	path := t.TempDir()
	dir := lockTestRoot(t, path)
	original := editDocument(t, []byte("<movie>\r\n\t<!--keep--><title>old</title><vendor x='1'/>\r\n</movie>"))
	replacement, err := original.WithText(context.Background(), 0, "title", "new", DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "movie.nfo"), original.original, 0600); err != nil {
		t.Fatal(err)
	}
	return path, dir, original, replacement
}

func checkNoNFOStages(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".jelee-nfo-stage-") {
			t.Fatal("temporary NFO stage leaked")
		}
	}
}

func TestReplaceNFODocumentPreservesXMLRotatesAndReducesBackups(t *testing.T) {
	path, dir, original, replacement := writeDocumentFixture(t)
	if err := replaceNFODocument(context.Background(), dir, "movie.nfo", original, replacement, 3); err != nil {
		t.Fatal(err)
	}
	data, _ := dir.ReadFile("movie.nfo")
	backup, _ := dir.ReadFile("movie.nfo.jelee.bak")
	if !bytes.Equal(data, replacement.original) || !bytes.Equal(backup, original.original) {
		t.Fatal("bytes not preserved")
	}
	for i := 0; i < 4; i++ {
		next, err := replacement.WithText(context.Background(), 0, "title", strconv.Itoa(i), DefaultMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := replaceNFODocument(context.Background(), dir, "movie.nfo", replacement, next, 3); err != nil {
			t.Fatal(err)
		}
		replacement = next
	}
	for i, title := range []string{"2", "1", "0"} {
		data, err := dir.ReadFile(nfoBackupName("movie.nfo", i))
		if err != nil || editDocument(t, data).Metadata.Title != title {
			t.Fatal("backup order incorrect")
		}
	}
	next, _ := replacement.WithText(context.Background(), 0, "title", "last", DefaultMaxBytes)
	if err := replaceNFODocument(context.Background(), dir, "movie.nfo", replacement, next, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.Stat("movie.nfo.jelee.bak.1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retention count ignored")
	}
	checkNoNFOStages(t, path)
}

func TestReplaceNFODocumentRejectsChangedSourceAndKeepsNoOpIdentity(t *testing.T) {
	path, dir, original, replacement := writeDocumentFixture(t)
	before, _ := dir.Stat("movie.nfo")
	if err := replaceNFODocument(context.Background(), dir, "movie.nfo", original, original, 2); err != nil {
		t.Fatal(err)
	}
	after, _ := dir.Stat("movie.nfo")
	if !os.SameFile(before, after) {
		t.Fatal("no-op replaced inode")
	}
	if err := dir.WriteFile("movie.nfo", []byte(`<movie><title>user edit</title></movie>`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := replaceNFODocument(context.Background(), dir, "movie.nfo", original, replacement, 1); err != ErrChanged {
		t.Fatal("user change overwritten")
	}
	data, _ := dir.ReadFile("movie.nfo")
	if !bytes.Contains(data, []byte("user edit")) {
		t.Fatal("changed file mutated")
	}
	checkNoNFOStages(t, path)
}

func TestReplaceNFODocumentFailuresRollbackAndCleanStages(t *testing.T) {
	for _, stage := range []string{"file_sync", "backup_rename", "backup_directory_sync", "target_rename", "target_directory_sync"} {
		t.Run(stage, func(t *testing.T) {
			path, dir, original, replacement := writeDocumentFixture(t)
			ops := nativeNFOWriteOperations()
			base := ops
			failed := errors.New("injected")
			if stage == "file_sync" {
				ops.syncFile = func(*os.File) error { return failed }
			}
			ops.rename = func(root *os.Root, from, to string) error {
				if stage == "backup_rename" && to == nfoBackupName("movie.nfo", 0) || stage == "target_rename" && to == "movie.nfo" {
					return failed
				}
				return base.rename(root, from, to)
			}
			calls := 0
			ops.syncDirectory = func(root *os.Root) error {
				calls++
				if stage == "backup_directory_sync" && calls == 1 || stage == "target_directory_sync" && calls == 2 {
					return failed
				}
				return base.syncDirectory(root)
			}
			if err := replaceNFODocumentWithOperations(context.Background(), dir, "movie.nfo", original, replacement, 2, ops); err != ErrReplace {
				t.Fatalf("failure: %v", err)
			}
			data, _ := dir.ReadFile("movie.nfo")
			if !bytes.Equal(data, original.original) {
				t.Fatal("original not restored")
			}
			checkNoNFOStages(t, path)
			// The native lock is released on every failure path.
			if err := replaceNFODocument(context.Background(), dir, "movie.nfo", original, replacement, 2); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReplaceNFODocumentRollbackFailureRetainsRecoveryBytes(t *testing.T) {
	path, dir, original, replacement := writeDocumentFixture(t)
	ops := nativeNFOWriteOperations()
	base := ops
	renames := 0
	ops.rename = func(root *os.Root, from, to string) error {
		renames++
		if renames == 2 {
			return errors.New("rollback unavailable")
		}
		return base.rename(root, from, to)
	}
	ops.syncDirectory = func(*os.Root) error { return errors.New("directory sync unavailable") }
	if err := replaceNFODocumentWithOperations(context.Background(), dir, "movie.nfo", original, replacement, 0, ops); err != ErrRollback {
		t.Fatalf("rollback failure: %v", err)
	}
	entries, _ := os.ReadDir(path)
	retained := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".jelee-nfo-stage-") {
			data, _ := dir.ReadFile(entry.Name())
			if !bytes.Equal(data, original.original) {
				t.Fatal("wrong recovery bytes")
			}
			retained++
		}
	}
	if retained != 1 {
		t.Fatal("recovery source not retained")
	}
}

func TestReplaceNFODocumentCancelsBeforeCommitAndRejectsBackupDirectory(t *testing.T) {
	path, dir, original, replacement := writeDocumentFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	ops := nativeNFOWriteOperations()
	base := ops
	ops.syncFile = func(file *os.File) error { cancel(); return base.syncFile(file) }
	if err := replaceNFODocumentWithOperations(ctx, dir, "movie.nfo", original, replacement, 0, ops); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel ignored")
	}
	data, _ := dir.ReadFile("movie.nfo")
	if !bytes.Equal(data, original.original) {
		t.Fatal("canceled write changed source")
	}
	checkNoNFOStages(t, path)
	if err := os.Mkdir(filepath.Join(path, "movie.nfo.jelee.bak"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := replaceNFODocument(context.Background(), dir, "movie.nfo", original, replacement, 1); err != ErrReplace {
		t.Fatal("backup directory replaced")
	}
}

func TestReplaceNFODocumentDetectsUserEditWithRestoredSizeAndTime(t *testing.T) {
	path, dir, original, replacement := writeDocumentFixture(t)
	before, err := dir.Stat("movie.nfo")
	if err != nil {
		t.Fatal(err)
	}
	userBytes := bytes.Replace(original.original, []byte("old"), []byte("usr"), 1)
	ops := nativeNFOWriteOperations()
	base := ops
	changed := false
	ops.syncFile = func(file *os.File) error {
		if !changed {
			changed = true
			if err := dir.WriteFile("movie.nfo", userBytes, 0600); err != nil {
				return err
			}
			if err := os.Chtimes(filepath.Join(path, "movie.nfo"), before.ModTime(), before.ModTime()); err != nil {
				return err
			}
		}
		return base.syncFile(file)
	}
	if err := replaceNFODocumentWithOperations(context.Background(), dir, "movie.nfo", original, replacement, 1, ops); err != ErrChanged {
		t.Fatalf("restored timestamp bypass: %v", err)
	}
	data, _ := dir.ReadFile("movie.nfo")
	if !bytes.Equal(data, userBytes) {
		t.Fatal("concurrent user edit overwritten")
	}
	checkNoNFOStages(t, path)
}

func TestReplaceNFODocumentDoesNotRollbackOverExternalChange(t *testing.T) {
	path, dir, original, replacement := writeDocumentFixture(t)
	userBytes := bytes.Replace(replacement.original, []byte("new"), []byte("usr"), 1)
	ops := nativeNFOWriteOperations()
	ops.syncDirectory = func(*os.Root) error {
		if err := dir.WriteFile("movie.nfo", userBytes, 0600); err != nil {
			return err
		}
		return errors.New("commit sync failed after external edit")
	}
	if err := replaceNFODocumentWithOperations(context.Background(), dir, "movie.nfo", original, replacement, 0, ops); err != ErrRollback {
		t.Fatal("external edit not detected during rollback")
	}
	data, _ := dir.ReadFile("movie.nfo")
	if !bytes.Equal(data, userBytes) {
		t.Fatal("rollback overwrote external edit")
	}
	entries, _ := os.ReadDir(path)
	found := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".jelee-nfo-stage-") {
			data, _ := dir.ReadFile(entry.Name())
			found = found || bytes.Equal(data, original.original)
		}
	}
	if !found {
		t.Fatal("rollback failure lost original recovery bytes")
	}
}

func TestReplaceNFODocumentOneHundredConcurrentUpdates(t *testing.T) {
	path, dir, original, _ := writeDocumentFixture(t)
	initial, _ := original.WithText(context.Background(), 0, "title", "0", DefaultMaxBytes)
	if err := replaceNFODocument(context.Background(), dir, "movie.nfo", original, initial, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				document, err := ReadFile(ctx, path, "movie.nfo", DefaultMaxBytes)
				if errors.Is(err, ErrChanged) {
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				n, err := strconv.Atoi(document.Metadata.Title)
				if err != nil {
					t.Error("damaged title")
					return
				}
				next, err := document.WithText(ctx, 0, "title", strconv.Itoa(n+1), DefaultMaxBytes)
				if err != nil {
					t.Error(err)
					return
				}
				err = replaceNFODocument(ctx, dir, "movie.nfo", document, next, 0)
				if errors.Is(err, ErrChanged) {
					continue
				}
				if err != nil {
					t.Error(err)
				}
				return
			}
			t.Error("concurrent update deadline")
		}()
	}
	wg.Wait()
	data, err := dir.ReadFile("movie.nfo")
	if err != nil || editDocument(t, data).Metadata.Title != "100" || !bytes.Contains(data, []byte("<!--keep-->")) || !bytes.Contains(data, []byte("<vendor x='1'/>")) {
		t.Fatal("concurrent write lost data")
	}
	checkNoNFOStages(t, path)
}
