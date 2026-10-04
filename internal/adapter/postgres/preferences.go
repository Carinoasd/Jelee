package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// GetPreferences reads the caller's interface preferences; a user who never
// saved any reads the defaults.
func (s *Store) GetPreferences(ctx context.Context, actor domain.Actor) (domain.UserPreferences, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.UserPreferences{}, err
	}
	defer tx.Rollback(ctx)
	p := domain.DefaultUserPreferences()
	err = tx.QueryRow(ctx, `SELECT theme,density FROM user_preferences WHERE user_id=$1::uuid`, actor.UserID).Scan(&p.Theme, &p.Density)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.UserPreferences{}, storageError(err)
	}
	return p, storageError(tx.Commit(ctx))
}

// SetPreferences replaces the caller's interface preferences.
func (s *Store) SetPreferences(ctx context.Context, actor domain.Actor, p domain.UserPreferences) (domain.UserPreferences, error) {
	if !p.Valid() {
		return domain.UserPreferences{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.UserPreferences{}, err
	}
	defer tx.Rollback(ctx)
	var stored domain.UserPreferences
	if err = tx.QueryRow(ctx, `INSERT INTO user_preferences(user_id,theme,density) VALUES($1::uuid,$2,$3)
 ON CONFLICT(user_id) DO UPDATE SET theme=EXCLUDED.theme,density=EXCLUDED.density,updated_at=now() RETURNING theme,density`, actor.UserID, p.Theme, p.Density).Scan(&stored.Theme, &stored.Density); err != nil {
		return domain.UserPreferences{}, storageError(err)
	}
	return stored, storageError(tx.Commit(ctx))
}
