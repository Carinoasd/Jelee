package postgres

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type progressClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *progressClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *progressClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type progressFixture struct {
	playbackFixture
	clock    *progressClock
	progress *app.Progress
	viewer   domain.Actor
	runtime  int64
}

func newProgressFixture(t *testing.T) progressFixture {
	t.Helper()
	f := progressFixture{playbackFixture: newPlaybackFixture(t), clock: &progressClock{t: time.Now().UTC().Truncate(time.Microsecond)}}
	f.viewer = imageRepositoryActor(t, f.jobFixture, "progress-viewer", access.ClientNative)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, f.viewer.UserID, f.registration.Library.ID)
	var err error
	f.progress, err = app.NewProgress(f.s, f.s, app.ProgressOptions{Clock: f.clock, FlushInterval: 10 * time.Second, SessionTimeout: time.Minute, Retention: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	f.runtime = 7200 * domain.PlaybackTicksPerSecond
	return f
}

func (f progressFixture) report(t *testing.T, actor domain.Actor, kind domain.PlaybackReportKind, key, item string, position time.Duration) {
	t.Helper()
	r := domain.PlaybackReport{Kind: kind, PlayKey: key, ItemID: item, PositionTicks: domain.DurationToTicks(position), PositionKnown: true}
	if err := f.progress.Report(f.ctx, actor, r); err != nil {
		t.Fatalf("%s %s: %v", kind, key, err)
	}
}

func (f progressFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(f.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f progressFixture) userData(t *testing.T, actor domain.Actor) (domain.UserItemData, bool) {
	t.Helper()
	data, err := f.s.UserItemData(f.ctx, actor.UserID, []string{f.item})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := data[f.item]
	return d, ok
}

// Start, progress, stop: the resume point follows the stop rules, the
// session row, its samples and the user data are written, repeated stops
// change nothing and a completed playback marks the item played.
func TestPlaybackProgressLifecyclePostgres(t *testing.T) {
	f := newProgressFixture(t)
	f.report(t, f.viewer, domain.PlaybackReportStart, "play-1", f.item, 0)
	var state, source string
	var runtime int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT state,source_id::text,runtime_ticks FROM playback_sessions WHERE user_id=$1::uuid AND play_key='play-1'`, f.viewer.UserID).Scan(&state, &source, &runtime); err != nil {
		t.Fatal(err)
	}
	if state != "active" || source != f.probed || runtime != f.runtime {
		t.Fatalf("started session %s %s %d", state, source, runtime)
	}
	for second := 1; second <= 120; second++ {
		f.clock.Advance(time.Second)
		f.report(t, f.viewer, domain.PlaybackReportProgress, "play-1", "", time.Duration(second)*time.Second)
	}
	if n := f.count(t, `SELECT count(*) FROM user_item_data`); n != 0 {
		t.Fatal("progress was written before a flush")
	}
	// Buffered positions are visible to the reader on this instance.
	if data, err := f.progress.UserItemData(f.ctx, f.viewer.UserID, []string{f.item}); err != nil || data[f.item].ResumeTicks != domain.DurationToTicks(2*time.Minute) {
		t.Fatalf("buffered user data %+v %v", data, err)
	}
	result, err := f.progress.Flush(f.ctx)
	if err != nil || result.Statements != 1 || result.Sessions != 1 || result.UserData != 1 || result.Samples < 2 {
		t.Fatalf("flush %+v %v", result, err)
	}
	f.clock.Advance(time.Second)
	f.report(t, f.viewer, domain.PlaybackReportStop, "play-1", "", 3600*time.Second)
	d, ok := f.userData(t, f.viewer)
	if !ok || d.ResumeTicks != 3600*domain.PlaybackTicksPerSecond || d.Played || d.PlayCount != 0 || d.LastPlayedAt == nil {
		t.Fatalf("resume point after stop %+v", d)
	}
	var reports, samples int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT state,report_count,sample_count FROM playback_sessions WHERE play_key='play-1'`).Scan(&state, &reports, &samples); err != nil {
		t.Fatal(err)
	}
	if state != "stopped" || reports != 121 || samples != f.count(t, `SELECT count(*) FROM playback_samples`) || samples < 3 || samples > 10 {
		t.Fatalf("stopped session state=%s reports=%d samples=%d", state, reports, samples)
	}
	// Repeated stop: accepted, nothing changes; progress cannot reopen it.
	f.report(t, f.viewer, domain.PlaybackReportStop, "play-1", f.item, 10*time.Second)
	if err := f.progress.Report(f.ctx, f.viewer, domain.PlaybackReport{Kind: domain.PlaybackReportProgress, PlayKey: "play-1", ItemID: f.item}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stopped session reopened", err)
	}
	if again, _ := f.userData(t, f.viewer); again.ResumeTicks != d.ResumeTicks {
		t.Fatal("repeated stop changed the resume point")
	}
	page, err := f.s.ListResume(f.ctx, f.viewer.UserID, domain.ResumeQuery{Limit: 10})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Item.ID != f.item || page.Items[0].RuntimeTicks != f.runtime || page.Items[0].Data.ResumeTicks != d.ResumeTicks {
		t.Fatalf("resume list %+v %v", page, err)
	}
	// A second playback to the credits completes the item.
	f.clock.Advance(time.Hour)
	f.report(t, f.viewer, domain.PlaybackReportStart, "play-2", f.item, 3600*time.Second)
	f.clock.Advance(time.Second)
	f.report(t, f.viewer, domain.PlaybackReportStop, "play-2", "", 7000*time.Second)
	if d, _ = f.userData(t, f.viewer); !d.Played || d.PlayCount != 1 || d.ResumeTicks != 0 {
		t.Fatalf("completed playback %+v", d)
	}
	if page, _ = f.s.ListResume(f.ctx, f.viewer.UserID, domain.ResumeQuery{Limit: 10}); page.Total != 0 {
		t.Fatal("played item still offered to resume")
	}
	// Explicit marks follow the upstream semantics.
	if d, err = f.s.SetPlayed(f.ctx, f.viewer.UserID, f.item, true, f.clock.Now()); err != nil || !d.Played || d.PlayCount != 2 {
		t.Fatalf("mark played %+v %v", d, err)
	}
	if d, err = f.s.SetPlayed(f.ctx, f.viewer.UserID, f.item, false, f.clock.Now()); err != nil || d.Played || d.PlayCount != 0 || d.ResumeTicks != 0 {
		t.Fatalf("mark unplayed %+v %v", d, err)
	}
	// Web sessions cannot report; the item check happens in storage too.
	web := imageRepositoryActor(t, f.jobFixture, "progress-web", access.ClientWeb)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, web.UserID, f.registration.Library.ID)
	if err := f.progress.Report(f.ctx, web, domain.PlaybackReport{Kind: domain.PlaybackReportStart, PlayKey: "w", ItemID: f.item}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("web session started playback", err)
	}
	if _, err := f.s.StartPlayback(f.ctx, domain.PlaybackStart{Actor: web, PlayKey: "w", ItemID: f.item, At: f.clock.Now()}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("storage accepted a web session", err)
	}
	// A version of another item is refused.
	if _, err := f.s.StartPlayback(f.ctx, domain.PlaybackStart{Actor: f.viewer, PlayKey: "x", ItemID: metadataItem(t, f.jobFixture), SourceID: f.probed, At: f.clock.Now()}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign version accepted", err)
	}
}

