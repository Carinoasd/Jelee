//go:build windows || linux

package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

type stagingRepository struct {
	read                  func(context.Context, domain.JobLease, int) (domain.NFOWriteTask, error)
	reads, plans, readies atomic.Int32
}

func (*stagingRepository) GetNFOWriteCommitFiles(_ context.Context, l domain.JobLease, seq int, token string) (domain.NFOWriteCommitFileEvidence, error) {
	return domain.NFOWriteCommitFileEvidence{Record: domain.NFOWriteCommitRecord{JobID: l.Job.ID, Owner: l.Owner, Generation: l.Generation, Sequence: seq, Token: token}}, nil
}

func (r *stagingRepository) GetNFOWriteTask(ctx context.Context, l domain.JobLease, seq int) (domain.NFOWriteTask, error) {
	r.reads.Add(1)
	return r.read(ctx, l, seq)
}
func (*stagingRepository) BeginNFOWriteCommit(context.Context, domain.JobLease, int) (domain.NFOWriteCommitRecord, error) {
	return domain.NFOWriteCommitRecord{}, domain.ErrInvalid
}
func (r *stagingRepository) SaveNFOWriteCommitFilePlan(_ context.Context, _ domain.JobLease, _ int, _ string, p domain.NFOWriteCommitFilePlan) (domain.NFOWriteCommitFilePlan, error) {
	r.plans.Add(1)
	return p, nil
}
func (r *stagingRepository) SaveNFOWriteCommitFilesReady(_ context.Context, _ domain.JobLease, _ int, _ string, p domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error) {
	r.readies.Add(1)
	return p, nil
}
func stagingLease() (domain.JobLease, domain.NFOWriteCommitRecord) {
	l := domain.JobLease{Job: domain.Job{ID: "a0000000-0000-0000-0000-000000000010"}, Owner: "stage-owner", Generation: 1}
	r := domain.NFOWriteCommitRecord{JobID: l.Job.ID, Owner: l.Owner, Generation: l.Generation, Sequence: 1, Token: "a0000000-0000-0000-0000-000000000011"}
	return l, r
}

func TestStageCommitFilesCoalescesBeforeRead(t *testing.T) {
	root, source, p, b := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
	w, _ := NewWriterWithBudget(b)
	l, record := stagingLease()
	gate := make(chan struct{})
	repo := &stagingRepository{read: func(ctx context.Context, l domain.JobLease, seq int) (domain.NFOWriteTask, error) {
		if stats := b.Stats(); stats.IO != 1 || stats.CPU != 0 || stats.Total != 1 {
			t.Error("read lacked exclusive I/O permit", stats)
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return domain.NFOWriteTask{}, ctx.Err()
		}
		return domain.NFOWriteTask{JobID: l.Job.ID, Sequence: seq, Preparation: p}, nil
	}}
	submitted := make(chan struct{}, 100)
	ops := nativeNFOWriteOperations()
	ops.submitted = func() { submitted <- struct{}{} }
	results := make(chan error, 100)
	for i := 0; i < 100; i++ {
		go func() { results <- w.stageCommitFiles(context.Background(), source, l, record, repo, ops) }()
	}
	for i := 0; i < 100; i++ {
		select {
		case <-submitted:
		case <-time.After(5 * time.Second):
			t.Fatal("call did not join")
		}
	}
	close(gate)
	for i := 0; i < 100; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if repo.reads.Load() != 1 || repo.plans.Load() != 1 || repo.readies.Load() != 1 {
		t.Fatal("duplicate payload or staging", repo.reads.Load(), repo.plans.Load(), repo.readies.Load())
	}
	data, err := os.ReadFile(filepath.Join(root, source.relative))
	if err != nil || string(data) != string(p.Original) {
		t.Fatal("target changed")
	}
	// Completion is not cached; existing retained artifacts require a resolver.
	if err := w.StageCommitFiles(context.Background(), source, l, record, repo); err == nil {
		t.Fatal("retry guessed committed outcome")
	}
	if repo.reads.Load() != 2 || b.Stats() != (resources.Stats{}) {
		t.Fatal("stale flight or permit leak")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.intents) != 0 {
		t.Fatal("completed source retained")
	}
}

