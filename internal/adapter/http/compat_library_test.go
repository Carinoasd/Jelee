package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
)

type compatLibraryItem struct {
	Name           string `json:"Name"`
	ID             string `json:"Id"`
	Type           string `json:"Type"`
	ParentID       string `json:"ParentId"`
	SortName       string `json:"SortName"`
	Overview       string `json:"Overview"`
	PremiereDate   string `json:"PremiereDate"`
	ProductionYear int    `json:"ProductionYear"`
	IsFolder       bool   `json:"IsFolder"`
	CollectionType string `json:"CollectionType"`
	RunTimeTicks   *int64 `json:"RunTimeTicks"`
	MediaSources   []struct {
		ID                   string `json:"Id"`
		Container            string `json:"Container"`
		SupportsDirectPlay   bool   `json:"SupportsDirectPlay"`
		SupportsDirectStream bool   `json:"SupportsDirectStream"`
		SupportsTranscoding  bool   `json:"SupportsTranscoding"`
	} `json:"MediaSources"`
}

type compatLibraryPage struct {
	Items            []compatLibraryItem `json:"Items"`
	TotalRecordCount int                 `json:"TotalRecordCount"`
	StartIndex       int                 `json:"StartIndex"`
}

// compatLibraryFixture holds two users with one library each: viewer A sees
// "Films A" (five movies with metadata, one with a media source), viewer B
// sees "Shows B" (a series, a season and three episodes).
type compatLibraryFixture struct {
	userA, userB, tokenA, tokenB, adminToken  string
	libA, libB, series, season, movie, source string
	episodes                                  []string
	markersB                                  []string
}

func compatInsert(t *testing.T, ctx context.Context, store *postgres.Store, query string, args ...any) string {
	t.Helper()
	var id string
	if err := store.Pool.QueryRow(ctx, query, args...).Scan(&id); err != nil {
		t.Fatalf("fixture insert failed: %v", err)
	}
	return id
}

