package postgres

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

type watchStatsFixture struct {
	progressFixture
	stats *app.WatchStats
	day   time.Time
}

func newWatchStatsFixture(t *testing.T) watchStatsFixture {
	t.Helper()
	f := watchStatsFixture{progressFixture: newProgressFixture(t)}
	// A fixed morning keeps every session of the test on one UTC day.
	f.day = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	f.clock.t = f.day.Add(8 * time.Hour)
	var err error
	if f.stats, err = app.NewWatchStats(f.s, app.WatchStatsOptions{Clock: f.clock}); err != nil {
		t.Fatal(err)
	}
	return f
}

// play reports a session of actor on item from position from for d,
// reporting every 30 seconds, and stops it unless keep is set.
func (f watchStatsFixture) play(t *testing.T, actor domain.Actor, key, item string, from, d time.Duration, keep bool) {
	t.Helper()
	f.report(t, actor, domain.PlaybackReportStart, key, item, from)
	for elapsed := 30 * time.Second; elapsed <= d; elapsed += 30 * time.Second {
		f.clock.Advance(30 * time.Second)
		f.report(t, actor, domain.PlaybackReportProgress, key, "", from+elapsed)
	}
	if !keep {
		f.report(t, actor, domain.PlaybackReportStop, key, "", from+d)
	}
}

