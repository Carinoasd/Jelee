//go:build linux || windows

package postgres

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

func TestIgnoreRunnerNativeClaimAndPublish(t *testing.T) { runIgnoreRunnerNative(t, false, "") }
func TestIgnoreRunnerNativeNFO(t *testing.T)             { runIgnoreRunnerNative(t, true, "") }
func TestIgnoreRunnerNativeResume(t *testing.T)          { runIgnoreRunnerNative(t, false, "resume") }
func TestIgnoreRunnerNativeChangedSource(t *testing.T)   { runIgnoreRunnerNative(t, false, "changed") }
func runIgnoreRunnerNative(t *testing.T, withNFO bool, mode string) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	nf := newNFOFixture(t)
	f := nf.jobFixture
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{".jeleeignore": "*.tmp\n", "keep.mkv": "video", "skip.tmp": "excluded"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var job domain.Job
	if withNFO {
		for name, data := range map[string]string{"keep.nfo": "<movie><title>kept</title></movie>", "excluded.nfo": "<movie><broken>"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, ".jeleeignore"), []byte("*.tmp\nexcluded.nfo\n"), 0600); err != nil {
			t.Fatal(err)
		}
		var err error
		job, _, err = f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "real-runner", domain.JobPriorityManual, domain.ScanIntent{Ignore: ignoreTestIntent(), NFO: true}, f.policy, nil, &nf.identity)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		job = ignoreSubmit(t, f, "real-runner")
	}
	if _, err := f.s.ClaimJob(f.ctx, "incapable", false, time.Minute); err != domain.ErrNotFound {
		t.Fatal("incapable worker admitted", err)
	}
	scanner := scan.NewIgnoreScanner()
	if mode == "resume" {
		lease, err := f.s.ClaimJobWithCapabilities(f.ctx, "prior-worker", false, time.Minute, domain.ScanCapabilities{Ignore: true})
		if err != nil {
			t.Fatal(err)
		}
		d, err := f.s.NextIgnoreScanDirectory(f.ctx, lease)
		if err != nil {
			t.Fatal(err)
		}
		if err = scanner.ScanIgnoreDirectory(f.ctx, d, ignoreTestIntent(), func(b domain.IgnoreScanBatch) error { return f.s.SaveIgnoreScanBatch(f.ctx, lease, d, b) }); err != nil {
			t.Fatal(err)
		}
		classifyForPublication(t, f, lease, func(*domain.IgnoreBaselineDecision) {}, false)
		if err = f.s.ReleaseJob(f.ctx, lease); err != nil {
			t.Fatal(err)
		}
	}
	opts := jobs.DefaultOptions()
	opts.Workers = 1
	if withNFO {
		reader, err := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
		if err != nil {
			t.Fatal(err)
		}
		observed, err := nfo.NewObservedReader(reader)
		if err != nil {
			t.Fatal(err)
		}
		opts.NFO = &jobs.NFOOptions{Repository: f.s, Reader: observed, MaxConcurrent: 1}
	}
	opts.Ignore = &jobs.IgnoreOptions{Repository: f.s, Scanner: scanner, Observer: scanner}
	if mode == "changed" {
		opts.Ignore.Observer = &changingIgnoreObserver{IgnoreBaselineObserver: scanner, root: root}
	}
	runner, err := jobs.New(f.s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("worker did not finish")
		case <-ticker.C:
			current := f.get(t, job.ID)
			if current.State == domain.JobFailed || current.State == domain.JobCancelled {
				if mode == "changed" && current.State == domain.JobFailed {
					var count int
					if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`, job.LibraryID).Scan(&count); err != nil || count != 0 {
						t.Fatal("changed source published", err)
					}
					return
				}
				t.Fatal("worker failed", current.ErrorCode)
			}
			if current.State != domain.JobSucceeded {
				continue
			}
			if mode == "changed" {
				t.Fatal("changed source succeeded")
			}
			if mode == "resume" && current.Attempts != 2 {
				t.Fatal("job was not reclaimed")
			}
			wantFiles := int64(2)
			if withNFO {
				wantFiles = 3
			}
			if current.Files != wantFiles || current.ReviewRequired {
				t.Fatal("unexpected filtered result", current)
			}
			var excluded int
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid AND path IN ('skip.tmp','excluded.nfo')`, job.LibraryID).Scan(&excluded); err != nil || excluded != 0 {
				t.Fatal("excluded media published", err)
			}
			if withNFO {
				var parsed, valid, invalid int
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT parsed,valid,invalid FROM nfo_job_state WHERE job_id=$1::uuid`, job.ID).Scan(&parsed, &valid, &invalid); err != nil || parsed != 1 || valid != 1 || invalid != 0 {
					t.Fatal("excluded NFO entered parser", parsed, valid, invalid, err)
				}
			}
			return
		}
	}
}

type changingIgnoreObserver struct {
	app.IgnoreBaselineObserver
	root    string
	changed bool
}

func (o *changingIgnoreObserver) ReobserveIgnoreProof(ctx context.Context, root string, p domain.IgnoreDirectoryProof) (domain.IgnoreDirectoryProof, error) {
	if !o.changed {
		o.changed = true
		if err := os.WriteFile(filepath.Join(o.root, ".jeleeignore"), []byte("*.mkv\n"), 0600); err != nil {
			return domain.IgnoreDirectoryProof{}, err
		}
	}
	return o.IgnoreBaselineObserver.ReobserveIgnoreProof(ctx, root, p)
}
