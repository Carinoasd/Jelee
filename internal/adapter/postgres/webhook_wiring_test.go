package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Stage 11 C2: the G12.1 events that had no production producer. Every test
// here enables events, subscribes an endpoint to everything and checks the
// rows the change wrote to webhook_outbox.

// assertWebhookPayloads fails when a stored payload carries a secret,
// identifier of the device or connection, account name or local path, or
// would not pass the envelope validation the deliverer applies.
func assertWebhookPayloads(t *testing.T, rows []outboxRow, forbidden ...string) {
	t.Helper()
	for _, r := range rows {
		raw, _ := json.Marshal(r.data)
		for _, word := range append(forbidden, "127.0.0.1", "token", "secret", "deviceId", "contract-test") {
			if word != "" && bytes.Contains(raw, []byte(word)) {
				t.Fatalf("%s payload leaks %q: %s", r.eventType, word, raw)
			}
		}
		event := domain.WebhookEvent{EventID: r.eventID, Type: domain.WebhookEventType(r.eventType), Version: domain.WebhookEventVersion, OccurredAt: time.Now(),
			Subject: domain.WebhookSubject{Kind: domain.WebhookSubjectKind(r.subjectKind), ID: r.subjectID}, Data: r.data}
		if err := event.Validate(); err != nil {
			t.Fatalf("stored %s event invalid: %v", r.eventType, err)
		}
	}
}

