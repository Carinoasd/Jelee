package app

import (
	"context"
	"errors"
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
	result, err := repository.ApplyItemNFO(ctx, actor, scope, last.Fields)
	return domain.CloneMetadataApplyResult(result), err
}

func (m *Metadata) readItemNFO(ctx context.Context, actor domain.Actor, item string, expected int64) (domain.NFOItemScope, domain.NFOItemSelection, error) {
	repository, ok := m.items.(NFOItemScopeRepository)
	if !ok || m.nfoFields == nil {
		return domain.NFOItemScope{}, domain.NFOItemSelection{}, domain.ErrMetadataUnavailable
	}
	scope, err := repository.ResolveItemNFO(ctx, actor, item, expected)
	if err != nil {
		return domain.NFOItemScope{}, domain.NFOItemSelection{}, err
	}
	if !domain.ValidNFOItemScope(scope) || scope.ItemID != item || scope.Revision != expected {
		return domain.NFOItemScope{}, domain.NFOItemSelection{}, domain.ErrMetadataUnavailable
	}
	selected, err := m.selectItemNFO(ctx, scope)
	if err != nil {
		return domain.NFOItemScope{}, domain.NFOItemSelection{}, nfoItemError(ctx, err)
	}
	return scope, selected, nil
}

func (m *Metadata) selectItemNFO(ctx context.Context, scope domain.NFOItemScope) (domain.NFOItemSelection, error) {
	var selected domain.NFOItemSelection
	var err error
	if reader, ok := m.nfoFields.(NFOItemSelectionReader); ok {
		selected, err = reader.SelectItemNFO(ctx, scope)
	} else {
		// Older internal readers retain their explicit adjacent-file contract.
		selected.Fields, err = m.nfoFields.ReadItemFields(ctx, scope.Source, scope.Kind)
		selected.RelativePath = scope.Source.RelativePath
		selected.CandidateDigest = domain.NFOCandidateDigest([]string{selected.RelativePath})
	}
	if err != nil {
		return domain.NFOItemSelection{}, err
	}
	if !domain.ValidNFOItemSelection(scope, selected) || !validNFOForScope(scope, selected.Fields) {
		return domain.NFOItemSelection{}, domain.ErrMetadataUnavailable
	}
	return selected, nil
}

func (m *Metadata) rereadItemNFO(ctx context.Context, scope domain.NFOItemScope, first domain.NFOItemSelection) (domain.NFOItemSelection, error) {
	// Recheck selection as well as full bytes: a higher-priority name may appear
	// during provider lookup. These checkpoints are not a filesystem snapshot.
	last, err := m.selectItemNFO(ctx, scope)
	if err != nil {
		return domain.NFOItemSelection{}, nfoItemError(ctx, err)
	}
	if first.RelativePath != last.RelativePath || first.CandidateDigest != last.CandidateDigest || first.Fields.Stamp != last.Fields.Stamp || first.Fields.Identity != last.Fields.Identity || first.Fields.LockData != last.Fields.LockData || !slices.Equal(first.Fields.Fields, last.Fields.Fields) || !slices.Equal(first.Fields.LockedFields, last.Fields.LockedFields) {
		return domain.NFOItemSelection{}, domain.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return domain.NFOItemSelection{}, err
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
	if errors.Is(err, domain.ErrNFOSourceChanged) {
		return domain.ErrConflict
	}
	// Filesystem errors must not disclose paths through metadata HTTP responses.
	return domain.ErrMetadataUnavailable
}
