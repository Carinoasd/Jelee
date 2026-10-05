//go:build jelee_probe_tests

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"runtime/metrics"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const scanMemoryMaxSamples = 3600
const scanMemoryMaxGap = 5 * time.Second
const scanMemoryMaxElapsed = time.Hour
const scanGCPauseMetric = "/sched/pauses/total/gc:seconds"

var scanMemoryPhases = [...]string{"startup", "login", "scan", "shutdown", "stopped"}
var errScanMemoryProfile = errors.New("scan memory evidence unavailable or invalid")

type scanPauseHistogram struct {
	Metric      string   `json:"metric"`
	BoundsNanos []any    `json:"boundsNanos"`
	Counts      []uint64 `json:"counts"`
	// Preserve the exact SDK boundaries for the before/after comparison. The
	// JSON boundaries are conservative ceil(ns), never float approximations.
	sourceBounds []float64
}

type scanGCBoundary struct {
	Counters               memoryRuntimeCounters `json:"counters"`
	CountersStartedNanos   int64                 `json:"countersStartedNanos"`
	CountersFinishedNanos  int64                 `json:"countersFinishedNanos"`
	HistogramStartedNanos  int64                 `json:"histogramStartedNanos"`
	HistogramFinishedNanos int64                 `json:"histogramFinishedNanos"`
	Histogram              scanPauseHistogram    `json:"histogram"`
}

type scanResidentProfile struct {
	Version           int              `json:"version"`
	Scope             string           `json:"scope"`
	RSSSource         string           `json:"rssSource"`
	Approximate       bool             `json:"approximate"`
	SampleEveryMillis int              `json:"sampleEveryMillis"`
	MaxSamples        int              `json:"maxSamples"`
	Complete          bool             `json:"complete"`
	Samples           []residentSample `json:"samples"`
}

type scanMemoryReport struct {
	Version                 int                   `json:"version"`
	Complete                bool                  `json:"complete"`
	Runtime                 memoryRuntimeSettings `json:"runtime"`
	Before                  memoryCgroupSnapshot  `json:"before"`
	After                   memoryCgroupSnapshot  `json:"after"`
	BeforeReadStartedNanos  int64                 `json:"beforeReadStartedNanos"`
	BeforeReadFinishedNanos int64                 `json:"beforeReadFinishedNanos"`
	AfterReadStartedNanos   int64                 `json:"afterReadStartedNanos"`
	AfterReadFinishedNanos  int64                 `json:"afterReadFinishedNanos"`
	Resident                scanResidentProfile   `json:"resident"`
	GCBefore                scanGCBoundary        `json:"gcBefore"`
	GCAfter                 scanGCBoundary        `json:"gcAfter"`
	ElapsedNanos            int64                 `json:"elapsedNanos"`
	ElapsedMillis           int64                 `json:"elapsedMillis"`
}

func copyScanPauseHistogram(histogram *metrics.Float64Histogram) (scanPauseHistogram, error) {
	if histogram == nil || len(histogram.Counts) < 2 || len(histogram.Counts) > 2048 || len(histogram.Buckets) != len(histogram.Counts)+1 {
		return scanPauseHistogram{}, errScanMemoryProfile
	}
	// runtime/metrics owns these slices and may reuse their storage on Read.
	bounds := append([]float64(nil), histogram.Buckets...)
	counts := append([]uint64(nil), histogram.Counts...)
	result := scanPauseHistogram{Metric: scanGCPauseMetric, BoundsNanos: make([]any, len(bounds)), Counts: counts, sourceBounds: bounds}
	if !math.IsInf(bounds[0], -1) || !math.IsInf(bounds[len(bounds)-1], 1) {
		return scanPauseHistogram{}, errScanMemoryProfile
	}
	var previousNS int64 = -1
	for index, bound := range bounds {
		if math.IsNaN(bound) || index > 0 && bound <= bounds[index-1] {
			return scanPauseHistogram{}, errScanMemoryProfile
		}
		switch {
		case index == 0:
			result.BoundsNanos[index] = "-Inf"
		case index == len(bounds)-1:
			result.BoundsNanos[index] = "+Inf"
		default:
			nanoseconds := math.Ceil(bound * 1e9)
			if math.IsInf(bound, 0) || bound < 0 || math.IsInf(nanoseconds, 0) || nanoseconds >= float64(math.MaxInt64) {
				return scanPauseHistogram{}, errScanMemoryProfile
			}
			value := int64(nanoseconds)
			if value <= previousNS {
				return scanPauseHistogram{}, errScanMemoryProfile
			}
			result.BoundsNanos[index] = value
			previousNS = value
		}
	}
	return result, nil
}

