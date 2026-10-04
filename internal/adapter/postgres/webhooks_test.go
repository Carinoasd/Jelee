package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/secretbox"
)

type webhookTarget struct{}

func (webhookTarget) CheckWebhookTarget(raw string) error {
	if !strings.HasPrefix(raw, "https://") {
		return domain.ErrWebhookTargetDenied
	}
	return nil
}

type webhookNoDeliverer struct{}

func (webhookNoDeliverer) Deliver(context.Context, app.WebhookRequest) (domain.WebhookOutcome, error) {
	return domain.WebhookOutcome{}, errors.New("not used")
}

func webhookBox(t *testing.T, fill byte) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{fill}, secretbox.KeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// createWebhook configures an endpoint through the administration service
// and returns its id and signing secret.
func createWebhook(t *testing.T, f jobFixture, retry *app.WebhookRetryView, events ...domain.WebhookEventType) (string, string) {
	t.Helper()
	service, err := app.NewWebhooks(f.s, webhookBox(t, 7), webhookTarget{}, webhookNoDeliverer{}, app.WebhookOptions{})
	if err != nil {
		t.Fatal(err)
	}
	view, secret, err := service.Create(f.ctx, f.a, app.WebhookEndpointInput{Name: "test hook", URL: "https://hooks.example/jelee", Enabled: true, Events: events,
		Headers: map[string]string{"Authorization": "Bearer consumer-token-Qx1"}, Retry: retry})
	if err != nil {
		t.Fatal(err)
	}
	return view.ID, secret
}

type outboxRow struct {
	eventID, eventType, subjectKind, subjectID string
	data                                       map[string]any
}