func TestWebhookWiredPlaybackPausedAndProgressPostgres(t *testing.T) {
	f := newProgressFixture(t)
	var err error
	f.progress, err = app.NewProgress(f.s, f.s, app.ProgressOptions{Clock: f.clock, FlushInterval: 10 * time.Second, SessionTimeout: time.Minute, ProgressEventInterval: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.s.EnableWebhookEvents(true)
	createWebhook(t, f.jobFixture, nil)
	f.report(t, f.viewer, domain.PlaybackReportStart, "hook-pause", f.item, 0)
	// A minute of playing with a flush every 10 s: two progress events.
	for second := 10; second <= 60; second += 10 {
		f.clock.Advance(10 * time.Second)
		f.report(t, f.viewer, domain.PlaybackReportProgress, "hook-pause", "", time.Duration(second)*time.Second)
		if _, err = f.progress.Flush(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	f.clock.Advance(5 * time.Second)
	paused := domain.PlaybackReport{Kind: domain.PlaybackReportProgress, PlayKey: "hook-pause", PositionTicks: domain.DurationToTicks(65 * time.Second), PositionKnown: true, Paused: true}
	if err = f.progress.Report(f.ctx, f.viewer, paused); err != nil {
		t.Fatal(err)
	}
	if _, err = f.progress.Flush(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Second)
	f.report(t, f.viewer, domain.PlaybackReportStop, "hook-pause", "", 66*time.Second)
	var session string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM playback_sessions WHERE play_key='hook-pause'`).Scan(&session); err != nil {
		t.Fatal(err)
	}
	progress, pausedRows := outboxRows(t, f.jobFixture, "playback.progress"), outboxRows(t, f.jobFixture, "playback.paused")
	if len(progress) != 2 || len(pausedRows) != 1 {
		t.Fatalf("progress=%+v paused=%+v", progress, pausedRows)
	}
	if r := progress[1]; r.subjectKind != "session" || r.subjectID != session || r.data["userId"] != f.viewer.UserID || r.data["itemId"] != f.item ||
		r.data["positionTicks"] != float64(domain.DurationToTicks(60*time.Second)) || r.data["paused"] != false {
		t.Fatalf("progress %+v", r)
	}
	if r := pausedRows[0]; r.subjectID != session || r.data["positionTicks"] != float64(domain.DurationToTicks(65*time.Second)) || r.data["itemId"] != f.item {
		t.Fatalf("paused %+v", r)
	}
	if len(outboxRows(t, f.jobFixture, "playback.stopped")) != 1 {
		t.Fatal("stop event missing")
	}
	assertWebhookPayloads(t, outboxRows(t, f.jobFixture, "playback.%"), f.root, f.viewer.SessionID)
	// A session that ended elsewhere raises nothing for the marks of a
	// late flush.
	before := len(outboxRows(t, f.jobFixture, "playback.%"))
	if _, err = f.s.FlushPlayback(f.ctx, []domain.PlaybackFlush{{SessionID: session, LastReportAt: f.clock.Now(), Progress: true,
		Pause: &domain.PlaybackPause{At: f.clock.Now(), PositionTicks: 1}}}); err != nil {
		t.Fatal(err)
	}
	if after := len(outboxRows(t, f.jobFixture, "playback.%")); after != before {
		t.Fatalf("ended session raised %d events", after-before)
	}
}

func TestWebhookWiredSessionEventsPostgres(t *testing.T) {
	f := newJobFixture(t)
	created := createAccount(t, f.ctx, f.s, f.a, "hook-sessions")
	// Disabled: neither a login nor a revocation names the outbox.
	quiet := accountLogin(t, f.ctx, f.s, created.Name)
	createWebhook(t, f, nil)
	if err := f.s.RevokeSession(f.ctx, accountActor(quiet), created.ID, quiet.Session.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(outboxRows(t, f, "session.%")); n != 0 {
		t.Fatalf("disabled store raised %d session events", n)
	}
	f.s.EnableWebhookEvents(true)
	web := accountLogin(t, f.ctx, f.s, created.Name)
	other := accountLogin(t, f.ctx, f.s, created.Name)
	rows := outboxRows(t, f, "session.created")
	if len(rows) != 2 || rows[0].subjectKind != "session" || rows[0].subjectID != web.Session.ID || rows[0].data["userId"] != created.ID ||
		rows[0].data["clientKind"] != "web" || rows[0].data["reason"] != "login" || rows[0].data["expiresAt"] == nil {
		t.Fatalf("session.created %+v", rows)
	}
	// Rotation ends the old session and creates its replacement.
	rotated, err := f.s.RotateSession(f.ctx, accountActor(web), "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Logout of the rotated session, then an administrator revokes the rest.
	if err = f.s.RevokeSession(f.ctx, accountActor(rotated), created.ID, rotated.Session.ID); err != nil {
		t.Fatal(err)
	}
	// An already revoked session ends nothing again.
	if err = f.s.RevokeSessions(f.ctx, f.a, created.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.RevokeSessions(f.ctx, f.a, created.ID); err != nil {
		t.Fatal(err)
	}
	ended := outboxRows(t, f, "session.ended")
	reasons := map[string]string{}
	for _, r := range ended {
		reasons[r.subjectID] = r.data["reason"].(string)
	}
	if len(ended) != 3 || reasons[web.Session.ID] != "rotated" || reasons[rotated.Session.ID] != "logout" || reasons[other.Session.ID] != "revoked" {
		t.Fatalf("session.ended %+v", ended)
	}
	if created := outboxRows(t, f, "session.created"); len(created) != 3 || created[2].subjectID != rotated.Session.ID || created[2].data["reason"] != "rotated" {
		t.Fatalf("rotation session.created %+v", created)
	}
	assertWebhookPayloads(t, outboxRows(t, f, "session.%"), created.Name, web.Token, other.Token)
}

func TestWebhookWiredSessionExpiryPostgres(t *testing.T) {
	f := newJobFixture(t)
	created := createAccount(t, f.ctx, f.s, f.a, "hook-expiry")
	createWebhook(t, f, nil, domain.WebhookSessionEnded)
	f.s.EnableWebhookEvents(true)
	// Sessions that expired before the watermark (the upgrade) raise
	// nothing; so does one revoked before it expired.
	old := accountLogin(t, f.ctx, f.s, created.Name)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE sessions SET expires_at=now()-interval '1 hour' WHERE id=$1::uuid`, old.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE session_expiry_watermark SET through_at=now()-interval '1 minute',through_id=NULL`); err != nil {
		t.Fatal(err)
	}
	revoked := accountLogin(t, f.ctx, f.s, created.Name)
	if err := f.s.RevokeSessions(f.ctx, f.a, created.ID); err != nil {
		t.Fatal(err)
	}
	var expired []domain.SessionGrant
	for range 3 {
		expired = append(expired, accountLogin(t, f.ctx, f.s, created.Name))
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE sessions SET expires_at=now()-interval '1 second' WHERE id=ANY($1::uuid[])`,
		[]string{expired[0].Session.ID, expired[1].Session.ID, expired[2].Session.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=now()-interval '10 seconds',expires_at=now()-interval '1 second' WHERE id=$1::uuid`, revoked.Session.ID); err != nil {
		t.Fatal(err)
	}
	before := len(outboxRows(t, f, "session.ended"))
	// Pages of two: the first is full, the second short.
	if n, err := f.s.RaiseExpiredSessions(f.ctx, 2); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if n, err := f.s.RaiseExpiredSessions(f.ctx, 2); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := f.s.RaiseExpiredSessions(f.ctx, 2); err != nil || n != 0 {
		t.Fatal("expiry raised twice", n, err)
	}
	rows := outboxRows(t, f, "session.ended")[before:]
	if len(rows) != 3 {
		t.Fatalf("expired session.ended %+v", rows)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.data["reason"] != "expired" || r.data["userId"] != created.ID {
			t.Fatalf("expiry event %+v", r)
		}
		seen[r.subjectID] = true
	}
	for _, g := range expired {
		if !seen[g.Session.ID] {
			t.Fatalf("expired session %s not raised", g.Session.ID)
		}
	}
	// Revoking an expired session raises nothing more.
	if err := f.s.RevokeSessions(f.ctx, f.a, created.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(outboxRows(t, f, "session.ended")); n != before+3 {
		t.Fatalf("revoking expired sessions raised %d", n-before-3)
	}
	// With events off the sweep does nothing.
	f.s.EnableWebhookEvents(false)
	if n, err := f.s.RaiseExpiredSessions(f.ctx, 2); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	assertWebhookPayloads(t, rows, created.Name)
}

func TestWebhookWiredScanStartedPostgres(t *testing.T) {
	f := newJobFixture(t)
	f.s.EnableWebhookEvents(true)
	createWebhook(t, f, nil, domain.WebhookScanStarted)
	job := f.submit(t, "hook-start")
	lease := f.claim(t, "hook-worker")
	if err := f.s.PauseJob(f.ctx, lease); err != nil {
		t.Fatal(err)
	}
	// Resuming the paused scan claims it again without a second event.
	f.claim(t, "hook-worker")
	started := outboxRows(t, f, "scan.started")
	if len(started) != 1 || started[0].subjectKind != "library" || started[0].subjectID != f.registration.Library.ID ||
		started[0].data["jobId"] != job.ID || started[0].data["priority"] != domain.JobPriorityManual {
		t.Fatalf("scan.started %+v", started)
	}
	assertWebhookPayloads(t, started)
}

func TestWebhookWiredMediaUpdatedPostgres(t *testing.T) {
	f := newJobFixtureWithin(t, 3*time.Minute)
	root := syncRootPath(t, f)
	writeSyncFiles(t, root, "Heat (1995).mp4")
	enableAutoSync(t, f)
	stop := startSyncWorker(t, f, 1)
	defer stop()
	f.s.EnableWebhookEvents(true)
	createWebhook(t, f, nil, domain.WebhookMediaAdded, domain.WebhookMediaUpdated)
	f.submit(t, "hook-media-1")
	waitJobsIdle(t, f)
	added := outboxRows(t, f, "media.added")
	if len(added) != 1 || len(outboxRows(t, f, "media.updated")) != 0 {
		t.Fatalf("first sync added=%+v", added)
	}
	heat := added[0].subjectID
	// A second version joins the item; the first file changes.
	writeSyncFiles(t, root, "Heat (1995).mkv")
	if err := os.WriteFile(filepath.Join(root, "Heat (1995).mp4"), []byte("media:changed and longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "hook-media-2")
	waitJobsIdle(t, f)
	updated := outboxRows(t, f, "media.updated")
	reasons := map[string]int{}
	for _, r := range updated {
		if r.subjectKind != "item" || r.subjectID != heat || r.data["libraryId"] != f.registration.Library.ID || r.data["sourceId"] == nil {
			t.Fatalf("media.updated %+v", r)
		}
		reasons[r.data["reason"].(string)]++
	}
	if len(updated) != 2 || reasons["source_added"] != 1 || reasons["source_changed"] != 1 || len(outboxRows(t, f, "media.added")) != 1 {
		t.Fatalf("media.updated %+v", updated)
	}
	// An unchanged rescan updates nothing.
	f.submit(t, "hook-media-3")
	waitJobsIdle(t, f)
	if n := len(outboxRows(t, f, "media.updated")); n != 2 {
		t.Fatalf("unchanged rescan raised %d media.updated", n-2)
	}
	assertWebhookPayloads(t, outboxRows(t, f, "media.%"), root)
}

func TestWebhookWiredImagesFetchedPostgres(t *testing.T) {
	w := newRefreshWorld(t)
	fetchedItem, _ := w.remote(t, "ok-hook.png", false, false)
	failedItem, _ := w.remote(t, "missing.png", false, false)
	w.s.EnableWebhookEvents(true)
	createWebhook(t, w.jobFixture, nil, domain.WebhookImagesFetched)
	stop := w.start(t)
	job := w.wait(t, w.submit(t, "hook-refresh").ID)
	stop()
	if job.State != domain.JobSucceeded {
		t.Fatalf("refresh %+v", job)
	}
	rows := outboxRows(t, w.jobFixture, "images.fetched")
	if len(rows) != 1 || rows[0].subjectKind != "item" || rows[0].subjectID != fetchedItem || rows[0].data["jobId"] != job.ID ||
		rows[0].data["libraryId"] != w.registration.Library.ID || rows[0].data["images"] != float64(1) {
		t.Fatalf("images.fetched %+v (failed item %s)", rows, failedItem)
	}
	assertWebhookPayloads(t, rows, "art.example")
}

func TestWebhookWiredSystemAlertPostgres(t *testing.T) {
	f := newJobFixture(t)
	f.s.EnableWebhookEvents(true)
	createWebhook(t, f, nil, domain.WebhookSystemAlert)
	alerts := app.NewSystemAlerts(0, nil)
	alerts.Raise(app.SystemAlert{Code: "probe_runtime_unavailable", Component: "probe", Severity: "warning"})
	producers, err := app.NewWebhookProducers(f.s, alerts, f.s, app.WebhookProducerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		producers.Run(ctx)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(outboxRows(t, f, "system.alert")) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	rows := outboxRows(t, f, "system.alert")
	if len(rows) != 1 || rows[0].subjectKind != "system" || rows[0].subjectID != "" || rows[0].data["code"] != "probe_runtime_unavailable" || rows[0].data["severity"] != "warning" {
		t.Fatalf("system.alert %+v", rows)
	}
	assertWebhookPayloads(t, rows)
}

func TestWebhookWiredSessionExpiryMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	want := downgradeAboveMigration(t, f, "session_expiry_events")
	version, dirty, err := Migrate(f.ctx, dsn, "down")
	if err != nil || dirty || version >= want {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='session_expiry_watermark'`) != 0 ||
		syncCount(t, f, `SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname='sessions_expiry_idx'`) != 0 {
		t.Fatal("downgrade left the expiry watermark")
	}
	if version, dirty, err = Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	// The upgrade starts the watermark now: nothing that expired before it
	// is raised.
	if syncCount(t, f, `SELECT count(*) FROM session_expiry_watermark WHERE through_at<=now() AND through_id IS NULL`) != 1 {
		t.Fatal("upgrade did not start the watermark")
	}
	f.s.EnableWebhookEvents(true)
	createWebhook(t, f, nil, domain.WebhookSessionEnded)
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET expires_at=now()-interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if n, err := f.s.RaiseExpiredSessions(f.ctx, 10); err != nil || n != 0 {
		t.Fatalf("sessions expired before the upgrade raised %d: %v", n, err)
	}
}
