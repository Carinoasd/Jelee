package domain

import "testing"

func TestUserPreferencesValid(t *testing.T) {
	if !DefaultUserPreferences().Valid() {
		t.Fatal("defaults must be valid")
	}
	for _, theme := range []string{ThemeSystem, ThemeLight, ThemeDark} {
		for _, density := range []string{DensityComfortable, DensityCompact} {
			if !(UserPreferences{Theme: theme, Density: density}).Valid() {
				t.Fatalf("%s/%s rejected", theme, density)
			}
		}
	}
	for _, p := range []UserPreferences{{}, {Theme: "Dark", Density: DensityCompact}, {Theme: ThemeDark}, {Theme: "auto", Density: DensityComfortable}, {Theme: ThemeLight, Density: "dense"}} {
		if p.Valid() {
			t.Fatalf("%+v accepted", p)
		}
	}
}

func TestUserDisplayValid(t *testing.T) {
	sepia, bad := PaletteSepia, "neon"
	good := UserDisplay{PosterRatio: PosterPortrait, PosterSize: PosterSizeMedium, ListDensity: ListDensityComfortable, View: ViewPoster}
	if !good.Valid() || !(UserPreferences{Theme: ThemeLight, Density: DensityComfortable, Display: &good}).Valid() {
		t.Fatal("defaults rejected")
	}
	good.Palette = &sepia
	if !good.Valid() {
		t.Fatal("sepia rejected")
	}
	mutations := map[string]func(UserDisplay) UserDisplay{
		"palette":     func(d UserDisplay) UserDisplay { d.Palette = &bad; return d },
		"light":       func(d UserDisplay) UserDisplay { l := ThemeLight; d.Palette = &l; return d },
		"ratio":       func(d UserDisplay) UserDisplay { d.PosterRatio = "wide"; return d },
		"size":        func(d UserDisplay) UserDisplay { d.PosterSize = ""; return d },
		"listDensity": func(d UserDisplay) UserDisplay { d.ListDensity = "tiny"; return d },
		"view":        func(d UserDisplay) UserDisplay { d.View = "grid"; return d },
	}
	for name, mutate := range mutations {
		d := mutate(good)
		if d.Valid() || (UserPreferences{Theme: ThemeLight, Density: DensityComfortable, Display: &d}).Valid() {
			t.Fatalf("%s accepted", name)
		}
	}
}
