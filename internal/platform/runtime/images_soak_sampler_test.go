//go:build jelee_probe_tests

package runtime

import (
	"context"
	"errors"
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
)

var errImagesSoakSampler = errors.New("images_soak_sampler_invalid")

type imagesSoakSampleBlock struct {
	FirstSampleIndex uint64                      `json:"firstSampleIndex"`
	Samples          []residentSample            `json:"samples"`
	ProcessorMaxima  imagesProcessorObservations `json:"processorMaxima"`
}

type imagesSoakSampleCommand struct {
	phase string
	stop  bool
	flush bool
	reply chan residentSample
}

// One goroutine owns sampling and its fixed-size block. A single stream writer
// drains blocks; backpressure is bounded to four blocks and five seconds.
// Runtime/cgroup/GC boundaries and whole-run acceptance belong to the caller.
type imagesSoakSampler struct {
	blocks   chan imagesSoakSampleBlock
	commands chan imagesSoakSampleCommand
	done     chan struct{}
	err      error // read only after done
	count    uint64
}

func startImagesSoakSampler(ctx context.Context, started time.Time, observe func() imageadapter.Stats) *imagesSoakSampler {
	ticker := time.NewTicker(time.Second)
	return newImagesSoakSampler(ctx, started, time.Now, readResidentSample, observe, ticker.C, ticker.Stop, 5*time.Second)
}

func newImagesSoakSampler(ctx context.Context, started time.Time, now func() time.Time, read func() (residentSample, error), observe func() imageadapter.Stats, ticks <-chan time.Time, stopTicks func(), queueTimeout time.Duration) *imagesSoakSampler {
	s := &imagesSoakSampler{blocks: make(chan imagesSoakSampleBlock, 4), commands: make(chan imagesSoakSampleCommand), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer close(s.blocks)
		defer stopTicks()
		phase := "startup"
		block := imagesSoakSampleBlock{Samples: make([]residentSample, 0, 60)}
		var previous residentSample
		flush := func() bool {
			if len(block.Samples) == 0 {
				return true
			}
			timer := time.NewTimer(queueTimeout)
			defer timer.Stop()
			select {
			case s.blocks <- block:
				// Each sent block exclusively owns its backing array.
				block = imagesSoakSampleBlock{FirstSampleIndex: s.count, Samples: make([]residentSample, 0, 60)}
				return true
			case <-ctx.Done():
			case <-timer.C:
			}
			s.err = errImagesSoakSampler
			return false
		}
		capture := func() bool {
			if ctx.Err() != nil || observe == nil || s.count >= 90000 {
				s.err = errImagesSoakSampler
				return false
			}
			value, err := read()
			value.ElapsedNanos, value.Phase = now().Sub(started).Nanoseconds(), phase
			if err != nil || value.RSSBytes == 0 || value.RSSBytes > 464<<20 || value.Goroutines < 1 ||
				value.ElapsedNanos < 0 || value.ElapsedNanos > int64(25*time.Hour) ||
				(s.count == 0 && value.ElapsedNanos > int64(time.Second)) ||
				(s.count > 0 && (value.ElapsedNanos <= previous.ElapsedNanos || value.ElapsedNanos-previous.ElapsedNanos > int64(5*time.Second) ||
					value.TotalAllocBytes < previous.TotalAllocBytes || value.NumGC < previous.NumGC || value.PauseTotalNS < previous.PauseTotalNS)) {
				s.err = errImagesSoakSampler
				return false
			}
			stats := observe()
			if stats.Active < 0 || stats.Active > 2 || stats.ReservedBytes < 0 || stats.ReservedBytes > 192<<20 ||
				stats.MaxEstimatedImageBytes < 0 || stats.MaxEstimatedImageBytes > 96<<20 ||
				stats.CacheEntries < 0 || stats.CacheEntries > 128 || stats.CacheBytes < 0 || stats.CacheBytes > 32<<20 {
				s.err = errImagesSoakSampler
				return false
			}
			maxima := &block.ProcessorMaxima
			maxima.Observations++
			maxima.MaxActive = max(maxima.MaxActive, stats.Active)
			maxima.MaxReservedBytes = max(maxima.MaxReservedBytes, stats.ReservedBytes)
			maxima.MaxEstimatedImageBytes = max(maxima.MaxEstimatedImageBytes, stats.MaxEstimatedImageBytes)
			maxima.MaxCacheEntries = max(maxima.MaxCacheEntries, stats.CacheEntries)
			maxima.MaxCacheBytes = max(maxima.MaxCacheBytes, stats.CacheBytes)
			block.Samples = append(block.Samples, value)
			previous = value
			s.count++
			return len(block.Samples) < 60 || flush()
		}
		if !capture() {
			return
		}
		for {
			select {
			case <-ctx.Done():
				s.err = errImagesSoakSampler
				return
			case _, open := <-ticks:
				if !open {
					s.err = errImagesSoakSampler
					return
				}
				if !capture() {
					return
				}
			case command := <-s.commands:
				if !imagesSoakPhaseValid(command.phase) || command.stop && command.phase != "stopped" {
					s.err = errImagesSoakSampler
					return
				}
				phase = command.phase
				if !capture() {
					return
				}
				if (command.stop || command.flush) && !flush() {
					return
				}
				command.reply <- previous
				if command.stop {
					return
				}
			}
		}
	}()
	return s
}

func imagesSoakPhaseValid(phase string) bool {
	switch phase {
	case "startup", "login", "scan", "cold", "warm", "idle", "rotate", "negative", "cancellation", "shutdown", "stopped":
		return true
	}
	return false
}

func (s *imagesSoakSampler) samplePhase(ctx context.Context, phase string, stop bool) (residentSample, error) {
	return s.samplePhaseFlush(ctx, phase, stop, false)
}

func (s *imagesSoakSampler) samplePhaseFlush(ctx context.Context, phase string, stop, flush bool) (residentSample, error) {
	command := imagesSoakSampleCommand{phase: phase, stop: stop, flush: flush, reply: make(chan residentSample, 1)}
	select {
	case s.commands <- command:
	case <-s.done:
		return residentSample{}, errImagesSoakSampler
	case <-ctx.Done():
		return residentSample{}, errImagesSoakSampler
	}
	select {
	case sample := <-command.reply:
		if stop {
			<-s.done
		}
		return sample, nil
	case <-s.done:
		// A successful stop may close done before this select observes reply.
		select {
		case sample := <-command.reply:
			return sample, s.err
		default:
			return residentSample{}, errImagesSoakSampler
		}
	case <-ctx.Done():
		return residentSample{}, errImagesSoakSampler
	}
}
