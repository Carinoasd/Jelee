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
	for _, name := range []string{"keep.mkv", "custom.tmp", "gone/plain.txt", "gone/movie.mkv", "hidden/movie.mkv", "invalid/movie.mkv"} {
		_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO library_inventory_baseline(library_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision) SELECT id,$2::uuid,$3,true,'video',7,1,inventory_generation,inventory_baseline_revision FROM libraries WHERE id=$1::uuid`, f.registration.Library.ID, d.RootID, name)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	for {
		page, e := f.s.NextFamilyIgnoreBaselinePage(f.ctx, l)
		if e != nil {
			t.Fatal(e)
		}
		if page.Complete {
			break
		}
		var evaluations []domain.FamilyBaselineEvaluation
		for _, candidate := range page.Unseen {
			evaluated, e := scanner.EvaluateFamilyIgnoreBaseline(f.ctx, d.RootPath, candidate, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive})
			if e != nil {
				t.Fatal(e)
			}
			evaluations = append(evaluations, evaluated)
		}
		if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, page.Token, evaluations); err != nil {
			t.Fatal("native classification storage", err)
		}
	}
	counts, _, complete := comparisonCounts(t, f, l.Job.ID)
	if !complete || counts != (domain.IgnoreComparisonCounts{Observed: 1, Missing: 1, Excluded: 4}) {
		t.Fatal("native classification counts", counts)
	}
	if err = f.s.BeginFamilyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	observer := scan.NewIgnoreScanner()
	for {
		page, e := f.s.NextFamilyIgnoreVerificationPage(f.ctx, l)
		if e != nil {
			t.Fatal(e)
		}
		if page.Complete {
			break
		}
		var observed []domain.IgnoreDirectoryProof
		for _, proof := range page.Proofs {
			p, e := observer.ReobserveIgnoreProof(f.ctx, d.RootPath, proof)
			if e != nil {
				t.Fatal(e)
			}
			observed = append(observed, p)
		}
		if err = f.s.CommitFamilyIgnoreVerificationPage(f.ctx, l, page.Token, observed); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.s.SealFamilyIgnoreVerification(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("custom-only seal", err)
	}
	for {
		page, e := f.s.NextLegacyIgnoreVerificationPage(f.ctx, l)
		if e != nil {
			t.Fatal(e)
		}
		if page.Complete {
			break
		}
		var observed []domain.LegacyIgnoreObservation
		for _, source := range page.Observations {
			o, e := observer.ReobserveLegacyIgnore(f.ctx, d.RootPath, source)
			if e != nil {
				t.Fatal(e)
			}
			observed = append(observed, o)
		}
		if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, page.Token, observed); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.s.SealFamilyIgnoreVerification(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("missing-boundary verification omitted", err)
	}
	for {
		page, e := f.s.NextLegacyIgnoreBaselineVerificationPage(f.ctx, l)
		if e != nil {
			t.Fatal(e)
		}
		if page.Complete {
			break
		}
		var observed []domain.LegacyIgnoreBaselineObservation
		for _, source := range page.Observations {
			o, e := observer.ReobserveLegacyIgnoreBaseline(f.ctx, d.RootPath, source)
			if e != nil {
				t.Fatal(e)
			}
			observed = append(observed, o)
		}
		if err = f.s.CommitLegacyIgnoreBaselineVerificationPage(f.ctx, l, page.Token, observed); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.s.SealFamilyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal("joint native seal", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET missing_percent_limit=100 WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.FinishFamilyIgnoreJob(f.ctx, l); err != nil {
		t.Fatal("native publication", err)
	}
	job := f.get(t, l.Job.ID)
	if job.State != domain.JobSucceeded || job.ReviewRequired || job.Missing != 1 {
		t.Fatal("native publication outcome", job)
	}
	var total, historical, inventory int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE observed_revision=1),(SELECT count(*) FROM job_inventory WHERE job_id=$2::uuid) FROM library_inventory_baseline WHERE library_id=$1::uuid`, f.registration.Library.ID, l.Job.ID).Scan(&total, &historical, &inventory); err != nil || historical != 4 || total != inventory+4 {
		t.Fatal("native protected merge", total, historical, inventory, err)
	}
	if runner.Stats().Active != 0 {
		t.Fatal("helper still active")
	}
}
