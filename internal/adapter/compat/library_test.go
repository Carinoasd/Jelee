package compat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

const (
	testLibMovies  = "a0000000-0000-4000-8000-00000000000a"
	testLibShows   = "b0000000-0000-4000-8000-00000000000b"
	testMovieID    = "c0000000-0000-4000-8000-00000000000c"
	testSeriesID   = "d0000000-0000-4000-8000-00000000000d"
	testSourceID   = "e0000000-0000-4000-8000-00000000000e"
	testOtherToken = "otherotherotherotherotherotherotherotherotw"
	testAssTrackID = "f1000000-0000-4000-8000-000000000001"
	testSrtTrackID = "f1000000-0000-4000-8000-000000000002"
)

func wire(id string) string { return strings.ReplaceAll(id, "-", "") }

var (
	testMovie  = domain.BrowseItem{ID: testMovieID, LibraryID: testLibMovies, ParentID: testLibMovies, Kind: "Movie", Title: "Arrival", SortTitle: "Arrival (2016)", Overview: "A linguist is recruited.", PremiereDate: "2016-11-11", Year: 2016}
	testSeries = domain.BrowseItem{ID: testSeriesID, LibraryID: testLibShows, ParentID: testLibShows, Kind: "Series", Title: "Dark", Year: 2017}
)

func int64p(v int64) *int64 { return &v }

// fakeCatalog grants testUserID the movie library, testOtherID the shows
// library and testAdminID both, like the store's library grants.
type fakeCatalog struct {
	queries   []domain.BrowseQuery
	users     []string
	actors    []domain.Actor
	views     int
	lookups   []string
	page      domain.BrowsePage
	err       error
	sourceErr error
	// sources replaces the movie's sources when set.
	sources []domain.PlaybackSource
}

func (f *fakeCatalog) libraries(userID string) []domain.LibraryView {
	movies := domain.LibraryView{ID: testLibMovies, Name: "Movies", ContentKinds: []string{"Movie"}}
	shows := domain.LibraryView{ID: testLibShows, Name: "Shows", ContentKinds: []string{"Series", "Episode"}}
	switch userID {
	case testUserID:
		return []domain.LibraryView{movies}
	case testOtherID:
		return []domain.LibraryView{shows}
	case testAdminID:
		return []domain.LibraryView{movies, shows, {ID: "f0000000-0000-4000-8000-00000000000f", Name: "Mixed", ContentKinds: []string{"Movie", "Series"}}}
	}
	return nil
}

func (f *fakeCatalog) LibraryViews(_ context.Context, userID string) ([]domain.LibraryView, error) {
	f.views++
	f.users = append(f.users, userID)
	if f.err != nil {
		return nil, f.err
	}
	return f.libraries(userID), nil
}

func (f *fakeCatalog) Browse(_ context.Context, userID string, q domain.BrowseQuery) (domain.BrowsePage, error) {
	f.users = append(f.users, userID)
	f.queries = append(f.queries, q)
	if f.err != nil {
		return domain.BrowsePage{}, f.err
	}
	return f.page, nil
}

func (f *fakeCatalog) BrowseItem(_ context.Context, userID, id string) (domain.BrowseItem, error) {
	f.users = append(f.users, userID)
	f.lookups = append(f.lookups, id)
	if f.err != nil {
		return domain.BrowseItem{}, f.err
	}
	for _, lib := range f.libraries(userID) {
		if lib.ID == id {
			return domain.BrowseItem{ID: lib.ID, LibraryID: lib.ID, Kind: domain.BrowseKindLibrary, Title: lib.Name, ContentKinds: lib.ContentKinds}, nil
		}
		for _, item := range []domain.BrowseItem{testMovie, testSeries} {
			if item.ID == id && item.LibraryID == lib.ID {
				return item, nil
			}
		}
	}
	return domain.BrowseItem{}, domain.ErrNotFound
}

