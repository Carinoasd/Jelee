//go:build jelee_probe_tests

package runtime

import (
	"context"
	"sync/atomic"
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
)

type imagesSoakObserver struct{ read func() imageadapter.Stats }
type imagesSoakRange struct{ samples, rssMin, rssPeak, heapMin, heapPeak uint64 }
type imagesSoakCollectorReply struct {
	ranges imagesSoakRange
	err    error
}
type imagesSoakCollectorRequest struct {
	data       any
	beginNanos int64
	hour       int
	reply      chan imagesSoakCollectorReply
}

type imagesSoakMemorySummary struct {
	SamplerFailure          *imagesSoakSampleFailure    `json:"samplerFailure,omitempty"`
	CollectorFailureCode    string                      `json:"collectorFailureCode,omitempty"`
	Runtime                 memoryRuntimeSettings       `json:"runtime"`
	CgroupBefore            memoryCgroupSnapshot        `json:"cgroupBefore"`
	CgroupAfter             memoryCgroupSnapshot        `json:"cgroupAfter"`
	BeforeReadStartedNanos  int64                       `json:"beforeReadStartedNanos"`
	BeforeReadFinishedNanos int64                       `json:"beforeReadFinishedNanos"`
	AfterReadStartedNanos   int64                       `json:"afterReadStartedNanos"`
	AfterReadFinishedNanos  int64                       `json:"afterReadFinishedNanos"`
	GCBefore                scanGCBoundary              `json:"gcBefore"`
	GCAfter                 scanGCBoundary              `json:"gcAfter"`
	ProcessorMaxima         imagesProcessorObservations `json:"processorMaxima"`
	SampleCount             uint64                      `json:"sampleCount"`
	BlockCount              uint64                      `json:"blockCount"`
	Complete                bool                        `json:"complete"`
}

// A single consumer serializes all events and owns the bounded hourly ranges.
// No full-day sample slice is retained. Request replies are buffered so caller
// cancellation cannot leave the consumer blocked waiting for its caller.
type imagesSoakCollector struct {
	started     time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	observer    atomic.Pointer[imagesSoakObserver]
	sampler     *imagesSoakSampler
	stream      *imagesSoakStream
	requests    chan imagesSoakCollectorRequest
	done        chan struct{}
	err         error // consumer-owned; read only after done
	report      imagesSoakMemorySummary
	ranges      [24]imagesSoakRange
	workStarted int64
}

func startImagesSoakCollector(ctx context.Context, output imagesSoakOutput, runID, scope string, configuration imagesMemoryConfiguration) (*imagesSoakCollector, error) {
	started := time.Now()
	c, cancel := context.WithCancel(ctx)
	p := &imagesSoakCollector{started: started, ctx: c, cancel: cancel, requests: make(chan imagesSoakCollectorRequest), done: make(chan struct{}), workStarted: -1}
	fail := func() (*imagesSoakCollector, error) { cancel(); return nil, errImagesSoakStream }
	var err error
	p.stream, err = newImagesSoakStream(output, runID)
	if err != nil {
		return fail()
	}
	p.report.Runtime, err = readMemoryRuntimeSettings()
	if err != nil {
		return fail()
	}
	p.report.BeforeReadStartedNanos = time.Since(started).Nanoseconds()
	p.report.CgroupBefore, err = readMemoryCgroup(memoryCgroupRoot)
	p.report.BeforeReadFinishedNanos = time.Since(started).Nanoseconds()
	if err != nil {
		return fail()
	}
	p.report.GCBefore, err = readScanGCBoundary(started, time.Now)
	if err != nil {
		return fail()
	}
	start := imagesSoakStart{Scope: scope, Runtime: p.report.Runtime, Configuration: configuration, CgroupBefore: p.report.CgroupBefore, BeforeReadStartedNanos: p.report.BeforeReadStartedNanos, BeforeReadFinishedNanos: p.report.BeforeReadFinishedNanos, GCBefore: p.report.GCBefore}
	if p.stream.write(c, time.Since(started).Nanoseconds(), start) != nil {
		return fail()
	}
	p.sampler = startImagesSoakSampler(c, started, func() imageadapter.Stats {
		if observer := p.observer.Load(); observer != nil {
			return observer.read()
		}
		return imageadapter.Stats{}
	})
	go p.consume()
	return p, nil
}

