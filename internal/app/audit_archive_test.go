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

// archivingPurger models the repository contract: rows go to the sink first
// and are deleted only if the sink succeeded.
type archivingPurger struct {
	fakeAuditPurger
	rows    []domain.AuditRecord
	deleted int
	calls   int
}

func (p *archivingPurger) PurgeAuditArchived(ctx context.Context, batch int, sink domain.AuditArchiveSink) (domain.AuditPurgeResult, error) {
	p.calls++
	n := min(batch, len(p.rows))
	if n == 0 {
		return domain.AuditPurgeResult{}, nil
	}
	receipt, err := sink.Archive(ctx, p.rows[:n])
	if err != nil {
		return domain.AuditPurgeResult{}, err
	}
	p.rows = p.rows[n:]
	p.deleted += n
	return domain.AuditPurgeResult{Audit: int64(n), ArchivedFiles: receipt.Files}, nil
}

type fakeArchiver struct {
	archived [][]domain.AuditRecord
	err      error
	pruneErr error
	pruned   int
}

func (f *fakeArchiver) Archive(_ context.Context, records []domain.AuditRecord) (domain.AuditArchiveReceipt, error) {
	if f.err != nil {
		return domain.AuditArchiveReceipt{}, f.err
	}
	f.archived = append(f.archived, records)
	return domain.AuditArchiveReceipt{Files: 1, Bytes: 10}, nil
}

func (f *fakeArchiver) Prune(context.Context, time.Time) (domain.AuditArchivePrune, error) {
	return domain.AuditArchivePrune{Files: f.pruned}, f.pruneErr
}

func rowsN(n int) []domain.AuditRecord {
	out := make([]domain.AuditRecord, n)
	for i := range out {
		out[i] = domain.AuditRecord{ID: int64(i + 1), Category: "audit"}
	}
	return out
}

func TestAuditJanitorArchivesBeforeDelete(t *testing.T) {
	repo := &archivingPurger{rows: rowsN(5)}
	archive := &fakeArchiver{pruned: 2}
	var logs bytes.Buffer
	j, err := NewAuditJanitor(repo, AuditJanitorOptions{Interval: time.Hour, Batch: 2, MaxBatches: 10, Archive: archive, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if !j.Archiving() {
		t.Fatal("archiving not reported")
	}
	j.runPass(context.Background())
	j.pruneArchive(context.Background())
	if repo.deleted != 5 || len(archive.archived) != 3 || j.ArchivedRows() != 5 || j.ArchiveFailures() != 0 || j.ArchivePrunedFiles() != 2 {
		t.Fatalf("deleted %d archived %d rows %d failures %d pruned %d", repo.deleted, len(archive.archived), j.ArchivedRows(), j.ArchiveFailures(), j.ArchivePrunedFiles())
	}
	if repo.calls != 3 || repo.fakeAuditPurger.batches != nil {
		t.Fatal("plain PurgeAudit used while archiving")
	}
	if !strings.Contains(logs.String(), "archive files") {
		t.Fatalf("prune not logged: %s", logs.String())
	}
}

func TestAuditJanitorArchiveFailureKeepsRows(t *testing.T) {
	repo := &archivingPurger{rows: rowsN(4)}
	archive := &fakeArchiver{err: errors.New("audit archive write: no space left on device")}
	var logs bytes.Buffer
	j, _ := NewAuditJanitor(repo, AuditJanitorOptions{Interval: time.Hour, Batch: 2, MaxBatches: 10, Archive: archive, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	j.runPass(context.Background())
	if repo.deleted != 0 || len(repo.rows) != 4 || j.ArchiveFailures() != 1 || j.ArchivedRows() != 0 || repo.calls != 1 {
		t.Fatalf("deleted %d left %d failures %d calls %d", repo.deleted, len(repo.rows), j.ArchiveFailures(), repo.calls)
	}
	if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), "no space left") || !strings.Contains(logs.String(), "stay in the database") {
		t.Fatalf("failure not logged: %s", logs.String())
	}
	// Recovery: the next pass archives and deletes the same rows.
	archive.err = nil
	j.runPass(context.Background())
	if repo.deleted != 4 || j.ArchivedRows() != 4 {
		t.Fatalf("no recovery: deleted %d", repo.deleted)
	}
}

func TestAuditJanitorPruneFailureCounts(t *testing.T) {
	repo := &archivingPurger{}
	archive := &fakeArchiver{pruneErr: errors.New("prune")}
	j, _ := NewAuditJanitor(repo, AuditJanitorOptions{Interval: time.Hour, Batch: 1, MaxBatches: 1, Archive: archive, Logger: slog.New(slog.DiscardHandler)})
	j.pruneArchive(context.Background())
	if j.ArchiveFailures() != 1 {
		t.Fatalf("failures %d", j.ArchiveFailures())
	}
	// A canceled context is shutdown, not a failure.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j.pruneArchive(ctx)
	if j.ArchiveFailures() != 1 {
		t.Fatalf("shutdown counted: %d", j.ArchiveFailures())
	}
	// Without an archive the prune step is a no-op.
	plain, _ := NewAuditJanitor(&fakeAuditPurger{}, AuditJanitorOptions{Interval: time.Hour, Batch: 1, MaxBatches: 1, Logger: slog.New(slog.DiscardHandler)})
	plain.pruneArchive(context.Background())
	if plain.Archiving() || plain.ArchiveFailures() != 0 {
		t.Fatal("plain janitor reports an archive")
	}
}

func TestAuditJanitorArchiveNeedsCapableRepository(t *testing.T) {
	_, err := NewAuditJanitor(&fakeAuditPurger{}, AuditJanitorOptions{Interval: time.Hour, Batch: 1, MaxBatches: 1, Archive: &fakeArchiver{}, Logger: slog.New(slog.DiscardHandler)})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("archive accepted without PurgeAuditArchived: %v", err)
	}
}
