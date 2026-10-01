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

func TestLegacyBaselineNativeRetainRestoreRecheck(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f, l, _ := legacyManifestFixture(t)
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	root = filepath.Clean(root)
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("*.mkv"), 0600); err != nil {
		t.Fatal(err)
	}
	scanner := scan.NewIgnoreScanner()
	o, err := scanner.ObserveLegacyIgnoreBaseline(f.ctx, domain.ScanDirectory{RootID: f.registration.RootID, RootPath: root, Path: "gone/deep"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.RecordLegacyIgnoreBaselineObservations(f.ctx, l, []domain.LegacyIgnoreBaselineObservation{o}); err != nil {
		t.Fatal(err)
	}
	if err = f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	page, err := f.s.ReadLegacyIgnoreBaselinePage(f.ctx, l, domain.IgnoreProofCursor{})
	if err != nil || len(page) != 1 {
		t.Fatal("restore", err)
	}
	if _, err = scanner.ReobserveLegacyIgnoreBaseline(f.ctx, root, page[0]); err != nil {
		t.Fatal("stable boundary", err)
	}
	if err = os.Mkdir(filepath.Join(root, "gone"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = scanner.ReobserveLegacyIgnoreBaseline(f.ctx, root, page[0]); err != domain.ErrInventoryInvalidated {
		t.Fatal("boundary reappeared", err)
	}
}
