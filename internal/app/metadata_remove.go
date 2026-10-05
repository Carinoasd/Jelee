package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type MetadataExternalRemovalRepository interface {
	RemoveExternalMetadata(context.Context, domain.Actor, string, int64) (domain.MetadataRemoveResult, error)
}

// RemoveExternal deletes provider-sourced field values of one item (G14.7).
// It needs no provider credentials, so removal stays available after a TMDB
// key is withdrawn. Local, manual and locked values are preserved.
func (m *Metadata) RemoveExternal(ctx context.Context, actor domain.Actor, item string, expected int64) (domain.MetadataRemoveResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.MetadataRemoveResult{}, err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.MetadataRemoveResult{}, domain.ErrUnauthenticated
	}
	if !domain.ValidMetadataRemoveInput(item, expected) {
		return domain.MetadataRemoveResult{}, domain.ErrInvalid
	}
	if !m.HasItemMetadata() {
		return domain.MetadataRemoveResult{}, domain.ErrMetadataUnavailable
	}
	repository, ok := m.items.(MetadataExternalRemovalRepository)
	if !ok {
		return domain.MetadataRemoveResult{}, domain.ErrMetadataUnavailable
	}
	value, err := repository.RemoveExternalMetadata(ctx, actor, item, expected)
	if err != nil {
		return domain.MetadataRemoveResult{}, err
	}
	return domain.CloneMetadataRemoveResult(value), nil
}
