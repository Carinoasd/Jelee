package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeProgressRepo keeps sessions in memory and counts every write call.
type fakeProgressRepo struct {
	mu        sync.Mutex
	sessions  map[string]*domain.PlaybackSessionRecord // by user+key
	states    map[string]domain.PlaybackState          // by session id
	starts    int
	flushes   [][]domain.PlaybackFlush
	failFlush error
	stale     []domain.PlaybackSessionRecord
	purged    []time.Time
	cleared   []string
	userData  map[string]domain.UserItemData
	next      int
}

func newFakeProgressRepo() *fakeProgressRepo {
	return &fakeProgressRepo{sessions: map[string]*domain.PlaybackSessionRecord{}, states: map[string]domain.PlaybackState{}, userData: map[string]domain.UserItemData{}}
}

func (r *fakeProgressRepo) StartPlayback(_ context.Context, s domain.PlaybackStart) (domain.PlaybackSessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	key := s.Actor.UserID + "/" + s.PlayKey
	if existing := r.sessions[key]; existing != nil {
		state := r.states[existing.ID]
		if existing.ItemID != s.ItemID || state == domain.PlaybackStopped || state == domain.PlaybackFailed {
			return domain.PlaybackSessionRecord{}, domain.ErrConflict
		}
		r.states[existing.ID] = domain.PlaybackActive
		out := *existing
		out.Created = false
		return out, nil
	}
	r.next++
	record := &domain.PlaybackSessionRecord{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", r.next), Created: true, UserID: s.Actor.UserID, ItemID: s.ItemID,
		SourceID: s.SourceID, StartedAt: s.At, LastReportAt: s.At, PositionTicks: s.PositionTicks, RuntimeTicks: s.RuntimeTicks, Paused: s.Paused}
	r.sessions[key] = record
	r.states[record.ID] = domain.PlaybackActive
	return *record, nil
}

func (r *fakeProgressRepo) FlushPlayback(_ context.Context, entries []domain.PlaybackFlush) (domain.PlaybackFlushResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failFlush != nil {
		return domain.PlaybackFlushResult{}, r.failFlush
	}
	r.flushes = append(r.flushes, append([]domain.PlaybackFlush(nil), entries...))
	result := domain.PlaybackFlushResult{Statements: 1}
	for _, e := range entries {
		if r.states[e.SessionID] != domain.PlaybackActive {
			continue
		}
		result.Sessions++
		result.UserData++
		result.Samples += len(e.Samples)
		if e.End != nil {
			r.states[e.SessionID] = e.End.State
		}
	}
	return result, nil
}

func (r *fakeProgressRepo) ListStalePlayback(_ context.Context, before time.Time, limit int) ([]domain.PlaybackSessionRecord, error) {
	return r.stale, nil
}

func (r *fakeProgressRepo) PurgePlaybackHistory(_ context.Context, cutoff time.Time, limit int) (int, error) {
	r.purged = append(r.purged, cutoff)
	return 0, nil
}

func (r *fakeProgressRepo) UserItemData(_ context.Context, userID string, ids []string) (map[string]domain.UserItemData, error) {
	out := map[string]domain.UserItemData{}
	for _, id := range ids {
		if d, ok := r.userData[id]; ok {
			out[id] = d
		}
	}
	return out, nil
}

func (r *fakeProgressRepo) SetPlayed(_ context.Context, userID, itemID string, played bool, at time.Time) (domain.UserItemData, error) {
	return domain.UserItemData{ItemID: itemID, Played: played}, nil
}

func (r *fakeProgressRepo) ListResume(context.Context, string, domain.ResumeQuery) (domain.ResumePage, error) {
	return domain.ResumePage{}, nil
}

func (r *fakeProgressRepo) ClearPlaybackHistory(_ context.Context, actor domain.Actor) error {
	r.cleared = append(r.cleared, actor.UserID)
	return nil
}

func (r *fakeProgressRepo) ListActivePlayback(context.Context, domain.Actor, int) ([]domain.ActivePlayback, error) {
	return nil, nil
}

func (r *fakeProgressRepo) flushCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.flushes)
}

// fakeSources answers every item with one source of a known runtime, except
// the hidden item.
type fakeSources struct{ runtime time.Duration }

