package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// fakeWatchStatsRepo hands out pending session batches and records calls.
type fakeWatchStatsRepo struct {
	mu        sync.Mutex
	pending   []domain.WatchStatsSession
	conflict  bool
	batches   int
	outcomes  []domain.WatchStatsOutcome
	queries   []domain.WatchStatsQuery
	exports   []domain.WatchStatsExportQuery
	exportErr error
	streamed  int
	runs      chan struct{}
	purged    []time.Time
}

func (r *fakeWatchStatsRepo) AggregateWatchStats(_ context.Context, limit int, compute func(domain.WatchStatsBatch) (domain.WatchStatsOutcome, error)) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches++
	if r.runs != nil {
		select {
		case r.runs <- struct{}{}:
		default:
		}
	}
	if r.conflict {
		return 0, domain.ErrConflict
	}
	n := min(limit, len(r.pending))
	batch := r.pending[:n]
	r.pending = r.pending[n:]
	if n == 0 {
		return 0, nil
	}
	out, err := compute(domain.WatchStatsBatch{Sessions: batch})
	if err != nil {
		return 0, err
	}
	r.outcomes = append(r.outcomes, out)
	return n, nil
}

func (r *fakeWatchStatsRepo) PurgeWatchStats(_ context.Context, before time.Time, _ int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purged = append(r.purged, before)
	return 0, nil
}

func (r *fakeWatchStatsRepo) PendingWatchStats(context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.pending)), nil
}

func (r *fakeWatchStatsRepo) WatchStatsReport(_ context.Context, _ domain.Actor, q domain.WatchStatsQuery) (domain.WatchStatsReport, error) {
	r.queries = append(r.queries, q)
	return domain.WatchStatsReport{From: q.From.Format(time.DateOnly), To: q.To.Format(time.DateOnly), Period: q.Period}, nil
}

func (r *fakeWatchStatsRepo) AuditWatchStatsExport(_ context.Context, _ domain.Actor, q domain.WatchStatsExportQuery) (int, error) {
	r.exports = append(r.exports, q)
	return 2, r.exportErr
}

func (r *fakeWatchStatsRepo) StreamWatchStatsExport(_ context.Context, _ domain.Actor, _ domain.WatchStatsExportQuery, write func(domain.WatchStatsExportRow) error) error {
	for range 2 {
		r.streamed++
		if err := write(domain.WatchStatsExportRow{}); err != nil {
			return err
		}
	}
	return nil
}

func pendingSession(i int, at time.Time) domain.WatchStatsSession {
	s := domain.WatchStatsSession{ID: progressItem(900 + i), UserID: progressActor(1).UserID, ItemID: progressItem(1), LibraryID: progressItem(500),
		StartedAt: at, EndedAt: at.Add(10 * time.Minute)}
	for m := 0; m <= 10; m++ {
		s.Samples = append(s.Samples, domain.PlaybackSample{Seq: m, At: at.Add(time.Duration(m) * time.Minute), Kind: domain.WatchSampleProgress, PositionTicks: ticksOf(time.Duration(m) * time.Minute)})
	}
	return s
}

func TestWatchStatsAggregateRunsBatchesUntilDrained(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	repo := &fakeWatchStatsRepo{}
	for i := range 25 {
		repo.pending = append(repo.pending, pendingSession(i, at.Add(time.Duration(i)*time.Hour)))
	}
	w, err := NewWatchStats(repo, WatchStatsOptions{Batch: 10, MaxBatches: 2})
	if err != nil {
		t.Fatal(err)
	}
	// MaxBatches bounds one run; the next run continues.
	if n, err := w.Aggregate(context.Background()); err != nil || n != 20 || repo.batches != 2 {
		t.Fatalf("first run n=%d batches=%d %v", n, repo.batches, err)
	}
	if n, err := w.Aggregate(context.Background()); err != nil || n != 5 || repo.batches != 3 {
		t.Fatalf("second run n=%d batches=%d %v", n, repo.batches, err)
	}
	if c := w.Counters(); c.Aggregated != 25 || c.Runs != 2 {
		t.Fatalf("counters %+v", c)
	}
	var effective int64
	for _, out := range repo.outcomes {
		for _, d := range out.Daily {
			effective += d.EffectiveMillis
		}
	}
	if effective != 25*600_000 {
		t.Fatalf("effective %d", effective)
	}
	// A conflict ends the run quietly; the next run retries.
	repo.conflict = true
	if n, err := w.Aggregate(context.Background()); err != nil || n != 0 || w.Counters().Conflicts != 1 {
		t.Fatalf("conflict n=%d %v", n, err)
	}
}