func TestStageCommitFilesReadAdmission(t *testing.T) {
	root, source, _, b := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
	w, _ := NewWriterWithBudget(b)
	l, r := stagingLease()
	repo := &stagingRepository{read: func(context.Context, domain.JobLease, int) (domain.NFOWriteTask, error) {
		t.Error("busy read reached repository")
		return domain.NFOWriteTask{}, domain.ErrInvalid
	}}
	release, err := b.Acquire(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.StageCommitFiles(context.Background(), source, l, r, repo); !errors.Is(err, domain.ErrResourceBusy) {
		t.Fatal("read admission", err)
	}
	release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.StageCommitFiles(ctx, source, l, r, repo); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || repo.reads.Load() != 0 || b.Stats() != (resources.Stats{}) {
		t.Fatal("rejected read had side effects")
	}
}

func TestStageCommitFilesCancellationJoinsOwner(t *testing.T) {
	_, source, _, b := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
	w, _ := NewWriterWithBudget(b)
	l, r := stagingLease()
	entered, clean := make(chan struct{}), make(chan struct{})
	repo := &stagingRepository{read: func(ctx context.Context, _ domain.JobLease, _ int) (domain.NFOWriteTask, error) {
		close(entered)
		<-ctx.Done()
		<-clean
		return domain.NFOWriteTask{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner := make(chan error, 1)
	go func() { owner <- w.StageCommitFiles(ctx, source, l, r, repo) }()
	<-entered
	waitCtx, waitCancel := context.WithCancel(context.Background())
	submitted := make(chan struct{}, 1)
	ops := nativeNFOWriteOperations()
	ops.submitted = func() { submitted <- struct{}{} }
	waiter := make(chan error, 1)
	go func() { waiter <- w.stageCommitFiles(waitCtx, source, l, r, repo, ops) }()
	<-submitted
	waitCancel()
	if err := <-waiter; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if b.Stats().IO != 1 || repo.reads.Load() != 1 {
		t.Fatal("waiter released owner")
	}
	cancel()
	select {
	case <-owner:
		t.Fatal("owner returned before read joined")
	case <-time.After(20 * time.Millisecond):
	}
	close(clean)
	if err := <-owner; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if b.Stats() != (resources.Stats{}) {
		t.Fatal("read permit leaked")
	}
}

func TestStageCommitFilesDoesNotShareOtherIntent(t *testing.T) {
	for _, reason := range []string{"job", "owner", "generation", "sequence", "token", "repository", "root", "entity", "stamp"} {
		t.Run(reason, func(t *testing.T) {
			_, source, _, _ := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
			b, _ := resources.New(resources.Limits{CPU: 2, IO: 2, Total: 2, Queue: 0})
			w, _ := NewWriterWithBudget(b)
			l, r := stagingLease()
			otherLease, otherRecord := l, r
			gate, entered := make(chan struct{}), make(chan struct{}, 2)
			read := func(ctx context.Context, _ domain.JobLease, _ int) (domain.NFOWriteTask, error) {
				entered <- struct{}{}
				select {
				case <-gate:
				case <-ctx.Done():
				}
				return domain.NFOWriteTask{}, domain.ErrForbidden
			}
			repo := &stagingRepository{read: read}
			otherRepo := repo
			otherSource := *source
			switch reason {
			case "job":
				otherLease.Job.ID = "a0000000-0000-0000-0000-000000000012"
				otherRecord.JobID = otherLease.Job.ID
			case "owner":
				otherLease.Owner = "other-owner"
				otherRecord.Owner = otherLease.Owner
			case "generation":
				otherLease.Generation++
				otherRecord.Generation++
			case "sequence":
				otherRecord.Sequence++
			case "token":
				otherRecord.Token = "a0000000-0000-0000-0000-000000000012"
			case "repository":
				otherRepo = &stagingRepository{read: read}
			case "root":
				otherSource.rootPath += "-other"
			case "entity":
				_, different, _, _ := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
				otherSource.fileInfo = different.fileInfo
			case "stamp":
				otherSource.stamp.ModifiedUnixNano++
			}
			results := make(chan error, 2)
			go func() { results <- w.StageCommitFiles(context.Background(), source, l, r, repo) }()
			<-entered
			go func() {
				results <- w.StageCommitFiles(context.Background(), &otherSource, otherLease, otherRecord, otherRepo)
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				close(gate)
				t.Fatal("distinct intent joined")
			}
			close(gate)
			for i := 0; i < 2; i++ {
				if err := <-results; !errors.Is(err, domain.ErrForbidden) {
					t.Fatal(err)
				}
			}
		})
	}
}
