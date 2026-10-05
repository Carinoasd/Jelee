package app

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Site-wide web client settings (G32.4, G33.2–G33.5). Every signed-in user
// reads the effective values; administrators read and replace the stored
// documents. Input is validated and the CSS sanitized here before storage,
// and the CSS is sanitized again whenever it is served, so text stored by a
// request that bypassed the web client never reaches another browser.

// SiteAppearance is the effective appearance for any signed-in user.
func (a *Accounts) SiteAppearance(ctx context.Context, actor domain.Actor) (domain.SiteAppearanceView, error) {
	if !validActor(actor) {
		return domain.SiteAppearanceView{}, domain.ErrInvalid
	}
	r, err := a.repository.GetSiteAppearance(ctx, actor, false)
	if err != nil {
		return domain.SiteAppearanceView{}, err
	}
	return r.View(), nil
}

// SiteAppearanceConfig is the administrator's stored appearance document.
func (a *Accounts) SiteAppearanceConfig(ctx context.Context, actor domain.Actor) (domain.SiteAppearanceConfig, error) {
	if !validActor(actor) {
		return domain.SiteAppearanceConfig{}, domain.ErrInvalid
	}
	r, err := a.repository.GetSiteAppearance(ctx, actor, true)
	if err != nil {
		return domain.SiteAppearanceConfig{}, err
	}
	return r.Config(), nil
}

// SetSiteAppearance replaces the appearance when revision is current.
func (a *Accounts) SetSiteAppearance(ctx context.Context, actor domain.Actor, in domain.SiteAppearance, revision int64) (domain.SiteAppearanceConfig, error) {
	if !validActor(actor) || revision < 0 {
		return domain.SiteAppearanceConfig{}, domain.ErrInvalid
	}
	normalized, err := domain.NormalizeSiteAppearance(in)
	if err != nil {
		return domain.SiteAppearanceConfig{}, err
	}
	r, err := a.repository.SetSiteAppearance(ctx, actor, normalized, revision, "update")
	if err != nil {
		return domain.SiteAppearanceConfig{}, err
	}
	return r.Config(), nil
}

// ResetSiteAppearance restores the default appearance (G33.3).
func (a *Accounts) ResetSiteAppearance(ctx context.Context, actor domain.Actor) (domain.SiteAppearanceConfig, error) {
	if !validActor(actor) {
		return domain.SiteAppearanceConfig{}, domain.ErrInvalid
	}
	r, err := a.repository.SetSiteAppearance(ctx, actor, domain.DefaultSiteAppearance(), domain.AnyRevision, "reset")
	if err != nil {
		return domain.SiteAppearanceConfig{}, err
	}
	return r.Config(), nil
}

// SitePlugins is the effective plugin configuration for any signed-in user.
func (a *Accounts) SitePlugins(ctx context.Context, actor domain.Actor) (domain.SitePlugins, error) {
	if !validActor(actor) {
		return domain.SitePlugins{}, domain.ErrInvalid
	}
	r, err := a.repository.GetSitePlugins(ctx, actor, false)
	if err != nil {
		return domain.SitePlugins{}, err
	}
	return r.View(), nil
}

// SitePluginsConfig is the administrator's stored plugin document.
func (a *Accounts) SitePluginsConfig(ctx context.Context, actor domain.Actor) (domain.SitePluginsRecord, error) {
	if !validActor(actor) {
		return domain.SitePluginsRecord{}, domain.ErrInvalid
	}
	return a.repository.GetSitePlugins(ctx, actor, true)
}

// SetSitePlugins replaces the plugin document when revision is current.
func (a *Accounts) SetSitePlugins(ctx context.Context, actor domain.Actor, in domain.SitePlugins, revision int64) (domain.SitePluginsRecord, error) {
	if !validActor(actor) || revision < 0 {
		return domain.SitePluginsRecord{}, domain.ErrInvalid
	}
	normalized, err := domain.NormalizeSitePlugins(in)
	if err != nil {
		return domain.SitePluginsRecord{}, err
	}
	return a.repository.SetSitePlugins(ctx, actor, normalized, revision, "update")
}

// ResetSitePlugins restores every bundled plugin's default (G33.3).
func (a *Accounts) ResetSitePlugins(ctx context.Context, actor domain.Actor) (domain.SitePluginsRecord, error) {
	if !validActor(actor) {
		return domain.SitePluginsRecord{}, domain.ErrInvalid
	}
	return a.repository.SetSitePlugins(ctx, actor, domain.DefaultSitePlugins(), domain.AnyRevision, "reset")
}

// ExportSiteSettings returns both documents as one importable file (G33.3).
func (a *Accounts) ExportSiteSettings(ctx context.Context, actor domain.Actor, now time.Time) (domain.SiteSettingsDocument, error) {
	if !validActor(actor) {
		return domain.SiteSettingsDocument{}, domain.ErrInvalid
	}
	appearance, plugins, err := a.repository.GetSiteSettings(ctx, actor)
	if err != nil {
		return domain.SiteSettingsDocument{}, err
	}
	at := now.UTC().Truncate(time.Second)
	return domain.SiteSettingsDocument{Format: domain.SiteSettingsFormat, Version: domain.SiteSettingsFormatVersion, ExportedAt: &at, Appearance: appearance.SiteAppearance, Plugins: plugins.SitePlugins}, nil
}

// ImportSiteSettings replaces both documents atomically from an exported
// file. It validates everything before storing anything and overrides
// concurrent changes (the import is the administrator's explicit choice).
func (a *Accounts) ImportSiteSettings(ctx context.Context, actor domain.Actor, document domain.SiteSettingsDocument) (domain.SiteSettingsImport, error) {
	if !validActor(actor) || document.Format != domain.SiteSettingsFormat || document.Version != domain.SiteSettingsFormatVersion {
		return domain.SiteSettingsImport{}, domain.ErrInvalid
	}
	appearance, err := domain.NormalizeSiteAppearance(document.Appearance)
	if err != nil {
		return domain.SiteSettingsImport{}, err
	}
	plugins, err := domain.NormalizeSitePlugins(document.Plugins)
	if err != nil {
		return domain.SiteSettingsImport{}, err
	}
	storedAppearance, storedPlugins, err := a.repository.ImportSiteSettings(ctx, actor, appearance, plugins)
	if err != nil {
		return domain.SiteSettingsImport{}, err
	}
	return domain.SiteSettingsImport{Appearance: storedAppearance.Config(), Plugins: storedPlugins}, nil
}

// SiteFontHosts are the effective external font hosts for the frontend
// Content-Security-Policy. Entries that are not plain host names are
// dropped, so storage can never inject a CSP source expression.
func (a *Accounts) SiteFontHosts(ctx context.Context) ([]string, error) {
	hosts, err := a.repository.SiteFontHosts(ctx)
	if err != nil {
		return nil, err
	}
	valid := make([]string, 0, len(hosts))
	for _, host := range hosts {
		if normalized, ok := domain.NormalizeFontHost(host); ok && normalized == host && len(valid) < domain.FontHostLimit {
			valid = append(valid, host)
		}
	}
	return valid, nil
}
