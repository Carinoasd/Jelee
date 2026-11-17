package app

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type favoriteRepoStub struct {
	marks map[string]bool
	calls int
}

func (r *favoriteRepoStub) SetFavorite(_ context.Context, _, itemID string, favorite bool) (domain.UserItemData, error) {
	r.calls++
	r.marks[itemID] = favorite
	return domain.UserItemData{ItemID: itemID, IsFavorite: favorite}, nil
}

func (r *favoriteRepoStub) ListFavorites(_ context.Context, _ string, q domain.FavoriteQuery) (domain.FavoritePage, error) {
	r.calls++
	return domain.FavoritePage{Items: []domain.FavoriteEntry{}, Total: q.Offset}, nil
}

func TestCatalogFavorites(t *testing.T) {
	ctx := context.Background()
	user, item := "10000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000001"
	unwired := NewCatalog(catalogRepositoryStub{})
	if _, err := unwired.SetFavorite(ctx, user, item, true); !errors.Is(err, domain.ErrDatabase) {
		t.Fatalf("unwired mark: %v", err)
	}
	if _, err := unwired.Favorites(ctx, user, domain.FavoriteQuery{Limit: 1}); !errors.Is(err, domain.ErrDatabase) {
		t.Fatalf("unwired list: %v", err)
	}
	if _, err := unwired.WithFavorites(nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil repository accepted")
	}
	repo := &favoriteRepoStub{marks: map[string]bool{}}
	catalog, err := unwired.WithFavorites(repo)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := catalog.SetFavorite(ctx, user, item, true); err != nil || !d.IsFavorite || !repo.marks[item] {
		t.Fatalf("mark %+v %v", d, err)
	}
	// Malformed identifiers never reach storage and read like a missing item.
	for _, ids := range [][2]string{{"x", item}, {user, "y"}} {
		if _, err := catalog.SetFavorite(ctx, ids[0], ids[1], false); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%v: %v", ids, err)
		}
	}
	if page, err := catalog.Favorites(ctx, user, domain.FavoriteQuery{Limit: 10, Offset: 3}); err != nil || page.Total != 3 {
		t.Fatalf("list %+v %v", page, err)
	}
	if _, err := catalog.Favorites(ctx, user, domain.FavoriteQuery{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid query reached storage")
	}
	if _, err := catalog.Favorites(ctx, "x", domain.FavoriteQuery{Limit: 1}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid user reached storage")
	}
	if repo.calls != 2 {
		t.Fatalf("storage calls %d", repo.calls)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := catalog.SetFavorite(cancelled, user, item, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
