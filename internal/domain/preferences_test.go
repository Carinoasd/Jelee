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
