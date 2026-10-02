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
	result, err := repository.ApplyItemNFO(ctx, actor, scope, last.selected.Fields)
	return domain.CloneMetadataApplyResult(result), err
}

type nfoItemRead struct {
	selected    domain.NFOItemSelection
	observation NFOItemObservation
}

func (m *Metadata) readItemNFO(ctx context.Context, actor domain.Actor, item string, expected int64) (domain.NFOItemScope, nfoItemRead, error) {
	repository, ok := m.items.(NFOItemScopeRepository)
	if !ok || m.nfoFields == nil {
		return domain.NFOItemScope{}, nfoItemRead{}, domain.ErrMetadataUnavailable
	}
	scope, err := repository.ResolveItemNFO(ctx, actor, item, expected)
	if err != nil {
		return domain.NFOItemScope{}, nfoItemRead{}, err
	}
	if !domain.ValidNFOItemScope(scope) || scope.ItemID != item || scope.Revision != expected {
		return domain.NFOItemScope{}, nfoItemRead{}, domain.ErrMetadataUnavailable
	}
	selected, err := m.selectItemNFO(ctx, scope)
	if err != nil {
		return domain.NFOItemScope{}, nfoItemRead{}, nfoItemError(ctx, err)
	}
	return scope, selected, nil
}

func (m *Metadata) selectItemNFO(ctx context.Context, scope domain.NFOItemScope) (nfoItemRead, error) {
	var value nfoItemRead
	var err error
	if reader, ok := m.nfoFields.(NFOItemObservationReader); ok {
		value.observation, err = reader.ObserveItemNFO(ctx, scope)
		if err == nil && value.observation != nil {
			value.selected = value.observation.Selection()
		}
	} else if reader, ok := m.nfoFields.(NFOItemSelectionReader); ok {
		value.selected, err = reader.SelectItemNFO(ctx, scope)
	} else {
		value.selected.Fields, err = m.nfoFields.ReadItemFields(ctx, scope.Source, scope.Kind)
		value.selected.RelativePath = scope.Source.RelativePath
		value.selected.CandidateDigest = domain.NFOCandidateDigest([]string{value.selected.RelativePath})
	}
	if err != nil {
		return nfoItemRead{}, err
	}
	if !domain.ValidNFOItemSelection(scope, value.selected) || !validNFOForScope(scope, value.selected.Fields) {
		return nfoItemRead{}, domain.ErrMetadataUnavailable
	}
	return value, nil
}

func (m *Metadata) rereadItemNFO(ctx context.Context, scope domain.NFOItemScope, first nfoItemRead) (nfoItemRead, error) {
	var last nfoItemRead
	var err error
	if first.observation != nil {
		last.observation, err = first.observation.Recheck(ctx)
		if err == nil && last.observation != nil {
			last.selected = last.observation.Selection()
		}
	} else {
		last, err = m.selectItemNFO(ctx, scope)
	}
	if err != nil {
		return nfoItemRead{}, nfoItemError(ctx, err)
	}
	a, b := first.selected, last.selected
	if !domain.ValidNFOItemSelection(scope, b) || !validNFOForScope(scope, b.Fields) || a.RelativePath != b.RelativePath || a.CandidateDigest != b.CandidateDigest || a.Fields.Stamp != b.Fields.Stamp || a.Fields.Identity != b.Fields.Identity || a.Fields.LockData != b.Fields.LockData || !slices.Equal(a.Fields.Fields, b.Fields.Fields) || !slices.Equal(a.Fields.LockedFields, b.Fields.LockedFields) {
		return nfoItemRead{}, domain.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return nfoItemRead{}, err
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
