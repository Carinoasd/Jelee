package runtime

import (
	"context"
	"errors"
	"os"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/scratch"
)

type familyIgnoreEvaluator interface {
	Evaluate(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error)
}
type preparedFamilyIgnore struct {
	evaluator familyIgnoreEvaluator
	close     func() error
}

// This boundary sees helper errors, never filesystem lookup errors. A lost
// runtime disables admission and claims while already-running work fails closed.
type familyIgnoreService struct {
	mu         sync.RWMutex
	available  atomic.Bool
	backend    familyIgnoreEvaluator
	cleanup    func() error
	closeOnce  sync.Once
	closeError error
}

func newFamilyIgnoreService(ctx context.Context, enabled bool, prepare func(context.Context) (preparedFamilyIgnore, error)) (*familyIgnoreService, error) {
	s := &familyIgnoreService{}
	if !enabled {
		return s, nil
	}
	if ctx == nil || prepare == nil {
		return nil, process.ErrInvalid
	}
	p, err := prepare(ctx)
	s.backend, s.cleanup = p.evaluator, p.close
	if err != nil || p.evaluator == nil || p.close == nil {
		if e := s.Close(); e != nil {
			return nil, errors.New("ignore helper temporary cleanup failed")
		}
		return s, nil
	}
	s.available.Store(true)
	batch := legacyignore.Batch{Source: "*.mkv\n!keep.mkv", Paths: []string{"/keep.mkv", "/drop.mkv"}}
	result, err := s.Evaluate(ctx, batch)
	if err != nil || len(result.Decisions) != 2 || result.Decisions[0] != (legacyignore.Decision{Kind: legacyignore.RuleInclude, Line: 2}) || result.Decisions[1] != (legacyignore.Decision{Kind: legacyignore.RuleExclude, Line: 1}) {
		if e := s.Close(); e != nil {
			return nil, errors.New("ignore helper temporary cleanup failed")
		}
	}
	return s, nil
}
func prepareProductionFamilyIgnore(ctx context.Context) (preparedFamilyIgnore, error) {
	if ctx == nil || ctx.Err() != nil {
		return preparedFamilyIgnore{}, process.ErrCancelled
	}
	if goruntime.GOOS != "linux" && goruntime.GOOS != "windows" {
		return preparedFamilyIgnore{}, process.ErrIgnoreUnavailable
	}
	directory, err := scratch.MkdirOwned("", scratch.ServiceIgnore)
	if err != nil {
		return preparedFamilyIgnore{}, process.ErrStart
	}
	p := preparedFamilyIgnore{close: func() error { return os.RemoveAll(directory) }}
	p.evaluator, err = process.NewIgnoreRunner(directory, 2, 5*time.Second)
	return p, err
}
func (s *familyIgnoreService) Available() bool { return s != nil && s.available.Load() }
func (s *familyIgnoreService) Evaluate(ctx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
	if ctx == nil {
		return legacyignore.BatchResult{}, process.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return legacyignore.BatchResult{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.Available() {
		return legacyignore.BatchResult{}, process.ErrIgnoreUnavailable
	}
	result, err := s.backend.Evaluate(ctx, b)
	if err == nil {
		err = legacyignore.ValidateResult(ctx, b, result)
	}
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, process.ErrBusy) && !errors.Is(err, process.ErrInvalid) && !errors.Is(err, legacyignore.ErrBatch) {
			s.available.Store(false)
		}
		return legacyignore.BatchResult{}, err
	}
	return result, nil
}

// The lifetime joins workers before Close. The lock also prevents cleanup
// during an Evaluate call if Close is invoked by another lifecycle caller.
func (s *familyIgnoreService) Close() error {
	s.available.Store(false)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeOnce.Do(func() {
		if s.cleanup != nil {
			s.closeError = s.cleanup()
		}
	})
	return s.closeError
}
