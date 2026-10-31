package domain

// User interface preferences (G33.3, G33.5). They only change how the web
// client presents itself, never what a user may see or do, so they carry no
// audit trail and are not part of metadata backups.

// Known themes and layout densities.
const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"

	DensityComfortable = "comfortable"
	DensityCompact     = "compact"
)

// Display choices of the web client (G33.2). Palette names a preset beyond
// plain light and dark; the web client keeps the theme member in step with
// the preset's color scheme, so clients that only know theme still follow it.
const (
	PaletteSepia    = "sepia"
	PaletteContrast = "contrast"

	PosterPortrait  = "portrait"
	PosterSquare    = "square"
	PosterLandscape = "landscape"

	PosterSizeSmall  = "small"
	PosterSizeMedium = "medium"
	PosterSizeLarge  = "large"

	ListDensityComfortable = "comfortable"
	ListDensityCompact     = "compact"

	ViewPoster = "poster"
	ViewList   = "list"
)

// UserDisplay holds display choices: a user's own, or the site default for
// users who did not choose (SiteAppearance.DefaultDisplay). Every member is
// optional and nil follows the next layer member by member: the user's
// choice, then the site default, then the web client's built-in value.
// Palette belongs with the theme: a user's nil palette is plain light or dark
// as the theme member says, and the site default palette reaches only users
// who never saved preferences, like the site default theme.
type UserDisplay struct {
	Palette     *string `json:"palette"`
	PosterRatio *string `json:"posterRatio"`
	PosterSize  *string `json:"posterSize"`
	ListDensity *string `json:"listDensity"`
	View        *string `json:"view"`
}

// UserDisplayMaxBytes bounds the encoded display members (database check).
const UserDisplayMaxBytes = 1 << 10

func displayMember(value *string, allowed ...string) bool {
	if value == nil {
		return true
	}
	for _, candidate := range allowed {
		if *value == candidate {
			return true
		}
	}
	return false
}

// Valid reports whether every member is unset or holds a known value.
func (d UserDisplay) Valid() bool {
	return displayMember(d.Palette, PaletteSepia, PaletteContrast) &&
		displayMember(d.PosterRatio, PosterPortrait, PosterSquare, PosterLandscape) &&
		displayMember(d.PosterSize, PosterSizeSmall, PosterSizeMedium, PosterSizeLarge) &&
		displayMember(d.ListDensity, ListDensityComfortable, ListDensityCompact) &&
		displayMember(d.View, ViewPoster, ViewList)
}

// Empty reports whether no member is set.
func (d UserDisplay) Empty() bool {
	return d.Palette == nil && d.PosterRatio == nil && d.PosterSize == nil && d.ListDensity == nil && d.View == nil
}

// NormalizeDisplay validates optional display choices; a document without
// any member set is the same as none (nil).
func NormalizeDisplay(d *UserDisplay) (*UserDisplay, bool) {
	if d == nil || d.Empty() {
		return nil, d == nil || d.Valid()
	}
	if !d.Valid() {
		return nil, false
	}
	out := *d
	return &out, true
}

// UserPreferences are a user's stored interface preferences. A user who
// never saved any reads DefaultUserPreferences.
type UserPreferences struct {
	// Theme is system (follow the browser's color scheme), light or dark.
	Theme string `json:"theme"`
	// Density is reserved for the web client's layout density.
	Density string `json:"density"`
	// Layout is the user's page layout and saved presets; nil until the
	// user customizes it, when the site default layout applies.
	Layout *UserLayout `json:"layout"`
	// Display is the user's display choices; nil until the user saves any.
	Display *UserDisplay `json:"display"`
}

// DefaultUserPreferences follow the browser and the regular layout. A
// user without stored preferences reads the site's default theme instead.
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
	if p.Density != DensityComfortable && p.Density != DensityCompact {
		return false
	}
	if p.Display != nil && !p.Display.Valid() {
		return false
	}
	return p.Layout == nil || p.Layout.Valid()
}