func newCompatLibraryFixture(t *testing.T, ctx context.Context, store *postgres.Store) compatLibraryFixture {
	t.Helper()
	var f compatLibraryFixture
	var err error
	if f.adminToken, err = store.Provision(ctx, "browse-admin", access.ClientNative, true); err != nil {
		t.Fatal(err)
	}
	if f.tokenA, err = store.Provision(ctx, "browse-a", access.ClientNative, false); err != nil {
		t.Fatal(err)
	}
	if f.tokenB, err = store.Provision(ctx, "browse-b", access.ClientNative, false); err != nil {
		t.Fatal(err)
	}
	f.userA = compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='browse-a'`)
	f.userB = compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='browse-b'`)
	base := t.TempDir()
	rootA := filepath.Join(base, "films-a")
	if err = os.Mkdir(rootA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(rootA, "echo.mkv"), []byte("Jelee synthetic browse probe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.libA = compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Films A') RETURNING id::text`)
	f.libB = compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Shows B Secret Zq9') RETURNING id::text`)
	rootIDA := compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, f.libA, rootA)
	compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, f.libB, filepath.Join(base, "shows-b"))

	// Movies: title, sort title, release date, year fact, overview.
	movies := []struct{ title, sortTitle, date, overview string }{
		{"Charlie", "", "2001-05-01", ""},
		{"alpha", "", "", "First of all."},
		{"Bravo", "", "1999-12-31", ""},
		{"The Delta", "Delta", "2010-01-01", ""},
		{"Echo 100%_x", "", "2005-06-15", "Has wildcards."},
	}
	movieIDs := map[string]string{}
	for _, m := range movies {
		id := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,'Movie') RETURNING id::text`, f.libA, m.title)
		movieIDs[m.title] = id
		compatInsert(t, ctx, store, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,2) RETURNING item_id::text`, id)
		for field, value := range map[string]string{"sortTitle": m.sortTitle, "date": m.date, "overview": m.overview} {
			if value == "" {
				continue
			}
			compatInsert(t, ctx, store, `INSERT INTO item_metadata_fields(item_id,field,value,source,updated_at) VALUES($1::uuid,$2,$3,'manual',now()) RETURNING item_id::text`, id, field, value)
		}
	}
	// The year fact wins over the release date's year.
	compatInsert(t, ctx, store, `INSERT INTO item_metadata_facts(item_id,field,value,source,updated_at) VALUES($1::uuid,'year','1998'::jsonb,'manual',now()) RETURNING item_id::text`, movieIDs["Bravo"])
	f.movie = movieIDs["Echo 100%_x"]
	f.source = compatInsert(t, ctx, store, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'echo.mkv','video/x-matroska') RETURNING id::text`, f.movie, f.libA, rootIDA)

	// Shows: series -> season -> two episodes, plus one episode linked to the series directly.
	f.series = compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Secret Series Zq9','Series') RETURNING id::text`, f.libB)
	f.season = compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Season 1','Season') RETURNING id::text`, f.libB)
	compatInsert(t, ctx, store, `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) VALUES($1::uuid,$2::uuid,'Season',$3::uuid,'Series') RETURNING item_id::text`, f.season, f.libB, f.series)
	for i, title := range []string{"Pilot Zq9", "Second Zq9", "Special Zq9"} {
		id := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,'Episode') RETURNING id::text`, f.libB, title)
		parent, kind := f.season, "Season"
		if i == 2 {
			parent, kind = f.series, "Series"
		}
		compatInsert(t, ctx, store, `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) VALUES($1::uuid,$2::uuid,'Episode',$3::uuid,$4) RETURNING item_id::text`, id, f.libB, parent, kind)
		f.episodes = append(f.episodes, id)
	}
	compatInsert(t, ctx, store, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid) RETURNING user_id::text`, f.userA, f.libA)
	compatInsert(t, ctx, store, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid) RETURNING user_id::text`, f.userB, f.libB)
	for _, id := range append([]string{f.libB, f.series, f.season}, f.episodes...) {
		f.markersB = append(f.markersB, id, leakWire(id))
	}
	f.markersB = append(f.markersB, "Shows B Secret Zq9", "Secret Series Zq9", "Zq9")
	return f
}

// TestCompatLibraryPostgres drives the library module through the real
// catalog tables: each user sees only the libraries granted to them, every
// listing and lookup of the other user's content is empty or the hidden
// status, and paging, sorting, search, hierarchy and details work.
func TestCompatLibraryPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	f := newCompatLibraryFixture(t, ctx, store)
	handler := leakHandler(t, store, leakConfig(t, dsn, 0))
	get := func(target, token string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, compatUserRequest(http.MethodGet, target, "", compatTokenAuth(token)))
		return w
	}
	noMarkers := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		for _, marker := range f.markersB {
			if strings.Contains(w.Body.String(), marker) {
				t.Fatalf("%s: user A sees user B content %q: %s", name, marker, w.Body.String())
			}
		}
	}
	page := func(target, token string) compatLibraryPage {
		t.Helper()
		w := get(target, token)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %q", target, w.Code, w.Body.String())
		}
		if token == f.tokenA {
			noMarkers(target, w)
		}
		var p compatLibraryPage
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	names := func(p compatLibraryPage) []string {
		out := make([]string, 0, len(p.Items))
		for _, item := range p.Items {
			out = append(out, item.Name)
		}
		return out
	}
	empty := func(name string, w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status || w.Body.Len() != 0 {
			t.Fatalf("%s: %d %q, want empty %d", name, w.Code, w.Body.String(), status)
		}
	}

	// Library views: each user sees exactly their own library.
	views := page("/compat/UserViews", f.tokenA)
	if len(views.Items) != 1 || views.Items[0].ID != leakWire(f.libA) || views.Items[0].Type != "CollectionFolder" || views.Items[0].CollectionType != "movies" || views.TotalRecordCount != 1 {
		t.Fatalf("views A: %+v", views)
	}
	if v := page("/compat/Users/"+leakWire(f.userB)+"/Views", f.tokenB); len(v.Items) != 1 || v.Items[0].Name != "Shows B Secret Zq9" || v.Items[0].CollectionType != "tvshows" {
		t.Fatalf("views B: %+v", v)
	}
	if v := page("/compat/UserViews", f.adminToken); len(v.Items) != 2 {
		t.Fatalf("admin views: %+v", v)
	}
	if v := page("/compat/Users/"+f.userA+"/Views", f.adminToken); len(v.Items) != 1 || v.Items[0].Name != "Films A" {
		t.Fatalf("admin as A: %+v", v)
	}
	if root := page("/compat/Items", f.tokenA); !slices.Equal(names(root), []string{"Films A"}) {
		t.Fatalf("root A: %+v", root)
	}
	empty("A reads as B", get("/compat/Users/"+leakWire(f.userB)+"/Views", f.tokenA), http.StatusForbidden)
	empty("A lists as B", get("/compat/Items?userId="+leakWire(f.userB)+"&Recursive=true", f.tokenA), http.StatusForbidden)
	empty("A item as B", get("/compat/Users/"+leakWire(f.userB)+"/Items/"+leakWire(f.series), f.tokenA), http.StatusForbidden)

	// Recursive listing with the default name order (sort title, case-insensitive).
	all := page("/compat/Items?Recursive=true&Fields=SortName,ParentId", f.tokenA)
	want := []string{"alpha", "Bravo", "Charlie", "The Delta", "Echo 100%_x"}
	if !slices.Equal(names(all), want) || all.TotalRecordCount != 5 || all.Items[0].ParentID != leakWire(f.libA) || all.Items[3].SortName != "delta" {
		t.Fatalf("all A: %v %+v", names(all), all)
	}
	if b := page("/compat/Items?Recursive=true", f.tokenB); b.TotalRecordCount != 5 || slices.Contains(names(b), "alpha") {
		t.Fatalf("all B: %+v", b)
	}
	// Paging: pages concatenate to the full order; past the end keeps the total.
	var paged []string
	for start := 0; start < 6; start += 2 {
		p := page("/compat/Items?Recursive=true&Limit=2&StartIndex="+string(rune('0'+start)), f.tokenA)
		if p.TotalRecordCount != 5 || p.StartIndex != start {
			t.Fatalf("page %d: %+v", start, p)
		}
		paged = append(paged, names(p)...)
	}
	if !slices.Equal(paged, want) {
		t.Fatalf("paged %v", paged)
	}
	if p := page("/compat/Items?Recursive=true&StartIndex=10", f.tokenA); len(p.Items) != 0 || p.TotalRecordCount != 5 || p.StartIndex != 10 {
		t.Fatalf("past the end: %+v", p)
	}
	if p := page("/compat/Items?Recursive=true&Limit=0", f.tokenA); len(p.Items) != 0 || p.TotalRecordCount != 5 {
		t.Fatalf("count only: %+v", p)
	}
	// Sorting: year (fact first, else the date's year) descending with
	// unknown last; release date ascending with unknown first.
	if p := page("/compat/Items?Recursive=true&SortBy=ProductionYear,SortName&SortOrder=Descending,Ascending", f.tokenA); !slices.Equal(names(p), []string{"The Delta", "Echo 100%_x", "Charlie", "Bravo", "alpha"}) || p.Items[3].ProductionYear != 1998 {
		t.Fatalf("by year: %v %+v", names(p), p.Items)
	}
	if p := page("/compat/Items?Recursive=true&SortBy=PremiereDate", f.tokenA); !slices.Equal(names(p), []string{"alpha", "Bravo", "Charlie", "Echo 100%_x", "The Delta"}) || p.Items[1].PremiereDate != "1999-12-31T00:00:00.0000000Z" {
		t.Fatalf("by date: %v", names(p))
	}
	if p := page("/compat/Items?Recursive=true&SortBy=SortName&SortOrder=Descending", f.tokenA); !slices.Equal(names(p), []string{"Echo 100%_x", "The Delta", "Charlie", "Bravo", "alpha"}) {
		t.Fatalf("by name descending: %v", names(p))
	}
	// Search: case-insensitive substring, wildcards literal, scoped to grants.
	for term, wantNames := range map[string][]string{"ALP": {"alpha"}, "%": {"Echo 100%_x"}, "0%_": {"Echo 100%_x"}, "_": {"Echo 100%_x"}, "a_p": {}, "zq9": {}, "secret": {}} {
		if p := page("/compat/Items?Recursive=true&SearchTerm="+url.QueryEscape(term), f.tokenA); !slices.Equal(names(p), wantNames) || p.TotalRecordCount != len(wantNames) {
			t.Fatalf("search %q: %v", term, names(p))
		}
	}
	if p := page("/compat/Items?Recursive=true&SearchTerm=pilot", f.tokenB); !slices.Equal(names(p), []string{"Pilot Zq9"}) {
		t.Fatalf("search B: %v", names(p))
	}

	// Hierarchy for B: library top level, children, descendants, types.
	if p := page("/compat/Items?ParentId="+leakWire(f.libB), f.tokenB); !slices.Equal(names(p), []string{"Secret Series Zq9"}) || !p.Items[0].IsFolder {
		t.Fatalf("library top: %+v", p)
	}
	if p := page("/compat/Items?ParentId="+leakWire(f.series), f.tokenB); !slices.Equal(names(p), []string{"Season 1", "Special Zq9"}) {
		t.Fatalf("series children: %v", names(p))
	}
	if p := page("/compat/Items?ParentId="+leakWire(f.series)+"&Recursive=true&IncludeItemTypes=Episode&Fields=ParentId", f.tokenB); !slices.Equal(names(p), []string{"Pilot Zq9", "Second Zq9", "Special Zq9"}) || p.Items[0].ParentID != leakWire(f.season) || p.Items[2].ParentID != leakWire(f.series) {
		t.Fatalf("series episodes: %+v", p)
	}
	if p := page("/compat/Items?ParentId="+leakWire(f.libB)+"&IncludeItemTypes=Episode", f.tokenB); p.TotalRecordCount != 3 {
		t.Fatalf("library episodes (recursive by default): %+v", p)
	}
	if p := page("/compat/Items?ParentId="+leakWire(f.season), f.tokenB); !slices.Equal(names(p), []string{"Pilot Zq9", "Second Zq9"}) {
		t.Fatalf("season children: %v", names(p))
	}

	// User A cannot reach any of B's content by parent, ID list or lookup.
	for _, target := range []string{
		"/compat/Items?ParentId=" + leakWire(f.libB),
		"/compat/Items?ParentId=" + leakWire(f.libB) + "&Recursive=true",
		"/compat/Items?ParentId=" + leakWire(f.libB) + "&IncludeItemTypes=Episode",
		"/compat/Items?ParentId=" + leakWire(f.series) + "&Recursive=true",
		"/compat/Items?ParentId=" + leakWire(f.season),
		"/compat/Items?Ids=" + leakWire(f.series) + "," + leakWire(f.episodes[0]) + "," + leakWire(f.libB),
		"/compat/Items?Recursive=true&IncludeItemTypes=Series,Season,Episode",
	} {
		if p := page(target, f.tokenA); len(p.Items) != 0 || p.TotalRecordCount != 0 {
			t.Fatalf("%s: A sees %+v", target, p)
		}
	}
	missing := leakUUID(t)
	for _, id := range append([]string{f.libB, f.series, f.season, missing}, f.episodes...) {
		w := get("/compat/Items/"+leakWire(id), f.tokenA)
		empty("A looks up "+id, w, http.StatusNotFound)
	}

	// Details: everything known, plus the direct delivery source.
	w := get("/compat/Items/"+leakWire(f.movie), f.tokenA)
	var movie compatLibraryItem
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &movie) != nil {
		t.Fatalf("movie: %d %s", w.Code, w.Body.String())
	}
	if movie.Type != "Movie" || movie.Overview != "Has wildcards." || movie.ProductionYear != 2005 || movie.ParentID != leakWire(f.libA) || len(movie.MediaSources) != 1 ||
		movie.MediaSources[0].ID != leakWire(f.source) || movie.MediaSources[0].Container != "mkv" || !movie.MediaSources[0].SupportsDirectPlay ||
		movie.MediaSources[0].SupportsDirectStream || movie.MediaSources[0].SupportsTranscoding || strings.Contains(w.Body.String(), "echo.mkv") || strings.Contains(w.Body.String(), "films-a") {
		t.Fatalf("movie detail: %s", w.Body.String())
	}
	if lib := get("/compat/Users/"+f.userA+"/Items/"+leakWire(f.libA), f.tokenA); lib.Code != http.StatusOK || !strings.Contains(lib.Body.String(), `"Type":"CollectionFolder"`) {
		t.Fatalf("library detail: %d %s", lib.Code, lib.Body.String())
	}
	if series := get("/compat/Items/"+leakWire(f.series), f.tokenB); series.Code != http.StatusOK || strings.Contains(series.Body.String(), "MediaSources") {
		t.Fatalf("series detail: %d %s", series.Code, series.Body.String())
	}

	// Grants are read live: withdrawing A's grant hides everything at once.
	if _, err := store.Pool.Exec(ctx, `DELETE FROM library_acl WHERE user_id=$1::uuid`, f.userA); err != nil {
		t.Fatal(err)
	}
	if v := page("/compat/UserViews", f.tokenA); len(v.Items) != 0 {
		t.Fatalf("views after revoke: %+v", v)
	}
	if p := page("/compat/Items?Recursive=true", f.tokenA); p.TotalRecordCount != 0 {
		t.Fatalf("items after revoke: %+v", p)
	}
	empty("detail after revoke", get("/compat/Items/"+leakWire(f.movie), f.tokenA), http.StatusNotFound)
	// The configured 403 hidden status applies to compatibility lookups too.
	handler = leakHandler(t, store, leakConfig(t, dsn, http.StatusForbidden))
	empty("hidden 403", get("/compat/Items/"+leakWire(f.movie), f.tokenA), http.StatusForbidden)
	empty("missing 403", get("/compat/Items/"+leakWire(missing), f.tokenA), http.StatusForbidden)
}
