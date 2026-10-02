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
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	worker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"
)

type scaleQueryTrace struct{ logger *slog.Logger }
type scaleQueryKey struct{}
type scaleQueryStart struct {
	at  time.Time
	sql string
}

func (s scaleQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, scaleQueryKey{}, scaleQueryStart{time.Now(), d.SQL})
}
func (s scaleQueryTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	v := ctx.Value(scaleQueryKey{}).(scaleQueryStart)
	elapsed := time.Since(v.at)
	if elapsed >= 100*time.Millisecond || d.Err != nil {
		// SQL templates contain placeholders. Never record bind arguments or
		// raw driver errors, which can contain configured paths or credentials.
		s.logger.Info("scale database query", "milliseconds", elapsed.Milliseconds(), "sql", v.sql, "errorType", fmt.Sprintf("%T", d.Err))
	}
}

type measuredScanner struct {
	inner    *scan.Scanner
	maxBatch atomic.Int64
	batches  atomic.Int64
}

func (s *measuredScanner) ScanDirectory(ctx context.Context, d domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
	return s.inner.ScanDirectory(ctx, d, func(b domain.ScanBatch) error {
		s.batches.Add(1)
		n := int64(len(b.Entries) + len(b.Directories))
		for old := s.maxBatch.Load(); n > old; old = s.maxBatch.Load() {
			if s.maxBatch.CompareAndSwap(old, n) {
				break
			}
		}
		return emit(b)
	})
}

type scaleSample struct {
	Seconds    float64 `json:"seconds"`
	Files      int64   `json:"files"`
	Heap       uint64  `json:"heapBytes"`
	HeapInuse  uint64  `json:"heapInuseBytes"`
	RSS        uint64  `json:"rssBytes"`
	TotalAlloc uint64  `json:"totalAllocBytes"`
	GC         uint32  `json:"gcCount"`
	GCPause    uint64  `json:"gcPauseTotalNanoseconds"`
	Goroutines int     `json:"goroutines"`
	FD         int     `json:"fileDescriptors"`
	DBAcquired int32   `json:"dbAcquired"`
}

func sampleScale(t *testing.T, s *Store, start time.Time, files int64) scaleSample {
	t.Helper()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	status, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		t.Fatal("read process memory")
	}
	fields := strings.Fields(string(status))
	if len(fields) < 2 {
		t.Fatal("invalid process memory")
	}
	rss, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		t.Fatal("invalid process RSS")
	}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal("read descriptor count")
	}
	return scaleSample{Seconds: time.Since(start).Seconds(), Files: files, Heap: m.HeapAlloc, HeapInuse: m.HeapInuse, RSS: rss * uint64(os.Getpagesize()), TotalAlloc: m.TotalAlloc, GC: m.NumGC, GCPause: m.PauseTotalNs, Goroutines: runtime.NumGoroutine(), FD: len(fds), DBAcquired: s.Pool.Stat().AcquiredConns()}
}

func scaleProfile(t *testing.T, path string) {
	t.Helper()
	runtime.GC()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("create heap profile")
	}
	err = pprof.WriteHeapProfile(f)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal("write heap profile")
	}
}

