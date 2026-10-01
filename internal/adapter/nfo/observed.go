package nfo

import (
	"context"
	"sync/atomic"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ReaderStats counts actual reader/parser API invocations, independently of
// committed cache progress. CompletedReadBytes excludes unknown partial reads
// on failure. ActiveCalls/PeakCalls count API calls, not retained source buffers.
type ReaderStats struct {
	ReadCalls, CompletedReads, CompletedReadBytes, HashCompletions, ParseCalls uint64
	ActiveCalls, PeakCalls                                                     int64
}

// ObservedReader adds process-local counters to a trusted readonly reader.
// It has no persistence or execution authority and adds no goroutines.
type ObservedReader struct {
	reader                                  app.NFOReader
	reads, completed, bytes, hashes, parses atomic.Uint64
	active, peak                            atomic.Int64
}

func NewObservedReader(reader app.NFOReader) (*ObservedReader, error) {
	if reader == nil || domain.ValidateNFOIdentity(reader.Identity()) != nil {
		return nil, domain.ErrInvalid
	}
	return &ObservedReader{reader: reader}, nil
}

func (*ObservedReader) String() string   { return "nfo observed reader (data redacted)" }
func (*ObservedReader) GoString() string { return "nfo observed reader (data redacted)" }

func (r *ObservedReader) Identity() domain.NFOIdentity {
	if r == nil || r.reader == nil {
		return domain.NFOIdentity{}
	}
	return r.reader.Identity()
}

// Stats is a race-safe live observation, not an atomic transaction across all
// counters. Quiescent snapshots can be differenced for acceptance measurements.
func (r *ObservedReader) Stats() ReaderStats {
	if r == nil {
		return ReaderStats{}
	}
	return ReaderStats{r.reads.Load(), r.completed.Load(), r.bytes.Load(), r.hashes.Load(), r.parses.Load(), r.active.Load(), r.peak.Load()}
}

func (r *ObservedReader) enter() func() {
	active := r.active.Add(1)
	for old := r.peak.Load(); active > old; old = r.peak.Load() {
		if r.peak.CompareAndSwap(old, active) {
			break
		}
	}
	return func() { r.active.Add(-1) }
}

func (r *ObservedReader) Read(ctx context.Context, source domain.NFOSource) (app.NFOReadSource, error) {
	if err := summaryContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.reader == nil {
		return nil, domain.ErrNFOReaderUnavailable
	}
	r.reads.Add(1)
	leave := r.enter()
	defer leave()
	result, err := r.reader.Read(ctx, source)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if result == nil {
		return nil, domain.ErrNFOReaderUnavailable
	}
	stamp := result.Stamp()
	if domain.ValidateNFOStamp(stamp) != nil || stamp.Size > r.Identity().MaxSourceBytes {
		return nil, domain.ErrNFOReaderUnavailable
	}
	r.completed.Add(1)
	r.bytes.Add(uint64(stamp.Size))
	r.hashes.Add(1)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return &observedSource{source: result, stamp: stamp, owner: r}, nil
}

type observedSource struct {
	source app.NFOReadSource
	stamp  domain.NFOStamp
	owner  *ObservedReader
}

func (*observedSource) String() string   { return "nfo observed source (data redacted)" }
func (*observedSource) GoString() string { return "nfo observed source (data redacted)" }
func (s *observedSource) Stamp() domain.NFOStamp {
	if s == nil {
		return domain.NFOStamp{}
	}
	return s.stamp
}
func (s *observedSource) Parse(ctx context.Context) (domain.NFOValidationSummary, error) {
	if err := summaryContext(ctx); err != nil {
		return domain.NFOValidationSummary{}, err
	}
	if s == nil || s.source == nil || s.owner == nil {
		return domain.NFOValidationSummary{}, domain.ErrNFOReaderUnavailable
	}
	s.owner.parses.Add(1)
	leave := s.owner.enter()
	defer leave()
	result, err := s.source.Parse(ctx)
	if ctx.Err() != nil {
		return domain.NFOValidationSummary{}, ctx.Err()
	}
	if err != nil {
		return domain.NFOValidationSummary{}, err
	}
	return result, nil
}
