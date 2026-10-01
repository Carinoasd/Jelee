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

func TestIgnoreNativeBridgePublication(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "source-change"}[changed], func(t *testing.T) {
			f, l, _ := manifestFixture(t)
			d, err := f.s.NextIgnoreScanDirectory(f.ctx, l)
			if err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string]string{".jeleeignore": "hidden/\n*.tmp\n", "kept.mkv": "video", "skip.tmp": "excluded", "child/kept.jpg": "image", "hidden/no.mkv": "excluded"} {
				target := filepath.Join(d.RootPath, filepath.FromSlash(name))
				if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(target, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			scanner := scan.NewIgnoreScanner()
			for {
				if err = scanner.ScanIgnoreDirectory(f.ctx, d, ignoreTestIntent(), func(b domain.IgnoreScanBatch) error { return f.s.SaveIgnoreScanBatch(f.ctx, l, d, b) }); err != nil {
					t.Fatal(err)
				}
				d, err = f.s.NextIgnoreScanDirectory(f.ctx, l)
				if err == domain.ErrNotFound {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			var root string
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
				t.Fatal(err)
			}
			classifyForPublication(t, f, l, func(*domain.IgnoreBaselineDecision) {}, false)
			if err = f.s.BeginIgnoreVerification(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			if changed {
				if err = os.WriteFile(filepath.Join(root, ".jeleeignore"), []byte("*.mkv\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for {
				page, e := f.s.NextIgnoreVerificationPage(f.ctx, l)
				if e != nil {
					t.Fatal(e)
				}
				if page.Complete {
					break
				}
				observed := make([]domain.IgnoreDirectoryProof, 0, len(page.Proofs))
				for _, retained := range page.Proofs {
					p, e := scanner.ReobserveIgnoreProof(f.ctx, root, retained)
					if e != nil {
						t.Fatal(e)
					}
					observed = append(observed, p)
				}
				err = f.s.CommitIgnoreVerificationPage(f.ctx, l, page.Token, observed)
				if changed {
					if err != domain.ErrInventoryInvalidated {
						t.Fatal("changed native source accepted", err)
					}
					if err = f.s.FinishIgnoreJob(f.ctx, l); err == nil {
						t.Fatal("invalid source published")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = f.s.SealIgnoreVerification(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			if err = f.s.FinishIgnoreJob(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			var rows, excluded int
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE path='skip.tmp' OR path LIKE 'hidden/%') FROM library_inventory_baseline WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&rows, &excluded); err != nil || rows != 3 || excluded != 0 {
				t.Fatal("wrong native publication", rows, excluded, err)
			}
		})
	}
}
