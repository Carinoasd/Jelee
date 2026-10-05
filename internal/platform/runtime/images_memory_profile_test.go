//go:build jelee_probe_tests

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
)

const imagesMemoryMaxSamples = 3664
const imagesMemoryMaxGap = 5 * time.Second
const imagesMemoryMaxElapsed = time.Hour

var imagesMemoryPhases = [...]string{"startup", "login", "cold", "warm", "negative", "cancellation", "shutdown", "stopped"}
var errImagesMemoryProfile = errors.New("image memory evidence unavailable or invalid")

// Preserve the scan sampler's raw-reader and histogram contracts without
// changing its phase sequence, sample limit, or acceptance semantics.
type imagesMemoryHooks = scanMemoryHooks

type imagesProcessorObservations struct {
	Observations           uint64 `json:"observations"`
	MaxActive              int64  `json:"maxActive"`
	MaxReservedBytes       int64  `json:"maxReservedBytes"`
	MaxEstimatedImageBytes int64  `json:"maxEstimatedImageBytes"`
	MaxCacheEntries        int    `json:"maxCacheEntries"`
	MaxCacheBytes          int64  `json:"maxCacheBytes"`
}

type imagesMemoryReport struct {
	Version                 int                         `json:"version"`
	Complete                bool                        `json:"complete"`
	Runtime                 memoryRuntimeSettings       `json:"runtime"`
	Before                  memoryCgroupSnapshot        `json:"before"`
	After                   memoryCgroupSnapshot        `json:"after"`
	BeforeReadStartedNanos  int64                       `json:"beforeReadStartedNanos"`
	BeforeReadFinishedNanos int64                       `json:"beforeReadFinishedNanos"`
	AfterReadStartedNanos   int64                       `json:"afterReadStartedNanos"`
	AfterReadFinishedNanos  int64                       `json:"afterReadFinishedNanos"`
	Resident                scanResidentProfile         `json:"resident"`
	GCBefore                scanGCBoundary              `json:"gcBefore"`
	GCAfter                 scanGCBoundary              `json:"gcAfter"`
	ElapsedNanos            int64                       `json:"elapsedNanos"`
	ElapsedMillis           int64                       `json:"elapsedMillis"`
	Processor               imagesProcessorObservations `json:"processor"`
}

type imagesMemoryProfile struct {
	mu        sync.Mutex
	ctx       context.Context
	started   time.Time
	hooks     imagesMemoryHooks
	phase     int
	report    imagesMemoryReport
	err       error
	observe   func() imageadapter.Stats
	finalized bool
	stopOnce  sync.Once
	stop      chan struct{}
	done      chan struct{}
}

func startImagesMemoryProfile() (*imagesMemoryProfile, error) {
	started := time.Now()
	ticker := time.NewTicker(time.Second)
	return newImagesMemoryProfile(context.Background(), started, imagesMemoryHooks{
		now: time.Now, sample: readResidentSample, settings: readMemoryRuntimeSettings,
		cgroup:   func() (memoryCgroupSnapshot, error) { return readMemoryCgroup(memoryCgroupRoot) },
		boundary: readScanGCBoundary,
	}, ticker.C, ticker.Stop)
}

