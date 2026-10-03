//go:build jelee_probe_tests

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
)

func soakSamplerFixture(ctx context.Context, read func() (residentSample, error), observe func() imageadapter.Stats) (*imagesSoakSampler, *atomic.Bool) {
	started := time.Now()
	var clock atomic.Int64
	stopped := &atomic.Bool{}
	if read == nil {
		read = func() (residentSample, error) {
			return residentSample{RSSBytes: 200 << 20, HeapBytes: 100 << 20, Goroutines: 20}, nil
		}
	}
	if observe == nil {
		observe = func() imageadapter.Stats { return imageadapter.Stats{Active: 2, ReservedBytes: 192 << 20} }
	}
	s := newImagesSoakSampler(ctx, started, func() time.Time { return started.Add(time.Duration(clock.Add(1)) * time.Millisecond) },
		read, observe, make(chan time.Time), func() { stopped.Store(true) }, 10*time.Millisecond)
	return s, stopped
}

func awaitSoakSampler(t *testing.T, s *imagesSoakSampler) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("sampler did not join")
	}
}

func TestImagesSoakSamplerBlocksOwnershipAndFinalFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, stopped := soakSamplerFixture(ctx, nil, nil)
	for i := 0; i < 60; i++ {
		if _, err := s.samplePhase(ctx, "cold", false); err != nil {
			t.Fatal(err)
		}
	}
	first := <-s.blocks
	if first.FirstSampleIndex != 0 || len(first.Samples) != 60 || first.ProcessorMaxima.Observations != 60 {
		t.Fatal("incorrect full block")
	}
	first.Samples[0].RSSBytes = 1
	if _, err := s.samplePhase(ctx, "stopped", true); err != nil {
		t.Fatal(err)
	}
	last := <-s.blocks
	if last.FirstSampleIndex != 60 || len(last.Samples) != 2 || last.Samples[1].Phase != "stopped" || last.Samples[0].RSSBytes != 200<<20 {
		t.Fatal("incorrect final block or aliased storage")
	}
	if _, open := <-s.blocks; open || s.err != nil || s.count != 62 || !stopped.Load() {
		t.Fatal("sampler not cleanly stopped")
	}
}

func TestImagesSoakSamplerBackpressureIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, stopped := soakSamplerFixture(ctx, nil, nil)
	for i := 0; i < 310; i++ {
		if _, err := s.samplePhase(ctx, "scan", false); err != nil {
			break
		}
	}
	awaitSoakSampler(t, s)
	if s.err == nil || len(s.blocks) != 4 || s.count != 300 || !stopped.Load() {
		t.Fatal("backpressure did not fail at the bounded queue")
	}
	if s.failure == nil || s.failure.Code != "block_queue_timeout" {
		t.Fatal("backpressure diagnosis lost")
	}
}

func TestImagesSoakSamplerCancellationJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s, stopped := soakSamplerFixture(ctx, nil, nil)
	cancel()
	awaitSoakSampler(t, s)
	if s.err == nil || !stopped.Load() {
		t.Fatal("cancellation not observed")
	}
}

func TestImagesSoakSamplerBadReadAndBudgetFail(t *testing.T) {
	for _, tc := range []struct {
		name    string
		read    func() (residentSample, error)
		observe func() imageadapter.Stats
	}{
		{name: "read", read: func() (residentSample, error) { return residentSample{}, errors.New("private") }},
		{name: "rss", read: func() (residentSample, error) { return residentSample{RSSBytes: (464 << 20) + 1, Goroutines: 1}, nil }},
		{name: "processor", observe: func() imageadapter.Stats { return imageadapter.Stats{Active: 3} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := soakSamplerFixture(context.Background(), tc.read, tc.observe)
			awaitSoakSampler(t, s)
			if s.err != errImagesSoakSampler || s.count != 0 {
				t.Fatal("invalid observation accepted or private error exposed")
			}
			want := map[string]string{"read": "resident_read_failed", "rss": "rss_budget_invalid", "processor": "processor_budget_invalid"}[tc.name]
			if s.failure == nil || s.failure.Code != want || s.failure.SampleIndex != 0 {
				t.Fatalf("lost failed observation: %+v", s.failure)
			}
			if tc.name == "rss" && s.failure.Rejected.RSSBytes != (464<<20)+1 || tc.name == "processor" && s.failure.Processor.Active != 3 {
				t.Fatal("failure discarded observed over-budget value")
			}
			encoded, err := json.Marshal(s.failure)
			if err != nil || strings.Contains(string(encoded), "private") {
				t.Fatal("failure serialized a private reader error")
			}
		})
	}
}

func TestImagesSoakSamplerInvalidPhaseFails(t *testing.T) {
	for _, phase := range []string{"unknown", "cold"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		s, _ := soakSamplerFixture(ctx, nil, nil)
		_, err := s.samplePhase(ctx, phase, true)
		awaitSoakSampler(t, s)
		cancel()
		if err == nil || s.err == nil {
			t.Fatal("invalid stop accepted")
		}
	}
}

func TestImagesSoakSamplerTicksAndClosedSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	var clock atomic.Int64
	ticks := make(chan time.Time)
	s := newImagesSoakSampler(ctx, started, func() time.Time {
		return started.Add(time.Duration(clock.Add(1)) * time.Millisecond)
	}, func() (residentSample, error) {
		return residentSample{RSSBytes: 1, Goroutines: 1}, nil
	}, func() imageadapter.Stats { return imageadapter.Stats{} }, ticks, func() {}, time.Second)
	for i := 0; i < 59; i++ {
		select {
		case ticks <- time.Now():
		case <-ctx.Done():
			t.Fatal("tick reader stalled")
		}
	}
	select {
	case block := <-s.blocks:
		if len(block.Samples) != 60 {
			t.Fatal("tick samples missing")
		}
	case <-ctx.Done():
		t.Fatal("block not emitted")
	}
	close(ticks)
	awaitSoakSampler(t, s)
	if s.err == nil {
		t.Fatal("closed tick source accepted")
	}
}

func TestImagesSoakSamplerCounterRegression(t *testing.T) {
	var reads atomic.Int64
	s, _ := soakSamplerFixture(context.Background(), func() (residentSample, error) {
		return residentSample{RSSBytes: 1, Goroutines: 1, TotalAllocBytes: uint64(3 - reads.Add(1))}, nil
	}, nil)
	if _, err := s.samplePhase(context.Background(), "cold", false); err == nil {
		t.Fatal("counter regression accepted")
	}
	awaitSoakSampler(t, s)
}
