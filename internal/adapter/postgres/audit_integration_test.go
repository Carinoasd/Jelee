package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

type auditFixture struct {
	ctx   context.Context
	s     *Store
	admin domain.Actor
	user  domain.Actor
}

func newAuditFixture(t *testing.T) auditFixture {
	t.Helper()
	ctx, s, _ := accountTestStore(t)
	if _, err := s.BootstrapAdmin(ctx, accountInput("audit-admin")); err != nil {
		t.Fatal(err)
	}
	admin := accountActor(accountLogin(t, ctx, s, "audit-admin"))
	if _, _, err := s.CreateUser(ctx, admin, accountInput("audit-user"), "audit-user-key"); err != nil {
		t.Fatal(err)
	}
	return auditFixture{ctx: ctx, s: s, admin: admin, user: accountActor(accountLogin(t, ctx, s, "audit-user"))}
}

func auditRejected(t *testing.T, err error, what string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("%s was not rejected by the append-only guard: %v", what, err)
	}
}

// jobFixture exposes the store for migration helpers shared with job tests.
func (f auditFixture) jobFixture() jobFixture { return jobFixture{ctx: f.ctx, s: f.s} }

func (f auditFixture) count(t *testing.T, where string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// backdate inserts a row with an old timestamp by suspending the insert guard
// as the table owner, which is the only way to forge age in this fixture.
func (f auditFixture) backdate(t *testing.T, event, category string, age time.Duration) int64 {
	t.Helper()
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	var id int64
	if _, err = tx.Exec(f.ctx, `ALTER TABLE audit_logs DISABLE TRIGGER guard_audit_logs_insert`); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(f.ctx, `INSERT INTO audit_logs(event,category,target_ref,occurred_at) VALUES($1,$2,'fixture',now()-$3::interval) RETURNING id`, event, category, age.String()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.ctx, `ALTER TABLE audit_logs ENABLE TRIGGER guard_audit_logs_insert`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAuditIntegrationAppendOnly(t *testing.T) {
	f := newAuditFixture(t)
	if f.count(t, `true`) == 0 {
		t.Fatal("fixture produced no audit rows")
	}
	old := f.backdate(t, "user.updated", "audit", 400*24*time.Hour)
	for name, stmt := range map[string]string{
		"update":         `UPDATE audit_logs SET event='user.deleted'`,
		"update one":     `UPDATE audit_logs SET after_state='{}'::jsonb WHERE id=(SELECT max(id) FROM audit_logs)`,
		"delete":         `DELETE FROM audit_logs`,
		"delete expired": `DELETE FROM audit_logs WHERE occurred_at<now()-interval '300 days'`,
		"truncate":       `TRUNCATE audit_logs`,
		"retention row":  `DELETE FROM audit_retention`,
	} {
		_, err := f.s.Pool.Exec(f.ctx, stmt)
		auditRejected(t, err, name)
	}
	// A session that forges the purge flag still cannot remove a row inside
	// retention, nor alter any row.
	// The transaction is rolled back before asserting so a failure cannot leave
	// an open transaction that blocks fixture cleanup.
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.ctx, `SET LOCAL jelee.audit_purge='on'`); err == nil {
		_, err = tx.Exec(f.ctx, `DELETE FROM audit_logs WHERE id<>$1`, old)
	}
	_ = tx.Rollback(f.ctx)
	auditRejected(t, err, "forged purge of live rows")
	if f.count(t, `id=$1`, old) != 1 {
		t.Fatal("expired row disappeared outside purge")
	}
	// Inserts cannot backdate themselves into an expired window.
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO audit_logs(event,target_ref,occurred_at) VALUES('user.updated','forged',now()-interval '1000 days')`); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `target_ref='forged' AND occurred_at>now()-interval '1 minute'`) != 1 {
		t.Fatal("insert kept a caller-supplied timestamp")
	}
	for name, stmt := range map[string]string{
		"category": `INSERT INTO audit_logs(event,category,target_ref) VALUES('user.updated','debug','x')`,
		"event":    `INSERT INTO audit_logs(event,target_ref) VALUES('Not An Event','x')`,
		"targets":  `INSERT INTO audit_logs(event,target_id,target_ref) VALUES('user.updated',gen_random_uuid(),'x')`,
		"request":  `INSERT INTO audit_logs(event,target_ref,request_id) VALUES('user.updated','x','has space')`,
		"size":     `INSERT INTO audit_logs(event,target_ref,after_state) VALUES('user.updated','x',jsonb_build_object('v',repeat('a',70000)))`,
	} {
		_, err := f.s.Pool.Exec(f.ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("%s constraint did not reject the row: %v", name, err)
		}
	}
}

func TestAuditIntegrationAppendContract(t *testing.T) {
	f := newAuditFixture(t)
	write := func(e AuditEntry) error {
		tx, err := f.s.Pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(f.ctx)
		if err = appendAudit(f.ctx, tx, e); err != nil {
			return err
		}
		return tx.Commit(f.ctx)
	}
	before := f.count(t, `true`)
	for name, e := range map[string]AuditEntry{
		"unknown event": {Event: "user.exploded", TargetRef: "x"},
		"empty event":   {TargetRef: "x"},
		"two targets":   {Event: "user.updated", TargetID: f.admin.UserID, TargetRef: "x"},
		"bad ref":       {Event: "user.updated", TargetRef: "a\nb"},
		"bad uuid":      {Event: "user.updated", TargetID: "not-a-uuid"},
	} {
		if err := write(e); err == nil {
			t.Fatalf("%s was appended", name)
		}
	}
	if f.count(t, `true`) != before {
		t.Fatal("rejected entries left rows")
	}
	if err := auditAccount(f.ctx, nil, f.admin, "user.updated", "", nil, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("auditAccount must still require a target")
	}
	err := write(AuditEntry{Event: "login.failed", Actor: domain.Actor{UserID: f.user.UserID, IP: "::ffff:10.1.2.3"}, TargetID: f.user.UserID, RequestID: "req-0123.abc",
		Before: map[string]any{"password": "hunter2", "nested": []any{map[string]string{"refreshToken": "tok-xyz"}}},
		After:  map[string]any{"overview": strings.Repeat("z", auditStateLimit+1)}})
	if err != nil {
		t.Fatal(err)
	}
	var category, ip, request, b, a string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT category,host(actor_ip),request_id,before_state::text,after_state::text FROM audit_logs WHERE request_id='req-0123.abc'`).Scan(&category, &ip, &request, &b, &a); err != nil {
		t.Fatal(err)
	}
	if category != "security" || ip != "10.1.2.3" || strings.Contains(b, "hunter2") || strings.Contains(b, "tok-xyz") || !strings.Contains(b, auditRedacted) || !strings.Contains(a, `"truncated": true`) || len(a) > 200 {
		t.Fatalf("stored entry: category=%s ip=%s before=%s after=%.200s", category, ip, b, a)
	}
	if err = write(AuditEntry{Event: "user.updated", TargetRef: "settings:audit", RequestID: "bad id"}); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `target_ref='settings:audit' AND target_id IS NULL AND request_id IS NULL AND category='audit'`) != 1 {
		t.Fatal("non-UUID target or dropped request id not stored as designed")
	}
	// Account flows never store credentials in audit states.
	if f.count(t, `before_state::text ~* '(argon2|passwordHash|"token")' OR after_state::text ~* '(argon2|passwordHash|"token")'`) != 0 {
		t.Fatal("account audit stored a credential")
	}
}

