package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func hasVary(h http.Header, name string) bool {
	for _, v := range h.Values("Vary") {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), name) {
				return true
			}
		}
	}
	return false
}

func transportRequest(path, token string, header ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	return r
}

// TestCatalogReadsCarryValidators covers G25.2 on the catalog reads: a
// content ETag, the revalidated private policy, 304 on a match (also for
// the weak form a compressed response carries) and a different tag for a
// different projection.
func TestCatalogReadsCarryValidators(t *testing.T) {
	handler, repo, _, _ := itemsFixture(t, 0)
	repo.get = func(_ context.Context, _, id string) (domain.Item, error) {
		if id != itemID {
			return domain.Item{}, domain.ErrNotFound
		}
		return domain.Item{ID: itemID, LibraryID: libraryID, Title: "Arrival", Kind: "Movie"}, nil
	}
	token := strings.Repeat("n", 43)
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{
		"/api/v1/items",
		"/api/v1/items?offset=0",
		"/api/v1/items/" + itemID,
		"/api/v1/items/" + itemID + "/details",
		"/api/v1/items/" + itemID + "/sources",
	} {
		first := serve(transportRequest(path, token))
		etag := first.Header().Get("ETag")
		if first.Code != 200 || len(etag) != jsonETagHexLength+2 || etag != jsonETag(first.Body.Bytes()) {
			t.Fatalf("%s: %d ETag %q", path, first.Code, etag)
		}
		if first.Header().Get("Cache-Control") != "private, no-cache" || !hasVary(first.Header(), "Authorization") || !hasVary(first.Header(), "Cookie") {
			t.Fatalf("%s: cache policy %v", path, first.Header())
		}
		if first.Header().Get("Content-Length") != fmt.Sprint(first.Body.Len()) {
			t.Fatalf("%s: Content-Length %q for %d bytes", path, first.Header().Get("Content-Length"), first.Body.Len())
		}
		if again := serve(transportRequest(path, token)); again.Header().Get("ETag") != etag || !bytes.Equal(again.Body.Bytes(), first.Body.Bytes()) {
			t.Fatalf("%s: the same representation must keep its tag", path)
		}
		for _, match := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
			w := serve(transportRequest(path, token, "If-None-Match", match))
			if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("ETag") != etag || w.Header().Get("Cache-Control") != "private, no-cache" {
				t.Fatalf("%s If-None-Match %s: %d %q %v", path, match, w.Code, w.Body.String(), w.Header())
			}
		}
		if w := serve(transportRequest(path, token, "If-None-Match", `"stale"`)); w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("%s: stale validator must get the body: %d", path, w.Code)
		}
	}

	// A projection is another representation with its own tag.
	full := serve(transportRequest("/api/v1/items/"+itemID+"/details", token))
	slim := serve(transportRequest("/api/v1/items/"+itemID+"/details?fields=title", token))
	var got struct {
		Data map[string]any `json:"data"`
	}
	if slim.Code != 200 || json.Unmarshal(slim.Body.Bytes(), &got) != nil {
		t.Fatalf("fields: %d %s", slim.Code, slim.Body.String())
	}
	if keys := sortedKeys(got.Data); !slices.Equal(keys, []string{"id", "title"}) || got.Data["title"] != "Arrival" {
		t.Fatalf("details projection %v", got.Data)
	}
	if slim.Header().Get("ETag") == full.Header().Get("ETag") || slim.Body.Len() >= full.Body.Len() {
		t.Fatal("a projection must have its own, smaller representation")
	}
	if w := serve(transportRequest("/api/v1/items/"+itemID+"/details?fields=title", token, "If-None-Match", full.Header().Get("ETag"))); w.Code != 200 {
		t.Fatalf("the full representation's tag must not validate a projection: %d", w.Code)
	}
	one := serve(transportRequest("/api/v1/items/"+itemID+"?fields=kind,title", token))
	got.Data = nil
	if one.Code != 200 || json.Unmarshal(one.Body.Bytes(), &got) != nil || !slices.Equal(sortedKeys(got.Data), []string{"id", "kind", "title"}) {
		t.Fatalf("item projection %d %s", one.Code, one.Body.String())
	}
	for _, bad := range []string{"?fields=", "?fields=path", "?fields=title&fields=id", "?fields=title&x=1"} {
		if w := serve(transportRequest("/api/v1/items/"+itemID+bad, token)); w.Code != 400 || playbackErrorCode(t, w) != "invalid_request" {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	// Sources has no detail contract and still takes no query parameters.
	if w := serve(transportRequest("/api/v1/items/"+itemID+"/sources?fields=itemId", token)); w.Code != 400 {
		t.Errorf("sources accepted fields: %d", w.Code)
	}

	// Compressed: the tag is weakened by the compressor and still validates.
	big := serve(transportRequest("/api/v1/items/"+itemID+"/sources", token, "Accept-Encoding", "gzip"))
	if big.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("sources were not compressed (%d bytes)", big.Body.Len())
	}
	weak := big.Header().Get("ETag")
	if !strings.HasPrefix(weak, `W/"`) || big.Header().Get("Content-Length") != "" {
		t.Fatalf("compressed validator %v", big.Header())
	}
	if w := serve(transportRequest("/api/v1/items/"+itemID+"/sources", token, "Accept-Encoding", "gzip", "If-None-Match", weak)); w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("Content-Encoding") != "" {
		t.Fatalf("weak revalidation: %d %v", w.Code, w.Header())
	}

	// Errors carry no validator.
	if w := serve(transportRequest("/api/v1/items/"+libraryID+"/details", token)); w.Code != 404 || w.Header().Get("ETag") != "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("error response %d %v", w.Code, w.Header())
	}
	// The visibility check runs before the conditional: an unauthenticated
	// request with a valid tag is still 401.
	if w := serve(transportRequest("/api/v1/items/"+itemID, "x", "If-None-Match", "*")); w.Code != 401 {
		t.Fatalf("conditional before authentication: %d", w.Code)
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// TestAccountReadsCarryValidatorsAndStayNoStore: account reads answer
// If-None-Match but keep no-store, and a detail read accepts fields only on
// GET.
func TestAccountReadsCarryValidatorsAndStayNoStore(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := newAccountHTTPFixture(t, httpAccountRepository{get: func(_ context.Context, _ domain.Actor, id string) (domain.User, error) {
		return domain.User{ID: id, Name: "alice", DisplayName: "Alice", Locale: "en-US", AllowNative: true, CreatedAt: created}, nil
	}}, nil)
	me := f.serve(accountRequest(http.MethodGet, "/api/v1/users/me", "", "u"))
	etag := me.Header().Get("ETag")
	if me.Code != 200 || etag != jsonETag(me.Body.Bytes()) || me.Header().Get("Cache-Control") != "no-store" || hasVary(me.Header(), "Authorization") {
		t.Fatalf("users/me: %d %v", me.Code, me.Header())
	}
	r := accountRequest(http.MethodGet, "/api/v1/users/me", "", "u")
	r.Header.Set("If-None-Match", etag)
	if w := f.serve(r); w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("users/me conditional: %d %v", w.Code, w.Header())
	}
	slim := f.serve(accountRequest(http.MethodGet, "/api/v1/users/me?fields=displayName", "", "u"))
	var got struct {
		Data map[string]any `json:"data"`
	}
	if slim.Code != 200 || json.Unmarshal(slim.Body.Bytes(), &got) != nil || !slices.Equal(sortedKeys(got.Data), []string{"displayName", "id"}) {
		t.Fatalf("users/me fields: %d %s", slim.Code, slim.Body.String())
	}
	if w := f.serve(accountRequest(http.MethodGet, "/api/v1/users/me?fields=password", "", "u")); w.Code != 400 {
		t.Fatalf("unknown member: %d", w.Code)
	}
	// A route without a detail contract keeps refusing query parameters.
	if w := f.serve(accountRequest(http.MethodGet, "/api/v1/users/me/preferences?fields=theme", "", "u")); w.Code != 400 {
		t.Fatalf("preferences accepted fields: %d", w.Code)
	}
	// fields belongs to the read; a write on the same pattern refuses it.
	if w := f.serve(accountRequest(http.MethodPut, "/api/v1/users/"+userID+"?fields=name", `{}`, "a")); w.Code != 400 {
		t.Fatalf("PUT accepted fields: %d", w.Code)
	}
}

