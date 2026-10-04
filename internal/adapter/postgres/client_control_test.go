package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func clientDenyRule(pattern string) domain.ClientRuleInput {
	return domain.ClientRuleInput{Dimension: "user_agent", Match: "exact", Pattern: pattern, Action: "deny", ScopeKind: "global", ScopeValues: []string{}, Enabled: true}
}

func clientVersion(t *testing.T, f jobFixture) int64 {
	t.Helper()
	v, err := f.s.ClientControlVersion(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func clientKeyFor(n int) string { return strings.Repeat(string(rune('a'+n%6)), 64) }

func TestClientControlStorage(t *testing.T) {
	f := newJobFixture(t)
	viewer := contentAccessPrincipal(t, f, "cc-store-viewer", false)

	t.Run("administrators only", func(t *testing.T) {
		if _, err := f.s.ListClientRules(f.ctx, viewer.actor); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("viewer listed rules: %v", err)
		}
		if _, err := f.s.CreateClientRule(f.ctx, viewer.actor, clientDenyRule("x")); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("viewer created a rule: %v", err)
		}
	})

	t.Run("session lookup carries labels and the version", func(t *testing.T) {
		token, err := f.s.Provision(f.ctx, "cc-labelled", access.ClientNative, false)
		if err != nil {
			t.Fatal(err)
		}
		imageRepositoryExec(t, f, `UPDATE sessions SET device_id='dev-1',client_name='Player',client_version='2.0',device_name='Den' WHERE user_id=(SELECT id FROM users WHERE name='cc-labelled')`)
		p, client, version, err := f.s.AuthenticateClient(f.ctx, token, "198.51.100.4")
		if err != nil || p.Kind != access.ClientNative || client.DeviceID != "dev-1" || client.Name != "Player" || client.Version != "2.0" || client.DeviceName != "Den" || client.IssuedAt.IsZero() || version != clientVersion(t, f) {
			t.Fatalf("authenticate: %+v %+v %d %v", p, client, version, err)
		}
	})

	t.Run("versions bump on evaluation changes only", func(t *testing.T) {
		v0 := clientVersion(t, f)
		r, err := f.s.CreateClientRule(f.ctx, f.a, clientDenyRule("Versioned/1"))
		if err != nil {
			t.Fatal(err)
		}
		v1 := clientVersion(t, f)
		if _, err = f.s.UpdateClientRule(f.ctx, f.a, r.ID, clientDenyRule("Versioned/1")); err != nil {
			t.Fatal(err)
		}
		if v1 != v0+1 || clientVersion(t, f) != v1 {
			t.Fatalf("create bumps, unchanged update does not: %d %d %d", v0, v1, clientVersion(t, f))
		}
		if _, err = f.s.SetClientRuleEnforcing(f.ctx, f.a, r.ID, true); err != nil || clientVersion(t, f) != v1 {
			t.Fatalf("enforcing an enforcing rule is a no-op: %v", err)
		}
		if err = f.s.DeleteClientRule(f.ctx, f.a, r.ID); err != nil || clientVersion(t, f) != v1+1 {
			t.Fatalf("delete: %v", err)
		}
		if err = f.s.DeleteClientRule(f.ctx, f.a, r.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("second delete: %v", err)
		}
	})

	t.Run("storage refuses what the gate cannot enforce", func(t *testing.T) {
		bad := clientDenyRule("x")
		bad.Action = "restrict_libraries"
		if _, err := f.s.CreateClientRule(f.ctx, f.a, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("restrict_libraries stored: %v", err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO client_rules(dimension,match_kind,pattern,action,scope_kind,scope_values) VALUES('user_agent','exact','x','deny','library','{a}')`); err == nil {
			t.Fatal("library scope stored directly")
		}
		if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO client_rules(dimension,match_kind,pattern,action) VALUES('user_agent','exact','x','observe')`); err == nil {
			t.Fatal("observe rule without intent stored directly")
		}
	})

	t.Run("enabled regex limit", func(t *testing.T) {
		imageRepositoryExec(t, f, `INSERT INTO client_rules(dimension,match_kind,pattern,action) SELECT 'user_agent','regex','^x'||n,'deny' FROM generate_series(1,1000) n`)
		re := clientDenyRule("^y")
		re.Match = "regex"
		if _, err := f.s.CreateClientRule(f.ctx, f.a, re); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("1001st enabled regex: %v", err)
		}
		re.Enabled = false
		r, err := f.s.CreateClientRule(f.ctx, f.a, re)
		if err != nil {
			t.Fatalf("disabled regex over the limit: %v", err)
		}
		re.Enabled = true
		if _, err = f.s.UpdateClientRule(f.ctx, f.a, r.ID, re); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("enabling the 1001st regex: %v", err)
		}
		imageRepositoryExec(t, f, `DELETE FROM client_rules`)
	})

	t.Run("activity, trust and kick", func(t *testing.T) {
		now := time.Now().UTC()
		acts := []domain.ClientActivity{
			{Key: clientKeyFor(0), AppName: "Player", AppVersion: "1.0", DeviceID: "dev-k", DeviceName: "Den", ClientKind: "native", UserID: viewer.principal.UserID, SessionID: viewer.principal.SessionID, IP: "203.0.113.9", At: now.Add(-time.Minute)},
			{Key: clientKeyFor(0), AppVersion: "1.1", UserID: viewer.principal.UserID, SessionID: viewer.principal.SessionID, IP: "203.0.113.10", At: now},
			{Key: clientKeyFor(1), UserAgent: "Browser/1\x00evil", ClientKind: "web", At: now.Add(-time.Hour)},
			{Key: "short", At: now},
		}
		if err := f.s.RecordClientActivity(f.ctx, acts); err != nil {
			t.Fatal(err)
		}
		clients, next, err := f.s.ListKnownClients(f.ctx, f.a, "", 1)
		if err != nil || len(clients) != 1 || next == "" {
			t.Fatalf("first page: %+v %q %v", clients, next, err)
		}
		c := clients[0]
		if c.AppName != "Player" || c.AppVersion != "1.1" || c.DeviceName != "Den" || c.LastIP != "203.0.113.10" || c.ActiveSessions != 1 || c.LastUserID != viewer.principal.UserID {
			t.Fatalf("merged client: %+v", c)
		}
		rest, next, err := f.s.ListKnownClients(f.ctx, f.a, next, 10)
		if err != nil || len(rest) != 1 || next != "" || rest[0].UserAgent != "Browser/1evil" {
			t.Fatalf("second page: %+v %q %v", rest, next, err)
		}
		v := clientVersion(t, f)
		alias := "Den player"
		if _, err = f.s.UpdateKnownClient(f.ctx, f.a, c.ID, domain.KnownClientUpdate{Alias: &alias}); err != nil || clientVersion(t, f) != v {
			t.Fatalf("rename bumped the version: %v", err)
		}
		trusted := true
		if _, err = f.s.UpdateKnownClient(f.ctx, f.a, c.ID, domain.KnownClientUpdate{Trusted: &trusted}); err != nil || clientVersion(t, f) != v+1 {
			t.Fatalf("trust: %v", err)
		}
		state, err := f.s.ClientControlState(f.ctx)
		if err != nil || len(state.TrustedKeys) != 1 || state.TrustedKeys[0] != clientKeyFor(0) {
			t.Fatalf("trusted keys: %+v %v", state.TrustedKeys, err)
		}
		audits := syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='client_control.client_updated'`)
		if _, err = f.s.UpdateKnownClient(f.ctx, f.a, c.ID, domain.KnownClientUpdate{Trusted: &trusted}); err != nil || syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='client_control.client_updated'`) != audits {
			t.Fatalf("unchanged update audited: %v", err)
		}
		n, err := f.s.KickKnownClient(f.ctx, f.a, c.ID)
		if err != nil || n != 1 {
			t.Fatalf("kick: %d %v", n, err)
		}
		if syncCount(t, f, `SELECT count(*) FROM sessions WHERE id=$1::uuid AND revoked_at IS NOT NULL`, viewer.principal.SessionID) != 1 {
			t.Fatal("kick left the session active")
		}
		rule, err := f.s.BlockKnownClient(f.ctx, f.a, c.ID)
		if err != nil || rule.Dimension != "device_id" || rule.Pattern != "dev-k" || rule.Priority != clientBlockPriority || !strings.Contains(rule.Note, "Den player") {
			t.Fatalf("block: %+v %v", rule, err)
		}
		if _, err = f.s.BlockKnownClient(f.ctx, f.a, rest[0].ID); err != nil {
			t.Fatalf("block by user agent: %v", err)
		}
		imageRepositoryExec(t, f, `DELETE FROM client_rules`)
	})

	t.Run("hits, counters, retention and export", func(t *testing.T) {
		r, err := f.s.CreateClientRule(f.ctx, f.a, clientDenyRule("Hit/1"))
		if err != nil {
			t.Fatal(err)
		}
		gone, err := f.s.CreateClientRule(f.ctx, f.a, clientDenyRule("Gone/1"))
		if err != nil {
			t.Fatal(err)
		}
		if err = f.s.DeleteClientRule(f.ctx, f.a, gone.ID); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Minute)
		hits := []domain.ClientHit{
			{Bucket: now, RuleID: r.ID, Mode: "enforced", Action: "deny", Surface: "native", UserID: viewer.principal.UserID, IP: "2001:db8:1:2::9", UserAgent: "Hit/1", Hits: 3},
			{Bucket: now, RuleID: gone.ID, Mode: "observe", Action: "deny", Surface: "compat", IP: "203.0.113.9", Hits: 2},
			{Bucket: now.Add(-40 * 24 * time.Hour), Mode: "default", Action: "pending_approval", Surface: "native", Hits: 1},
		}
		counts := []domain.ClientRuleCount{{RuleID: r.ID, Hits: 3, Last: now}, {RuleID: gone.ID, Hits: 2, Last: now}}
		if err = f.s.RecordClientHits(f.ctx, hits, counts); err != nil {
			t.Fatal(err)
		}
		// The bucket past the retention was purged in the same write.
		page, next, err := f.s.ListClientHits(f.ctx, f.a, domain.ClientHitFilter{}, "", 1)
		if err != nil || len(page) != 1 || next == "" {
			t.Fatalf("page: %+v %q %v", page, next, err)
		}
		rest, next, err := f.s.ListClientHits(f.ctx, f.a, domain.ClientHitFilter{}, next, 10)
		if err != nil || len(rest) != 1 || next != "" {
			t.Fatalf("rest: %+v %q %v", rest, next, err)
		}
		all := append(page, rest...)
		byMode := map[string]domain.ClientHitRecord{}
		for _, h := range all {
			byMode[h.Mode] = h
		}
		if byMode["enforced"].Network != "2001:db8:1::/48" || byMode["enforced"].RuleID != r.ID || byMode["observe"].RuleID != "" || byMode["observe"].Network != "203.0.113.0/24" {
			t.Fatalf("masked records: %+v", all)
		}
		stored, err := f.s.GetClientRule(f.ctx, f.a, r.ID)
		if err != nil || stored.HitCount != 3 || stored.LastHitAt == nil {
			t.Fatalf("counter: %+v %v", stored, err)
		}
		if _, err = f.s.ExportClientHits(f.ctx, f.a, domain.ClientHitFilter{}, 1); !errors.Is(err, domain.ErrWatchStatsExportLimit) {
			t.Fatalf("export over its limit: %v", err)
		}
		exported, err := f.s.ExportClientHits(f.ctx, f.a, domain.ClientHitFilter{RuleID: r.ID}, 10)
		if err != nil || len(exported) != 1 {
			t.Fatalf("export: %+v %v", exported, err)
		}
		stats, err := f.s.ClientHitStats(f.ctx, f.a, now.Add(-time.Hour), 5)
		if err != nil || stats.Total != 5 || stats.Blocked != 3 || stats.Observed != 2 || len(stats.TopIPs) != 2 || stats.TopIPs[0].Value != "2001:db8:1:2::9" {
			t.Fatalf("stats: %+v %v", stats, err)
		}
	})

	t.Run("emergency reset", func(t *testing.T) {
		if _, err := f.s.CreateClientRule(f.ctx, f.a, clientDenyRule("Everyone")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.SetClientPolicy(f.ctx, f.a, domain.ClientPolicy{UnknownClients: "deny", ExemptAdmins: false, ExemptLoopback: false}); err != nil {
			t.Fatal(err)
		}
		v := clientVersion(t, f)
		reset, err := f.s.ResetClientPolicies(f.ctx)
		if err != nil || reset.RulesDisabled != 2 || reset.Policy != (domain.ClientPolicy{UnknownClients: "allow", ExemptAdmins: true, ExemptLoopback: true, Version: v + 1, UpdatedAt: reset.Policy.UpdatedAt}) {
			t.Fatalf("reset: %+v %v", reset, err)
		}
		if syncCount(t, f, `SELECT count(*) FROM client_rules WHERE enabled`) != 0 || syncCount(t, f, `SELECT count(*) FROM client_rules`) != 2 {
			t.Fatal("reset must disable, not delete, the rules")
		}
		if syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='client_control.policies_reset' AND category='security'`) != 1 {
			t.Fatal("reset not audited")
		}
	})
}

func TestClientControlMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	if _, err := f.s.CreateClientRule(f.ctx, f.a, clientDenyRule("Kept/1")); err != nil {
		t.Fatal(err)
	}
	want := migrationVersion(t, "client_control")
	if want != SchemaVersion {
		t.Fatalf("client control is migration %d, schema %d", want, SchemaVersion)
	}
	if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
		t.Fatal("an enforcing client rule was dropped by a downgrade")
	}
	version, dirty, err := Migrate(f.ctx, dsn, "status")
	if err != nil || version >= want || !dirty {
		t.Fatal("refused downgrade state", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM client_rules`) != 1 {
		t.Fatal("refused downgrade removed rules")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=false`, want); err != nil {
		t.Fatal(err)
	}
	// The recovery the downgrade message points at clears the guard.
	if _, err = f.s.ResetClientPolicies(f.ctx); err != nil {
		t.Fatal(err)
	}
	if version, dirty, err = Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('client_control_policy','client_rules','known_clients','known_client_sessions','client_control_hits')`) != 0 {
		t.Fatal("downgrade left client control schema")
	}
	if syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event LIKE 'client_control.%'`) != 2 {
		t.Fatal("downgrade removed audit history")
	}
	if version, dirty, err = Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if err = f.s.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CreateClientRule(f.ctx, f.a, clientDenyRule("Again/1")); err != nil {
		t.Fatal(err)
	}
	if p, err := f.s.ClientPolicy(f.ctx, f.a); err != nil || p.UnknownClients != "allow" || !p.ExemptAdmins || p.Version != 2 {
		t.Fatalf("fresh policy after round trip: %+v %v", p, err)
	}
}
