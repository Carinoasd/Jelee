package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
)

type devTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *devTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *devTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// devTestController is a capable instance over s. The clock starts at the
// real time because the permission relaxation is also checked by SQL against
// the database clock.
func devTestController(t *testing.T, s *Store, local devmode.Inputs, ttl time.Duration) (*devmode.Controller, *devTestClock) {
	t.Helper()
	clock := &devTestClock{now: time.Now()}
	c, err := devmode.NewController(devmode.ControllerOptions{Store: s, Clock: clock, Local: local, TTL: ttl, Available: devmode.Toggles()})
	if err != nil {
		t.Fatal(err)
	}
	return c, clock
}

func devEnable(t *testing.T, ctx context.Context, c *devmode.Controller) {
	t.Helper()
	token, _, err := c.IssueToken(ctx, devmode.Actor{IP: "127.0.0.1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Enable(ctx, devmode.Actor{}, token, "cli", "dev.enabled=true", 0); err != nil {
		t.Fatal(err)
	}
}

func TestDevModeStoragePostgres(t *testing.T) {
	f := newJobFixture(t)
	capable := devmode.Inputs{EnvFlag: true, ConfigEnabled: true}
	server, clock := devTestController(t, f.s, capable, time.Hour)
	audit := func(event string) int {
		return syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event=$1 AND category='security' AND target_ref='dev_mode'`, event)
	}

	// Missing thresholds are refused and audited; the token is consumed.
	token, _, err := server.IssueToken(f.ctx, devmode.Actor{IP: "127.0.0.1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	cli, _ := devTestController(t, f.s, devmode.Inputs{EnvFlag: true}, time.Hour)
	if _, err = cli.Enable(f.ctx, devmode.Actor{}, token, "cli", "", 0); !errors.Is(err, devmode.ErrDenied) {
		t.Fatalf("enable without dev.enabled: %v", err)
	}
	if _, err = server.Enable(f.ctx, devmode.Actor{}, token, "cli", "", 0); !errors.Is(err, devmode.ErrDenied) {
		t.Fatalf("token reused: %v", err)
	}
	if audit("devmode.token_issued") != 1 || audit("devmode.denied") != 2 {
		t.Fatal("token issuance and denials not audited")
	}
	if syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE after_state::text LIKE '%jdm_%'`) != 0 {
		t.Fatal("token leaked into the audit trail")
	}

	devEnable(t, f.ctx, server)
	admin := devmode.Actor{UserID: f.a.UserID, SessionID: f.a.SessionID, IP: "127.0.0.1"}
	if _, err = server.SetToggle(f.ctx, admin, devmode.RelaxLoginRateLimit, true, devmode.Confirmation{}, "api"); err != nil {
		t.Fatal(err)
	}
	// A second instance sees the shared session.
	other, otherClock := devTestController(t, f.s, capable, time.Hour)
	otherClock.now = clock.Now()
	if err = other.Refresh(f.ctx); err != nil || !other.Effective(devmode.RelaxLoginRateLimit) {
		t.Fatalf("second instance: %v", err)
	}
	if audit("devmode.enabled") != 1 || audit("devmode.toggle_changed") != 1 ||
		syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='devmode.toggle_changed' AND actor_id=$1::uuid AND after_state->>'toggle'='relax_login_rate_limit'`, f.a.UserID) != 1 ||
		syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='devmode.enabled' AND after_state->>'configDiff'='dev.enabled=true' AND after_state ? 'expiresAt'`) != 1 {
		t.Fatal("enable and toggle audit")
	}

	// Expiry restores production and is recorded once.
	clock.Advance(time.Hour)
	otherClock.Advance(time.Hour)
	if server.Active() || other.Effective(devmode.RelaxLoginRateLimit) {
		t.Fatal("session outlived its deadline")
	}
	if err = server.Refresh(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = other.Refresh(f.ctx); err != nil {
		t.Fatal(err)
	}
	if rec, err := f.s.LoadDevSession(f.ctx); err != nil || rec.Active || len(rec.Toggles) != 0 {
		t.Fatalf("expired session stored: %+v %v", rec, err)
	}
	if audit("devmode.expired") != 1 || syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='devmode.expired' AND after_state->'restored' ? 'relax_login_rate_limit'`) != 1 {
		t.Fatal("expiry audit")
	}

	// Explicit disable.
	devEnable(t, f.ctx, server)
	if err = server.Disable(f.ctx, admin, "cli", "operator"); err != nil || server.Active() || audit("devmode.disabled") != 1 {
		t.Fatalf("disable: %v", err)
	}
	if err = server.Disable(f.ctx, admin, "cli", "operator"); !errors.Is(err, devmode.ErrInactive) {
		t.Fatalf("second disable: %v", err)
	}

	// Storage constraints back the state machine.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE dev_mode_state SET active=true,enabled_at=now(),expires_at=now()+interval '25 hours'`); err == nil {
		t.Fatal("a session longer than 24 hours was stored")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE dev_mode_state SET toggles='{relax_host_strict}'`); err == nil {
		t.Fatal("an inactive session stored toggles")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO dev_mode_tokens(digest,issued_at,expires_at) VALUES(decode(repeat('ab',32),'hex'),now(),now()+interval '1 hour')`); err == nil {
		t.Fatal("a long-lived token was stored")
	}
}