// TestValidatedJSONWriter covers the writer's own branches.
func TestValidatedJSONWriter(t *testing.T) {
	value := map[string]any{"data": map[string]any{"id": itemID}}
	serve := func(method string, header ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost/api/v1/items/"+itemID, nil)
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		router := chi.NewRouter()
		router.MethodFunc(method, "/api/v1/items/{id}", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Attachment") != "" {
				w.Header().Set("Content-Disposition", "attachment")
			}
			writeValidatedJSON(w, r, value)
		})
		router.ServeHTTP(w, r)
		return w
	}
	get := serve(http.MethodGet)
	if get.Code != 200 || get.Header().Get("ETag") == "" {
		t.Fatalf("GET %d %v", get.Code, get.Header())
	}
	head := serve(http.MethodHead)
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("ETag") != get.Header().Get("ETag") || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
		t.Fatalf("HEAD %d %v", head.Code, head.Header())
	}
	if post := serve(http.MethodPost, "If-None-Match", "*"); post.Code != 200 || post.Header().Get("ETag") != "" {
		t.Fatalf("POST must not be conditional: %d %v", post.Code, post.Header())
	}
	if download := serve(http.MethodGet, "X-Attachment", "1", "If-None-Match", "*"); download.Code != 200 || download.Header().Get("ETag") != "" {
		t.Fatalf("attachments are not validated: %d %v", download.Code, download.Header())
	}
	w := httptest.NewRecorder()
	writeValidatedJSON(w, httptest.NewRequest(http.MethodGet, "http://localhost/x", nil), map[string]any{"bad": func() {}})
	if w.Code != 500 || w.Header().Get("ETag") != "" {
		t.Fatalf("unencodable value: %d %v", w.Code, w.Header())
	}
	var q listRequest
	if out, err := q.projectOne(value); err != nil || fmt.Sprint(out) != fmt.Sprint(value) {
		t.Fatal("no projection must keep the value")
	}
	q.Fields = map[string]bool{"id": true}
	if _, err := q.projectOne(func() {}); err == nil {
		t.Fatal("unencodable value must fail projection")
	}
}

