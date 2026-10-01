//go:build linux || windows

package ignoresource

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyNearestSourceAndRootBoundary(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "library")
	child := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outer, ".ignore"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewResolver()
	ctx := context.Background()
	absent, err := r.ObserveLegacy(ctx, root, "a/b")
	if err != nil || absent.Present || len(absent.Bytes()) != 0 {
		t.Fatal("root boundary", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("root"), 0600); err != nil {
		t.Fatal(err)
	}
	inherited, err := r.ObserveLegacy(ctx, root, "a/b")
	if err != nil || !inherited.Present || inherited.SourceDirectory != "." || string(inherited.Bytes()) != "root" {
		t.Fatal("inheritance", err)
	}
	if inherited.Token() == absent.Token() {
		t.Fatal("new source missing from token")
	}
	if err := os.WriteFile(filepath.Join(child, ".ignore"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	nearest, err := r.ObserveLegacy(ctx, root, "a/b")
	if err != nil || !nearest.Present || nearest.SourceDirectory != "a/b" || len(nearest.Bytes()) != 0 {
		t.Fatal("empty nearest must override ancestor", err)
	}
	// A shadowed ancestor which is not a regular file must not be opened.
	if err := os.Remove(filepath.Join(root, ".ignore")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".ignore"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ObserveLegacy(ctx, root, "a/b"); err != nil {
		t.Fatal("shadowed ancestor read", err)
	}
	if err := os.Remove(filepath.Join(child, ".ignore")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ObserveLegacy(ctx, root, "a/b"); err != ErrUnsafe {
		t.Fatal("unsafe nearest became absent", err)
	}
}
func TestLegacyObservationDetectsNearerSourceBetweenPasses(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	if err := os.Mkdir(filepath.Join(root, "a"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("root"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	opener := func(path string) (directory, error) {
		calls++
		if calls == 2 {
			if err := os.WriteFile(filepath.Join(root, "a", ".ignore"), []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return openNativeRoot(path)
	}
	got, err := NewResolver().observeLegacy(context.Background(), root, "a", opener)
	if err != ErrChanged || got.Present || got.Token() != ([32]byte{}) {
		t.Fatal("nearer source changed without invalidation", err)
	}
}
func TestLegacyObservationOwnsSourceAndRejectsTraversal(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewResolver()
	got, err := r.ObserveLegacy(context.Background(), root, ".")
	if err != nil {
		t.Fatal(err)
	}
	copy := got.Bytes()
	copy[0] = 'b'
	if string(got.Bytes()) != "a" {
		t.Fatal("mutable result source")
	}
	for _, path := range []string{"../outside", "/absolute", "a/../b"} {
		if _, err := r.ObserveLegacy(context.Background(), root, path); err != ErrInvalid {
			t.Fatal("traversal accepted", err)
		}
	}
}
