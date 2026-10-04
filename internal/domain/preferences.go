package domain

// User interface preferences (G33.3). They only change how the web client
// presents itself, never what a user may see or do, so they carry no audit
// trail and are not part of metadata backups.

// Known themes and layout densities.
const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"

	DensityComfortable = "comfortable"
	DensityCompact     = "compact"
)

// UserPreferences are a user's stored interface preferences. A user who
// never saved any reads DefaultUserPreferences.
type UserPreferences struct {
	// Theme is system (follow the browser's color scheme), light or dark.
	Theme string `json:"theme"`
	// Density is reserved for the web client's layout density.
	Density string `json:"density"`
}

// DefaultUserPreferences follow the browser and the regular layout.
func DefaultUserPreferences() UserPreferences {
	return UserPreferences{Theme: ThemeSystem, Density: DensityComfortable}
}

// Valid reports whether every field holds a known value.
func (p UserPreferences) Valid() bool {
	switch p.Theme {
	case ThemeSystem, ThemeLight, ThemeDark:
	default:
		return false
	}
	return p.Density == DensityComfortable || p.Density == DensityCompact
}