func (p *imagesSoakCollector) observe(read func() imageadapter.Stats) error {
	if read == nil || !p.observer.CompareAndSwap(nil, &imagesSoakObserver{read}) {
		return errImagesSoakSampler
	}
	return nil
}

func (p *imagesSoakCollector) consumeBlock(block imagesSoakSampleBlock) error {
	if block.FirstSampleIndex != p.report.SampleCount || len(block.Samples) == 0 {
		return errImagesSoakSampler
	}
	if p.stream.write(p.ctx, time.Since(p.started).Nanoseconds(), block) != nil {
		return errImagesSoakStream
	}
	p.report.SampleCount += uint64(len(block.Samples))
	p.report.BlockCount++
	a, b := &p.report.ProcessorMaxima, block.ProcessorMaxima
	a.Observations += b.Observations
	a.MaxActive = max(a.MaxActive, b.MaxActive)
	a.MaxReservedBytes = max(a.MaxReservedBytes, b.MaxReservedBytes)
	a.MaxEstimatedImageBytes = max(a.MaxEstimatedImageBytes, b.MaxEstimatedImageBytes)
	a.MaxCacheEntries = max(a.MaxCacheEntries, b.MaxCacheEntries)
	a.MaxCacheBytes = max(a.MaxCacheBytes, b.MaxCacheBytes)
	for _, sample := range block.Samples {
		if p.workStarted < 0 || sample.ElapsedNanos < p.workStarted {
			continue
		}
		hour := (sample.ElapsedNanos - p.workStarted) / int64(time.Hour)
		if hour >= 24 {
			continue
		}
		r := &p.ranges[hour]
		if r.samples == 0 {
			r.rssMin = sample.RSSBytes
			r.heapMin = sample.HeapBytes
		}
		r.samples++
		r.rssMin = min(r.rssMin, sample.RSSBytes)
		r.rssPeak = max(r.rssPeak, sample.RSSBytes)
		r.heapMin = min(r.heapMin, sample.HeapBytes)
		r.heapPeak = max(r.heapPeak, sample.HeapBytes)
	}
	return nil
}

func (p *imagesSoakCollector) consume() {
	defer close(p.done)
	defer func() {
		if p.err != nil {
			p.cancel()
		}
		<-p.sampler.done
	}()
	for {
		select {
		case <-p.ctx.Done():
			p.err = errImagesSoakStream
			return
		case block, open := <-p.sampler.blocks:
			if !open {
				<-p.sampler.done
				p.err = p.sampler.err
				return
			}
			if p.err = p.consumeBlock(block); p.err != nil {
				return
			}
		case request := <-p.requests:
			if request.beginNanos >= 0 {
				if p.workStarted >= 0 || request.beginNanos > time.Since(p.started).Nanoseconds() {
					p.err = errImagesSoakStream
					request.reply <- imagesSoakCollectorReply{err: p.err}
					return
				}
				p.workStarted = request.beginNanos
			}
			// A preceding flush has already enqueued the last sample of an hour.
			// Drain before taking its range so channel selection cannot omit it.
		drain:
			for {
				select {
				case block, open := <-p.sampler.blocks:
					if !open {
						break drain
					}
					if p.err = p.consumeBlock(block); p.err != nil {
						request.reply <- imagesSoakCollectorReply{err: p.err}
						return
					}
				default:
					break drain
				}
			}
			reply := imagesSoakCollectorReply{}
			if request.hour >= 0 {
				if request.hour >= 24 || p.ranges[request.hour].samples == 0 {
					reply.err = errImagesSoakStream
				} else {
					reply.ranges = p.ranges[request.hour]
				}
			} else if request.data != nil {
				reply.err = p.stream.write(p.ctx, time.Since(p.started).Nanoseconds(), request.data)
			}
			request.reply <- reply
			if reply.err != nil {
				p.err = reply.err
				return
			}
		}
	}
}

