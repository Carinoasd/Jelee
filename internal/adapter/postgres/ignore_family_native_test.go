//go:build !race && (linux || windows)

package postgres

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

func TestFamilyIgnoreNativeStorage(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f, l, _ := legacyManifestFixture(t)
	d, err := f.s.NextFamilyIgnoreScanDirectory(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	d.RootPath = filepath.Clean(d.RootPath)
	for name, data := range map[string]string{".jeleeignore": "!keep.mkv\ncustom.tmp\n", ".ignore": "*.mkv\n", "keep.mkv": "keep", "drop.mkv": "drop", "custom.tmp": "custom", "hidden/.ignore": "", "invalid/.ignore": "["} {
		target := filepath.Join(d.RootPath, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(target, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runner, err := process.NewIgnoreRunner(t.TempDir(), 2, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	scanner := scan.NewFamilyIgnoreScanner(runner)
	if err = scanner.ScanFamilyIgnoreDirectory(f.ctx, d, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}, func(b domain.FamilyIgnoreScanBatch) error { return f.s.SaveFamilyIgnoreScanBatch(f.ctx, l, d, b) }); err != nil {
		t.Fatal(err)
	}
	var kept, excluded, blank, invalid int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_inventory WHERE job_id=$1::uuid AND path='keep.mkv'`, l.Job.ID).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE reason='blank-source' AND rule_line=0),count(*) FILTER(WHERE reason='invalid-source' AND rule_line=0) FROM job_ignore_family_exclusions WHERE job_id=$1::uuid`, l.Job.ID).Scan(&excluded, &blank, &invalid); err != nil {
		t.Fatal(err)
	}
	if kept != 1 || excluded != 4 || blank != 1 || invalid != 1 {
		t.Fatal("native provenance", kept, excluded, blank, invalid)
	}
	if _, err = f.s.NextFamilyIgnoreScanDirectory(f.ctx, l); err != domain.ErrNotFound {
		t.Fatal("excluded directory queued", err)
	}
	if runner.Stats().Active != 0 {
		t.Fatal("helper still active")
	}
}
