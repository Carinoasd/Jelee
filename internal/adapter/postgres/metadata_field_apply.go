package postgres

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"time"
)

func applyNFOFields(ctx context.Context, tx pgx.Tx, before domain.ItemMetadata, scope domain.NFOItemScope, fields domain.NFOItemFields, now time.Time) (domain.MetadataApplyResult, error) {
	identity, err := domain.NFOIdentityDigest(fields.Identity)
	if err != nil {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	result := domain.MetadataApplyResult{Applied: []string{}, Skipped: []domain.MetadataFieldSkip{}}
	for _, incoming := range fields.Fields {
		old := domain.ItemMetadataField{Field: incoming.Field}
		for _, existing := range before.Fields {
			if existing.Field == incoming.Field {
				old = existing
				break
			}
		}
		reason := ""
		if old.Locked || old.NFOOrigin != nil && old.NFOOrigin.Locked || old.NFOLockOrigin != nil && old.NFOLockOrigin.Locked {
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
		if err := writeItemMetadataField(ctx, tx, scope.ItemID, field, now); err != nil {
			return domain.MetadataApplyResult{}, err
		}
		result.Applied = append(result.Applied, incoming.Field)
	}
	if err := applyNFOFacts(ctx, tx, before, scope, fields, identity, now, &result); err != nil {
		return domain.MetadataApplyResult{}, err
	}
	if err := writeNFOFieldLocks(ctx, tx, scope, fields, identity); err != nil {
		return domain.MetadataApplyResult{}, err
	}
	return result, nil
}

func applyTMDBFields(ctx context.Context, tx pgx.Tx, before domain.ItemMetadata, item string, update domain.TMDBMetadataUpdate, now time.Time) (domain.MetadataApplyResult, error) {
	result := domain.MetadataApplyResult{Applied: []string{}, Skipped: []domain.MetadataFieldSkip{}}
	for _, incoming := range update.Fields {
		old := domain.ItemMetadataField{Field: incoming.Field}
		for _, existing := range before.Fields {
			if existing.Field == incoming.Field {
				old = existing
				break
			}
		}
		if reason := domain.TMDBMetadataSkip(old, update.ReplaceExistingTitle); reason != "" {
			result.Skipped = append(result.Skipped, domain.MetadataFieldSkip{Field: incoming.Field, Reason: reason})
			continue
		}
		origin := incoming.Origin
		field := domain.ItemMetadataField{Field: incoming.Field, Value: incoming.Value, Source: "tmdb", ProviderOrigin: &origin}
		if err := writeItemMetadataField(ctx, tx, item, field, now); err != nil {
			return domain.MetadataApplyResult{}, err
		}
		result.Applied = append(result.Applied, incoming.Field)
	}
	return result, nil
}
