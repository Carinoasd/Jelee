package postgres

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"time"
)

func (s *Store) ApplyTMDBMetadata(ctx context.Context, actor domain.Actor, item string, expected int64, update domain.TMDBMetadataUpdate) (domain.MetadataApplyResult, error) {
	if !domain.ValidID(item) || expected < 1 || expected >= domain.ItemMetadataRevisionMax || !domain.ValidTMDBMetadataUpdate(update) {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	defer tx.Rollback(ctx)
	before, err := readItemMetadata(ctx, tx, item, true)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	if before.Revision != expected {
		return domain.MetadataApplyResult{}, domain.ErrConflict
	}
	if before.NFOMode != domain.NFOModeOff {
		return domain.MetadataApplyResult{}, domain.ErrMetadataUnavailable
	}
	if !domain.MetadataResourceMatchesKind(update.Resource, before.Kind) {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	result := domain.MetadataApplyResult{Applied: []string{}, Skipped: []domain.MetadataFieldSkip{}}
	if _, err = tx.Exec(ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,$2) ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision`, item, expected+1); err != nil {
		return result, storageError(err)
	}
	result, err = applyTMDBFields(ctx, tx, before, item, update, time.Now().UTC())
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	if len(result.Applied) > 0 && before.Kind == "HomeVideo" && update.Resource == "movie" {
		if _, err = tx.Exec(ctx, `UPDATE items SET kind='Movie' WHERE id=$1::uuid`, item); err != nil {
			return domain.MetadataApplyResult{}, storageError(err)
		}
	}
	result.Metadata, err = readItemMetadata(ctx, tx, item, false)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	after := map[string]any{"metadata": result.Metadata, "applied": result.Applied, "skipped": result.Skipped, "requestSource": update.Fields[0].Origin}
	if err = auditAccount(ctx, tx, actor, "item.tmdb_metadata_applied", item, before, after); err != nil {
		return domain.MetadataApplyResult{}, err
	}
	return result, storageError(tx.Commit(ctx))
}
