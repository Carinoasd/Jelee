// Package app defines the catalog use case and its storage port.
package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type CatalogRepository interface {
	ListItems(context.Context, string, string, int) ([]domain.Item, error)
	GetItem(context.Context, string, string) (domain.Item, error)
}

type Catalog struct {
	repository CatalogRepository
	playback   PlaybackRepository
	browse     CatalogBrowseRepository
	details    CatalogDetailsRepository
	progress   *Progress
}

func NewCatalog(repository CatalogRepository) *Catalog { return &Catalog{repository: repository} }
func (c *Catalog) List(ctx context.Context, userID, cursor string, limit int) ([]domain.Item, error) {
	if !domain.ValidID(userID) || cursor != "" && !domain.ValidID(cursor) || limit < 1 || limit > 100 {
		return nil, domain.ErrInvalid
	}
	return c.repository.ListItems(ctx, userID, cursor, limit)
}
func (c *Catalog) Get(ctx context.Context, userID, id string) (domain.Item, error) {
	if !domain.ValidID(userID) || !domain.ValidID(id) {
		return domain.Item{}, domain.ErrNotFound
	}
	return c.repository.GetItem(ctx, userID, id)
}