func readScanGCBoundary(started time.Time, now func() time.Time) (scanGCBoundary, error) {
	var boundary scanGCBoundary
	// Neither read forces a GC. These are separate observations, with separate
	// timestamps, because MemStats and metrics.Read are not an atomic snapshot.
	boundary.CountersStartedNanos = now().Sub(started).Nanoseconds()
	boundary.Counters = readMemoryRuntimeCounters()
	boundary.CountersFinishedNanos = now().Sub(started).Nanoseconds()
	samples := []metrics.Sample{{Name: scanGCPauseMetric}}
	boundary.HistogramStartedNanos = now().Sub(started).Nanoseconds()
	metrics.Read(samples)
	boundary.HistogramFinishedNanos = now().Sub(started).Nanoseconds()
	if samples[0].Value.Kind() != metrics.KindFloat64Histogram {
		return scanGCBoundary{}, errScanMemoryProfile
	}
	histogram, err := copyScanPauseHistogram(samples[0].Value.Float64Histogram())
	if err != nil {
		return scanGCBoundary{}, err
	}
	boundary.Histogram = histogram
	if !validScanBoundaryTimes(boundary) {
		return scanGCBoundary{}, errScanMemoryProfile
	}
	return boundary, nil
}

func validScanBoundaryTimes(boundary scanGCBoundary) bool {
	return boundary.CountersStartedNanos >= 0 &&
		boundary.CountersFinishedNanos >= boundary.CountersStartedNanos &&
		boundary.HistogramStartedNanos >= boundary.CountersFinishedNanos &&
		boundary.HistogramFinishedNanos >= boundary.HistogramStartedNanos
}

func compareScanGCBoundaries(before, after scanGCBoundary) error {
	if !validScanBoundaryTimes(before) || !validScanBoundaryTimes(after) ||
		after.CountersStartedNanos < before.HistogramFinishedNanos ||
		after.Counters.TotalAllocBytes < before.Counters.TotalAllocBytes ||
		after.Counters.NumGC < before.Counters.NumGC || after.Counters.PauseTotalNS < before.Counters.PauseTotalNS ||
		before.Histogram.Metric != scanGCPauseMetric || after.Histogram.Metric != scanGCPauseMetric ||
		len(before.Histogram.Counts) == 0 || !reflect.DeepEqual(before.Histogram.sourceBounds, after.Histogram.sourceBounds) ||
		!reflect.DeepEqual(before.Histogram.BoundsNanos, after.Histogram.BoundsNanos) ||
		len(before.Histogram.Counts) != len(after.Histogram.Counts) {
		return errScanMemoryProfile
	}
	for index, count := range before.Histogram.Counts {
		if after.Histogram.Counts[index] < count {
			return errScanMemoryProfile
		}
	}
	return nil
}

type scanMemoryHooks struct {
	now      func() time.Time
	sample   func() (residentSample, error)
	settings func() (memoryRuntimeSettings, error)
	cgroup   func() (memoryCgroupSnapshot, error)
	boundary func(time.Time, func() time.Time) (scanGCBoundary, error)
}

