package postgres

import (
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestUserPreferencesStorage(t *testing.T) {
	f := newJobFixture(t)
	viewer := contentAccessPrincipal(t, f, "prefs-viewer", false)
	other := contentAccessPrincipal(t, f, "prefs-other", false)

	if p, err := f.s.GetPreferences(f.ctx, viewer.actor); err != nil || p != domain.DefaultUserPreferences() {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	dark := domain.UserPreferences{Theme: domain.ThemeDark, Density: domain.DensityCompact}
	if p, err := f.s.SetPreferences(f.ctx, viewer.actor, dark); err != nil || p != dark {
		t.Fatalf("set: %+v %v", p, err)
	}
	light := domain.UserPreferences{Theme: domain.ThemeLight, Density: domain.DensityComfortable}
	if p, err := f.s.SetPreferences(f.ctx, viewer.actor, light); err != nil || p != light {
		t.Fatalf("replace: %+v %v", p, err)
	}
	if p, err := f.s.GetPreferences(f.ctx, viewer.actor); err != nil || p != light {
		t.Fatalf("read back: %+v %v", p, err)
	}
	// Each user reads only their own row.
	if p, err := f.s.GetPreferences(f.ctx, other.actor); err != nil || p != domain.DefaultUserPreferences() {
		t.Fatalf("other user saw preferences: %+v %v", p, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM user_preferences`) != 1 {
		t.Fatal("unexpected preference rows")
	}
	if _, err := f.s.SetPreferences(f.ctx, viewer.actor, domain.UserPreferences{Theme: "auto", Density: domain.DensityCompact}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid preferences: %v", err)
	}
	// The storage constraint backs the domain check.
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE user_preferences SET theme='sepia'`); err == nil {
		t.Fatal("storage accepted an unknown theme")
	}
	// Preferences need the live session.
	imageRepositoryExec(t, f, `UPDATE sessions SET revoked_at=now() WHERE id=$1::uuid`, viewer.actor.SessionID)
	if _, err := f.s.GetPreferences(f.ctx, viewer.actor); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("revoked session read preferences: %v", err)
	}
	if _, err := f.s.SetPreferences(f.ctx, viewer.actor, dark); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("revoked session wrote preferences: %v", err)
	}
	if audits := syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event LIKE '%preference%' AND event<>'library.metadata_preferences_changed'`); audits != 0 {
		t.Fatalf("presentation preferences were audited: %d", audits)
	}
}

func TestUserPreferencesMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	viewer := contentAccessPrincipal(t, f, "prefs-migrate", false)
	if _, err := f.s.SetPreferences(f.ctx, viewer.actor, domain.UserPreferences{Theme: domain.ThemeDark, Density: domain.DensityCompact}); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f, "user_preferences")
	// Preferences only change presentation; the downgrade drops them.
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='user_preferences'`) != 0 ||
		syncCount(t, f, `SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname='client_rules_block_lookup_idx'`) != 0 {
		t.Fatal("downgrade left schema 73 objects")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if p, err := f.s.GetPreferences(f.ctx, viewer.actor); err != nil || p != domain.DefaultUserPreferences() {
		t.Fatalf("after round trip: %+v %v", p, err)
	}
}

func TestUserDisplayPreferencesStorage(t *testing.T) {
	f := newJobFixture(t)
	viewer := contentAccessPrincipal(t, f, "prefs-display", false)
	sepia := domain.PaletteSepia
	display := &domain.UserDisplay{Palette: &sepia, PosterRatio: domain.PosterSquare, PosterSize: domain.PosterSizeLarge, ListDensity: domain.ListDensityCompact, View: domain.ViewList}
	saved, err := f.s.SetPreferences(f.ctx, viewer.actor, domain.UserPreferences{Theme: domain.ThemeLight, Density: domain.DensityComfortable, Display: display})
	if err != nil || saved.Display == nil || saved.Display.Palette == nil || *saved.Display.Palette != sepia || saved.Display.PosterRatio != domain.PosterSquare || saved.Display.View != domain.ViewList {
		t.Fatalf("display: %+v %v", saved, err)
	}
	if p, err := f.s.GetPreferences(f.ctx, viewer.actor); err != nil || p.Display == nil || p.Display.PosterSize != domain.PosterSizeLarge || p.Display.ListDensity != domain.ListDensityCompact {
		t.Fatalf("read back: %+v %v", p, err)
	}
	// A null palette is kept as null, not dropped.
	display.Palette = nil
	if p, err := f.s.SetPreferences(f.ctx, viewer.actor, domain.UserPreferences{Theme: domain.ThemeDark, Density: domain.DensityComfortable, Display: display}); err != nil || p.Display == nil || p.Display.Palette != nil {
		t.Fatalf("null palette: %+v %v", p, err)
	}
	bad := *display
	bad.View = "grid"
	if _, err := f.s.SetPreferences(f.ctx, viewer.actor, domain.UserPreferences{Theme: domain.ThemeDark, Density: domain.DensityComfortable, Display: &bad}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid display: %v", err)
	}
	// Clearing the display keeps theme and layout rules intact.
	if p, err := f.s.SetPreferences(f.ctx, viewer.actor, domain.UserPreferences{Theme: domain.ThemeDark, Density: domain.DensityComfortable}); err != nil || p.Display != nil {
		t.Fatalf("clear: %+v %v", p, err)
	}
	// The storage constraint backs the API: only an object within 1 KiB.
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE user_preferences SET display='[]'::jsonb`); err == nil {
		t.Fatal("storage accepted a non-object display")
	}
	// A row that bypassed the API with an unknown value is refused on read.
	imageRepositoryExec(t, f, `UPDATE user_preferences SET display='{"palette":null,"posterRatio":"wide","posterSize":"medium","listDensity":"comfortable","view":"poster"}'::jsonb`)
	if _, err := f.s.GetPreferences(f.ctx, viewer.actor); !errors.Is(err, domain.ErrDatabase) {
		t.Fatalf("unknown stored value: %v", err)
	}
}
