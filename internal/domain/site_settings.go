package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Site-wide web client settings (G32.4, G33.2–G33.5). Administrators set the
// appearance (default theme, design token overrides, custom CSS, external
// font allowlist, default page layout) and the plugin configuration
// (enabled plugins in order and each plugin's settings namespace). Every
// signed-in user reads the effective values, without revision data or the
// raw CSS. Both documents are audited and exported together as one JSON
// file (G33.3).

// AnyRevision skips the optimistic revision check (reset and import).
const AnyRevision int64 = -1

// ThemeTokenNames are the design tokens an administrator may override; the
// same names plugins may set through theme.token (web/src/plugins/sdk/hooks.ts).
var ThemeTokenNames = []string{
	"color-bg", "color-surface", "color-text", "color-text-muted", "color-border",
	"color-primary", "color-primary-text", "color-focus", "color-badge-bg",
	"color-info-bg", "color-chart", "radius-sm", "radius-md", "font-family",
}

// SiteTokens overrides design tokens for the light and the dark scheme.
type SiteTokens struct {
	Light map[string]string `json:"light"`
	Dark  map[string]string `json:"dark"`
}

// LayoutEntry is one block or panel of a page layout.
type LayoutEntry struct {
	ID      string `json:"id"`
	Visible bool   `json:"visible"`
}

// PageLayout orders the home page blocks and the item page panels. Block
// IDs belong to the web client; the server only bounds their shape, so a
// newer client can add blocks without a schema change.
type PageLayout struct {
	Home   []LayoutEntry `json:"home"`
	Detail []LayoutEntry `json:"detail"`
}

// LayoutPreset is a named layout a user saved.
type LayoutPreset struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Layout PageLayout `json:"layout"`
}

// UserLayout is a user's current layout and saved presets (G33.5).
type UserLayout struct {
	Current PageLayout     `json:"current"`
	Presets []LayoutPreset `json:"presets"`
}

// Layout limits.
const (
	LayoutAreaLimit      = 32
	LayoutPresetLimit    = 10
	LayoutPresetNameMax  = 40
	UserLayoutMaxBytes   = 16 << 10
	SiteTokensMaxBytes   = 8 << 10
	SitePluginLimit      = 64
	PluginSettingsMaxKey = 64
	// PluginSettingsMaxBytes bounds one plugin's settings namespace, the
	// same limit the web client enforces before saving.
	PluginSettingsMaxBytes = 16 << 10
	pluginSettingsMaxDepth = 8
)