type scanMemoryProfile struct {
	mu        sync.Mutex
	ctx       context.Context
	started   time.Time
	hooks     scanMemoryHooks
	phase     int
	report    scanMemoryReport
	err       error
	finalized bool
	stopOnce  sync.Once
	stop      chan struct{}
	done      chan struct{}
}

func startScanMemoryProfile() (*scanMemoryProfile, error) {
	started := time.Now()
	ticker := time.NewTicker(time.Second)
	return newScanMemoryProfile(context.Background(), started, scanMemoryHooks{
		now: time.Now, sample: readResidentSample, settings: readMemoryRuntimeSettings,
		cgroup:   func() (memoryCgroupSnapshot, error) { return readMemoryCgroup(memoryCgroupRoot) },
		boundary: readScanGCBoundary,
	}, ticker.C, ticker.Stop)
}

// Only this opt-in acceptance helper owns the sampler; product runtime has no
// new polling worker. Injected readers and ticks keep failure tests bounded.
func newScanMemoryProfile(ctx context.Context, started time.Time, hooks scanMemoryHooks, ticks <-chan time.Time, stopTicks func()) (*scanMemoryProfile, error) {
	s := &scanMemoryProfile{ctx: ctx, started: started, hooks: hooks, stop: make(chan struct{}), done: make(chan struct{}),
		report: scanMemoryReport{Version: 1, Resident: scanResidentProfile{Version: 1,
			Scope: "worker-process", RSSSource: "/proc/self/statm", Approximate: true,
			SampleEveryMillis: 1000, MaxSamples: scanMemoryMaxSamples,
			Samples: make([]residentSample, 0, scanMemoryMaxSamples)}}}
	fail := func() (*scanMemoryProfile, error) { stopTicks(); return nil, errScanMemoryProfile }
	if ctx.Err() != nil {
		return fail()
	}
	var err error
	if s.report.Runtime, err = hooks.settings(); err != nil {
		return fail()
	}
	s.report.BeforeReadStartedNanos = hooks.now().Sub(started).Nanoseconds()
	s.report.Before, err = hooks.cgroup()
	s.report.BeforeReadFinishedNanos = hooks.now().Sub(started).Nanoseconds()
	if err != nil {
		return fail()
	}
	if s.report.GCBefore, err = hooks.boundary(started, hooks.now); err != nil {
		return fail()
	}
	if s.report.BeforeReadStartedNanos < 0 || s.report.BeforeReadFinishedNanos < s.report.BeforeReadStartedNanos ||
		s.report.GCBefore.CountersStartedNanos < s.report.BeforeReadFinishedNanos || !validScanBoundaryTimes(s.report.GCBefore) {
		return fail()
	}
	if s.captureLocked() != nil {
		return fail()
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
				s.err = errScanMemoryProfile
				s.mu.Unlock()
				return
			case _, open := <-ticks:
				if !open {
					s.mu.Lock()
					s.err = errScanMemoryProfile
					s.mu.Unlock()
					return
				}
				if s.capture() != nil {
					return
				}
			}
		}
	}()
	return s, nil
}

func (s *scanMemoryProfile) captureLocked() error {
	if s.err != nil {
		return s.err
	}
	if len(s.report.Resident.Samples) >= scanMemoryMaxSamples {
		s.err = errScanMemoryProfile
		return s.err
	}
	sample, err := s.hooks.sample()
	sample.ElapsedNanos, sample.Phase = s.hooks.now().Sub(s.started).Nanoseconds(), scanMemoryPhases[s.phase]
	if err != nil || sample.RSSBytes == 0 || sample.Goroutines < 1 || sample.ElapsedNanos < 0 || sample.ElapsedNanos > int64(scanMemoryMaxElapsed) {
		s.err = errScanMemoryProfile
		return s.err
	}
	if len(s.report.Resident.Samples) == 0 {
		if sample.ElapsedNanos > int64(time.Second) || sample.ElapsedNanos < s.report.GCBefore.HistogramFinishedNanos {
			s.err = errScanMemoryProfile
			return s.err
		}
	} else {
		previous := s.report.Resident.Samples[len(s.report.Resident.Samples)-1]
		if sample.ElapsedNanos <= previous.ElapsedNanos || sample.ElapsedNanos-previous.ElapsedNanos > int64(scanMemoryMaxGap) ||
			sample.TotalAllocBytes < previous.TotalAllocBytes || sample.NumGC < previous.NumGC || sample.PauseTotalNS < previous.PauseTotalNS {
			s.err = errScanMemoryProfile
			return s.err
		}
	}
	s.report.Resident.Samples = append(s.report.Resident.Samples, sample)
	return nil
}

