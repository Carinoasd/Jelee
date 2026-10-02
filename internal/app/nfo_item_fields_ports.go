package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Sources must come from the repository's item/media/root relationship.
// The reader observes bytes only; it does not authorize or persist an item.
type NFOItemFieldsReader interface {
	ReadItemFields(context.Context, domain.NFOSource, string) (domain.NFOItemFields, error)
}

// Implemented by the production reader; the scope must be repository-owned.
type NFOItemSelectionReader interface {
	SelectItemNFO(context.Context, domain.NFOItemScope) (domain.NFOItemSelection, error)
}

type NFOItemScopeRepository interface {
	ResolveItemNFO(context.Context, domain.Actor, string, int64) (domain.NFOItemScope, error)
}

type NFOItemApplyRepository interface {
	NFOItemScopeRepository
	ApplyItemNFO(context.Context, domain.Actor, domain.NFOItemScope, domain.NFOItemFields) (domain.MetadataApplyResult, error)
}