// Only this opt-in acceptance helper owns the sampler; product runtime has no
// new polling worker. Injected readers and ticks keep failure tests bounded.
func newImagesMemoryProfile(ctx context.Context, started time.Time, hooks imagesMemoryHooks, ticks <-chan time.Time, stopTicks func()) (*imagesMemoryProfile, error) {
	s := &imagesMemoryProfile{ctx: ctx, started: started, hooks: hooks, stop: make(chan struct{}), done: make(chan struct{}),
		report: imagesMemoryReport{Version: 1, Resident: scanResidentProfile{Version: 1,
			Scope: "worker-process", RSSSource: "/proc/self/statm", Approximate: true,
			SampleEveryMillis: 1000, MaxSamples: imagesMemoryMaxSamples,
			Samples: make([]residentSample, 0, imagesMemoryMaxSamples)}}}
	fail := func() (*imagesMemoryProfile, error) { stopTicks(); return nil, errImagesMemoryProfile }
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
				s.err = errImagesMemoryProfile
				s.mu.Unlock()
				return
			case _, open := <-ticks:
				if !open {
					s.mu.Lock()
					s.err = errImagesMemoryProfile
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

func (s *imagesMemoryProfile) captureLocked() error {
	if s.err != nil {
		return s.err
	}
	if len(s.report.Resident.Samples) >= imagesMemoryMaxSamples {
		s.err = errImagesMemoryProfile
		return s.err
	}
	sample, err := s.hooks.sample()
	sample.ElapsedNanos, sample.Phase = s.hooks.now().Sub(s.started).Nanoseconds(), imagesMemoryPhases[s.phase]
	if err != nil || sample.RSSBytes == 0 || sample.Goroutines < 1 || sample.ElapsedNanos < 0 || sample.ElapsedNanos > int64(imagesMemoryMaxElapsed) {
		s.err = errImagesMemoryProfile
		return s.err
	}
	if len(s.report.Resident.Samples) == 0 {
		if sample.ElapsedNanos > int64(time.Second) || sample.ElapsedNanos < s.report.GCBefore.HistogramFinishedNanos {
			s.err = errImagesMemoryProfile
			return s.err
		}
	} else {
		previous := s.report.Resident.Samples[len(s.report.Resident.Samples)-1]
		if sample.ElapsedNanos <= previous.ElapsedNanos || sample.ElapsedNanos-previous.ElapsedNanos > int64(imagesMemoryMaxGap) ||
			sample.TotalAllocBytes < previous.TotalAllocBytes || sample.NumGC < previous.NumGC || sample.PauseTotalNS < previous.PauseTotalNS {
			s.err = errImagesMemoryProfile
			return s.err
		}
	}
	if err := s.captureProcessorLocked(); err != nil {
		return err
	}
	s.report.Resident.Samples = append(s.report.Resident.Samples, sample)
	return nil
}

func (s *imagesMemoryProfile) capture() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
		return errImagesMemoryProfile
	default:
	}
	return s.captureLocked()
}

func (s *imagesMemoryProfile) changePhase(phase string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	select {
	case <-s.stop:
		return errImagesMemoryProfile
	default:
	}
	if phase == imagesMemoryPhases[s.phase] {
		return nil
	}
	if s.phase+1 >= len(imagesMemoryPhases) || phase != imagesMemoryPhases[s.phase+1] {
		s.err = errImagesMemoryProfile
		return s.err
	}
	s.phase++
	return s.captureLocked()
}