func (s *scanMemoryProfile) capture() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
		return errScanMemoryProfile
	default:
	}
	return s.captureLocked()
}

func (s *scanMemoryProfile) changePhase(phase string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	select {
	case <-s.stop:
		return errScanMemoryProfile
	default:
	}
	if phase == scanMemoryPhases[s.phase] {
		return nil
	}
	if s.phase+1 >= len(scanMemoryPhases) || phase != scanMemoryPhases[s.phase+1] {
		s.err = errScanMemoryProfile
		return s.err
	}
	s.phase++
	return s.captureLocked()
}

func cloneScanBoundary(boundary scanGCBoundary) scanGCBoundary {
	boundary.Histogram.BoundsNanos = append([]any(nil), boundary.Histogram.BoundsNanos...)
	boundary.Histogram.Counts = append([]uint64(nil), boundary.Histogram.Counts...)
	boundary.Histogram.sourceBounds = append([]float64(nil), boundary.Histogram.sourceBounds...)
	return boundary
}

func cloneScanCgroup(group memoryCgroupSnapshot) memoryCgroupSnapshot {
	copy := make(map[string]uint64, len(group.Events))
	for name, value := range group.Events {
		copy[name] = value
	}
	group.Events = copy
	return group
}

func (s *scanMemoryProfile) finish() (scanMemoryReport, error) {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.finalized {
		// Join first, then take the last stopped sample and final observations.
		// No cooling delay, extra GC, or warm-up changes the measured workload.
		if s.ctx.Err() != nil {
			s.err = errScanMemoryProfile
		}
		if s.err == nil {
			_ = s.captureLocked()
		}
		if s.phase != len(scanMemoryPhases)-1 {
			s.err = errScanMemoryProfile
		}
		var err error
		s.report.GCAfter, err = s.hooks.boundary(s.started, s.hooks.now)
		if err != nil || compareScanGCBoundaries(s.report.GCBefore, s.report.GCAfter) != nil {
			s.err = errScanMemoryProfile
		}
		s.report.AfterReadStartedNanos = s.hooks.now().Sub(s.started).Nanoseconds()
		s.report.After, err = s.hooks.cgroup()
		s.report.AfterReadFinishedNanos = s.hooks.now().Sub(s.started).Nanoseconds()
		if err != nil || s.report.AfterReadStartedNanos < s.report.GCAfter.HistogramFinishedNanos ||
			s.report.AfterReadFinishedNanos < s.report.AfterReadStartedNanos {
			s.err = errScanMemoryProfile
		}
		settings, err := s.hooks.settings()
		if err != nil || settings != s.report.Runtime {
			s.err = errScanMemoryProfile
		}
		s.report.ElapsedNanos = s.hooks.now().Sub(s.started).Nanoseconds()
		s.report.ElapsedMillis = s.report.ElapsedNanos / int64(time.Millisecond)
		if s.report.ElapsedNanos < s.report.AfterReadFinishedNanos || s.report.ElapsedNanos > int64(scanMemoryMaxElapsed) {
			s.err = errScanMemoryProfile
		}
		if len(s.report.Resident.Samples) > 0 {
			last := s.report.Resident.Samples[len(s.report.Resident.Samples)-1].ElapsedNanos
			if s.report.GCAfter.CountersStartedNanos < last || s.report.ElapsedNanos-last > int64(scanMemoryMaxGap) {
				s.err = errScanMemoryProfile
			}
		}
		s.report.Complete, s.report.Resident.Complete = s.err == nil, s.err == nil
		s.finalized = true
	}
	report := s.report
	report.Resident.Samples = append([]residentSample(nil), report.Resident.Samples...)
	report.GCBefore, report.GCAfter = cloneScanBoundary(report.GCBefore), cloneScanBoundary(report.GCAfter)
	report.Before, report.After = cloneScanCgroup(report.Before), cloneScanCgroup(report.After)
	return report, s.err
}

