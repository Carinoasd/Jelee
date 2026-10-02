package postgres

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.MetadataNFOFusionRepository = (*Store)(nil)
var _ app.MetadataNFOObservationRepository = (*Store)(nil)

func (s *Store) ApplyTMDBWithNFO(ctx context.Context, actor domain.Actor, scope domain.NFOItemScope, nfo domain.NFOItemFields, update domain.TMDBMetadataUpdate) (domain.MetadataApplyResult, error) {
	return s.applyTMDBWithNFO(ctx, actor, scope, nfo, nil, update)
}

func (s *Store) ApplyTMDBWithNFOObservation(ctx context.Context, actor domain.Actor, scope domain.NFOItemScope, state domain.NFOItemObservationState, update domain.TMDBMetadataUpdate) (domain.MetadataApplyResult, error) {
	if !domain.ValidNFOItemObservationState(scope, state) {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	return s.applyTMDBWithNFO(ctx, actor, scope, state.Selection.Fields, &state, update)
}

func (s *Store) applyTMDBWithNFO(ctx context.Context, actor domain.Actor, scope domain.NFOItemScope, nfo domain.NFOItemFields, state *domain.NFOItemObservationState, update domain.TMDBMetadataUpdate) (domain.MetadataApplyResult, error) {
	useNFO := state == nil || state.Status == domain.NFOItemObservedValid
	if !domain.ValidNFOItemScope(scope) || !domain.ValidTMDBMetadataUpdate(update) || !domain.MetadataResourceMatchesKind(update.Resource, scope.Kind) || useNFO && (!domain.ValidNFOItemFields(nfo) || !(nfo.Kind == scope.Kind || scope.Kind == "HomeVideo" && nfo.Kind == "Movie")) {
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
	if _, err = tx.Exec(ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,$2) ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision`, scope.ItemID, scope.Revision+1); err != nil {
		return domain.MetadataApplyResult{}, storageError(err)
	}
	now := time.Now().UTC()
	local := domain.MetadataApplyResult{Applied: []string{}, Skipped: []domain.MetadataFieldSkip{}}
	if useNFO {
		local, err = applyNFOFields(ctx, tx, before, scope, nfo, now)
		if err != nil {
			return domain.MetadataApplyResult{}, err
		}
	}
	merged, err := readItemMetadata(ctx, tx, scope.ItemID, false)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	provider, err := applyTMDBFields(ctx, tx, merged, scope.ItemID, update, now)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	result := combineMetadataFieldResults(local, provider)
	result.NFO.Status = domain.NFOItemObservedValid
	if state != nil {
		result.NFO.Status = state.Status
	}
	if len(result.Applied) > 0 && scope.Kind == "HomeVideo" && update.Resource == "movie" {
		if _, err = tx.Exec(ctx, `UPDATE items SET kind='Movie' WHERE id=$1::uuid`, scope.ItemID); err != nil {
			return domain.MetadataApplyResult{}, storageError(err)
		}
	}
	if state != nil {
		if err = writeConfirmedNFOObservation(ctx, tx, scope, *state); err != nil {
			return domain.MetadataApplyResult{}, err
		}
	}
	result.Metadata, err = readItemMetadata(ctx, tx, scope.ItemID, false)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	// One accepted review, one revision and one audit for both sources. The
	// resolver's private paths are not included in either audit image.
	after := map[string]any{"metadata": result.Metadata, "applied": result.Applied, "skipped": result.Skipped, "nfo": result.NFO, "tmdb": result.TMDB, "requestSource": update.Fields[0].Origin}
	if err = auditAccount(ctx, tx, actor, "item.tmdb_metadata_applied", scope.ItemID, before, after); err != nil {
		return domain.MetadataApplyResult{}, err
	}
	return result, storageError(tx.Commit(ctx))
}

func combineMetadataFieldResults(local, provider domain.MetadataApplyResult) domain.MetadataApplyResult {
	result := domain.MetadataApplyResult{Applied: []string{}, Skipped: []domain.MetadataFieldSkip{}, NFO: &domain.MetadataFieldApplyReport{Applied: local.Applied, Skipped: local.Skipped}, TMDB: &domain.MetadataFieldApplyReport{Applied: provider.Applied, Skipped: provider.Skipped}}
	applied, skipped := map[string]bool{}, map[string]bool{}
	for _, fields := range [][]string{local.Applied, provider.Applied} {
		for _, field := range fields {
			if !applied[field] {
				applied[field] = true
				result.Applied = append(result.Applied, field)
			}
		}
	}
	for _, fields := range [][]domain.MetadataFieldSkip{local.Skipped, provider.Skipped} {
		for _, field := range fields {
			if !applied[field.Field] && !skipped[field.Field] {
				skipped[field.Field] = true
				result.Skipped = append(result.Skipped, field)
			}
		}
	}
	return result
}
