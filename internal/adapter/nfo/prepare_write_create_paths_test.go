//go:build linux

package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestCreateBaseDocumentIsAnEmptyRootPerKind(t *testing.T) {
	for kind, root := range map[string]string{"Movie": "movie", "HomeVideo": "movie", "Series": "tvshow", "Season": "season", "Episode": "episodedetails"} {
		document, err := createBaseDocument(context.Background(), kind)
		if err != nil || document.Root != root || len(document.Entries) != 1 || bytes.HasPrefix(document.original, []byte("\xef\xbb\xbf")) {
			t.Fatal("base document of", kind, err)
		}
		again, _ := createBaseDocument(context.Background(), kind)
		if !bytes.Equal(document.original, again.original) {
			t.Fatal("the base bytes must be identical for every replay", kind)
		}
	}
	for _, kind := range []string{"", "Collection", "movie"} {
		if _, err := createBaseDocument(context.Background(), kind); !errors.Is(err, ErrInvalidInput) {
			t.Fatal("unknown kind", kind, err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := createBaseDocument(cancelled, "Movie"); err == nil {
		t.Fatal("a cancelled parse succeeded")
	}
}

func TestRebuildPreparedCreateReproducesOnlyTheSavedBytes(t *testing.T) {
	scope, _ := createScope(t, "Movie")
	b := writerBudget(t, 1, 0)
	preparer, _ := NewWritePreparer(b)
	prepared, err := preparer.PrepareNFOWrite(context.Background(), scope, createRequest(scope))
	if err != nil {
		t.Fatal(err)
	}
	w, _ := NewWriterWithBudget(b)
	rebuilt, err := w.rebuildPreparedCreate(context.Background(), prepared)
	if err != nil || !bytes.Equal(rebuilt.original, prepared.Replacement) {
		t.Fatal("the saved recipe must reproduce the saved bytes", err)
	}
	// The recipe is replayed, not the bytes: a changed edit no longer matches.
	changed := domain.CloneNFOWritePreparation(prepared)
	changed.Request.Edits[0].Value += " changed"
	if _, err := w.rebuildPreparedCreate(context.Background(), changed); err == nil {
		t.Fatal("a recipe that differs from the saved bytes was rebuilt")
	}
	// A rewrite preparation is not a create.
	rewrite := domain.CloneNFOWritePreparation(prepared)
	rewrite.Request.Create = false
	if _, err := w.rebuildPreparedCreate(context.Background(), rewrite); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("rewrite", err)
	}
	unbound := domain.CloneNFOWritePreparation(prepared)
	unbound.Scope.RootGeneration = 0
	if _, err := w.rebuildPreparedCreate(context.Background(), unbound); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("unbound root", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.rebuildPreparedCreate(cancelled, prepared); err == nil {
		t.Fatal("a cancelled rebuild succeeded")
	}
}

func TestWritePreparerCreateRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("invalid arguments", func(t *testing.T) {
		scope, _ := createScope(t, "Movie")
		p, _ := NewWritePreparer(writerBudget(t, 1, 0))
		request := createRequest(scope)
		stale := request
		stale.Revision = 2
		unbound := scope
		unbound.RootGeneration = 0
		for name, call := range map[string]func() error{
			"revision mismatch": func() error { _, err := p.PrepareNFOWrite(ctx, scope, stale); return err },
			"unbound root":      func() error { _, err := p.PrepareNFOWrite(ctx, unbound, request); return err },
			"nil preparer":      func() error { _, err := (*WritePreparer)(nil).PrepareNFOWrite(ctx, scope, request); return err },
		} {
			if err := call(); !errors.Is(err, domain.ErrInvalid) {
				t.Fatal(name, err)
			}
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		scope, _ := createScope(t, "Movie")
		p, _ := NewWritePreparer(writerBudget(t, 1, 0))
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := p.PrepareNFOWrite(cancelled, scope, createRequest(scope)); err == nil {
			t.Fatal("a cancelled create was prepared")
		}
	})
	t.Run("existing target is a conflict", func(t *testing.T) {
		scope, root := createScope(t, "Movie")
		target := filepath.Join(root, filepath.FromSlash(scope.Source.RelativePath))
		if err := os.WriteFile(target, []byte("<movie/>"), 0o600); err != nil {
			t.Fatal(err)
		}
		p, _ := NewWritePreparer(writerBudget(t, 1, 0))
		if _, err := p.PrepareNFOWrite(ctx, scope, createRequest(scope)); !errors.Is(err, domain.ErrConflict) {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(target); string(data) != "<movie/>" {
			t.Fatal("preparation touched the existing target")
		}
	})
	t.Run("an edit the new document cannot take", func(t *testing.T) {
		scope, root := createScope(t, "Movie")
		request := createRequest(scope)
		request.Edits = []domain.NFOWriteTextEdit{{Field: "title", Value: "bad \x00 value"}}
		p, _ := NewWritePreparer(writerBudget(t, 1, 0))
		if value, err := p.PrepareNFOWrite(ctx, scope, request); err == nil || len(value.Replacement) != 0 {
			t.Fatal("an invalid edit was prepared", err)
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(scope.Source.RelativePath))); err == nil {
			t.Fatal("preparation wrote the target")
		}
	})
	t.Run("a kind without a root", func(t *testing.T) {
		scope, _ := createScope(t, "Movie")
		p, _ := NewWritePreparer(writerBudget(t, 1, 0))
		if _, err := p.prepareCreateBytes(ctx, domain.NFOItemScope{Kind: "Unknown"}, createRequest(scope)); !errors.Is(err, ErrInvalidInput) {
			t.Fatal(err)
		}
	})
}
