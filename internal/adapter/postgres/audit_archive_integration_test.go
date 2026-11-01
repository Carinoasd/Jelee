package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/auditarchive"
)

// probeSink records what it receives and, while the purge transaction is
// still open, how many of those rows another connection still sees: archive
// first, delete after.
type probeSink struct {
	f        auditFixture
	records  []domain.AuditRecord
	seen     int
	err      error
	inner    domain.AuditArchiveSink
	callsNum int
}

func (p *probeSink) Archive(ctx context.Context, records []domain.AuditRecord) (domain.AuditArchiveReceipt, error) {
	p.callsNum++
	p.records = append(p.records, records...)
	for _, r := range records {
		var n int
		_ = p.f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE id=$1`, r.ID).Scan(&n)
		p.seen += n
	}
	if p.err != nil {
		return domain.AuditArchiveReceipt{}, p.err
	}
	if p.inner != nil {
		return p.inner.Archive(ctx, records)
	}
	return domain.AuditArchiveReceipt{Files: 1}, nil
}

func TestAuditIntegrationArchiveBeforePurge(t *testing.T) {
	f := newAuditFixture(t)
	day := 24 * time.Hour
	keepAudit := f.backdate(t, "user.updated", "audit", 300*day)
	expired := []int64{
		f.backdate(t, "user.updated", "audit", 400*day),
		f.backdate(t, "login.failed", "security", 500*day),
		f.backdate(t, "user.created", "audit", 380*day),
	}
	// Nothing expired in a fresh batch of zero: the sink is not called.
	dir := filepath.Join(t.TempDir(), "archive")
	archive, err := auditarchive.Open(auditarchive.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archive.Close() }()
	sink := &probeSink{f: f, inner: archive}
	result, err := f.s.PurgeAuditArchived(f.ctx, 2, sink)
	if err != nil || result.Audit+result.Security != 2 || result.ArchivedFiles < 1 {
		t.Fatalf("first batch: %+v %v", result, err)
	}
	// While the transaction was open the rows were still present and locked.
	if sink.seen != 2 || len(sink.records) != 2 || sink.records[0].ID != expired[0] || sink.records[1].ID != expired[1] {
		t.Fatalf("archive did not run before the delete: seen %d records %+v", sink.seen, sink.records)
	}
	if f.count(t, `id IN ($1,$2)`, expired[0], expired[1]) != 0 || f.count(t, `id=$1`, expired[2]) != 1 {
		t.Fatal("first batch deleted the wrong rows")
	}
	second, err := f.s.PurgeAuditArchived(f.ctx, 2, sink)
	if err != nil || second.Audit+second.Security != 1 || sink.records[2].ID != expired[2] {
		t.Fatalf("second batch: %+v %v", second, err)
	}
	calls := sink.callsNum
	if again, err := f.s.PurgeAuditArchived(f.ctx, 2, sink); err != nil || again.Audit+again.Security != 0 || sink.callsNum != calls {
		t.Fatalf("empty batch called the sink: %+v %v", again, err)
	}
	if f.count(t, `id=$1`, keepAudit) != 1 {
		t.Fatal("a row inside retention was removed")
	}
	// Every purged row is in the archive and every archive file verifies.
	results, err := auditarchive.VerifyDir(dir)
	if err != nil || len(results) == 0 {
		t.Fatalf("verify: %+v %v", results, err)
	}
	rows := 0
	for _, r := range results {
		if r.Err != "" {
			t.Fatalf("%s: %s", r.Name, r.Err)
		}
		rows += r.Rows
	}
	if rows != 3 {
		t.Fatalf("archived %d rows, want 3", rows)
	}
	// The purge event records the archive files, once per batch.
	if f.count(t, `event='audit.retention_purged' AND (after_state->>'archivedFiles')::int>=1`) != 2 {
		t.Fatal("purge events do not record the archive")
	}
	if entries, _ := os.ReadDir(dir); len(entries) < 2 {
		t.Fatalf("archive files: %v", entries)
	}
}

func TestAuditIntegrationArchiveFailureDeletesNothing(t *testing.T) {
	f := newAuditFixture(t)
	day := 24 * time.Hour
	ids := []int64{f.backdate(t, "user.updated", "audit", 400*day), f.backdate(t, "login.failed", "security", 500*day)}
	events := f.count(t, `event='audit.retention_purged'`)
	sink := &probeSink{f: f, err: errors.New("disk full")}
	if _, err := f.s.PurgeAuditArchived(f.ctx, 10, sink); err == nil || err.Error() != "disk full" {
		t.Fatalf("archive failure not surfaced: %v", err)
	}
	if f.count(t, `id IN ($1,$2)`, ids[0], ids[1]) != 2 || f.count(t, `event='audit.retention_purged'`) != events {
		t.Fatal("rows were deleted or a purge was recorded although the archive failed")
	}
	// The failed transaction released its row locks: the next attempt works.
	sink.err = nil
	if result, err := f.s.PurgeAuditArchived(f.ctx, 10, sink); err != nil || result.Audit != 1 || result.Security != 1 {
		t.Fatalf("retry: %+v %v", result, err)
	}
	if f.count(t, `id IN ($1,$2)`, ids[0], ids[1]) != 0 {
		t.Fatal("retry left expired rows")
	}
}

func TestAuditIntegrationArchiveNilSinkIsPlainPurge(t *testing.T) {
	f := newAuditFixture(t)
	id := f.backdate(t, "user.updated", "audit", 400*24*time.Hour)
	result, err := f.s.PurgeAuditArchived(f.ctx, 10, nil)
	if err != nil || result.Audit != 1 || result.ArchivedFiles != 0 || f.count(t, `id=$1`, id) != 0 {
		t.Fatalf("plain purge: %+v %v", result, err)
	}
}
