package compat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	testEpisodeID      = "c1000000-0000-4000-8000-000000000001"
	testSagasID        = "c2000000-0000-4000-8000-000000000001"
	testShowsOnlyID    = "c2000000-0000-4000-8000-000000000002"
	testEmptyID        = "c2000000-0000-4000-8000-000000000003"
	testMineID         = "c3000000-0000-4000-8000-000000000001"
	testOthersPublicID = "c3000000-0000-4000-8000-000000000002"
	testOthersSecretID = "c3000000-0000-4000-8000-000000000003"
	testEntry1         = "e3000000-0000-4000-8000-000000000001"
	testEntry2         = "e3000000-0000-4000-8000-000000000002"
	testEntry3         = "e3000000-0000-4000-8000-000000000003"
	testEntry4         = "e3000000-0000-4000-8000-000000000004"
	testEntry5         = "e3000000-0000-4000-8000-000000000005"
)

var (
	fakeMovie   = domain.Item{ID: testMovieID, LibraryID: testLibMovies, Title: "Arrival", Kind: "Movie"}
	fakeSeries  = domain.Item{ID: testSeriesID, LibraryID: testLibShows, Title: "Dark", Kind: "Series"}
	fakeEpisode = domain.Item{ID: testEpisodeID, LibraryID: testLibShows, Title: "Secrets", Kind: "Episode", ParentID: testSeriesID}
)

type fakeCollection struct {
	id, name, overview string
	members            []domain.Item
}

type fakePlaylist struct {
	id, name, owner string
	public          bool
	entries         []domain.PlaylistEntry
}

type fakeMove struct{ playlist, entry, before string }

// fakeCollections applies the store's rules: testUserID sees the movie
// library, testOtherID the shows library, testAdminID everything; a
// collection without a visible member is listed to the administrator only;
// another user's playlist is readable when public with a visible entry.
type fakeCollections struct {
	collections []fakeCollection
	playlists   []*fakePlaylist
	actors      []domain.Actor
	reads       int
	created     []domain.PlaylistInput
	deleted     []string
	moves       []fakeMove
	removed     []string
	nextID      int
	err         error
}

func newFakeCollections() *fakeCollections {
	return &fakeCollections{
		collections: []fakeCollection{
			{id: testSagasID, name: "Sagas", overview: "Long stories.", members: []domain.Item{fakeMovie, fakeSeries}},
			{id: testShowsOnlyID, name: "Shows Only", members: []domain.Item{fakeSeries}},
			{id: testEmptyID, name: "Empty"},
		},
		playlists: []*fakePlaylist{
			{id: testMineID, name: "Mine", owner: testUserID, entries: []domain.PlaylistEntry{{EntryID: testEntry1, Item: fakeMovie}, {EntryID: testEntry2, Item: fakeEpisode}, {EntryID: testEntry3, Item: fakeMovie}}},
			{id: testOthersPublicID, name: "Others Public", owner: testOtherID, public: true, entries: []domain.PlaylistEntry{{EntryID: testEntry4, Item: fakeEpisode}, {EntryID: testEntry5, Item: fakeMovie}}},
			{id: testOthersSecretID, name: "Others Secret", owner: testOtherID, entries: []domain.PlaylistEntry{{EntryID: "e3000000-0000-4000-8000-000000000006", Item: fakeEpisode}}},
		},
	}
}

// fakeCreated is in another zone to show the wire form is UTC.
var fakeCreated = time.Date(2026, 1, 2, 11, 4, 5, 600_000_000, time.FixedZone("UTC+8", 8*3600))

func fakeVisible(userID string, item domain.Item) bool {
	switch userID {
	case testAdminID:
		return true
	case testUserID:
		return item.LibraryID == testLibMovies
	case testOtherID:
		return item.LibraryID == testLibShows
	}
	return false
}

func (f *fakeCollections) collection(actor domain.Actor, c fakeCollection) domain.CollectionView {
	view := domain.CollectionView{Collection: domain.Collection{ID: c.id, Name: c.name, Overview: c.overview, CreatedAt: fakeCreated}, Items: []domain.CollectionMember{}}
	for _, m := range c.members {
		if fakeVisible(actor.UserID, m) {
			view.Items = append(view.Items, domain.CollectionMember{Item: m, Manual: true})
		}
	}
	view.Collection.ItemCount = len(view.Items)
	return view
}