func TestScanMemoryHistogramOwnsSDKSnapshotAndSafeJSON(t *testing.T) {
	source := &metrics.Float64Histogram{Buckets: []float64{math.Inf(-1), 0, 0.0000000641, math.Inf(1)}, Counts: []uint64{0, 600, 3}}
	snapshot, err := copyScanPauseHistogram(source)
	if err != nil || snapshot.BoundsNanos[2] != int64(65) {
		t.Fatal("histogram conversion did not conservatively round the finite bound", err)
	}
	source.Buckets[2], source.Counts[1] = 999, 0
	if snapshot.sourceBounds[2] != 0.0000000641 || snapshot.Counts[1] != 600 {
		t.Fatal("histogram retained runtime-owned slice storage")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || !strings.Contains(string(encoded), `"boundsNanos":["-Inf",0,65,"+Inf"]`) || !strings.Contains(string(encoded), `"counts":[0,600,3]`) {
		t.Fatal("histogram omitted full cumulative counts or safe infinity endpoints", err)
	}
	for _, bounds := range [][]float64{
		{math.Inf(-1), 0, math.NaN(), math.Inf(1)},
		{math.Inf(-1), 0, math.Inf(1), math.Inf(1)},
		{math.Inf(-1), -1, 0, math.Inf(1)},
		{math.Inf(-1), 0, 1e20, math.Inf(1)},
		{math.Inf(-1), 0, 0, math.Inf(1)},
		{0, 1, 2, math.Inf(1)},
	} {
		if _, err := copyScanPauseHistogram(&metrics.Float64Histogram{Buckets: bounds, Counts: []uint64{0, 1, 0}}); err == nil {
			t.Fatal("invalid or unrepresentable histogram bounds accepted")
		}
	}
	if _, err := copyScanPauseHistogram(&metrics.Float64Histogram{Buckets: []float64{math.Inf(-1), 0, math.Inf(1)}, Counts: []uint64{1}}); err == nil {
		t.Fatal("missing histogram count accepted")
	}
}

func TestScanMemoryGCUsesFullRuntimeHistogramWithoutForcingGC(t *testing.T) {
	started := time.Now()
	before, err := readScanGCBoundary(started, time.Now)
	if err != nil {
		t.Fatal("required runtime histogram unavailable", err)
	}
	after, err := readScanGCBoundary(started, time.Now)
	if err != nil || compareScanGCBoundaries(before, after) != nil || len(before.Histogram.Counts) < 2 {
		t.Fatal("successive real runtime observations violated the cumulative contract", err)
	}
	// A delta larger than the MemStats ring is retained in full. There is no
	// comparison between STW event counts and GC cycle counts.
	after = cloneScanBoundary(before)
	after.CountersStartedNanos = before.HistogramFinishedNanos + 1
	after.CountersFinishedNanos = after.CountersStartedNanos
	after.HistogramStartedNanos, after.HistogramFinishedNanos = after.CountersStartedNanos, after.CountersStartedNanos
	after.Histogram.Counts[1] += 600
	if compareScanGCBoundaries(before, after) != nil || after.Histogram.Counts[1]-before.Histogram.Counts[1] != 600 {
		t.Fatal("full histogram interval was truncated to recent GC cycles")
	}
	for _, mutate := range []func(*scanGCBoundary){
		func(value *scanGCBoundary) { value.Histogram.Counts = value.Histogram.Counts[:1] },
		func(value *scanGCBoundary) { value.Histogram.sourceBounds[1] = 0.0000000001 },
		func(value *scanGCBoundary) { value.Histogram.BoundsNanos[1] = int64(7) },
		func(value *scanGCBoundary) { value.CountersStartedNanos = -1 },
	} {
		broken := cloneScanBoundary(after)
		mutate(&broken)
		if compareScanGCBoundaries(before, broken) == nil {
			t.Fatal("changed histogram dimensions or invalid observation time accepted")
		}
	}
	before.Histogram.Counts[1], after.Histogram.Counts[1] = 9, 8
	if compareScanGCBoundaries(before, after) == nil {
		t.Fatal("decreasing cumulative histogram accepted")
	}
}

func scanMemoryTestHooks(started time.Time) (scanMemoryHooks, *atomic.Int64) {
	clock := &atomic.Int64{}
	hooks := scanMemoryHooks{
		now: func() time.Time { return started.Add(time.Duration(clock.Add(int64(time.Millisecond)))) },
		sample: func() (residentSample, error) {
			return residentSample{RSSBytes: 4096, HeapBytes: 1024, TotalAllocBytes: 2048, NumGC: 1, PauseTotalNS: 100, Goroutines: 1}, nil
		},
		settings: func() (memoryRuntimeSettings, error) {
			return memoryRuntimeSettings{GOGCPercent: 100, GoMemoryLimitBytes: 512 << 20}, nil
		},
		cgroup: func() (memoryCgroupSnapshot, error) {
			return memoryCgroupSnapshot{CgroupVersion: 2, CurrentBytes: 4096, PeakBytes: 8192, MaxBytes: 768 << 20, Events: map[string]uint64{"oom": 0}}, nil
		},
		boundary: func(start time.Time, now func() time.Time) (scanGCBoundary, error) {
			histogram, err := copyScanPauseHistogram(&metrics.Float64Histogram{Buckets: []float64{math.Inf(-1), 0, 0.001, math.Inf(1)}, Counts: []uint64{0, 2, 0}})
			return scanGCBoundary{Counters: memoryRuntimeCounters{TotalAllocBytes: 2048, NumGC: 1, PauseTotalNS: 100},
				CountersStartedNanos: now().Sub(start).Nanoseconds(), CountersFinishedNanos: now().Sub(start).Nanoseconds(),
				HistogramStartedNanos: now().Sub(start).Nanoseconds(), HistogramFinishedNanos: now().Sub(start).Nanoseconds(), Histogram: histogram}, err
		},
	}
	return hooks, clock
}

func TestScanMemorySamplerCompleteOrderingOwnershipAndJoin(t *testing.T) {
	started := time.Now()
	hooks, _ := scanMemoryTestHooks(started)
	var stopped atomic.Int32
	profile, err := newScanMemoryProfile(context.Background(), started, hooks, nil, func() { stopped.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = profile.finish() })
	for _, phase := range scanMemoryPhases[1:] {
		if err := profile.changePhase(phase); err != nil {
			t.Fatal(err)
		}
	}
	report, err := profile.finish()
	if err != nil || !report.Complete || !report.Resident.Complete || stopped.Load() != 1 || len(report.Resident.Samples) != 6 {
		t.Fatal("sampler did not stop and retain every phase plus a terminal observation", err)
	}
	if report.Resident.MaxSamples != 3600 || report.Resident.SampleEveryMillis != 1000 || report.Resident.RSSSource != "/proc/self/statm" || !report.Resident.Approximate {
		t.Fatal("report lost the bounded sampling and measurement-source contract")
	}
	for index, phase := range scanMemoryPhases {
		if report.Resident.Samples[index].Phase != phase || index > 0 && report.Resident.Samples[index].ElapsedNanos <= report.Resident.Samples[index-1].ElapsedNanos {
			t.Fatal("phase samples were missing or timestamps moved backwards")
		}
	}
	last := report.Resident.Samples[len(report.Resident.Samples)-1].ElapsedNanos
	if report.BeforeReadFinishedNanos > report.GCBefore.CountersStartedNanos || report.GCBefore.HistogramFinishedNanos > report.Resident.Samples[0].ElapsedNanos ||
		last > report.GCAfter.CountersStartedNanos || report.GCAfter.HistogramFinishedNanos > report.AfterReadStartedNanos || report.AfterReadFinishedNanos > report.ElapsedNanos {
		t.Fatal("report did not preserve the actual independent read boundaries")
	}
	report.Resident.Samples[0].RSSBytes = 1
	report.GCBefore.Histogram.Counts[1] = 900
	report.GCAfter.Histogram.BoundsNanos[1] = int64(900)
	report.Before.Events["oom"] = 900
	again, err := profile.finish()
	if err != nil || again.Resident.Samples[0].RSSBytes != 4096 || again.GCBefore.Histogram.Counts[1] != 2 || again.GCAfter.Histogram.BoundsNanos[1] != int64(0) || again.Before.Events["oom"] != 0 || stopped.Load() != 1 {
		t.Fatal("returned evidence aliases sampler state or finish repeated observations", err)
	}
	if profile.capture() == nil || profile.changePhase("stopped") == nil {
		t.Fatal("finalized sampler admitted another observation")
	}
}

func TestScanMemorySamplerFailsClosedAndKeepsBoundedPartialEvidence(t *testing.T) {
	for _, kind := range []string{"read", "zero-rss", "zero-goroutines", "gap", "backward-time", "counter", "phase", "early-finish", "capacity", "cancelled", "histogram", "settings"} {
		t.Run(kind, func(t *testing.T) {
			started := time.Now()
			hooks, clock := scanMemoryTestHooks(started)
			var broken atomic.Bool
			originalSample := hooks.sample
			hooks.sample = func() (residentSample, error) {
				sample, err := originalSample()
				if broken.Load() {
					switch kind {
					case "read":
						return residentSample{}, errors.New("PRIVATE_SOURCE_ERROR")
					case "zero-rss":
						sample.RSSBytes = 0
					case "zero-goroutines":
						sample.Goroutines = 0
					case "counter":
						sample.TotalAllocBytes--
					}
				}
				return sample, err
			}
			originalBoundary := hooks.boundary
			hooks.boundary = func(start time.Time, now func() time.Time) (scanGCBoundary, error) {
				boundary, err := originalBoundary(start, now)
				if broken.Load() && kind == "histogram" {
					boundary.Histogram.Counts[1]--
				}
				return boundary, err
			}
			originalSettings := hooks.settings
			hooks.settings = func() (memoryRuntimeSettings, error) {
				settings, err := originalSettings()
				if broken.Load() && kind == "settings" {
					settings.GOGCPercent = 50
				}
				return settings, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var stopped atomic.Bool
			profile, err := newScanMemoryProfile(ctx, started, hooks, nil, func() { stopped.Store(true) })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = profile.finish() })
			broken.Store(true)
			switch kind {
			case "gap":
				clock.Add(int64(scanMemoryMaxGap))
				_ = profile.capture()
			case "backward-time":
				clock.Store(0)
				_ = profile.capture()
			case "phase":
				_ = profile.changePhase("scan")
			case "capacity":
				for index := 1; index <= scanMemoryMaxSamples; index++ {
					_ = profile.capture()
				}
			case "cancelled":
				cancel()
				select {
				case <-profile.done:
				case <-time.After(time.Second):
					t.Fatal("cancelled sampler did not stop")
				}
			case "early-finish":
			case "histogram", "settings":
				for _, phase := range scanMemoryPhases[1:] {
					if err := profile.changePhase(phase); err != nil {
						t.Fatal(err)
					}
				}
			default:
				_ = profile.capture()
			}
			report, err := profile.finish()
			if !errors.Is(err, errScanMemoryProfile) || report.Complete || report.Resident.Complete || len(report.Resident.Samples) == 0 || len(report.Resident.Samples) > scanMemoryMaxSamples || !stopped.Load() {
				t.Fatal("invalid evidence was accepted, lost, or failed to stop its sampler", err)
			}
			if kind == "capacity" && (len(report.Resident.Samples) != scanMemoryMaxSamples || cap(profile.report.Resident.Samples) != scanMemoryMaxSamples) {
				t.Fatal("capacity failure discarded samples or allocated an unbounded buffer")
			}
			body, marshalErr := json.Marshal(report)
			if marshalErr != nil || strings.Contains(string(body), "PRIVATE_SOURCE_ERROR") || strings.Contains(err.Error(), "PRIVATE_SOURCE_ERROR") {
				t.Fatal("partial evidence leaked an arbitrary source error")
			}
		})
	}
}

