package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Site-wide web client settings (G32.4, G33.2–G33.5). Every signed-in user
// reads the effective appearance and plugin configuration; administrators
// read the stored documents with their revision, replace them, reset them
// and move both between servers as one JSON file (G33.3).

// siteBodyLimit admits 64 Ki UTF-16 units of CSS written entirely as JSON
// escapes, or 64 plugin namespaces of 16 KiB each.
const siteBodyLimit = 2 << 20

// siteLayoutEntry requires both members: a missing "visible" must not read
// as hidden.
type siteLayoutEntry struct {
	ID      *string `json:"id"`
	Visible *bool   `json:"visible"`
}

type siteLayoutArea []siteLayoutEntry

func (a siteLayoutArea) toDomain() ([]domain.LayoutEntry, bool) {
	if a == nil {
		return nil, false
	}
	out := make([]domain.LayoutEntry, 0, len(a))
	for _, entry := range a {
		if entry.ID == nil || entry.Visible == nil {
			return nil, false
		}
		out = append(out, domain.LayoutEntry{ID: *entry.ID, Visible: *entry.Visible})
	}
	return out, true
}

type sitePageLayout struct {
	Home   siteLayoutArea `json:"home"`
	Detail siteLayoutArea `json:"detail"`
}

func (l *sitePageLayout) toDomain() (domain.PageLayout, bool) {
	if l == nil {
		return domain.PageLayout{}, false
	}
	home, okHome := l.Home.toDomain()
	detail, okDetail := l.Detail.toDomain()
	layout := domain.PageLayout{Home: home, Detail: detail}
	return layout, okHome && okDetail && layout.Valid()
}

