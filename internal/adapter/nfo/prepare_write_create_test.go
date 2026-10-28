//go:build windows || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

// createScope is a library root with the media of an item and no NFO.
func createScope(t *testing.T, kind string) (domain.NFOItemScope, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "library")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	scope := domain.NFOItemScope{ItemID: "a0000000-0000-0000-0000-000000000001", LibraryID: "a0000000-0000-0000-0000-000000000002", SourceID: "a0000000-0000-0000-0000-000000000003", RootID: "a0000000-0000-0000-0000-000000000004", Kind: kind, Revision: 1, Generation: 1, RootGeneration: 1, Source: domain.NFOSource{RootPath: root}}
	if kind == "Series" || kind == "Season" {
		scope.DirectoryPath = "Show"
		if kind == "Season" {
			scope.DirectoryPath = "Show/Season 1"
		}
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(scope.DirectoryPath)), 0o700); err != nil {
			t.Fatal(err)
		}
		scope.Source.RelativePath, _ = domain.DirectoryNFOPath(scope.DirectoryPath, kind)
		return scope, root
	}
	scope.MediaPath = "Show/e01 電影.mkv"
	if err := os.MkdirAll(filepath.Join(root, "Show"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(scope.MediaPath)), []byte("owned synthetic media"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope.Source.RelativePath, _ = domain.AdjacentNFOPath(scope.MediaPath)
	return scope, root
}

func createRequest(scope domain.NFOItemScope) domain.NFOWritePrepareRequest {
	return domain.NFOWritePrepareRequest{ItemID: scope.ItemID, Revision: 1, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "新的 & 標題"}, {Field: "plot", Value: "created <plot>"}}, MaxBytes: DefaultMaxBytes, Create: true}
}

func TestWritePreparerCreatesAnEmptyRootOfTheItemKind(t *testing.T) {
	for _, c := range []struct{ kind, root, name string }{
		{"Movie", "movie", "Show/e01 電影.nfo"},
		{"HomeVideo", "movie", "Show/e01 電影.nfo"},
		{"Episode", "episodedetails", "Show/e01 電影.nfo"},
		{"Series", "tvshow", "Show/tvshow.nfo"},
		{"Season", "season", "Show/Season 1/season.nfo"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			scope, root := createScope(t, c.kind)
			if scope.Source.RelativePath != c.name {
				t.Fatal("the NFO name the reader looks up differs from the create name", scope.Source.RelativePath)
			}
			request := createRequest(scope)
			b := &recordingWriterBudget{Budget: writerBudget(t, 1, 0)}
			p, _ := NewWritePreparer(b)
			before, _ := os.ReadDir(root)
			value, err := p.PrepareNFOWrite(context.Background(), scope, request)
			if err != nil || domain.ValidateNFOWritePreparation(value) != nil {
				t.Fatal("create preparation failed", err)
			}
			empty := sha256.Sum256(nil)
			if len(value.Original) != 0 || value.Stamp.Size != 0 || value.Stamp.SHA256 != hex.EncodeToString(empty[:]) || !value.NativeObservation.TargetAbsent() || value.NativeObservation.NFOFileIdentity() != ([48]byte{}) || !value.Request.Create {
				t.Fatal("a create preparation has no original and an absent-target receipt")
			}
			document, err := Read(context.Background(), bytes.NewReader(value.Replacement), request.MaxBytes)
			if err != nil || document.Root != c.root || document.Entries[0].Title != "新的 & 標題" || document.Entries[0].Plot != "created <plot>" || len(document.Entries[0].UniqueIDs) != 1 || document.Entries[0].UniqueIDs[0].Type != "jelee" {
				t.Fatal("created document differs", err)
			}
			for _, issue := range document.Issues {
				if issue.Severity == "error" {
					t.Fatal("created document has errors", issue)
				}
			}
			if !bytes.HasPrefix(value.Replacement, []byte("<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?>\n<"+c.root+">\n  <title>")) || !bytes.HasSuffix(value.Replacement, []byte("</"+c.root+">\n")) || bytes.HasPrefix(value.Replacement, []byte{0xef, 0xbb, 0xbf}) {
				t.Fatalf("created layout differs: %q", value.Replacement)
			}
			if len(b.classes) != 3 || b.classes[0] != app.WorkIO || b.classes[1] != app.WorkCPU || b.classes[2] != app.WorkIO {
				t.Fatal("stages nested or omitted")
			}
			if b.Stats() != (resources.Stats{}) {
				t.Fatal("permit leaked")
			}
			after, _ := os.ReadDir(root)
			if len(before) != len(after) {
				t.Fatal("preparation wrote the filesystem")
			}
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(scope.Source.RelativePath))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("preparation created the file")
			}
			// The writer rebuilds exactly the saved bytes from the recipe alone.
			writer, _ := NewWriterWithBudget(writerBudget(t, 1, 0))
			rebuilt, err := writer.rebuildPreparedCreate(context.Background(), value)
			if err != nil || !bytes.Equal(rebuilt.original, value.Replacement) {
				t.Fatal("create replay differs", err)
			}
			changed := domain.CloneNFOWritePreparation(value)
			changed.Request.Edits[0].Value = "another"
			if _, err := writer.rebuildPreparedCreate(context.Background(), changed); err == nil {
				t.Fatal("a recipe that does not produce the saved bytes was accepted")
			}
		})
	}
}