func (f watchStatsFixture) aggregate(t *testing.T) int {
	t.Helper()
	n, err := f.stats.Aggregate(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type dailyRow struct {
	effective                                         int64
	sessions, views, first, rewatches, completions, n int
}

func (f watchStatsFixture) daily(t *testing.T, user string) dailyRow {
	t.Helper()
	var r dailyRow
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT COALESCE(sum(effective_ms),0)::bigint,COALESCE(sum(sessions),0)::int,COALESCE(sum(views),0)::int,
 COALESCE(sum(first_plays),0)::int,COALESCE(sum(rewatches),0)::int,COALESCE(sum(completions),0)::int,count(*)::int FROM watch_stats_daily WHERE user_id=$1::uuid`, user).
		Scan(&r.effective, &r.sessions, &r.views, &r.first, &r.rewatches, &r.completions, &r.n); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f watchStatsFixture) query(subject string) domain.WatchStatsQuery {
	return domain.WatchStatsQuery{SubjectID: subject, From: f.day.AddDate(0, 0, -10), To: f.day.AddDate(0, 0, 10), Period: domain.WatchPeriodDay, Top: 10, WeekStart: time.Monday}
}

// Ended sessions roll up into daily rows once: effective time without
// pauses, counted views, overlapping devices counted once across runs, a
// rejoined session adding only its new part, and retention never losing
// statistics.
func TestWatchStatsAggregationPostgres(t *testing.T) {
	f := newWatchStatsFixture(t)
	user := f.viewer.UserID
	// 20 minutes with a 10 minute pause in the middle.
	f.play(t, f.viewer, "one", f.item, 0, 10*time.Minute, true)
	paused := func(on bool) {
		t.Helper()
		if err := f.progress.Report(f.ctx, f.viewer, domain.PlaybackReport{Kind: domain.PlaybackReportProgress, PlayKey: "one",
			PositionTicks: domain.DurationToTicks(10 * time.Minute), PositionKnown: true, Paused: on}); err != nil {
			t.Fatal(err)
		}
	}
	paused(true)
	for range 20 {
		f.clock.Advance(30 * time.Second)
		paused(true)
	}
	paused(false)
	for elapsed := 30 * time.Second; elapsed <= 10*time.Minute; elapsed += 30 * time.Second {
		f.clock.Advance(30 * time.Second)
		f.report(t, f.viewer, domain.PlaybackReportProgress, "one", "", 10*time.Minute+elapsed)
	}
	// A jump ahead by an hour is not watched time.
	f.clock.Advance(5 * time.Second)
	f.report(t, f.viewer, domain.PlaybackReportProgress, "one", "", 80*time.Minute)
	f.clock.Advance(30 * time.Second)
	f.report(t, f.viewer, domain.PlaybackReportStop, "one", "", 80*time.Minute+30*time.Second)
	if n := f.count(t, `SELECT count(*) FROM watch_stats_daily`); n != 0 {
		t.Fatal("statistics written before aggregation")
	}
	if pending, err := f.s.PendingWatchStats(f.ctx); err != nil || pending != 1 {
		t.Fatal("pending", pending, err)
	}
	if n := f.aggregate(t); n != 1 {
		t.Fatal("aggregated", n)
	}
	r := f.daily(t, user)
	if r.effective != (20*time.Minute+30*time.Second).Milliseconds() || r.sessions != 1 || r.views != 1 || r.first != 1 || r.completions != 0 || r.n != 1 {
		t.Fatalf("first session %+v", r)
	}
	var through, ended time.Time
	var day string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT stats_through,ended_at,to_char(stats_day,'YYYY-MM-DD') FROM playback_sessions WHERE play_key='one'`).Scan(&through, &ended, &day); err != nil ||
		!through.Equal(ended) || day != "2026-03-10" {
		t.Fatal("session mark", through, ended, day, err)
	}
	// Idempotent: nothing pending, nothing added.
	if n := f.aggregate(t); n != 0 || f.daily(t, user) != r {
		t.Fatal("second run changed the roll-up", n)
	}

	// Two devices play the same stretch; one stops early and is aggregated
	// before the other ends. The overlap counts once.
	f.clock.Advance(time.Hour)
	f.report(t, f.viewer, domain.PlaybackReportStart, "phone", f.item, 0)
	f.report(t, f.viewer, domain.PlaybackReportStart, "tv", f.item, 0)
	for elapsed := 30 * time.Second; elapsed <= 10*time.Minute; elapsed += 30 * time.Second {
		f.clock.Advance(30 * time.Second)
		f.report(t, f.viewer, domain.PlaybackReportProgress, "tv", "", elapsed)
		if elapsed <= 5*time.Minute {
			f.report(t, f.viewer, domain.PlaybackReportProgress, "phone", "", elapsed)
		}
		if elapsed == 5*time.Minute {
			f.report(t, f.viewer, domain.PlaybackReportStop, "phone", "", elapsed)
			if _, err := f.progress.Flush(f.ctx); err != nil {
				t.Fatal(err)
			}
			if n := f.aggregate(t); n != 1 {
				t.Fatal("phone aggregated", n)
			}
		}
	}
	f.report(t, f.viewer, domain.PlaybackReportStop, "tv", "", 10*time.Minute)
	if n := f.aggregate(t); n != 1 {
		t.Fatal("tv aggregated", n)
	}
	r2 := f.daily(t, user)
	if r2.effective-r.effective != (10*time.Minute).Milliseconds() || r2.sessions != 3 || r2.views != 1 {
		t.Fatalf("two devices %+v after %+v", r2, r)
	}

	// A session times out, is aggregated, then the client comes back: only
	// the new part is added and the session counts once.
	f.clock.Advance(time.Hour)
	f.play(t, f.viewer, "nap", f.item, 0, 5*time.Minute, true)
	f.clock.Advance(2 * time.Minute)
	if _, err := f.progress.Flush(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.aggregate(t); n != 1 {
		t.Fatal("timed out session aggregated", n)
	}
	r3 := f.daily(t, f.viewer.UserID)
	if r3.effective-r2.effective != (5*time.Minute).Milliseconds() || r3.sessions != 4 {
		t.Fatalf("timed out %+v", r3)
	}
	f.report(t, f.viewer, domain.PlaybackReportProgress, "nap", f.item, 5*time.Minute)
	for elapsed := 30 * time.Second; elapsed <= 5*time.Minute; elapsed += 30 * time.Second {
		f.clock.Advance(30 * time.Second)
		f.report(t, f.viewer, domain.PlaybackReportProgress, "nap", "", 5*time.Minute+elapsed)
	}
	f.report(t, f.viewer, domain.PlaybackReportStop, "nap", "", 10*time.Minute)
	if n := f.aggregate(t); n != 1 {
		t.Fatal("rejoined session aggregated", n)
	}
	r4 := f.daily(t, f.viewer.UserID)
	if r4.effective-r3.effective != (5*time.Minute).Milliseconds() || r4.sessions != 4 || r4.views != 1 {
		t.Fatalf("rejoined %+v after %+v", r4, r3)
	}

	// Retention: an unaggregated session is kept past the cutoff; once
	// aggregated it goes, and the roll-up stays.
	f.clock.Advance(time.Hour)
	f.play(t, f.viewer, "old", f.item, 0, 3*time.Minute, false)
	cutoff := f.clock.Now().Add(time.Minute)
	if n, err := f.s.PurgePlaybackHistory(f.ctx, cutoff, 100); err != nil || n != 4 || f.count(t, `SELECT count(*) FROM playback_sessions WHERE play_key='old'`) != 1 {
		t.Fatal("purge of aggregated sessions", n, err)
	}
	f.aggregate(t)
	if n, err := f.s.PurgePlaybackHistory(f.ctx, cutoff, 100); err != nil || n != 1 {
		t.Fatal("purge after aggregation", n, err)
	}
	if r5 := f.daily(t, user); r5.effective-r4.effective != (3*time.Minute).Milliseconds() || r5.sessions != 5 || f.count(t, `SELECT count(*) FROM playback_sessions`) != 0 {
		t.Fatalf("roll-up after purge %+v", r5)
	}
}

// Reports read the roll-up with the viewer's grants (G48.3), administrators
// read every user, clearing history and deleting a user remove statistics,
// and a batch that loses a session to a concurrent clear writes nothing.
func TestWatchStatsReportsACLAndClearPostgres(t *testing.T) {
	f := newWatchStatsFixture(t)
	other := imageRepositoryActor(t, f.jobFixture, "stats-other", access.ClientNative)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, other.UserID, f.registration.Library.ID)
	// A second library holding a second item, granted to the viewer.
	second, err := f.s.RegisterLibrary(f.ctx, "second", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var episode string
	if err = f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Second Episode','Episode') RETURNING id::text`, second.Library.ID).Scan(&episode); err != nil {
		t.Fatal(err)
	}
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, f.viewer.UserID, second.Library.ID)

	f.play(t, f.viewer, "movie", f.item, 0, 10*time.Minute, false)
	f.clock.Advance(16 * time.Hour) // 2026-03-11
	f.play(t, f.viewer, "episode", episode, 0, 4*time.Minute, false)
	f.play(t, other, "movie", f.item, 0, 6*time.Minute, false)
	if n := f.aggregate(t); n != 3 {
		t.Fatal("aggregated", n)
	}

	own, err := f.s.WatchStatsReport(f.ctx, f.viewer, f.query(f.viewer.UserID))
	if err != nil {
		t.Fatal(err)
	}
	if own.Totals.EffectiveSeconds != 840 || own.Totals.Sessions != 2 || own.Totals.Views != 2 || len(own.Periods) != 2 || own.Periods[0].Start != "2026-03-10" ||
		own.Periods[0].EffectiveSeconds != 600 || own.Periods[1].EffectiveSeconds != 240 || own.TopUsers != nil {
		t.Fatalf("own report %+v", own)
	}
	if len(own.TopItems) != 2 || own.TopItems[0].ItemID != f.item || own.TopItems[0].Kind != "Movie" || own.TopItems[0].UserData == nil ||
		own.TopItems[0].UserData.ResumeTicks != domain.DurationToTicks(10*time.Minute) || own.TopItems[1].ItemID != episode {
		t.Fatalf("own top items %+v", own.TopItems)
	}
	if len(own.Libraries) != 2 || own.Libraries[0].LibraryID != f.registration.Library.ID || own.Libraries[0].Name != "primary" || len(own.Kinds) != 2 || own.Kinds[0].Kind != "Movie" {
		t.Fatalf("own breakdowns %+v %+v", own.Libraries, own.Kinds)
	}
	// Weeks and months are sums of the daily rows; both days share a week
	// starting Monday 2026-03-09 and a month.
	for _, c := range []struct {
		period domain.WatchPeriod
		start  time.Weekday
		want   string
	}{{domain.WatchPeriodWeek, time.Monday, "2026-03-09"}, {domain.WatchPeriodWeek, time.Sunday, "2026-03-08"}, {domain.WatchPeriodMonth, time.Monday, "2026-03-01"}, {domain.WatchPeriodYear, time.Monday, "2026-01-01"}} {
		q := f.query(f.viewer.UserID)
		q.Period, q.WeekStart = c.period, c.start
		r, err := f.s.WatchStatsReport(f.ctx, f.viewer, q)
		if err != nil || len(r.Periods) != 1 || r.Periods[0].Start != c.want || r.Periods[0].EffectiveSeconds != 840 ||
			r.Periods[0].Start != domain.WatchStatsBucket(f.day, c.period, c.start).Format(time.DateOnly) {
			t.Fatalf("%s/%s periods %+v %v", c.period, c.start, r.Periods, err)
		}
	}
	// Top 1 and an empty range.
	q := f.query(f.viewer.UserID)
	q.Top = 1
	if r, err := f.s.WatchStatsReport(f.ctx, f.viewer, q); err != nil || len(r.TopItems) != 1 {
		t.Fatal("top 1", r.TopItems, err)
	}
	q.From, q.To = f.day.AddDate(0, 1, 0), f.day.AddDate(0, 1, 5)
	if r, err := f.s.WatchStatsReport(f.ctx, f.viewer, q); err != nil || r.Totals != (domain.WatchStatsTotals{}) || len(r.Periods) != 0 || len(r.TopItems) != 0 {
		t.Fatal("empty range", r, err)
	}

	// Authorization: viewers read only themselves.
	if _, err = f.s.WatchStatsReport(f.ctx, f.viewer, f.query("")); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer read every user", err)
	}
	if _, err = f.s.WatchStatsReport(f.ctx, f.viewer, f.query(other.UserID)); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer read another user", err)
	}
	if _, err = f.s.WatchStatsReport(f.ctx, f.a, f.query("10000000-0000-4000-8000-000000000099")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown subject", err)
	}
	all, err := f.s.WatchStatsReport(f.ctx, f.a, f.query(""))
	if err != nil || all.Totals.EffectiveSeconds != 1200 || all.Totals.Sessions != 3 || len(all.TopUsers) != 2 || all.TopUsers[0].UserID != f.viewer.UserID ||
		all.TopUsers[0].UserName != "progress-viewer" || all.TopItems[0].UserData != nil {
		t.Fatalf("every user %+v %v", all, err)
	}

	// G48.3: withdrawing the second library hides its item from the
	// viewer's statistics; administrators still see it. Restoring the
	// grant brings it back: the rows were never deleted.
	imageRepositoryExec(t, f.jobFixture, `DELETE FROM library_acl WHERE user_id=$1::uuid AND library_id=$2::uuid`, f.viewer.UserID, second.Library.ID)
	own, err = f.s.WatchStatsReport(f.ctx, f.viewer, f.query(f.viewer.UserID))
	if err != nil || own.Totals.EffectiveSeconds != 600 || len(own.TopItems) != 1 || own.TopItems[0].ItemID != f.item || len(own.Libraries) != 1 || len(own.Kinds) != 1 || len(own.Periods) != 1 {
		t.Fatalf("hidden library still counted %+v %v", own, err)
	}
	if r, err := f.s.WatchStatsReport(f.ctx, f.a, f.query(f.viewer.UserID)); err != nil || r.Totals.EffectiveSeconds != 840 {
		t.Fatal("administrator view of the user", r.Totals, err)
	}
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, f.viewer.UserID, second.Library.ID)
	if own, err = f.s.WatchStatsReport(f.ctx, f.viewer, f.query(f.viewer.UserID)); err != nil || own.Totals.EffectiveSeconds != 840 {
		t.Fatal("restored grant", own.Totals, err)
	}

	// A batch that loses its session to a concurrent clear writes nothing.
	f.play(t, f.viewer, "race", f.item, 0, 3*time.Minute, false)
	before := f.daily(t, f.viewer.UserID)
	_, err = f.s.AggregateWatchStats(f.ctx, 10, func(batch domain.WatchStatsBatch) (domain.WatchStatsOutcome, error) {
		if err := f.progress.ClearHistory(context.WithoutCancel(f.ctx), f.viewer); err != nil {
			t.Error(err)
		}
		return domain.AggregateWatchStats(batch, domain.DefaultWatchStatsRules(), time.UTC)
	})
	if !errors.Is(err, domain.ErrConflict) || before.n == 0 {
		t.Fatal("batch over a cleared session", err)
	}
	// G23.4: clearing history zeroes the user's statistics, not others'.
	if r := f.daily(t, f.viewer.UserID); r.n != 0 || f.count(t, `SELECT count(*) FROM watch_stats_history WHERE user_id=$1::uuid`, f.viewer.UserID) != 0 {
		t.Fatalf("cleared history kept statistics %+v", r)
	}
	if own, err = f.s.WatchStatsReport(f.ctx, f.viewer, f.query(f.viewer.UserID)); err != nil || own.Totals != (domain.WatchStatsTotals{}) || len(own.TopItems) != 0 {
		t.Fatal("report after clear", own, err)
	}
	if r := f.daily(t, other.UserID); r.n != 1 {
		t.Fatal("clearing one user touched another", r)
	}
	// Deleting a user removes its statistics; restoring does not bring them back.
	if err = f.s.DeleteUser(f.ctx, f.a, other.UserID); err != nil {
		t.Fatal(err)
	}
	if r := f.daily(t, other.UserID); r.n != 0 {
		t.Fatal("deleted user kept statistics")
	}
	if _, err = f.s.RestoreUser(f.ctx, f.a, other.UserID); err != nil {
		t.Fatal(err)
	}
	if r := f.daily(t, other.UserID); r.n != 0 {
		t.Fatal("restore brought statistics back")
	}
	// Deleting the item cascades.
	f.play(t, f.viewer, "gone", episode, 0, 3*time.Minute, false)
	f.aggregate(t)
	imageRepositoryExec(t, f.jobFixture, `DELETE FROM items WHERE id=$1::uuid`, episode)
	if n := f.count(t, `SELECT count(*) FROM watch_stats_daily WHERE item_id=$1::uuid`, episode); n != 0 {
		t.Fatal("deleted item kept statistics")
	}
}