func outboxRows(t *testing.T, f jobFixture, eventType string) []outboxRow {
	t.Helper()
	rows, err := f.s.Pool.Query(f.ctx, `SELECT event_id,event_type,subject_kind,COALESCE(subject_id,''),COALESCE(data::text,'{}') FROM webhook_outbox WHERE event_type LIKE $1 ORDER BY id`, eventType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		var raw string
		if err = rows.Scan(&r.eventID, &r.eventType, &r.subjectKind, &r.subjectID, &raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(raw), &r.data); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// Events are appended in the transaction of the change, only while events
// are enabled and an enabled endpoint subscribes, and roll back with it.
func TestWebhookOutboxSameTransactionPostgres(t *testing.T) {
	f := newJobFixture(t)
	created := createAccount(t, f.ctx, f.s, f.a, "hook-user")
	fail := func() {
		t.Helper()
		c, err := f.s.Credentials(f.ctx, created.Name)
		if err != nil {
			t.Fatal(err)
		}
		in := accountLoginInput(c, false)
		in.LockAfter = 2
		if _, err = f.s.CommitLogin(f.ctx, in); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatal(err)
		}
	}
	// Events disabled: nothing is written, even with a subscriber.
	createWebhook(t, f, nil, domain.WebhookUserLoginFailed, domain.WebhookUserLocked)
	fail()
	if n := len(outboxRows(t, f, "%")); n != 0 {
		t.Fatalf("disabled store appended %d events", n)
	}
	f.s.EnableWebhookEvents(true)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET failed_login=0,locked_until=NULL`); err != nil {
		t.Fatal(err)
	}
	fail()
	fail()
	failed, locked := outboxRows(t, f, "user.login_failed"), outboxRows(t, f, "user.locked")
	if len(failed) != 2 || len(locked) != 1 || failed[0].subjectKind != "user" || failed[0].subjectID != created.ID || locked[0].data["failedLogins"] != float64(2) {
		t.Fatalf("failed=%+v locked=%+v", failed, locked)
	}
	for _, r := range append(failed, locked...) {
		raw, _ := json.Marshal(r.data)
		if bytes.Contains(raw, []byte("127.0.0.1")) || bytes.Contains(raw, []byte(created.Name)) {
			t.Fatalf("event data leaks the address or name: %s", raw)
		}
	}
	// No subscriber for user.login: a successful login appends nothing.
	accountLogin(t, f.ctx, f.s, "job-admin")
	if n := len(outboxRows(t, f, "user.login")); n != 0 {
		t.Fatalf("unsubscribed event appended %d", n)
	}
	// The event and the change commit or roll back together.
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = appendWebhook(f.ctx, tx, true, domain.WebhookUserLocked, time.Now(), domain.WebhookSubject{Kind: domain.WebhookSubjectUser, ID: created.ID}, map[string]any{"marker": "rolled-back"}); err != nil {
		t.Fatal(err)
	}
	var inside int
	if err = tx.QueryRow(f.ctx, `SELECT count(*) FROM webhook_outbox WHERE data->>'marker'='rolled-back'`).Scan(&inside); err != nil || inside != 1 {
		t.Fatal("event not visible inside its transaction", err)
	}
	if err = tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(outboxRows(t, f, "user.locked")); n != 1 {
		t.Fatalf("rolled back event survived: %d", n)
	}
	// Redaction happens before the row is written.
	if err = f.s.Append(f.ctx, domain.WebhookEvent{EventID: "x1", Type: domain.WebhookUserLocked, Version: 1, OccurredAt: time.Now(),
		Subject: domain.WebhookSubject{Kind: domain.WebhookSubjectUser, ID: created.ID}, Data: map[string]any{"token": "abc"}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unredacted event appended: %v", err)
	}
	redacted, err := domain.NewWebhookEvent("x2", domain.WebhookUserLocked, time.Now(), domain.WebhookSubject{Kind: domain.WebhookSubjectUser, ID: created.ID},
		map[string]any{"token": "abc", "path": "/srv/media/Movies/Heat.mkv", "note": "see https://u:p@host/x"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Append(f.ctx, redacted); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT data::text FROM webhook_outbox WHERE event_id='x2'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "abc") || strings.Contains(stored, "/srv") || strings.Contains(stored, "u:p@") || !strings.Contains(stored, "Heat.mkv") {
		t.Fatalf("stored data %s", stored)
	}
}

// The delivery store end to end: fan-out once, leases, at least once after
// a crash between delivery and recording, retries, dead letter, replay with
// the same eventId, the attempt log and purging.
func TestWebhookDeliveryLifecyclePostgres(t *testing.T) {
	f := newJobFixture(t)
	f.s.EnableWebhookEvents(true)
	hook, _ := createWebhook(t, f, &app.WebhookRetryView{MaxAttempts: 2, BaseDelaySeconds: 10, MaxDelaySeconds: 60}, domain.WebhookScanCompleted)
	other, _ := createWebhook(t, f, nil, domain.WebhookMediaAdded)
	event, err := domain.NewWebhookEvent("evt-life", domain.WebhookScanCompleted, time.Now(), domain.WebhookSubject{Kind: domain.WebhookSubjectLibrary, ID: f.registration.Library.ID}, map[string]any{"missing": 0})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Append(f.ctx, event); err != nil {
		t.Fatal(err)
	}
	unplanned, err := f.s.FetchUnplanned(f.ctx, 10)
	if err != nil || len(unplanned) != 1 || unplanned[0].EventID != "evt-life" || unplanned[0].Validate() != nil {
		t.Fatalf("unplanned %+v %v", unplanned, err)
	}
	endpoints, err := f.s.ListDeliveryEndpoints(f.ctx)
	if err != nil || len(endpoints) != 2 {
		t.Fatal(endpoints, err)
	}
	targets := app.PlanWebhookFanout(unplanned[0], endpoints)
	if len(targets) != 1 || targets[0] != hook {
		t.Fatalf("fanout %v", targets)
	}
	for i := 0; i < 2; i++ {
		if err = f.s.CreateDeliveries(f.ctx, unplanned[0], targets, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if n := syncCount(t, f, `SELECT count(*) FROM webhook_deliveries`); n != 1 {
		t.Fatalf("deliveries %d", n)
	}
	if again, _ := f.s.FetchUnplanned(f.ctx, 10); len(again) != 0 {
		t.Fatal("planned event fetched again")
	}
	now := time.Now().Add(time.Second)
	lease := 2 * time.Minute
	claimed, err := f.s.ClaimDue(f.ctx, now, now.Add(lease), 10)
	if err != nil || len(claimed) != 1 || claimed[0].EndpointID != hook || claimed[0].Event.EventID != "evt-life" || claimed[0].Attempts != 0 || claimed[0].Event.Validate() != nil {
		t.Fatalf("claim %+v %v", claimed, err)
	}
	first := claimed[0]
	if again, _ := f.s.ClaimDue(f.ctx, now.Add(time.Minute), now.Add(time.Minute+lease), 10); len(again) != 0 {
		t.Fatal("leased delivery claimed twice")
	}
	// The deliverer crashed after sending: nothing was recorded. After the
	// lease the same delivery, with the same eventId, is claimed again.
	now = now.Add(lease + time.Second)
	claimed, err = f.s.ClaimDue(f.ctx, now, now.Add(lease), 10)
	if err != nil || len(claimed) != 1 || claimed[0].ID != first.ID || claimed[0].Event.EventID != first.Event.EventID || claimed[0].LeaseToken == first.LeaseToken || claimed[0].Attempts != 0 {
		t.Fatalf("reclaim %+v %v", claimed, err)
	}
	second := claimed[0]
	policy := endpoints[0].Retry
	for _, e := range endpoints {
		if e.ID == hook {
			policy = e.Retry
		}
	}
	failure := domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: 502}
	if err = f.s.RecordAttempt(f.ctx, app.ResolveWebhookAttempt(first, policy, failure, now, now, nil)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expired lease recorded: %v", err)
	}
	record := app.ResolveWebhookAttempt(second, policy, failure, now, now.Add(time.Second), nil)
	if record.Decision.State != domain.WebhookDeliveryPending {
		t.Fatal(record)
	}
	if err = f.s.RecordAttempt(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	if err = f.s.RecordAttempt(f.ctx, record); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("attempt recorded twice", err)
	}
	if early, _ := f.s.ClaimDue(f.ctx, record.Decision.NextAt.Add(-time.Second), record.Decision.NextAt.Add(lease), 10); len(early) != 0 {
		t.Fatal("claimed before its retry time")
	}
	now = record.Decision.NextAt
	claimed, err = f.s.ClaimDue(f.ctx, now, now.Add(lease), 10)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("retry claim %+v %v", claimed, err)
	}
	record = app.ResolveWebhookAttempt(claimed[0], policy, failure, now, now, nil)
	if record.Decision.State != domain.WebhookDeliveryDead {
		t.Fatal("not dead after max attempts", record.Decision)
	}
	if err = f.s.RecordAttempt(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.s.ClaimDue(f.ctx, now.Add(time.Hour), now.Add(2*time.Hour), 10); len(again) != 0 {
		t.Fatal("dead delivery claimed")
	}
	dead, err := f.s.ListWebhookDeliveries(f.ctx, f.a, hook, domain.WebhookDeliveryDead, "", 10)
	if err != nil || len(dead) != 1 || dead[0].Attempts != 2 || dead[0].LastOutcome != domain.WebhookOutcomeHTTP || dead[0].LastStatus != 502 || dead[0].EventID != "evt-life" {
		t.Fatalf("dead letters %+v %v", dead, err)
	}
	// Purge keeps nothing pending but deletes settled history.
	if n, err := f.s.PurgeWebhookHistory(f.ctx, time.Now().Add(-time.Hour), 100); err != nil || n != 0 {
		t.Fatalf("purged recent history %d %v", n, err)
	}
	// Manual replay: same eventId, fresh budget, a new round in the log.
	replayed, err := f.s.ReplayWebhookDelivery(f.ctx, f.a, hook, first.ID, now)
	if err != nil || replayed.State != domain.WebhookDeliveryPending || replayed.Attempts != 0 || replayed.Replays != 1 || replayed.EventID != "evt-life" {
		t.Fatalf("replay %+v %v", replayed, err)
	}
	if _, err = f.s.ReplayWebhookDelivery(f.ctx, f.a, hook, first.ID, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("pending delivery replayed", err)
	}
	if _, err = f.s.ReplayWebhookDelivery(f.ctx, f.a, other, first.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("delivery replayed through another endpoint", err)
	}
	claimed, err = f.s.ClaimDue(f.ctx, now, now.Add(lease), 10)
	if err != nil || len(claimed) != 1 || claimed[0].Event.EventID != "evt-life" || claimed[0].Attempts != 0 {
		t.Fatalf("replay claim %+v %v", claimed, err)
	}
	if err = f.s.RecordAttempt(f.ctx, app.ResolveWebhookAttempt(claimed[0], policy, domain.WebhookOutcome{Kind: domain.WebhookOutcomeDelivered, StatusCode: 200}, now, now, nil)); err != nil {
		t.Fatal(err)
	}
	detail, err := f.s.GetWebhookDelivery(f.ctx, f.a, hook, first.ID)
	if err != nil || detail.State != domain.WebhookDeliveryDelivered || len(detail.History) != 3 {
		t.Fatalf("detail %+v %v", detail, err)
	}
	if h := detail.History; h[0].Round != 2 || h[0].Attempt != 1 || h[0].Outcome != domain.WebhookOutcomeDelivered || h[1].Round != 1 || h[1].Attempt != 2 || h[2].NextAttempt == nil || h[2].StatusCode != 502 {
		t.Fatalf("history %+v", h)
	}
	// The viewer cannot read the log; audit recorded the replay.
	viewer := imageRepositoryActor(t, f, "hook-viewer", access.ClientNative)
	if _, err = f.s.ListWebhookDeliveries(f.ctx, viewer, hook, "", "", 10); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer read the delivery log", err)
	}
	if n := syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='webhook.delivery_replayed' AND target_id=$1::uuid`, first.ID); n != 1 {
		t.Fatal("replay not audited", n)
	}
	// Disabled endpoints keep their pending deliveries without claiming.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE webhooks SET enabled=false WHERE id=$1::uuid`, hook); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ReplayWebhookDelivery(f.ctx, f.a, hook, first.ID, now); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.s.ClaimDue(f.ctx, now.Add(time.Minute), now.Add(time.Hour), 10); len(again) != 0 {
		t.Fatal("disabled endpoint claimed")
	}
	// An event no enabled endpoint takes is dropped at fan-out.
	orphan, _ := domain.NewWebhookEvent("evt-orphan", domain.WebhookMediaAdded, time.Now(), domain.WebhookSubject{Kind: domain.WebhookSubjectItem, ID: "i"}, nil)
	if err = f.s.Append(f.ctx, orphan); err != nil {
		t.Fatal(err)
	}
	if err = f.s.CreateDeliveries(f.ctx, orphan, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := syncCount(t, f, `SELECT count(*) FROM webhook_outbox WHERE event_id='evt-orphan'`); n != 0 {
		t.Fatal("orphan event kept")
	}
	// Settled history older than the cutoff is purged with its log.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE webhooks SET enabled=true WHERE id=$1::uuid`, hook); err != nil {
		t.Fatal(err)
	}
	if n, err := f.s.PurgeWebhookHistory(f.ctx, time.Now().Add(time.Hour), 100); err != nil || n != 0 {
		t.Fatalf("pending event purged %d %v", n, err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE webhook_deliveries SET state='delivered',next_attempt_at=NULL`); err != nil {
		t.Fatal(err)
	}
	if n, err := f.s.PurgeWebhookHistory(f.ctx, time.Now().Add(time.Hour), 100); err != nil || n != 1 || syncCount(t, f, `SELECT count(*) FROM webhook_delivery_attempts`) != 0 {
		t.Fatalf("purge %d %v", n, err)
	}
}

// Secrets and header values are stored sealed with the master key; the
// deliverer opens them with the same key only.
func TestWebhookSecretsAreSealedPostgres(t *testing.T) {
	f := newJobFixture(t)
	id, secret := createWebhook(t, f, nil)
	var sealed, headers []byte
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT secret_sealed,headers_sealed FROM webhooks WHERE id=$1::uuid`, id).Scan(&sealed, &headers); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(secret)) || bytes.Contains(sealed, []byte(secret[6:20])) || bytes.Contains(headers, []byte("consumer-token-Qx1")) {
		t.Fatal("secret or header value stored in plain text")
	}
	var dump string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT string_agg(after_state::text,'') FROM audit_logs WHERE event LIKE 'webhook.%'`).Scan(&dump); err != nil || strings.Contains(dump, secret) || strings.Contains(dump, "consumer-token") {
		t.Fatal("audit carries secret material", err)
	}
	loaded, err := f.s.LoadWebhookEndpoint(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, keys, err := app.OpenWebhookEndpoint(webhookBox(t, 7), loaded)
	if err != nil || string(keys.Current) != secret || endpoint.Headers["Authorization"] != "Bearer consumer-token-Qx1" {
		t.Fatalf("open %v", err)
	}
	if _, _, err = app.OpenWebhookEndpoint(webhookBox(t, 8), loaded); !app.ErrWebhookSealed(err) {
		t.Fatalf("other master key opened the secret: %v", err)
	}
	// A sealed secret copied to another endpoint does not open there.
	other, _ := createWebhook(t, f, nil)
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE webhooks SET secret_sealed=$2 WHERE id=$1::uuid`, other, sealed); err != nil {
		t.Fatal(err)
	}
	moved, err := f.s.LoadWebhookEndpoint(f.ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = app.OpenWebhookEndpoint(webhookBox(t, 7), moved); !app.ErrWebhookSealed(err) {
		t.Fatal("secret opened under another endpoint", err)
	}
}