// This opt-in test exercises the real filesystem, worker, transactions and
// accepted baseline. Empty-looking synthetic videos are inventory fixtures;
// they are never advertised as probe/image or full-server workload coverage.
func TestScanScale(t *testing.T) {
	if os.Getenv("JELEE_RUN_SCAN_SCALE") != "true" {
		t.Skip("scan scale NOT RUN: JELEE_RUN_SCAN_SCALE unset")
	}
	count, err := strconv.Atoi(os.Getenv("JELEE_SCAN_SCALE_FILES"))
	if err != nil || (count != 10000 && count != 100000 && count != 500000) {
		t.Fatal("scale requires 10000, 100000 or 500000 files")
	}
	output := os.Getenv("JELEE_SCAN_SCALE_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("scale output must be an absolute owned directory")
	}
	if err = os.Mkdir(output, 0700); err != nil {
		t.Fatal("scale output must not already exist")
	}
	root := t.TempDir()
	var fs unix.Statfs_t
	if unix.Statfs(root, &fs) != nil || fs.Type != unix.EXT4_SUPER_MAGIC {
		t.Fatal("scale fixtures require native ext4; do not use tmpfs or DrvFS")
	}
	if fs.Bavail*uint64(fs.Bsize) < uint64(count)*8192+1<<30 || fs.Ffree < uint64(count)+10000 {
		t.Fatal("insufficient scale fixture disk space or inodes")
	}
	ctx, s, _ := accountTestStoreWithTimeout(t, 2*time.Hour)
	if _, err = s.BootstrapAdmin(ctx, accountInput("scan-scale")); err != nil {
		t.Fatal("bootstrap scale account")
	}
	a := accountActor(accountLogin(t, ctx, s, "scan-scale"))
	registration, err := s.RegisterLibrary(ctx, "scale", root)
	if err != nil {
		t.Fatal("register scale library")
	}
	data := []byte("Jelee synthetic inventory fixture\n")
	for i := 0; i < count; i++ {
		if err = os.WriteFile(filepath.Join(root, fmt.Sprintf("video-%06d.mkv", i)), data, 0600); err != nil {
			t.Fatal("create scale fixture")
		}
	}
	policy := jobTestPolicy()
	policy.MaxEntries = 500000
	policy.HistoryLimit = 4
	policy.MaxDirectories = 1000
	measured := &measuredScanner{inner: scan.New()}
	opts := worker.DefaultOptions()
	opts.Workers = 1
	opts.PollInterval = 100 * time.Millisecond
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if os.Getenv("JELEE_SCAN_SCALE_DIAGNOSTICS") == "true" {
		log, err := os.OpenFile(filepath.Join(output, "slow-queries.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("create scale diagnostics")
		}
		t.Cleanup(func() { _ = log.Close() })
		logger = slog.New(slog.NewJSONHandler(log, nil))
		config := s.Pool.Config()
		config.ConnConfig.Tracer = scaleQueryTrace{logger}
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal("open diagnostic pool")
		}
		s.Pool.Close()
		s.Pool = pool
		t.Cleanup(pool.Close)
	}
	runner, err := worker.New(s, measured, opts, logger)
	if err != nil {
		t.Fatal("construct scale worker")
	}
	if err = runner.Start(ctx); err != nil {
		t.Fatal("start scale worker")
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if runner.Stop(stop) != nil {
			t.Error("stop scale worker")
		}
	}()
	type result struct {
		Pass           string      `json:"pass"`
		Files          int         `json:"files"`
		GOMAXPROCS     int         `json:"gomaxprocs"`
		GoVersion      string      `json:"goVersion"`
		Seconds        float64     `json:"seconds"`
		FilesPerSecond float64     `json:"filesPerSecond"`
		MaxBatch       int64       `json:"maxBatch"`
		Batches        int64       `json:"batches"`
		PeakHeap       uint64      `json:"peakHeapBytes"`
		PeakRSS        uint64      `json:"peakRSSBytes"`
		AfterGC        scaleSample `json:"afterGC"`
	}
	var results []result
	for _, pass := range []string{"initial", "unchanged"} {
		measured.maxBatch.Store(0)
		measured.batches.Store(0)
		scaleProfile(t, filepath.Join(output, pass+"-before.heap"))
		log, err := os.OpenFile(filepath.Join(output, pass+"-samples.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("create scale samples")
		}
		start := time.Now()
		job, replay, err := s.SubmitJob(ctx, a, registration.Library.ID, "scale-"+pass, domain.JobPriorityManual, policy)
		if err != nil || replay {
			log.Close()
			t.Fatal("submit scale scan")
		}
		encoder := json.NewEncoder(log)
		var peakHeap, peakRSS uint64
		lastSample := time.Time{}
		tick := time.NewTicker(250 * time.Millisecond)
		for {
			job, err = s.GetJob(ctx, a, job.ID)
			if err != nil {
				tick.Stop()
				log.Close()
				t.Fatalf("read scale job: %v", err)
			}
			terminal := job.State == domain.JobSucceeded || job.State == domain.JobFailed || job.State == domain.JobCancelled
			if time.Since(lastSample) >= time.Second || terminal {
				sample := sampleScale(t, s, start, job.Files)
				peakHeap = max(peakHeap, sample.Heap)
				peakRSS = max(peakRSS, sample.RSS)
				if err = encoder.Encode(sample); err != nil {
					tick.Stop()
					log.Close()
					t.Fatal("write scale sample")
				}
				lastSample = time.Now()
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
			t.Fatal("close scale samples")
		}
		if job.State != domain.JobSucceeded || job.Files != int64(count) || job.Bytes != int64(count*len(data)) || job.Skipped != 0 || job.Missing != 0 || job.ReviewRequired {
			t.Fatalf("scale job state=%s files=%d code=%s review=%t", job.State, job.Files, job.ErrorCode, job.ReviewRequired)
		}
		var stored, baseline int64
		if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM job_inventory WHERE job_id=$1::uuid`, job.ID).Scan(&stored); err != nil {
			t.Fatal("count scale inventory")
		}
		if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`, registration.Library.ID).Scan(&baseline); err != nil {
			t.Fatal("count scale baseline")
		}
		if stored != int64(count) || baseline != int64(count) || measured.maxBatch.Load() > domain.ScanBatchMaxEntries {
			t.Fatal("scale persistence or batch bound mismatch")
		}
		scaleProfile(t, filepath.Join(output, pass+"-after.heap"))
		results = append(results, result{Pass: pass, Files: count, GOMAXPROCS: runtime.GOMAXPROCS(0), GoVersion: runtime.Version(), Seconds: elapsed, FilesPerSecond: float64(count) / elapsed, MaxBatch: measured.maxBatch.Load(), Batches: measured.batches.Load(), PeakHeap: peakHeap, PeakRSS: peakRSS, AfterGC: sampleScale(t, s, start, job.Files)})
		body, err := json.MarshalIndent(results, "", "  ")
		if err != nil || os.WriteFile(filepath.Join(output, "result.json"), append(body, '\n'), 0600) != nil {
			t.Fatal("write scale result")
		}
		t.Logf("scale files=%d pass=%s seconds=%.3f heap_peak=%d rss_peak=%d", count, pass, elapsed, peakHeap, peakRSS)
	}
	for _, i := range []int{0, count / 2, count - 1} {
		value, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("video-%06d.mkv", i)))
		if err != nil || string(value) != string(data) {
			t.Fatal("scale fixture changed")
		}
	}
}
