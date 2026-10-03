//go:build windows || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func preparedWriterFixture(t *testing.T, content []byte, kind string) (string, *Source, domain.NFOWritePreparation, *resources.Budget) {
	t.Helper()
	root, name := sourceFixture(t, content)
	if kind != "Series" && kind != "Season" {
		if err := os.WriteFile(filepath.Join(root, "電影 title.mkv"), []byte("owned synthetic media"), 0600); err != nil {
			t.Fatal("create owned prepared writer media")
		}
	}
	scope := domain.NFOItemScope{ItemID: "a0000000-0000-0000-0000-000000000001", LibraryID: "a0000000-0000-0000-0000-000000000002", SourceID: "a0000000-0000-0000-0000-000000000003", RootID: "a0000000-0000-0000-0000-000000000004", Kind: kind, Revision: 1, Generation: 1, RootGeneration: 1, MediaPath: "電影 title.mkv", Source: domain.NFOSource{RootPath: root, RelativePath: name}}
	if kind == "Series" || kind == "Season" {
		scope.MediaPath = ""
		scope.DirectoryPath = "folder"
		scope.Source.RelativePath, _ = domain.DirectoryNFOPath(scope.DirectoryPath, kind)
		if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, name), filepath.Join(root, scope.Source.RelativePath)); err != nil {
			t.Fatal(err)
		}
	}
	request := domain.NFOWritePrepareRequest{ItemID: scope.ItemID, Revision: 1, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "新的 & 標題"}, {Field: "plot", Value: "new plot"}}, CreateMissing: true, BOM: "preserve", Indent: "\t", MaxBytes: DefaultMaxBytes, Backups: 2}
	b := writerBudget(t, 1, 0)
	p, _ := NewWritePreparer(b)
	prepared, err := p.PrepareNFOWrite(context.Background(), scope, request)
	if err != nil {
		t.Fatal("prepare fixture", err)
	}
	source, err := ReadSource(context.Background(), root, scope.Source.RelativePath, request.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return root, source, prepared, b
}

func TestPreparedWriterReconstructsFrozenOutput(t *testing.T) {
	for _, fixture := range []struct {
		name, kind string
		content    []byte
	}{
		{"generated", "Movie", []byte("\xef\xbb\xbf<movie x='1'>\r\n  <title>old</title><!--keep-->\r\n  <vendor a='1'/>\r\n</movie>")},
		{"manual", "HomeVideo", []byte(`<movie><title>old</title><uniqueid type='custom'>manual</uniqueid><!--keep--><vendor/></movie>`)},
		{"utf16", "Movie", encodeUTF16(`<?xml version="1.0" encoding="UTF-16"?><movie><title>中文</title><!--keep--></movie>`, true, true)},
		{"series", "Series", []byte(`<tvshow><title>old</title><!--keep--></tvshow>`)},
		{"season", "Season", []byte(`<season><title>old</title><!--keep--></season>`)},
		{"episode", "Episode", []byte(`<episodedetails><title>old</title><!--keep--></episodedetails>`)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root, source, prepared, b := preparedWriterFixture(t, fixture.content, fixture.kind)
			w, _ := NewWriterWithBudget(b)
			if err := w.ReplacePrepared(context.Background(), source, prepared); err != nil {
				t.Fatal("saved output rejected", err)
			}
			actual, err := os.ReadFile(filepath.Join(root, source.relative))
			backup, backupErr := os.ReadFile(filepath.Join(root, nfoBackupName(source.relative, 0)))
			if err != nil || backupErr != nil || !bytes.Equal(actual, prepared.Replacement) || !bytes.Equal(backup, prepared.Original) || b.Stats() != (resources.Stats{}) {
				t.Fatal("saved bytes, backup or budget differs")
			}
			// A committed output requires durable recovery evidence. Equality with
			// the expected output is not silently converted into successful replay.
			fresh, err := ReadSource(context.Background(), root, source.relative, prepared.Request.MaxBytes)
			if err != nil || w.ReplacePrepared(context.Background(), fresh, prepared) != ErrChanged {
				t.Fatal("already changed source inferred as committed")
			}
			if _, err := os.Stat(filepath.Join(root, nfoBackupName(source.relative, 1))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected replay rotated backups")
			}
		})
	}
}

