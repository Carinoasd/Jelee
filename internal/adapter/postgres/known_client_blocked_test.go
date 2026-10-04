package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// knownClientsByKey lists every known client keyed by its device ID or,
// without one, its user agent.
func knownClientsByKey(t *testing.T, f jobFixture) map[string]domain.KnownClient {
	t.Helper()
	clients, _, err := f.s.ListKnownClients(f.ctx, f.a, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]domain.KnownClient{}
	for _, c := range clients {
		key := c.DeviceID
		if key == "" {
			key = c.UserAgent[:min(len(c.UserAgent), 16)]
		}
		out[key] = c
	}
	return out
}

func TestKnownClientBlockedMirrorsTheBlockAction(t *testing.T) {
	f := newJobFixture(t)
	now := time.Now().UTC()
	longAgent := "Clipped/1 " + strings.Repeat("x", 600)
	if err := f.s.RecordClientActivity(f.ctx, []domain.ClientActivity{
		{Key: clientKeyFor(0), AppName: "Player", DeviceID: "dev-blocked", ClientKind: "native", At: now},
		{Key: clientKeyFor(1), UserAgent: "Browser/2", ClientKind: "web", At: now.Add(-time.Minute)},
		{Key: clientKeyFor(2), UserAgent: longAgent, ClientKind: "web", At: now.Add(-2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	clients := knownClientsByKey(t, f)
	device, browser, clipped := clients["dev-blocked"], clients["Browser/2"], clients["Clipped/1 xxxxxx"]
	for _, c := range []domain.KnownClient{device, browser, clipped} {
		if c.ID == "" || c.Blocked || c.BlockRuleID != "" {
			t.Fatalf("client blocked before any rule: %+v", c)
		}
	}

	// Rules that would not block like the action does are not reported.
	notBlocking := []domain.ClientRuleInput{
		{Dimension: "device_id", Match: "exact", Pattern: "dev-blocked", Action: "deny", ScopeKind: "global", ScopeValues: []string{}, Enabled: false},
		{Dimension: "device_id", Match: "exact", Pattern: "dev-blocked", Action: "read_only", ScopeKind: "global", ScopeValues: []string{}, Enabled: true},
		{Dimension: "device_id", Match: "exact", Pattern: "dev-blocked", Action: "observe", Intent: "deny", ScopeKind: "global", ScopeValues: []string{}, Enabled: true},
		{Dimension: "device_id", Match: "exact", Pattern: "dev-blocked", Action: "deny", ScopeKind: "client_kind", ScopeValues: []string{"native"}, Enabled: true},
		{Dimension: "device_id", Match: "exact", Pattern: "dev-blocked", Action: "deny", ScopeKind: "global", ScopeValues: []string{}, Enabled: true, Window: &domain.ClientRuleWindow{DailyStart: "01:00", DailyEnd: "02:00"}},
		{Dimension: "device_id", Match: "prefix", Pattern: "dev-blocked", Action: "deny", ScopeKind: "global", ScopeValues: []string{}, Enabled: true},
		{Dimension: "user_agent", Match: "exact", Pattern: "dev-blocked", Action: "deny", ScopeKind: "global", ScopeValues: []string{}, Enabled: true},
		{Dimension: "user_agent", Match: "exact", Pattern: "Browser/2", Action: "deny", ScopeKind: "user", ScopeValues: []string{f.a.UserID}, Enabled: true},
	}
	for _, in := range notBlocking {
		if _, err := f.s.CreateClientRule(f.ctx, f.a, in); err != nil {
			t.Fatalf("create %+v: %v", in, err)
		}
	}
	for key, c := range knownClientsByKey(t, f) {
		if c.Blocked {
			t.Fatalf("%s reported blocked by a rule the block action would not create", key)
		}
	}

	rules := map[string]domain.ClientRule{}
	for name, c := range map[string]domain.KnownClient{"device": device, "browser": browser, "clipped": clipped} {
		r, err := f.s.BlockKnownClient(f.ctx, f.a, c.ID)
		if err != nil {
			t.Fatalf("block %s: %v", name, err)
		}
		rules[name] = r
	}
	if rules["clipped"].Match != "prefix" {
		t.Fatalf("clipped user agent blocked with %s", rules["clipped"].Match)
	}
	clients = knownClientsByKey(t, f)
	for name, key := range map[string]string{"device": "dev-blocked", "browser": "Browser/2", "clipped": "Clipped/1 xxxxxx"} {
		if c := clients[key]; !c.Blocked || c.BlockRuleID != rules[name].ID {
			t.Fatalf("%s not reported blocked by %s: %+v", name, rules[name].ID, c)
		}
	}

	// Disabling the block rule unblocks; a single client read agrees.
	off := domain.ClientRuleInput{Dimension: "device_id", Match: "exact", Pattern: "dev-blocked", Priority: clientBlockPriority, Action: "deny", ScopeKind: "global", ScopeValues: []string{}, Enabled: false, Note: rules["device"].Note}
	if _, err := f.s.UpdateClientRule(f.ctx, f.a, rules["device"].ID, off); err != nil {
		t.Fatal(err)
	}
	trusted := true
	updated, err := f.s.UpdateKnownClient(f.ctx, f.a, device.ID, domain.KnownClientUpdate{Trusted: &trusted})
	if err != nil || updated.Blocked || updated.BlockRuleID != "" {
		t.Fatalf("disabled block rule still reported: %+v %v", updated, err)
	}
	if err = f.s.DeleteClientRule(f.ctx, f.a, rules["browser"].ID); err != nil {
		t.Fatal(err)
	}
	if c := knownClientsByKey(t, f)["Browser/2"]; c.Blocked {
		t.Fatalf("deleted block rule still reported: %+v", c)
	}
}
