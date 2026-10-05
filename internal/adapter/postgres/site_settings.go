package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Site-wide web client settings (G32.4, G33.2–G33.5): two single-row
// documents. Reads need a live session (administrators for the stored
// documents with revision data, any user for the effective values that the
// application derives); writes need an administrator, check the expected
// revision and are audited in the same transaction. An unchanged write is a
// no-op without a new revision or audit record.

const siteAppearanceColumns = `default_theme,tokens,custom_css,allow_external_fonts,font_hosts,default_layout,revision,updated_at`

func scanSiteAppearance(row pgx.Row) (domain.SiteAppearanceRecord, error) {
	var r domain.SiteAppearanceRecord
	var tokens, layout []byte
	if err := row.Scan(&r.DefaultTheme, &tokens, &r.CustomCSS, &r.AllowExternalFonts, &r.FontHosts, &layout, &r.Revision, &r.UpdatedAt); err != nil {
		return r, storageError(err)
	}
	if err := json.Unmarshal(tokens, &r.Tokens); err != nil || r.Tokens.Light == nil || r.Tokens.Dark == nil {
		return r, domain.ErrDatabase
	}
	if layout != nil {
		r.DefaultLayout = &domain.PageLayout{}
		if err := json.Unmarshal(layout, r.DefaultLayout); err != nil {
			return r, domain.ErrDatabase
		}
	}
	if r.FontHosts == nil {
		r.FontHosts = []string{}
	}
	r.UpdatedAt = r.UpdatedAt.UTC()
	return r, nil
}

const sitePluginsColumns = `plugins,settings,revision,updated_at`

func scanSitePlugins(row pgx.Row) (domain.SitePluginsRecord, error) {
	var r domain.SitePluginsRecord
	var plugins, settings []byte
	if err := row.Scan(&plugins, &settings, &r.Revision, &r.UpdatedAt); err != nil {
		return r, storageError(err)
	}
	if err := json.Unmarshal(plugins, &r.Plugins); err != nil || r.Plugins == nil {
		return r, domain.ErrDatabase
	}
	if err := json.Unmarshal(settings, &r.Settings); err != nil || r.Settings == nil {
		return r, domain.ErrDatabase
	}
	r.UpdatedAt = r.UpdatedAt.UTC()
	return r, nil
}

// GetSiteAppearance reads the stored appearance; adminOnly refuses other users.
func (s *Store) GetSiteAppearance(ctx context.Context, actor domain.Actor, adminOnly bool) (domain.SiteAppearanceRecord, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, adminOnly)
	if err != nil {
		return domain.SiteAppearanceRecord{}, err
	}
	defer tx.Rollback(ctx)
	r, err := scanSiteAppearance(tx.QueryRow(ctx, `SELECT `+siteAppearanceColumns+` FROM site_appearance WHERE id`))
	if err != nil {
		return r, err
	}
	return r, storageError(tx.Commit(ctx))
}

// GetSitePlugins reads the stored plugin document; adminOnly refuses other users.
func (s *Store) GetSitePlugins(ctx context.Context, actor domain.Actor, adminOnly bool) (domain.SitePluginsRecord, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, adminOnly)
	if err != nil {
		return domain.SitePluginsRecord{}, err
	}
	defer tx.Rollback(ctx)
	r, err := scanSitePlugins(tx.QueryRow(ctx, `SELECT `+sitePluginsColumns+` FROM site_plugins WHERE id`))
	if err != nil {
		return r, err
	}
	return r, storageError(tx.Commit(ctx))
}

// GetSiteSettings reads both documents in one snapshot (export).
func (s *Store) GetSiteSettings(ctx context.Context, actor domain.Actor) (domain.SiteAppearanceRecord, domain.SitePluginsRecord, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.SiteAppearanceRecord{}, domain.SitePluginsRecord{}, err
	}
	defer tx.Rollback(ctx)
	appearance, err := scanSiteAppearance(tx.QueryRow(ctx, `SELECT `+siteAppearanceColumns+` FROM site_appearance WHERE id`))
	if err != nil {
		return appearance, domain.SitePluginsRecord{}, err
	}
	plugins, err := scanSitePlugins(tx.QueryRow(ctx, `SELECT `+sitePluginsColumns+` FROM site_plugins WHERE id`))
	if err != nil {
		return appearance, plugins, err
	}
	return appearance, plugins, storageError(tx.Commit(ctx))
}

