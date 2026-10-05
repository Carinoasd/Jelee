package domain

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestThemeTokenNamesMatchPluginSDK(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "plugins", "sdk", "hooks.ts"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)export const themeTokenNames = \[(.*?)\] as const`).FindSubmatch(data)
	if block == nil {
		t.Fatal("themeTokenNames not found in the SDK")
	}
	var sdk []string
	for _, m := range regexp.MustCompile(`"([a-z-]+)"`).FindAllSubmatch(block[1], -1) {
		sdk = append(sdk, string(m[1]))
	}
	if !reflect.DeepEqual(sdk, ThemeTokenNames) {
		t.Fatalf("SDK tokens %v, server %v", sdk, ThemeTokenNames)
	}
}

func layoutOf(ids ...string) PageLayout {
	entries := []LayoutEntry{}
	for _, id := range ids {
		entries = append(entries, LayoutEntry{ID: id, Visible: true})
	}
	return PageLayout{Home: entries, Detail: []LayoutEntry{}}
}

func TestNormalizeSiteAppearance(t *testing.T) {
	in := DefaultSiteAppearance()
	in.DefaultTheme = ThemeDark
	in.Tokens.Light["color-primary"] = "  #0f766e "
	in.Tokens.Dark["font-family"] = `"Noto Sans", sans-serif`
	in.FontHosts = []string{" Fonts.Example.COM "}
	layout := layoutOf("libraries", "latest")
	in.DefaultLayout = &layout
	in.CustomCSS = "a { color: red } b { background: url(https://evil.example/x) }"
	out, err := NormalizeSiteAppearance(in)
	if err != nil || out.Tokens.Light["color-primary"] != "#0f766e" || out.FontHosts[0] != "fonts.example.com" || out.CustomCSS != in.CustomCSS {
		t.Fatalf("normalize: %+v %v", out, err)
	}
	for name, change := range map[string]func(*SiteAppearance){
		"theme":            func(a *SiteAppearance) { a.DefaultTheme = "sepia" },
		"nil light tokens": func(a *SiteAppearance) { a.Tokens.Light = nil },
		"unknown token":    func(a *SiteAppearance) { a.Tokens.Dark = map[string]string{"z-index": "1"} },
		"token url":        func(a *SiteAppearance) { a.Tokens.Dark = map[string]string{"color-bg": "url(/x)"} },
		"quoted color":     func(a *SiteAppearance) { a.Tokens.Dark = map[string]string{"color-bg": `"red"`} },
		"nil hosts":        func(a *SiteAppearance) { a.FontHosts = nil },
		"host injection":   func(a *SiteAppearance) { a.FontHosts = []string{"a.example.com 'unsafe-inline'"} },
		"duplicate host":   func(a *SiteAppearance) { a.FontHosts = []string{"a.example.com", "A.EXAMPLE.COM"} },
		"ip host":          func(a *SiteAppearance) { a.FontHosts = []string{"10.0.0.1"} },
		"bad layout":       func(a *SiteAppearance) { a.DefaultLayout = &PageLayout{Home: []LayoutEntry{}} },
		"invalid utf8":     func(a *SiteAppearance) { a.CustomCSS = "a{color:red}\xff" },
	} {
		v := in
		v.Tokens = SiteTokens{Light: map[string]string{}, Dark: map[string]string{}}
		change(&v)
		if _, err := NormalizeSiteAppearance(v); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	v := in
	v.CustomCSS = "a{color:red}</style><script>alert(1)</script>"
	if _, err := NormalizeSiteAppearance(v); !errors.Is(err, ErrCustomCSSRejected) {
		t.Fatalf("hostile CSS: %v", err)
	}

	r := SiteAppearanceRecord{SiteAppearance: out, Revision: 3}
	if config := r.Config(); len(config.CSSIssues) != 1 || config.CSSIssues[0].Code != CSSIssueExternalURL {
		t.Fatalf("config issues: %+v", config.CSSIssues)
	}
	// External fonts count only while enabled.
	out.CustomCSS = "@font-face { font-family: X; src: url(https://fonts.example.com/x.woff2) }"
	if view := out.View(); len(view.FontHosts) != 0 || strings.Contains(view.CSS, "https://") {
		t.Fatalf("disabled fonts leaked: %+v", view)
	}
	out.AllowExternalFonts = true
	if view := out.View(); len(view.FontHosts) != 1 || view.CSS != "@font-face{font-family:X;src:url(https://fonts.example.com/x.woff2)}" {
		t.Fatalf("enabled fonts: %+v", view)
	}
	if empty := (SiteAppearanceRecord{SiteAppearance: DefaultSiteAppearance()}).Config(); empty.CSSIssues == nil {
		t.Fatal("issues must be an empty list, not null")
	}
}

func TestUserLayoutLimits(t *testing.T) {
	valid := UserLayout{Current: layoutOf("a", "b"), Presets: []LayoutPreset{{ID: "custom-1", Name: "Mine 我的", Layout: layoutOf("b")}}}
	if !valid.Valid() {
		t.Fatal("valid layout refused")
	}
	tooMany := make([]string, LayoutAreaLimit+1)
	for i := range tooMany {
		tooMany[i] = "block" + strings.Repeat("x", i)
	}
	presets := func(n int) []LayoutPreset {
		out := []LayoutPreset{}
		for i := 0; i < n; i++ {
			out = append(out, LayoutPreset{ID: "custom-" + strconv.Itoa(i+1), Name: "p", Layout: layoutOf()})
		}
		return out
	}
	for name, l := range map[string]UserLayout{
		"nil presets":      {Current: layoutOf()},
		"missing area":     {Current: PageLayout{Home: []LayoutEntry{}}, Presets: []LayoutPreset{}},
		"duplicate block":  {Current: layoutOf("a", "a"), Presets: []LayoutPreset{}},
		"bad block id":     {Current: layoutOf("<script>"), Presets: []LayoutPreset{}},
		"too many blocks":  {Current: layoutOf(tooMany...), Presets: []LayoutPreset{}},
		"too many presets": {Current: layoutOf(), Presets: presets(LayoutPresetLimit + 1)},
		"bad preset id":    {Current: layoutOf(), Presets: []LayoutPreset{{ID: "x", Name: "p", Layout: layoutOf()}}},
		"duplicate preset": {Current: layoutOf(), Presets: []LayoutPreset{{ID: "custom-1", Name: "p", Layout: layoutOf()}, {ID: "custom-1", Name: "q", Layout: layoutOf()}}},
		"empty name":       {Current: layoutOf(), Presets: []LayoutPreset{{ID: "custom-1", Name: "", Layout: layoutOf()}}},
		"padded name":      {Current: layoutOf(), Presets: []LayoutPreset{{ID: "custom-1", Name: " p", Layout: layoutOf()}}},
		"control name":     {Current: layoutOf(), Presets: []LayoutPreset{{ID: "custom-1", Name: "a b", Layout: layoutOf()}}},
		"long name":        {Current: layoutOf(), Presets: []LayoutPreset{{ID: "custom-1", Name: strings.Repeat("名", LayoutPresetNameMax+1), Layout: layoutOf()}}},
	} {
		if l.Valid() {
			t.Errorf("%s accepted", name)
		}
	}
	if !(UserLayout{Current: layoutOf(), Presets: presets(LayoutPresetLimit)}).Valid() || !(UserLayout{Current: layoutOf(), Presets: []LayoutPreset{{ID: "custom-1", Name: strings.Repeat("名", LayoutPresetNameMax), Layout: layoutOf()}}}).Valid() {
		t.Fatal("name at the limit refused")
	}
	// The encoded size is bounded too: ten presets of 32 long IDs exceed 16 KiB.
	long := make([]string, LayoutAreaLimit)
	for i := range long {
		long[i] = "b" + strings.Repeat("x", 60) + string(rune('A'+i))
	}
	big := UserLayout{Current: layoutOf(long...), Presets: []LayoutPreset{}}
	for i := 0; i < LayoutPresetLimit; i++ {
		big.Presets = append(big.Presets, LayoutPreset{ID: "custom-" + string(rune('0'+i)), Name: "p", Layout: layoutOf(long...)})
	}
	if big.Valid() {
		t.Fatal("oversized layout accepted")
	}
	prefs := UserPreferences{Theme: ThemeDark, Density: DensityCompact, Layout: &UserLayout{}}
	if prefs.Valid() {
		t.Fatal("preferences accepted an invalid layout")
	}
}

func TestNormalizeSitePlugins(t *testing.T) {
	in := SitePlugins{Plugins: []SitePluginState{{ID: "jelee.accent-tokens", Enabled: true}}, Settings: map[string]json.RawMessage{"jelee.accent-tokens": json.RawMessage(`{ "accent": "teal", "n": 1.50, "x": null, "deep": {"a": [true]} }`)}}
	out, err := NormalizeSitePlugins(in)
	if err != nil || string(out.Settings["jelee.accent-tokens"]) != `{"accent":"teal","n":1.50,"x":null,"deep":{"a":[true]}}` {
		t.Fatalf("normalize: %s %v", out.Settings["jelee.accent-tokens"], err)
	}
	deep := strings.Repeat("[", 9) + "1" + strings.Repeat("]", 9)
	ok := strings.Repeat("[", 8) + "1" + strings.Repeat("]", 8)
	if _, err := NormalizeSitePlugins(SitePlugins{Plugins: []SitePluginState{}, Settings: map[string]json.RawMessage{"a.b": json.RawMessage(`{"k":` + ok + `}`)}}); err != nil {
		t.Fatalf("eight levels refused: %v", err)
	}
	keys := map[string]any{}
	for i := 0; i <= PluginSettingsMaxKey; i++ {
		keys["k"+strings.Repeat("x", i)] = 1
	}
	tooManyKeys, _ := json.Marshal(keys)
	for name, raw := range map[string]string{
		"null":           `null`,
		"array":          `[]`,
		"string":         `"x"`,
		"two values":     `{} {}`,
		"bad key":        `{"1x":1}`,
		"nested bad key": `{"a":{"b c":1}}`,
		"too deep":       `{"k":` + deep + `}`,
		"too many keys":  string(tooManyKeys),
		"too large":      `{"k":"` + strings.Repeat("x", PluginSettingsMaxBytes) + `"}`,
	} {
		if _, err := NormalizeSitePlugins(SitePlugins{Plugins: []SitePluginState{}, Settings: map[string]json.RawMessage{"a.b": json.RawMessage(raw)}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	many := SitePlugins{Plugins: []SitePluginState{}, Settings: map[string]json.RawMessage{}}
	for i := 0; i <= SitePluginLimit; i++ {
		many.Plugins = append(many.Plugins, SitePluginState{ID: "a.p" + strings.Repeat("x", i%30) + string(rune('a'+i/30)), Enabled: true})
	}
	for name, p := range map[string]SitePlugins{
		"nil plugins":   {Settings: map[string]json.RawMessage{}},
		"nil settings":  {Plugins: []SitePluginState{}},
		"bad id":        {Plugins: []SitePluginState{{ID: "noperiod"}}, Settings: map[string]json.RawMessage{}},
		"upper id":      {Plugins: []SitePluginState{{ID: "A.b"}}, Settings: map[string]json.RawMessage{}},
		"duplicate":     {Plugins: []SitePluginState{{ID: "a.b"}, {ID: "a.b", Enabled: true}}, Settings: map[string]json.RawMessage{}},
		"bad namespace": {Plugins: []SitePluginState{}, Settings: map[string]json.RawMessage{"x": json.RawMessage(`{}`)}},
		"too many":      many,
	} {
		if _, err := NormalizeSitePlugins(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if ids := (SitePlugins{Settings: map[string]json.RawMessage{"b.b": nil, "a.a": nil}}).PluginSettingIDs(); strings.Join(ids, ",") != "a.a,b.b" {
		t.Fatal(ids)
	}
}
