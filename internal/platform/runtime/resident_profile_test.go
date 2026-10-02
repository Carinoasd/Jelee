//go:build jelee_probe_tests

package runtime

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const residentMaxSamples = 1024

var residentPhases = [...]string{"startup", "cold", "warm", "changed", "sustained", "cancellation", "shutdown", "stopped"}

type residentSample struct {
	ElapsedNanos    int64  `json:"elapsedNanos"`
	Phase           string `json:"phase"`
	RSSBytes        uint64 `json:"rssBytes"`
	HeapBytes       uint64 `json:"heapBytes"`
	TotalAllocBytes uint64 `json:"totalAllocBytes"`
	NumGC           uint32 `json:"numGC"`
	PauseTotalNS    uint64 `json:"pauseTotalNs"`
	Goroutines      int    `json:"goroutines"`
}

type residentProfile struct {
	Version           int              `json:"version"`
	Scope             string           `json:"scope"`
	RSSSource         string           `json:"rssSource"`
	Approximate       bool             `json:"approximate"`
	SampleEveryMillis int              `json:"sampleEveryMillis"`
	MaxSamples        int              `json:"maxSamples"`
	Complete          bool             `json:"complete"`
	Samples           []residentSample `json:"samples"`
}

// Only this acceptance sampler reads /proc. Product runtime and telemetry do
// not acquire a polling worker or an additional memory source.
func readResidentSample() (residentSample, error) {
	rss, err := readResidentRSS("/proc/self/statm", uint64(os.Getpagesize()))
	if err != nil {
		return residentSample{}, err
	}
	var stats goruntime.MemStats
	goruntime.ReadMemStats(&stats)
	return residentSample{RSSBytes: rss, HeapBytes: stats.HeapAlloc, TotalAllocBytes: stats.TotalAlloc,
		NumGC: stats.NumGC, PauseTotalNS: stats.PauseTotalNs, Goroutines: goruntime.NumGoroutine()}, nil
}

func readResidentRSS(path string, pageSize uint64) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, errors.New("resident memory source unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil || len(data) > 256 {
		return 0, errors.New("resident memory source unreadable or oversized")
	}
	return parseResidentRSS(data, pageSize)
}

func parseResidentRSS(data []byte, pageSize uint64) (uint64, error) {
	invalid := errors.New("invalid resident memory page counts")
	if len(data) == 0 || len(data) > 256 || pageSize == 0 {
		return 0, invalid
	}
	fields := strings.Fields(string(data))
	if len(fields) != 7 {
		return 0, invalid
	}
	var pages uint64
	for i, field := range fields {
		value, err := strictMemoryUint(field)
		if err != nil {
			return 0, invalid
		}
		if i == 1 {
			pages = value
		}
	}
	if pages == 0 || pages > math.MaxUint64/pageSize {
		return 0, invalid
	}
	return pages * pageSize, nil
}

type residentSampler struct {
	mu        sync.Mutex
	started   time.Time
	phase     int
	read      func() (residentSample, error)
	report    residentProfile
	err       error
	finalized bool
	stopOnce  sync.Once
	stop      chan struct{}
	done      chan struct{}
}

func startResidentSampler(started time.Time) (*residentSampler, error) {
	ticker := time.NewTicker(time.Second)
	return newResidentSampler(context.Background(), started, readResidentSample, ticker.C, ticker.Stop)
}

// The injected reader and tick channel are test seams. Native acceptance uses
// only readResidentSample and a real one-second ticker; no sample is invented.
func newResidentSampler(ctx context.Context, started time.Time, read func() (residentSample, error), ticks <-chan time.Time, stopTicks func()) (*residentSampler, error) {
	s := &residentSampler{started: started, read: read, stop: make(chan struct{}), done: make(chan struct{}),
		report: residentProfile{Version: 1, Scope: "worker-process", RSSSource: "/proc/self/statm", Approximate: true, SampleEveryMillis: 1000, MaxSamples: residentMaxSamples, Samples: make([]residentSample, 0, residentMaxSamples)}}
	if ctx.Err() != nil {
		stopTicks()
		return nil, errors.New("resident sampling cancelled before startup")
	}
	if err := s.captureLocked(); err != nil {
		stopTicks()
		return nil, err
	}
	go func() {
		defer close(s.done)
		defer stopTicks()
		for {
			select {
			case <-s.stop:
				return
			case <-ctx.Done():
				s.mu.Lock()
				if s.err == nil {
					s.err = errors.New("resident sampling cancelled")
				}
				s.mu.Unlock()
				return
			case <-ticks:
				if err := s.capture(); err != nil {
					return
				}
			}
		}
	}()
	return s, nil
}

