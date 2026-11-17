package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// FavoriteRepository stores favourites (G24.2). Like the progress reads,
// every method applies the library grant and item rules of the live user in
// the same statement: an invisible item is answered like a missing one and
// never listed.
type FavoriteRepository interface {
	// SetFavorite marks or unmarks a visible item and returns the user's
	// data of it; an invisible or missing item is ErrNotFound.
	SetFavorite(ctx context.Context, userID, itemID string, favorite bool) (domain.UserItemData, error)
	// ListFavorites pages the user's visible favourites, most recently
	// marked first.
	ListFavorites(ctx context.Context, userID string, query domain.FavoriteQuery) (domain.FavoritePage, error)
}

// WithFavorites enables favourites on a catalog.
func (c *Catalog) WithFavorites(repository FavoriteRepository) (*Catalog, error) {
	if c == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	next := *c
	next.favorites = repository
	return &next, nil
}

func (c *Catalog) favoritesReady(ctx context.Context) error {
	if c == nil || ctx == nil {
		return domain.ErrInvalid
	}
	if c.favorites == nil {
		// Not wired: refuse instead of pretending nothing is a favourite.
		return domain.ErrDatabase
	}
	return ctx.Err()
}

// SetFavorite marks (favorite true) or unmarks an item userID can see.
func (c *Catalog) SetFavorite(ctx context.Context, userID, itemID string, favorite bool) (domain.UserItemData, error) {
	if err := c.favoritesReady(ctx); err != nil {
		return domain.UserItemData{}, err
	}
	if !domain.ValidID(userID) || !domain.ValidID(itemID) {
		return domain.UserItemData{}, domain.ErrNotFound
	}
	return c.favorites.SetFavorite(ctx, userID, itemID, favorite)
}

// Favorites pages userID's visible favourites.
func (c *Catalog) Favorites(ctx context.Context, userID string, query domain.FavoriteQuery) (domain.FavoritePage, error) {
	if err := c.favoritesReady(ctx); err != nil {
		return domain.FavoritePage{}, err
	}
	if !domain.ValidID(userID) || !domain.ValidFavoriteQuery(query) {
		return domain.FavoritePage{}, domain.ErrInvalid
	}
	return c.favorites.ListFavorites(ctx, userID, query)
}
