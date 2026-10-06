package postgres

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Settings documents in a metadata backup pass the checks the API applies
// before anything is written (G36.4). The site appearance and plugin
// documents are normalized like an administrator save and the staged record
// is replaced by the normalized form; a user layout must be one the
// preferences API would accept; audit retention must lie in its range. A
// record that fails is a settings_invalid conflict, so --skip-conflicts
// leaves it out and keeps the target's value. More than one record of a
// single-row kind is a corrupt document.

// metadataSingletonKey is the bk_map source of single-row kinds.
const metadataSingletonKey = "00000000-0000-0000-0000-000000000000"

func metadataSettingsConflict(ctx context.Context, tx pgx.Tx, kind, src string) error {
	_, err := tx.Exec(ctx, `INSERT INTO bk_map(kind,src,state,reason) VALUES($1,$2::uuid,'conflict','settings_invalid') ON CONFLICT DO NOTHING`, kind, src)
	return storageError(err)
}

// metadataSingleton returns the staged document of a single-row kind, or nil.
func metadataSingleton(ctx context.Context, tx pgx.Tx, kind string) ([]byte, error) {
	rows, err := tx.Query(ctx, `SELECT doc::text FROM bk_stage WHERE kind=$1 LIMIT 2`, kind)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var docs [][]byte
	for rows.Next() {
		var doc []byte
		if err = rows.Scan(&doc); err != nil {
			return nil, storageError(err)
		}
		docs = append(docs, doc)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	switch len(docs) {
	case 0:
		return nil, nil
	case 1:
		return docs[0], nil
	}
	return nil, domain.ErrMetadataBackupCorrupt
}

func decodeMetadataDoc(doc []byte, v any) bool {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil && !dec.More()
}

type metadataSiteAppearance struct {
	DefaultTheme       string             `json:"default_theme"`
	Tokens             domain.SiteTokens  `json:"tokens"`
	CustomCSS          string             `json:"custom_css"`
	AllowExternalFonts bool               `json:"allow_external_fonts"`
	FontHosts          []string           `json:"font_hosts"`
	DefaultLayout      *domain.PageLayout `json:"default_layout"`
}

type metadataSitePlugins struct {
	Plugins  []domain.SitePluginState   `json:"plugins"`
	Settings map[string]json.RawMessage `json:"settings"`
}

type metadataAuditRetention struct {
	AuditDays    int `json:"audit_days"`
	SecurityDays int `json:"security_days"`
}

func validateMetadataSettings(ctx context.Context, tx pgx.Tx) error {
	replace := func(kind string, v any) error {
		doc, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE bk_stage SET doc=$2::jsonb WHERE kind=$1`, kind, doc)
		return storageError(err)
	}
	doc, err := metadataSingleton(ctx, tx, "site_appearance")
	if err != nil {
		return err
	}
	if doc != nil {
		var in metadataSiteAppearance
		ok := decodeMetadataDoc(doc, &in)
		var out domain.SiteAppearance
		if ok {
			out, err = domain.NormalizeSiteAppearance(domain.SiteAppearance{DefaultTheme: in.DefaultTheme, Tokens: in.Tokens, CustomCSS: in.CustomCSS,
				AllowExternalFonts: in.AllowExternalFonts, FontHosts: in.FontHosts, DefaultLayout: in.DefaultLayout})
			ok = err == nil
		}
		if !ok {
			err = metadataSettingsConflict(ctx, tx, "site_appearance", metadataSingletonKey)
		} else {
			err = replace("site_appearance", metadataSiteAppearance{DefaultTheme: out.DefaultTheme, Tokens: out.Tokens, CustomCSS: out.CustomCSS,
				AllowExternalFonts: out.AllowExternalFonts, FontHosts: out.FontHosts, DefaultLayout: out.DefaultLayout})
		}
		if err != nil {
			return err
		}
	}
	if doc, err = metadataSingleton(ctx, tx, "site_plugins"); err != nil {
		return err
	}
	if doc != nil {
		var in metadataSitePlugins
		ok := decodeMetadataDoc(doc, &in)
		var out domain.SitePlugins
		if ok {
			out, err = domain.NormalizeSitePlugins(domain.SitePlugins{Plugins: in.Plugins, Settings: in.Settings})
			ok = err == nil
		}
		if !ok {
			err = metadataSettingsConflict(ctx, tx, "site_plugins", metadataSingletonKey)
		} else {
			err = replace("site_plugins", metadataSitePlugins{Plugins: out.Plugins, Settings: out.Settings})
		}
		if err != nil {
			return err
		}
	}
	if doc, err = metadataSingleton(ctx, tx, "audit_retention"); err != nil {
		return err
	}
	if doc != nil {
		var in metadataAuditRetention
		if !decodeMetadataDoc(doc, &in) || !validAuditRetentionDays(in.AuditDays) || !validAuditRetentionDays(in.SecurityDays) {
			if err = metadataSettingsConflict(ctx, tx, "audit_retention", metadataSingletonKey); err != nil {
				return err
			}
		}
	}
	return validateMetadataLayouts(ctx, tx)
}

// validateMetadataLayouts checks every saved user layout. One row per user
// is held at a time.
func validateMetadataLayouts(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT doc->>'user_id',(doc->'layout')::text FROM bk_stage WHERE kind='user_preference' AND jsonb_typeof(doc->'layout')<>'null'`)
	if err != nil {
		return storageError(err)
	}
	var invalid []string
	for rows.Next() {
		var user string
		var layout []byte
		if err = rows.Scan(&user, &layout); err != nil {
			rows.Close()
			return storageError(err)
		}
		var l domain.UserLayout
		if !domain.ValidID(user) || !decodeMetadataDoc(layout, &l) || !l.Valid() {
			invalid = append(invalid, user)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return storageError(err)
	}
	for _, user := range invalid {
		if !domain.ValidID(user) {
			return domain.ErrMetadataBackupCorrupt
		}
		if err = metadataSettingsConflict(ctx, tx, "user_preference", user); err != nil {
			return err
		}
	}
	return nil
}
