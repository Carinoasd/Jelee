package postgres

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func siteAppearanceFixture() domain.SiteAppearance {
	a := domain.DefaultSiteAppearance()
	a.DefaultTheme = domain.ThemeDark
	a.Tokens.Light["color-primary"] = "#0f766e"
	a.CustomCSS = "a { color: red } b { background: url(https://evil.example/x) }"
	a.AllowExternalFonts = true
	a.FontHosts = []string{"fonts.example.com"}
	a.DefaultLayout = &domain.PageLayout{Home: []domain.LayoutEntry{{ID: "libraries", Visible: true}}, Detail: []domain.LayoutEntry{}}
	return a
}

func TestSiteSettingsStorage(t *testing.T) {
	f := newJobFixture(t)
	admin := contentAccessPrincipal(t, f, "site-admin", true)
	viewer := contentAccessPrincipal(t, f, "site-viewer", false)
	audits := func(event string) int {
		return syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event=$1`, event)
	}

	// Defaults, readable by every user; the stored document needs an administrator.
	r, err := f.s.GetSiteAppearance(f.ctx, viewer.actor, false)
	if err != nil || r.Revision != 0 || r.DefaultTheme != domain.ThemeSystem || r.CustomCSS != "" || len(r.FontHosts) != 0 || r.DefaultLayout != nil || r.Tokens.Light == nil {
		t.Fatalf("default appearance: %+v %v", r, err)
	}
	if _, err := f.s.GetSiteAppearance(f.ctx, viewer.actor, true); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer read the stored document: %v", err)
	}
	if _, err := f.s.SetSiteAppearance(f.ctx, viewer.actor, siteAppearanceFixture(), 0, "update"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer wrote the appearance: %v", err)
	}
	if hosts, err := f.s.SiteFontHosts(f.ctx); err != nil || len(hosts) != 0 {
		t.Fatalf("default font hosts: %v %v", hosts, err)
	}

	stored, err := f.s.SetSiteAppearance(f.ctx, admin.actor, siteAppearanceFixture(), 0, "update")
	if err != nil || stored.Revision != 1 || stored.DefaultLayout == nil || stored.DefaultLayout.Home[0].ID != "libraries" || stored.Tokens.Light["color-primary"] != "#0f766e" || stored.CustomCSS != siteAppearanceFixture().CustomCSS {
		t.Fatalf("set: %+v %v", stored, err)
	}
	if hosts, err := f.s.SiteFontHosts(f.ctx); err != nil || strings.Join(hosts, ",") != "fonts.example.com" {
		t.Fatalf("font hosts: %v %v", hosts, err)
	}
	// Unchanged is a no-op; a stale revision is a conflict.
	if again, err := f.s.SetSiteAppearance(f.ctx, admin.actor, siteAppearanceFixture(), 1, "update"); err != nil || again.Revision != 1 {
		t.Fatalf("unchanged: %+v %v", again, err)
	}
	changed := siteAppearanceFixture()
	changed.AllowExternalFonts = false
	if _, err := f.s.SetSiteAppearance(f.ctx, admin.actor, changed, 0, "update"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if next, err := f.s.SetSiteAppearance(f.ctx, admin.actor, changed, 1, "update"); err != nil || next.Revision != 2 {
		t.Fatalf("second write: %+v %v", next, err)
	}
	if hosts, _ := f.s.SiteFontHosts(f.ctx); len(hosts) != 0 {
		t.Fatalf("disabled external fonts still allowed: %v", hosts)
	}
	// Storage refuses what the application would refuse.
	hostile := siteAppearanceFixture()
	hostile.CustomCSS = "</style><script>alert(1)</script>"
	if _, err := f.s.SetSiteAppearance(f.ctx, admin.actor, hostile, 2, "update"); !errors.Is(err, domain.ErrCustomCSSRejected) {
		t.Fatalf("hostile CSS: %v", err)
	}
	if _, err := f.s.SetSiteAppearance(f.ctx, admin.actor, changed, 2, "sideways"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown source: %v", err)
	}
	if audits("site.appearance_changed") != 2 {
		t.Fatalf("appearance audits: %d", audits("site.appearance_changed"))
	}
	var category, before, after string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT category,before_state::text,after_state::text FROM audit_logs WHERE event='site.appearance_changed' ORDER BY id LIMIT 1`).Scan(&category, &before, &after); err != nil {
		t.Fatal(err)
	}
	if category != domain.AuditCategorySecurity || !strings.Contains(after, `"source": "update"`) || !strings.Contains(after, "cssSha256") || strings.Contains(after, "evil.example") {
		t.Fatalf("audit record: %s %s %s", category, before, after)
	}

	// Plugins.
	plugins := domain.SitePlugins{Plugins: []domain.SitePluginState{{ID: "jelee.item-facts", Enabled: false}, {ID: "jelee.accent-tokens", Enabled: true}},
		Settings: map[string]json.RawMessage{"jelee.accent-tokens": json.RawMessage(`{"accent":"violet","n":1.50,"x":null}`)}}
	if _, err := f.s.SetSitePlugins(f.ctx, viewer.actor, plugins, 0, "update"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer wrote plugins: %v", err)
	}
	p, err := f.s.SetSitePlugins(f.ctx, admin.actor, plugins, 0, "update")
	if err != nil || p.Revision != 1 || p.Plugins[0].ID != "jelee.item-facts" || p.Plugins[1].Enabled != true {
		t.Fatalf("plugins: %+v %v", p, err)
	}
	var settings map[string]any
	if err := json.Unmarshal(p.Settings["jelee.accent-tokens"], &settings); err != nil || settings["accent"] != "violet" || settings["x"] != nil {
		t.Fatalf("settings: %s %v", p.Settings["jelee.accent-tokens"], err)
	}
	if read, err := f.s.GetSitePlugins(f.ctx, viewer.actor, false); err != nil || read.Revision != 1 || len(read.Plugins) != 2 {
		t.Fatalf("viewer read: %+v %v", read, err)
	}
	if again, err := f.s.SetSitePlugins(f.ctx, admin.actor, plugins, 1, "update"); err != nil || again.Revision != 1 {
		t.Fatalf("unchanged plugins: %+v %v", again, err)
	}
	if _, err := f.s.SetSitePlugins(f.ctx, admin.actor, domain.DefaultSitePlugins(), 0, "reset"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale plugins: %v", err)
	}
	if audits("site.plugins_changed") != 1 {
		t.Fatalf("plugin audits: %d", audits("site.plugins_changed"))
	}

	// An import is atomic: an invalid plugin document leaves the appearance alone.
	bad := domain.SitePlugins{Plugins: []domain.SitePluginState{{ID: "Bad"}}, Settings: map[string]json.RawMessage{}}
	if _, _, err := f.s.ImportSiteSettings(f.ctx, admin.actor, siteAppearanceFixture(), bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad import: %v", err)
	}
	if r, _ := f.s.GetSiteAppearance(f.ctx, admin.actor, true); r.Revision != 2 || r.AllowExternalFonts {
		t.Fatalf("a failed import changed the appearance: %+v", r)
	}
	a, pl, err := f.s.ImportSiteSettings(f.ctx, admin.actor, siteAppearanceFixture(), domain.DefaultSitePlugins())
	if err != nil || a.Revision != 3 || !a.AllowExternalFonts || pl.Revision != 2 || len(pl.Plugins) != 0 {
		t.Fatalf("import: %+v %+v %v", a, pl, err)
	}
	if appearance, exported, err := f.s.GetSiteSettings(f.ctx, admin.actor); err != nil || appearance.Revision != 3 || exported.Revision != 2 {
		t.Fatalf("export read: %v", err)
	}
	if _, _, err := f.s.GetSiteSettings(f.ctx, viewer.actor); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer exported: %v", err)
	}
	if syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event LIKE 'site.%' AND after_state->>'source'='import'`) != 2 {
		t.Fatal("import audit missing")
	}

	// The constraints back the application checks.
	for _, statement := range []string{
		`UPDATE site_appearance SET default_theme='sepia'`,
		`UPDATE site_appearance SET font_hosts=array_fill('a.example.com'::text, ARRAY[11])`,
		`UPDATE site_appearance SET tokens='[]'`,
		`UPDATE site_plugins SET settings='[]'`,
		`INSERT INTO site_plugins(id) VALUES(false)`,
		`INSERT INTO user_preferences(user_id,layout) VALUES('` + admin.actor.UserID + `','[]')`,
	} {
		if _, err := f.s.Pool.Exec(f.ctx, statement); err == nil {
			t.Fatalf("storage accepted %s", statement)
		}
	}

	// Revoked sessions read nothing.
	imageRepositoryExec(t, f, `UPDATE sessions SET revoked_at=now() WHERE id=$1::uuid`, viewer.actor.SessionID)
	if _, err := f.s.GetSiteAppearance(f.ctx, viewer.actor, false); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("revoked session read the appearance: %v", err)
	}
}

func TestUserPreferencesLayoutAndSiteDefaultTheme(t *testing.T) {
	f := newJobFixture(t)
	admin := contentAccessPrincipal(t, f, "prefs-site-admin", true)
	viewer := contentAccessPrincipal(t, f, "prefs-site-viewer", false)
	if _, err := f.s.SetSiteAppearance(f.ctx, admin.actor, siteAppearanceFixture(), 0, "update"); err != nil {
		t.Fatal(err)
	}
	// A user who never saved preferences gets the site's default theme.
	if p, err := f.s.GetPreferences(f.ctx, viewer.actor); err != nil || p.Theme != domain.ThemeDark || p.Density != domain.DensityComfortable || p.Layout != nil {
		t.Fatalf("site default: %+v %v", p, err)
	}
	layout := &domain.UserLayout{Current: domain.PageLayout{Home: []domain.LayoutEntry{{ID: "latest", Visible: false}}, Detail: []domain.LayoutEntry{}},
		Presets: []domain.LayoutPreset{{ID: "custom-1", Name: "審查", Layout: domain.PageLayout{Home: []domain.LayoutEntry{}, Detail: []domain.LayoutEntry{{ID: "nfo", Visible: true}}}}}}
	saved, err := f.s.SetPreferences(f.ctx, viewer.actor, domain.UserPreferences{Theme: domain.ThemeSystem, Density: domain.DensityCompact, Layout: layout})
	if err != nil || saved.Layout == nil || saved.Layout.Presets[0].Name != "審查" || saved.Layout.Current.Home[0].Visible {
		t.Fatalf("layout: %+v %v", saved, err)
	}
	// Once saved, the user's own theme wins over the site default.
	if p, err := f.s.GetPreferences(f.ctx, viewer.actor); err != nil || p.Theme != domain.ThemeSystem || p.Layout == nil || p.Layout.Presets[0].Layout.Detail[0].ID != "nfo" {
		t.Fatalf("read back: %+v %v", p, err)
	}
	if _, err := f.s.SetPreferences(f.ctx, viewer.actor, domain.UserPreferences{Theme: domain.ThemeSystem, Density: domain.DensityCompact, Layout: &domain.UserLayout{}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid layout: %v", err)
	}
	if p, err := f.s.SetPreferences(f.ctx, viewer.actor, domain.UserPreferences{Theme: domain.ThemeLight, Density: domain.DensityCompact}); err != nil || p.Layout != nil {
		t.Fatalf("clear layout: %+v %v", p, err)
	}
}

func TestSiteSettingsMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	admin := contentAccessPrincipal(t, f, "site-migrate", true)
	if _, err := f.s.SetSiteAppearance(f.ctx, admin.actor, siteAppearanceFixture(), 0, "update"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetPreferences(f.ctx, admin.actor, domain.UserPreferences{Theme: domain.ThemeDark, Density: domain.DensityCompact,
		Layout: &domain.UserLayout{Current: domain.PageLayout{Home: []domain.LayoutEntry{}, Detail: []domain.LayoutEntry{}}, Presets: []domain.LayoutPreset{}}}); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f, "site_settings")
	// Presentation settings only: the downgrade drops them; audit records stay.
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('site_appearance','site_plugins')`) != 0 ||
		syncCount(t, f, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='user_preferences' AND column_name='layout'`) != 0 {
		t.Fatal("downgrade left schema 74 objects")
	}
	if syncCount(t, f, `SELECT count(*) FROM user_preferences WHERE theme='dark'`) != 1 || syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='site.appearance_changed'`) != 1 {
		t.Fatal("downgrade lost older data")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	r, err := f.s.GetSiteAppearance(f.ctx, admin.actor, true)
	if err != nil || r.Revision != 0 || r.CustomCSS != "" {
		t.Fatalf("after round trip: %+v %v", r, err)
	}
	if p, err := f.s.GetPreferences(f.ctx, admin.actor); err != nil || p.Theme != domain.ThemeDark || p.Layout != nil {
		t.Fatalf("preferences after round trip: %+v %v", p, err)
	}
	if _, err := f.s.SetSiteAppearance(f.ctx, admin.actor, siteAppearanceFixture(), 0, "update"); err != nil {
		t.Fatalf("write after round trip: %v", err)
	}
}