// Two devices of one user on one item in the same flush fold into one user
// data row; the latest report decides the resume point.
func TestPlaybackProgressFoldsDevicesPostgres(t *testing.T) {
	f := newProgressFixture(t)
	f.report(t, f.viewer, domain.PlaybackReportStart, "phone", f.item, 0)
	f.report(t, f.viewer, domain.PlaybackReportStart, "tv", f.item, 0)
	f.clock.Advance(time.Second)
	f.report(t, f.viewer, domain.PlaybackReportProgress, "phone", "", 10*time.Minute)
	f.clock.Advance(time.Second)
	f.report(t, f.viewer, domain.PlaybackReportProgress, "tv", "", 20*time.Minute)
	result, err := f.progress.Flush(f.ctx)
	if err != nil || result.Sessions != 2 || result.UserData != 1 {
		t.Fatalf("flush %+v %v", result, err)
	}
	if d, _ := f.userData(t, f.viewer); d.ResumeTicks != domain.DurationToTicks(20*time.Minute) {
		t.Fatalf("folded resume %+v", d)
	}
}

// G48.3: once the library grant is withdrawn the item has no user data and
// is not offered to continue watching; restoring the grant brings both back.
func TestPlaybackProgressHiddenAfterGrantWithdrawnPostgres(t *testing.T) {
	f := newProgressFixture(t)
	f.report(t, f.viewer, domain.PlaybackReportStart, "p", f.item, 0)
	f.clock.Advance(time.Minute)
	f.report(t, f.viewer, domain.PlaybackReportStop, "p", "", 20*time.Minute)
	if page, _ := f.s.ListResume(f.ctx, f.viewer.UserID, domain.ResumeQuery{Limit: 5}); page.Total != 1 {
		t.Fatal("resume fixture missing")
	}
	imageRepositoryExec(t, f.jobFixture, `DELETE FROM library_acl WHERE user_id=$1::uuid`, f.viewer.UserID)
	if _, ok := f.userData(t, f.viewer); ok {
		t.Fatal("invisible item has user data")
	}
	if page, err := f.s.ListResume(f.ctx, f.viewer.UserID, domain.ResumeQuery{Limit: 5}); err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatal("invisible item offered to resume", page.Total, err)
	}
	if _, err := f.s.SetPlayed(f.ctx, f.viewer.UserID, f.item, true, f.clock.Now()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("invisible item marked", err)
	}
	if err := f.progress.Report(f.ctx, f.viewer, domain.PlaybackReport{Kind: domain.PlaybackReportStart, PlayKey: "again", ItemID: f.item}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("invisible item started", err)
	}
	// The administrator still sees it; the viewer's rows are kept.
	if data, err := f.s.UserItemData(f.ctx, f.a.UserID, []string{f.item}); err != nil || len(data) != 1 {
		t.Fatal("administrator lost the item", err)
	}
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, f.viewer.UserID, f.registration.Library.ID)
	if d, ok := f.userData(t, f.viewer); !ok || d.ResumeTicks != domain.DurationToTicks(20*time.Minute) {
		t.Fatal("restored grant lost the resume point", d)
	}
}

