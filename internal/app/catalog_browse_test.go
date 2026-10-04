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

type detailsRepositoryStub struct {
	calls   int
	actor   domain.Actor
	sources []domain.PlaybackSourceRecord
}

func (s *detailsRepositoryStub) GetItemDetails(_ context.Context, _, id string) (domain.ItemDetailsRecord, error) {
	s.calls++
	return domain.ItemDetailsRecord{Item: domain.BrowseItem{ID: id}, Genres: []byte(`["Drama"]`)}, nil
}

func (s *detailsRepositoryStub) ListItemSources(_ context.Context, actor domain.Actor, _ string) ([]domain.PlaybackSourceRecord, error) {
	s.calls++
	s.actor = actor
	return s.sources, nil
}

func TestCatalogDetailsWiringAndValidation(t *testing.T) {
	ctx := context.Background()
	unwired := NewCatalog(catalogRepositoryStub{})
	if _, err := unwired.ItemDetails(ctx, catalogUserID, catalogItemID); !errors.Is(err, domain.ErrDatabase) {
		t.Fatalf("unwired details: %v", err)
	}
	if _, err := unwired.ItemSources(ctx, domain.Actor{UserID: catalogUserID, SessionID: catalogUserID}, catalogItemID); !errors.Is(err, domain.ErrDatabase) {
		t.Fatalf("unwired sources: %v", err)
	}
	if _, err := unwired.WithDetails(nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil details repository accepted")
	}
	repo := &detailsRepositoryStub{sources: []domain.PlaybackSourceRecord{
		{ID: "b0000000-0000-4000-8000-000000000000", ContentType: "video/mp4", FileName: "Film.720p.mp4"},
		{ID: "a0000000-0000-4000-8000-000000000000", ContentType: "video/x-matroska", FileName: "Film.2160p.mkv",
			Sidecars: []domain.SidecarTrackRecord{{ID: catalogItemID, Track: domain.SidecarTrack{Kind: "subtitle", Format: "srt"}}}},
	}}
	catalog, err := unwired.WithBrowse(&browseRepositoryStub{})
	if err == nil {
		catalog, err = catalog.WithDetails(repo)
	}
	if err != nil || !catalog.CanBrowse() || unwired.details != nil {
		t.Fatal("wiring lost or shared", err)
	}
	for _, id := range []string{"", "x", catalogItemID + "0"} {
		if _, err := catalog.ItemDetails(ctx, catalogUserID, id); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("invalid id %q: %v", id, err)
		}
		if _, err := catalog.ItemSources(ctx, domain.Actor{UserID: catalogUserID, SessionID: catalogUserID}, id); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("invalid source id %q: %v", id, err)
		}
	}
	if _, err := catalog.ItemSources(ctx, domain.Actor{UserID: catalogUserID}, catalogItemID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("missing session accepted", err)
	}
	if repo.calls != 0 {
		t.Fatal("invalid request reached storage")
	}
	d, err := catalog.ItemDetails(ctx, catalogUserID, catalogItemID)
	if err != nil || d.ID != catalogItemID || len(d.Genres) != 1 || d.NFO.Status != domain.ItemDetailsNFOUnread {
		t.Fatalf("details: %+v %v", d, err)
	}
	actor := domain.Actor{UserID: catalogUserID, SessionID: catalogItemID}
	sources, err := catalog.ItemSources(ctx, actor, catalogItemID)
	if err != nil || len(sources) != 2 || sources[0].Version.Resolution != "2160p" || repo.actor != actor || len(sources[0].External) != 1 || sources[0].External[0].URL != "" {
		t.Fatalf("sources: %+v %v", sources, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := catalog.ItemDetails(cancelled, catalogUserID, catalogItemID); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}
