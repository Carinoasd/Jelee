package runtime

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type nfoMaintenance interface {
	EnsureNFOCachePolicy(context.Context, domain.NFOCachePolicy) error
	SweepNFOCache(context.Context, int) (domain.NFOSweepResult, error)
}

// NFO parsing is local Go code and remains independent of probe tool health.
// Per-library off and the durable request opt-in control actual execution.
type nfoService struct {
	backend     *nfo.ObservedReader
	identity    *domain.NFOIdentity
	repository  nfoMaintenance
	logger      *slog.Logger
	unavailable atomic.Bool
	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
}

func newNFOService(ctx context.Context, repository nfoMaintenance, reader app.NFOReader) (*nfoService, error) {
	if ctx == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := &nfoService{}
	if reader == nil {
		return s, nil
	}
	observed, err := nfo.NewObservedReader(reader)
	if err != nil {
		return s, nil
	}
	if err := repository.EnsureNFOCachePolicy(ctx, domain.DefaultNFOCachePolicy()); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("nfo cache policy is unavailable or differs from this instance")
	}
	identity := observed.Identity()
	s.identity, s.backend, s.repository = &identity, observed, repository
	return s, nil
}

func (s *nfoService) Available() bool { return s != nil && s.backend != nil && !s.unavailable.Load() }
func (s *nfoService) Disable() {
	if s != nil {
		s.unavailable.Store(true)
	}
}
func (s *nfoService) Identity() domain.NFOIdentity {
	if s == nil || s.backend == nil {
		return domain.NFOIdentity{}
	}
	return s.backend.Identity()
}
func (s *nfoService) Read(ctx context.Context, source domain.NFOSource) (app.NFOReadSource, error) {
	if ctx == nil {
		return nil, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.Available() {
		return nil, domain.ErrNFOReaderUnavailable
	}
	return s.backend.Read(ctx, source)
}
func (s *nfoService) Stats() nfo.ReaderStats {
	if s == nil || s.backend == nil {
		return nfo.ReaderStats{}
	}
	return s.backend.Stats()
}

func (s *nfoService) startMaintenance(ctx context.Context) {
	if s == nil || s.backend == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		return
	}
	life, cancel := context.WithCancel(ctx)
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-life.Done():
				return
			case <-ticker.C:
				s.sweep(life)
			}
		}
	}()
}
func (s *nfoService) sweep(ctx context.Context) {
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := s.repository.SweepNFOCache(dbCtx, domain.NFOSweepMax); err != nil && ctx.Err() == nil && s.logger != nil {
		s.logger.Warn("nfo cache maintenance failed", "component", "nfo", "code", "nfo_maintenance_failed")
	}
}
func (s *nfoService) cancelMaintenance() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (s *nfoService) stopMaintenance(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.cancelMaintenance()
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
