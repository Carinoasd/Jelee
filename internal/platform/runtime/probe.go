package runtime

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/mkvruntime"
	"github.com/MoYuanCN/Jelee/internal/platform/proberuntime"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/MoYuanCN/Jelee/internal/platform/scratch"
)

type probeMaintenance interface {
	EnsureProbePolicy(context.Context, domain.ProbeCachePolicy) error
	SweepProbeCache(context.Context, int) (domain.ProbeSweepResult, error)
}

type preparedProbe struct {
	prober   app.MetadataProber
	identity domain.ProbeIdentity
	close    func() error
	stats    func() process.Stats
}

type probeService struct {
	mu         sync.RWMutex
	capability domain.ProbeCapability
	backend    app.MetadataProber
	identity   *domain.ProbeIdentity
	repository probeMaintenance
	logger     *slog.Logger
	cleanup    func() error
	closeOnce  sync.Once
	closeError error
	cancel     context.CancelFunc
	done       chan struct{}
	// These are private invocation counters for diagnostics and integration
	// evidence. Probe calls include source failures before a child can start.
	probeCalls   atomic.Uint64
	inspectCalls atomic.Uint64
	processStats func() process.Stats
}

// Disabled configuration returns before calling a factory or touching storage.
// A missing/unhealthy tool disables only this capability. Database policy
// failure is a startup error because an instance must not change shared limits.
func newProbeService(ctx context.Context, enabled bool, repository probeMaintenance, prepare func(context.Context) (preparedProbe, string, error)) (*probeService, error) {
	p := &probeService{capability: domain.ProbeCapability{State: "disabled", Reason: "configuration_disabled"}}
	if !enabled {
		return p, nil
	}
	if ctx == nil || repository == nil || prepare == nil {
		return nil, domain.ErrInvalid
	}
	p.capability = domain.ProbeCapability{Enabled: true, State: "unavailable", Reason: "runtime_unavailable"}
	prepared, reason, err := prepare(ctx)
	if err != nil {
		if prepared.close != nil && prepared.close() != nil {
			reason = "cleanup_failed"
		}
		switch reason {
		case "platform_unsupported", "temporary_unavailable", "cleanup_failed", "health_check_failed":
			p.capability.Reason = reason
		}
		return p, nil
	}
	digest, identityErr := domain.ProbeIdentityDigest(prepared.identity)
	if identityErr != nil || prepared.prober == nil || prepared.close == nil || prepared.prober.IdentityDigest() != digest {
		if prepared.close != nil && prepared.close() != nil {
			p.capability.Reason = "cleanup_failed"
		}
		return p, nil
	}
	if err := repository.EnsureProbePolicy(ctx, domain.DefaultProbeCachePolicy()); err != nil {
		if prepared.close() != nil {
			return nil, errors.New("probe cache policy and temporary cleanup are unavailable")
		}
		return nil, errors.New("probe cache policy is unavailable or differs from this instance")
	}
	value := prepared.identity
	p.identity, p.backend, p.repository, p.cleanup = &value, prepared.prober, repository, prepared.close
	p.processStats = prepared.stats
	p.capability.Available, p.capability.State, p.capability.Reason = true, "available", "isolated_helper_verified"
	return p, nil
}

func prepareProductionProbe(ctx context.Context) (preparedProbe, string, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return preparedProbe{}, "platform_unsupported", domain.ErrProbeRuntimeUnavailable
	}
	identity, err := proberuntime.Identity()
	if err != nil {
		return preparedProbe{}, "runtime_unavailable", domain.ErrProbeRuntimeUnavailable
	}
	// Diagnose executes the protected helper against deliberate invalid health
	// bytes. Health invocations are separate from metadata worker invocations.
	if health := proberuntime.Diagnose(ctx); health.Capability != "available" {
		return preparedProbe{}, "health_check_failed", domain.ErrProbeRuntimeUnavailable
	}
	directory, err := scratch.MkdirOwned("", scratch.ServiceProbe)
	if err != nil {
		return preparedProbe{}, "temporary_unavailable", domain.ErrProbeRuntimeUnavailable
	}
	prepared := preparedProbe{identity: identity, close: func() error { return os.RemoveAll(directory) }}
	runner, err := proberuntime.New(ctx, directory)
	if err != nil {
		return prepared, "runtime_unavailable", domain.ErrProbeRuntimeUnavailable
	}
	// The optional MediaInfo supplement (G19.1, E4) joins only when its
	// shipped files verify; its presence is part of the identity.
	supplement := prepareSupplement(ctx, directory, &identity)
	prepared.identity = identity
	digest, err := domain.ProbeIdentityDigest(identity)
	if err != nil {
		return prepared, "runtime_unavailable", domain.ErrProbeRuntimeUnavailable
	}
	var adapter *probe.Adapter
	if supplement != nil {
		adapter, err = probe.NewAdapterWithSupplement(runner, supplement, digest)
	} else {
		adapter, err = probe.NewAdapter(runner, digest)
	}
	if err != nil {
		return prepared, "runtime_unavailable", domain.ErrProbeRuntimeUnavailable
	}
	prepared.prober, err = probe.NewWorkerBridge(adapter, identity)
	prepared.stats = runner.Stats
	return prepared, "runtime_unavailable", err
}

