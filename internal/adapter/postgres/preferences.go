package postgres

import (
	"context"
	"encoding/json"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// GetPreferences reads the caller's interface preferences. A user who never
// saved any reads the defaults with the site's default theme (G33.2).
func (s *Store) GetPreferences(ctx context.Context, actor domain.Actor) (domain.UserPreferences, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.UserPreferences{}, err
	}
	defer tx.Rollback(ctx)
	p := domain.DefaultUserPreferences()
	var layout []byte
	err = tx.QueryRow(ctx, `SELECT COALESCE(p.theme,a.default_theme),COALESCE(p.density,'comfortable'),p.layout FROM site_appearance a LEFT JOIN user_preferences p ON p.user_id=$1::uuid WHERE a.id`, actor.UserID).Scan(&p.Theme, &p.Density, &layout)
	if err != nil {
		return domain.UserPreferences{}, storageError(err)
	}
	if p.Layout, err = decodeUserLayout(layout); err != nil {
		return domain.UserPreferences{}, err
	}
	return p, storageError(tx.Commit(ctx))
}

func decodeUserLayout(data []byte) (*domain.UserLayout, error) {
	if data == nil {
		return nil, nil
	}
	var layout domain.UserLayout
	if err := json.Unmarshal(data, &layout); err != nil {
		return nil, domain.ErrDatabase
	}
	return &layout, nil
}

// SetPreferences replaces the caller's interface preferences.
func (s *Store) SetPreferences(ctx context.Context, actor domain.Actor, p domain.UserPreferences) (domain.UserPreferences, error) {
	if !p.Valid() {
		return domain.UserPreferences{}, domain.ErrInvalid
	}
	var layout any
	if p.Layout != nil {
		data, err := json.Marshal(p.Layout)
		if err != nil {
			return domain.UserPreferences{}, domain.ErrInvalid
		}
		layout = string(data)
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.UserPreferences{}, err
	}
	defer tx.Rollback(ctx)
	var stored domain.UserPreferences
	var storedLayout []byte
	if err = tx.QueryRow(ctx, `INSERT INTO user_preferences(user_id,theme,density,layout) VALUES($1::uuid,$2,$3,$4::jsonb)
 ON CONFLICT(user_id) DO UPDATE SET theme=EXCLUDED.theme,density=EXCLUDED.density,layout=EXCLUDED.layout,updated_at=now() RETURNING theme,density,layout`, actor.UserID, p.Theme, p.Density, layout).Scan(&stored.Theme, &stored.Density, &storedLayout); err != nil {
		return domain.UserPreferences{}, storageError(err)
	}
	if stored.Layout, err = decodeUserLayout(storedLayout); err != nil {
		return domain.UserPreferences{}, err
	}
	return stored, storageError(tx.Commit(ctx))
}
