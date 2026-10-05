//go:build !race && (linux || windows)

package postgres

import (
	"context"
	"errors"
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
func TestFamilyRunnerNativeExpiredLease(t *testing.T) {
	runFamilyRunnerNative(t, false, true, "expired")
}
func TestFamilyRunnerNativeCancelVerification(t *testing.T) {
	runFamilyRunnerNative(t, false, false, "cancel")
}

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
	helper, err := process.NewIgnoreRunner(t.TempDir(), 2, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewJobsWithScanStages(f.s, f.policy, f.s, app.ScanServices{NFOAdmin: f.s, NFOQueries: f.s, Images: f.s, NFOIdentity: &nf.identity, NFOAvailable: func() bool { return true }, FamilyIgnoreAvailable: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	job, replay, err := service.SubmitScanOptions(f.ctx, f.a, f.registration.Library.ID, "family-native", domain.JobPriorityManual, false, withNFO, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive})
	if err != nil || replay {
		t.Fatal("formal family admission", err)
	}
	for _, capability := range []domain.ScanCapabilities{{}, {Ignore: true}, {Probe: true, NFO: true, Ignore: true}} {
		if _, err := f.s.ClaimJobWithCapabilities(f.ctx, "incapable", false, time.Minute, capability); err != domain.ErrNotFound {
			t.Fatal("old capability claimed family", err)
		}
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
		var savedBatch domain.FamilyIgnoreScanBatch
		if e = scanner.ScanFamilyIgnoreDirectory(f.ctx, d, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}, func(b domain.FamilyIgnoreScanBatch) error {
			savedBatch = b
			return f.s.SaveFamilyIgnoreScanBatch(f.ctx, lease, d, b)
		}); e != nil {
			t.Fatal(e)
		}
		if mode == "expired" {
			if _, e = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, lease.Job.ID); e != nil {
				t.Fatal(e)
			}
			for name, call := range map[string]func() error{
				"progress": func() error { _, e := f.s.ReadFamilyIgnoreProgress(f.ctx, lease); return e },
				"save":     func() error { return f.s.SaveFamilyIgnoreScanBatch(f.ctx, lease, d, savedBatch) },
				"finish":   func() error { return f.s.FinishFamilyIgnoreJob(f.ctx, lease) },
			} {
				if err := call(); !errors.Is(err, domain.ErrJobLeaseLost) {
					t.Fatal("expired family owner retained authority", name, err)
				}
			}
		} else if e = f.s.ReleaseJob(f.ctx, lease); e != nil {
			t.Fatal(e)
		}
	}
	opts := jobs.DefaultOptions()
	opts.Workers = 1
	opts.FamilyIgnore = &jobs.FamilyIgnoreOptions{Repository: f.s, Scanner: scanner}
	entered := make(chan struct{}, 1)
	if mode != "" {
		opts.FamilyIgnore.Scanner = &familyFaultScanner{FamilyIgnoreScanner: scanner, root: root, mode: mode, entered: entered}
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
	if mode == "cancel" {
		select {
		case <-entered:
		case <-time.After(15 * time.Second):
			t.Fatal("family verification did not reach cancellation barrier")
		}
		if _, err := service.Cancel(f.ctx, f.a, job.ID); err != nil {
			t.Fatal(err)
		}
	}
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
				if mode == "cancel" && current.State == domain.JobCancelled {
					var count int
					if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`, job.LibraryID).Scan(&count); err != nil || count != 0 || current.Missing != 0 || helper.Stats().Active != 0 {
						t.Fatal("cancelled verification published baseline or retained helper", err)
					}
					for name, data := range files {
						actual, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
						if err != nil || string(actual) != data {
							t.Fatal("cancellation modified original files")
						}
					}
					return
				}
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
			report, e := service.IgnoreReport(f.ctx, f.a, job.ID, 100, "")
			if e != nil || !report.Enabled || report.ReviewRequired != current.ReviewRequired {
				t.Fatal("formal worker report", e)
			}
			custom, legacy := 0, 0
			for _, entry := range report.Entries {
				if entry.Source == "scan" {
					switch entry.Family {
					case domain.IgnoreFamilyCustom:
						custom++
					case domain.IgnoreFamilyLegacy:
						legacy++
					default:
						t.Fatal("worker exclusion lost family")
					}
				}
			}
			if custom != 1 || legacy != 2 {
				t.Fatal("worker report missing source families", custom, legacy)
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
	entered    chan struct{}
}

func (s *familyFaultScanner) EvaluateFamilyIgnoreBaseline(ctx context.Context, root string, c domain.IgnoreBaselineCandidate, i domain.IgnoreIntent) (domain.FamilyBaselineEvaluation, error) {
	if s.mode == "unknown" {
		return domain.FamilyBaselineEvaluation{}, domain.ErrIgnoreUnavailable
	}
	return s.FamilyIgnoreScanner.EvaluateFamilyIgnoreBaseline(ctx, root, c, i)
}
func (s *familyFaultScanner) ReobserveLegacyIgnore(ctx context.Context, root string, p domain.LegacyIgnoreObservation) (domain.LegacyIgnoreObservation, error) {
	if s.mode == "cancel" {
		if _, err := s.FamilyIgnoreScanner.ReobserveLegacyIgnore(ctx, root, p); err != nil {
			return domain.LegacyIgnoreObservation{}, err
		}
		select {
		case s.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return domain.LegacyIgnoreObservation{}, ctx.Err()
	}
	if s.mode == "changed" {
		if err := os.WriteFile(filepath.Join(s.root, ".ignore"), []byte("changed\n"), 0600); err != nil {
			return domain.LegacyIgnoreObservation{}, err
		}
	}
	return s.FamilyIgnoreScanner.ReobserveLegacyIgnore(ctx, root, p)
}