func (s *imagesMemoryProfile) finish() (imagesMemoryReport, error) {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.finalized {
		// Join first, then take the last stopped sample and final observations.
		// No cooling delay, extra GC, or warm-up changes the measured workload.
		if s.ctx.Err() != nil {
			s.err = errImagesMemoryProfile
		}
		if s.err == nil {
			_ = s.captureLocked()
		}
		if s.phase != len(imagesMemoryPhases)-1 || s.observe == nil || s.report.Processor.Observations == 0 {
			s.err = errImagesMemoryProfile
		}
		var err error
		s.report.GCAfter, err = s.hooks.boundary(s.started, s.hooks.now)
		if err != nil || compareScanGCBoundaries(s.report.GCBefore, s.report.GCAfter) != nil {
			s.err = errImagesMemoryProfile
		}
		s.report.AfterReadStartedNanos = s.hooks.now().Sub(s.started).Nanoseconds()
		s.report.After, err = s.hooks.cgroup()
		s.report.AfterReadFinishedNanos = s.hooks.now().Sub(s.started).Nanoseconds()
		if err != nil || s.report.AfterReadStartedNanos < s.report.GCAfter.HistogramFinishedNanos ||
			s.report.AfterReadFinishedNanos < s.report.AfterReadStartedNanos {
			s.err = errImagesMemoryProfile
		}
		settings, err := s.hooks.settings()
		if err != nil || settings != s.report.Runtime {
			s.err = errImagesMemoryProfile
		}
		s.report.ElapsedNanos = s.hooks.now().Sub(s.started).Nanoseconds()
		s.report.ElapsedMillis = s.report.ElapsedNanos / int64(time.Millisecond)
		if s.report.ElapsedNanos < s.report.AfterReadFinishedNanos || s.report.ElapsedNanos > int64(imagesMemoryMaxElapsed) {
			s.err = errImagesMemoryProfile
		}
		if len(s.report.Resident.Samples) > 0 {
			last := s.report.Resident.Samples[len(s.report.Resident.Samples)-1].ElapsedNanos
			if s.report.GCAfter.CountersStartedNanos < last || s.report.ElapsedNanos-last > int64(imagesMemoryMaxGap) {
				s.err = errImagesMemoryProfile
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

// The acceptance test supplies the very Processor created by the production
// Fx graph. Aggregate observations do not claim atomic counter relationships.
func (s *imagesMemoryProfile) observeProcessor(observe func() imageadapter.Stats) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if observe == nil || s.observe != nil || s.finalized || s.err != nil {
		return errImagesMemoryProfile
	}
	select {
	case <-s.stop:
		return errImagesMemoryProfile
	default:
	}
	s.observe = observe
	return s.captureProcessorLocked()
}

func (s *imagesMemoryProfile) captureProcessorLocked() error {
	if s.observe == nil {
		return nil
	}
	value := s.observe()
	if value.Active < 0 || value.Active > 2 || value.ReservedBytes < 0 || value.ReservedBytes > 192<<20 ||
		value.MaxEstimatedImageBytes < 0 || value.MaxEstimatedImageBytes > 96<<20 ||
		value.CacheEntries < 0 || value.CacheEntries > 128 || value.CacheBytes < 0 || value.CacheBytes > 32<<20 {
		s.err = errImagesMemoryProfile
		return s.err
	}
	result := &s.report.Processor
	result.Observations++
	result.MaxActive = max(result.MaxActive, value.Active)
	result.MaxReservedBytes = max(result.MaxReservedBytes, value.ReservedBytes)
	result.MaxEstimatedImageBytes = max(result.MaxEstimatedImageBytes, value.MaxEstimatedImageBytes)
	result.MaxCacheEntries = max(result.MaxCacheEntries, value.CacheEntries)
	result.MaxCacheBytes = max(result.MaxCacheBytes, value.CacheBytes)
	return nil
}

func TestImagesMemorySamplerCompleteOrderingOwnershipAndJoin(t *testing.T) {
	started := time.Now()
	hooks, _ := scanMemoryTestHooks(started)
	var stopped atomic.Int32
	profile, err := newImagesMemoryProfile(context.Background(), started, hooks, nil, func() { stopped.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = profile.finish() })
	if err := profile.observeProcessor(func() imageadapter.Stats { return imageadapter.Stats{} }); err != nil {
		t.Fatal(err)
	}
	for _, phase := range imagesMemoryPhases[1:] {
		if err := profile.changePhase(phase); err != nil {
			t.Fatal(err)
		}
	}
	report, err := profile.finish()
	if err != nil || !report.Complete || !report.Resident.Complete || stopped.Load() != 1 || len(report.Resident.Samples) != 9 {
		t.Fatal("sampler did not stop and retain every phase plus a terminal observation", err)
	}
	if report.Resident.MaxSamples != 3664 || report.Resident.SampleEveryMillis != 1000 || report.Resident.RSSSource != "/proc/self/statm" || !report.Resident.Approximate {
		t.Fatal("report lost the bounded sampling and measurement-source contract")
	}
	for index, phase := range imagesMemoryPhases {
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

func TestImagesMemorySamplerFailsClosedAndKeepsBoundedPartialEvidence(t *testing.T) {
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
			profile, err := newImagesMemoryProfile(ctx, started, hooks, nil, func() { stopped.Store(true) })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = profile.finish() })
			if err := profile.observeProcessor(func() imageadapter.Stats { return imageadapter.Stats{} }); err != nil {
				t.Fatal(err)
			}
			broken.Store(true)
			switch kind {
			case "gap":
				clock.Add(int64(imagesMemoryMaxGap))
				_ = profile.capture()
			case "backward-time":
				clock.Store(0)
				_ = profile.capture()
			case "phase":
				_ = profile.changePhase("scan")
			case "capacity":
				for index := 1; index <= imagesMemoryMaxSamples; index++ {
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
				for _, phase := range imagesMemoryPhases[1:] {
					if err := profile.changePhase(phase); err != nil {
						t.Fatal(err)
					}
				}
			default:
				_ = profile.capture()
			}
			report, err := profile.finish()
			if !errors.Is(err, errImagesMemoryProfile) || report.Complete || report.Resident.Complete || len(report.Resident.Samples) == 0 || len(report.Resident.Samples) > imagesMemoryMaxSamples || !stopped.Load() {
				t.Fatal("invalid evidence was accepted, lost, or failed to stop its sampler", err)
			}
			if kind == "capacity" && (len(report.Resident.Samples) != imagesMemoryMaxSamples || cap(profile.report.Resident.Samples) != imagesMemoryMaxSamples) {
				t.Fatal("capacity failure discarded samples or allocated an unbounded buffer")
			}
			body, marshalErr := json.Marshal(report)
			if marshalErr != nil || strings.Contains(string(body), "PRIVATE_SOURCE_ERROR") || strings.Contains(err.Error(), "PRIVATE_SOURCE_ERROR") {
				t.Fatal("partial evidence leaked an arbitrary source error")
			}
		})
	}
}

func TestImagesMemorySamplerUnavailableStartupStopsTicker(t *testing.T) {
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
			profile, err := newImagesMemoryProfile(ctx, started, hooks, nil, func() { stopped.Add(1) })
			if profile != nil || !errors.Is(err, errImagesMemoryProfile) || stopped.Load() != 1 || strings.Contains(err.Error(), "PRIVATE_SOURCE_ERROR") {
				t.Fatal("failed startup leaked its ticker or arbitrary source failure")
			}
		})
	}
}

