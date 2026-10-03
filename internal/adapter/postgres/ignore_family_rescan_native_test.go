//go:build !race && (linux || windows)

package postgres

import (
	"context"
	"fmt"
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

func TestFamilyRunnerNativeRescanSameSizeAndMtime(t *testing.T) {
	for _, percent := range []int{50, 100} {
		t.Run(fmt.Sprintf("missing-percent-%d", percent), func(t *testing.T) {
			familyRunnerNativeRescan(t, percent)
		})
	}
}

func familyRunnerNativeRescan(t *testing.T, percent int) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f := newJobFixture(t)
	f.policy.MissingPercentLimit = percent
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	media := map[string]string{"a.mkv": "first-media", "b.mkv": "second-media"}
	for name, data := range media {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	helper, err := process.NewIgnoreRunner(t.TempDir(), 2, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewJobsWithScanStages(f.s, f.policy, f.s, app.ScanServices{NFOAdmin: f.s, NFOQueries: f.s, Images: f.s, NFOAvailable: func() bool { return false }, FamilyIgnoreAvailable: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	opts := jobs.DefaultOptions()
	opts.Workers = 1
	opts.FamilyIgnore = &jobs.FamilyIgnoreOptions{Repository: f.s, Scanner: scan.NewFamilyIgnoreScanner(helper)}
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
		if err := runner.Stop(c); err != nil {
			t.Error(err)
		}
	}()
	rounds := []struct{ custom, legacy, excluded, family string }{
		{"!a.mkv\n", "*.mkv\n", "b.mkv", domain.IgnoreFamilyLegacy},
		{"!b.mkv\n", "*.mkv\n", "a.mkv", domain.IgnoreFamilyLegacy},
		{"a.mkv\n", "!a.mkv\n", "a.mkv", domain.IgnoreFamilyCustom},
		{"!a.mkv\n", "a.mkv\n", "", ""},
		{"!a.mkv\n", "b.mkv\n", "b.mkv", domain.IgnoreFamilyLegacy},
		{"", "", "", ""},
	}
	stamp := time.Unix(1700000000, 0)
	previous := map[string]int64{}
	for round, rules := range rounds {
		wantReview := round == len(rounds)-1 && percent == 50
		for name, data := range map[string]string{".jeleeignore": rules.custom, ".ignore": rules.legacy} {
			path := filepath.Join(root, name)
			if data == "" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Size() != int64(len(data)) || !info.ModTime().Equal(stamp) {
				t.Fatal("same-mtime rule fixture")
			}
		}
		job, replay, err := service.SubmitScanOptions(f.ctx, f.a, f.registration.Library.ID, fmt.Sprintf("family-rescan-%d", round), domain.JobPriorityManual, false, false, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive})
		if err != nil || replay {
			t.Fatal("family rescan admission", err)
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			current := f.get(t, job.ID)
			if current.State == domain.JobSucceeded {
				missing := int64(0)
				if round == len(rounds)-1 {
					missing = 2
				}
				if current.ReviewRequired != wantReview || current.Missing != missing {
					t.Fatal("rule removal review threshold or missing count", round, current.Missing, current.ReviewRequired)
				}
				break
			}
			if current.State == domain.JobFailed || current.State == domain.JobCancelled || time.Now().After(deadline) {
				t.Fatal("family rescan failed", round, current.State, current.ErrorCode)
			}
			time.Sleep(20 * time.Millisecond)
		}
		report, err := service.IgnoreReport(f.ctx, f.a, job.ID, 100, "")
		wantExcluded := int64(0)
		if rules.excluded != "" {
			wantExcluded = 1
		}
		if err != nil || report.ReviewRequired != wantReview || report.ExcludedFiles != wantExcluded || report.Unknown != 0 {
			t.Fatal("family report counters", round, err)
		}
		scanRows, baselineRows := 0, 0
		for _, entry := range report.Entries {
			if round == len(rounds)-1 && entry.Source == "baseline" && (entry.Path == ".jeleeignore" || entry.Path == ".ignore") && entry.Outcome == domain.IgnoreBaselineMissing {
				continue
			}
			if entry.Path != rules.excluded || entry.Outcome != domain.IgnoreBaselineExcluded || entry.Family != rules.family || entry.Reason != domain.IgnoreReasonRule || entry.RuleLine != 1 || entry.RuleDirectory != "." || entry.MatchedPath != entry.Path {
				t.Fatal("stale or wrong-family provenance", round)
			}
			if entry.Source == "scan" {
				scanRows++
			} else {
				baselineRows++
			}
		}
		wantBaseline := 0
		if rules.excluded != "" && previous[rules.excluded] != 0 {
			wantBaseline = 1
		}
		if int64(scanRows) != wantExcluded || baselineRows != wantBaseline {
			t.Fatal("scan or historical provenance missing", round, scanRows, baselineRows)
		}
		for name, data := range media {
			var count, revision, size int64
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*),COALESCE(max(observed_revision),0),COALESCE(max(size),0) FROM library_inventory_baseline WHERE library_id=$1::uuid AND path=$2`, f.registration.Library.ID, name).Scan(&count, &revision, &size); err != nil {
				t.Fatal(err)
			}
			if round == 0 && name == rules.excluded {
				if count != 0 {
					t.Fatal("initially excluded media entered baseline")
				}
				continue
			}
			preserve := wantReview || name == rules.excluded
			if count != 1 || size != int64(len(data)) || (preserve && revision != previous[name]) || (!preserve && revision <= previous[name]) {
				t.Fatal("baseline revision did not preserve exclusion or refresh inclusion", round, name)
			}
			previous[name] = revision
			actual, err := os.ReadFile(filepath.Join(root, name))
			if err != nil || string(actual) != data {
				t.Fatal("rescan modified media")
			}
		}
		if round == len(rounds)-1 {
			var controls int64
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid AND path IN ('.ignore','.jeleeignore')`, f.registration.Library.ID).Scan(&controls); err != nil {
				t.Fatal(err)
			}
			wantControls := int64(0)
			if wantReview {
				wantControls = 2
			}
			if controls != wantControls {
				t.Fatal("review must retain baseline; accepted removal must delete control records", controls)
			}
		}
		if helper.Stats().Active != 0 {
			t.Fatal("helper survived completed rescan")
		}
	}
}