// Exports are administrator only, bounded and audited before any row.
func TestWatchStatsExportPostgres(t *testing.T) {
	f := newWatchStatsFixture(t)
	for i, key := range []string{"a", "b", "c"} {
		f.play(t, f.viewer, key, f.item, 0, 3*time.Minute, false)
		f.clock.Advance(time.Duration(24-i) * time.Hour)
	}
	f.aggregate(t)
	q := domain.WatchStatsExportQuery{From: f.day, To: f.day.AddDate(0, 0, 5), Format: domain.WatchStatsExportCSV, Limit: 3}
	if _, err := f.s.AuditWatchStatsExport(f.ctx, f.viewer, q); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer exported", err)
	}
	rows, err := f.s.AuditWatchStatsExport(f.ctx, f.a, q)
	if err != nil || rows != 3 {
		t.Fatal("export count", rows, err)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE event='watch_stats.exported' AND actor_id=$1::uuid AND after_state::jsonb->>'rows'='3' AND after_state::jsonb->>'format'='csv'`, f.a.UserID); n != 1 {
		t.Fatal("export not audited", n)
	}
	var got []domain.WatchStatsExportRow
	if err = f.s.StreamWatchStatsExport(f.ctx, f.a, q, func(r domain.WatchStatsExportRow) error { got = append(got, r); return nil }); err != nil || len(got) != 3 {
		t.Fatal("stream", len(got), err)
	}
	if got[0].Day != "2026-03-10" || got[2].Day != "2026-03-12" || got[0].UserName != "progress-viewer" || got[0].Kind != "Movie" || got[0].EffectiveMillis != 180_000 || got[0].Sessions != 1 {
		t.Fatalf("rows %+v", got)
	}
	// Over the limit: refused before any audit.
	q.Limit = 2
	if _, err = f.s.AuditWatchStatsExport(f.ctx, f.a, q); !errors.Is(err, domain.ErrWatchStatsExportLimit) {
		t.Fatal("export over the limit", err)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE event='watch_stats.exported'`); n != 1 {
		t.Fatal("refused export audited", n)
	}
	// One user's rows; the stream stops at the limit even if rows appear.
	q.SubjectID, q.Limit = f.viewer.UserID, 2
	q.To = f.day
	if rows, err = f.s.AuditWatchStatsExport(f.ctx, f.a, q); err != nil || rows != 1 {
		t.Fatal("subject export", rows, err)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE event='watch_stats.exported' AND target_id=$1::uuid`, f.viewer.UserID); n != 1 {
		t.Fatal("subject export not audited", n)
	}
	// The stream refuses a viewer even if called directly.
	got = nil
	if err = f.s.StreamWatchStatsExport(f.ctx, f.viewer, q, func(r domain.WatchStatsExportRow) error { got = append(got, r); return nil }); err != nil || len(got) != 0 {
		t.Fatal("viewer stream", len(got), err)
	}
	// Statistics retention deletes whole days before the cutoff.
	if n, err := f.s.PurgeWatchStats(f.ctx, f.day.AddDate(0, 0, 1), 10); err != nil || n != 1 || f.count(t, `SELECT count(*) FROM watch_stats_daily`) != 2 {
		t.Fatal("statistics retention", n, err)
	}
}

// Storage constraints of the roll-up.
func TestWatchStatsConstraintsPostgres(t *testing.T) {
	f := newWatchStatsFixture(t)
	f.play(t, f.viewer, "c", f.item, 0, 3*time.Minute, false)
	f.aggregate(t)
	for _, statement := range []string{
		`UPDATE watch_stats_daily SET effective_ms=-1`,
		`UPDATE watch_stats_daily SET effective_ms=90000001`,
		`UPDATE watch_stats_daily SET views=views+1`,
		`UPDATE watch_stats_daily SET completions=-1`,
		`UPDATE watch_stats_daily SET completion_milli=-1`,
		`UPDATE watch_stats_daily SET views=sessions+1,first_plays=sessions+1`,
		`UPDATE watch_stats_daily SET library_id='` + f.viewer.UserID + `'`,
		`UPDATE playback_sessions SET stats_play='other'`,
		`UPDATE playback_sessions SET stats_completion_milli=1001`,
		`UPDATE playback_sessions SET stats_day=NULL WHERE stats_counted`,
	} {
		if _, err := f.s.Pool.Exec(f.ctx, statement); err == nil {
			t.Errorf("accepted: %s", statement)
		}
	}
}

func TestWatchStatsMigrationRoundTrip(t *testing.T) {
	f := newWatchStatsFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	want := downgradeAboveMigration(t, f.jobFixture, "watch_statistics")
	f.play(t, f.viewer, "m", f.item, 0, 3*time.Minute, false)
	f.aggregate(t)
	if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
		t.Fatal("retained statistics downgraded")
	}
	version, dirty, err := Migrate(f.ctx, dsn, "status")
	if err != nil || version != want-1 || !dirty {
		t.Fatal("refused downgrade state", version, dirty, err)
	}
	if f.count(t, `SELECT count(*) FROM watch_stats_daily`) != 1 {
		t.Fatal("refused downgrade removed statistics")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=false`, want); err != nil {
		t.Fatal(err)
	}
	if err = f.progress.ClearHistory(f.ctx, f.viewer); err != nil {
		t.Fatal(err)
	}
	// Sessions without statistics remain; schema 66 keeps them.
	f.play(t, f.viewer, "kept", f.item, 0, 3*time.Minute, false)
	if version, dirty, err = Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if f.count(t, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('watch_stats_daily','watch_stats_history')`) != 0 ||
		f.count(t, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='playback_sessions' AND column_name LIKE 'stats_%'`) != 0 {
		t.Fatal("downgrade left statistics schema")
	}
	if f.count(t, `SELECT count(*) FROM playback_sessions`) != 1 {
		t.Fatal("downgrade removed sessions")
	}
	if version, dirty, err = Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if err = f.s.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Sessions of schema 66 are aggregated after the upgrade.
	if n := f.aggregate(t); n != 1 || f.daily(t, f.viewer.UserID).effective != (3*time.Minute).Milliseconds() {
		t.Fatal("backfill after upgrade", n)
	}
}

