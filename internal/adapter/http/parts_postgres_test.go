package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
)

// TestMultiPartPlaybackHTTPPostgres serves a multi-part version (G21.1)
// through the real store: the native playback information lists it as one
// version with its parts, every part is delivered as it is, and the
// compatibility layer reports the part count, points PlaybackInfo at part 1
// and lists the later parts under AdditionalParts with the item's own
// permissions: a hidden item and a missing item get the same answer.
func TestMultiPartPlaybackHTTPPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	viewerToken, err := store.Provision(ctx, "parts-viewer", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	viewer := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='parts-viewer'`)
	dir, hiddenDir := t.TempDir(), t.TempDir()
	for _, d := range []string{dir, hiddenDir} {
		for name, data := range map[string]string{"film - cd1.mkv": "first part", "film - cd2.mkv": "second part", "film 2160p.mkv": "single file"} {
			if err := os.WriteFile(filepath.Join(d, name), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	library := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Part Films') RETURNING id::text`)
	root := compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, library, dir)
	compatInsert(t, ctx, store, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid) RETURNING user_id::text`, viewer, library)
	hiddenLibrary := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Hidden Part Films') RETURNING id::text`)
	hiddenRoot := compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, hiddenLibrary, hiddenDir)

	movie := func(library, root, title string, files ...string) (string, []string) {
		item := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,'Movie') RETURNING id::text`, library, title)
		var sources []string
		for _, file := range files {
			sources = append(sources, compatInsert(t, ctx, store, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska') RETURNING id::text`, item, library, root, file))
		}
		return item, sources
	}
	parts := func(sources ...string) {
		for i, source := range sources {
			compatInsert(t, ctx, store, `INSERT INTO media_source_parts(source_id,version_source_id,part_number) VALUES($1::uuid,$2::uuid,$3) RETURNING source_id::text`, source, sources[0], i+1)
		}
	}
	item, sources := movie(library, root, "Part Film", "film - cd1.mkv", "film - cd2.mkv", "film 2160p.mkv")
	parts(sources[0], sources[1])
	// The multi-part version is the main version, so it is the default.
	compatInsert(t, ctx, store, `INSERT INTO item_primary_versions(item_id,library_id,source_id) VALUES($1::uuid,$2::uuid,$3::uuid) RETURNING item_id::text`, item, library, sources[0])
	hidden, hiddenSources := movie(hiddenLibrary, hiddenRoot, "Hidden Part Film", "film - cd1.mkv", "film - cd2.mkv")
	parts(hiddenSources...)

	handler := leakHandler(t, store, leakConfig(t, dsn, 0))
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		if strings.HasPrefix(path, "/compat/") {
			query := r.URL.Query()
			query.Set("ApiKey", viewerToken)
			r.URL.RawQuery = query.Encode()
		} else {
			r.Header.Set("Authorization", "Bearer "+viewerToken)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// Native playback information: two versions, the first in two parts.
	w := get("/api/v1/items/" + item + "/playback")
	var native struct {
		Data struct {
			Sources []struct {
				ID    string `json:"id"`
				Parts []struct {
					ID     string `json:"id"`
					Number int    `json:"number"`
				} `json:"parts"`
			} `json:"sources"`
		} `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &native) != nil || len(native.Data.Sources) != 2 {
		t.Fatalf("native: %d %s", w.Code, w.Body.String())
	}
	var multi *struct {
		ID    string `json:"id"`
		Parts []struct {
			ID     string `json:"id"`
			Number int    `json:"number"`
		} `json:"parts"`
	}
	for i := range native.Data.Sources {
		if native.Data.Sources[i].ID == sources[0] {
			multi = &native.Data.Sources[i]
		} else if native.Data.Sources[i].ID != sources[2] || len(native.Data.Sources[i].Parts) != 0 {
			t.Fatalf("single version: %s", w.Body.String())
		}
	}
	if multi == nil || len(multi.Parts) != 2 || multi.Parts[0].ID != sources[0] || multi.Parts[1].ID != sources[1] || multi.Parts[1].Number != 2 {
		t.Fatalf("parts: %s", w.Body.String())
	}
	// Every part is delivered as it is from its own source route.
	if w = get("/api/v1/sources/" + sources[1] + "/stream"); w.Code != 200 || w.Body.String() != "second part" {
		t.Fatalf("part 2 stream: %d %q", w.Code, w.Body.String())
	}

	// Compatibility layer.
	var detail struct{ PartCount int }
	if w = get("/compat/Items/" + leakWire(item)); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil {
		t.Fatalf("compat item: %d %s", w.Code, w.Body.String())
	}
	var additional struct {
		Items []struct {
			ID           string `json:"Id"`
			MediaSources []struct {
				ID string `json:"Id"`
			}
		}
		TotalRecordCount int
	}
	w = get("/compat/Videos/" + leakWire(item) + "/AdditionalParts")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &additional) != nil || additional.TotalRecordCount != 1 || len(additional.Items) != 1 ||
		additional.Items[0].ID != leakWire(item) || len(additional.Items[0].MediaSources) != 1 || additional.Items[0].MediaSources[0].ID != leakWire(sources[1]) {
		t.Fatalf("additional parts: %d %s", w.Code, w.Body.String())
	}
	// PlaybackInfo points at part 1 of the default version; a later part
	// is never listed as a version.
	var info struct {
		MediaSources []struct {
			ID string `json:"Id"`
		}
	}
	if w = get("/compat/Items/" + leakWire(item) + "/PlaybackInfo"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil || len(info.MediaSources) != 2 ||
		info.MediaSources[0].ID != leakWire(sources[0]) || info.MediaSources[1].ID != leakWire(sources[2]) || detail.PartCount != 2 {
		t.Fatalf("playback info: %d %s, part count %d", w.Code, w.Body.String(), detail.PartCount)
	}
	if w = get("/compat/Items/" + leakWire(item) + "/PlaybackInfo?MediaSourceId=" + leakWire(sources[1])); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil ||
		len(info.MediaSources) != 1 || info.MediaSources[0].ID != leakWire(sources[1]) {
		t.Fatalf("part 2 playback info: %d %s", w.Code, w.Body.String())
	}
	if w = get("/compat/Videos/" + leakWire(item) + "/stream?static=true&MediaSourceId=" + url.QueryEscape(leakWire(sources[1]))); w.Code != 200 || w.Body.String() != "second part" {
		t.Fatalf("compat part 2 stream: %d %q", w.Code, w.Body.String())
	}

	// With the single file as the default version the item has no
	// additional parts.
	compatInsert(t, ctx, store, `UPDATE item_primary_versions SET source_id=$2::uuid WHERE item_id=$1::uuid RETURNING item_id::text`, item, sources[2])
	if w = get("/compat/Videos/" + leakWire(item) + "/AdditionalParts"); w.Code != 200 || !strings.Contains(w.Body.String(), `"TotalRecordCount":0`) {
		t.Fatalf("single default: %d %s", w.Code, w.Body.String())
	}
	detail.PartCount = 0
	if w = get("/compat/Items/" + leakWire(item)); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.PartCount != 0 {
		t.Fatalf("single default part count: %s", w.Body.String())
	}

	// A hidden item is answered exactly like a missing one.
	hiddenAnswer := get("/compat/Videos/" + leakWire(hidden) + "/AdditionalParts")
	missingAnswer := get("/compat/Videos/" + leakWire("d0000000-0000-4000-8000-0000000000dd") + "/AdditionalParts")
	if hiddenAnswer.Code != 404 || missingAnswer.Code != 404 || hiddenAnswer.Body.String() != missingAnswer.Body.String() {
		t.Fatalf("hidden %d %q, missing %d %q", hiddenAnswer.Code, hiddenAnswer.Body.String(), missingAnswer.Code, missingAnswer.Body.String())
	}
	if w = get("/compat/Videos/" + leakWire(hidden) + "/stream?static=true&MediaSourceId=" + leakWire(hiddenSources[1])); w.Code != 404 {
		t.Fatalf("hidden part stream: %d", w.Code)
	}
	// A part of another item is not a version of this one.
	if w = get("/compat/Videos/" + leakWire(item) + "/stream?static=true&MediaSourceId=" + leakWire(hiddenSources[1])); w.Code != 404 {
		t.Fatalf("foreign part stream: %d", w.Code)
	}
}
