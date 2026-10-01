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
	identity, err := domain.NFOIdentityDigest(fields.Identity)
	if err != nil {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	result := domain.MetadataApplyResult{Applied: []string{}, Skipped: []domain.MetadataFieldSkip{}}
	if _, err = tx.Exec(ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,$2) ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision`, scope.ItemID, scope.Revision+1); err != nil {
		return result, storageError(err)
	}
	now := time.Now().UTC()
	for _, incoming := range fields.Fields {
		old := domain.ItemMetadataField{Field: incoming.Field}
		for _, existing := range before.Fields {
			if existing.Field == incoming.Field {
				old = existing
				break
			}
		}
		reason := ""
		if old.Locked || old.NFOOrigin != nil && old.NFOOrigin.Locked {
			reason = "locked"
		} else if old.Source == "manual" {
			reason = "manual"
		}
		if reason != "" {
			result.Skipped = append(result.Skipped, domain.MetadataFieldSkip{Field: incoming.Field, Reason: reason})
			continue
		}
		origin := &domain.NFOItemOrigin{SourceID: scope.SourceID, RootID: scope.RootID, Generation: scope.Generation, SHA256: fields.Stamp.SHA256, IdentityDigest: identity, Projection: fields.Version, ReadAt: fields.ReadAt.UTC(), Locked: domain.NFOFieldLocked(fields, incoming.Field)}
		field := domain.ItemMetadataField{Field: incoming.Field, Value: incoming.Value, Source: "nfo", NFOOrigin: origin}
		if err = writeItemMetadataField(ctx, tx, scope.ItemID, field, now); err != nil {
			return domain.MetadataApplyResult{}, err
		}
		result.Applied = append(result.Applied, incoming.Field)
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