// TestOpenAPIDetailFieldsMatchSchema: every detail contract is a documented
// GET whose fields parameter is declared and whose selectable members are
// exactly the members of the data schema; every revalidated route is a
// documented GET.
func TestOpenAPIDetailFieldsMatchSchema(t *testing.T) {
	doc := referenceDocument(t)
	paths := doc["paths"].(map[string]any)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for path, c := range detailContracts {
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Errorf("%s: not documented", path)
			continue
		}
		op := item["get"].(map[string]any)
		declared := false
		for _, raw := range asList(op["parameters"]) {
			p := raw.(map[string]any)
			declared = declared || (p["in"] == "query" && p["name"] == "fields")
		}
		if !declared {
			t.Errorf("%s: fields is not declared", path)
		}
		data := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)["data"].(map[string]any)
		ref, _ := data["$ref"].(string)
		schema, _ := schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		members := make([]string, 0, len(props))
		for name := range props {
			members = append(members, name)
		}
		slices.Sort(members)
		fields := slices.Clone(c.Fields)
		slices.Sort(fields)
		if !slices.Equal(members, fields) {
			t.Errorf("%s: fields %v, schema members %v", path, fields, members)
		}
		if id := c.id(); id != "" && !slices.Contains(fields, id) {
			t.Errorf("%s: identifying member %s is not selectable", path, id)
		}
	}
	for path := range revalidatedRoutes {
		if item, ok := paths[path].(map[string]any); !ok || item["get"] == nil {
			t.Errorf("revalidated route %s is not a documented GET", path)
		}
	}
}

// TestTransportVolume quantifies G25.2 on a 1000 row catalog listing (ten
// pages at the 100 row maximum, encoded as one document so the numbers are
// comparable): identity, gzip at the default level and threshold, a two
// member projection, the projection compressed, and a 304 revalidation. The
// figures are recorded in docs/perf-report.md; the assertions are the
// acceptance floor.
func TestTransportVolume(t *testing.T) {
	titles := []string{"Arrival", "The Long Season", "Spirited Away", "Episode", "Pilot", "Blade Runner 2049", "鬼滅の刃", "進撃の巨人 The Final Season"}
	kinds := []string{"Movie", "Series", "Season", "Episode"}
	rows := make([]map[string]any, 0, 1000)
	for i := range 1000 {
		item := domain.BrowseItem{ID: fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i*7919), LibraryID: libraryID, Kind: kinds[i%len(kinds)],
			Title: fmt.Sprintf("%s %d", titles[i%len(titles)], i), PremiereDate: fmt.Sprintf("%04d-%02d-%02d", 1960+i%60, 1+i%12, 1+i%28), Year: 1960 + i%60}
		if i%3 != 0 {
			item.ParentID = fmt.Sprintf("%08x-1111-4000-8000-%012x", i/10, i/10)
		}
		rows = append(rows, catalogItemJSON(item))
	}
	contract := listContracts["/api/v1/items"]
	full := listRequest{contract: contract}
	slim := listRequest{contract: contract, Fields: map[string]bool{"id": true, "title": true}}
	z := newTestCompression(0) // the defaults: level 5, 1 KiB threshold
	measure := func(q listRequest, header ...string) *httptest.ResponseRecorder {
		data, err := q.project(rows)
		if err != nil {
			t.Fatal(err)
		}
		r := gzipRequest(header...)
		if !slices.Contains(header, "Accept-Encoding") {
			r.Header.Del("Accept-Encoding")
		}
		w := httptest.NewRecorder()
		serveCompressed(z, r, w, func(w http.ResponseWriter, r *http.Request) {
			writeValidatedJSON(w, r, map[string]any{"data": data, "pagination": map[string]any{"nextCursor": "", "limit": 100}})
		})
		return w
	}
	identity := measure(full)
	gzipped := measure(full, "Accept-Encoding", "gzip")
	projected := measure(slim)
	projectedGzip := measure(slim, "Accept-Encoding", "gzip")
	revalidated := measure(full, "Accept-Encoding", "gzip", "If-None-Match", gzipped.Header().Get("ETag"))
	if gzipped.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(gunzip(t, gzipped.Body.Bytes()), identity.Body.Bytes()) {
		t.Fatal("gzip representation differs from identity")
	}
	if revalidated.Code != http.StatusNotModified || revalidated.Body.Len() != 0 {
		t.Fatalf("revalidation %d with %d bytes", revalidated.Code, revalidated.Body.Len())
	}
	id, gz, pr, prgz := identity.Body.Len(), gzipped.Body.Len(), projected.Body.Len(), projectedGzip.Body.Len()
	t.Logf("1000 rows: identity %d B, gzip %d B (%.1f%%), fields=id,title %d B (%.1f%%), fields+gzip %d B (%.1f%%), 304 0 B",
		id, gz, 100*float64(gz)/float64(id), pr, 100*float64(pr)/float64(id), prgz, 100*float64(prgz)/float64(id))
	if gz*4 > id || pr*2 > id || prgz >= gz {
		t.Fatalf("transport floor not met: identity %d gzip %d projected %d projected+gzip %d", id, gz, pr, prgz)
	}
}