// TestWatchStatsScalePostgres is the G23.5 evidence: with
// JELEE_STATS_SCALE_SESSIONS ended sessions (100000 in
// docs/watch-statistics.md) it drains the aggregation backlog, then times
// the reports and prints their plans, which read the daily roll-up only.
func TestWatchStatsScalePostgres(t *testing.T) {
	sessions, _ := strconv.Atoi(os.Getenv("JELEE_STATS_SCALE_SESSIONS"))
	if sessions <= 0 {
		t.Skip("set JELEE_STATS_SCALE_SESSIONS to run the scale evidence")
	}
	f := newWatchStatsFixture(t)
	const users, items, samples = 1000, 2000, 11
	start := time.Now()
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO users(name) SELECT 'scale-'||g FROM generate_series(1,$1::int) g`, users)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) SELECT id,$1::uuid FROM users WHERE name LIKE 'scale-%'`, f.registration.Library.ID)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO items(library_id,title,kind) SELECT $1::uuid,'Scale '||g,CASE WHEN g%3=0 THEN 'Episode' ELSE 'Movie' END FROM generate_series(1,$2::int) g`, f.registration.Library.ID, items)
	// Sessions spread over a year, 40 minutes each with a sample every 4
	// minutes; every fifth one plays to the end of a 45 minute runtime.
	imageRepositoryExec(t, f.jobFixture, `WITH u AS (SELECT id,row_number() OVER (ORDER BY id) n FROM users WHERE name LIKE 'scale-%'),
 i AS (SELECT id,library_id,row_number() OVER (ORDER BY id) n FROM items WHERE title LIKE 'Scale %')
