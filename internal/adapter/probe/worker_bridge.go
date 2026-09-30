package probe

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

// WorkerBridge exposes the domain-only worker port over the concrete isolated
// adapter. Construction records trusted identity data; it never authorizes an
// executable from DB/HTTP fields or introduces an unsandboxed fallback.
type WorkerBridge struct {
	adapter Adapter
	digest  string
}

var _ app.MetadataProber = (*WorkerBridge)(nil)

func NewWorkerBridge(adapter *Adapter, identity domain.ProbeIdentity) (*WorkerBridge, error) {
	if adapter == nil || adapter.runner == nil {
		return nil, domain.ErrProbeRuntimeUnavailable
	}
	digest, err := domain.ProbeIdentityDigest(identity)
	if err != nil || adapter.identity != digest {
		return nil, domain.ErrProbeIdentityMismatch
	}
	// Copy the adapter value and digest so subsequent replacement of the caller's
	// adapter/identity values cannot silently change this bridge's registration.
	return &WorkerBridge{adapter: *adapter, digest: digest}, nil
}

func (b *WorkerBridge) IdentityDigest() string {
	if b == nil {
		return ""
	}
	return b.digest
}

// Inspect starts no process. The caller supplies its bounded context. As with
// the underlying safe opener, a blocked OS open/stat call is not a guaranteed
// interruptible operation; no promise of a filesystem snapshot is made.
func (b *WorkerBridge) Inspect(ctx context.Context, source domain.ProbeSource) (domain.ProbeStamp, error) {
	if err := b.check(ctx); err != nil {
		return domain.ProbeStamp{}, err
	}
	stamp, err := b.adapter.Inspect(ctx, source)
	if ctx.Err() != nil {
		return domain.ProbeStamp{}, ctx.Err()
	}
	if err != nil {
		return domain.ProbeStamp{}, workerError(err)
	}
	if domain.ValidateProbeStamp(stamp) != nil {
		return domain.ProbeStamp{}, domain.ErrProbeRuntimeUnavailable
	}
	return stamp, nil
}

// A per-process timeout is context.DeadlineExceeded while the caller's context
// can still be live. Workers must check their parent context first before
// classifying that error as a single-file failure. Cancellation is never cached.
func (b *WorkerBridge) Probe(ctx context.Context, source domain.ProbeSource) (domain.ProbeObservation, error) {
	if err := b.check(ctx); err != nil {
		return domain.ProbeObservation{}, err
	}
	observation, err := b.adapter.Probe(ctx, source)
	if ctx.Err() != nil {
		return domain.ProbeObservation{}, ctx.Err()
	}
	if err != nil {
		return domain.ProbeObservation{}, workerError(err)
	}
	result, err := workerObservation(observation, b.digest)
	if ctx.Err() != nil {
		return domain.ProbeObservation{}, ctx.Err()
	}
	return result, err
}

func (b *WorkerBridge) check(ctx context.Context) error {
	if ctx == nil {
		return domain.ErrProbeRuntimeUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if b == nil || b.adapter.runner == nil || b.digest == "" {
		return domain.ErrProbeRuntimeUnavailable
	}
	return nil
}

func workerObservation(observation Observation, digest string) (domain.ProbeObservation, error) {
	if observation.ToolIdentity != digest || digest == "" {
		return domain.ProbeObservation{}, domain.ErrProbeIdentityMismatch
	}
	stamp := domain.ProbeStamp{Size: observation.File.Size, ModifiedUnixNano: observation.File.ModifiedUnixNano, Fingerprint: observation.Fingerprint, FingerprintVersion: observation.FingerprintVersion}
	if domain.ValidateProbeStamp(stamp) != nil {
		return domain.ProbeObservation{}, domain.ErrProbeRuntimeUnavailable
	}
	if _, err := domain.MarshalProbeMetadata(observation.Metadata); err != nil {
		return domain.ProbeObservation{}, domain.ErrProbeMetadataInvalid
	}
	if size := observation.Metadata.Format.SizeBytes; size != nil && *size != stamp.Size {
		return domain.ProbeObservation{}, domain.ErrProbeSourceChanged
	}
	return domain.ProbeObservation{Metadata: observation.Metadata, Stamp: stamp, IdentityDigest: digest}, nil
}

func workerError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, process.ErrCancelled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, process.ErrTimeout):
		return context.DeadlineExceeded
	case errors.Is(err, ErrChanged):
		return domain.ErrProbeSourceChanged
	case errors.Is(err, ErrUnavailable), errors.Is(err, ErrInvalidInput):
		return domain.ErrProbeInputUnavailable
	case errors.Is(err, ErrMetadataInvalid):
		return domain.ErrProbeMetadataInvalid
	case errors.Is(err, ErrMetadataLimit):
		return domain.ErrProbeMetadataLimit
	case errors.Is(err, process.ErrOutputLimit):
		return domain.ErrProbeOutputLimit
	case errors.Is(err, process.ErrBusy):
		return domain.ErrProbeBusy
	case errors.Is(err, ErrFailed), errors.Is(err, process.ErrExit):
		return domain.ErrProbeFailed
	default:
		return domain.ErrProbeRuntimeUnavailable
	}
}
