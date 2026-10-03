//go:build windows || linux

package nfo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

const stagePayloadReservation = 3 * domain.NFOMaxSourceBytes

func TestStagePayloadHeldAcrossCPUWaitAndSharedWriters(t *testing.T) {
	_, source, p, _ := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
	b, _ := resources.NewWithPayloadLimit(resources.Limits{CPU: 1, IO: 1, Total: 2, Queue: 1}, stagePayloadReservation)
	w, _ := NewWriterWithBudget(b)
	other, _ := NewWriterWithBudget(b)
	l, record := stagingLease()
	repo := &stagingRepository{read: func(_ context.Context, l domain.JobLease, seq int) (domain.NFOWriteTask, error) {
		return domain.NFOWriteTask{JobID: l.Job.ID, Sequence: seq, Preparation: p}, nil
	}}
	cpu, err := b.Acquire(context.Background(), app.WorkCPU)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.StageCommitFiles(ctx, source, l, record, repo) }()
	deadline := time.After(5 * time.Second)
	for b.Stats().Waiting != 1 {
		select {
		case <-deadline:
			t.Fatal("CPU wait not reached")
		case <-time.After(time.Millisecond):
		}
	}
	if used, _ := b.PayloadBytes(); used != stagePayloadReservation || repo.reads.Load() != 1 || b.Stats().IO != 0 {
		t.Fatal("payload released while waiting CPU")
	}
	if err := other.StageCommitFiles(context.Background(), source, l, record, repo); !errors.Is(err, domain.ErrResourceBusy) {
		t.Fatal("other writer bypassed shared byte limit", err)
	}
	if repo.reads.Load() != 1 {
		t.Fatal("busy bytes still fetched payload")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if used, _ := b.PayloadBytes(); used != 0 {
		t.Fatal("cancelled CPU waiter leaked bytes")
	}
	cpu()
	if err := other.StageCommitFiles(context.Background(), source, l, record, repo); err != nil {
		t.Fatal("capacity not reusable", err)
	}
	if used, _ := b.PayloadBytes(); used != 0 || b.Stats() != (resources.Stats{}) {
		t.Fatal("successful stage leaked reservation")
	}
}

type readyPayloadRepository struct {
	*stagingRepository
	entered, cleanup chan struct{}
}

func (r *readyPayloadRepository) SaveNFOWriteCommitFilesReady(ctx context.Context, _ domain.JobLease, _ int, _ string, ready domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error) {
	close(r.entered)
	<-ctx.Done()
	<-r.cleanup
	return domain.NFOWriteCommitFilesReady{}, ctx.Err()
}

func TestStagePayloadHeldThroughUnknownReadyCleanup(t *testing.T) {
	root, source, p, _ := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
	b, _ := resources.NewWithPayloadLimit(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0}, stagePayloadReservation)
	w, _ := NewWriterWithBudget(b)
	l, record := stagingLease()
	repo := &readyPayloadRepository{stagingRepository: &stagingRepository{read: func(_ context.Context, l domain.JobLease, seq int) (domain.NFOWriteTask, error) {
		return domain.NFOWriteTask{JobID: l.Job.ID, Sequence: seq, Preparation: p}, nil
	}}, entered: make(chan struct{}), cleanup: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.StageCommitFiles(ctx, source, l, record, repo) }()
	<-repo.entered
	cancel()
	select {
	case <-done:
		t.Fatal("owner returned before ready cleanup joined")
	case <-time.After(20 * time.Millisecond):
	}
	if used, _ := b.PayloadBytes(); used != stagePayloadReservation || b.Stats().IO != 1 {
		t.Fatal("live ready lost bytes or IO")
	}
	close(repo.cleanup)
	if err := <-done; !errors.Is(err, ErrReplace) {
		t.Fatal(err)
	}
	if used, _ := b.PayloadBytes(); used != 0 {
		t.Fatal("ready failure leaked bytes")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	retained := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".jelee-nfo-commit-") {
			retained++
		}
	}
	if retained != 5 {
		t.Fatal("unknown ready outcome destroyed evidence", retained)
	}
}

type classOnlyBudget struct{ app.WorkBudget }

func TestStagePayloadRejectsBeforeReadWithoutCapacity(t *testing.T) {
	_, source, _, _ := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
	l, record := stagingLease()
	repo := &stagingRepository{read: func(context.Context, domain.JobLease, int) (domain.NFOWriteTask, error) {
		t.Error("rejected payload fetched")
		return domain.NFOWriteTask{}, domain.ErrInvalid
	}}
	b, _ := resources.NewWithPayloadLimit(resources.Limits{CPU: 1, IO: 1, Total: 1}, stagePayloadReservation-1)
	w, _ := NewWriterWithBudget(b)
	if err := w.StageCommitFiles(context.Background(), source, l, record, repo); !errors.Is(err, domain.ErrResourceBusy) {
		t.Fatal("insufficient byte capacity", err)
	}
	w.budget = classOnlyBudget{b}
	if err := w.StageCommitFiles(context.Background(), source, l, record, repo); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("missing byte capability silently bypassed", err)
	}
	if repo.reads.Load() != 0 {
		t.Fatal("rejected payload loaded")
	}
}