func TestPreparedWriterRejectsUncontrolledOutputBeforeFilesystem(t *testing.T) {
	for _, scenario := range []string{"unknown XML", "manual ID", "request", "missing ID", "provider ID", "default ID", "non-v4 ID", "scope kind", "scope path", "stamp", "read cap"} {
		t.Run(scenario, func(t *testing.T) {
			content := []byte(`<movie><title>old</title><!--keep--><vendor/></movie>`)
			if scenario == "manual ID" {
				content = []byte(`<movie><title>old</title><uniqueid type='custom'>manual</uniqueid><!--keep--><vendor/></movie>`)
			}
			root, source, prepared, b := preparedWriterFixture(t, content, "Movie")
			switch scenario {
			case "unknown XML":
				prepared.Replacement = bytes.ReplaceAll(prepared.Replacement, []byte("<!--keep-->"), []byte("<!--tampered-->"))
			case "manual ID":
				prepared.Replacement = bytes.ReplaceAll(prepared.Replacement, []byte("manual"), []byte("changed"))
			case "request":
				prepared.Request.Edits[0].Value = "different recipe"
			case "missing ID":
				prepared.Replacement = bytes.ReplaceAll(prepared.Replacement, []byte("uniqueid"), []byte("vendorid"))
			case "provider ID":
				prepared.Replacement = bytes.ReplaceAll(prepared.Replacement, []byte(`type="jelee"`), []byte(`type="custom"`))
			case "default ID":
				prepared.Replacement = bytes.ReplaceAll(prepared.Replacement, []byte(`type="jelee"`), []byte(`type="jelee" default="true"`))
			case "non-v4 ID":
				frozen, _ := parseOriginal(context.Background(), prepared.Replacement)
				id := frozen.Entries[0].UniqueIDs[0].Value
				prepared.Replacement = bytes.ReplaceAll(prepared.Replacement, []byte(id), []byte(id[:14]+"1"+id[15:]))
			case "scope kind":
				prepared.Scope.Kind = "Episode"
			case "scope path":
				prepared.Scope.MediaPath = "other.mkv"
				prepared.Scope.Source.RelativePath = "other.nfo"
			case "stamp":
				prepared.Stamp.ModifiedUnixNano++
			case "read cap":
				prepared.Request.MaxBytes--
			}
			w, _ := NewWriterWithBudget(b)
			if err := w.ReplacePrepared(context.Background(), source, prepared); err == nil {
				t.Fatal("uncontrolled or unbound output accepted")
			}
			actual, _ := os.ReadFile(filepath.Join(root, source.relative))
			entries, _ := os.ReadDir(root)
			if !bytes.Equal(actual, content) || len(entries) != 2 || b.Stats() != (resources.Stats{}) {
				t.Fatal("rejection wrote source, sidecar, backup or leaked budget")
			}
		})
	}
}

func TestPreparedWriterFailureRetryKeepsFrozenID(t *testing.T) {
	root, source, prepared, b := preparedWriterFixture(t, []byte(`<movie><title>old</title><!--keep--></movie>`), "Movie")
	w, _ := NewWriterWithBudget(b)
	ops := nativeNFOWriteOperations()
	ops.syncFile = func(*os.File) error { return errors.New("injected sync failure") }
	if err := w.replacePrepared(context.Background(), source, prepared, ops); err != ErrReplace || b.Stats() != (resources.Stats{}) {
		t.Fatal("failed write or cleanup differs", err)
	}
	actual, _ := os.ReadFile(filepath.Join(root, source.relative))
	if !bytes.Equal(actual, prepared.Original) {
		t.Fatal("failed staging changed original")
	}
	// The next process/instance consumes the same persisted UUID and recipe.
	fresh, err := ReadSource(context.Background(), root, source.relative, prepared.Request.MaxBytes)
	w, _ = NewWriterWithBudget(b)
	if err != nil || w.ReplacePrepared(context.Background(), fresh, prepared) != nil {
		t.Fatal("saved intent cannot retry")
	}
	actual, _ = os.ReadFile(filepath.Join(root, source.relative))
	if !bytes.Equal(actual, prepared.Replacement) || b.Stats() != (resources.Stats{}) {
		t.Fatal("retry regenerated saved UUID or leaked budget")
	}
}

func TestPreparedWriterBusyCancellationAndRequiredBudget(t *testing.T) {
	root, source, prepared, b := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
	if err := (&Writer{}).ReplacePrepared(context.Background(), source, prepared); err != ErrInvalidInput {
		t.Fatal("unbudgeted prepared write accepted")
	}
	w, _ := NewWriterWithBudget(b)
	release, _ := b.Acquire(context.Background(), app.WorkCPU)
	if err := w.ReplacePrepared(context.Background(), source, prepared); err != domain.ErrResourceBusy {
		t.Fatal("busy prepared write accepted", err)
	}
	release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.ReplacePrepared(ctx, source, prepared); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled prepared write accepted", err)
	}
	actual, _ := os.ReadFile(filepath.Join(root, source.relative))
	entries, _ := os.ReadDir(root)
	if !bytes.Equal(actual, prepared.Original) || len(entries) != 2 || b.Stats() != (resources.Stats{}) {
		t.Fatal("busy/cancelled preparation touched filesystem")
	}
}

