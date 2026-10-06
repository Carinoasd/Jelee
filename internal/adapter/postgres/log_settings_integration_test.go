package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// TestLogSettingsIntegration covers the stored log settings (G46.2, G46.9,
// G46.10): defaults, administrator-only access, override replacement and
// expiry pruning, audits of every change, the security audit of a refused
// mandatory change, and the retention write across both tables.
func TestLogSettingsIntegration(t *testing.T) {
	f := newAuditFixture(t)
	settings, err := f.s.LogSettings(f.ctx)
	if err != nil || settings.LogDays != 30 || settings.LogMaxTotalMB != 2048 || len(settings.Overrides) != 0 || settings.Revision != 1 {
		t.Fatalf("defaults %+v %v", settings, err)
	}
	if _, _, err = f.s.AdminLogSettings(f.ctx, f.user); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("user read: %v", err)
	}
	if _, err = f.s.SetLogLevelOverride(f.ctx, f.user, domain.LogLevelOverride{Component: "http", Level: "debug"}, false); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("user change: %v", err)
	}
	if _, _, err = f.s.SetLogRetention(f.ctx, f.user, domain.LogRetention{LogDays: 1, LogMaxTotalMB: 1, AuditDays: 30, SecurityDays: 30}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("user retention: %v", err)
	}
	if err = f.s.RecordLogLevelRefused(f.ctx, f.user, "audit", "error"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("user refusal record: %v", err)
	}

	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if settings, err = f.s.SetLogLevelOverride(f.ctx, f.admin, domain.LogLevelOverride{Component: "scan", Level: "debug", ExpiresAt: expires}, false); err != nil {
		t.Fatal(err)
	}
	if settings, err = f.s.SetLogLevelOverride(f.ctx, f.admin, domain.LogLevelOverride{Component: "", Level: "warn"}, false); err != nil {
		t.Fatal(err)
	}
	if len(settings.Overrides) != 2 || settings.Overrides[0].Component != "" || settings.Overrides[1].Component != "scan" || !settings.Overrides[1].ExpiresAt.Equal(expires) || settings.Revision != 3 {
		t.Fatalf("overrides %+v", settings)
	}
	// Replacing one scope keeps the others; an expired entry is pruned on
	// the next write.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE log_settings SET level_overrides=level_overrides||'[{"component":"nfo","level":"debug","expiresAt":"2020-01-01T00:00:00Z"}]'::jsonb`); err != nil {
		t.Fatal(err)
	}
	if settings, err = f.s.SetLogLevelOverride(f.ctx, f.admin, domain.LogLevelOverride{Component: "scan", Level: "error"}, false); err != nil {
		t.Fatal(err)
	}
	components := []string{}
	for _, o := range settings.Overrides {
		components = append(components, o.Component+"="+o.Level)
	}
	if !slices.Equal(components, []string{"=warn", "scan=error"}) {
		t.Fatalf("after replace: %v", components)
	}
	if settings, err = f.s.SetLogLevelOverride(f.ctx, f.admin, domain.LogLevelOverride{Component: ""}, true); err != nil || len(settings.Overrides) != 1 {
		t.Fatalf("reset: %+v %v", settings, err)
	}
	if f.count(t, `event='logging.level_changed' AND category='audit' AND actor_id=$1::uuid`, f.admin.UserID) != 4 ||
		f.count(t, `event='logging.level_changed' AND target_ref='log_level:scan' AND after_state->'override'->>'level'='error' AND before_state->'override'->>'level'='debug'`) != 1 ||
		f.count(t, `event='logging.level_changed' AND target_ref='log_level:global' AND after_state->'override' = 'null'::jsonb`) != 1 {
		t.Fatal("level changes not audited")
	}
	if _, err = f.s.SetLogLevelOverride(f.ctx, f.admin, domain.LogLevelOverride{Component: "http"}, false); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing level: %v", err)
	}

	// G46.10: the refusal is a security event with its trace ID.
	trace, _ := domain.NewTraceID()
	span, _ := domain.NewSpanID()
	sc := domain.NewRootSpanContext(trace, span, true)
	if err = f.s.RecordLogLevelRefused(domain.ContextWithSpan(f.ctx, sc), f.admin, "audit", "error"); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `event='logging.level_change_refused' AND category='security' AND target_ref='log_level:audit' AND after_state->>'level'='error' AND request_id=$1`, sc.TraceHex()) != 1 {
		t.Fatal("refusal not audited as a security event")
	}

	// Retention: both tables in one write; unchanged parts write nothing.
	logs, audit, err := f.s.SetLogRetention(f.ctx, f.admin, domain.LogRetention{LogDays: 14, LogMaxTotalMB: 512, AuditDays: 365, SecurityDays: 90})
	if err != nil || logs.LogDays != 14 || logs.LogMaxTotalMB != 512 || audit.SecurityDays != 90 || audit.AuditDays != 365 {
		t.Fatalf("retention %+v %+v %v", logs, audit, err)
	}
	if _, _, err = f.s.SetLogRetention(f.ctx, f.admin, domain.LogRetention{LogDays: 14, LogMaxTotalMB: 512, AuditDays: 365, SecurityDays: 90}); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `event='logging.retention_changed' AND before_state->>'logDays'='30' AND after_state->>'logDays'='14'`) != 1 ||
		f.count(t, `event='audit.retention_changed' AND after_state->>'securityDays'='90'`) != 1 || f.count(t, `event='logging.retention_changed'`) != 1 {
		t.Fatal("retention changes not audited exactly once")
	}
	got, auditGot, err := f.s.AdminLogSettings(f.ctx, f.admin)
	if err != nil || got.LogDays != 14 || auditGot.SecurityDays != 90 || len(got.Overrides) != 1 {
		t.Fatalf("admin read %+v %+v %v", got, auditGot, err)
	}
	for _, r := range []domain.LogRetention{{LogDays: -1, AuditDays: 30, SecurityDays: 30}, {LogDays: 3651, AuditDays: 30, SecurityDays: 30}, {LogMaxTotalMB: 1<<20 + 1, AuditDays: 30, SecurityDays: 30}, {AuditDays: 6, SecurityDays: 30}} {
		if _, _, err = f.s.SetLogRetention(f.ctx, f.admin, r); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%+v accepted", r)
		}
	}

	// The row is retained and bounded by the schema.
	for name, statement := range map[string]string{
		"delete":    `DELETE FROM log_settings`,
		"truncate":  `TRUNCATE log_settings`,
		"days":      `UPDATE log_settings SET log_days=4000`,
		"not array": `UPDATE log_settings SET level_overrides='{}'::jsonb`,
	} {
		if _, err = f.s.Pool.Exec(f.ctx, statement); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// TestLoginFailureNotesSecurityEvents: storage notes failed logins and the
// lock they cause for the request's security log (G46.3).
func TestLoginFailureNotesSecurityEvents(t *testing.T) {
	ctx, s, _ := accountTestStore(t)
	if _, err := s.BootstrapAdmin(ctx, accountInput("notes-admin")); err != nil {
		t.Fatal(err)
	}
	c, err := s.Credentials(ctx, "notes-admin")
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for range 4 {
		reqCtx, notes := domain.WithSecurityNotes(ctx)
		if _, err = s.CommitLogin(reqCtx, accountLoginInput(c, false)); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("failed login: %v", err)
		}
		events = append(events, notes.Events()...)
	}
	want := []string{"login_failed", "login_failed", "login_failed", "account_locked", "login_while_locked"}
	if !slices.Equal(events, want) {
		t.Fatalf("notes %v, want %v", events, want)
	}
	// Outside a request nothing is recorded and nothing fails.
	domain.NoteSecurityEvent(context.Background(), "ignored")
}

// TestLogSettingsMigrationRoundTrip: the settings are operational, so a
// downgrade drops them (instances return to their configured levels) and an
// upgrade recreates the default row; audit history is kept.
func TestLogSettingsMigrationRoundTrip(t *testing.T) {
	f := newAuditFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	if _, err := f.s.SetLogLevelOverride(f.ctx, f.admin, domain.LogLevelOverride{Component: "http", Level: "debug"}, false); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f.jobFixture(), "log_settings")
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version >= want {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f.jobFixture(), `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='log_settings'`) != 0 ||
		syncCount(t, f.jobFixture(), `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.proname='guard_log_settings'`) != 0 {
		t.Fatal("downgrade left log settings behind")
	}
	if f.count(t, `event='logging.level_changed'`) != 1 {
		t.Fatal("downgrade removed audit history")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if settings, err := f.s.LogSettings(f.ctx); err != nil || len(settings.Overrides) != 0 || settings.LogDays != 30 {
		t.Fatalf("after upgrade %+v %v", settings, err)
	}
}