func (p *imagesSoakCollector) request(ctx context.Context, request imagesSoakCollectorRequest) (imagesSoakCollectorReply, error) {
	request.reply = make(chan imagesSoakCollectorReply, 1)
	select {
	case p.requests <- request:
	case <-ctx.Done():
		return imagesSoakCollectorReply{}, errImagesSoakStream
	case <-p.done:
		return imagesSoakCollectorReply{}, errImagesSoakStream
	}
	select {
	case reply := <-request.reply:
		return reply, reply.err
	case <-ctx.Done():
		return imagesSoakCollectorReply{}, errImagesSoakStream
	case <-p.done:
		select {
		case reply := <-request.reply:
			return reply, reply.err
		default:
			return imagesSoakCollectorReply{}, errImagesSoakStream
		}
	}
}

func (p *imagesSoakCollector) begin(nanos int64) error {
	if nanos < 0 {
		return errImagesSoakStream
	}
	_, err := p.request(p.ctx, imagesSoakCollectorRequest{beginNanos: nanos, hour: -1})
	return err
}
func (p *imagesSoakCollector) emit(ctx context.Context, data any) error {
	_, err := p.request(ctx, imagesSoakCollectorRequest{data: data, beginNanos: -1, hour: -1})
	return err
}
func (p *imagesSoakCollector) phase(ctx context.Context, phase string) (residentSample, error) {
	return p.sampler.samplePhase(ctx, phase, phase == "stopped")
}
func (p *imagesSoakCollector) hourRange(ctx context.Context, index int) (uint64, uint64, uint64, uint64, error) {
	if index < 0 || index >= 24 {
		return 0, 0, 0, 0, errImagesSoakStream
	}
	if _, err := p.sampler.samplePhaseFlush(ctx, "idle", false, true); err != nil {
		return 0, 0, 0, 0, err
	}
	reply, err := p.request(ctx, imagesSoakCollectorRequest{beginNanos: -1, hour: index})
	return reply.ranges.rssMin, reply.ranges.rssPeak, reply.ranges.heapMin, reply.ranges.heapPeak, err
}

func (p *imagesSoakCollector) finish() (imagesSoakMemorySummary, error) {
	<-p.done
	if p.err != nil || p.ctx.Err() != nil || p.observer.Load() == nil {
		return p.report, errImagesSoakStream
	}
	var err error
	p.report.GCAfter, err = readScanGCBoundary(p.started, time.Now)
	if err != nil || compareScanGCBoundaries(p.report.GCBefore, p.report.GCAfter) != nil {
		return p.report, errImagesSoakStream
	}
	p.report.AfterReadStartedNanos = time.Since(p.started).Nanoseconds()
	p.report.CgroupAfter, err = readMemoryCgroup(memoryCgroupRoot)
	p.report.AfterReadFinishedNanos = time.Since(p.started).Nanoseconds()
	if err != nil {
		return p.report, errImagesSoakStream
	}
	settings, err := readMemoryRuntimeSettings()
	if err != nil || settings != p.report.Runtime {
		return p.report, errImagesSoakStream
	}
	p.report.Complete = true
	return p.report, nil
}

func (p *imagesSoakCollector) abort() { p.cancel(); <-p.done }

// Both goroutines have joined before their failure observations are read.
// Preserve collected partial evidence without marking it complete.
func (p *imagesSoakCollector) failedSummary() imagesSoakMemorySummary {
	<-p.done
	<-p.sampler.done
	report := p.report
	report.SamplerFailure = p.sampler.failure
	if p.err == errImagesSoakSampler {
		report.CollectorFailureCode = "sampler_failed"
	} else if p.err != nil {
		report.CollectorFailureCode = "stream_or_context_failed"
	}
	return report
}
