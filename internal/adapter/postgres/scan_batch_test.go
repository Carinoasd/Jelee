package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type scanQueryCounter struct{ calls atomic.Int64 }

func (c *scanQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.calls.Add(1)
	return ctx
}
func (*scanQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestScanBatchBoundedDatabaseRoundTrips(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "batch-round-trips")
	l := f.claim(t, "batch-worker")
	d := f.directory(t, l)
	config := f.s.Pool.Config()
	counter := &scanQueryCounter{}
	config.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	f.s.Pool.Close()
	f.s.Pool = pool
	t.Cleanup(pool.Close)
	entries := make([]domain.InventoryEntry, domain.ScanBatchMaxEntries)
	for i := range entries {
		entries[i] = scanEntry(d, fmt.Sprintf("batch-%03d.mkv", i), int64(i+1))
	}
	check := func(batch domain.ScanBatch) {
		t.Helper()
		counter.calls.Store(0)
		if err := f.s.SaveScanBatch(f.ctx, l, d, batch); err != nil {
			t.Fatal(err)
		}
		if count := counter.calls.Load(); count > 20 {
			t.Errorf("128-entry batch requires %d database round trips; limit 20", count)
		} else {
			t.Logf("bounded batch database round trips: %d", count)
		}
	}
	check(domain.ScanBatch{Entries: entries})
	var firstID string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM job_inventory WHERE job_id=$1::uuid AND path=$2`, j.ID, entries[0].Path).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	entries[0].Size = 100
	check(domain.ScanBatch{Entries: entries})
	var afterID string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM job_inventory WHERE job_id=$1::uuid AND path=$2`, j.ID, entries[0].Path).Scan(&afterID); err != nil {
		t.Fatal(err)
	}
	if firstID != afterID {
		t.Fatal("batch replay replaced persistent entry identity")
	}
	got := f.get(t, j.ID)
	if got.Files != 128 || got.Bytes != 128*129/2+99 {
		t.Fatalf("replay counters files=%d bytes=%d", got.Files, got.Bytes)
	}
	dirs := make([]string, domain.ScanBatchMaxEntries)
	for i := range dirs {
		dirs[i] = fmt.Sprintf("folder-%03d", i)
	}
	check(domain.ScanBatch{Directories: dirs})
	check(domain.ScanBatch{Directories: dirs})
	var total int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT directory_total FROM jobs WHERE id=$1::uuid`, j.ID).Scan(&total); err != nil || total != 129 {
		t.Fatal("directory replay changed total", total, err)
	}
}

func TestScanBatchConflictRollsBackRows(t *testing.T) {
	for _, fileFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(fileFirst), func(t *testing.T) {
			f := newJobFixture(t)
			j := f.submit(t, "batch-conflict")
			l := f.claim(t, "batch-conflict-worker")
			d := f.directory(t, l)
			initial := domain.ScanBatch{Directories: []string{"occupied"}}
			conflict := domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "new.mkv", 7), scanEntry(d, "occupied", 9)}}
			if fileFirst {
				initial = domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "occupied", 3)}}
				conflict = domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "new.mkv", 7)}, Directories: []string{"new-dir", "occupied"}}
			}
			if err := f.s.SaveScanBatch(f.ctx, l, d, initial); err != nil {
				t.Fatal(err)
			}
			before := f.get(t, j.ID)
			if err := f.s.SaveScanBatch(f.ctx, l, d, conflict); !errors.Is(err, domain.ErrConflict) {
				t.Fatal("file/directory conflict accepted", err)
			}
			after := f.get(t, j.ID)
			if before.Files != after.Files || before.Bytes != after.Bytes {
				t.Fatal("conflict changed counters")
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM job_inventory WHERE job_id=$1::uuid AND path='new.mkv')+(SELECT count(*) FROM job_directories WHERE job_id=$1::uuid AND path='new-dir')`, j.ID).Scan(&count); err != nil || count != 0 {
				t.Fatal("conflict retained provisional rows", count, err)
			}
		})
	}
}