// TestTransportVolumeFattestRows measures the row type with the largest
// default shape, a metadata refresh review with its five stored candidates,
// on a full page (100 rows) against a summary projection. It backs the
// lightweight DTO recommendation in docs/perf-report.md: the default shape
// stays (jelee-cli and other clients read it) and summaries use fields.
func TestTransportVolumeFattestRows(t *testing.T) {
	year := 2016
	job := sourceID
	rows := make([]domain.MetadataRefreshReview, 0, 100)
	for i := range 100 {
		candidates := make([]domain.MetadataRefreshCandidate, 0, domain.MetadataRefreshMaxCandidates)
		for c := range domain.MetadataRefreshMaxCandidates {
			candidates = append(candidates, domain.MetadataRefreshCandidate{ProviderID: int32(1000 + i*10 + c), Title: fmt.Sprintf("Arrival Part %d", c), OriginalTitle: fmt.Sprintf("Story of Your Life %d", c), Date: "2016-11-11", Similarity: 90 - c*10})
		}
		rows = append(rows, domain.MetadataRefreshReview{ItemID: fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i), LibraryID: libraryID, Kind: "Movie", Title: fmt.Sprintf("Arrival %d", i), Revision: 3,
			State: "pending", Resource: "movie", Confidence: "low", Reason: "ambiguous", QueryTitle: "Arrival", QueryYear: &year, Candidates: candidates, JobID: &job, UpdatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)})
	}
	contract := listContracts["/api/v1/libraries/{id}/metadata/refresh/reviews"]
	z := newTestCompression(0)
	measure := func(fields map[string]bool, encoding string) int {
		q := listRequest{contract: contract, Fields: fields}
		data, err := q.project(map[string]any{"reviews": rows, "nextCursor": ""})
		if err != nil {
			t.Fatal(err)
		}
		r := gzipRequest()
		r.Header.Set("Accept-Encoding", encoding)
		w := httptest.NewRecorder()
		serveCompressed(z, r, w, func(w http.ResponseWriter, r *http.Request) { writeValidatedJSON(w, r, map[string]any{"data": data}) })
		return w.Body.Len()
	}
	summary := map[string]bool{"itemId": true, "title": true, "state": true, "confidence": true}
	full, fullGzip, slim, slimGzip := measure(nil, "identity"), measure(nil, "gzip"), measure(summary, "identity"), measure(summary, "gzip")
	t.Logf("100 reviews: identity %d B, gzip %d B (%.1f%%), summary fields %d B (%.1f%%), summary+gzip %d B (%.1f%%)",
		full, fullGzip, 100*float64(fullGzip)/float64(full), slim, 100*float64(slim)/float64(full), slimGzip, 100*float64(slimGzip)/float64(full))
	if slim*4 > full || fullGzip*4 > full {
		t.Fatalf("review rows: identity %d gzip %d summary %d", full, fullGzip, slim)
	}
}