func (s *residentSampler) captureLocked() error {
	if s.err != nil {
		return s.err
	}
	if len(s.report.Samples) >= residentMaxSamples {
		s.err = errors.New("resident sample limit reached")
		return s.err
	}
	sample, err := s.read()
	if err != nil || sample.RSSBytes == 0 || sample.Goroutines < 1 {
		s.err = errors.New("resident sample unavailable")
		return s.err
	}
	sample.ElapsedNanos, sample.Phase = time.Since(s.started).Nanoseconds(), residentPhases[s.phase]
	if sample.ElapsedNanos < 0 || len(s.report.Samples) > 0 && sample.ElapsedNanos <= s.report.Samples[len(s.report.Samples)-1].ElapsedNanos {
		s.err = errors.New("resident sample time did not advance")
		return s.err
	}
	s.report.Samples = append(s.report.Samples, sample)
	return nil
}

func (s *residentSampler) capture() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
		return errors.New("resident sampling stopped")
	default:
	}
	return s.captureLocked()
}

func (s *residentSampler) changePhase(phase string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	select {
	case <-s.stop:
		return errors.New("resident sampling stopped")
	default:
	}
	if phase == residentPhases[s.phase] {
		return nil
	}
	if s.phase+1 >= len(residentPhases) || phase != residentPhases[s.phase+1] {
		s.err = errors.New("resident phase must advance in the fixed order")
		return s.err
	}
	s.phase++
	return s.captureLocked()
}

func (s *residentSampler) finish() (residentProfile, error) {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.finalized {
		// The background reader is joined before taking this final synchronous
		// sample. No later timer or phase change may read the process again.
		if s.err == nil {
			_ = s.captureLocked()
		}
		if s.err == nil && s.phase != len(residentPhases)-1 {
			s.err = errors.New("resident sampling ended before the stopped phase")
		}
		s.report.Complete = s.err == nil
		s.finalized = true
	}
	report := s.report
	report.Samples = append([]residentSample(nil), s.report.Samples...)
	return report, s.err
}

func (profile *memoryProfileReport) residentPhase(t *testing.T, phase string) {
	t.Helper()
	if profile == nil {
		return
	}
	if err := profile.residentSampler.changePhase(phase); err != nil {
		t.Fatal("resident memory phase sampling failed", err)
	}
}

func TestResidentStatmRequiresBoundedPageCounts(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		pageSize   uint64
		want       uint64
	}{
		{"valid", "4096 512 128 12 0 1000 0\n", 4096, 2 << 20},
		{"empty", "", 4096, 0},
		{"truncated", "4096 512", 4096, 0},
		{"extra-field", "4096 512 128 12 0 1000 0 8", 4096, 0},
		{"negative-resident", "4096 -1 128 12 0 1000 0", 4096, 0},
		{"negative-unused-field", "4096 512 128 -1 0 1000 0", 4096, 0},
		{"overflow-parse", "4096 18446744073709551616 0 0 0 0 0", 4096, 0},
		{"overflow-product", "4096 4503599627370496 0 0 0 0 0", 4096, 0},
		{"zero-resident", "4096 0 0 0 0 0 0", 4096, 0},
		{"zero-page-size", "4096 512 0 0 0 0 0", 0, 0},
		{"oversized", strings.Repeat(" ", 257) + "4096 512 0 0 0 0 0", 4096, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "statm")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := readResidentRSS(path, tc.pageSize)
			if tc.want == 0 {
				if err == nil {
					t.Fatal("malformed resident source was accepted")
				}
			} else if err != nil || got != tc.want {
				t.Fatal("resident page conversion differs", got, err)
			}
		})
	}
	if _, err := readResidentRSS(filepath.Join(t.TempDir(), "missing"), 4096); err == nil {
		t.Fatal("missing resident source was replaced by zero")
	}
}

func residentTestReading() (residentSample, error) {
	// Test readers do no I/O. A scheduler yield alone does not advance the
	// Windows clock, so wait for an actual tick instead of inventing timestamps.
	// Native samples always use the real proc/MemStats reader without this wait.
	started := time.Now()
	for time.Since(started) == 0 {
		time.Sleep(time.Millisecond)
	}
	return residentSample{RSSBytes: 4096, HeapBytes: 1024, TotalAllocBytes: 2048, NumGC: 1, PauseTotalNS: 1, Goroutines: 1}, nil
}

func TestResidentSamplerStartupFailureStopsTicker(t *testing.T) {
	for _, mode := range []string{"cancelled", "source-error", "zero-rss", "zero-goroutines"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			stopped, reads := 0, 0
			sampler, err := newResidentSampler(ctx, time.Now(), func() (residentSample, error) {
				reads++
				reading, _ := residentTestReading()
				switch mode {
				case "source-error":
					return residentSample{}, errors.New("PRIVATE_SOURCE_ERROR")
				case "zero-rss":
					reading.RSSBytes = 0
				case "zero-goroutines":
					reading.Goroutines = 0
				}
				return reading, nil
			}, nil, func() { stopped++ })
			if sampler != nil || err == nil || strings.Contains(err.Error(), "PRIVATE") || stopped != 1 || mode == "cancelled" && reads != 0 {
				t.Fatal("failed resident startup retained a ticker or accepted unavailable evidence")
			}
		})
	}
}