func TestImagesMemorySamplerFinishWaitsForActiveReader(t *testing.T) {
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
	profile, err := newImagesMemoryProfile(context.Background(), started, hooks, ticks, func() {})
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
	for _, phase := range imagesMemoryPhases[1:] {
		if err := profile.changePhase(phase); err != nil {
			t.Fatal(err)
		}
	}
	if err := profile.observeProcessor(func() imageadapter.Stats { return imageadapter.Stats{} }); err != nil {
		t.Fatal(err)
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

func TestImagesMemoryProcessorObservationUsesOnlyTheAttachedInstance(t *testing.T) {
	started := time.Now()
	hooks, _ := scanMemoryTestHooks(started)
	profile, err := newImagesMemoryProfile(context.Background(), started, hooks, nil, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = profile.finish() })
	var active atomic.Int64
	active.Store(2)
	observer := func() imageadapter.Stats {
		return imageadapter.Stats{Active: active.Load(), ReservedBytes: 192 << 20, MaxEstimatedImageBytes: 90 << 20, CacheEntries: 128, CacheBytes: 32 << 20}
	}
	if profile.observeProcessor(nil) == nil || profile.observeProcessor(observer) != nil || profile.observeProcessor(observer) == nil {
		t.Fatal("processor observer admission is not single-use")
	}
	for _, phase := range imagesMemoryPhases[1:] {
		if profile.changePhase(phase) != nil {
			t.Fatal("image phase failed")
		}
	}
	active.Store(0)
	report, err := profile.finish()
	if err != nil || report.Processor.Observations != 9 || report.Processor.MaxActive != 2 || report.Processor.MaxReservedBytes != 192<<20 || report.Processor.MaxEstimatedImageBytes != 90<<20 || report.Processor.MaxCacheEntries != 128 || report.Processor.MaxCacheBytes != 32<<20 {
		t.Fatal("processor aggregation lost observed bounds", err)
	}
	if profile.observeProcessor(observer) == nil {
		t.Fatal("finalized sampler accepted a replacement processor")
	}
}

func TestImagesMemoryProcessorMissingOrOutOfBudgetRejectsCompletion(t *testing.T) {
	for _, kind := range []string{"missing", "active", "reserved", "estimated", "entries", "bytes", "negative"} {
		t.Run(kind, func(t *testing.T) {
			started := time.Now()
			hooks, _ := scanMemoryTestHooks(started)
			profile, err := newImagesMemoryProfile(context.Background(), started, hooks, nil, func() {})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = profile.finish() })
			value := imageadapter.Stats{}
			switch kind {
			case "active":
				value.Active = 3
			case "reserved":
				value.ReservedBytes = 192<<20 + 1
			case "estimated":
				value.MaxEstimatedImageBytes = 96<<20 + 1
			case "entries":
				value.CacheEntries = 129
			case "bytes":
				value.CacheBytes = 32<<20 + 1
			case "negative":
				value.Active = -1
			}
			if kind != "missing" {
				if profile.observeProcessor(func() imageadapter.Stats { return value }) == nil {
					t.Fatal("processor limit violation accepted")
				}
			}
			for _, phase := range imagesMemoryPhases[1:] {
				_ = profile.changePhase(phase)
			}
			report, err := profile.finish()
			if err != errImagesMemoryProfile || report.Complete || report.Resident.Complete {
				t.Fatal("missing or invalid processor evidence passed")
			}
		})
	}
}