func TestPreparedWriterConcurrentRequestsShareFrozenOutput(t *testing.T) {
	root, source, prepared, _ := preparedWriterFixture(t, []byte(`<movie><title>old</title><!--keep--></movie>`), "Movie")
	b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 2, Queue: 256})
	w, _ := NewWriterWithBudget(b)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	release := make(chan struct{})
	var closeOnce sync.Once
	unblock := func() { closeOnce.Do(func() { close(release) }) }
	defer unblock()
	submitted := make(chan struct{}, 100)
	results := make(chan error, 100)
	var replacements atomic.Int32
	ops := nativeNFOWriteOperations()
	fileSync, rename := ops.syncFile, ops.rename
	ops.submitted = func() { submitted <- struct{}{} }
	ops.syncFile = func(file *os.File) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return fileSync(file)
	}
	ops.rename = func(directory *os.Root, from, to string) error {
		if to == source.relative {
			replacements.Add(1)
		}
		return rename(directory, from, to)
	}
	for range 100 {
		go func() { results <- w.replacePrepared(ctx, source, prepared, ops) }()
	}
	for range 100 {
		select {
		case <-submitted:
		case <-ctx.Done():
			unblock()
			t.Fatal("prepared requests failed to join shared operation")
		}
	}
	unblock()
	for range 100 {
		if err := <-results; err != nil {
			t.Fatal("shared frozen write failed", err)
		}
	}
	actual, _ := os.ReadFile(filepath.Join(root, source.relative))
	if replacements.Load() != 1 || !bytes.Equal(actual, prepared.Replacement) || b.Stats() != (resources.Stats{}) {
		t.Fatal("concurrent frozen requests repeated replacement, changed ID or leaked budget")
	}
}

func TestPreparedWriterExternalEditAfterRebuildIsPreserved(t *testing.T) {
	root, source, prepared, b := preparedWriterFixture(t, []byte(`<movie><title>old</title><!--keep--></movie>`), "Movie")
	w, _ := NewWriterWithBudget(b)
	external := bytes.ReplaceAll(prepared.Original, []byte("old"), []byte("new"))
	path := filepath.Join(root, source.relative)
	info, _ := os.Stat(path)
	ops := nativeNFOWriteOperations()
	fileSync := ops.syncFile
	var once sync.Once
	ops.syncFile = func(file *os.File) error {
		once.Do(func() {
			if err := os.WriteFile(path, external, 0600); err != nil {
				t.Error("inject external edit", err)
			}
			if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
				t.Error("restore external stamp", err)
			}
		})
		return fileSync(file)
	}
	if err := w.replacePrepared(context.Background(), source, prepared, ops); err != ErrChanged {
		t.Fatal("same-size/time external edit escaped final check", err)
	}
	actual, _ := os.ReadFile(path)
	if !bytes.Equal(actual, external) || b.Stats() != (resources.Stats{}) {
		t.Fatal("external edit overwritten or budget leaked")
	}
	if _, err := os.Stat(path + ".jelee.bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected external edit rotated backup")
	}
}

func TestPreparedWriterDoesNotShareDifferentPrivateRootBinding(t *testing.T) {
	for _, scenario := range []string{"saved root", "observed root"} {
		t.Run(scenario, func(t *testing.T) {
			root, source, prepared, _ := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
			b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 2, Queue: 4})
			w, _ := NewWriterWithBudget(b)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			started := make(chan struct{}, 1)
			result := make(chan error, 1)
			ops := nativeNFOWriteOperations()
			fileSync := ops.syncFile
			ops.syncFile = func(file *os.File) error {
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				return fileSync(file)
			}
			go func() { result <- w.replacePrepared(ctx, source, prepared, ops) }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("owner did not enter IO")
			}
			bad := domain.CloneNFOWritePreparation(prepared)
			observed := *source
			if scenario == "saved root" {
				bad.Scope.Source.RootPath = root + "-different"
			} else {
				observed.rootPath = root + "-different"
			}
			// NFOSource paths deliberately disappear from public JSON. The private
			// intent key must bind both saved and observed paths explicitly.
			if err := w.ReplacePrepared(ctx, &observed, bad); err != ErrChanged {
				unblock()
				t.Fatal("different private root shared owner result", err)
			}
			unblock()
			if err := <-result; err != nil || b.Stats() != (resources.Stats{}) {
				t.Fatal("valid owner failed or budget leaked", err)
			}
		})
	}
}
