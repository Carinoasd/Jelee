//go:build linux && !race

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"golang.org/x/sys/unix"
)

type measuredFamilyScanner struct {
	app.FamilyIgnoreScanner
	maxBatch atomic.Int64
	batches  atomic.Int64
}

func (s *measuredFamilyScanner) ScanFamilyIgnoreDirectory(ctx context.Context, d domain.ScanDirectory, intent domain.IgnoreIntent, emit func(domain.FamilyIgnoreScanBatch) error) error {
	return s.FamilyIgnoreScanner.ScanFamilyIgnoreDirectory(ctx, d, intent, func(b domain.FamilyIgnoreScanBatch) error {
		s.batches.Add(1)
		n := int64(len(b.Inventory.Entries) + len(b.Inventory.Directories) + len(b.Excluded))
		for old := s.maxBatch.Load(); n > old; old = s.maxBatch.Load() {
			if s.maxBatch.CompareAndSwap(old, n) {
				break
			}
		}
		return emit(b)
	})
}

func (s *measuredFamilyScanner) EvaluateFamilyIgnoreBaselineBatch(ctx context.Context, root string, candidates []domain.IgnoreBaselineCandidate, intent domain.IgnoreIntent) ([]domain.FamilyBaselineEvaluation, error) {
	return s.FamilyIgnoreScanner.(app.FamilyIgnoreBaselineBatchScanner).EvaluateFamilyIgnoreBaselineBatch(ctx, root, candidates, intent)
}

func ignoreScalePath(i int) string {
	prefix := "keep"
	if i%2 == 1 {
		prefix = "drop"
	}
	return filepath.Join(fmt.Sprintf("bucket-%03d", i/1000), fmt.Sprintf("%s-%06d.mkv", prefix, i))
}