func prepareSupplement(ctx context.Context, directory string, identity *domain.ProbeIdentity) *process.IsolatedToolRunner {
	closure, arguments, err := mkvruntime.SupplementIdentity()
	if err != nil {
		return nil
	}
	supplement, err := mkvruntime.New(ctx, sandbox.ToolMediaInfo, directory)
	if err != nil {
		return nil
	}
	extended, err := proberuntime.WithSupplement(*identity, closure, arguments)
	if err != nil {
		return nil
	}
	*identity = extended
	return supplement
}

func (p *probeService) Capability() domain.ProbeCapability {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.capability
}
func (p *probeService) Available() bool { return p.Capability().Available }
func (p *probeService) Disable() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.capability.Available, p.capability.State, p.capability.Reason = false, "unavailable", "runtime_unavailable"
}
func (p *probeService) IdentityDigest() string {
	if p.backend == nil {
		return ""
	}
	return p.backend.IdentityDigest()
}
func (p *probeService) Inspect(ctx context.Context, source domain.ProbeSource) (domain.ProbeStamp, error) {
	if !p.Available() {
		return domain.ProbeStamp{}, domain.ErrProbeRuntimeUnavailable
	}
	p.inspectCalls.Add(1)
	return p.backend.Inspect(ctx, source)
}
func (p *probeService) Probe(ctx context.Context, source domain.ProbeSource) (domain.ProbeObservation, error) {
	if !p.Available() {
		return domain.ProbeObservation{}, domain.ErrProbeRuntimeUnavailable
	}
	p.probeCalls.Add(1)
	return p.backend.Probe(ctx, source)
}

func (p *probeService) startMaintenance(ctx context.Context) {
	if p.backend == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done != nil {
		return
	}
	life, cancel := context.WithCancel(ctx)
	p.cancel, p.done = cancel, make(chan struct{})
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-life.Done():
				return
			case <-ticker.C:
				p.sweep(life)
			}
		}
	}()
}
func (p *probeService) sweep(ctx context.Context) {
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := p.repository.SweepProbeCache(dbCtx, domain.ProbeSweepMax); err != nil && ctx.Err() == nil && p.logger != nil {
		p.logger.Warn("probe cache maintenance failed", "component", "probe", "code", "probe_maintenance_failed")
	}
}
func (p *probeService) stopMaintenance(ctx context.Context) error {
	p.mu.RLock()
	cancel, done := p.cancel, p.done
	p.mu.RUnlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *probeService) Close() error {
	p.closeOnce.Do(func() {
		if p.cleanup != nil {
			p.closeError = p.cleanup()
		}
	})
	return p.closeError
}

type probeWorker struct {
	worker serviceWorker
	probe  *probeService
	nfo    *nfoService
}

func (w *probeWorker) Start(ctx context.Context) error {
	if err := w.worker.Start(ctx); err != nil {
		return err
	}
	w.probe.startMaintenance(ctx)
	w.nfo.startMaintenance(ctx)
	return nil
}
func (w *probeWorker) Stop(ctx context.Context) error {
	// Cancel the maintenance query before joining either participant.
	w.probe.mu.RLock()
	cancel := w.probe.cancel
	w.probe.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	w.nfo.cancelMaintenance()
	if err := w.worker.Stop(ctx); err != nil {
		return err
	}
	return errors.Join(w.probe.stopMaintenance(ctx), w.nfo.stopMaintenance(ctx))
}

func (w *probeWorker) NotifyJobCancellation(id string) {
	if notifier, ok := w.worker.(app.JobCancellationNotifier); ok {
		notifier.NotifyJobCancellation(id)
	}
}