// TestDevModePermissionRelaxPostgres is G48.9: relax_permission_strict
// suspends restrict_admins for administrators only, never widens what other
// users see, and ends with the session.
func TestDevModePermissionRelaxPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	everything := f.all(true)
	granted := f.all(false)
	for _, rule := range []struct {
		user contentAccessUser
		item string
	}{{f.admin, "movie-g"}, {f.admin, "movie-other"}, {f.viewer, "movie-r"}} {
		if _, err := f.s.SetItemAccessRule(f.ctx, f.a, rule.user.principal.UserID, f.items[rule.item], domain.ItemAccessHide); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.SetAccessPolicy(f.ctx, f.a, domain.AccessPolicy{RestrictAdmins: true}); err != nil {
		t.Fatal(err)
	}
	strictAdmin := without(everything, "movie-g", "movie-other")
	viewer := without(granted, "movie-r")
	f.observe(t, "strict admin", f.admin, strictAdmin)

	c, clock := devTestController(t, f.s, devmode.Inputs{EnvFlag: true, ConfigEnabled: true}, time.Hour)
	devEnable(t, f.ctx, c)
	// An active session alone relaxes nothing.
	f.observe(t, "session without toggle", f.admin, strictAdmin)
	if _, err := c.SetToggle(f.ctx, devmode.Actor{}, devmode.RelaxPermissionStrict, true, devmode.Confirmation{}, "api"); !errors.Is(err, devmode.ErrConfirmationNeeded) {
		t.Fatalf("unconfirmed relaxation: %v", err)
	}
	if _, err := c.SetToggle(f.ctx, devmode.Actor{}, devmode.RelaxPermissionStrict, true, devmode.Confirmation{IUnderstand: true}, "api"); err != nil {
		t.Fatal(err)
	}
	f.observe(t, "relaxed admin", f.admin, everything)
	f.observe(t, "viewer during relaxation", f.viewer, viewer)
	f.observe(t, "peer during relaxation", f.peer, granted)

	// Switching the toggle off restores the strict view immediately.
	if _, err := c.SetToggle(f.ctx, devmode.Actor{}, devmode.RelaxPermissionStrict, false, devmode.Confirmation{}, "api"); err != nil {
		t.Fatal(err)
	}
	f.observe(t, "toggle off", f.admin, strictAdmin)

	// So does the end of the session.
	if _, err := c.SetToggle(f.ctx, devmode.Actor{}, devmode.RelaxPermissionStrict, true, devmode.Confirmation{IUnderstand: true}, "api"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	if err := c.Refresh(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.observe(t, "after expiry", f.admin, strictAdmin)
}

func TestDevModeMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	want := migrationVersion(t, "dev_mode")
	if want != SchemaVersion {
		t.Fatalf("developer mode is migration %d, schema %d", want, SchemaVersion)
	}
	c, _ := devTestController(t, f.s, devmode.Inputs{EnvFlag: true, ConfigEnabled: true}, time.Hour)
	devEnable(t, f.ctx, c)
	// An active session never blocks the downgrade: the older schema simply
	// has no relaxations, which is the production default.
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('dev_mode_state','dev_mode_tokens')`) != 0 {
		t.Fatal("downgrade left developer mode schema")
	}
	if syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event LIKE 'devmode.%'`) != 2 {
		t.Fatal("downgrade removed audit history")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	if rec, err := f.s.LoadDevSession(f.ctx); err != nil || rec.Active || rec.Version != 0 {
		t.Fatalf("fresh session after round trip: %+v %v", rec, err)
	}
	devEnable(t, f.ctx, c)
}