// G07.7 and G23.4: deleting a user and clearing one's own history remove
// every playback row, and nothing buffered writes them back.
func TestPlaybackProgressDeleteUserAndClearHistoryPostgres(t *testing.T) {
	f := newProgressFixture(t)
	other := imageRepositoryActor(t, f.jobFixture, "progress-other", access.ClientNative)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, other.UserID, f.registration.Library.ID)
	for _, a := range []domain.Actor{f.viewer, other} {
		f.report(t, a, domain.PlaybackReportStart, "done", f.item, 0)
		f.clock.Advance(time.Second)
		f.report(t, a, domain.PlaybackReportStop, "done", "", 30*time.Minute)
		f.report(t, a, domain.PlaybackReportStart, "live", f.item, 30*time.Minute)
		f.clock.Advance(time.Second)
		f.report(t, a, domain.PlaybackReportProgress, "live", "", 31*time.Minute)
	}
	rows := func(user string) int {
		return f.count(t, `SELECT (SELECT count(*) FROM playback_sessions WHERE user_id=$1::uuid)+(SELECT count(*) FROM user_item_data WHERE user_id=$1::uuid)
 +(SELECT count(*) FROM playback_samples x JOIN playback_sessions p ON p.id=x.session_id WHERE p.user_id=$1::uuid)`, user)
	}
	if rows(f.viewer.UserID) < 4 || rows(other.UserID) < 4 {
		t.Fatal("fixture rows missing")
	}
	// Clear own history: the buffered live session is dropped first.
	if err := f.progress.ClearHistory(f.ctx, f.viewer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.progress.Flush(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := rows(f.viewer.UserID); n != 0 {
		t.Fatal("cleared history left rows", n)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE event='playback.history_cleared' AND target_id=$1::uuid AND after_state::jsonb->>'sessions'='2'`, f.viewer.UserID); n != 1 {
		t.Fatal("history clear not audited with counts", n)
	}
	// Delete the other user while its live session is still buffered.
	if err := f.s.DeleteUser(f.ctx, f.a, other.UserID); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Second)
	if _, err := f.progress.Flush(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := rows(other.UserID); n != 0 {
		t.Fatal("deleted user kept playback rows", n)
	}
	if _, err := f.s.RestoreUser(f.ctx, f.a, other.UserID); err != nil {
		t.Fatal(err)
	}
	if n := rows(other.UserID); n != 0 {
		t.Fatal("restore brought playback rows back", n)
	}
	// Hard deletion cascades too.
	f.report(t, f.viewer, domain.PlaybackReportStart, "hard", f.item, 0)
	f.clock.Advance(time.Second)
	f.report(t, f.viewer, domain.PlaybackReportStop, "hard", "", time.Hour)
	imageRepositoryExec(t, f.jobFixture, `DELETE FROM users WHERE id=$1::uuid`, f.viewer.UserID)
	if n := rows(f.viewer.UserID); n != 0 {
		t.Fatal("hard delete left rows", n)
	}
}

// Disconnected sessions time out on this instance; sessions of a stopped
// instance are closed by the storage sweep, and history past the retention
// period is purged.
func TestPlaybackProgressTimeoutSweepAndRetentionPostgres(t *testing.T) {
	f := newProgressFixture(t)
	f.report(t, f.viewer, domain.PlaybackReportStart, "silent", f.item, 0)
	f.clock.Advance(time.Second)
	f.report(t, f.viewer, domain.PlaybackReportProgress, "silent", "", 7100*time.Second)
	f.clock.Advance(2 * time.Minute)
	if _, err := f.progress.Flush(f.ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT state FROM playback_sessions WHERE play_key='silent'`).Scan(&state); err != nil || state != "timed_out" {
		t.Fatal("silent session state", state, err)
	}
	if d, _ := f.userData(t, f.viewer); !d.Played || d.PlayCount != 1 {
		t.Fatalf("timed out at the credits %+v", d)
	}
	// Timed out sessions rejoin when the client comes back.
	f.report(t, f.viewer, domain.PlaybackReportProgress, "silent", f.item, 100*time.Second)
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT state FROM playback_sessions WHERE play_key='silent'`).Scan(&state); err != nil || state != "active" {
		t.Fatal("timed out session did not rejoin", state, err)
	}
	// An orphan left by another instance.
	orphan, err := app.NewProgress(f.s, f.s, app.ProgressOptions{Clock: f.clock, SessionTimeout: time.Minute, FlushInterval: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err = orphan.Report(f.ctx, f.viewer, domain.PlaybackReport{Kind: domain.PlaybackReportStart, PlayKey: "orphan", ItemID: f.item, PositionTicks: 600 * domain.PlaybackTicksPerSecond, PositionKnown: true}); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(5 * time.Minute)
	if err = f.progress.Maintain(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT state FROM playback_sessions WHERE play_key='orphan'`).Scan(&state); err != nil || state != "timed_out" {
		t.Fatal("orphan state", state, err)
	}
	// Retention: ended sessions older than 30 days go, with their samples.
	imageRepositoryExec(t, f.jobFixture, `UPDATE playback_sessions SET ended_at=$1::timestamptz,started_at=$1::timestamptz,last_report_at=$1::timestamptz WHERE play_key='orphan'`, f.clock.Now().Add(-40*24*time.Hour))
	purged, err := f.s.PurgePlaybackHistory(f.ctx, f.clock.Now().Add(-30*24*time.Hour), 10)
	if err != nil || purged != 1 || f.count(t, `SELECT count(*) FROM playback_sessions WHERE play_key='orphan'`) != 0 {
		t.Fatal("retention purge", purged, err)
	}
	if f.count(t, `SELECT count(*) FROM playback_samples x LEFT JOIN playback_sessions p ON p.id=x.session_id WHERE p.id IS NULL`) != 0 {
		t.Fatal("purge left samples")
	}
	// Administrators list running sessions; viewers are refused.
	sessions, err := f.progress.ActiveSessions(f.ctx, f.a)
	if err != nil || len(sessions) != 1 || sessions[0].ItemID != f.item || sessions[0].Delivery != "direct" || sessions[0].PositionTicks != 100*domain.PlaybackTicksPerSecond {
		t.Fatalf("active sessions %+v %v", sessions, err)
	}
	if _, err = f.s.ListActivePlayback(f.ctx, f.viewer, 10); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer listed sessions", err)
	}
}