var (
	layoutIDPattern       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
	layoutPresetIDPattern = regexp.MustCompile(`^custom-[0-9]{1,6}$`)
	pluginIDPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}(?:\.[a-z][a-z0-9-]{0,31}){1,3}$`)
	pluginSettingKey      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
)

func validLayoutArea(entries []LayoutEntry) bool {
	if entries == nil || len(entries) > LayoutAreaLimit {
		return false
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !layoutIDPattern.MatchString(entry.ID) || seen[entry.ID] {
			return false
		}
		seen[entry.ID] = true
	}
	return true
}

// Valid reports whether both areas are present, bounded and free of duplicates.
func (l PageLayout) Valid() bool { return validLayoutArea(l.Home) && validLayoutArea(l.Detail) }

func validPresetName(name string) bool {
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name || name == "" || utf8.RuneCountInString(name) > LayoutPresetNameMax {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == 0x2028 || r == 0x2029 {
			return false
		}
	}
	return true
}

// Valid bounds a user layout's structure and encoded size.
func (l UserLayout) Valid() bool {
	if !l.Current.Valid() || l.Presets == nil || len(l.Presets) > LayoutPresetLimit {
		return false
	}
	seen := map[string]bool{}
	for _, preset := range l.Presets {
		if !layoutPresetIDPattern.MatchString(preset.ID) || seen[preset.ID] || !validPresetName(preset.Name) || !preset.Layout.Valid() {
			return false
		}
		seen[preset.ID] = true
	}
	data, err := json.Marshal(l)
	return err == nil && len(data) <= UserLayoutMaxBytes
}

// SiteAppearance is the administrator's appearance document (G33.2–G33.5).
type SiteAppearance struct {
	// DefaultTheme applies to users who never saved their own theme.
	DefaultTheme string     `json:"defaultTheme"`
	Tokens       SiteTokens `json:"tokens"`
	// CustomCSS is the administrator's text as entered. Only its sanitized
	// form ever reaches other users.
	CustomCSS          string   `json:"customCss"`
	AllowExternalFonts bool     `json:"allowExternalFonts"`
	FontHosts          []string `json:"fontHosts"`
	// DefaultLayout applies to users who never saved a layout; nil keeps
	// the web client's built-in standard layout.
	DefaultLayout *PageLayout `json:"defaultLayout"`
}

// DefaultSiteAppearance is the state of a new installation and of a reset.
func DefaultSiteAppearance() SiteAppearance {
	return SiteAppearance{DefaultTheme: ThemeSystem, Tokens: SiteTokens{Light: map[string]string{}, Dark: map[string]string{}}, FontHosts: []string{}}
}

// SiteAppearanceRecord is the stored appearance with its revision.
type SiteAppearanceRecord struct {
	SiteAppearance
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SiteAppearanceConfig is the administrator's view: the stored document,
// its revision and what the sanitizer removed from the CSS.
type SiteAppearanceConfig struct {
	SiteAppearanceRecord
	CSSIssues []CSSIssue `json:"cssIssues"`
}

// SiteAppearanceView is what every signed-in user reads: sanitized CSS,
// the effective font hosts and no revision data.
type SiteAppearanceView struct {
	DefaultTheme  string      `json:"defaultTheme"`
	Tokens        SiteTokens  `json:"tokens"`
	CSS           string      `json:"css"`
	FontHosts     []string    `json:"fontHosts"`
	DefaultLayout *PageLayout `json:"defaultLayout"`
}

func (a SiteAppearance) policy() CSSPolicy {
	if !a.AllowExternalFonts {
		return CSSPolicy{}
	}
	return CSSPolicy{AllowExternalFonts: true, FontHosts: a.FontHosts}
}

// EffectiveFontHosts are the hosts fonts may be loaded from (CSP font-src).
func (a SiteAppearance) EffectiveFontHosts() []string {
	if !a.AllowExternalFonts {
		return []string{}
	}
	return append([]string{}, a.FontHosts...)
}

// Config adds the sanitizer's findings for the administrator.
func (r SiteAppearanceRecord) Config() SiteAppearanceConfig {
	issues := SanitizeCustomCSS(r.CustomCSS, r.policy()).Issues
	if issues == nil {
		issues = []CSSIssue{}
	}
	return SiteAppearanceConfig{SiteAppearanceRecord: r, CSSIssues: issues}
}

// View is the effective appearance. The CSS is sanitized on every read, so
// a stricter sanitizer applies to text stored by an older release.
func (a SiteAppearance) View() SiteAppearanceView {
	return SiteAppearanceView{DefaultTheme: a.DefaultTheme, Tokens: a.Tokens, CSS: SanitizeCustomCSS(a.CustomCSS, a.policy()).CSS, FontHosts: a.EffectiveFontHosts(), DefaultLayout: a.DefaultLayout}
}

func normalizeTokens(in map[string]string) (map[string]string, bool) {
	if in == nil {
		return nil, false
	}
	out := make(map[string]string, len(in))
	for name, value := range in {
		known := false
		for _, candidate := range ThemeTokenNames {
			known = known || candidate == name
		}
		safe, ok := SanitizeTokenValue(value, name == "font-family")
		if !known || !ok {
			return nil, false
		}
		out[name] = safe
	}
	return out, true
}

// NormalizeSiteAppearance validates an appearance document and returns it
// in canonical form (trimmed token values, lower-case hosts). Structural
// CSS problems are ErrCustomCSSRejected; everything else is ErrInvalid.
// Individual rules the sanitizer drops are not an error: they are reported
// to the administrator and never reach other users.
func NormalizeSiteAppearance(a SiteAppearance) (SiteAppearance, error) {
	switch a.DefaultTheme {
	case ThemeSystem, ThemeLight, ThemeDark:
	default:
		return SiteAppearance{}, ErrInvalid
	}
	light, okLight := normalizeTokens(a.Tokens.Light)
	dark, okDark := normalizeTokens(a.Tokens.Dark)
	if !okLight || !okDark {
		return SiteAppearance{}, ErrInvalid
	}
	tokens := SiteTokens{Light: light, Dark: dark}
	if data, err := json.Marshal(tokens); err != nil || len(data) > SiteTokensMaxBytes {
		return SiteAppearance{}, ErrInvalid
	}
	if a.FontHosts == nil || len(a.FontHosts) > FontHostLimit {
		return SiteAppearance{}, ErrInvalid
	}
	hosts := make([]string, 0, len(a.FontHosts))
	for _, raw := range a.FontHosts {
		host, ok := NormalizeFontHost(raw)
		if !ok {
			return SiteAppearance{}, ErrInvalid
		}
		for _, existing := range hosts {
			if existing == host {
				return SiteAppearance{}, ErrInvalid
			}
		}
		hosts = append(hosts, host)
	}
	if a.DefaultLayout != nil && !a.DefaultLayout.Valid() {
		return SiteAppearance{}, ErrInvalid
	}
	if !utf8.ValidString(a.CustomCSS) {
		return SiteAppearance{}, ErrInvalid
	}
	out := SiteAppearance{DefaultTheme: a.DefaultTheme, Tokens: tokens, CustomCSS: a.CustomCSS, AllowExternalFonts: a.AllowExternalFonts, FontHosts: hosts, DefaultLayout: a.DefaultLayout}
	if SanitizeCustomCSS(out.CustomCSS, out.policy()).Rejected {
		return SiteAppearance{}, ErrCustomCSSRejected
	}
	return out, nil
}

// SitePluginState enables or disables one plugin; the list order is the
// administrator's plugin order. Plugins not listed keep their bundled default.
type SitePluginState struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// SitePlugins is the administrator's plugin document (G32.4).
type SitePlugins struct {
	Plugins []SitePluginState `json:"plugins"`
	// Settings holds one JSON object per plugin ID: the plugin's own
	// settings namespace.
	Settings map[string]json.RawMessage `json:"settings"`
}

// DefaultSitePlugins lists nothing: every bundled plugin keeps its default.
func DefaultSitePlugins() SitePlugins {
	return SitePlugins{Plugins: []SitePluginState{}, Settings: map[string]json.RawMessage{}}
}

// SitePluginsRecord is the stored plugin document with its revision.
type SitePluginsRecord struct {
	SitePlugins
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// View is what every signed-in user reads: the plugin list and the
// settings of plugins that are not disabled. Plugins run in every user's
// browser and need their settings, so settings are not secret.
func (p SitePlugins) View() SitePlugins {
	disabled := map[string]bool{}
	for _, state := range p.Plugins {
		disabled[state.ID] = !state.Enabled
	}
	settings := make(map[string]json.RawMessage, len(p.Settings))
	for id, value := range p.Settings {
		if !disabled[id] {
			settings[id] = value
		}
	}
	return SitePlugins{Plugins: append([]SitePluginState{}, p.Plugins...), Settings: settings}
}

// ValidPluginID matches the manifest ID pattern of the plugin SDK.
func ValidPluginID(id string) bool { return pluginIDPattern.MatchString(id) }

// pluginSettingValue walks one decoded JSON value like the web client's
// isSettingValue: JSON scalars, arrays and objects with setting keys, at
// most eight levels deep.
func pluginSettingValue(value any, depth int) bool {
	if depth > pluginSettingsMaxDepth {
		return false
	}
	switch v := value.(type) {
	case nil, string, bool, json.Number:
		return true
	case []any:
		for _, entry := range v {
			if !pluginSettingValue(entry, depth+1) {
				return false
			}
		}
		return true
	case map[string]any:
		for key, entry := range v {
			if !pluginSettingKey.MatchString(key) || !pluginSettingValue(entry, depth+1) {
				return false
			}
		}
		return true
	}
	return false
}

// normalizePluginSettings checks one namespace and returns it compacted.
func normalizePluginSettings(raw json.RawMessage) (json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil || values == nil || len(values) > PluginSettingsMaxKey {
		return nil, false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	for key, value := range values {
		if !pluginSettingKey.MatchString(key) || !pluginSettingValue(value, 0) {
			return nil, false
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil || compact.Len() > PluginSettingsMaxBytes {
		return nil, false
	}
	return compact.Bytes(), true
}

// NormalizeSitePlugins validates a plugin document: plugin IDs, no
// duplicates, at most 64 plugins and 64 settings namespaces, each namespace
// a JSON object of at most 16 KiB.
func NormalizeSitePlugins(p SitePlugins) (SitePlugins, error) {
	if p.Plugins == nil || p.Settings == nil || len(p.Plugins) > SitePluginLimit || len(p.Settings) > SitePluginLimit {
		return SitePlugins{}, ErrInvalid
	}
	seen := map[string]bool{}
	plugins := make([]SitePluginState, 0, len(p.Plugins))
	for _, state := range p.Plugins {
		if !ValidPluginID(state.ID) || seen[state.ID] {
			return SitePlugins{}, ErrInvalid
		}
		seen[state.ID] = true
		plugins = append(plugins, state)
	}
	settings := make(map[string]json.RawMessage, len(p.Settings))
	for id, raw := range p.Settings {
		if !ValidPluginID(id) {
			return SitePlugins{}, ErrInvalid
		}
		value, ok := normalizePluginSettings(raw)
		if !ok {
			return SitePlugins{}, ErrInvalid
		}
		settings[id] = value
	}
	return SitePlugins{Plugins: plugins, Settings: settings}, nil
}

// PluginSettingIDs lists the settings namespaces in a stable order.
func (p SitePlugins) PluginSettingIDs() []string {
	ids := make([]string, 0, len(p.Settings))
	for id := range p.Settings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// SiteSettingsFormat names the export document (G33.3).
const (
	SiteSettingsFormat        = "jelee.site-settings"
	SiteSettingsFormatVersion = 1
)

// SiteSettingsDocument is the exported and imported JSON file.
type SiteSettingsDocument struct {
	Format     string         `json:"format"`
	Version    int            `json:"version"`
	ExportedAt *time.Time     `json:"exportedAt,omitempty"`
	Appearance SiteAppearance `json:"appearance"`
	Plugins    SitePlugins    `json:"plugins"`
}

// SiteSettingsImport is the result of an import: both stored documents.
type SiteSettingsImport struct {
	Appearance SiteAppearanceConfig `json:"appearance"`
	Plugins    SitePluginsRecord    `json:"plugins"`
}
