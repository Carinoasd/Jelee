//go:build !race && (linux || windows)

package postgres

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFamilyRunnerNativePublish(t *testing.T) { runFamilyRunnerNative(t, false, false, "") }
func TestFamilyRunnerNativeNFO(t *testing.T)     { runFamilyRunnerNative(t, true, false, "") }
func TestFamilyRunnerNativeResume(t *testing.T)  { runFamilyRunnerNative(t, false, true, "") }

func TestFamilyRunnerNativeChangedSource(t *testing.T) {
	runFamilyRunnerNative(t, false, false, "changed")
}
func TestFamilyRunnerNativeUnknown(t *testing.T) { runFamilyRunnerNative(t, false, false, "unknown") }

func runFamilyRunnerNative(t *testing.T, withNFO, resume bool, mode string) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	nf := newNFOFixture(t)
	f := nf.jobFixture
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{".jeleeignore": "!keep.mkv\ncustom.tmp\n", ".ignore": "*.mkv\n", "keep.mkv": "keep", "drop.mkv": "drop", "custom.tmp": "custom", "hidden/.ignore": ""}
	if withNFO {
		files["movie.nfo"] = "<movie><title>Included</title></movie>"
		files["hidden/movie.nfo"] = "malformed excluded NFO"
	}
	for name, data := range files {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var job domain.Job
	if withNFO {
		var err error
		job, _, err = f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "family-native", domain.JobPriorityManual, domain.ScanIntent{Ignore: ignoreTestIntent(), NFO: true}, f.policy, nil, &nf.identity)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		job = ignoreSubmit(t, f, "family-native")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM job_ignore_requests WHERE job_id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_requests(job_id,library_id,mode,case_mode,program_version,proof_version)VALUES($1::uuid,$2::uuid,'jeleeignore-legacy-v1','sensitive','jeleeignore-legacy-v1','jeleeignore-legacy-proof-v1')`, job.ID, job.LibraryID); err != nil {
		t.Fatal(err)
	}
	for _, capability := range []domain.ScanCapabilities{{}, {Ignore: true}, {Probe: true, NFO: true, Ignore: true}} {
		if _, err := f.s.ClaimJobWithCapabilities(f.ctx, "incapable", false, time.Minute, capability); err != domain.ErrNotFound {
			t.Fatal("old capability claimed family", err)
		}
	}
	helper, err := process.NewIgnoreRunner(t.TempDir(), 2, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	scanner := scan.NewFamilyIgnoreScanner(helper)
	if resume {
		lease, e := f.s.ClaimJobWithCapabilities(f.ctx, "prior-worker", false, time.Minute, domain.ScanCapabilities{FamilyIgnore: true})
		if e != nil {
			t.Fatal(e)
		}
		d, e := f.s.NextFamilyIgnoreScanDirectory(f.ctx, lease)
		if e != nil {
			t.Fatal(e)
		}
		if e = scanner.ScanFamilyIgnoreDirectory(f.ctx, d, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}, func(b domain.FamilyIgnoreScanBatch) error { return f.s.SaveFamilyIgnoreScanBatch(f.ctx, lease, d, b) }); e != nil {
			t.Fatal(e)
		}
		if e = f.s.ReleaseJob(f.ctx, lease); e != nil {
			t.Fatal(e)
		}
	}
	opts := jobs.DefaultOptions()
	opts.Workers = 1
	opts.FamilyIgnore = &jobs.FamilyIgnoreOptions{Repository: f.s, Scanner: scanner}
	if mode != "" {
		opts.FamilyIgnore.Scanner = &familyFaultScanner{FamilyIgnoreScanner: scanner, root: root, mode: mode}
	}
	if mode == "unknown" {
		if _, e := f.s.Pool.Exec(f.ctx, `INSERT INTO library_inventory_baseline(library_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision)SELECT id,$2::uuid,'gone/plain.txt',true,'video',7,1,inventory_generation,inventory_baseline_revision FROM libraries WHERE id=$1::uuid`, job.LibraryID, f.registration.RootID); e != nil {
			t.Fatal(e)
		}
	}
	if withNFO {
		reader, e := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
		if e != nil {
			t.Fatal(e)
		}
		observed, e := nfo.NewObservedReader(reader)
		if e != nil {
			t.Fatal(e)
		}
		opts.NFO = &jobs.NFOOptions{Repository: f.s, Reader: observed, MaxConcurrent: 1}
	}
	runner, err := jobs.New(f.s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := runner.Stop(c); e != nil {
			t.Error(e)
		}
	}()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("family worker did not finish")
		case <-ticker.C:
			current := f.get(t, job.ID)
			if current.State == domain.JobFailed || current.State == domain.JobCancelled {
				if mode == "changed" && current.State == domain.JobFailed {
					var count int
					if e := f.s.Pool.QueryRow(f.ctx, `SELECT count(*)FROM library_inventory_baseline WHERE library_id=$1::uuid`, job.LibraryID).Scan(&count); e != nil || count != 0 {
						t.Fatal("changed source published baseline", e)
					}
					return
				}
				t.Fatal("family worker failed", current.ErrorCode)
			}
			if current.State != domain.JobSucceeded {
				continue
			}
			if mode == "changed" {
				t.Fatal("changed source succeeded")
			}
			if mode == "unknown" {
				if !current.ReviewRequired || current.Missing != 0 {
					t.Fatal("unknown became confirmed absence")
				}
				var total, retained int
				if e := f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*)FILTER(WHERE path='gone/plain.txt' AND observed_revision=1)FROM library_inventory_baseline WHERE library_id=$1::uuid`, job.LibraryID).Scan(&total, &retained); e != nil || total != 1 || retained != 1 {
					t.Fatal("unknown changed baseline", e)
				}
				return
			}
			if current.ReviewRequired || current.Missing != 0 {
				t.Fatal("unexpected publication", current)
			}
			if resume && current.Attempts != 2 {
				t.Fatal("recovery did not reclaim")
			}
			var kept, excluded int
			if e := f.s.Pool.QueryRow(f.ctx, `SELECT count(*)FILTER(WHERE path='keep.mkv'),count(*)FILTER(WHERE path IN('drop.mkv','custom.tmp','hidden/movie.nfo'))FROM library_inventory_baseline WHERE library_id=$1::uuid`, job.LibraryID).Scan(&kept, &excluded); e != nil || kept != 1 || excluded != 0 {
				t.Fatal("incorrect filtered baseline", kept, excluded, e)
			}
			if withNFO {
				var parsed, valid, invalid int
				if e := f.s.Pool.QueryRow(f.ctx, `SELECT parsed,valid,invalid FROM nfo_job_state WHERE job_id=$1::uuid`, job.ID).Scan(&parsed, &valid, &invalid); e != nil || parsed != 1 || valid != 1 || invalid != 0 {
					t.Fatal("filtered NFO phase", parsed, valid, invalid, e)
				}
			}
			if helper.Stats().Active != 0 {
				t.Fatal("helper remains active")
			}
			return
		}
	}
}

type familyFaultScanner struct {
	app.FamilyIgnoreScanner
	root, mode string
}

func (s *familyFaultScanner) EvaluateFamilyIgnoreBaseline(ctx context.Context, root string, c domain.IgnoreBaselineCandidate, i domain.IgnoreIntent) (domain.FamilyBaselineEvaluation, error) {
	if s.mode == "unknown" {
		return domain.FamilyBaselineEvaluation{}, domain.ErrIgnoreUnavailable
	}
	return s.FamilyIgnoreScanner.EvaluateFamilyIgnoreBaseline(ctx, root, c, i)
}
func (s *familyFaultScanner) ReobserveLegacyIgnore(ctx context.Context, root string, p domain.LegacyIgnoreObservation) (domain.LegacyIgnoreObservation, error) {
	if s.mode == "changed" {
		if err := os.WriteFile(filepath.Join(s.root, ".ignore"), []byte("changed\n"), 0600); err != nil {
			return domain.LegacyIgnoreObservation{}, err
		}
	}
	return s.FamilyIgnoreScanner.ReobserveLegacyIgnore(ctx, root, p)
}