// Storage constraints hold even for writers that bypass the service.
func TestPlaybackProgressConstraintsPostgres(t *testing.T) {
	f := newProgressFixture(t)
	f.report(t, f.viewer, domain.PlaybackReportStart, "c", f.item, 0)
	for _, statement := range []string{
		`UPDATE playback_sessions SET state='stopped'`,
		`UPDATE playback_sessions SET delivery='remux'`,
		`UPDATE playback_sessions SET state='failed',ended_at=now()`,
		`UPDATE playback_sessions SET failure_reason='other'`,
		`UPDATE playback_sessions SET position_ticks=-1`,
		`UPDATE playback_sessions SET play_key='a b'`,
		`INSERT INTO playback_samples(session_id,seq,at,kind,position_ticks) SELECT id,4096,now(),'progress',0 FROM playback_sessions`,
		`INSERT INTO playback_samples(session_id,seq,at,kind,position_ticks) SELECT id,1,now(),'jump',0 FROM playback_sessions`,
		`INSERT INTO user_item_data(user_id,item_id,resume_ticks,updated_at) VALUES('` + f.viewer.UserID + `','` + f.item + `',-1,now())`,
	} {
		if _, err := f.s.Pool.Exec(f.ctx, statement); err == nil {
			t.Errorf("accepted: %s", statement)
		}
	}
}

