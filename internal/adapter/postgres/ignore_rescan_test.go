//go:build linux || windows

package postgres

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

func TestIgnoreRunnerRescanSameSizeAndMtime(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f := newJobFixture(t)
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"a.mkv": "first-media", "b.mkv": "second-media"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	scanner := scan.NewIgnoreScanner()
	opts := jobs.DefaultOptions()
	opts.Workers = 1
	opts.Ignore = &jobs.IgnoreOptions{Repository: f.s, Scanner: scanner, Observer: scanner}
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
	ruleFile := filepath.Join(root, ".jeleeignore")
	stamp := time.Unix(1700000000, 0)
	previousRevision := map[string]int64{}
	for stage, excluded := range []string{"a.mkv", "b.mkv", "a.mkv", ""} {
		if excluded == "" {
			if err = os.Remove(ruleFile); err != nil {
				t.Fatal(err)
			}
		} else {
			if err = os.WriteFile(ruleFile, []byte(excluded+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.Chtimes(ruleFile, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(ruleFile)
			if err != nil || info.Size() != 6 || !info.ModTime().Equal(stamp) {
				t.Fatal("same-size/mtime fixture failed", err)
			}
		}
		job := ignoreSubmit(t, f, fmt.Sprintf("rescan-%d", stage))
		deadline := time.Now().Add(15 * time.Second)
		for {
			current := f.get(t, job.ID)
			if current.State == domain.JobSucceeded {
				wantMissing := int64(0)
				if excluded == "" {
					wantMissing = 1 // Only the deliberately removed rule file.
				}
				if current.ReviewRequired || current.Missing != wantMissing {
					t.Fatal("rule change became missing or review", stage)
				}
				break
			}
			if current.State == domain.JobFailed || current.State == domain.JobCancelled || time.Now().After(deadline) {
				t.Fatal("rescan did not succeed", stage, current.State, current.ErrorCode)
			}
			time.Sleep(20 * time.Millisecond)
		}
		report, err := f.s.GetIgnoreReport(f.ctx, f.a, job.ID, 100, "")
		wantExcluded := int64(1)
		if excluded == "" {
			wantExcluded = 0
		}
		if err != nil || report.ExcludedFiles != wantExcluded || report.Unknown != 0 {
			t.Fatal("rescan report counters", stage, err)
		}
		scanCount, baselineCount := 0, 0
		for _, entry := range report.Entries {
			// Removing the rule file itself is a real included absence.
			if excluded == "" && entry.Source == "baseline" && entry.Path == ".jeleeignore" && entry.Outcome == domain.IgnoreBaselineMissing {
				continue
			}
			if entry.Path != excluded || entry.Outcome != domain.IgnoreBaselineExcluded || entry.RuleDirectory != "." || entry.RuleLine != 1 || entry.MatchedPath != excluded {
				t.Fatal("stale rule decision survived content change", stage, entry)
			}
			if entry.Source == "scan" {
				scanCount++
			} else {
				baselineCount++
			}
		}
		wantBaseline := 1
		if stage == 0 || excluded == "" {
			wantBaseline = 0
		}
		if int64(scanCount) != wantExcluded || baselineCount != wantBaseline {
			t.Fatal("missing current/historical rule provenance", stage, scanCount, baselineCount)
		}
		for _, name := range []string{"a.mkv", "b.mkv"} {
			var count, revision, size int64
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*),COALESCE(max(observed_revision),0),COALESCE(max(size),0) FROM library_inventory_baseline WHERE library_id=$1::uuid AND path=$2`, f.registration.Library.ID, name).Scan(&count, &revision, &size); err != nil {
				t.Fatal(err)
			}
			if stage == 0 && name == excluded {
				if count != 0 {
					t.Fatal("initial exclusion entered baseline")
				}
				continue
			}
			wantSize := int64(11)
			if name == "b.mkv" {
				wantSize = 12
			}
			if count != 1 || size != wantSize || name == excluded && revision != previousRevision[name] || name != excluded && revision <= previousRevision[name] {
				t.Fatal("baseline did not preserve excluded or refresh included media", stage, name, revision)
			}
			previousRevision[name] = revision
		}
	}
}
