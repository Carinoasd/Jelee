package postgres

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.NFOItemApplyRepository = (*Store)(nil)

func (s *Store) ApplyItemNFO(ctx context.Context, actor domain.Actor, scope domain.NFOItemScope, fields domain.NFOItemFields) (domain.MetadataApplyResult, error) {
	if !domain.ValidNFOItemScope(scope) || !domain.ValidNFOItemFields(fields) || !(fields.Kind == scope.Kind || scope.Kind == "HomeVideo" && fields.Kind == "Movie") {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	defer tx.Rollback(ctx)
	current, err := readItemNFOScope(ctx, tx, scope.ItemID, scope.Revision)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	if current != scope {
		return domain.MetadataApplyResult{}, domain.ErrConflict
	}
	before, err := readItemMetadata(ctx, tx, scope.ItemID, true)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	result := domain.MetadataApplyResult{Applied: []string{}, Skipped: []domain.MetadataFieldSkip{}}
	if _, err = tx.Exec(ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,$2) ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision`, scope.ItemID, scope.Revision+1); err != nil {
		return result, storageError(err)
	}
	result, err = applyNFOFields(ctx, tx, before, scope, fields, time.Now().UTC())
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	if len(result.Applied) > 0 && scope.Kind == "HomeVideo" && fields.Kind == "Movie" {
		if _, err = tx.Exec(ctx, `UPDATE items SET kind='Movie' WHERE id=$1::uuid`, scope.ItemID); err != nil {
			return domain.MetadataApplyResult{}, storageError(err)
		}
	}
	result.Metadata, err = readItemMetadata(ctx, tx, scope.ItemID, false)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	if err = auditAccount(ctx, tx, actor, "item.nfo_metadata_applied", scope.ItemID, before, result); err != nil {
		return domain.MetadataApplyResult{}, err
	}
	return result, storageError(tx.Commit(ctx))
}
