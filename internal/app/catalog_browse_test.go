package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type browseRepositoryStub struct {
	calls   int
	queries []domain.BrowseQuery
}

func (s *browseRepositoryStub) ListLibraryViews(context.Context, string) ([]domain.LibraryView, error) {
	s.calls++
	return []domain.LibraryView{{ID: catalogItemID, Name: "Movies"}}, nil
}

func (s *browseRepositoryStub) BrowseItems(_ context.Context, _ string, q domain.BrowseQuery) (domain.BrowsePage, error) {
	s.calls++
	s.queries = append(s.queries, q)
	return domain.BrowsePage{Items: []domain.BrowseItem{}, Total: 3}, nil
}

func (s *browseRepositoryStub) GetBrowseItem(context.Context, string, string) (domain.BrowseItem, error) {
	s.calls++
	return domain.BrowseItem{ID: catalogItemID}, nil
}

func TestCatalogBrowseNeedsWiring(t *testing.T) {
	catalog := NewCatalog(catalogRepositoryStub{})
	if catalog.CanBrowse() {
		t.Fatal("unwired catalog claims browsing")
	}
	if _, err := catalog.LibraryViews(context.Background(), catalogUserID); !errors.Is(err, domain.ErrDatabase) {
		t.Fatalf("unwired views: %v", err)
	}
	if _, err := catalog.WithBrowse(nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil browse repository accepted")
	}
	// Browsing keeps the playback wiring and vice versa.
	withPlayback, err := catalog.WithPlayback(playbackRepositoryFunc(nil))
	if err != nil {
		t.Fatal(err)
	}
	both, err := withPlayback.WithBrowse(&browseRepositoryStub{})
	if err != nil || !both.CanBrowse() || both.playback == nil || catalog.CanBrowse() {
		t.Fatal("wiring lost or shared")
	}
}

func TestCatalogBrowseValidatesBeforeStorage(t *testing.T) {
	repo := &browseRepositoryStub{}
	catalog, err := NewCatalog(catalogRepositoryStub{}).WithBrowse(repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := catalog.LibraryViews(ctx, "../other"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("malformed user accepted")
	}
	if _, err := catalog.Browse(ctx, catalogUserID, domain.BrowseQuery{Limit: domain.BrowseLimitMax + 1}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("oversized page accepted")
	}
	if _, err := catalog.BrowseItem(ctx, catalogUserID, "not-an-id"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("malformed item accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := catalog.Browse(canceled, catalogUserID, domain.BrowseQuery{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled request reached storage")
	}
	if repo.calls != 0 {
		t.Fatal("invalid request reached storage")
	}
	query := domain.BrowseQuery{Scope: domain.BrowseParent, ParentID: catalogItemID, Limit: 20}
	page, err := catalog.Browse(ctx, catalogUserID, query)
	if err != nil || page.Total != 3 || !reflect.DeepEqual(repo.queries, []domain.BrowseQuery{query}) {
		t.Fatalf("browse: %+v %v %+v", page, err, repo.queries)
	}
}

type playbackRepositoryFunc func(context.Context, domain.Actor, string) ([]domain.PlaybackSourceRecord, error)

func (f playbackRepositoryFunc) ListPlaybackSources(ctx context.Context, actor domain.Actor, itemID string) ([]domain.PlaybackSourceRecord, error) {
	return f(ctx, actor, itemID)
}
