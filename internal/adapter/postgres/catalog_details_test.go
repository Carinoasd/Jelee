package postgres

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// catalogDetailsFixture holds two libraries and two viewers, each granted
// exactly one library, with display metadata, a series tree and sources.
type catalogDetailsFixture struct {
	probeFixture
	other                                       domain.LibraryRegistration
	alpha, beta, series, season, episode, gamma string
	hidden, hiddenSource, source                string
	viewerA, viewerB                            domain.Actor
	tokenA, tokenB                              string
	root, hiddenRoot                            string
}

func detailsItem(t *testing.T, f jobFixture, library, title, kind string) string {
	t.Helper()
	var id string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,$3) RETURNING id::text`, library, title, kind).Scan(&id); err != nil {
		t.Fatal("insert item", err)
	}
	return id
}

func detailsMetadata(t *testing.T, f jobFixture, item string, fields map[string]string, facts map[string]string) {
	t.Helper()
	patches := []domain.ItemMetadataPatch{}
	for field, value := range fields {
		patches = append(patches, domain.ItemMetadataPatch{Field: field, Value: &value})
	}
	factPatches := []domain.ItemMetadataFactPatch{}
	for field, value := range facts {
		factPatches = append(factPatches, domain.ItemMetadataFactPatch{Field: field, Value: json.RawMessage(value)})
	}
	if _, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, item, 1, patches, factPatches); err != nil {
		t.Fatal("seed item metadata", err)
	}
}

func newCatalogDetailsFixture(t *testing.T) catalogDetailsFixture {
	t.Helper()
	f := catalogDetailsFixture{probeFixture: newProbeFixture(t)}
	var err error
	if f.other, err = f.s.RegisterLibrary(f.ctx, "details-hidden-Qz8", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		id   string
		path *string
	}{{f.registration.RootID, &f.root}, {f.other.RootID, &f.hiddenRoot}} {
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, r.id).Scan(r.path); err != nil {
			t.Fatal(err)
		}
	}
	lib := f.registration.Library.ID
	f.alpha = detailsItem(t, f.jobFixture, lib, "Alpha 100%_Off", "Movie")
	f.beta = detailsItem(t, f.jobFixture, lib, "beta", "Movie")
	f.gamma = detailsItem(t, f.jobFixture, lib, "Gamma 100 Off", "HomeVideo")
	f.series = detailsItem(t, f.jobFixture, lib, "Delta Series", "Series")
	f.season = detailsItem(t, f.jobFixture, lib, "Season 1", "Season")
	f.episode = detailsItem(t, f.jobFixture, lib, "Pilot", "Episode")
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) VALUES
 ($1::uuid,$3::uuid,'Season',$4::uuid,'Series'),($2::uuid,$3::uuid,'Episode',$1::uuid,'Season')`, f.season, f.episode, lib, f.series)
	f.hidden = detailsItem(t, f.jobFixture, f.other.Library.ID, "Alpha Hidden Qz8", "Movie")
	detailsMetadata(t, f.jobFixture, f.alpha, map[string]string{"overview": "Alpha overview", "originalTitle": "Alpha Original", "tagline": "Alpha tagline", "date": "2001-02-03", "sortTitle": "zz alpha"},
		map[string]string{"genres": `["Drama","Thriller"]`, "uniqueIds": `[{"type":"tmdb","value":"42","default":true},{"type":"imdb","value":"tt0042"}]`})
	detailsMetadata(t, f.jobFixture, f.beta, map[string]string{"date": "1999-05-01"}, nil)
	detailsMetadata(t, f.jobFixture, f.gamma, nil, map[string]string{"year": "2010"})
	detailsMetadata(t, f.jobFixture, f.hidden, map[string]string{"overview": "Hidden overview Qz8"}, map[string]string{"year": "2005"})

	f.source = sidecarSource(t, f.jobFixture, f.alpha, "Movie/Alpha.2160p.mkv")
	seedPlaybackProbe(t, f.probeFixture, "Movie/Alpha.2160p.mkv", 9000000000, 11, 24)
	sidecarUpsert(t, f.jobFixture, f.source, sidecarInput(f.jobFixture, "Movie.zh.srt"), sidecarInput(f.jobFixture, "Movie.commentary.ac3"))
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'Hidden/Qz8-secret.mkv','video/x-matroska') RETURNING id::text`,
		f.hidden, f.other.Library.ID, f.other.RootID).Scan(&f.hiddenSource); err != nil {
		t.Fatal(err)
	}

	for _, viewer := range []struct {
		name    string
		library string
		actor   *domain.Actor
		token   *string
	}{{"details-viewer-a", lib, &f.viewerA, &f.tokenA}, {"details-viewer-b", f.other.Library.ID, &f.viewerB, &f.tokenB}} {
		token, err := f.s.Provision(f.ctx, viewer.name, access.ClientWeb, false)
		if err != nil {
			t.Fatal(err)
		}
		principal, err := f.s.Authenticate(f.ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		*viewer.actor, *viewer.token = domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID}, token
		imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, principal.UserID, viewer.library)
	}
	return f
}

func detailsIDs(page domain.BrowsePage) []string {
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

func TestCatalogBrowseFiltersAcrossGrants(t *testing.T) {
	f := newCatalogDetailsFixture(t)
	lib, other := f.registration.Library.ID, f.other.Library.ID
	browse := func(actor domain.Actor, q domain.BrowseQuery) domain.BrowsePage {
		t.Helper()
		if q.Limit == 0 {
			q.Limit = 50
		}
		page, err := f.s.BrowseItems(f.ctx, actor.UserID, q)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	a, b := f.viewerA, f.viewerB
	name := []domain.BrowseSort{{Key: domain.BrowseSortName}}

	// Library filter: every level of the library, never another library.
	page := browse(a, domain.BrowseQuery{LibraryID: lib, Sort: name})
	if page.Total != 6 || len(page.Items) != 6 || fmt.Sprint(detailsIDs(page)) != fmt.Sprint([]string{f.beta, f.series, f.gamma, f.episode, f.season, f.alpha}) {
		t.Fatalf("library listing %d %v", page.Total, detailsIDs(page))
	}
	// An invisible library and a missing one look the same: nothing, total 0.
	for _, q := range []domain.BrowseQuery{{LibraryID: other}, {LibraryID: "10000000-0000-4000-8000-000000000099"}, {Scope: domain.BrowseParent, ParentID: other},
		{Scope: domain.BrowseParent, ParentID: f.hidden}, {Scope: domain.BrowseParent, ParentID: f.series, LibraryID: other}} {
		if page := browse(a, q); page.Total != 0 || len(page.Items) != 0 {
			t.Fatalf("invisible scope %+v leaked %d", q, page.Total)
		}
	}
	if page := browse(b, domain.BrowseQuery{LibraryID: lib}); page.Total != 0 {
		t.Fatal("viewer B sees library A", page.Total)
	}
	if page := browse(b, domain.BrowseQuery{LibraryID: other}); page.Total != 1 || page.Items[0].ID != f.hidden {
		t.Fatal("viewer B misses its own library", page.Total)
	}
	// parentId: top level of a library, children of a series or season.
	page = browse(a, domain.BrowseQuery{Scope: domain.BrowseParent, ParentID: lib, Sort: name})
	if fmt.Sprint(detailsIDs(page)) != fmt.Sprint([]string{f.beta, f.series, f.gamma, f.alpha}) || page.Total != 4 {
		t.Fatalf("library top level %v", detailsIDs(page))
	}
	if page = browse(a, domain.BrowseQuery{Scope: domain.BrowseParent, ParentID: f.series, LibraryID: lib}); page.Total != 1 || page.Items[0].ID != f.season || page.Items[0].ParentID != f.series {
		t.Fatalf("series children %v", detailsIDs(page))
	}
	if page = browse(b, domain.BrowseQuery{Scope: domain.BrowseParent, ParentID: f.series}); page.Total != 0 {
		t.Fatal("viewer B lists a hidden series")
	}
	// Types, sort and offset with a total that ignores the page.
	page = browse(a, domain.BrowseQuery{Kinds: []string{"Movie", "HomeVideo"}, Sort: []domain.BrowseSort{{Key: domain.BrowseSortProductionYear, Descending: true}}, Limit: 2})
	if page.Total != 3 || fmt.Sprint(detailsIDs(page)) != fmt.Sprint([]string{f.gamma, f.alpha}) || page.Items[0].Year != 2010 || page.Items[1].PremiereDate != "2001-02-03" {
		t.Fatalf("year order %d %v", page.Total, detailsIDs(page))
	}
	page = browse(a, domain.BrowseQuery{Kinds: []string{"Movie", "HomeVideo"}, Sort: []domain.BrowseSort{{Key: domain.BrowseSortPremiereDate}}, Offset: 1, Limit: 1})
	if page.Total != 3 || fmt.Sprint(detailsIDs(page)) != fmt.Sprint([]string{f.beta}) {
		t.Fatalf("date order %d %v", page.Total, detailsIDs(page))
	}
	if page = browse(a, domain.BrowseQuery{Kinds: []string{"Movie"}, Offset: 10}); page.Total != 2 || len(page.Items) != 0 {
		t.Fatal("page past the end loses the total", page.Total)
	}
	// Search: literal wildcards, case-insensitive, never across grants.
	for term, want := range map[string][]string{"100%_": {f.alpha}, "%": {f.alpha}, "_": {f.alpha}, "ALPHA": {f.alpha}, "100": {f.gamma, f.alpha}, "Qz8": {}} {
		page := browse(a, domain.BrowseQuery{SearchTerm: term, Sort: name})
		if page.Total != len(want) || fmt.Sprint(detailsIDs(page)) != fmt.Sprint(want) {
			t.Errorf("search %q: %d %v", term, page.Total, detailsIDs(page))
		}
	}
	if page := browse(b, domain.BrowseQuery{SearchTerm: "alpha"}); page.Total != 1 || page.Items[0].ID != f.hidden {
		t.Fatal("viewer B search crosses grants", page.Total)
	}
}

func TestCatalogDetailsAndSourcesAcrossGrants(t *testing.T) {
	f := newCatalogDetailsFixture(t)
	record, err := f.s.GetItemDetails(f.ctx, f.viewerA.UserID, f.alpha)
	if err != nil {
		t.Fatal(err)
	}
	d := domain.BuildItemDetails(record)
	if d.Overview != "Alpha overview" || d.OriginalTitle != "Alpha Original" || d.Tagline != "Alpha tagline" || d.Year != 2001 || d.PremiereDate != "2001-02-03" ||
		d.SortTitle != "zz alpha" || fmt.Sprint(d.Genres) != "[Drama Thriller]" || len(d.ExternalIDs) != 2 || d.ExternalIDs[0] != (domain.ItemExternalID{Type: "tmdb", Value: "42", Default: true}) ||
		d.NFO.Status != domain.ItemDetailsNFOUnread || len(d.NFO.Fields) != 0 {
		t.Fatalf("details differ: %+v", d)
	}
	if record, err = f.s.GetItemDetails(f.ctx, f.viewerA.UserID, f.beta); err != nil || record.Genres != nil || record.UniqueIDs != nil || record.Observation != nil {
		t.Fatalf("absent facts not nil: %+v %v", record, err)
	}
	for _, tc := range []struct {
		actor domain.Actor
		item  string
	}{{f.viewerA, f.hidden}, {f.viewerB, f.alpha}, {f.viewerA, f.registration.Library.ID}, {f.viewerA, "10000000-0000-4000-8000-000000000099"}} {
		if _, err := f.s.GetItemDetails(f.ctx, tc.actor.UserID, tc.item); err != domain.ErrNotFound {
			t.Fatalf("details of %s: %v", tc.item, err)
		}
		if v, err := f.s.ListItemSources(f.ctx, tc.actor, tc.item); v != nil || err != domain.ErrNotFound {
			t.Fatalf("sources of %s: %v", tc.item, err)
		}
	}

	// A web session reads file information but still cannot use the
	// native playback read.
	records, err := f.s.ListItemSources(f.ctx, f.viewerA, f.alpha)
	if err != nil || len(records) != 1 || records[0].Metadata == nil || len(records[0].Sidecars) != 2 {
		t.Fatalf("web file information: %d %v", len(records), err)
	}
	if text := fmt.Sprintf("%v %#v", records[0], records[0]); strings.Contains(text, f.root) || strings.Contains(text, "Movie/") {
		t.Fatal("record diagnostics expose paths")
	}
	if _, err := f.s.ListPlaybackSources(f.ctx, f.viewerA, f.alpha); err != domain.ErrNotFound {
		t.Fatal("web session reached playback information", err)
	}

	catalog, err := app.NewCatalog(f.s).WithPlayback(f.s)
	if err == nil {
		catalog, err = catalog.WithBrowse(f.s)
	}
	if err == nil {
		catalog, err = catalog.WithDetails(f.s)
	}
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Resources: config.DefaultResourcesConfig(), Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test",
		MaxConnections: 16, MaxStreams: 2, RequestTimeoutSeconds: 5, EnableCatalog: true, EnableDirect: true}
	handler, err := httpapi.New(cfg, f.s, catalog, f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	trace := regexp.MustCompile(`"traceId":"[0-9a-f]*"`)
	get := func(path, token string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://localhost"+path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, trace.ReplaceAllString(w.Body.String(), "")
	}
	forbidden := []string{f.root, f.hiddenRoot, "Movie/", "Alpha.2160p", "/api/v1/sources", "/subtitles/", "/audio/", "/stream", `"url"`}
	clean := func(stage, body string, extra ...string) {
		t.Helper()
		for _, marker := range append(forbidden, extra...) {
			if strings.Contains(body, marker) {
				t.Fatalf("%s exposes %q: %s", stage, marker, body)
			}
		}
	}
	status, body := get("/api/v1/items/"+f.alpha+"/details", f.tokenA)
	if status != 200 || !strings.Contains(body, `"overview":"Alpha overview"`) || !strings.Contains(body, `"genres":["Drama","Thriller"]`) || !strings.Contains(body, `"status":"unread"`) {
		t.Fatalf("details %d %s", status, body)
	}
	clean("details", body)
	// A confirmed NFO observation reports its state and time only.
	observation, err := json.Marshal(domain.LastConfirmedNFOObservation{Version: domain.NFOItemObservationVersion, Status: domain.NFOItemObservedMissing,
		SourceID: f.source, RootID: f.registration.RootID, Generation: 1, IdentityDigest: strings.Repeat("e", 64), CandidateDigest: domain.NFOCandidateDigest([]string{}),
		ReadAt: time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC), AcceptedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO item_nfo_observations(item_id,observation) VALUES($1::uuid,$2::jsonb)`, f.alpha, observation)
	status, body = get("/api/v1/items/"+f.alpha+"/details", f.tokenA)
	if status != 200 || !strings.Contains(body, `"nfo":{"fields":[],"readAt":"2026-09-01T08:30:00Z","status":"missing"}`) {
		t.Fatalf("details with observation %d %s", status, body)
	}
	clean("details with observation", body, f.source, f.registration.RootID, strings.Repeat("e", 64))
	status, body = get("/api/v1/items/"+f.alpha+"/sources", f.tokenA)
	if status != 200 || !strings.Contains(body, `"durationMicros":7200000000`) || !strings.Contains(body, `"codec":"truehd"`) || !strings.Contains(body, `"language":"zh"`) || !strings.Contains(body, `"commentary":true`) {
		t.Fatalf("sources %d %s", status, body)
	}
	clean("sources", body)
	status, body = get("/api/v1/items?libraryId="+f.registration.Library.ID+"&type=Movie&sort=productionYear&order=desc&q=a", f.tokenA)
	if status != 200 || !strings.Contains(body, `"total":2`) || !strings.Contains(body, f.alpha) || !strings.Contains(body, f.beta) {
		t.Fatalf("listing %d %s", status, body)
	}
	clean("listing", body, f.hidden, "Qz8")

	// Viewer B: hidden and missing answers are byte-identical.
	missing := "10000000-0000-4000-8000-000000000099"
	for _, suffix := range []string{"", "/details", "/sources"} {
		hiddenStatus, hiddenBody := get("/api/v1/items/"+f.alpha+suffix, f.tokenB)
		missingStatus, missingBody := get("/api/v1/items/"+missing+suffix, f.tokenB)
		if hiddenStatus != 404 || missingStatus != 404 || hiddenBody != missingBody {
			t.Fatalf("%s hidden %d %s vs missing %d %s", suffix, hiddenStatus, hiddenBody, missingStatus, missingBody)
		}
	}
	for _, query := range []string{"libraryId=" + f.registration.Library.ID, "parentId=" + f.registration.Library.ID, "parentId=" + f.series, "q=Alpha%20100"} {
		hiddenStatus, hiddenBody := get("/api/v1/items?"+query, f.tokenB)
		if hiddenStatus != 200 || !strings.Contains(hiddenBody, `"total":0`) || !strings.Contains(hiddenBody, `"data":[]`) {
			t.Fatalf("viewer B %s: %d %s", query, hiddenStatus, hiddenBody)
		}
		clean("viewer B "+query, hiddenBody, f.alpha, f.beta, f.series)
	}
	missingStatus, missingBody := get("/api/v1/items?libraryId="+missing, f.tokenB)
	hiddenStatus, hiddenBody := get("/api/v1/items?libraryId="+f.registration.Library.ID, f.tokenB)
	if missingStatus != hiddenStatus || missingBody != hiddenBody {
		t.Fatal("hidden library listing differs from a missing one")
	}
	// A revoked session sees nothing.
	imageRepositoryExec(t, f.jobFixture, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, f.viewerA.SessionID)
	if v, err := f.s.ListItemSources(f.ctx, f.viewerA, f.alpha); v != nil || err != domain.ErrNotFound {
		t.Fatal("revoked session reads sources", err)
	}
}