func TestScanMemorySamplerUnavailableStartupStopsTicker(t *testing.T) {
	for _, kind := range []string{"cancelled", "settings", "cgroup", "histogram", "sample", "late-first-sample"} {
		t.Run(kind, func(t *testing.T) {
			started := time.Now()
			hooks, clock := scanMemoryTestHooks(started)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			private := errors.New("PRIVATE_SOURCE_ERROR")
			switch kind {
			case "cancelled":
				cancel()
			case "settings":
				hooks.settings = func() (memoryRuntimeSettings, error) { return memoryRuntimeSettings{}, private }
			case "cgroup":
				hooks.cgroup = func() (memoryCgroupSnapshot, error) { return memoryCgroupSnapshot{}, private }
			case "histogram":
				hooks.boundary = func(time.Time, func() time.Time) (scanGCBoundary, error) { return scanGCBoundary{}, private }
			case "sample":
				hooks.sample = func() (residentSample, error) { return residentSample{}, private }
			case "late-first-sample":
				clock.Store(int64(time.Second))
			}
			var stopped atomic.Int32
			profile, err := newScanMemoryProfile(ctx, started, hooks, nil, func() { stopped.Add(1) })
			if profile != nil || !errors.Is(err, errScanMemoryProfile) || stopped.Load() != 1 || strings.Contains(err.Error(), "PRIVATE_SOURCE_ERROR") {
				t.Fatal("failed startup leaked its ticker or arbitrary source failure")
			}
		})
	}
}