// decodeLayoutValue reads an optional layout member: absent is an error
// (every field is required), null is nil.
func decodeLayoutValue[T any](raw json.RawMessage) (*T, bool) {
	if raw == nil {
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return &value, true
}

type siteUserLayout struct {
	Current *sitePageLayout `json:"current"`
	Presets *[]struct {
		ID     *string         `json:"id"`
		Name   *string         `json:"name"`
		Layout *sitePageLayout `json:"layout"`
	} `json:"presets"`
}

// userLayoutFrom converts the layout member of PUT /users/me/preferences.
func userLayoutFrom(raw json.RawMessage) (*domain.UserLayout, bool) {
	input, ok := decodeLayoutValue[siteUserLayout](raw)
	if !ok || input == nil {
		return nil, ok
	}
	current, ok := input.Current.toDomain()
	if !ok || input.Presets == nil {
		return nil, false
	}
	layout := domain.UserLayout{Current: current, Presets: make([]domain.LayoutPreset, 0, len(*input.Presets))}
	for _, preset := range *input.Presets {
		if preset.ID == nil || preset.Name == nil {
			return nil, false
		}
		pageLayout, ok := preset.Layout.toDomain()
		if !ok {
			return nil, false
		}
		layout.Presets = append(layout.Presets, domain.LayoutPreset{ID: *preset.ID, Name: *preset.Name, Layout: pageLayout})
	}
	return &layout, layout.Valid()
}

// siteAppearanceInput names every member of an appearance document.
type siteAppearanceInput struct {
	DefaultTheme *string `json:"defaultTheme"`
	Tokens       *struct {
		Light *map[string]string `json:"light"`
		Dark  *map[string]string `json:"dark"`
	} `json:"tokens"`
	CustomCSS          *string         `json:"customCss"`
	AllowExternalFonts *bool           `json:"allowExternalFonts"`
	FontHosts          *[]string       `json:"fontHosts"`
	DefaultLayout      json.RawMessage `json:"defaultLayout"`
}

func (in *siteAppearanceInput) toDomain() (domain.SiteAppearance, error) {
	if in == nil || in.DefaultTheme == nil || in.Tokens == nil || in.Tokens.Light == nil || in.Tokens.Dark == nil || in.CustomCSS == nil || in.AllowExternalFonts == nil || in.FontHosts == nil {
		return domain.SiteAppearance{}, domain.ErrInvalid
	}
	layoutInput, ok := decodeLayoutValue[sitePageLayout](in.DefaultLayout)
	if !ok {
		return domain.SiteAppearance{}, domain.ErrInvalid
	}
	var layout *domain.PageLayout
	if layoutInput != nil {
		value, ok := layoutInput.toDomain()
		if !ok {
			return domain.SiteAppearance{}, domain.ErrInvalid
		}
		layout = &value
	}
	return domain.SiteAppearance{DefaultTheme: *in.DefaultTheme, Tokens: domain.SiteTokens{Light: *in.Tokens.Light, Dark: *in.Tokens.Dark}, CustomCSS: *in.CustomCSS,
		AllowExternalFonts: *in.AllowExternalFonts, FontHosts: *in.FontHosts, DefaultLayout: layout}, nil
}

// sitePluginsInput names every member of a plugin document.
type sitePluginsInput struct {
	Plugins *[]struct {
		ID      *string `json:"id"`
		Enabled *bool   `json:"enabled"`
	} `json:"plugins"`
	Settings *map[string]json.RawMessage `json:"settings"`
}

func (in *sitePluginsInput) toDomain() (domain.SitePlugins, error) {
	if in == nil || in.Plugins == nil || in.Settings == nil || *in.Settings == nil {
		return domain.SitePlugins{}, domain.ErrInvalid
	}
	plugins := make([]domain.SitePluginState, 0, len(*in.Plugins))
	for _, state := range *in.Plugins {
		if state.ID == nil || state.Enabled == nil {
			return domain.SitePlugins{}, domain.ErrInvalid
		}
		plugins = append(plugins, domain.SitePluginState{ID: *state.ID, Enabled: *state.Enabled})
	}
	return domain.SitePlugins{Plugins: plugins, Settings: *in.Settings}, nil
}

// Paths where a request body may hold null (see decodeJSONNullable).
func appearanceNullable(path string) bool { return path == "/DEFAULTLAYOUT" }

// Plugin settings are JSON values; null is a value inside a namespace, but
// a namespace itself must be an object.
func pluginsNullable(path string) bool {
	return strings.HasPrefix(path, "/SETTINGS/") && strings.Count(path, "/") >= 3
}

func importNullable(path string) bool {
	return path == "/APPEARANCE/DEFAULTLAYOUT" || strings.HasPrefix(path, "/PLUGINS/SETTINGS/") && strings.Count(path, "/") >= 4
}

func (s *Server) siteSettingsRoutes(r chi.Router) {
	r.Get("/api/v1/site/appearance", s.accountEndpoint(false, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		view, err := s.accounts.SiteAppearance(r.Context(), a)
		return view, 200, err
	}))
	r.Get("/api/v1/site/appearance/config", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		config, err := s.accounts.SiteAppearanceConfig(r.Context(), a)
		return config, 200, err
	}))
	r.Put("/api/v1/site/appearance", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			siteAppearanceInput
			Revision *int64 `json:"revision"`
		}
		if err := decodeJSONNullable(w, r, &input, siteBodyLimit, appearanceNullable); err != nil {
			return nil, 0, err
		}
		appearance, err := input.siteAppearanceInput.toDomain()
		if err != nil || input.Revision == nil {
			return nil, 0, domain.ErrInvalid
		}
		config, err := s.accounts.SetSiteAppearance(r.Context(), a, appearance, *input.Revision)
		s.fontPolicyChanged(err)
		return config, 200, err
	}))
	r.Post("/api/v1/site/appearance/reset", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		config, err := s.accounts.ResetSiteAppearance(r.Context(), a)
		s.fontPolicyChanged(err)
		return config, 200, err
	}))
	r.Get("/api/v1/site/plugins", s.accountEndpoint(false, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		view, err := s.accounts.SitePlugins(r.Context(), a)
		return view, 200, err
	}))
	r.Get("/api/v1/site/plugins/config", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		config, err := s.accounts.SitePluginsConfig(r.Context(), a)
		return config, 200, err
	}))
	r.Put("/api/v1/site/plugins", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			sitePluginsInput
			Revision *int64 `json:"revision"`
		}
		if err := decodeJSONNullable(w, r, &input, siteBodyLimit, pluginsNullable); err != nil {
			return nil, 0, err
		}
		plugins, err := input.sitePluginsInput.toDomain()
		if err != nil || input.Revision == nil {
			return nil, 0, domain.ErrInvalid
		}
		config, err := s.accounts.SetSitePlugins(r.Context(), a, plugins, *input.Revision)
		return config, 200, err
	}))
	r.Post("/api/v1/site/plugins/reset", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		config, err := s.accounts.ResetSitePlugins(r.Context(), a)
		return config, 200, err
	}))
	r.Get("/api/v1/site/export", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		document, err := s.accounts.ExportSiteSettings(r.Context(), a, time.Now())
		return document, 200, err
	}))
	r.Post("/api/v1/site/import", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Format     *string              `json:"format"`
			Version    *int                 `json:"version"`
			ExportedAt *time.Time           `json:"exportedAt"`
			Appearance *siteAppearanceInput `json:"appearance"`
			Plugins    *sitePluginsInput    `json:"plugins"`
		}
		if err := decodeJSONNullable(w, r, &input, siteBodyLimit, importNullable); err != nil {
			return nil, 0, err
		}
		if input.Format == nil || input.Version == nil {
			return nil, 0, domain.ErrInvalid
		}
		appearance, err := input.Appearance.toDomain()
		if err != nil {
			return nil, 0, err
		}
		plugins, err := input.Plugins.toDomain()
		if err != nil {
			return nil, 0, err
		}
		result, err := s.accounts.ImportSiteSettings(r.Context(), a, domain.SiteSettingsDocument{Format: *input.Format, Version: *input.Version, Appearance: appearance, Plugins: plugins})
		s.fontPolicyChanged(err)
		return result, 200, err
	}))
}