func TestPlaybackProgressMigrationRoundTrip(t *testing.T) {
	f := newProgressFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	want := downgradeAboveMigration(t, f.jobFixture, "playback_progress")
	f.report(t, f.viewer, domain.PlaybackReportStart, "m", f.item, 0)
	if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
		t.Fatal("retained playback history downgraded")
	}
	version, dirty, err := Migrate(f.ctx, dsn, "status")
	if err != nil || version != want-1 || !dirty {
		t.Fatal("refused downgrade state", version, dirty, err)
	}
	if f.count(t, `SELECT count(*) FROM playback_sessions`) != 1 {
		t.Fatal("refused downgrade removed history")
	}
	// Recover the refused step, clear the history, then go down and up.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=false`, want); err != nil {
		t.Fatal(err)
	}
	if err = f.progress.ClearHistory(f.ctx, f.viewer); err != nil {
		t.Fatal(err)
	}
	if version, dirty, err = Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if f.count(t, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('playback_sessions','playback_samples','user_item_data')`) != 0 {
		t.Fatal("downgrade left playback tables")
	}
	if n := f.count(t, `SELECT count(*) FROM audit_logs WHERE event='playback.history_cleared'`); n != 1 {
		t.Fatal("downgrade removed audit history", n)
	}
	if version, dirty, err = Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if err = f.s.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.report(t, f.viewer, domain.PlaybackReportStart, "after", f.item, 0)
	f.clock.Advance(time.Second)
	f.report(t, f.viewer, domain.PlaybackReportStop, "after", "", 20*time.Minute)
	if d, ok := f.userData(t, f.viewer); !ok || d.ResumeTicks != domain.DurationToTicks(20*time.Minute) {
		t.Fatal("progress after round trip", d)
	}
}

// keys used by the write amplification test.
func progressKey(i int) string { return fmt.Sprintf("load-%03d", i) }
