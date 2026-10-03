//go:build jelee_probe_tests

package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
)

func collectorFixture(t *testing.T) (*imagesSoakCollector, net.Conn, <-chan []imagesSoakEnvelope) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	writer, reader := net.Pipe()
	stream, err := newImagesSoakStream(writer, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	stream.timeout = 50 * time.Millisecond
	output := make(chan []imagesSoakEnvelope, 1)
	go func() {
		var events []imagesSoakEnvelope
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			var envelope struct {
				Event imagesSoakEnvelope `json:"imagesSoakEvent"`
			}
			if json.Unmarshal(scanner.Bytes(), &envelope) != nil {
				output <- nil
				return
			}
			events = append(events, envelope.Event)
		}
		output <- events
	}()
	started := time.Now()
	if stream.write(ctx, 0, imagesSoakStart{Scope: "smoke"}) != nil {
		t.Fatal("start stream")
	}
	p := &imagesSoakCollector{started: started, ctx: ctx, cancel: cancel, stream: stream, requests: make(chan imagesSoakCollectorRequest), done: make(chan struct{}), workStarted: -1}
	var clock atomic.Int64
	// Tight unit-test requests can share a Windows clock tick. Keep the injected
	// clock strictly increasing; production retains the native monotonic reader.
	now := func() time.Time { return started.Add(time.Since(started) + time.Duration(clock.Add(1))) }
	p.sampler = newImagesSoakSampler(ctx, started, now, func() (residentSample, error) {
		return residentSample{RSSBytes: 200 << 20, HeapBytes: 100 << 20, Goroutines: 20}, nil
	}, func() imageadapter.Stats {
		if o := p.observer.Load(); o != nil {
			return o.read()
		}
		return imageadapter.Stats{}
	}, make(chan time.Time), func() {}, time.Second)
	go p.consume()
	t.Cleanup(func() { p.abort(); writer.Close(); reader.Close() })
	return p, writer, output
}

func TestImagesSoakCollectorFlushRangesAndSerializedOrdering(t *testing.T) {
	p, writer, output := collectorFixture(t)
	if p.observe(func() imageadapter.Stats { return imageadapter.Stats{Active: 2, ReservedBytes: 192 << 20} }) != nil {
		t.Fatal("attach actual observer")
	}
	if p.observe(func() imageadapter.Stats { return imageadapter.Stats{} }) == nil {
		t.Fatal("observer replaced")
	}
	if p.begin(time.Since(p.started).Nanoseconds()) != nil {
		t.Fatal("begin")
	}
	for i := 0; i < 120; i++ {
		if _, err := p.phase(p.ctx, "cold"); err != nil {
			t.Fatal(err)
		}
	}
	lo, hi, heapLo, heapHi, err := p.hourRange(p.ctx, 0)
	if err != nil || lo != 200<<20 || hi != lo || heapLo != 100<<20 || heapHi != heapLo {
		t.Fatal("flush missed current-hour samples")
	}
	if _, err := p.phase(p.ctx, "stopped"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("collector failed to join")
	}
	if p.err != nil || p.report.SampleCount != 123 || p.report.ProcessorMaxima.MaxActive != 2 {
		t.Fatal("incomplete collector summary")
	}
	writer.Close()
	var events []imagesSoakEnvelope
	select {
	case events = <-output:
	case <-time.After(time.Second):
		t.Fatal("reader failed to join")
	}
	if len(events) != 5 {
		t.Fatalf("event count=%d", len(events))
	}
	last := int64(0)
	for i, event := range events {
		if event.Seq != uint64(i) || event.ElapsedNanos < last {
			t.Fatal("unordered stream")
		}
		last = event.ElapsedNanos
	}
}

func TestImagesSoakCollectorFailedWriterCancelsSampler(t *testing.T) {
	p, writer, _ := collectorFixture(t)
	writer.Close()
	if p.emit(p.ctx, imagesSoakHour{Index: 0}) == nil {
		t.Fatal("closed writer accepted")
	}
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("collector leaked")
	}
	select {
	case <-p.sampler.done:
	default:
		t.Fatal("sampler leaked")
	}
	if p.err == nil || p.ctx.Err() == nil {
		t.Fatal("failure did not cancel producer")
	}
}

func TestImagesSoakCollectorRejectsDuplicateBeginAndInvalidHour(t *testing.T) {
	p, _, _ := collectorFixture(t)
	if _, _, _, _, err := p.hourRange(p.ctx, -1); err == nil {
		t.Fatal("invalid hour accepted")
	}
	if p.begin(time.Since(p.started).Nanoseconds()) != nil {
		t.Fatal("begin")
	}
	if p.begin(time.Since(p.started).Nanoseconds()) == nil {
		t.Fatal("duplicate begin accepted")
	}
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("duplicate begin left consumer running")
	}
}

func TestImagesSoakCollectorRetainsRejectedObservation(t *testing.T) {
	p, _, _ := collectorFixture(t)
	if p.observe(func() imageadapter.Stats { return imageadapter.Stats{Active: 3} }) != nil {
		t.Fatal("attach observer")
	}
	if _, err := p.phase(p.ctx, "cold"); err == nil {
		t.Fatal("invalid observation accepted")
	}
	p.abort()
	report := p.failedSummary()
	if report.Complete || report.CollectorFailureCode != "sampler_failed" || report.SamplerFailure == nil || report.SamplerFailure.Code != "processor_budget_invalid" || report.SamplerFailure.Processor.Active != 3 {
		t.Fatalf("failure evidence lost: %+v", report)
	}
}