func (f *fakeCollections) ListCollections(_ context.Context, actor domain.Actor, _ string, _ int) (domain.CollectionPage, error) {
	f.actors = append(f.actors, actor)
	f.reads++
	page := domain.CollectionPage{Collections: []domain.Collection{}}
	if f.err != nil {
		return page, f.err
	}
	for _, c := range f.collections {
		if view := f.collection(actor, c); view.Collection.ItemCount > 0 || actor.UserID == testAdminID {
			page.Collections = append(page.Collections, view.Collection)
		}
	}
	return page, nil
}

func (f *fakeCollections) Collection(_ context.Context, actor domain.Actor, id string) (domain.CollectionView, error) {
	f.actors = append(f.actors, actor)
	f.reads++
	if f.err != nil {
		return domain.CollectionView{}, f.err
	}
	for _, c := range f.collections {
		if c.id == id {
			if view := f.collection(actor, c); view.Collection.ItemCount > 0 || actor.UserID == testAdminID {
				return view, nil
			}
		}
	}
	return domain.CollectionView{}, domain.ErrNotFound
}

func (f *fakeCollections) playlist(actor domain.Actor, p *fakePlaylist) (domain.PlaylistView, bool) {
	view := domain.PlaylistView{Playlist: domain.Playlist{ID: p.id, Name: p.name, Public: p.public, OwnerID: p.owner, Owned: p.owner == actor.UserID, CreatedAt: fakeCreated}, Entries: []domain.PlaylistEntry{}}
	for _, e := range p.entries {
		if fakeVisible(actor.UserID, e.Item) {
			view.Entries = append(view.Entries, e)
		}
	}
	view.Playlist.ItemCount = len(view.Entries)
	return view, view.Playlist.Owned || p.public && len(view.Entries) > 0
}

func (f *fakeCollections) ListPlaylists(_ context.Context, actor domain.Actor, _ string, limit int) (domain.PlaylistPage, error) {
	f.actors = append(f.actors, actor)
	f.reads++
	page := domain.PlaylistPage{Playlists: []domain.Playlist{}}
	if f.err != nil {
		return page, f.err
	}
	for _, p := range f.playlists {
		if view, ok := f.playlist(actor, p); ok && len(page.Playlists) < limit {
			page.Playlists = append(page.Playlists, view.Playlist)
		}
	}
	return page, nil
}

func (f *fakeCollections) Playlist(_ context.Context, actor domain.Actor, id string) (domain.PlaylistView, error) {
	f.actors = append(f.actors, actor)
	f.reads++
	if f.err != nil {
		return domain.PlaylistView{}, f.err
	}
	for _, p := range f.playlists {
		if p.id == id {
			if view, ok := f.playlist(actor, p); ok {
				return view, nil
			}
		}
	}
	return domain.PlaylistView{}, domain.ErrNotFound
}

