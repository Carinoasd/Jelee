package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type fakeAuditPurger struct {
	backlog  domain.AuditPurgeResult
	batches  []int
	failAt   int
	canceled bool
}

func (f *fakeAuditPurger) PurgeAudit(ctx context.Context, batch int) (domain.AuditPurgeResult, error) {
	f.batches = append(f.batches, batch)
	if f.failAt > 0 && len(f.batches) == f.failAt {
		return domain.AuditPurgeResult{}, domain.ErrDatabase
	}
	if _, ok := ctx.Deadline(); !ok {
		f.canceled = true
	}
	var got domain.AuditPurgeResult
	got.Audit = min(f.backlog.Audit, int64(batch))
	got.Security = min(f.backlog.Security, int64(batch)-got.Audit)
	f.backlog.Audit -= got.Audit
	f.backlog.Security -= got.Security
	return got, nil
}

func TestAuditJanitorOptions(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	repo := &fakeAuditPurger{}
	for name, o := range map[string]AuditJanitorOptions{
		"no interval":    {Batch: 1, MaxBatches: 1, Logger: logger},
		"zero batch":     {Interval: time.Second, MaxBatches: 1, Logger: logger},
		"huge batch":     {Interval: time.Second, Batch: AuditPurgeBatchMax + 1, MaxBatches: 1, Logger: logger},
		"no batches":     {Interval: time.Second, Batch: 1, Logger: logger},
		"too many":       {Interval: time.Second, Batch: 1, MaxBatches: AuditPurgeMaxBatchesMax + 1, Logger: logger},
		"no logger":      {Interval: time.Second, Batch: 1, MaxBatches: 1},
		"negative limit": {Interval: time.Second, Batch: 1, MaxBatches: 1, Timeout: -1, Logger: logger},
	} {
		if _, err := NewAuditJanitor(repo, o); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := NewAuditJanitor(nil, AuditJanitorOptions{Interval: time.Second, Batch: 1, MaxBatches: 1, Logger: logger}); !errors.Is(err, domain.ErrInvalid) {
		t.Error("nil repository accepted")
	}
}

func TestAuditJanitorPassIsBounded(t *testing.T) {
	var logs bytes.Buffer
	repo := &fakeAuditPurger{backlog: domain.AuditPurgeResult{Audit: 7, Security: 4}}
	j, err := NewAuditJanitor(repo, AuditJanitorOptions{Interval: time.Hour, Batch: 3, MaxBatches: 2, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	// Two full batches: the pass stops at its limit with rows left.
	j.runPass(context.Background())
	if len(repo.batches) != 2 || repo.backlog.Audit+repo.backlog.Security != 5 || repo.canceled {
		t.Fatalf("first pass: %v %+v", repo.batches, repo.backlog)
	}
	if !strings.Contains(logs.String(), "batch limit") || !strings.Contains(logs.String(), `"count":6`) {
		t.Fatalf("limit not logged: %s", logs.String())
	}
	logs.Reset()
	// The next pass drains the rest and ends on a short batch.
	result, err := j.Pass(context.Background())
	if err != nil || result.Limited || result.Batches != 2 || result.Audit+result.Security != 5 || repo.backlog != (domain.AuditPurgeResult{}) {
		t.Fatalf("second pass: %+v %v", result, err)
	}
	// Nothing to do: one batch, nothing logged.
	j.runPass(context.Background())
	if logs.Len() != 0 {
		t.Fatalf("idle pass logged: %s", logs.String())
	}
	// A failing batch ends the pass and is logged as a warning.
	repo.backlog = domain.AuditPurgeResult{Audit: 10}
	repo.batches, repo.failAt = nil, 2
	j.runPass(context.Background())
	if len(repo.batches) != 2 || !strings.Contains(logs.String(), "purge failed") || !strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Fatalf("failure: %v %s", repo.batches, logs.String())
	}
}

type signalAuditPurger struct{ called chan struct{} }

func (s signalAuditPurger) PurgeAudit(context.Context, int) (domain.AuditPurgeResult, error) {
	select {
	case s.called <- struct{}{}:
	default:
	}
	return domain.AuditPurgeResult{}, nil
}

func TestAuditJanitorRunStopsWithContext(t *testing.T) {
	repo := signalAuditPurger{called: make(chan struct{}, 1)}
	j, err := NewAuditJanitor(repo, AuditJanitorOptions{Interval: time.Millisecond, Batch: 1, MaxBatches: 1, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		j.Run(ctx)
	}()
	select {
	case <-repo.called:
	case <-time.After(5 * time.Second):
		t.Fatal("no pass ran")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
