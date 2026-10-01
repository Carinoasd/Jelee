package app

import (
	"context"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func (m *Metadata) WithNFOItemFields(reader NFOItemFieldsReader) (*Metadata, error) {
	if m == nil || reader == nil {
		return nil, domain.ErrInvalid
	}
	copy := *m
	copy.nfoFields = reader
	return &copy, nil
}

// ApplyNFO obtains its source exclusively from live repository authorization.
// No file handle or transaction is retained while reading/parsing the NFO.
func (m *Metadata) ApplyNFO(ctx context.Context, actor domain.Actor, item string, expected int64, confirmed bool) (domain.MetadataApplyResult, error) {
	if ctx == nil {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.MetadataApplyResult{}, err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.MetadataApplyResult{}, domain.ErrUnauthenticated
	}
	if !confirmed || !domain.ValidID(item) || expected < 1 || expected >= domain.ItemMetadataRevisionMax {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	if m == nil || m.nfoFields == nil {
		return domain.MetadataApplyResult{}, domain.ErrMetadataUnavailable
	}
	repository, ok := m.items.(NFOItemApplyRepository)
	if !ok {
		return domain.MetadataApplyResult{}, domain.ErrMetadataUnavailable
	}
	scope, first, err := m.readItemNFO(ctx, actor, item, expected)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	last, err := m.rereadItemNFO(ctx, scope, first)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	result, err := repository.ApplyItemNFO(ctx, actor, scope, last)
	return domain.CloneMetadataApplyResult(result), err
}

func (m *Metadata) readItemNFO(ctx context.Context, actor domain.Actor, item string, expected int64) (domain.NFOItemScope, domain.NFOItemFields, error) {
	repository, ok := m.items.(NFOItemScopeRepository)
	if !ok || m.nfoFields == nil {
		return domain.NFOItemScope{}, domain.NFOItemFields{}, domain.ErrMetadataUnavailable
	}
	scope, err := repository.ResolveItemNFO(ctx, actor, item, expected)
	if err != nil {
		return domain.NFOItemScope{}, domain.NFOItemFields{}, err
	}
	if !domain.ValidNFOItemScope(scope) || scope.ItemID != item || scope.Revision != expected {
		return domain.NFOItemScope{}, domain.NFOItemFields{}, domain.ErrMetadataUnavailable
	}
	fields, err := m.nfoFields.ReadItemFields(ctx, scope.Source, scope.Kind)
	if err != nil {
		return domain.NFOItemScope{}, domain.NFOItemFields{}, nfoItemError(ctx, err)
	}
	if !validNFOForScope(scope, fields) {
		return domain.NFOItemScope{}, domain.NFOItemFields{}, domain.ErrMetadataUnavailable
	}
	return scope, fields, nil
}

func (m *Metadata) rereadItemNFO(ctx context.Context, scope domain.NFOItemScope, first domain.NFOItemFields) (domain.NFOItemFields, error) {
	// A full-byte reread catches content replacement since the first parse even
	// when size and mtime are restored. These checkpoints are not a filesystem
	// snapshot; changes after the final observation remain possible.
	last, err := m.nfoFields.ReadItemFields(ctx, scope.Source, scope.Kind)
	if err != nil {
		return domain.NFOItemFields{}, nfoItemError(ctx, err)
	}
	if !validNFOForScope(scope, last) || first.Stamp != last.Stamp || first.Identity != last.Identity || first.LockData != last.LockData || !slices.Equal(first.Fields, last.Fields) || !slices.Equal(first.LockedFields, last.LockedFields) {
		return domain.NFOItemFields{}, domain.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return domain.NFOItemFields{}, err
	}
	return last, nil
}

func validNFOForScope(scope domain.NFOItemScope, fields domain.NFOItemFields) bool {
	return domain.ValidNFOItemFields(fields) && (fields.Kind == scope.Kind || scope.Kind == "HomeVideo" && fields.Kind == "Movie")
}

func nfoItemError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Filesystem errors must not disclose paths through metadata HTTP responses.
	return domain.ErrMetadataUnavailable
}