// Opt-in real filesystem/worker/history test. The two rule files count toward
// the 500,000 entry limit. Sharding bounds each directory to 1,000 media files;
// this fixture does not claim flat-directory, image or probe performance.
func TestIgnoreScanScale(t *testing.T) {
	if os.Getenv("JELEE_RUN_IGNORE_SCALE") != "true" {
		t.Skip("ignore scale NOT RUN: JELEE_RUN_IGNORE_SCALE unset")
	}
	count, err := strconv.Atoi(os.Getenv("JELEE_SCAN_SCALE_FILES"))
	if err != nil || (count != 10000 && count != 100000 && count != 500000) {
		t.Fatal("ignore scale requires 10000, 100000 or 500000 total files")
	}
	output := os.Getenv("JELEE_SCAN_SCALE_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("absolute owned output directory required")
	}
	if err = os.Mkdir(output, 0700); err != nil {
		t.Fatal("output must not exist")
	}
	root := t.TempDir()
	var fs unix.Statfs_t
	if unix.Statfs(root, &fs) != nil || fs.Type != unix.EXT4_SUPER_MAGIC || fs.Bavail*uint64(fs.Bsize) < uint64(count)*8192+1<<30 || fs.Ffree < uint64(count)+10000 {
		t.Fatal("native ext4 with sufficient free space and inodes required")
	}
	ctx, s, _ := accountTestStoreWithTimeout(t, 2*time.Hour)
	if _, err = s.BootstrapAdmin(ctx, accountInput("ignore-scale")); err != nil {
		t.Fatal("bootstrap scale account")
	}
	a := accountActor(accountLogin(t, ctx, s, "ignore-scale"))
	registration, err := s.RegisterLibrary(ctx, "ignore-scale", root)
	if err != nil {
		t.Fatal("register scale library")
	}
	data := []byte("Jelee synthetic inventory fixture\n")
	mediaCount := count - 2
	for i := 0; i < mediaCount; i++ {
		name := filepath.Join(root, ignoreScalePath(i))
		if i%1000 == 0 {
			if err = os.Mkdir(filepath.Dir(name), 0700); err != nil {
				t.Fatal("create fixture directory")
			}
		}
		if err = os.WriteFile(name, data, 0600); err != nil {
			t.Fatal("create fixture media")
		}
	}
	custom := "# scale\n"
	if err = os.WriteFile(filepath.Join(root, ".jeleeignore"), []byte(custom), 0600); err != nil {
		t.Fatal(err)
	}
	policy := jobTestPolicy()
	policy.MaxEntries = 500000
	policy.MaxDirectories = 1000
	policy.HistoryLimit = 4
	helper, err := process.NewIgnoreRunner(t.TempDir(), 2, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	measured := &measuredFamilyScanner{FamilyIgnoreScanner: scan.NewFamilyIgnoreScanner(helper)}
	service, err := app.NewJobsWithScanStages(s, policy, s, app.ScanServices{NFOAdmin: s, NFOQueries: s, Images: s, NFOAvailable: func() bool { return false }, FamilyIgnoreAvailable: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	opts := jobs.DefaultOptions()
	opts.Workers = 1
	opts.PollInterval = 100 * time.Millisecond
	opts.FamilyIgnore = &jobs.FamilyIgnoreOptions{Repository: s, Scanner: measured}
	runner, err := jobs.New(s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runner.Stop(stop); err != nil {
			t.Error(err)
		}
	}()
	type result struct {
		Pass                 string      `json:"pass"`
		TotalFiles           int         `json:"totalFiles"`
		ObservedFiles        int64       `json:"observedFiles"`
		HistoricalFiles      int64       `json:"historicalFiles"`
		RetainedBaselineRows int64       `json:"retainedBaselineRows"`
		GOMAXPROCS           int         `json:"gomaxprocs"`
		Seconds              float64     `json:"seconds"`
		State                string      `json:"state"`
		ErrorCode            string      `json:"errorCode"`
		Attempts             int         `json:"attempts"`
		MaxBatch             int64       `json:"maxBatch"`
		Batches              int64       `json:"batches"`
		HelperStarts         uint64      `json:"helperStarts"`
		PeakHeap             uint64      `json:"peakHeapBytes"`
		PeakRSS              uint64      `json:"peakRSSBytes"`
		AfterGC              scaleSample `json:"afterGC"`
	}
	var results []result
	for round, pass := range []string{"initial", "ignored", "cleanup"} {
		legacy := "!*.mkv\n"
		wantHistory := int64(0)
		if round > 0 {
			legacy = "drop-*.mkv\n"
			wantHistory = int64(mediaCount / 2)
		}
		if round < 2 {
			if err = os.WriteFile(filepath.Join(root, ".ignore"), []byte(legacy), 0600); err != nil {
				t.Fatal(err)
			}
		}
		wantObserved := int64(count) - wantHistory
		wantBytes := (wantObserved-2)*int64(len(data)) + int64(len(custom)+len(legacy))
		measured.maxBatch.Store(0)
		measured.batches.Store(0)
		starts := helper.Stats().Started
		scaleProfile(t, filepath.Join(output, pass+"-before.heap"))
		log, err := os.OpenFile(filepath.Join(output, pass+"-samples.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		job, replay, err := service.SubmitScanOptions(ctx, a, registration.Library.ID, "ignore-scale-"+pass, domain.JobPriorityManual, false, false, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive})
		if err != nil || replay {
			log.Close()
			t.Fatal("submit scale scan", err)
		}
		encoder := json.NewEncoder(log)
		var heap, rss uint64
		last := time.Time{}
		tick := time.NewTicker(250 * time.Millisecond)
		for {
			job, err = s.GetJob(ctx, a, job.ID)
			if err != nil {
				tick.Stop()
				log.Close()
				t.Fatal("read scale status", err)
			}
			terminal := job.State == domain.JobSucceeded || job.State == domain.JobFailed || job.State == domain.JobCancelled
			if time.Since(last) >= time.Second || terminal {
				sample := sampleScale(t, s, start, job.Files)
				heap = max(heap, sample.Heap)
				rss = max(rss, sample.RSS)
				if err = encoder.Encode(sample); err != nil {
					tick.Stop()
					log.Close()
					t.Fatal(err)
				}
				last = time.Now()
			}
			if terminal {
				break
			}
			select {
			case <-ctx.Done():
				tick.Stop()
				log.Close()
				t.Fatal("scale timeout")
			case <-tick.C:
			}
		}
		tick.Stop()
		elapsed := time.Since(start).Seconds()
		if err = log.Close(); err != nil {
			t.Fatal(err)
		}
		var baseline, historical, current, retained int64
		if err = s.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE observed_revision=2 AND kind='video' AND $2::boolean),count(*) FILTER(WHERE observed_revision=l.inventory_baseline_revision) FROM library_inventory_baseline b JOIN libraries l ON l.id=b.library_id WHERE b.library_id=$1::uuid`, registration.Library.ID, round > 0).Scan(&baseline, &historical, &current); err != nil {
			t.Fatal(err)
		}
		if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM library_inventory_baseline_data WHERE library_id=$1::uuid`, registration.Library.ID).Scan(&retained); err != nil {
			t.Fatal(err)
		}
		scaleProfile(t, filepath.Join(output, pass+"-after.heap"))
		results = append(results, result{Pass: pass, TotalFiles: count, ObservedFiles: job.Files, HistoricalFiles: historical, RetainedBaselineRows: retained, GOMAXPROCS: runtime.GOMAXPROCS(0), Seconds: elapsed, State: job.State, ErrorCode: job.ErrorCode, Attempts: job.Attempts, MaxBatch: measured.maxBatch.Load(), Batches: measured.batches.Load(), HelperStarts: helper.Stats().Started - starts, PeakHeap: heap, PeakRSS: rss, AfterGC: sampleScale(t, s, start, job.Files)})
		body, err := json.MarshalIndent(results, "", "  ")
		if err != nil || os.WriteFile(filepath.Join(output, "result.json"), append(body, '\n'), 0600) != nil {
			t.Fatal("write scale result")
		}
		if job.State != domain.JobSucceeded || job.Attempts != 1 || job.Files != wantObserved || job.Bytes != wantBytes || job.Missing != 0 || job.Skipped != 0 || job.ReviewRequired {
			t.Fatalf("ignore scale pass=%s state=%s files=%d want=%d code=%s attempts=%d", pass, job.State, job.Files, wantObserved, job.ErrorCode, job.Attempts)
		}
		wantRetained := int64(count)
		if round > 0 {
			wantRetained *= 2
		}
		if baseline != int64(count) || historical != wantHistory || current != wantObserved || retained != wantRetained || measured.maxBatch.Load() > domain.ScanBatchMaxEntries || helper.Stats().Active != 0 {
			t.Fatal("history provenance, retained snapshots or batch bound mismatch", baseline, historical, current, retained, measured.maxBatch.Load())
		}
		t.Logf("ignore scale total=%d pass=%s observed=%d historical=%d seconds=%.3f helper_starts=%d", count, pass, job.Files, historical, elapsed, helper.Stats().Started-starts)
	}
	for _, i := range []int{0, 1, mediaCount / 2, mediaCount - 1} {
		value, err := os.ReadFile(filepath.Join(root, ignoreScalePath(i)))
		if err != nil || string(value) != string(data) {
			t.Fatal("fixture media changed")
		}
	}
}