// SiteFontHosts reads the effective external font hosts for the frontend
// Content-Security-Policy. It is a system read without an actor: the hosts
// are public through that header anyway.
func (s *Store) SiteFontHosts(ctx context.Context) ([]string, error) {
	var hosts []string
	if err := s.Pool.QueryRow(ctx, `SELECT CASE WHEN allow_external_fonts THEN font_hosts ELSE '{}'::text[] END FROM site_appearance WHERE id`).Scan(&hosts); err != nil {
		return nil, storageError(err)
	}
	if hosts == nil {
		hosts = []string{}
	}
	return hosts, nil
}

// SetSiteAppearance replaces the appearance document. revision must equal
// the stored one (domain.AnyRevision skips the check); source names the
// operation in the audit record: update, reset or import.
func (s *Store) SetSiteAppearance(ctx context.Context, actor domain.Actor, in domain.SiteAppearance, revision int64, source string) (domain.SiteAppearanceRecord, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.SiteAppearanceRecord{}, err
	}
	defer tx.Rollback(ctx)
	r, err := setSiteAppearance(ctx, tx, actor, in, revision, source)
	if err != nil {
		return r, err
	}
	return r, storageError(tx.Commit(ctx))
}

// SetSitePlugins replaces the plugin document like SetSiteAppearance.
func (s *Store) SetSitePlugins(ctx context.Context, actor domain.Actor, in domain.SitePlugins, revision int64, source string) (domain.SitePluginsRecord, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.SitePluginsRecord{}, err
	}
	defer tx.Rollback(ctx)
	r, err := setSitePlugins(ctx, tx, actor, in, revision, source)
	if err != nil {
		return r, err
	}
	return r, storageError(tx.Commit(ctx))
}

// ImportSiteSettings replaces both documents atomically, without revision checks.
func (s *Store) ImportSiteSettings(ctx context.Context, actor domain.Actor, appearance domain.SiteAppearance, plugins domain.SitePlugins) (domain.SiteAppearanceRecord, domain.SitePluginsRecord, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.SiteAppearanceRecord{}, domain.SitePluginsRecord{}, err
	}
	defer tx.Rollback(ctx)
	a, err := setSiteAppearance(ctx, tx, actor, appearance, domain.AnyRevision, "import")
	if err != nil {
		return a, domain.SitePluginsRecord{}, err
	}
	p, err := setSitePlugins(ctx, tx, actor, plugins, domain.AnyRevision, "import")
	if err != nil {
		return a, p, err
	}
	return a, p, storageError(tx.Commit(ctx))
}

