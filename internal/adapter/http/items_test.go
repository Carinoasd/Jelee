package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type itemsBrowseRepository struct {
	httpBrowseRepository
	queries []domain.BrowseQuery
	page    domain.BrowsePage
}

func (r *itemsBrowseRepository) BrowseItems(_ context.Context, user string, q domain.BrowseQuery) (domain.BrowsePage, error) {
	if user != userID {
		return domain.BrowsePage{}, domain.ErrInvalid
	}
	r.queries = append(r.queries, q)
	return r.page, nil
}

type itemsDetailsRepository struct {
	actor   domain.Actor
	details domain.ItemDetailsRecord
	sources []domain.PlaybackSourceRecord
}

func (r *itemsDetailsRepository) GetItemDetails(_ context.Context, user, id string) (domain.ItemDetailsRecord, error) {
	if user != userID || id != itemID {
		return domain.ItemDetailsRecord{}, domain.ErrNotFound
	}
	return r.details, nil
}

func (r *itemsDetailsRepository) ListItemSources(_ context.Context, actor domain.Actor, id string) ([]domain.PlaybackSourceRecord, error) {
	r.actor = actor
	if id != itemID {
		return nil, domain.ErrNotFound
	}
	return r.sources, nil
}

func itemsFixture(t *testing.T, hiddenStatus int) (http.Handler, *fakeRepository, *itemsBrowseRepository, *itemsDetailsRepository) {
	t.Helper()
	repo := &fakeRepository{}
	browse := &itemsBrowseRepository{page: domain.BrowsePage{Total: 7, Items: []domain.BrowseItem{
		{ID: itemID, LibraryID: libraryID, ParentID: libraryID, Kind: "Movie", Title: "Arrival", PremiereDate: "2016-11-11", Year: 2016},
		{ID: sourceID, LibraryID: libraryID, ParentID: itemID, Kind: "Episode", Title: "Pilot"},
	}}}
	n := func(v int64) *int64 { return &v }
	codec := func(s string) *string { return &s }
	details := &itemsDetailsRepository{
		details: domain.ItemDetailsRecord{Item: domain.BrowseItem{ID: itemID, LibraryID: libraryID, ParentID: libraryID, Kind: "Movie", Title: "Arrival",
			SortTitle: "Arrival 2016", Overview: "A linguist is recruited.", PremiereDate: "2016-11-11", Year: 2016},
			OriginalTitle: "Story of Your Life", Tagline: "Why are they here?", Genres: json.RawMessage(`["Drama","Science Fiction","Drama"]`),
			UniqueIDs: json.RawMessage(`[{"type":"tmdb","value":"329865","default":true},{"type":"imdb","value":"tt2543164"}]`),
			NFOFields: []string{"genres", "overview", "mpaa"}, Revision: 3},
		sources: []domain.PlaybackSourceRecord{{ID: sourceID, ContentType: "video/x-matroska", FileName: "Arrival.2160p.mkv",
			Metadata: &domain.MediaMetadata{Format: domain.MediaFormat{DurationMicros: n(6_960_000_000), SizeBytes: n(9_000_000_000)}, Streams: []domain.MediaStream{
				{Index: 0, Kind: "video", Codec: codec("hevc"), Video: &domain.MediaVideo{Width: n(3840), Height: n(2160)}},
				{Index: 1, Kind: "audio", Codec: codec("truehd"), Language: codec("en"), Audio: &domain.MediaAudio{Channels: n(8)}},
				{Index: 2, Kind: "subtitle", Codec: codec("hdmv_pgs_subtitle"), Language: codec("en")},
			}},
			Sidecars: []domain.SidecarTrackRecord{
				{ID: libraryID, SourceID: sourceID, Track: domain.SidecarTrack{Kind: "subtitle", Format: "srt", Language: "zh"}, Size: 10},
				{ID: userID, SourceID: sourceID, Track: domain.SidecarTrack{Kind: "audio", Format: "ac3", Language: "ja", Commentary: true}, Size: 20},
			}}},
	}
	catalog, err := app.NewCatalog(repo).WithBrowse(browse)
	if err == nil {
		catalog, err = catalog.WithDetails(details)
	}
	if err != nil {
		t.Fatal(err)
	}
	cfg := validConfig()
	cfg.EnableCatalog, cfg.EnableDirect = true, true
	cfg.Access.HiddenStatus = hiddenStatus
	handler, err := New(cfg, &fakeBackend{}, catalog, &fakeResolver{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler, repo, browse, details
}

func TestItemsListBrowseParameters(t *testing.T) {
	handler, repo, browse, _ := itemsFixture(t, 0)
	native := strings.Repeat("n", 43)
	parent := "66666666-6666-4666-8666-666666666666"
	for _, tc := range []struct {
		query string
		want  domain.BrowseQuery
	}{
		{"offset=0", domain.BrowseQuery{Scope: domain.BrowseAll, Limit: 50, Sort: []domain.BrowseSort{{Key: domain.BrowseSortName}}}},
		{"libraryId=" + libraryID + "&limit=20&offset=40", domain.BrowseQuery{Scope: domain.BrowseAll, LibraryID: libraryID, Limit: 20, Offset: 40, Sort: []domain.BrowseSort{{Key: domain.BrowseSortName}}}},
		{"parentId=" + parent + "&libraryId=" + libraryID, domain.BrowseQuery{Scope: domain.BrowseParent, ParentID: parent, LibraryID: libraryID, Limit: 50, Sort: []domain.BrowseSort{{Key: domain.BrowseSortName}}}},
		{"type=Movie,Series&type=Movie&type=Episode", domain.BrowseQuery{Scope: domain.BrowseAll, Kinds: []string{"Movie", "Series", "Episode"}, Limit: 50, Sort: []domain.BrowseSort{{Key: domain.BrowseSortName}}}},
		{"sort=productionYear,name&order=desc", domain.BrowseQuery{Scope: domain.BrowseAll, Limit: 50, Sort: []domain.BrowseSort{{Key: domain.BrowseSortProductionYear, Descending: true}, {Key: domain.BrowseSortName, Descending: true}}}},
		{"sort=premiereDate", domain.BrowseQuery{Scope: domain.BrowseAll, Limit: 50, Sort: []domain.BrowseSort{{Key: domain.BrowseSortPremiereDate}}}},
		{"q=50%25_off&limit=", domain.BrowseQuery{Scope: domain.BrowseAll, SearchTerm: "50%_off", Limit: 50, Sort: []domain.BrowseSort{{Key: domain.BrowseSortName}}}},
		{"q=", domain.BrowseQuery{Scope: domain.BrowseAll, Limit: 50, Sort: []domain.BrowseSort{{Key: domain.BrowseSortName}}}},
	} {
		browse.queries = nil
		w := playbackHTTPRequest(handler, "GET", "/api/v1/items?"+tc.query, "", native)
		if w.Code != 200 || len(browse.queries) != 1 || !reflect.DeepEqual(browse.queries[0], tc.want) {
			t.Fatalf("%s: %d %s %+v", tc.query, w.Code, w.Body.String(), browse.queries)
		}
		var page struct {
			Data       []map[string]any `json:"data"`
			Pagination map[string]any   `json:"pagination"`
		}
		if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Data) != 2 || page.Pagination["total"] != float64(7) ||
			page.Pagination["nextCursor"] != "" || page.Pagination["offset"] != float64(tc.want.Offset) || page.Pagination["limit"] != float64(tc.want.Limit) {
			t.Fatalf("%s: envelope %s", tc.query, w.Body.String())
		}
		// parentId only for a linked child, as in the cursor form.
		if _, ok := page.Data[0]["parentId"]; ok || page.Data[1]["parentId"] != itemID || page.Data[0]["productionYear"] != float64(2016) ||
			page.Data[0]["premiereDate"] != "2016-11-11" || page.Data[1]["productionYear"] != nil {
			t.Fatalf("%s: items %v", tc.query, page.Data)
		}
	}
	if repo.listCalls != 0 {
		t.Fatal("browse form reached the cursor listing")
	}

	// The original form is untouched by the new parameters.
	w := playbackHTTPRequest(handler, "GET", "/api/v1/items?limit=1", "", native)
	if w.Code != 200 || repo.listCalls != 1 || strings.Contains(w.Body.String(), `"total"`) {
		t.Fatalf("cursor form %d %s", w.Code, w.Body.String())
	}

	browse.queries = nil
	for _, query := range []string{
		"cursor=" + itemID + "&offset=0", "cursor=" + itemID + "&libraryId=" + libraryID, "offset=-1", "offset=1000001", "offset=x", "offset=1&offset=2",
		"libraryId=bad", "libraryId=AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", "parentId=bad", "parentId=" + parent + "&parentId=" + parent,
		"type=Library", "type=movie", "type=", "type=Movie,", "sort=title", "sort=name,name", "sort=", "order=up", "order=desc&order=asc",
		"q=" + strings.Repeat("a", 129), "q=a%00b", "limit=101&offset=0", "limit=0&q=a", "libraryid=" + libraryID,
	} {
		w := playbackHTTPRequest(handler, "GET", "/api/v1/items?"+query, "", native)
		if w.Code != 400 || playbackErrorCode(t, w) != "invalid_request" {
			t.Errorf("%s: %d %s", query, w.Code, w.Body.String())
		}
	}
	if len(browse.queries) != 0 {
		t.Fatal("invalid browse query reached storage")
	}
	if w := playbackHTTPRequest(handler, "GET", "/api/v1/items?q="+strings.Repeat("字", 128), "", native); w.Code != 200 {
		t.Fatalf("128 characters rejected: %d", w.Code)
	}
}

func TestItemDetailsAndSourcesHTTP(t *testing.T) {
	handler, _, _, details := itemsFixture(t, 0)
	native, web := strings.Repeat("n", 43), strings.Repeat("w", 43)
	for _, token := range []string{native, web} {
		w := playbackHTTPRequest(handler, "GET", "/api/v1/items/"+itemID+"/details", "", token)
		var got struct {
			Data map[string]any `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("details %d %s", w.Code, w.Body.String())
		}
		d := got.Data
		if d["overview"] != "A linguist is recruited." || d["originalTitle"] != "Story of Your Life" || d["tagline"] != "Why are they here?" ||
			d["productionYear"] != float64(2016) || d["sortTitle"] != "Arrival 2016" || d["parentId"] != nil {
			t.Fatalf("details fields %v", d)
		}
		if genres, _ := json.Marshal(d["genres"]); string(genres) != `["Drama","Science Fiction"]` {
			t.Fatalf("genres %s", genres)
		}
		if ids, _ := json.Marshal(d["externalIds"]); string(ids) != `[{"default":true,"type":"tmdb","value":"329865"},{"default":false,"type":"imdb","value":"tt2543164"}]` {
			t.Fatalf("external IDs %s", ids)
		}
		if nfo, _ := json.Marshal(d["nfo"]); string(nfo) != `{"fields":["overview","genres"],"status":"unread"}` {
			t.Fatalf("nfo %s", nfo)
		}

		w = playbackHTTPRequest(handler, "GET", "/api/v1/items/"+itemID+"/sources", "", token)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, `"durationMicros":6960000000`) || !strings.Contains(body, `"codec":"hevc"`) ||
			!strings.Contains(body, `"format":"pgs"`) || !strings.Contains(body, `"commentary":true`) || !strings.Contains(body, `"language":"zh"`) {
			t.Fatalf("sources %d %s", w.Code, body)
		}
		// No delivery route and no file name ever reaches a caller here.
		for _, marker := range []string{"/api/v1/sources", "/subtitles/", "/audio/", "/stream", `"url"`, "Arrival.2160p"} {
			if strings.Contains(body, marker) {
				t.Fatalf("sources expose %q: %s", marker, body)
			}
		}
		if details.actor.UserID != userID || details.actor.SessionID != sessionID {
			t.Fatal("sources not read for the caller's session")
		}
	}

	for _, path := range []string{"/details", "/sources"} {
		for _, tc := range []struct {
			name, path, token string
			status            int
			code              string
		}{
			{"missing", "/api/v1/items/" + libraryID + path, native, 404, "not_found"},
			{"malformed", "/api/v1/items/x" + path, native, 404, "not_found"},
			{"query", "/api/v1/items/" + itemID + path + "?fields=all", native, 400, "invalid_request"},
			{"anonymous", "/api/v1/items/" + itemID + path, "x", 401, "authentication_required"},
		} {
			w := playbackHTTPRequest(handler, "GET", tc.path, "", tc.token)
			if w.Code != tc.status || playbackErrorCode(t, w) != tc.code {
				t.Errorf("%s %s: %d %s", path, tc.name, w.Code, w.Body.String())
			}
		}
	}
	hidden, _, _, _ := itemsFixture(t, http.StatusForbidden)
	for _, path := range []string{"/details", "/sources"} {
		if w := playbackHTTPRequest(hidden, "GET", "/api/v1/items/"+libraryID+path, "", native); w.Code != 403 || playbackErrorCode(t, w) != "forbidden" {
			t.Errorf("hidden status %s: %d", path, w.Code)
		}
	}
}

func TestItemDetailsNFOStateHTTP(t *testing.T) {
	handler, _, _, details := itemsFixture(t, 0)
	read := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	digest := strings.Repeat("a", 64)
	details.details.Observation = &domain.LastConfirmedNFOObservation{Version: domain.NFOItemObservationVersion, Status: domain.NFOItemObservedMissing,
		SourceID: sourceID, RootID: libraryID, Generation: 1, IdentityDigest: digest, CandidateDigest: domain.NFOCandidateDigest([]string{}), ReadAt: read, AcceptedRevision: 2}
	w := playbackHTTPRequest(handler, "GET", "/api/v1/items/"+itemID+"/details", "", strings.Repeat("w", 43))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"missing"`) || !strings.Contains(w.Body.String(), `"readAt":"2026-09-01T08:30:00Z"`) {
		t.Fatalf("observation %d %s", w.Code, w.Body.String())
	}
	// Observation identities stay private.
	for _, marker := range []string{sourceID, digest} {
		if bytes.Contains(w.Body.Bytes(), []byte(marker)) {
			t.Fatalf("details expose observation identity %s", marker)
		}
	}
}
