package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// CatalogBrowseRepository reads the browsing model (G24.2). Every method is
// evaluated for one user and applies that user's library grants in the same
// statement as the rows, exactly like ListItems and GetItem: an invisible
// library or item is answered like a missing one, and a disabled or deleted
// user sees nothing.
type CatalogBrowseRepository interface {
	// ListLibraryViews returns the libraries the user may browse, at most
	// domain.BrowseViewsMax, ordered by name.
	ListLibraryViews(ctx context.Context, userID string) ([]domain.LibraryView, error)
	// BrowseItems returns one page of visible items and the total count. A
	// parent that is not visible yields an empty page, never an error.
	BrowseItems(ctx context.Context, userID string, query domain.BrowseQuery) (domain.BrowsePage, error)
	// GetBrowseItem returns one visible item or library; ErrNotFound
	// otherwise.
	GetBrowseItem(ctx context.Context, userID, id string) (domain.BrowseItem, error)
}

// WithBrowse enables catalog browsing on a catalog.
func (c *Catalog) WithBrowse(repository CatalogBrowseRepository) (*Catalog, error) {
	if c == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	next := *c
	next.browse = repository
	return &next, nil
}

// CanBrowse reports whether browsing is wired.
func (c *Catalog) CanBrowse() bool { return c != nil && c.browse != nil }

// LibraryViews lists the libraries userID may browse.
func (c *Catalog) LibraryViews(ctx context.Context, userID string) ([]domain.LibraryView, error) {
	if err := c.browseReady(ctx); err != nil {
		return nil, err
	}
	if !domain.ValidID(userID) {
		return nil, domain.ErrInvalid
	}
	return c.browse.ListLibraryViews(ctx, userID)
}

// Browse lists one page of the items userID may see.
func (c *Catalog) Browse(ctx context.Context, userID string, query domain.BrowseQuery) (domain.BrowsePage, error) {
	if err := c.browseReady(ctx); err != nil {
		return domain.BrowsePage{}, err
	}
	if !domain.ValidID(userID) || !domain.ValidBrowseQuery(query) {
		return domain.BrowsePage{}, domain.ErrInvalid
	}
	return c.browse.BrowseItems(ctx, userID, query)
}

// BrowseItem returns one item or library userID may see.
func (c *Catalog) BrowseItem(ctx context.Context, userID, id string) (domain.BrowseItem, error) {
	if err := c.browseReady(ctx); err != nil {
		return domain.BrowseItem{}, err
	}
	if !domain.ValidID(userID) || !domain.ValidID(id) {
		return domain.BrowseItem{}, domain.ErrNotFound
	}
	return c.browse.GetBrowseItem(ctx, userID, id)
}

func (c *Catalog) browseReady(ctx context.Context) error {
	if c == nil || ctx == nil {
		return domain.ErrInvalid
	}
	if c.browse == nil {
		// Not wired: refuse instead of pretending the catalog is empty.
		return domain.ErrDatabase
	}
	return ctx.Err()
}