INSERT INTO playback_sessions(user_id,play_key,item_id,library_id,state,started_at,last_report_at,ended_at,position_ticks,runtime_ticks,sample_count)
SELECT u.id,'scale-'||g,i.id,i.library_id,'stopped',t,t+interval '40 minutes',t+interval '40 minutes',24000000000,27000000000,$3::int
 FROM generate_series(1,$1::int) g CROSS JOIN LATERAL (SELECT $4::timestamptz-(g%365)*interval '1 day'+(g%720)*interval '1 minute' AS t) x
 JOIN u ON u.n=1+g%$2::int JOIN i ON i.n=1+(g*7)%$5::int`, sessions, users, samples, f.day.Add(-time.Hour), items)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO playback_samples(session_id,seq,at,kind,position_ticks)
SELECT p.id,s,p.started_at+s*interval '4 minutes',CASE WHEN s=0 THEN 'start' WHEN s=$1::int-1 THEN 'stop' ELSE 'progress' END,
 CASE WHEN p.play_key::text ~ '[05]$' THEN s*240::bigint*10000000*16/10 ELSE s*240::bigint*10000000 END
 FROM playback_sessions p CROSS JOIN generate_series(0,$1::int-1) s WHERE p.play_key LIKE 'scale-%'`, samples)
	imageRepositoryExec(t, f.jobFixture, `ANALYZE playback_sessions; ANALYZE playback_samples; ANALYZE users; ANALYZE items; ANALYZE library_acl`)
	t.Logf("fixture: %d sessions, %d samples in %s", sessions, f.count(t, `SELECT count(*) FROM playback_samples`), time.Since(start).Round(time.Millisecond))

	stats, err := app.NewWatchStats(f.s, app.WatchStatsOptions{Clock: f.clock, Batch: 1000, MaxBatches: 1000})
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	total := 0
	for {
		n, err := stats.Aggregate(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if n == 0 {
			break
		}
	}
	elapsed := time.Since(start)
	if total != sessions {
		t.Fatalf("aggregated %d of %d", total, sessions)
	}
	imageRepositoryExec(t, f.jobFixture, `ANALYZE watch_stats_daily; ANALYZE watch_stats_history`)
	t.Logf("aggregation: %d sessions in %s (%.0f sessions/s), %d daily rows", total, elapsed.Round(time.Millisecond), float64(total)/elapsed.Seconds(),
		f.count(t, `SELECT count(*) FROM watch_stats_daily`))
	var user string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM users WHERE name='scale-1'`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	// Reports through the store, timed as the median of 9 runs.
	viewer := imageRepositoryActor(t, f.jobFixture, "scale-viewer", access.ClientWeb)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, viewer.UserID, f.registration.Library.ID)
	year := domain.WatchStatsQuery{From: f.day.AddDate(-1, 0, 1), To: f.day, Period: domain.WatchPeriodMonth, Top: 10, WeekStart: time.Monday}
	for _, c := range []struct {
		name  string
		actor domain.Actor
		query domain.WatchStatsQuery
	}{
		{"administrator, every user, one year by month", f.a, year},
		{"administrator, one user, one year by month", f.a, func() domain.WatchStatsQuery { q := year; q.SubjectID = user; return q }()},
		{"administrator, every user, 30 days by day", f.a, domain.WatchStatsQuery{From: f.day.AddDate(0, 0, -29), To: f.day, Period: domain.WatchPeriodDay, Top: 10}},
	} {
		durations := make([]time.Duration, 0, 9)
		var report domain.WatchStatsReport
		for range 9 {
			began := time.Now()
			if report, err = f.s.WatchStatsReport(f.ctx, c.actor, c.query); err != nil {
				t.Fatal(err)
			}
			durations = append(durations, time.Since(began))
		}
		slices.Sort(durations)
		t.Logf("report %s: median %s, max %s, %d periods, totals %+v", c.name, durations[4].Round(time.Microsecond), durations[8].Round(time.Microsecond), len(report.Periods), report.Totals)
	}
	explain := func(name, query string, args pgx.NamedArgs) {
		t.Helper()
		rows, err := f.s.Pool.Query(f.ctx, `EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) `+query, args)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		if strings.Contains(plan, "playback_samples") || strings.Contains(plan, "playback_sessions") {
			t.Fatalf("%s reads the session tables:\n%s", name, plan)
		}
		t.Logf("EXPLAIN %s:\n%s", name, plan)
	}
	args := pgx.NamedArgs{"from": year.From.Format(time.DateOnly), "to": year.To.Format(time.DateOnly), "admin": false, "viewer": user, "subject": user, "top": 10, "wstart": 1}
	explain("one user's totals (as that user)", `SELECT `+watchStatsSums+watchStatsScope(true), args)
	explain("one user's top items", `SELECT d.item_id,`+watchStatsSums+watchStatsScope(true)+` GROUP BY d.item_id ORDER BY 2 DESC,4 DESC,d.item_id LIMIT @top`, args)
	args["admin"], args["viewer"] = true, f.a.UserID
	explain("every user's months", `SELECT to_char(`+watchStatsBucket(domain.WatchPeriodMonth)+`,'YYYY-MM-DD'),`+watchStatsSums+watchStatsScope(false)+` GROUP BY 1 ORDER BY 1`, args)
	explain("every user's top items", `SELECT d.item_id,`+watchStatsSums+watchStatsScope(false)+` GROUP BY d.item_id ORDER BY 2 DESC,4 DESC,d.item_id LIMIT @top`, args)
	// The sample table the requests never read, for scale.
	began := time.Now()
	f.count(t, `SELECT count(*) FROM playback_samples`)
	t.Logf("for comparison, one full count of playback_samples: %s", time.Since(began).Round(time.Microsecond))
}