const (
	progressHidden = "00000000-0000-4000-8000-0000000000ff"
	progressSource = "00000000-0000-4000-9000-000000000001"
)

func (s fakeSources) ListPlaybackSources(_ context.Context, _ domain.Actor, itemID string) ([]domain.PlaybackSourceRecord, error) {
	if itemID == progressHidden {
		return nil, domain.ErrNotFound
	}
	micros := s.runtime.Microseconds()
	return []domain.PlaybackSourceRecord{{ID: progressSource, ContentType: "video/x-matroska", FileName: "a.mkv",
		Metadata: &domain.MediaMetadata{Format: domain.MediaFormat{DurationMicros: &micros}}}}, nil
}

func progressItem(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-1%011d", i) }

func progressActor(i int) domain.Actor {
	return domain.Actor{UserID: fmt.Sprintf("00000000-0000-4000-a000-%012d", i), SessionID: fmt.Sprintf("00000000-0000-4000-b000-%012d", i)}
}

func newTestProgress(t *testing.T, opts ProgressOptions) (*Progress, *fakeProgressRepo, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	repo := newFakeProgressRepo()
	opts.Clock = clock
	p, err := NewProgress(repo, fakeSources{runtime: 100 * time.Minute}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return p, repo, clock
}

func ticksOf(d time.Duration) int64 { return domain.DurationToTicks(d) }

func report(kind domain.PlaybackReportKind, key, item string, position time.Duration) domain.PlaybackReport {
	return domain.PlaybackReport{Kind: kind, PlayKey: key, ItemID: item, PositionTicks: ticksOf(position), PositionKnown: true}
}

// G23.2: 100 sessions reporting every second write one statement per flush,
// carrying only the latest state of each session.
func TestProgressBatchesReportsPerFlush(t *testing.T) {
	ctx := context.Background()
	p, repo, clock := newTestProgress(t, ProgressOptions{FlushInterval: 10 * time.Second})
	const sessions, seconds = 100, 30
	for i := range sessions {
		if err := p.Report(ctx, progressActor(i), report(domain.PlaybackReportStart, fmt.Sprintf("play-%d", i), progressItem(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	events := sessions
	for second := 1; second <= seconds; second++ {
		clock.Advance(time.Second)
		for i := range sessions {
			if err := p.Report(ctx, progressActor(i), report(domain.PlaybackReportProgress, fmt.Sprintf("play-%d", i), "", time.Duration(second)*time.Second)); err != nil {
				t.Fatal(err)
			}
			events++
		}
		if second%10 == 0 {
			if _, err := p.Flush(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if repo.starts != sessions {
		t.Fatalf("starts=%d", repo.starts)
	}
	if len(repo.flushes) != seconds/10 {
		t.Fatalf("flush statements=%d, want %d", len(repo.flushes), seconds/10)
	}
	for n, batch := range repo.flushes {
		if len(batch) != sessions {
			t.Fatalf("flush %d carries %d sessions", n, len(batch))
		}
		for _, e := range batch {
			if want := ticksOf(time.Duration((n+1)*10) * time.Second); e.PositionTicks != want || e.End != nil {
				t.Fatalf("flush %d entry position %d, want latest %d", n, e.PositionTicks, want)
			}
		}
	}
	stats := p.Stats()
	if stats.Reports != int64(events) || stats.FlushStatements != 3 || stats.StartWrites != sessions || stats.Coalesced < int64(events-2*sessions-3*sessions) {
		t.Fatalf("stats %+v events %d", stats, events)
	}
	// Nothing dirty: a flush writes nothing.
	if _, err := p.Flush(ctx); err != nil || len(repo.flushes) != 3 {
		t.Fatal("idle flush wrote", err)
	}
}

func TestProgressMaxBatchSplitsStatementsAndWakes(t *testing.T) {
	ctx := context.Background()
	p, repo, clock := newTestProgress(t, ProgressOptions{MaxBatch: 40})
	for i := range 100 {
		if err := p.Report(ctx, progressActor(i), report(domain.PlaybackReportStart, "k", progressItem(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(time.Second)
	for i := range 40 {
		_ = p.Report(ctx, progressActor(i), report(domain.PlaybackReportProgress, "k", "", time.Second))
	}
	select {
	case <-p.wake:
	default:
		t.Fatal("a full batch did not wake the flusher")
	}
	for i := 40; i < 100; i++ {
		_ = p.Report(ctx, progressActor(i), report(domain.PlaybackReportProgress, "k", "", time.Second))
	}
	result, err := p.Flush(ctx)
	if err != nil || result.Statements != 3 || len(repo.flushes) != 3 || len(repo.flushes[0]) != 40 || len(repo.flushes[2]) != 20 {
		t.Fatalf("result %+v flushes %d err %v", result, len(repo.flushes), err)
	}
}

// Session ID deduplication: repeated starts rejoin one session; repeated
// stops are accepted and write once.
func TestProgressDeduplicatesStartsAndStops(t *testing.T) {
	ctx := context.Background()
	p, repo, clock := newTestProgress(t, ProgressOptions{})
	actor := progressActor(1)
	for range 3 {
		if err := p.Report(ctx, actor, report(domain.PlaybackReportStart, "dup", progressItem(1), 0)); err != nil {
			t.Fatal(err)
		}
	}
	if repo.starts != 1 || len(p.sessions) != 1 {
		t.Fatalf("starts=%d live=%d", repo.starts, len(p.sessions))
	}
	clock.Advance(time.Minute)
	if err := p.Report(ctx, actor, report(domain.PlaybackReportStop, "dup", "", 50*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(repo.flushes) != 1 || repo.flushes[0][0].End == nil || repo.flushes[0][0].End.State != domain.PlaybackStopped {
		t.Fatalf("stop not written at once: %+v", repo.flushes)
	}
	// Another stop of the same play key: the stored session is stopped, so
	// rejoining is refused and the stop is accepted without a write.
	if err := p.Report(ctx, actor, report(domain.PlaybackReportStop, "dup", progressItem(1), 51*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(repo.flushes) != 1 {
		t.Fatal("repeated stop wrote again")
	}
	// Progress after the stop names a closed session.
	if err := p.Report(ctx, actor, report(domain.PlaybackReportProgress, "dup", progressItem(1), time.Minute)); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("progress reopened a stopped session", err)
	}
	// A play key bound to another item is a conflict.
	_ = p.Report(ctx, actor, report(domain.PlaybackReportStart, "other", progressItem(2), 0))
	if err := p.Report(ctx, actor, report(domain.PlaybackReportProgress, "other", progressItem(3), 0)); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("play key moved to another item", err)
	}
	// Invisible items and unknown keys without an item are not found.
	if err := p.Report(ctx, actor, report(domain.PlaybackReportStart, "hidden", progressHidden, 0)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("hidden item started", err)
	}
	if err := p.Report(ctx, actor, domain.PlaybackReport{Kind: domain.PlaybackReportProgress, PlayKey: "unknown"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown key without item", err)
	}
	// A ping of an unknown session is ignored.
	if err := p.Report(ctx, actor, domain.PlaybackReport{Kind: domain.PlaybackReportPing, PlayKey: "unknown"}); err != nil {
		t.Fatal(err)
	}
}

func TestProgressStopOutcome(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		position  time.Duration
		failed    bool
		resume    time.Duration
		completed bool
		state     domain.PlaybackState
	}{
		{"completed", 95 * time.Minute, false, 0, true, domain.PlaybackStopped},
		{"middle", 50 * time.Minute, false, 50 * time.Minute, false, domain.PlaybackStopped},
		{"beginning", 10 * time.Second, false, 0, false, domain.PlaybackStopped},
		{"failed near end", 95 * time.Minute, true, 0, false, domain.PlaybackFailed},
		{"failed middle", 40 * time.Minute, true, 40 * time.Minute, false, domain.PlaybackFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, repo, clock := newTestProgress(t, ProgressOptions{})
			actor := progressActor(1)
			if err := p.Report(ctx, actor, report(domain.PlaybackReportStart, "s", progressItem(1), 0)); err != nil {
				t.Fatal(err)
			}
			clock.Advance(time.Minute)
			stop := report(domain.PlaybackReportStop, "s", "", tc.position)
			stop.Failed = tc.failed
			if err := p.Report(ctx, actor, stop); err != nil {
				t.Fatal(err)
			}
			e := repo.flushes[0][0]
			if e.ResumeTicks != ticksOf(tc.resume) || e.Completed != tc.completed || e.End.State != tc.state || e.SourceID != progressSource {
				t.Fatalf("entry %+v end %+v", e, e.End)
			}
			if tc.failed && e.End.FailureReason != domain.PlaybackFailureError {
				t.Fatal("failure reason", e.End.FailureReason)
			}
			last := e.Samples[len(e.Samples)-1]
			if want := map[bool]domain.WatchSampleKind{false: domain.WatchSampleStop, true: domain.WatchSampleFail}[tc.failed]; last.Kind != want {
				t.Fatal("final sample", last.Kind)
			}
		})
	}
}

// Disconnected clients: a session silent for SessionTimeout is closed as
// timed out by the next flush, with the stop rules applied to its last
// position.
func TestProgressTimesOutSilentSessions(t *testing.T) {
	ctx := context.Background()
	p, repo, clock := newTestProgress(t, ProgressOptions{FlushInterval: 10 * time.Second, SessionTimeout: time.Minute})
	quiet, alive := progressActor(1), progressActor(2)
	for _, a := range []domain.Actor{quiet, alive} {
		if err := p.Report(ctx, a, report(domain.PlaybackReportStart, "t", progressItem(1), 0)); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(30 * time.Second)
	_ = p.Report(ctx, quiet, report(domain.PlaybackReportProgress, "t", "", 30*time.Minute))
	_ = p.Report(ctx, alive, report(domain.PlaybackReportProgress, "t", "", 30*time.Minute))
	at := clock.Now()
	clock.Advance(30 * time.Second)
	_ = p.Report(ctx, alive, domain.PlaybackReport{Kind: domain.PlaybackReportPing, PlayKey: "t"})
	clock.Advance(35 * time.Second)
	if _, err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var ended, kept int
	for _, e := range repo.flushes[len(repo.flushes)-1] {
		if e.End != nil {
			ended++
			if e.End.State != domain.PlaybackTimedOut || !e.End.At.Equal(at) || e.ResumeTicks != ticksOf(30*time.Minute) {
				t.Fatalf("timed out entry %+v %+v", e, e.End)
			}
		} else {
			kept++
		}
	}
	if ended != 1 || kept != 1 || len(p.sessions) != 1 || p.Stats().TimedOut != 1 {
		t.Fatalf("ended=%d kept=%d live=%d", ended, kept, len(p.sessions))
	}
}

// Samples kept for statistics: state changes, one progress sample per
// interval and both sides of a jump; ComputeWatchSession over them matches
// the full stream.
func TestProgressKeepsStatisticsSamples(t *testing.T) {
	ctx := context.Background()
	p, repo, clock := newTestProgress(t, ProgressOptions{SampleInterval: time.Minute})
	actor := progressActor(1)
	if err := p.Report(ctx, actor, report(domain.PlaybackReportStart, "s", progressItem(1), 0)); err != nil {
		t.Fatal(err)
	}
	var full []domain.WatchSample
	full = append(full, domain.WatchSample{At: clock.Now(), Kind: domain.WatchSampleStart})
	position := time.Duration(0)
	send := func(r domain.PlaybackReport, kind domain.WatchSampleKind) {
		t.Helper()
		if err := p.Report(ctx, actor, r); err != nil {
			t.Fatal(err)
		}
		full = append(full, domain.WatchSample{At: clock.Now(), Kind: kind, Position: domain.TicksToDuration(r.PositionTicks), Paused: r.Paused})
	}
	for range 300 { // five minutes at one report per second
		clock.Advance(time.Second)
		position += time.Second
		send(report(domain.PlaybackReportProgress, "s", "", position), domain.WatchSampleProgress)
	}
	clock.Advance(time.Second)
	position = 40 * time.Minute // seek forward
	send(report(domain.PlaybackReportProgress, "s", "", position), domain.WatchSampleProgress)
	for range 10 {
		clock.Advance(time.Second)
		position += time.Second
		send(report(domain.PlaybackReportProgress, "s", "", position), domain.WatchSampleProgress)
	}
	pause := report(domain.PlaybackReportProgress, "s", "", position)
	pause.Paused = true
	clock.Advance(time.Second)
	send(pause, domain.WatchSampleProgress)
	clock.Advance(time.Second)
	send(report(domain.PlaybackReportStop, "s", "", position), domain.WatchSampleStop)
	var kept []domain.PlaybackSample
	for _, batch := range repo.flushes {
		for _, e := range batch {
			kept = append(kept, e.Samples...)
		}
	}
	if len(kept) > 16 || len(kept) < 8 {
		t.Fatalf("kept %d samples of %d reports", len(kept), len(full))
	}
	kinds := map[domain.WatchSampleKind]int{}
	for i, s := range kept {
		if s.Seq != i {
			t.Fatal("sample sequence", i, s.Seq)
		}
		kinds[s.Kind]++
	}
	if kinds[domain.WatchSampleStart] != 1 || kinds[domain.WatchSamplePause] != 1 || kinds[domain.WatchSampleStop] != 1 {
		t.Fatalf("kinds %v", kinds)
	}
	samples := make([]domain.WatchSample, len(kept))
	for i, s := range kept {
		samples[i] = s.WatchSample()
	}
	rules := domain.DefaultWatchStatsRules()
	got, err := domain.ComputeWatchSession(samples, 100*time.Minute, rules)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := domain.ComputeWatchSession(full, 100*time.Minute, rules)
	if got.Effective != want.Effective || got.Covered != want.Covered || got.Anomalies.Jumps != want.Anomalies.Jumps || got.LastPosition != want.LastPosition {
		t.Fatalf("decimated stats %+v, full %+v", got, want)
	}
}

func TestProgressFlushFailureRetries(t *testing.T) {
	ctx := context.Background()
	p, repo, clock := newTestProgress(t, ProgressOptions{})
	actor := progressActor(1)
	_ = p.Report(ctx, actor, report(domain.PlaybackReportStart, "r", progressItem(1), 0))
	clock.Advance(time.Minute)
	_ = p.Report(ctx, actor, report(domain.PlaybackReportProgress, "r", "", time.Minute))
	repo.failFlush = domain.ErrDatabase
	if _, err := p.Flush(ctx); !errors.Is(err, domain.ErrDatabase) {
		t.Fatal(err)
	}
	if err := p.Report(ctx, actor, report(domain.PlaybackReportStop, "r", "", 2*time.Minute)); !errors.Is(err, domain.ErrDatabase) {
		t.Fatal("failed stop reported success", err)
	}
	repo.failFlush = nil
	if _, err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(repo.flushes) != 1 || len(repo.flushes[0]) != 1 {
		t.Fatalf("retry wrote %v", repo.flushes)
	}
	e := repo.flushes[0][0]
	if e.End == nil || e.End.State != domain.PlaybackStopped || e.ResumeTicks != ticksOf(2*time.Minute) || len(e.Samples) < 2 || e.Samples[0].Kind != domain.WatchSampleStart {
		t.Fatalf("retried stop %+v", e)
	}
	if len(p.sessions) != 0 || p.dirty != 0 {
		t.Fatal("retried stop left state", len(p.sessions), p.dirty)
	}
}

func TestProgressBoundsSessionsAndClearsHistory(t *testing.T) {
	ctx := context.Background()
	p, repo, _ := newTestProgress(t, ProgressOptions{MaxSessions: 2})
	for i := range 2 {
		if err := p.Report(ctx, progressActor(1), report(domain.PlaybackReportStart, fmt.Sprintf("k%d", i), progressItem(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Report(ctx, progressActor(2), report(domain.PlaybackReportStart, "k", progressItem(1), 0)); !errors.Is(err, domain.ErrPlaybackBusy) {
		t.Fatal("bound not applied", err)
	}
	_ = p.Report(ctx, progressActor(1), report(domain.PlaybackReportProgress, "k0", "", time.Minute))
	if err := p.ClearHistory(ctx, progressActor(1)); err != nil || len(repo.cleared) != 1 {
		t.Fatal(err)
	}
	if len(p.sessions) != 0 || p.dirty != 0 {
		t.Fatal("buffered sessions survived a clear")
	}
	if _, err := p.Flush(ctx); err != nil || len(repo.flushes) != 0 {
		t.Fatal("cleared sessions were written back")
	}
}

// Buffered positions show in user data only for items storage reports as
// visible.
func TestProgressUserDataOverlayStaysVisible(t *testing.T) {
	ctx := context.Background()
	p, repo, clock := newTestProgress(t, ProgressOptions{})
	actor := progressActor(1)
	visible, hidden := progressItem(1), progressItem(2)
	repo.userData[visible] = domain.UserItemData{ItemID: visible}
	for _, item := range []string{visible, hidden} {
		if err := p.Report(ctx, actor, report(domain.PlaybackReportStart, "k"+item, item, 0)); err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Second)
		if err := p.Report(ctx, actor, report(domain.PlaybackReportProgress, "k"+item, "", 20*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	data, err := p.UserItemData(ctx, actor.UserID, []string{visible, hidden})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := data[hidden]; ok || data[visible].ResumeTicks != ticksOf(20*time.Minute) {
		t.Fatalf("overlay %+v", data)
	}
}

func TestProgressMaintainClosesOrphansAndPurges(t *testing.T) {
	ctx := context.Background()
	p, repo, clock := newTestProgress(t, ProgressOptions{Retention: 24 * time.Hour})
	actor := progressActor(1)
	_ = p.Report(ctx, actor, report(domain.PlaybackReportStart, "live", progressItem(1), 0))
	live := p.sessions[liveKey(actor.UserID, "live")].id
	last := clock.Now().Add(-time.Hour)
	repo.stale = []domain.PlaybackSessionRecord{
		{ID: live, LastReportAt: last},
		{ID: "00000000-0000-4000-8000-00000000abcd", LastReportAt: last, PositionTicks: ticksOf(99 * time.Minute), RuntimeTicks: ticksOf(100 * time.Minute)},
	}
	if err := p.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if len(repo.flushes) != 1 || len(repo.flushes[0]) != 1 {
		t.Fatalf("orphans %v", repo.flushes)
	}
	e := repo.flushes[0][0]
	if e.SessionID == live || e.End.State != domain.PlaybackTimedOut || !e.Completed || len(repo.purged) != 1 || !repo.purged[0].Equal(clock.Now().Add(-24*time.Hour)) {
		t.Fatalf("orphan %+v purged %v", e, repo.purged)
	}
	// Purging runs at most hourly.
	clock.Advance(time.Minute)
	repo.stale = nil
	_ = p.Maintain(ctx)
	if len(repo.purged) != 1 {
		t.Fatal("purged again within the hour")
	}
}

func TestProgressRejectsInvalidReports(t *testing.T) {
	ctx := context.Background()
	p, _, _ := newTestProgress(t, ProgressOptions{})
	actor := progressActor(1)
	for _, r := range []domain.PlaybackReport{
		{Kind: "seek", PlayKey: "k", ItemID: progressItem(1)},
		{Kind: domain.PlaybackReportStart, PlayKey: "bad key", ItemID: progressItem(1)},
		{Kind: domain.PlaybackReportStart, PlayKey: "k", ItemID: "x"},
		{Kind: domain.PlaybackReportProgress, PlayKey: "k", ItemID: progressItem(1), PositionTicks: -1, PositionKnown: true},
		{Kind: domain.PlaybackReportProgress, PlayKey: "k", ItemID: progressItem(1), Failed: true},
		{Kind: domain.PlaybackReportStop, PlayKey: "k", ItemID: progressItem(1), Failed: true, FailureReason: "unknown"},
	} {
		if err := p.Report(ctx, actor, r); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%+v: %v", r, err)
		}
	}
	if err := p.Report(ctx, domain.Actor{UserID: "x"}, report(domain.PlaybackReportStart, "k", progressItem(1), 0)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := NewProgress(newFakeProgressRepo(), fakeSources{}, ProgressOptions{FlushInterval: time.Minute, SessionTimeout: time.Minute}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("timeout shorter than three flushes accepted")
	}
}