func TestAuditIntegrationRetentionAndPurge(t *testing.T) {
	f := newAuditFixture(t)
	r, err := f.s.AuditRetention(f.ctx, f.admin)
	if err != nil || r.AuditDays != 365 || r.SecurityDays != 365 || r.Revision != 1 {
		t.Fatalf("default retention: %+v %v", r, err)
	}
	if _, err = f.s.AuditRetention(f.ctx, f.user); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("non-admin read retention")
	}
	if _, err = f.s.SetAuditRetention(f.ctx, f.user, 30, 30); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("non-admin changed retention")
	}
	for _, days := range [][2]int{{6, 30}, {30, 6}, {36501, 30}} {
		if _, err = f.s.SetAuditRetention(f.ctx, f.admin, days[0], days[1]); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("retention %v accepted", days)
		}
	}
	if r, err = f.s.SetAuditRetention(f.ctx, f.admin, 365, 90); err != nil || r.SecurityDays != 90 || r.Revision != 2 {
		t.Fatalf("set retention: %+v %v", r, err)
	}
	if f.count(t, `event='audit.retention_changed' AND target_ref='audit_retention' AND target_id IS NULL AND actor_id=$1::uuid AND before_state->>'securityDays'='365' AND after_state->>'securityDays'='90'`, f.admin.UserID) != 1 {
		t.Fatal("retention change was not audited")
	}
	if result, err := f.s.PurgeAudit(f.ctx, 100); err != nil || result != (domain.AuditPurgeResult{}) {
		t.Fatalf("purge of fresh rows: %+v %v", result, err)
	}
	day := 24 * time.Hour
	keepAudit := f.backdate(t, "user.updated", "audit", 300*day)
	keepSecurity := f.backdate(t, "login.failed", "security", 80*day)
	expired := []int64{
		f.backdate(t, "user.updated", "audit", 400*day),
		f.backdate(t, "user.created", "audit", 366*day),
		f.backdate(t, "login.failed", "security", 91*day),
		f.backdate(t, "login.failed", "security", 400*day),
	}
	// Direct deletion of expired rows still bypasses the controlled path.
	_, err = f.s.Pool.Exec(f.ctx, `DELETE FROM audit_logs WHERE id=$1`, expired[0])
	auditRejected(t, err, "direct delete of an expired row")
	if _, err = f.s.PurgeAudit(f.ctx, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("zero purge batch accepted")
	}
	total := f.count(t, `true`)
	first, err := f.s.PurgeAudit(f.ctx, 3)
	if err != nil || first.Audit+first.Security != 3 {
		t.Fatalf("first purge batch: %+v %v", first, err)
	}
	second, err := f.s.PurgeAudit(f.ctx, 3)
	if err != nil || second.Audit+second.Security != 1 || first.Audit+second.Audit != 2 || first.Security+second.Security != 2 {
		t.Fatalf("second purge batch: %+v %v", second, err)
	}
	if again, err := f.s.PurgeAudit(f.ctx, 3); err != nil || again != (domain.AuditPurgeResult{}) {
		t.Fatalf("purge after completion: %+v %v", again, err)
	}
	for _, id := range expired {
		if f.count(t, `id=$1`, id) != 0 {
			t.Fatal("expired row survived purge")
		}
	}
	if f.count(t, `id IN ($1,$2)`, keepAudit, keepSecurity) != 2 {
		t.Fatal("purge removed a row inside retention")
	}
	if f.count(t, `event='audit.retention_purged' AND target_ref='audit_logs'`) != 2 || f.count(t, `true`) != total-4+2 {
		t.Fatal("purge batches were not recorded exactly once each")
	}
	var direct int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM purge_audit_logs(current_schema(),10) WHERE purged>0`).Scan(&direct); err != nil || direct != 0 {
		t.Fatal("controlled purge function is not idempotent", err)
	}
}

func TestAuditIntegrationListPagination(t *testing.T) {
	f := newAuditFixture(t)
	c, err := f.s.Credentials(f.ctx, "audit-user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CommitLogin(f.ctx, accountLoginInput(c, false)); err == nil {
		t.Fatal("failed login succeeded")
	}
	total := f.count(t, `true`)
	if total < 5 {
		t.Fatalf("fixture has only %d audit rows", total)
	}
	seen := map[int64]bool{}
	cursor, pages := "", 0
	last := int64(1 << 62)
	for {
		page, next, err := f.s.ListAudit(f.ctx, f.admin, domain.AuditFilter{}, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, r := range page {
			if r.ID >= last || seen[r.ID] || len(r.Before) == 0 || len(r.After) == 0 || r.OccurredAt.IsZero() {
				t.Fatalf("page order or content: %+v", r)
			}
			last, seen[r.ID] = r.ID, true
		}
		if next == "" {
			break
		}
		if len(page) != 2 || pages > total {
			t.Fatal("pagination did not advance")
		}
		cursor = next
	}
	if len(seen) != total {
		t.Fatalf("paged %d of %d rows", len(seen), total)
	}
	security, next, err := f.s.ListAudit(f.ctx, f.admin, domain.AuditFilter{Category: "security"}, "", 100)
	if err != nil || next != "" || len(security) != 1 || security[0].Event != "login.failed" || security[0].TargetID != f.user.UserID || security[0].ActorIP != "127.0.0.1" {
		t.Fatalf("security filter: %+v %v", security, err)
	}
	mine, _, err := f.s.ListAudit(f.ctx, f.admin, domain.AuditFilter{Target: f.user.UserID, Event: "login.failed", Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour)}, "", 100)
	if err != nil || len(mine) != 1 {
		t.Fatalf("target filter: %d %v", len(mine), err)
	}
	byActor, _, err := f.s.ListAudit(f.ctx, f.admin, domain.AuditFilter{ActorID: f.admin.UserID, Event: "user.created"}, "", 100)
	if err != nil || len(byActor) != 1 || byActor[0].ActorID != f.admin.UserID {
		t.Fatalf("actor filter: %d %v", len(byActor), err)
	}
	if _, _, err = f.s.ListAudit(f.ctx, f.user, domain.AuditFilter{}, "", 10); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("non-admin listed audit rows")
	}
	for _, bad := range []struct {
		cursor string
		limit  int
		filter domain.AuditFilter
	}{{"abc", 10, domain.AuditFilter{}}, {"", 0, domain.AuditFilter{}}, {"", 101, domain.AuditFilter{}}, {"", 10, domain.AuditFilter{Event: "user.exploded"}}} {
		if _, _, err = f.s.ListAudit(f.ctx, f.admin, bad.filter, bad.cursor, bad.limit); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid listing accepted: %+v", bad)
		}
	}
}

func TestAuditMigrationRoundTrip(t *testing.T) {
	f := newAuditFixture(t)
	c, err := f.s.Credentials(f.ctx, "audit-user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CommitLogin(f.ctx, accountLoginInput(c, false)); err == nil {
		t.Fatal("failed login succeeded")
	}
	dsn := f.s.Pool.Config().ConnString()
	snapshot := func() string {
		var s string
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_array(id,event,target_id,actor_id,actor_ip,before_state,after_state,occurred_at) ORDER BY id),'[]')::text FROM audit_logs`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()
	want := downgradeAboveMigration(t, f.jobFixture(), "audit_append_only")
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != want-1 {
		t.Fatalf("down: %d %t %v", v, dirty, err)
	}
	var legacy bool
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('audit_retention') IS NULL AND to_regprocedure('purge_audit_logs(text,integer)') IS NULL
	 AND NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='audit_logs' AND column_name IN ('category','request_id','target_ref'))
	 AND (SELECT is_nullable='NO' FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='audit_logs' AND column_name='target_id')
	 AND NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='audit_logs'::regclass AND NOT tgisinternal)`).Scan(&legacy); err != nil || !legacy {
		t.Fatal("downgrade left audit schema objects", err)
	}
	if snapshot() != before {
		t.Fatal("downgrade changed audit rows")
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || v != SchemaVersion {
		t.Fatalf("up: %d %t %v", v, dirty, err)
	}
	if snapshot() != before || f.count(t, `event='login.failed' AND category='security'`) != 1 || f.count(t, `event<>'login.failed' AND category<>'audit'`) != 0 {
		t.Fatal("upgrade did not preserve rows or derive categories")
	}
	if err = f.s.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Rows the previous schema cannot represent refuse the downgrade and stay intact.
	if _, err = f.s.SetAuditRetention(f.ctx, f.admin, 365, 365+1); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetAuditRetention(f.ctx, f.admin, 365, 365); err != nil {
		t.Fatal(err)
	}
	retained := snapshot()
	downgradeAboveMigration(t, f.jobFixture(), "audit_append_only")
	if _, _, err = Migrate(f.ctx, dsn, "down"); err == nil {
		t.Fatal("downgrade discarded non-UUID audit targets")
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "status"); err != nil || v != want-1 || !dirty {
		t.Fatalf("refused downgrade status: %d %t %v", v, dirty, err)
	}
	if snapshot() != retained || f.count(t, `target_ref='audit_retention'`) != 2 {
		t.Fatal("refused downgrade changed audit rows")
	}
	if err = f.s.Ready(f.ctx); err == nil {
		t.Fatal("runtime accepted a dirty refused downgrade")
	}
}