// Session ends wake the aggregator after the wake delay, well before the
// interval.
func TestWatchStatsRunWakesOnSessionEnd(t *testing.T) {
	repo := &fakeWatchStatsRepo{runs: make(chan struct{}, 1)}
	w, err := NewWatchStats(repo, WatchStatsOptions{Interval: time.Hour, WakeDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	p, _, clock := newTestProgress(t, ProgressOptions{OnEnded: w.Wake})
	actor := progressActor(1)
	if err := p.Report(ctx, actor, report(domain.PlaybackReportStart, "end", progressItem(1), 0)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if err := p.Report(ctx, actor, report(domain.PlaybackReportStop, "end", "", time.Minute)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repo.runs:
	case <-time.After(5 * time.Second):
		t.Fatal("a stop did not wake the aggregator")
	}
	cancel()
	<-done
}

func TestWatchStatsReportAndExportRequests(t *testing.T) {
	taipei, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	// 20:00 UTC on 3 October is already 4 October in Taipei.
	clock := &fakeClock{t: time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC)}
	repo := &fakeWatchStatsRepo{}
	w, err := NewWatchStats(repo, WatchStatsOptions{Clock: clock, Location: taipei, SundayWeeks: true, ExportMaxRows: 1000})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	actor := progressActor(1)
	report, err := w.Report(ctx, actor, domain.WatchStatsRequest{SubjectID: actor.UserID, Period: "week"})
	if err != nil || report.To != "2026-10-04" || report.From != "2026-09-05" || report.TimeZone != "Asia/Taipei" || report.WeekStart != "sunday" ||
		repo.queries[0].WeekStart != time.Sunday || repo.queries[0].SubjectID != actor.UserID {
		t.Fatalf("report %+v %v", report, err)
	}
	if _, err = w.Report(ctx, actor, domain.WatchStatsRequest{Period: "fortnight"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("bad period accepted", err)
	}
	if _, err = w.Report(ctx, domain.Actor{}, domain.WatchStatsRequest{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous report", err)
	}

	var begun, written int
	begin := func(rows int) error { begun = rows; return nil }
	write := func(domain.WatchStatsExportRow) error { written++; return nil }
	if err = w.Export(ctx, actor, WatchStatsExportRequest{From: "2026-01-01", To: "2026-10-04"}, begin, write); err != nil || begun != 2 || written != 2 {
		t.Fatalf("export begun=%d written=%d %v", begun, written, err)
	}
	if q := repo.exports[0]; q.Format != domain.WatchStatsExportCSV || q.Limit != 1000 || q.From.Format(time.DateOnly) != "2026-01-01" {
		t.Fatalf("export query %+v", q)
	}
	for name, r := range map[string]WatchStatsExportRequest{
		"over the bound": {Limit: 1001},
		"negative":       {Limit: -1},
		"format":         {Format: "xml"},
		"range":          {From: "2000-01-01"},
	} {
		if err = w.Export(ctx, actor, r, begin, write); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	if err = w.Export(ctx, actor, WatchStatsExportRequest{Limit: 10, Format: "ndjson"}, begin, write); err != nil || repo.exports[len(repo.exports)-1].Limit != 10 {
		t.Fatal("lower limit", err)
	}
	// A refused export never begins.
	repo.exportErr, begun = domain.ErrWatchStatsExportLimit, -1
	if err = w.Export(ctx, actor, WatchStatsExportRequest{}, begin, write); !errors.Is(err, domain.ErrWatchStatsExportLimit) || begun != -1 {
		t.Fatalf("refused export begun=%d %v", begun, err)
	}
	if _, err = NewWatchStats(repo, WatchStatsOptions{Batch: 1001}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("oversized batch accepted")
	}
}

// Retention cuts at local midnight of the reporting zone, at most hourly.
func TestWatchStatsRetentionPurge(t *testing.T) {
	taipei, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC)} // 4 October in Taipei
	repo := &fakeWatchStatsRepo{}
	w, err := NewWatchStats(repo, WatchStatsOptions{Clock: clock, Location: taipei, Retention: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = w.Purge(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(repo.purged) != 1 || repo.purged[0].Format(time.DateOnly) != "2026-09-04" {
		t.Fatalf("purges %v", repo.purged)
	}
	clock.Advance(time.Hour)
	if err = w.Purge(context.Background()); err != nil || len(repo.purged) != 2 {
		t.Fatal("hourly purge", repo.purged, err)
	}
	keep, _ := NewWatchStats(repo, WatchStatsOptions{})
	if err = keep.Purge(context.Background()); err != nil || len(repo.purged) != 2 {
		t.Fatal("purged without retention")
	}
}
