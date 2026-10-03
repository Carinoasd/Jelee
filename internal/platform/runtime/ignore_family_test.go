package runtime

import (
	"context"
	"errors"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type familyEvaluatorFunc func(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error)

func (f familyEvaluatorFunc) Evaluate(c context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
	return f(c, b)
}
func healthyFamilyResult(b legacyignore.Batch) legacyignore.BatchResult {
	if b.Source == "" {
		return legacyignore.BatchResult{Decisions: []legacyignore.Decision{{Kind: legacyignore.BlankExclude}}}
	}
	return legacyignore.BatchResult{Decisions: []legacyignore.Decision{{Kind: legacyignore.RuleInclude, Line: 2}, {Kind: legacyignore.RuleExclude, Line: 1}}}
}
func TestFamilyIgnoreServiceDisabledAndStartupFailures(t *testing.T) {
	s, err := newFamilyIgnoreService(nil, false, func(context.Context) (preparedFamilyIgnore, error) {
		t.Fatal("disabled factory invoked")
		return preparedFamilyIgnore{}, nil
	})
	if err != nil || s.Available() {
		t.Fatal("disabled service available")
	}
	for _, mode := range []string{"factory", "missing-backend", "bad-health", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			closed := 0
			prepare := func(context.Context) (preparedFamilyIgnore, error) {
				p := preparedFamilyIgnore{evaluator: familyEvaluatorFunc(func(_ context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
					if mode == "bad-health" {
						return legacyignore.BatchResult{Decisions: []legacyignore.Decision{{}, {}}}, nil
					}
					return healthyFamilyResult(b), nil
				}), close: func() error {
					closed++
					if mode == "cleanup" {
						return errors.New("private-root-secret")
					}
					return nil
				}}
				if mode == "missing-backend" {
					p.evaluator = nil
				}
				if mode == "factory" || mode == "cleanup" {
					return p, process.ErrStart
				}
				return p, nil
			}
			s, err := newFamilyIgnoreService(context.Background(), true, prepare)
			if closed != 1 {
				t.Fatal("failed startup leaked temporary resources")
			}
			if mode == "cleanup" {
				if err == nil || strings.Contains(err.Error(), "private-root-secret") {
					t.Fatal("cleanup error lost or leaked")
				}
				return
			}
			if err != nil || s.Available() {
				t.Fatal("unhealthy service available", err)
			}
			if err = s.Close(); err != nil || closed != 1 {
				t.Fatal("cleanup repeated")
			}
		})
	}
}
func TestFamilyIgnoreServiceHelperFailureAndCancellation(t *testing.T) {
	for _, failure := range []error{process.ErrBusy, process.ErrStart, process.ErrTimeout, legacyignore.ErrResult} {
		t.Run(failure.Error(), func(t *testing.T) {
			var current error
			calls := 0
			s, err := newFamilyIgnoreService(context.Background(), true, func(context.Context) (preparedFamilyIgnore, error) {
				return preparedFamilyIgnore{evaluator: familyEvaluatorFunc(func(_ context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
					calls++
					if current == legacyignore.ErrResult {
						return legacyignore.BatchResult{}, nil
					}
					if current != nil {
						return legacyignore.BatchResult{}, current
					}
					return healthyFamilyResult(b), nil
				}), close: func() error { return nil }}, nil
			})
			if err != nil || !s.Available() {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err = s.Evaluate(ctx, legacyignore.Batch{}); err != context.Canceled || calls != 1 || !s.Available() {
				t.Fatal("cancelled request changed readiness")
			}
			current = failure
			if r, err := s.Evaluate(context.Background(), legacyignore.Batch{Paths: []string{"/a"}}); err != failure || len(r.Decisions) != 0 {
				t.Fatal("helper error published partial results")
			}
			if s.Available() != (failure == process.ErrBusy) {
				t.Fatal("wrong readiness after helper failure")
			}
			before := calls
			if failure != process.ErrBusy {
				if _, err = s.Evaluate(context.Background(), legacyignore.Batch{Paths: []string{"/a"}}); err != process.ErrIgnoreUnavailable || calls != before {
					t.Fatal("disabled backend invoked")
				}
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestFamilyIgnoreServiceCloseJoinsEvaluation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var closed atomic.Int32
	s, err := newFamilyIgnoreService(context.Background(), true, func(context.Context) (preparedFamilyIgnore, error) {
		return preparedFamilyIgnore{evaluator: familyEvaluatorFunc(func(_ context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
			if b.Source == "" {
				close(entered)
				<-release
			}
			return healthyFamilyResult(b), nil
		}), close: func() error { closed.Add(1); return nil }}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluated := make(chan error, 1)
	go func() {
		_, err := s.Evaluate(context.Background(), legacyignore.Batch{Paths: []string{"/a"}})
		evaluated <- err
	}()
	<-entered
	cleanup := make(chan error, 1)
	go func() { cleanup <- s.Close() }()
	deadline := time.After(time.Second)
	for s.Available() {
		select {
		case <-deadline:
			t.Fatal("close did not disable admission")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if closed.Load() != 0 {
		t.Fatal("cleanup raced active evaluation")
	}
	close(release)
	if err := <-evaluated; err != nil {
		t.Fatal(err)
	}
	if err := <-cleanup; err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 || s.Available() {
		t.Fatal("cleanup or readiness incorrect")
	}
	if err = s.Close(); err != nil || closed.Load() != 1 {
		t.Fatal("cleanup not idempotent")
	}
}