func TestScanMemorySamplerFinishWaitsForActiveReader(t *testing.T) {
	started := time.Now()
	hooks, _ := scanMemoryTestHooks(started)
	entered, release := make(chan struct{}), make(chan struct{})
	var blocking atomic.Bool
	var once sync.Once
	original := hooks.sample
	hooks.sample = func() (residentSample, error) {
		if blocking.Load() {
			once.Do(func() { close(entered) })
			<-release
		}
		return original()
	}
	ticks := make(chan time.Time, 1)
	profile, err := newScanMemoryProfile(context.Background(), started, hooks, ticks, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_, _ = profile.finish()
	})
	for _, phase := range scanMemoryPhases[1:] {
		if err := profile.changePhase(phase); err != nil {
			t.Fatal(err)
		}
	}
	blocking.Store(true)
	ticks <- time.Now()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		_, _ = profile.finish()
		t.Fatal("sampler did not enter the controlled reader")
	}
	finished := make(chan error, 1)
	go func() { _, err := profile.finish(); finished <- err }()
	select {
	case <-profile.stop:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("finish did not close sample admission")
	}
	select {
	case <-finished:
		close(release)
		t.Fatal("finish returned while the background reader remained active")
	default:
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal("released sampler failed to preserve complete evidence", err)
		}
	case <-time.After(time.Second):
		t.Fatal("finish did not join the released reader")
	}
}
