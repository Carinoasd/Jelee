package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func scanMetadataPreferences(row pgx.Row) (domain.MetadataPreferences, error) {
	var value domain.MetadataPreferences
	err := row.Scan(&value.LibraryID, &value.Language, &value.Revision)
	return value, storageError(err)
}

func (s *Store) MetadataPreferences(ctx context.Context, actor domain.Actor, library string) (domain.MetadataPreferences, error) {
	if !domain.ValidID(library) {
		return domain.MetadataPreferences{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.MetadataPreferences{}, err
	}
	defer tx.Rollback(ctx)
	value, err := scanMetadataPreferences(tx.QueryRow(ctx, `SELECT id::text,metadata_language,metadata_preferences_revision FROM libraries WHERE id=$1::uuid`, library))
	if err != nil {
		return domain.MetadataPreferences{}, err
	}
	return value, storageError(tx.Commit(ctx))
}

func (s *Store) UpdateMetadataPreferences(ctx context.Context, actor domain.Actor, library, language string, expected int64) (domain.MetadataPreferences, error) {
	if !domain.ValidMetadataPreferenceUpdate(library, language, expected) {
		return domain.MetadataPreferences{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.MetadataPreferences{}, err
	}
	defer tx.Rollback(ctx)
	value, err := scanMetadataPreferences(tx.QueryRow(ctx, `SELECT id::text,metadata_language,metadata_preferences_revision FROM libraries WHERE id=$1::uuid FOR UPDATE`, library))
	if err != nil {
		return domain.MetadataPreferences{}, err
	}
	if value.Revision != expected {
		return domain.MetadataPreferences{}, domain.ErrConflict
	}
	before := value
	value, err = scanMetadataPreferences(tx.QueryRow(ctx, `UPDATE libraries SET metadata_language=$2,metadata_preferences_revision=metadata_preferences_revision+1 WHERE id=$1::uuid RETURNING id::text,metadata_language,metadata_preferences_revision`, library, language))
	if err != nil {
		return domain.MetadataPreferences{}, err
	}
	if err = auditAccount(ctx, tx, actor, "library.metadata_preferences_changed", library, before, value); err != nil {
		return domain.MetadataPreferences{}, err
	}
	return value, storageError(tx.Commit(ctx))
}