func TestWritePreparerCreateBOMAndIndent(t *testing.T) {
	scope, _ := createScope(t, "Movie")
	for _, c := range []struct{ bom, indent string }{{"include", "\t"}, {"omit", "    "}, {"preserve", ""}, {"", ""}} {
		request := createRequest(scope)
		request.BOM, request.Indent = c.bom, c.indent
		p, _ := NewWritePreparer(writerBudget(t, 1, 0))
		value, err := p.PrepareNFOWrite(context.Background(), scope, request)
		if err != nil {
			t.Fatal(err)
		}
		if got := bytes.HasPrefix(value.Replacement, []byte{0xef, 0xbb, 0xbf}); got != (c.bom == "include") {
			t.Fatal("BOM option differs", c.bom)
		}
		wantIndent := c.indent
		if wantIndent == "" {
			wantIndent = "  "
		}
		if !strings.Contains(string(value.Replacement), "\n"+wantIndent+"<title>") {
			t.Fatalf("indent %q differs: %q", c.indent, value.Replacement)
		}
		writer, _ := NewWriterWithBudget(writerBudget(t, 1, 0))
		if rebuilt, err := writer.rebuildPreparedCreate(context.Background(), value); err != nil || !bytes.Equal(rebuilt.original, value.Replacement) {
			t.Fatal("replay differs", c.bom, err)
		}
	}
}

func TestWritePreparerCreateRefusesAnExistingNameInAnyForm(t *testing.T) {
	for _, kind := range []string{"file", "empty file", "directory", "dangling symlink", "symlink to a file"} {
		t.Run(kind, func(t *testing.T) {
			scope, root := createScope(t, "Movie")
			target := filepath.Join(root, filepath.FromSlash(scope.Source.RelativePath))
			switch kind {
			case "file":
				if err := os.WriteFile(target, []byte("<movie><title>mine</title></movie>"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "empty file":
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			case "dangling symlink":
				if err := os.Symlink("nowhere", target); err != nil {
					t.Skip("symlinks unavailable")
				}
			case "symlink to a file":
				if err := os.Symlink(filepath.Join(root, filepath.FromSlash(scope.MediaPath)), target); err != nil {
					t.Skip("symlinks unavailable")
				}
			}
			before, _ := os.Lstat(target)
			p, _ := NewWritePreparer(writerBudget(t, 1, 0))
			if _, err := p.PrepareNFOWrite(context.Background(), scope, createRequest(scope)); !errors.Is(err, domain.ErrConflict) {
				t.Fatal("an existing name was not a conflict", err)
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("the existing object changed")
			}
		})
	}
}

func TestWritePreparerCreateNeedsItsMedia(t *testing.T) {
	scope, root := createScope(t, "Movie")
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(scope.MediaPath))); err != nil {
		t.Fatal(err)
	}
	p, _ := NewWritePreparer(writerBudget(t, 1, 0))
	if value, err := p.PrepareNFOWrite(context.Background(), scope, createRequest(scope)); err == nil || len(value.Replacement) != 0 {
		t.Fatal("a create was prepared for a missing media object")
	}
}

func TestCreateRequestRules(t *testing.T) {
	scope, _ := createScope(t, "Movie")
	request := createRequest(scope)
	if err := domain.ValidateNFOWritePrepareRequest(request); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*domain.NFOWritePrepareRequest){
		"backups":       func(r *domain.NFOWritePrepareRequest) { r.Backups = 1 },
		"create-update": func(r *domain.NFOWritePrepareRequest) { r.CreateMissing = true },
	} {
		bad := domain.CloneNFOWritePrepareRequest(request)
		change(&bad)
		if domain.ValidateNFOWritePrepareRequest(bad) == nil {
			t.Fatal("create request accepted", name)
		}
	}
	// The rewrite digest does not change: the key is omitted unless create is set.
	rewrite := domain.CloneNFOWritePrepareRequest(request)
	rewrite.Create, rewrite.Backups = false, 3
	digest, _ := domain.NFOWriteRequestDigest(rewrite)
	createDigest, _ := domain.NFOWriteRequestDigest(request)
	if digest == createDigest {
		t.Fatal("create and rewrite requests share a digest")
	}
}
