package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// CatalogDetailsRepository reads the user-facing item details and file
// information (G34.3). Like the browse reads, each method applies the
// library grant of the live user in the same statement as the rows, so an
// invisible item is answered like a missing one.
type CatalogDetailsRepository interface {
	// GetItemDetails returns the display metadata of one visible item;
	// ErrNotFound for a missing or invisible item and for a library ID.
	GetItemDetails(ctx context.Context, userID, id string) (domain.ItemDetailsRecord, error)
	// ListItemSources returns every media source of a visible item with its
	// current probe metadata and external tracks, for any live session of
	// the user: it describes files and never authorizes delivery.
	ListItemSources(ctx context.Context, actor domain.Actor, itemID string) ([]domain.PlaybackSourceRecord, error)
}

// WithDetails enables item details and file information on a catalog.
func (c *Catalog) WithDetails(repository CatalogDetailsRepository) (*Catalog, error) {
	if c == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	next := *c
	next.details = repository
	return &next, nil
}

// ItemDetails returns the display metadata of one item userID may see.
func (c *Catalog) ItemDetails(ctx context.Context, userID, id string) (domain.ItemDetails, error) {
	if err := c.detailsReady(ctx); err != nil {
		return domain.ItemDetails{}, err
	}
	if !domain.ValidID(userID) || !domain.ValidID(id) {
		return domain.ItemDetails{}, domain.ErrNotFound
	}
	record, err := c.details.GetItemDetails(ctx, userID, id)
	if err != nil {
		return domain.ItemDetails{}, err
	}
	return domain.BuildItemDetails(record), nil
}

// ItemSources describes every original resource of an item for any session
// kind, best version first. External tracks carry no delivery URL: this is
// file information, not playback.
func (c *Catalog) ItemSources(ctx context.Context, actor domain.Actor, itemID string) ([]domain.PlaybackSource, error) {
	if err := c.detailsReady(ctx); err != nil {
		return nil, err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) || !domain.ValidID(itemID) {
		return nil, domain.ErrNotFound
	}
	records, err := c.details.ListItemSources(ctx, actor, itemID)
	if err != nil {
		return nil, err
	}
	sources := describeSources(records)
	for i := range sources {
		for j := range sources[i].External {
			sources[i].External[j].URL = ""
		}
	}
	return sources, nil
}

func (c *Catalog) detailsReady(ctx context.Context) error {
	if c == nil || ctx == nil {
		return domain.ErrInvalid
	}
	if c.details == nil {
		// Not wired: refuse instead of pretending the item has no details.
		return domain.ErrDatabase
	}
	return ctx.Err()
}