func (f *fakeCatalog) PlaybackSources(_ context.Context, actor domain.Actor, itemID string) ([]domain.PlaybackSource, error) {
	f.actors = append(f.actors, actor)
	if f.sourceErr != nil {
		return nil, f.sourceErr
	}
	if itemID != testMovieID {
		return []domain.PlaybackSource{}, nil
	}
	if f.sources != nil {
		return f.sources, nil
	}
	return []domain.PlaybackSource{{
		ID: testSourceID, Container: "mkv", ContentType: "video/x-matroska", Probed: true,
		SizeBytes: int64p(4_000_000_000), DurationMicros: int64p(6_960_000_000), BitRate: int64p(4_597_701),
		Video:     []domain.PlaybackVideoTrack{{Index: 0, Codec: "hevc", Profile: "Main 10", Width: int64p(3840), Height: int64p(1608), Default: true, Primary: true}},
		Audio:     []domain.PlaybackAudioTrack{{Index: 1, Codec: "eac3", Language: "eng", Channels: int64p(6), SampleRate: int64p(48000), Default: true}},
		Subtitles: []domain.PlaybackSubtitleTrack{{Index: 2, Codec: "subrip", Format: "srt", Language: "chi", Forced: true}},
		External: []domain.PlaybackExternalTrack{
			{ID: testAssTrackID, Kind: "subtitle", Format: "ass", Codec: "ass", Language: "zho", Title: "Signs", Default: true, SizeBytes: 10},
			{ID: testSrtTrackID, Kind: "subtitle", Format: "srt", Codec: "srt", Language: "eng", SDH: true, SizeBytes: 10},
			{ID: "f1000000-0000-4000-8000-000000000003", Kind: "audio", Format: "ac3", Codec: "ac3", Language: "jpn", SizeBytes: 10},
		},
	}}, nil
}

type libraryHarness struct {
	harness
	catalog *fakeCatalog
}

func newLibraryHarness(t *testing.T, hidden int, direct bool) *libraryHarness {
	return newLibraryHarnessWith(t, hidden, direct, nil)
}

func newLibraryHarnessWith(t *testing.T, hidden int, direct bool, delivery Delivery) *libraryHarness {
	t.Helper()
	return newLibraryHarnessConfig(t, hidden, func(o *LibraryOptions) { o.DirectPlay, o.Delivery = direct, delivery })
}

