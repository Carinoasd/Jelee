package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestSiteDefaultDisplayStorage(t *testing.T) {
	f := newJobFixture(t)
	admin := contentAccessPrincipal(t, f, "site-display-admin", true)
	viewer := contentAccessPrincipal(t, f, "site-display-viewer", false)
	chooser := contentAccessPrincipal(t, f, "site-display-chooser", false)
	audits := func() int {
		return syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='site.appearance_changed'`)
	}

	// A new installation has no site default display.
	if r, err := f.s.GetSiteAppearance(f.ctx, viewer.actor, false); err != nil || r.DefaultDisplay != nil {
		t.Fatalf("default: %+v %v", r.DefaultDisplay, err)
	}

	// The site default round-trips member by member; unset members stay null.
	in := siteAppearanceFixture()
	in.DefaultTheme = domain.ThemeLight
	in.DefaultDisplay = &domain.UserDisplay{Palette: ptr(domain.PaletteSepia), PosterRatio: ptr(domain.PosterLandscape), View: ptr(domain.ViewList)}
	stored, err := f.s.SetSiteAppearance(f.ctx, admin.actor, in, 0, "update")
	if err != nil || stored.Revision != 1 || stored.DefaultDisplay == nil || *stored.DefaultDisplay.PosterRatio != domain.PosterLandscape || stored.DefaultDisplay.PosterSize != nil {
		t.Fatalf("set: %+v %v", stored.DefaultDisplay, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM site_appearance WHERE default_display='{"palette":"sepia","posterRatio":"landscape","posterSize":null,"listDensity":null,"view":"list"}'::jsonb`) != 1 {
		t.Fatal("stored form")
	}
	if r, err := f.s.GetSiteAppearance(f.ctx, viewer.actor, false); err != nil || r.View().DefaultDisplay == nil || *r.View().DefaultDisplay.View != domain.ViewList {
		t.Fatalf("viewer read: %+v %v", r.DefaultDisplay, err)
	}
	// Audited with the members as they are; an unchanged write is a no-op.
	var after string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT after_state::text FROM audit_logs WHERE event='site.appearance_changed' ORDER BY id DESC LIMIT 1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after, `"defaultDisplay": {"view": "list", "palette": "sepia", "posterSize": null, "listDensity": null, "posterRatio": "landscape"}`) {
		t.Fatalf("audit: %s", after)
	}
	if again, err := f.s.SetSiteAppearance(f.ctx, admin.actor, in, 1, "update"); err != nil || again.Revision != 1 || audits() != 1 {
		t.Fatalf("unchanged: %+v %v %d", again.Revision, err, audits())
	}
	// Changing only one display member is a change.
	changed := in
	changed.DefaultDisplay = &domain.UserDisplay{Palette: ptr(domain.PaletteSepia), PosterRatio: ptr(domain.PosterSquare), View: ptr(domain.ViewList)}
	if next, err := f.s.SetSiteAppearance(f.ctx, admin.actor, changed, 1, "update"); err != nil || next.Revision != 2 || audits() != 2 {
		t.Fatalf("member change: %+v %v", next.Revision, err)
	}
	bad := in
	bad.DefaultDisplay = &domain.UserDisplay{View: ptr("grid")}
	if _, err := f.s.SetSiteAppearance(f.ctx, admin.actor, bad, 2, "update"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid member: %v", err)
	}

	// A user who never saved preferences reads the site theme with its
	// preset; the other members are left to the client's member fallback.
	if p, err := f.s.GetPreferences(f.ctx, viewer.actor); err != nil || p.Theme != domain.ThemeLight || p.Display == nil || p.Display.Palette == nil ||
		*p.Display.Palette != domain.PaletteSepia || p.Display.PosterRatio != nil || p.Display.View != nil {
		t.Fatalf("unsaved user: %+v %v", p.Display, err)
	}
	// A user who saved preferences keeps them, palette included.
	if _, err := f.s.SetPreferences(f.ctx, chooser.actor, domain.UserPreferences{Theme: domain.ThemeLight, Density: domain.DensityComfortable}); err != nil {
		t.Fatal(err)
	}
	if p, err := f.s.GetPreferences(f.ctx, chooser.actor); err != nil || p.Theme != domain.ThemeLight || p.Display != nil {
		t.Fatalf("saved user: %+v %v", p.Display, err)
	}

	// Import replaces it, an import without it clears it, and reset drops it.
	imported := in
	imported.DefaultDisplay = &domain.UserDisplay{PosterSize: ptr(domain.PosterSizeLarge)}
	a, _, err := f.s.ImportSiteSettings(f.ctx, admin.actor, imported, domain.DefaultSitePlugins())
	if err != nil || a.DefaultDisplay == nil || *a.DefaultDisplay.PosterSize != domain.PosterSizeLarge || a.DefaultDisplay.Palette != nil {
		t.Fatalf("import: %+v %v", a.DefaultDisplay, err)
	}
	if p, err := f.s.GetPreferences(f.ctx, viewer.actor); err != nil || p.Display != nil {
		t.Fatalf("no site palette: %+v %v", p.Display, err)
	}
	if a, _, err = f.s.ImportSiteSettings(f.ctx, admin.actor, siteAppearanceFixture(), domain.DefaultSitePlugins()); err != nil || a.DefaultDisplay != nil {
		t.Fatalf("import without display: %+v %v", a.DefaultDisplay, err)
	}
	if _, err = f.s.SetSiteAppearance(f.ctx, admin.actor, in, domain.AnyRevision, "update"); err != nil {
		t.Fatal(err)
	}
	if r, err := f.s.SetSiteAppearance(f.ctx, admin.actor, domain.DefaultSiteAppearance(), domain.AnyRevision, "reset"); err != nil || r.DefaultDisplay != nil {
		t.Fatalf("reset: %+v %v", r.DefaultDisplay, err)
	}

	// The constraint backs the API: an object within 1 KiB.
	for _, statement := range []string{
		`UPDATE site_appearance SET default_display='[]'`,
		`UPDATE site_appearance SET default_display='"sepia"'`,
		`UPDATE site_appearance SET default_display=jsonb_build_object('view',repeat('x',1100))`,
	} {
		if _, err := f.s.Pool.Exec(f.ctx, statement); err == nil {
			t.Fatalf("storage accepted %s", statement)
		}
	}
	// A value that bypassed the API is refused on read.
	imageRepositoryExec(t, f, `UPDATE site_appearance SET default_display='{"view":"grid"}'`)
	if _, err := f.s.GetSiteAppearance(f.ctx, viewer.actor, false); !errors.Is(err, domain.ErrDatabase) {
		t.Fatalf("unknown stored value: %v", err)
	}
	imageRepositoryExec(t, f, `UPDATE site_appearance SET default_display=NULL`)
}

func TestSiteDefaultDisplayMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	admin := contentAccessPrincipal(t, f, "site-display-migrate", true)
	in := siteAppearanceFixture()
	in.DefaultDisplay = &domain.UserDisplay{PosterRatio: ptr(domain.PosterSquare)}
	if _, err := f.s.SetSiteAppearance(f.ctx, admin.actor, in, 0, "update"); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f, "site_default_display")
	// Presentation only: the downgrade drops the column and keeps the rest.
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='site_appearance' AND column_name='default_display'`) != 0 {
		t.Fatal("downgrade kept default_display")
	}
	if syncCount(t, f, `SELECT count(*) FROM site_appearance WHERE default_theme='dark' AND revision=1 AND default_layout IS NOT NULL`) != 1 {
		t.Fatal("downgrade lost the rest of the appearance")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	r, err := f.s.GetSiteAppearance(f.ctx, admin.actor, true)
	if err != nil || r.DefaultDisplay != nil || r.DefaultTheme != domain.ThemeDark {
		t.Fatalf("after round trip: %+v %v", r, err)
	}
	if _, err := f.s.SetSiteAppearance(f.ctx, admin.actor, in, r.Revision, "update"); err != nil {
		t.Fatalf("write after round trip: %v", err)
	}
}