func TestResidentSamplerStagesStopAndBoundedStorage(t *testing.T) {
	var calls atomic.Int32
	ticks := make(chan time.Time)
	tickerStopped := make(chan struct{})
	sampler, err := newResidentSampler(context.Background(), time.Now(), func() (residentSample, error) {
		calls.Add(1)
		return residentTestReading()
	}, ticks, func() { close(tickerStopped) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sampler.finish() })
	for _, phase := range residentPhases[1:] {
		if err := sampler.changePhase(phase); err != nil {
			t.Fatal(err)
		}
	}
	report, err := sampler.finish()
	if err != nil || !report.Complete || len(report.Samples) != len(residentPhases)+1 {
		t.Fatal("resident report omitted synchronous phase or terminal samples", err)
	}
	if report.Version != 1 || report.Scope != "worker-process" || report.RSSSource != "/proc/self/statm" || !report.Approximate || report.SampleEveryMillis != 1000 || report.MaxSamples != residentMaxSamples {
		t.Fatal("resident report omitted the source and bounded sampling contract")
	}
	select {
	case <-tickerStopped:
	default:
		t.Fatal("sampler returned before its ticker was stopped")
	}
	for i, phase := range residentPhases {
		if report.Samples[i].Phase != phase || i > 0 && report.Samples[i].ElapsedNanos <= report.Samples[i-1].ElapsedNanos {
			t.Fatal("resident phases or elapsed time moved backwards")
		}
	}
	before := calls.Load()
	if err := sampler.changePhase("stopped"); err == nil {
		t.Fatal("stopped sampler accepted another phase")
	}
	if _, err := sampler.finish(); err != nil || calls.Load() != before {
		t.Fatal("repeated sampler finish read the process again")
	}
	report.Samples[0].RSSBytes = 999
	retained, _ := sampler.finish()
	if retained.Samples[0].RSSBytes == 999 {
		t.Fatal("returned resident samples alias the retained report")
	}
	limited, err := newResidentSampler(context.Background(), time.Now(), residentTestReading, nil, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = limited.finish() })
	for i := 1; i < residentMaxSamples; i++ {
		if err := limited.capture(); err != nil {
			t.Fatal(err)
		}
	}
	if err := limited.capture(); err == nil {
		t.Fatal("resident sample capacity was silently exceeded")
	}
	bounded, err := limited.finish()
	if err == nil || bounded.Complete || len(bounded.Samples) != residentMaxSamples || cap(limited.report.Samples) != residentMaxSamples {
		t.Fatal("resident sample overflow did not preserve a bounded incomplete report")
	}
}

func TestResidentSamplerReaderFailureCancellationAndPhaseErrors(t *testing.T) {
	for _, mode := range []string{"read-error", "cancel", "phase-backwards", "phase-skipped"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ticks := make(chan time.Time, 1)
			var reads atomic.Int32
			sampler, err := newResidentSampler(ctx, time.Now(), func() (residentSample, error) {
				if reads.Add(1) > 1 && mode == "read-error" {
					return residentSample{}, errors.New("PRIVATE_SOURCE_ERROR")
				}
				return residentTestReading()
			}, ticks, func() {})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = sampler.finish() })
			switch mode {
			case "read-error":
				ticks <- time.Now()
			case "cancel":
				cancel()
			case "phase-backwards":
				if err := sampler.changePhase("cold"); err != nil {
					t.Fatal(err)
				}
				if err := sampler.changePhase("startup"); err == nil {
					t.Fatal("resident phase moved backwards")
				}
			case "phase-skipped":
				if err := sampler.changePhase("warm"); err == nil {
					t.Fatal("resident phase skipped the cold workload")
				}
			}
			if mode == "read-error" || mode == "cancel" {
				select {
				case <-sampler.done:
				case <-time.After(time.Second):
					t.Fatal("resident sampler did not terminate after failure or cancellation")
				}
			}
			report, err := sampler.finish()
			if err == nil || report.Complete || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("resident error was lost, leaked, or marked complete")
			}
		})
	}
}

func TestResidentSamplerStopJoinsActiveReader(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	ticks := make(chan time.Time, 1)
	var block atomic.Bool
	var releaseOnce sync.Once
	sampler, err := newResidentSampler(context.Background(), time.Now(), func() (residentSample, error) {
		if block.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return residentTestReading()
	}, ticks, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); _, _ = sampler.finish() })
	for _, phase := range residentPhases[1:] {
		if err := sampler.changePhase(phase); err != nil {
			t.Fatal(err)
		}
	}
	block.Store(true)
	ticks <- time.Now()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("controlled resident read did not enter")
	}
	finished := make(chan error, 1)
	go func() { _, err := sampler.finish(); finished <- err }()
	select {
	case <-sampler.stop:
	case <-time.After(time.Second):
		t.Fatal("resident sampler did not stop admission")
	}
	select {
	case <-finished:
		t.Fatal("resident sampler stop returned while its reader was active")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resident sampler did not join the released reader")
	}
}
