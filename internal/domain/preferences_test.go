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
	good := UserDisplay{PosterRatio: ptr(PosterPortrait), PosterSize: ptr(PosterSizeMedium), ListDensity: ptr(ListDensityComfortable), View: ptr(ViewPoster)}
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
		"ratio":       func(d UserDisplay) UserDisplay { d.PosterRatio = ptr("wide"); return d },
		"size":        func(d UserDisplay) UserDisplay { d.PosterSize = ptr(""); return d },
		"listDensity": func(d UserDisplay) UserDisplay { d.ListDensity = ptr("tiny"); return d },
		"view":        func(d UserDisplay) UserDisplay { d.View = ptr("grid"); return d },
		"case":        func(d UserDisplay) UserDisplay { d.View = ptr("Poster"); return d },
	}
	for name, mutate := range mutations {
		d := mutate(good)
		if d.Valid() || (UserPreferences{Theme: ThemeLight, Density: DensityComfortable, Display: &d}).Valid() {
			t.Fatalf("%s accepted", name)
		}
		if _, ok := NormalizeDisplay(&d); ok {
			t.Fatalf("%s normalized", name)
		}
	}
	// Every member is optional on its own: unset members follow the next layer.
	for _, partial := range []UserDisplay{{PosterSize: ptr(PosterSizeLarge)}, {Palette: &sepia}, {View: ptr(ViewList), ListDensity: ptr(ListDensityCompact)}} {
		got, ok := NormalizeDisplay(&partial)
		if !partial.Valid() || partial.Empty() || !ok || got == nil || got == &partial {
			t.Fatalf("%+v: %+v %v", partial, got, ok)
		}
	}
	if got, ok := NormalizeDisplay(&UserDisplay{}); got != nil || !ok || !(UserDisplay{}).Empty() {
		t.Fatalf("empty display: %+v %v", got, ok)
	}
	if got, ok := NormalizeDisplay(nil); got != nil || !ok {
		t.Fatalf("nil display: %+v %v", got, ok)
	}
}