// fontPolicyChanged makes this instance read the font allowlist again after
// a successful appearance write; other instances follow within the cache TTL.
func (s *Server) fontPolicyChanged(err error) {
	if err == nil && s.fontPolicy != nil {
		s.fontPolicy.invalidate()
	}
}

// frontendPolicy caches the frontend Content-Security-Policy, which depends
// on the external font allowlist in the site appearance (G33.4). The header
// is built for every web client response, so the allowlist is read at most
// once per TTL; an unreadable allowlist keeps the last good header, or the
// base policy without external fonts when none was read yet.
type frontendPolicy struct {
	load  func(context.Context) ([]string, error)
	ttl   time.Duration
	retry time.Duration
	now   func() time.Time

	mu         sync.Mutex
	value      string
	loaded     bool
	refreshing bool
	expires    time.Time
	generation uint64
}

const (
	frontendPolicyTTL     = 30 * time.Second
	frontendPolicyRetry   = 5 * time.Second
	frontendPolicyTimeout = 2 * time.Second
)

func newFrontendPolicy(load func(context.Context) ([]string, error)) *frontendPolicy {
	return &frontendPolicy{load: load, ttl: frontendPolicyTTL, retry: frontendPolicyRetry, now: time.Now}
}

// header returns the policy for one response. Only one request refreshes
// at a time; others use the previous value meanwhile.
func (p *frontendPolicy) header(ctx context.Context) string {
	p.mu.Lock()
	now := p.now()
	if p.refreshing || now.Before(p.expires) {
		value := p.value
		p.mu.Unlock()
		if value == "" {
			return frontendCSP
		}
		return value
	}
	p.refreshing = true
	generation := p.generation
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, frontendPolicyTimeout)
	hosts, err := p.load(ctx)
	cancel()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshing = false
	if err != nil {
		p.expires = now.Add(p.retry)
	} else {
		p.value, p.loaded, p.expires = frontendCSPWithFonts(hosts), true, now.Add(p.ttl)
	}
	if generation != p.generation {
		// Changed while loading: the value may predate the change.
		p.expires = time.Time{}
	}
	if p.value == "" {
		return frontendCSP
	}
	return p.value
}

func (p *frontendPolicy) invalidate() {
	p.mu.Lock()
	p.generation++
	p.expires = time.Time{}
	p.mu.Unlock()
}

// frontendCSPWithFonts adds "font-src 'self' https://<host>..." to the base
// frontend policy for each allowlisted host. Every other directive stays
// exactly as in frontendCSP. Hosts are checked again, so nothing but plain
// DNS names can reach the header; without hosts the base policy is used.
func frontendCSPWithFonts(hosts []string) string {
	sources := []string{"'self'"}
	for _, host := range hosts {
		if normalized, ok := domain.NormalizeFontHost(host); ok && normalized == host && len(sources) <= domain.FontHostLimit {
			sources = append(sources, "https://"+host)
		}
	}
	if len(sources) == 1 {
		return frontendCSP
	}
	return frontendCSP + "; font-src " + strings.Join(sources, " ")
}