// own finds a playlist the actor may change, with the native errors.
func (f *fakeCollections) own(actor domain.Actor, id string) (*fakePlaylist, error) {
	for _, p := range f.playlists {
		if p.id == id {
			if _, ok := f.playlist(actor, p); !ok {
				return nil, domain.ErrNotFound
			}
			if p.owner != actor.UserID {
				return nil, domain.ErrForbidden
			}
			return p, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeCollections) CreatePlaylist(_ context.Context, actor domain.Actor, in domain.PlaylistInput) (domain.PlaylistView, error) {
	f.actors = append(f.actors, actor)
	if in = in.Normalize(); !in.Valid() {
		return domain.PlaylistView{}, domain.ErrInvalid
	}
	f.created = append(f.created, in)
	f.nextID++
	p := &fakePlaylist{id: "c4000000-0000-4000-8000-00000000000" + string(rune('0'+f.nextID)), name: in.Name, owner: actor.UserID, public: in.Public}
	f.playlists = append(f.playlists, p)
	view, _ := f.playlist(actor, p)
	return view, nil
}

func (f *fakeCollections) DeletePlaylist(_ context.Context, actor domain.Actor, id string) error {
	if _, err := f.own(actor, id); err != nil {
		return err
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeCollections) AddPlaylistItems(_ context.Context, actor domain.Actor, id string, itemIDs []string) (domain.PlaylistView, error) {
	p, err := f.own(actor, id)
	if err != nil {
		return domain.PlaylistView{}, err
	}
	var added []domain.PlaylistEntry
	for _, itemID := range itemIDs {
		i := slices.IndexFunc([]domain.Item{fakeMovie, fakeSeries, fakeEpisode}, func(item domain.Item) bool { return item.ID == itemID })
		if i < 0 || !fakeVisible(actor.UserID, []domain.Item{fakeMovie, fakeSeries, fakeEpisode}[i]) {
			return domain.PlaylistView{}, domain.ErrNotFound
		}
		item := []domain.Item{fakeMovie, fakeSeries, fakeEpisode}[i]
		if !slices.Contains(domain.PlaylistKinds, item.Kind) {
			return domain.PlaylistView{}, domain.ErrInvalid
		}
		f.nextID++
		added = append(added, domain.PlaylistEntry{EntryID: "e4000000-0000-4000-8000-00000000000" + string(rune('0'+f.nextID)), Item: item})
	}
	p.entries = append(p.entries, added...)
	view, _ := f.playlist(actor, p)
	return view, nil
}

func (f *fakeCollections) RemovePlaylistEntry(_ context.Context, actor domain.Actor, id, entryID string) (domain.PlaylistView, error) {
	p, err := f.own(actor, id)
	if err != nil {
		return domain.PlaylistView{}, err
	}
	i := slices.IndexFunc(p.entries, func(e domain.PlaylistEntry) bool { return e.EntryID == entryID })
	if i < 0 {
		return domain.PlaylistView{}, domain.ErrNotFound
	}
	p.entries = slices.Delete(p.entries, i, i+1)
	f.removed = append(f.removed, entryID)
	view, _ := f.playlist(actor, p)
	return view, nil
}

func (f *fakeCollections) MovePlaylistEntry(_ context.Context, actor domain.Actor, id, entryID, beforeEntryID string) (domain.PlaylistView, error) {
	p, err := f.own(actor, id)
	if err != nil {
		return domain.PlaylistView{}, err
	}
	f.moves = append(f.moves, fakeMove{id, entryID, beforeEntryID})
	view, _ := f.playlist(actor, p)
	return view, nil
}

type collectionsHarness struct {
	*libraryHarness
	collections *fakeCollections
}

func newCollectionsHarness(t *testing.T, hidden int) *collectionsHarness {
	t.Helper()
	fake := newFakeCollections()
	h := newLibraryHarnessConfig(t, hidden, func(o *LibraryOptions) { o.DirectPlay, o.Collections = true, fake })
	// An unknown parent is an empty catalog page.
	h.catalog.page = domain.BrowsePage{Items: []domain.BrowseItem{}}
	return &collectionsHarness{libraryHarness: h, collections: fake}
}

func (h *collectionsHarness) send(method, target, token, contentType, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+target, strings.NewReader(body))
	for k, vs := range authHeader(token) {
		r.Header[k] = vs
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func itemNames(t *testing.T, w *httptest.ResponseRecorder) ([]string, int) {
	t.Helper()
	var result struct {
		Items []struct {
			Name string `json:"Name"`
		} `json:"Items"`
		TotalRecordCount int `json:"TotalRecordCount"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("listing: %d %s", w.Code, w.Body)
	}
	names := []string{}
	for _, item := range result.Items {
		names = append(names, item.Name)
	}
	return names, result.TotalRecordCount
}

func assertNames(t *testing.T, name string, w *httptest.ResponseRecorder, total int, want ...string) {
	t.Helper()
	got, n := itemNames(t, w)
	if want == nil {
		want = []string{}
	}
	if !reflect.DeepEqual(got, want) || n != total {
		t.Fatalf("%s: %v (%d), want %v (%d): %s", name, got, n, want, total, w.Body)
	}
}

func TestCollectionRoutesAbsentWithoutService(t *testing.T) {
	h := newLibraryHarness(t, http.StatusNotFound, true)
	for _, target := range []string{"/compat/Playlists/" + wire(testMineID) + "/Items", "/compat/Playlists"} {
		assertEmpty(t, target, h.get(target, nativeToken), http.StatusNotFound)
	}
	// Without the service no virtual folder is listed and BoxSet matches
	// nothing.
	if w := h.get("/compat/Items?IncludeItemTypes=BoxSet,Playlist&Recursive=true", nativeToken); w.Body.String() != `{"Items":[],"TotalRecordCount":0,"StartIndex":0}` {
		t.Fatalf("types without service: %s", w.Body)
	}
}

func TestCollectionsAsBoxSets(t *testing.T) {
	h := newCollectionsHarness(t, http.StatusNotFound)
	checkGolden(t, "collections_boxsets.json", h.get("/compat/Items?IncludeItemTypes=BoxSet&Recursive=true&Fields=Overview,SortName,ParentId", nativeToken))
	// Like upstream, a BoxSet-only filter ignores ParentId; the type name is
	// case-insensitive.
	assertNames(t, "library parent", h.get("/compat/Items?ParentId="+wire(testLibMovies)+"&IncludeItemTypes=boxset", nativeToken), 1, "Sagas")
	// The administrator also sees a collection only they can see the member
	// of, never one without a member.
	assertNames(t, "admin", h.get("/compat/Items?IncludeItemTypes=BoxSet&Recursive=true", adminToken), 2, "Sagas", "Shows Only")
	assertNames(t, "descending", h.get("/compat/Items?IncludeItemTypes=BoxSet&SortBy=SortName&SortOrder=Descending", adminToken), 2, "Shows Only", "Sagas")
	assertNames(t, "page", h.get("/compat/Items?IncludeItemTypes=BoxSet&StartIndex=1&Limit=1", adminToken), 2, "Shows Only")
	assertNames(t, "count only", h.get("/compat/Items?IncludeItemTypes=BoxSet&Limit=0", adminToken), 2)
	assertNames(t, "search", h.get("/compat/Items?IncludeItemTypes=BoxSet&SearchTerm=SHOW", adminToken), 1, "Shows Only")
	assertNames(t, "excluded", h.get("/compat/Items?IncludeItemTypes=BoxSet&ExcludeItemTypes=BoxSet", adminToken), 0)
	// Both container types, sorted together.
	assertNames(t, "both", h.get("/compat/Items?IncludeItemTypes=Playlist,BoxSet&Recursive=true", nativeToken), 3, "Mine", "Others Public", "Sagas")
	// Mixed with catalog types, collections are not listed.
	h.catalog.page = domain.BrowsePage{Items: []domain.BrowseItem{testMovie}, Total: 1}
	assertNames(t, "mixed", h.get("/compat/Items?IncludeItemTypes=Movie,BoxSet&Recursive=true", nativeToken), 1, "Arrival")
	// An administrator reading as another user gets no collection: the
	// native service reads as the session's own user only.
	reads := h.collections.reads
	assertNames(t, "admin as other", h.get("/compat/Users/"+wire(testOtherID)+"/Items?IncludeItemTypes=BoxSet", adminToken), 0)
	if h.collections.reads != reads {
		t.Fatal("read as another user reached the collection service")
	}
	for _, actor := range h.collections.actors {
		if actor.SessionID != testSessionID || actor.IP != testClientIP {
			t.Fatalf("actor %+v", actor)
		}
	}
	h.collections.err = domain.ErrDatabase
	assertEmpty(t, "database", h.get("/compat/Items?IncludeItemTypes=BoxSet", nativeToken), http.StatusServiceUnavailable)
}

func TestCollectionVirtualFolders(t *testing.T) {
	h := newCollectionsHarness(t, http.StatusNotFound)
	checkGolden(t, "collections_user_views.json", h.get("/compat/UserViews", nativeToken))
	views := newVirtualViews(testServerID)
	if views.boxsets == views.playlists || !domain.ValidID(views.boxsets) || newVirtualViews(testServerID) != views || newVirtualViews(strings.Repeat("1", 32)) == views {
		t.Fatalf("virtual folder identifiers %+v", views)
	}
	checkGolden(t, "collections_view_detail.json", h.get("/compat/Items/"+wire(views.boxsets), nativeToken))
	assertNames(t, "boxsets folder", h.get("/compat/Items?ParentId="+wire(views.boxsets), nativeToken), 1, "Sagas")
	assertNames(t, "playlists folder", h.get("/compat/Items?ParentId="+wire(views.playlists)+"&SortOrder=Descending&SortBy=Name", nativeToken), 2, "Others Public", "Mine")
	assertNames(t, "playlist type in folder", h.get("/compat/Items?ParentId="+wire(views.playlists)+"&IncludeItemTypes=Playlist", nativeToken), 2, "Mine", "Others Public")
	assertNames(t, "wrong type in folder", h.get("/compat/Items?ParentId="+wire(views.boxsets)+"&IncludeItemTypes=Movie", nativeToken), 0)
	// A user without any collection or playlist gets no virtual folder.
	h.collections.collections, h.collections.playlists = nil, nil
	assertNames(t, "nothing", h.get("/compat/UserViews", nativeToken), 1, "Movies")
	// An administrator reading as another user gets none either.
	h2 := newCollectionsHarness(t, http.StatusNotFound)
	assertNames(t, "admin as other", h2.get("/compat/Users/"+wire(testOtherID)+"/Views", adminToken), 1, "Shows")
	assertEmpty(t, "folder as other", h2.get("/compat/Users/"+wire(testOtherID)+"/Items/"+wire(views.boxsets), adminToken), http.StatusNotFound)
	h2.collections.err = domain.ErrDatabase
	assertEmpty(t, "views database", h2.get("/compat/UserViews", nativeToken), http.StatusServiceUnavailable)
}

func TestBoxSetDetailAndMembers(t *testing.T) {
	h := newCollectionsHarness(t, http.StatusNotFound)
	checkGolden(t, "collections_boxset_detail.json", h.get("/compat/Items/"+wire(testSagasID), nativeToken))
	checkGolden(t, "collections_boxset_members.json", h.get("/compat/Items?ParentId="+wire(testSagasID)+"&Fields=ParentId,SortName", nativeToken))
	// Typed member listings: the movie only for the user, the series for
	// the administrator.
	assertNames(t, "typed", h.get("/compat/Items?ParentId="+wire(testSagasID)+"&IncludeItemTypes=Series", nativeToken), 0)
	assertNames(t, "admin typed", h.get("/compat/Items?ParentId="+wire(testSagasID)+"&IncludeItemTypes=Series,Movie", adminToken), 2, "Arrival", "Dark")
	assertNames(t, "admin descending", h.get("/compat/Items?ParentId="+wire(testSagasID)+"&SortBy=SortName&SortOrder=Descending", adminToken), 2, "Dark", "Arrival")
	assertNames(t, "admin search", h.get("/compat/Items?ParentId="+wire(testSagasID)+"&SearchTerm=dar&Recursive=true", adminToken), 1, "Dark")

	// A collection without a visible member looks like a missing item, to
	// the administrator too when it has no member at all.
	invisible := h.get("/compat/Items/"+wire(testShowsOnlyID), nativeToken)
	missing := h.get("/compat/Items/"+strings.Repeat("ab", 16), nativeToken)
	assertEmpty(t, "invisible", invisible, http.StatusNotFound)
	assertEmpty(t, "missing", missing, http.StatusNotFound)
	if !reflect.DeepEqual(invisible.Header(), missing.Header()) {
		t.Fatal("invisible and missing collections are distinguishable")
	}
	assertEmpty(t, "empty for admin", h.get("/compat/Items/"+wire(testEmptyID), adminToken), http.StatusNotFound)
	assertNames(t, "invisible members", h.get("/compat/Items?ParentId="+wire(testShowsOnlyID), nativeToken), 0)
	assertEmpty(t, "admin as other", h.get("/compat/Users/"+wire(testOtherID)+"/Items/"+wire(testSagasID), adminToken), http.StatusNotFound)

	// Ids: collections and playlists by identifier, filtered by type.
	assertNames(t, "ids", h.get("/compat/Items?Ids="+wire(testSagasID)+","+wire(testMovieID)+","+wire(testShowsOnlyID)+","+wire(testMineID), nativeToken), 3, "Sagas", "Arrival", "Mine")
	assertNames(t, "ids typed", h.get("/compat/Items?Ids="+wire(testSagasID)+","+wire(testMineID)+"&IncludeItemTypes=Playlist", nativeToken), 1, "Mine")

	h403 := newCollectionsHarness(t, http.StatusForbidden)
	assertEmpty(t, "invisible 403", h403.get("/compat/Items/"+wire(testShowsOnlyID), nativeToken), http.StatusForbidden)
	h403.collections.err = domain.ErrDatabase
	assertEmpty(t, "detail database", h403.get("/compat/Items/"+wire(testSagasID), nativeToken), http.StatusServiceUnavailable)
}

func TestPlaylistItems(t *testing.T) {
	h := newCollectionsHarness(t, http.StatusNotFound)
	checkGolden(t, "playlists_items.json", h.get("/compat/Playlists/"+wire(testMineID)+"/Items", nativeToken))
	checkGolden(t, "playlists_detail.json", h.get("/compat/Items/"+wire(testMineID), nativeToken))
	assertNames(t, "page", h.get("/compat/Playlists/"+testMineID+"/Items?StartIndex=1&Limit=1&UserId="+testUserWire, nativeToken), 2, "Arrival")
	assertNames(t, "as parent", h.get("/compat/Items?ParentId="+wire(testMineID), nativeToken), 2, "Arrival", "Arrival")
	assertNames(t, "public of another user", h.get("/compat/Playlists/"+wire(testOthersPublicID)+"/Items", nativeToken), 1, "Arrival")
	assertNames(t, "listing", h.get("/compat/Items?IncludeItemTypes=Playlist&Recursive=true", nativeToken), 2, "Mine", "Others Public")

	secret := h.get("/compat/Playlists/"+wire(testOthersSecretID)+"/Items", nativeToken)
	missing := h.get("/compat/Playlists/"+strings.Repeat("ab", 16)+"/Items", nativeToken)
	assertEmpty(t, "secret", secret, http.StatusNotFound)
	assertEmpty(t, "missing", missing, http.StatusNotFound)
	if !reflect.DeepEqual(secret.Header(), missing.Header()) {
		t.Fatal("private and missing playlists are distinguishable")
	}
	assertEmpty(t, "secret detail", h.get("/compat/Items/"+wire(testOthersSecretID), nativeToken), http.StatusNotFound)
	// The administrator cannot read another user's private playlist either.
	assertEmpty(t, "admin secret", h.get("/compat/Playlists/"+wire(testOthersSecretID)+"/Items", adminToken), http.StatusNotFound)
	assertEmpty(t, "admin as other", h.get("/compat/Playlists/"+wire(testOthersSecretID)+"/Items?userId="+wire(testOtherID), adminToken), http.StatusNotFound)
	assertEmpty(t, "user as other", h.get("/compat/Playlists/"+wire(testMineID)+"/Items?userId="+wire(testOtherID), nativeToken), http.StatusForbidden)
	assertGeneric(t, "malformed", h.get("/compat/Playlists/nope/Items", nativeToken), http.StatusBadRequest)
	assertGeneric(t, "bad limit", h.get("/compat/Playlists/"+wire(testMineID)+"/Items?Limit=-1", nativeToken), http.StatusBadRequest)
	assertEmpty(t, "web token", h.get("/compat/Playlists/"+wire(testMineID)+"/Items", webToken), http.StatusUnauthorized)
	h403 := newCollectionsHarness(t, http.StatusForbidden)
	assertEmpty(t, "secret 403", h403.get("/compat/Playlists/"+wire(testOthersSecretID)+"/Items", nativeToken), http.StatusForbidden)
}

func TestCreatePlaylist(t *testing.T) {
	h := newCollectionsHarness(t, http.StatusNotFound)
	w := h.send(http.MethodPost, "/compat/Playlists", nativeToken, "application/json",
		`{"name":"Road Trip","Ids":["`+wire(testMovieID)+`"],"UserId":"`+testUserWire+`","MediaType":"Video","Users":[{"UserId":"`+wire(testOtherID)+`","CanEdit":true}]}`)
	if w.Code != http.StatusOK || w.Body.String() != `{"Id":"c4000000000040008000000000000001"}` {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	created := h.collections.playlists[len(h.collections.playlists)-1]
	if created.name != "Road Trip" || created.public || created.owner != testUserID || len(created.entries) != 1 {
		t.Fatalf("created %+v", created)
	}
	// Query form of older clients; IsPublic in the body.
	if w := h.send(http.MethodPost, "/compat/Playlists?name=Query&ids="+wire(testMovieID)+","+wire(testMovieID), nativeToken, "", ""); w.Code != http.StatusOK {
		t.Fatalf("query form: %d", w.Code)
	}
	if created := h.collections.playlists[len(h.collections.playlists)-1]; created.name != "Query" || len(created.entries) != 2 {
		t.Fatalf("query form created %+v", created)
	}
	if w := h.send(http.MethodPost, "/compat/Playlists", nativeToken, "application/json; charset=utf-8", `{"Name":"Open","IsPublic":true}`); w.Code != http.StatusOK || !h.collections.playlists[len(h.collections.playlists)-1].public {
		t.Fatalf("public: %d", w.Code)
	}

	count := len(h.collections.created)
	assertEmpty(t, "other user", h.send(http.MethodPost, "/compat/Playlists", nativeToken, "application/json", `{"Name":"x","UserId":"`+wire(testOtherID)+`"}`), http.StatusForbidden)
	assertEmpty(t, "admin for other", h.send(http.MethodPost, "/compat/Playlists?userId="+wire(testOtherID)+"&name=x", adminToken, "", ""), http.StatusForbidden)
	assertGeneric(t, "malformed user", h.send(http.MethodPost, "/compat/Playlists", nativeToken, "application/json", `{"Name":"x","UserId":"me"}`), http.StatusBadRequest)
	assertGeneric(t, "malformed ids", h.send(http.MethodPost, "/compat/Playlists?name=x&ids=nope", nativeToken, "", ""), http.StatusBadRequest)
	assertGeneric(t, "too many ids", h.send(http.MethodPost, "/compat/Playlists?name=x&ids="+strings.TrimSuffix(strings.Repeat(wire(testMovieID)+",", domain.MembershipBatchMax+1), ","), nativeToken, "", ""), http.StatusBadRequest)
	assertGeneric(t, "bad json", h.send(http.MethodPost, "/compat/Playlists", nativeToken, "application/json", `{"Name":`), http.StatusBadRequest)
	assertEmpty(t, "not json", h.send(http.MethodPost, "/compat/Playlists", nativeToken, "text/plain", `{"Name":"x"}`), http.StatusUnsupportedMediaType)
	assertEmpty(t, "too large", h.send(http.MethodPost, "/compat/Playlists", nativeToken, "application/json", `{"Name":"`+strings.Repeat("x", playlistBodyLimit)+`"}`), http.StatusRequestEntityTooLarge)
	if len(h.collections.created) != count {
		t.Fatal("refused request created a playlist")
	}
	assertGeneric(t, "no name", h.send(http.MethodPost, "/compat/Playlists", nativeToken, "application/json", `{"Name":"  "}`), http.StatusBadRequest)
	// An item the caller cannot see fails the request like a missing one,
	// and the new playlist is removed again.
	assertEmpty(t, "hidden item", h.send(http.MethodPost, "/compat/Playlists?name=Hidden&ids="+wire(testEpisodeID), nativeToken, "", ""), http.StatusNotFound)
	if len(h.collections.deleted) != 1 {
		t.Fatalf("failed creation left the playlist: %v", h.collections.deleted)
	}
	assertGeneric(t, "series", h.send(http.MethodPost, "/compat/Playlists?name=Series&ids="+wire(testSeriesID), adminToken, "", ""), http.StatusBadRequest)
	assertEmpty(t, "web token", h.send(http.MethodPost, "/compat/Playlists?name=x", webToken, "", ""), http.StatusUnauthorized)
}

func TestPlaylistChanges(t *testing.T) {
	h := newCollectionsHarness(t, http.StatusNotFound)
	mine := h.collections.playlists[0]
	add := "/compat/Playlists/" + wire(testMineID) + "/Items"
	if w := h.send(http.MethodPost, add+"?Ids="+wire(testMovieID)+"&UserId="+testUserWire, nativeToken, "", ""); w.Code != http.StatusNoContent || w.Body.Len() != 0 || len(mine.entries) != 4 {
		t.Fatalf("add: %d %d", w.Code, len(mine.entries))
	}
	assertEmpty(t, "add public of other", h.send(http.MethodPost, "/compat/Playlists/"+wire(testOthersPublicID)+"/Items?Ids="+wire(testMovieID), nativeToken, "", ""), http.StatusForbidden)
	assertEmpty(t, "add secret of other", h.send(http.MethodPost, "/compat/Playlists/"+wire(testOthersSecretID)+"/Items?Ids="+wire(testMovieID), nativeToken, "", ""), http.StatusNotFound)
	// The administrator cannot see, let alone change, a private playlist.
	assertEmpty(t, "add as admin", h.send(http.MethodPost, add+"?Ids="+wire(testMovieID), adminToken, "", ""), http.StatusNotFound)
	assertEmpty(t, "add hidden item", h.send(http.MethodPost, add+"?Ids="+wire(testEpisodeID), nativeToken, "", ""), http.StatusNotFound)
	assertEmpty(t, "add for other user", h.send(http.MethodPost, add+"?Ids="+wire(testMovieID)+"&userId="+wire(testOtherID), nativeToken, "", ""), http.StatusForbidden)
	assertGeneric(t, "add without ids", h.send(http.MethodPost, add, nativeToken, "", ""), http.StatusBadRequest)
	assertGeneric(t, "add malformed", h.send(http.MethodPost, "/compat/Playlists/x/Items?Ids="+wire(testMovieID), nativeToken, "", ""), http.StatusBadRequest)

	// Move among the visible entries: E1, E3 and the new entry; the hidden
	// E2 is not counted.
	newEntry := mine.entries[3].EntryID
	move := func(entry string, index string) *httptest.ResponseRecorder {
		return h.send(http.MethodPost, "/compat/Playlists/"+wire(testMineID)+"/Items/"+entry+"/Move/"+index, nativeToken, "", "")
	}
	for _, tc := range []struct {
		entry, index, before string
	}{{wire(newEntry), "0", testEntry1}, {wire(testEntry1), "1", newEntry}, {wire(testEntry1), "2", ""}, {wire(testEntry1), "99", ""}, {wire(testMovieID), "1", newEntry}} {
		h.collections.moves = nil
		if w := move(tc.entry, tc.index); w.Code != http.StatusNoContent || len(h.collections.moves) != 1 || h.collections.moves[0].before != tc.before {
			t.Fatalf("move %s to %s: %d %+v", tc.entry, tc.index, w.Code, h.collections.moves)
		}
	}
	if h.collections.moves[0].entry != testEntry1 {
		t.Fatalf("item identifier moved %+v", h.collections.moves)
	}
	h.collections.moves = nil
	assertEmpty(t, "move hidden entry", move(wire(testEntry2), "0"), http.StatusNotFound)
	assertEmpty(t, "move unknown", move(strings.Repeat("ab", 16), "0"), http.StatusNotFound)
	assertGeneric(t, "move negative", move(wire(testEntry1), "-1"), http.StatusBadRequest)
	assertGeneric(t, "move not a number", move(wire(testEntry1), "x"), http.StatusBadRequest)
	assertEmpty(t, "move public of other", h.send(http.MethodPost, "/compat/Playlists/"+wire(testOthersPublicID)+"/Items/"+wire(testEntry5)+"/Move/0", nativeToken, "", ""), http.StatusForbidden)
	if len(h.collections.moves) != 0 {
		t.Fatal("refused move reached the service")
	}

	// Remove by entry, by item and by unknown or hidden identifiers.
	remove := func(query string) *httptest.ResponseRecorder {
		return h.send(http.MethodDelete, "/compat/Playlists/"+wire(testMineID)+"/Items?"+query, nativeToken, "", "")
	}
	if w := remove("EntryIds=" + wire(testEntry1) + "," + wire(testEntry2) + "," + strings.Repeat("ab", 16)); w.Code != http.StatusNoContent || !reflect.DeepEqual(h.collections.removed, []string{testEntry1}) {
		t.Fatalf("remove: %d %v", w.Code, h.collections.removed)
	}
	if w := remove("entryIds=" + wire(testMovieID)); w.Code != http.StatusNoContent || len(mine.entries) != 1 || mine.entries[0].EntryID != testEntry2 {
		t.Fatalf("remove by item: %d %+v", w.Code, mine.entries)
	}
	assertGeneric(t, "remove without ids", remove(""), http.StatusBadRequest)
	assertEmpty(t, "remove public of other", h.send(http.MethodDelete, "/compat/Playlists/"+wire(testOthersPublicID)+"/Items?EntryIds="+wire(testEntry5), nativeToken, "", ""), http.StatusForbidden)
	assertEmpty(t, "remove secret of other", h.send(http.MethodDelete, "/compat/Playlists/"+wire(testOthersSecretID)+"/Items?EntryIds="+wire(testEntry5), nativeToken, "", ""), http.StatusNotFound)

	// Origin is refused at the layer boundary before any change.
	r := httptest.NewRequest(http.MethodPost, "http://localhost"+add+"?Ids="+wire(testMovieID), nil)
	for k, vs := range authHeader(nativeToken) {
		r.Header[k] = vs
	}
	r.Header.Set("Origin", "https://example.test")
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	assertEmpty(t, "origin", w, http.StatusForbidden)

	h.collections.err = domain.ErrDatabase
	assertEmpty(t, "database", remove("EntryIds="+wire(testEntry2)), http.StatusServiceUnavailable)
}