func validSiteSource(source string) bool {
	return source == "update" || source == "reset" || source == "import"
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// siteAppearanceAudit summarizes an appearance document: the CSS by size
// and digest, since it may be larger than an audit state.
func siteAppearanceAudit(a domain.SiteAppearance) map[string]any {
	layout := "standard"
	if a.DefaultLayout != nil {
		data, _ := json.Marshal(a.DefaultLayout)
		layout = digest(data)
	}
	return map[string]any{"defaultTheme": a.DefaultTheme, "tokens": a.Tokens, "cssBytes": len(a.CustomCSS), "cssSha256": digest([]byte(a.CustomCSS)),
		"allowExternalFonts": a.AllowExternalFonts, "fontHosts": a.FontHosts, "defaultLayout": layout}
}

// sitePluginsAudit records the plugin list and a digest per settings
// namespace: settings belong to plugins and are not copied into the log.
func sitePluginsAudit(p domain.SitePlugins) map[string]any {
	settings := map[string]any{}
	for _, id := range p.PluginSettingIDs() {
		settings[id] = digest(p.Settings[id])
	}
	return map[string]any{"plugins": p.Plugins, "settingsSha256": settings}
}

func setSiteAppearance(ctx context.Context, tx pgx.Tx, actor domain.Actor, in domain.SiteAppearance, revision int64, source string) (domain.SiteAppearanceRecord, error) {
	normalized, err := domain.NormalizeSiteAppearance(in)
	if err != nil {
		return domain.SiteAppearanceRecord{}, err
	}
	if !validSiteSource(source) || revision < domain.AnyRevision || !utf8.ValidString(normalized.CustomCSS) {
		return domain.SiteAppearanceRecord{}, domain.ErrInvalid
	}
	old, err := scanSiteAppearance(tx.QueryRow(ctx, `SELECT `+siteAppearanceColumns+` FROM site_appearance WHERE id FOR UPDATE`))
	if err != nil {
		return old, err
	}
	if revision != domain.AnyRevision && revision != old.Revision {
		return old, domain.ErrConflict
	}
	tokens, err := json.Marshal(normalized.Tokens)
	if err != nil {
		return old, domain.ErrInvalid
	}
	var layout []byte
	if normalized.DefaultLayout != nil {
		if layout, err = json.Marshal(normalized.DefaultLayout); err != nil {
			return old, domain.ErrInvalid
		}
	}
	before, after := siteAppearanceAudit(old.SiteAppearance), siteAppearanceAudit(normalized)
	if sameJSON(before, after) {
		return old, nil
	}
	r, err := scanSiteAppearance(tx.QueryRow(ctx, `UPDATE site_appearance SET default_theme=$1,tokens=$2::jsonb,custom_css=$3,allow_external_fonts=$4,font_hosts=$5,default_layout=$6::jsonb,revision=revision+1,updated_at=now() WHERE id RETURNING `+siteAppearanceColumns,
		normalized.DefaultTheme, string(tokens), normalized.CustomCSS, normalized.AllowExternalFonts, normalized.FontHosts, nullableJSON(layout)))
	if err != nil {
		return r, err
	}
	after["source"] = source
	if err = appendAudit(ctx, tx, AuditEntry{Event: "site.appearance_changed", Actor: actor, TargetRef: "site_appearance", Before: before, After: after}); err != nil {
		return r, err
	}
	return r, nil
}

func setSitePlugins(ctx context.Context, tx pgx.Tx, actor domain.Actor, in domain.SitePlugins, revision int64, source string) (domain.SitePluginsRecord, error) {
	normalized, err := domain.NormalizeSitePlugins(in)
	if err != nil {
		return domain.SitePluginsRecord{}, err
	}
	if !validSiteSource(source) || revision < domain.AnyRevision {
		return domain.SitePluginsRecord{}, domain.ErrInvalid
	}
	old, err := scanSitePlugins(tx.QueryRow(ctx, `SELECT `+sitePluginsColumns+` FROM site_plugins WHERE id FOR UPDATE`))
	if err != nil {
		return old, err
	}
	if revision != domain.AnyRevision && revision != old.Revision {
		return old, domain.ErrConflict
	}
	plugins, err := json.Marshal(normalized.Plugins)
	if err != nil {
		return old, domain.ErrInvalid
	}
	settings, err := json.Marshal(normalized.Settings)
	if err != nil {
		return old, domain.ErrInvalid
	}
	// Stored settings come back from jsonb with normalized spacing and key
	// order; compare through the same normalization.
	var unchanged bool
	if err = tx.QueryRow(ctx, `SELECT plugins=$1::jsonb AND settings=$2::jsonb FROM site_plugins WHERE id`, string(plugins), string(settings)).Scan(&unchanged); err != nil {
		return old, storageError(err)
	}
	if unchanged {
		return old, nil
	}
	r, err := scanSitePlugins(tx.QueryRow(ctx, `UPDATE site_plugins SET plugins=$1::jsonb,settings=$2::jsonb,revision=revision+1,updated_at=now() WHERE id RETURNING `+sitePluginsColumns, string(plugins), string(settings)))
	if err != nil {
		return r, err
	}
	after := sitePluginsAudit(r.SitePlugins)
	after["source"] = source
	if err = appendAudit(ctx, tx, AuditEntry{Event: "site.plugins_changed", Actor: actor, TargetRef: "site_plugins", Before: sitePluginsAudit(old.SitePlugins), After: after}); err != nil {
		return r, err
	}
	return r, nil
}

func nullableJSON(data []byte) any {
	if data == nil {
		return nil
	}
	return string(data)
}

func sameJSON(a, b any) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && bytes.Equal(left, right)
}
