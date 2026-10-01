//go:build linux || windows

package ignoresource

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyBaselineMissingParentRetainsNearestSource(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	if err := os.Mkdir(filepath.Join(root, "a"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("*.mkv"), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewResolver()
	o, err := r.ObserveLegacyBaseline(context.Background(), root, "a/missing/deeper")
	if err != nil || !o.Source.Present || o.Source.SourceDirectory != "." || string(o.Source.Bytes()) != "*.mkv" {
		t.Fatal("nearest existing source", err)
	}
	proofs := o.Source.DirectoryProofs()
	if len(proofs) != 2 || o.MissingDirectory.Directory != "a/missing" || !o.MissingDirectory.MissingDirectory || o.MissingDirectory.ParentIdentity != proofs[1].Identity || o.MissingDirectory.Identity != ([32]byte{}) {
		t.Fatal("missing boundary fabricated descendants")
	}
	if _, err = r.ObserveLegacy(context.Background(), root, "a/missing/deeper"); err == nil {
		t.Fatal("ordinary scan accepted missing candidate")
	}
	if err = os.WriteFile(filepath.Join(root, "a", ".ignore"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	nearer, err := r.ObserveLegacyBaseline(context.Background(), root, "a/missing/deeper")
	if err != nil || !nearer.Source.Present || nearer.Source.SourceDirectory != "a" || len(nearer.Source.Bytes()) != 0 || nearer.Token() == o.Token() {
		t.Fatal("empty nearer source lost", err)
	}
	data, err := json.Marshal(nearer)
	if err != nil || string(data) != "{}" {
		t.Fatal("baseline evidence exposed")
	}
	// A nearer empty file shadows even an unsafe ancestor source.
	if err = os.Remove(filepath.Join(root, ".ignore")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, ".ignore"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ObserveLegacyBaseline(context.Background(), root, "a/missing/deeper"); err != nil {
		t.Fatal("shadowed ancestor read", err)
	}
}

func TestLegacyBaselineRejectsBoundaryChangeBetweenPasses(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	calls := 0
	opener := func(p string) (directory, error) {
		calls++
		if calls == 2 {
			if err := os.Mkdir(filepath.Join(root, "missing"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		return openNativeRoot(p)
	}
	got, err := NewResolver().observeLegacyBaseline(context.Background(), root, "missing/deeper", opener)
	if err != ErrChanged || got.Token() != ([32]byte{}) || got.MissingDirectory.MissingDirectory || len(got.Source.DirectoryProofs()) != 0 {
		t.Fatal("changed boundary accepted", err)
	}
}

func TestLegacyBaselineExistingNoSourceAndUnsafeComponent(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	if err := os.Mkdir(filepath.Join(root, "a"), 0700); err != nil {
		t.Fatal(err)
	}
	r := NewResolver()
	o, err := r.ObserveLegacyBaseline(context.Background(), root, "a")
	if err != nil || o.Source.Present || o.MissingDirectory != (DirectoryProof{}) || len(o.Source.DirectoryProofs()) != 2 {
		t.Fatal("existing absence", err)
	}
	if err = os.WriteFile(filepath.Join(root, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ObserveLegacyBaseline(context.Background(), root, "file/deeper"); err == nil || got.Token() != ([32]byte{}) {
		t.Fatal("file treated as absent directory", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := r.ObserveLegacyBaseline(ctx, root, "a"); err != context.Canceled || got.Token() != ([32]byte{}) {
		t.Fatal("cancellation", err)
	}
}