// newLibraryHarnessConfig builds the library harness and lets the caller
// complete the library options.
func newLibraryHarnessConfig(t *testing.T, hidden int, configure func(*LibraryOptions)) *libraryHarness {
	t.Helper()
	library := &LibraryOptions{HiddenStatus: hidden, ClientIP: func(*http.Request) string { return testClientIP }}
	configure(library)
	h := &libraryHarness{catalog: &fakeCatalog{page: domain.BrowsePage{Items: []domain.BrowseItem{testMovie}, Total: 41}}}
	library.Catalog = h.catalog
	handler, err := NewRouter(Options{
		Authenticate: func(_ context.Context, token string) (access.Principal, error) {
			h.authCalls++
			switch token {
			case nativeToken:
				return access.Principal{UserID: testUserID, SessionID: testSessionID, Kind: access.ClientNative}, nil
			case adminToken:
				return access.Principal{UserID: testAdminID, SessionID: testSessionID, Kind: access.ClientNative, Admin: true}, nil
			case testOtherToken:
				return access.Principal{UserID: testOtherID, SessionID: testSessionID, Kind: access.ClientNative}, nil
			case webToken:
				return access.Principal{UserID: testUserID, SessionID: testSessionID, Kind: access.ClientWeb}, nil
			}
			return access.Principal{}, domain.ErrUnauthenticated
		},
		WriteRejection: func(w http.ResponseWriter, _ *http.Request, _ error) {
			h.rejections++
			w.WriteHeader(http.StatusConflict)
		},
		ServerID: testServerID,
		Timeout:  time.Second,
		Library:  library,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.handler = handler
	return h
}

func (h *libraryHarness) get(target, token string) *httptest.ResponseRecorder {
	return h.do(http.MethodGet, target, authHeader(token))
}

func TestNewRouterRequiresCompleteLibraryOptions(t *testing.T) {
	base := Options{Authenticate: func(context.Context, string) (access.Principal, error) { return access.Principal{}, nil },
		WriteRejection: func(http.ResponseWriter, *http.Request, error) {}, ServerID: testServerID, Timeout: time.Second}
	full := LibraryOptions{Catalog: &fakeCatalog{}, HiddenStatus: http.StatusNotFound, ClientIP: func(*http.Request) string { return "" }}
	for name, change := range map[string]func(*LibraryOptions){
		"catalog": func(o *LibraryOptions) { o.Catalog = nil },
		"ip":      func(o *LibraryOptions) { o.ClientIP = nil },
		"status":  func(o *LibraryOptions) { o.HiddenStatus = http.StatusOK },
		"zero":    func(o *LibraryOptions) { o.HiddenStatus = 0 },
	} {
		library := full
		change(&library)
		opts := base
		opts.Library = &library
		if r, err := NewRouter(opts); err == nil || r != nil {
			t.Fatalf("%s: incomplete library options accepted", name)
		}
	}
	opts := base
	opts.Library = &full
	if _, err := NewRouter(opts); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryRoutesAbsentWithoutCatalog(t *testing.T) {
	h := newHarness(t)
	for _, target := range []string{"/compat/UserViews", "/compat/Users/" + testUserWire + "/Views", "/compat/Items", "/compat/Users/" + testUserWire + "/Items",
		"/compat/Items/" + wire(testMovieID), "/compat/Users/" + testUserWire + "/Items/" + wire(testMovieID)} {
		assertEmpty(t, target, h.do(http.MethodGet, target, authHeader(nativeToken)), http.StatusNotFound)
	}
}

func TestLibraryRoutesAreWalkable(t *testing.T) {
	h := newLibraryHarness(t, http.StatusNotFound, true)
	seen := map[string]bool{}
	if err := chi.Walk(h.handler.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"GET /UserViews", "GET /Users/{id}/Views", "GET /Items", "GET /Users/{id}/Items", "GET /Items/{itemId}", "GET /Users/{id}/Items/{itemId}",
		"GET /Items/{itemId}/PlaybackInfo", "POST /Items/{itemId}/PlaybackInfo"} {
		if !seen[route] {
			t.Fatalf("route %s not walkable: %v", route, seen)
		}
	}
	// Without a delivery handler no stream route is registered.
	if len(seen) != 12 {
		t.Fatalf("unexpected routes %v", seen)
	}
}

func TestUserViews(t *testing.T) {
	h := newLibraryHarness(t, http.StatusNotFound, true)
	checkGolden(t, "library_user_views.json", h.get("/compat/UserViews", nativeToken))
	checkGolden(t, "library_user_views.json", h.get("/compat/userviews?userId="+testUserWire, nativeToken))
	checkGolden(t, "library_user_views.json", h.get("/compat/Users/"+testUserID+"/Views", nativeToken))
	// An all-zero identifier means the caller, as upstream reads it.
	checkGolden(t, "library_user_views.json", h.get("/compat/UserViews?UserId="+strings.Repeat("0", 32), nativeToken))
	checkGolden(t, "library_user_views_admin.json", h.get("/compat/UserViews", adminToken))
	if !reflect.DeepEqual(h.catalog.users, []string{testUserID, testUserID, testUserID, testUserID, testAdminID}) {
		t.Fatalf("read as %v", h.catalog.users)
	}

	// Another user: refused for a plain user before any catalog read,
	// whether or not the account exists; an administrator reads as them.
	calls := h.catalog.views
	assertEmpty(t, "other path", h.get("/compat/Users/"+wire(testOtherID)+"/Views", nativeToken), http.StatusForbidden)
	assertEmpty(t, "other query", h.get("/compat/UserViews?userId="+wire(testOtherID), nativeToken), http.StatusForbidden)
	assertEmpty(t, "unknown", h.get("/compat/UserViews?userId="+strings.Repeat("ab", 16), nativeToken), http.StatusForbidden)
	assertGeneric(t, "malformed", h.get("/compat/UserViews?userId=me", nativeToken), http.StatusBadRequest)
	assertGeneric(t, "malformed path", h.get("/compat/Users/x/Views", nativeToken), http.StatusBadRequest)
	if h.catalog.views != calls {
		t.Fatal("refused request read the catalog")
	}
	w := h.get("/compat/Users/"+wire(testOtherID)+"/Views", adminToken)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"Name":"Shows"`) || strings.Contains(w.Body.String(), `"Name":"Movies"`) ||
		h.catalog.users[len(h.catalog.users)-1] != testOtherID {
		t.Fatalf("administrator as other user: %d %s", w.Code, w.Body)
	}
	assertEmpty(t, "no token", h.do(http.MethodGet, "/compat/UserViews", nil), http.StatusUnauthorized)
	assertEmpty(t, "web token", h.get("/compat/UserViews", webToken), http.StatusUnauthorized)

	h.catalog.err = domain.ErrDatabase
	assertEmpty(t, "database", h.get("/compat/UserViews", nativeToken), http.StatusServiceUnavailable)
	h.catalog.err = errors.New("boom 10.0.0.5 /var/lib/jelee")
	assertGeneric(t, "internal", h.get("/compat/UserViews", nativeToken), http.StatusInternalServerError)
}

func TestItemsQueryMapping(t *testing.T) {
	h := newLibraryHarness(t, http.StatusNotFound, true)
	w := h.get("/compat/Items?ParentId="+wire(testLibMovies)+"&IncludeItemTypes=Movie,Audio,Video&ExcludeItemTypes=Video&Recursive=true&StartIndex=5&Limit=10"+
		"&SortBy=SortName,DateCreated,ProductionYear&SortOrder=Descending&SearchTerm=arr&Fields=Overview,ParentId,Genres&EnableImages=true", nativeToken)
	checkGolden(t, "library_items.json", w)
	want := domain.BrowseQuery{Scope: domain.BrowseParentRecursive, ParentID: testLibMovies, Kinds: []string{"Movie"}, SearchTerm: "arr",
		Sort: []domain.BrowseSort{{Key: domain.BrowseSortName, Descending: true}, {Key: domain.BrowseSortProductionYear, Descending: true}}, Offset: 5, Limit: 10, WithOverview: true}
	if len(h.catalog.queries) != 1 || !reflect.DeepEqual(h.catalog.queries[0], want) {
		t.Fatalf("query %+v\nwant  %+v", h.catalog.queries, want)
	}

	for _, tc := range []struct {
		name, query string
		want        domain.BrowseQuery
	}{
		// Lower-case members, upstream per-key orders, no type filter.
		{"defaults", "?recursive=TRUE&sortby=PremiereDate,SortName&sortorder=Ascending,Descending",
			domain.BrowseQuery{Scope: domain.BrowseAll, Sort: []domain.BrowseSort{{Key: domain.BrowseSortPremiereDate}, {Key: domain.BrowseSortName, Descending: true}}, Limit: itemsPageMax}},
		{"clamped", "?Recursive=true&Limit=100000", domain.BrowseQuery{Scope: domain.BrowseAll, Limit: itemsPageMax}},
		{"count only", "?Recursive=true&Limit=0", domain.BrowseQuery{Scope: domain.BrowseAll, Limit: 1}},
		{"children", "?ParentId=" + testSeriesID + "&Recursive=false", domain.BrowseQuery{Scope: domain.BrowseParent, ParentID: testSeriesID, Limit: itemsPageMax}},
		{"exclude", "?Recursive=true&ExcludeItemTypes=Episode,Season", domain.BrowseQuery{Scope: domain.BrowseAll, Kinds: []string{"HomeVideo", "Movie", "Series"}, Limit: itemsPageMax}},
		// Types on a library without Recursive list it recursively.
		{"library types", "?ParentId=" + wire(testLibMovies) + "&IncludeItemTypes=movie", domain.BrowseQuery{Scope: domain.BrowseParentRecursive, ParentID: testLibMovies, Kinds: []string{"Movie"}, Limit: itemsPageMax}},
	} {
		h.catalog.queries = nil
		w := h.get("/compat/Items"+tc.query, nativeToken)
		if w.Code != http.StatusOK || len(h.catalog.queries) != 1 || !reflect.DeepEqual(h.catalog.queries[0], tc.want) {
			t.Fatalf("%s: %d %s\nquery %+v\nwant  %+v", tc.name, w.Code, w.Body, h.catalog.queries, tc.want)
		}
	}
	w = h.get("/compat/Items?Recursive=true&Limit=0", nativeToken)
	if w.Body.String() != `{"Items":[],"TotalRecordCount":41,"StartIndex":0}` {
		t.Fatalf("count only: %s", w.Body)
	}
	// The legacy route reads as the path user.
	h.catalog.queries = nil
	if w := h.get("/compat/Users/"+testUserWire+"/Items?Recursive=true", nativeToken); w.Code != http.StatusOK || len(h.catalog.queries) != 1 {
		t.Fatalf("legacy route: %d", w.Code)
	}
}

func TestItemsWithoutCatalogQuery(t *testing.T) {
	h := newLibraryHarness(t, http.StatusNotFound, true)
	// Without a parent and not recursive: the library folders.
	checkGolden(t, "library_items_root.json", h.get("/compat/Items", nativeToken))
	w := h.get("/compat/Items?SortBy=SortName&SortOrder=Descending&StartIndex=1&Limit=1&SearchTerm=I", adminToken)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), `{"Items":[{"Name":"Mixed"`) || !strings.HasSuffix(w.Body.String(), `"TotalRecordCount":2,"StartIndex":1}`) {
		t.Fatalf("root page: %s", w.Body)
	}
	for _, query := range []string{"?IncludeItemTypes=Movie", "?Recursive=true&IncludeItemTypes=CollectionFolder", "?Recursive=true&IncludeItemTypes=MusicAlbum,BoxSet", "?ExcludeItemTypes=CollectionFolder"} {
		if w := h.get("/compat/Items"+query, nativeToken); w.Body.String() != `{"Items":[],"TotalRecordCount":0,"StartIndex":0}` {
			t.Fatalf("%s: %s", query, w.Body)
		}
	}
	// Types on an invisible library: one empty answer, no listing.
	if w := h.get("/compat/Items?ParentId="+wire(testLibShows)+"&IncludeItemTypes=Series&StartIndex=3", nativeToken); w.Body.String() != `{"Items":[],"TotalRecordCount":0,"StartIndex":3}` {
		t.Fatalf("invisible library: %s", w.Body)
	}
	if len(h.catalog.queries) != 0 {
		t.Fatalf("unexpected listing %+v", h.catalog.queries)
	}
	// Ids: the visible ones only, in order, as the reading user.
	w = h.get("/compat/Items?Ids="+wire(testSeriesID)+","+wire(testMovieID)+","+wire(testLibMovies)+"&Fields=SortName", nativeToken)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"SortName":"arrival (2016)"`) || strings.Contains(w.Body.String(), wire(testSeriesID)) ||
		!strings.HasSuffix(w.Body.String(), `"TotalRecordCount":2,"StartIndex":0}`) {
		t.Fatalf("ids: %s", w.Body)
	}
}

func TestItemsRefusals(t *testing.T) {
	h := newLibraryHarness(t, http.StatusNotFound, true)
	for _, query := range []string{
		"ParentId=x", "ParentId=" + strings.Repeat("0", 32), "Recursive=yes", "Recursive=1", "StartIndex=-1", "StartIndex=abc", "StartIndex=1000001",
		"Limit=-1", "Limit=1e3", "SortOrder=Up", "SortBy=Sort-Name", "IncludeItemTypes=Mo%20vie", "ExcludeItemTypes=a.b",
		"SearchTerm=" + strings.Repeat("x", domain.BrowseSearchMax+1), "SearchTerm=%00", "SearchTerm=%ff", "Ids=x",
		"Ids=" + strings.TrimSuffix(strings.Repeat(testUserWire+",", itemIDsMax+1), ","), "userId=nope",
	} {
		assertGeneric(t, query, h.get("/compat/Items?"+query, nativeToken), http.StatusBadRequest)
	}
	assertEmpty(t, "other user", h.get("/compat/Users/"+wire(testOtherID)+"/Items", nativeToken), http.StatusForbidden)
	assertEmpty(t, "other user query", h.get("/compat/Items?userId="+wire(testOtherID), nativeToken), http.StatusForbidden)
	if len(h.catalog.queries) != 0 || len(h.catalog.lookups) != 0 || h.catalog.views != 0 {
		t.Fatal("refused request read the catalog")
	}
	// A search term at the bound is accepted; unknown members are ignored.
	if w := h.get("/compat/Items?Recursive=true&SearchTerm="+strings.Repeat("字", domain.BrowseSearchMax)+"&Filters=IsFavorite&Genres=a|b", nativeToken); w.Code != http.StatusOK {
		t.Fatalf("bounded search: %d", w.Code)
	}
	h.catalog.err = domain.ErrInvalid
	assertGeneric(t, "invalid", h.get("/compat/Items?Recursive=true", nativeToken), http.StatusBadRequest)
	h.catalog.err = context.DeadlineExceeded
	assertEmpty(t, "deadline", h.get("/compat/Items?Recursive=true", nativeToken), http.StatusServiceUnavailable)
}

func TestItemByID(t *testing.T) {
	h := newLibraryHarness(t, http.StatusNotFound, true)
	checkGolden(t, "library_item_detail.json", h.get("/compat/Items/"+wire(testMovieID), nativeToken))
	checkGolden(t, "library_item_detail.json", h.get("/compat/Users/"+testUserWire+"/Items/"+testMovieID, nativeToken))
	if h.catalog.actors[0] != (domain.Actor{UserID: testUserID, SessionID: testSessionID, IP: testClientIP}) {
		t.Fatalf("playback actor %+v", h.catalog.actors[0])
	}
	checkGolden(t, "library_item_folder.json", h.get("/compat/Items/"+wire(testLibMovies), nativeToken))
	// Folders carry no sources and need no source lookup.
	sources := len(h.catalog.actors)
	if w := h.get("/compat/Items/"+wire(testSeriesID), testOtherToken); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "MediaSources") || len(h.catalog.actors) != sources {
		t.Fatalf("series: %d %s", w.Code, w.Body)
	}

	// Missing and invisible items look the same.
	invisible := h.get("/compat/Items/"+wire(testSeriesID), nativeToken)
	missing := h.get("/compat/Items/"+strings.Repeat("ab", 16), nativeToken)
	assertEmpty(t, "invisible", invisible, http.StatusNotFound)
	assertEmpty(t, "missing", missing, http.StatusNotFound)
	if !reflect.DeepEqual(invisible.Header(), missing.Header()) {
		t.Fatal("invisible and missing items are distinguishable")
	}
	assertEmpty(t, "other user", h.get("/compat/Users/"+wire(testOtherID)+"/Items/"+wire(testMovieID), nativeToken), http.StatusForbidden)
	assertGeneric(t, "malformed", h.get("/compat/Items/"+testMovieID+"0", nativeToken), http.StatusBadRequest)
	// An administrator reading as another user sees what that user sees.
	assertEmpty(t, "admin as other", h.get("/compat/Users/"+wire(testOtherID)+"/Items/"+wire(testMovieID), adminToken), http.StatusNotFound)
	if w := h.get("/compat/Users/"+wire(testOtherID)+"/Items/"+wire(testSeriesID), adminToken); w.Code != http.StatusOK {
		t.Fatalf("admin as other: %d", w.Code)
	}
	h.catalog.sourceErr = domain.ErrNotFound
	assertEmpty(t, "sources gone", h.get("/compat/Items/"+wire(testMovieID), nativeToken), http.StatusNotFound)
	h.catalog.sourceErr = domain.ErrDatabase
	assertEmpty(t, "sources unavailable", h.get("/compat/Items/"+wire(testMovieID), nativeToken), http.StatusServiceUnavailable)

	// The configured hidden status applies to both cases.
	h403 := newLibraryHarness(t, http.StatusForbidden, true)
	assertEmpty(t, "invisible 403", h403.get("/compat/Items/"+wire(testSeriesID), nativeToken), http.StatusForbidden)
	assertEmpty(t, "missing 403", h403.get("/compat/Items/"+strings.Repeat("ab", 16), nativeToken), http.StatusForbidden)
}

// G10.4: no response of the module offers any conversion; with direct
// delivery disabled nothing claims direct play either.
func TestLibraryNeverOffersTranscoding(t *testing.T) {
	for _, direct := range []bool{true, false} {
		h := newLibraryHarness(t, http.StatusNotFound, direct)
		body := h.get("/compat/Items/"+wire(testMovieID), nativeToken).Body.String()
		for _, banned := range []string{`"SupportsTranscoding":true`, `"SupportsDirectStream":true`, "TranscodingUrl", "TranscodingSubProtocol", "TranscodingContainer", `"Path"`, "f1000000"} {
			if strings.Contains(body, banned) {
				t.Fatalf("direct=%v publishes %q: %s", direct, banned, body)
			}
		}
		if strings.Contains(body, `"SupportsDirectPlay":true`) != direct || !strings.Contains(body, `"SupportsTranscoding":false`) {
			t.Fatalf("direct=%v: %s", direct, body)
		}
	}
}

func TestLibraryResponsesPublishNoAddresses(t *testing.T) {
	h := newLibraryHarness(t, http.StatusNotFound, true)
	for _, target := range []string{"/compat/UserViews", "/compat/Items", "/compat/Items?Recursive=true&Fields=Overview,SortName,ParentId,Path", "/compat/Items/" + wire(testMovieID)} {
		body := h.get(target, nativeToken).Body.String()
		for _, pattern := range []*regexp.Regexp{ipv4Pattern, absPathPattern} {
			if m := pattern.FindString(body); m != "" {
				t.Fatalf("%s publishes %q in %s", target, m, body)
			}
		}
		if strings.Contains(body, testClientIP) || strings.Contains(body, testMovieID) || strings.Contains(body, testLibMovies) {
			t.Fatalf("%s publishes an address or a dashed identifier: %s", target, body)
		}
	}
}
