//go:build linux || windows

package postgres

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestLegacyIgnoreNativeRetainRestoreRecheck(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f, l, _ := legacyManifestFixture(t)
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	root = filepath.Clean(root)
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("*.tmp\n"), 0600); err != nil {
		t.Fatal(err)
	}
	scanner := scan.NewIgnoreScanner()
	for _, directory := range []string{".", "child"} {
		o, err := scanner.ObserveLegacyIgnore(f.ctx, domain.ScanDirectory{RootID: f.registration.RootID, RootPath: root, Path: directory})
		if err != nil {
			t.Fatal(err)
		}
		if err = f.s.RecordLegacyIgnoreObservation(f.ctx, l, o); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	page, err := f.s.ReadLegacyIgnoreObservationPage(f.ctx, l, domain.IgnoreProofCursor{})
	if err != nil || len(page) != 2 {
		t.Fatal("restored native page", err)
	}
	for _, o := range page {
		if _, err := scanner.ReobserveLegacyIgnore(f.ctx, root, o); err != nil {
			t.Fatal("stable native source", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "child", ".ignore"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.ReobserveLegacyIgnore(f.ctx, root, page[0]); err != nil {
		t.Fatal("root query changed by descendant", err)
	}
	if got, err := scanner.ReobserveLegacyIgnore(f.ctx, root, page[1]); err != domain.ErrInventoryInvalidated || len(got.Proofs) != 0 {
		t.Fatal("new empty nearest source undetected", err)
	}
}