// Playback start and stop raise their events in the statements that write
// the session (W11-6 progress buffer).
func TestWebhookPlaybackEventsPostgres(t *testing.T) {
	f := newProgressFixture(t)
	f.s.EnableWebhookEvents(true)
	createWebhook(t, f.jobFixture, nil, domain.WebhookPlaybackStarted, domain.WebhookPlaybackStopped)
	f.report(t, f.viewer, domain.PlaybackReportStart, "hook-play", f.item, 0)
	f.report(t, f.viewer, domain.PlaybackReportStart, "hook-play", f.item, 0)
	f.clock.Advance(time.Second)
	f.report(t, f.viewer, domain.PlaybackReportProgress, "hook-play", "", time.Minute)
	if _, err := f.progress.Flush(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Second)
	f.report(t, f.viewer, domain.PlaybackReportStop, "hook-play", "", 20*time.Minute)
	f.report(t, f.viewer, domain.PlaybackReportStop, "hook-play", "", 20*time.Minute)
	var session string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM playback_sessions WHERE play_key='hook-play'`).Scan(&session); err != nil {
		t.Fatal(err)
	}
	started, stopped := outboxRows(t, f.jobFixture, "playback.started"), outboxRows(t, f.jobFixture, "playback.stopped")
	if len(started) != 1 || len(stopped) != 1 || started[0].subjectID != session || stopped[0].subjectID != session {
		t.Fatalf("started=%+v stopped=%+v", started, stopped)
	}
	if d := started[0].data; d["userId"] != f.viewer.UserID || d["itemId"] != f.item || d["sourceId"] == nil {
		t.Fatalf("started data %v", d)
	}
	if d := stopped[0].data; d["state"] != "stopped" || d["completed"] != false || d["positionTicks"] != float64(20*60*domain.PlaybackTicksPerSecond) {
		t.Fatalf("stopped data %v", d)
	}
	for _, r := range append(started, stopped...) {
		raw, _ := json.Marshal(r.data)
		if bytes.Contains(raw, []byte(f.root)) || bytes.Contains(raw, []byte("device")) || bytes.Contains(raw, []byte(f.viewer.SessionID)) {
			t.Fatalf("playback event leaks %s", raw)
		}
		event := domain.WebhookEvent{EventID: r.eventID, Type: domain.WebhookEventType(r.eventType), Version: 1, OccurredAt: time.Now(), Subject: domain.WebhookSubject{Kind: domain.WebhookSubjectKind(r.subjectKind), ID: r.subjectID}, Data: r.data}
		if err := event.Validate(); err != nil {
			t.Fatalf("stored playback event invalid: %v", err)
		}
	}
	// With events disabled the original statements run unchanged.
	f.s.EnableWebhookEvents(false)
	f.report(t, f.viewer, domain.PlaybackReportStart, "quiet", f.item, 0)
	f.clock.Advance(time.Second)
	f.report(t, f.viewer, domain.PlaybackReportStop, "quiet", "", time.Minute)
	if n := len(outboxRows(t, f.jobFixture, "playback.%")); n != 2 {
		t.Fatalf("disabled store appended playback events: %d", n)
	}
}

// Scan completion and failure, catalog synchronisation creating and
// removing items: all raise their events in the transaction of the change.
func TestWebhookScanFailedEventPostgres(t *testing.T) {
	f := newJobFixture(t)
	f.s.EnableWebhookEvents(true)
	createWebhook(t, f, nil)
	job := f.submit(t, "hook-fail")
	lease := f.claim(t, "hook-worker")
	if err := f.s.FinishJob(f.ctx, lease, domain.JobFailed, "scan_failed"); err != nil {
		t.Fatal(err)
	}
	failed := outboxRows(t, f, "scan.failed")
	if len(failed) != 1 || failed[0].subjectKind != "library" || failed[0].subjectID != f.registration.Library.ID || failed[0].data["jobId"] != job.ID || failed[0].data["errorCode"] != "scan_failed" {
		t.Fatalf("scan.failed %+v", failed)
	}
}

func TestWebhookScanAndMediaEventsPostgres(t *testing.T) {
	f := newJobFixture(t)
	f.s.EnableWebhookEvents(true)
	createWebhook(t, f, nil)
	root := syncRootPath(t, f)
	writeSyncFiles(t, root, "Heat (1995).mp4", "Alien (1979).mkv")
	enableAutoSync(t, f)
	stop := startSyncWorker(t, f, 1)
	defer stop()
	f.submit(t, "hook-first")
	waitJobsIdle(t, f)
	added := outboxRows(t, f, "media.added")
	if len(added) != 2 || syncCount(t, f, `SELECT count(*) FROM items WHERE library_id=$1::uuid`, f.registration.Library.ID) != 2 {
		t.Fatalf("media.added %+v", added)
	}
	alien := ""
	for _, r := range added {
		if r.data["libraryId"] != f.registration.Library.ID || r.data["kind"] != "Movie" {
			t.Fatalf("media.added data %v", r.data)
		}
		if r.data["title"] == "Alien" {
			alien = r.subjectID
		}
	}
	if alien == "" || len(outboxRows(t, f, "scan.completed")) != 1 {
		t.Fatalf("alien=%q completed=%d", alien, len(outboxRows(t, f, "scan.completed")))
	}
	if err := os.Remove(filepath.Join(root, "Alien (1979).mkv")); err != nil {
		t.Fatal(err)
	}
	reviewed := f.submit(t, "hook-second")
	waitJobsIdle(t, f)
	completed := outboxRows(t, f, "scan.completed")
	if len(completed) != 2 || completed[1].data["reviewRequired"] != true || completed[1].data["missing"] != float64(1) {
		t.Fatalf("scan.completed %+v", completed)
	}
	if _, err := f.s.AcceptInventoryMissing(f.ctx, f.a, reviewed.ID, 1, f.policy); err != nil {
		t.Fatal(err)
	}
	waitJobsIdle(t, f)
	deleted := outboxRows(t, f, "media.deleted")
	if len(deleted) != 1 || deleted[0].subjectID != alien || deleted[0].data["libraryId"] != f.registration.Library.ID {
		t.Fatalf("media.deleted %+v", deleted)
	}
	for _, r := range outboxRows(t, f, "%") {
		raw, _ := json.Marshal(r.data)
		if bytes.Contains(raw, []byte(root)) {
			t.Fatalf("%s leaks the library root: %s", r.eventType, raw)
		}
	}
}

func TestWebhookMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	want := downgradeAboveMigration(t, f, "webhooks")
	id, _ := createWebhook(t, f, nil)
	if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
		t.Fatal("configured webhooks downgraded")
	}
	version, dirty, err := Migrate(f.ctx, dsn, "status")
	if err != nil || version >= want || !dirty {
		t.Fatal("refused downgrade state", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM webhooks`) != 1 {
		t.Fatal("refused downgrade removed endpoints")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=false`, want); err != nil {
		t.Fatal(err)
	}
	if err = f.s.DeleteWebhook(f.ctx, f.a, id); err != nil {
		t.Fatal(err)
	}
	if version, dirty, err = Migrate(f.ctx, dsn, "down"); err != nil || dirty || version >= want {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name LIKE 'webhook%'`) != 0 {
		t.Fatal("downgrade left webhook tables")
	}
	if syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event IN ('webhook.created','webhook.deleted')`) != 2 {
		t.Fatal("downgrade removed audit history")
	}
	// Without the outbox, producers keep working while events are off. A
	// failed login produces user.login_failed; a successful one needs the
	// second factor tables of a later schema.
	c, err := f.s.Credentials(f.ctx, "job-admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CommitLogin(f.ctx, accountLoginInput(c, false)); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("failed login without the outbox: %v", err)
	}
	if version, dirty, err = Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if err = f.s.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.s.EnableWebhookEvents(true)
	createWebhook(t, f, nil, domain.WebhookUserLogin)
	accountLogin(t, f.ctx, f.s, "job-admin")
	if n := len(outboxRows(t, f, "user.login")); n != 1 {
		t.Fatalf("events after round trip: %d", n)
	}
}
