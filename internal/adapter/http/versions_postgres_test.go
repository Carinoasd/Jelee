package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
)

// TestVersionsAndTrackPreferencesHTTPPostgres drives the version decisions
// and the track preferences through the real store: preferences pick the
// default subtitle in the native playback information and in the
// compatibility PlaybackInfo, and the decisions answer viewers, hidden items
// and the G20.5 boundaries over HTTP.
func TestVersionsAndTrackPreferencesHTTPPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	adminToken, err := store.Provision(ctx, "versions-admin", access.ClientNative, true)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, err := store.Provision(ctx, "versions-viewer", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	viewer := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='versions-viewer'`)
	library := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Version Films') RETURNING id::text`)
	root := compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, library, t.TempDir())
	item := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Version Film','Movie') RETURNING id::text`, library)
	source := compatInsert(t, ctx, store, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'film.mkv','video/x-matroska') RETURNING id::text`, item, library, root)
	tracks := map[string]string{}
	for _, name := range []string{"film.en.srt", "film.ja.srt"} {
		tracks[name] = compatInsert(t, ctx, store, `INSERT INTO media_sidecar_tracks(source_id,library_id,root_id,relative_path,kind,format,language,languages,size,modified_unix_nano) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'subtitle','srt',$5,ARRAY[$5],10,0) RETURNING id::text`,
			source, library, root, name, strings.Split(name, ".")[1])
	}
	compatInsert(t, ctx, store, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid) RETURNING user_id::text`, viewer, library)
	handler := leakHandler(t, store, leakConfig(t, dsn, 0))
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body != "" {
			r = httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
		} else {
			r = httptest.NewRequest(method, "http://localhost"+path, nil)
		}
		if strings.HasPrefix(path, "/compat/") {
			r.URL.RawQuery = url.Values{"ApiKey": {token}}.Encode()
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	type defaults struct {
		Audio    *struct{ Kind, ID string } `json:"audio"`
		Subtitle *struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"subtitle"`
		Basis string `json:"basis"`
	}
	native := func(stage string) defaults {
		t.Helper()
		w := request("GET", "/api/v1/items/"+item+"/playback", "", viewerToken)
		var body struct {
			Data struct {
				Sources []struct {
					ID            string    `json:"id"`
					Primary       bool      `json:"primary"`
					DefaultTracks *defaults `json:"defaultTracks"`
				} `json:"sources"`
			} `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Data.Sources) != 1 || body.Data.Sources[0].DefaultTracks == nil {
			t.Fatalf("%s: %d %s", stage, w.Code, w.Body.String())
		}
		return *body.Data.Sources[0].DefaultTracks
	}
	compatIndex := func(stage string) string {
		t.Helper()
		w := request("GET", "/compat/Items/"+leakWire(item)+"/PlaybackInfo", "", viewerToken)
		var body struct {
			MediaSources []struct {
				DefaultSubtitleStreamIndex *int `json:"DefaultSubtitleStreamIndex"`
			} `json:"MediaSources"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.MediaSources) != 1 || body.MediaSources[0].DefaultSubtitleStreamIndex == nil {
			t.Fatalf("%s compat: %d %s", stage, w.Code, w.Body.String())
		}
		raw, _ := json.Marshal(*body.MediaSources[0].DefaultSubtitleStreamIndex)
		return string(raw)
	}

	// No preference: the account language alone finds no forced subtitle.
	if d := native("defaults"); d.Subtitle != nil || d.Basis != "locale" || compatIndex("defaults") != "-1" {
		t.Fatalf("defaults: %+v", d)
	}
	// Item level: Japanese subtitles always.
	if w := request("PUT", "/api/v1/items/"+item+"/track-preferences", `{"subtitleMode":"always","subtitleLanguage":"jpn"}`, viewerToken); w.Code != 200 || !strings.Contains(w.Body.String(), `"subtitleLanguage":"ja"`) {
		t.Fatalf("item preference: %d %s", w.Code, w.Body.String())
	}
	if d := native("item"); d.Subtitle == nil || d.Subtitle.ID != tracks["film.ja.srt"] || d.Basis != "item" || compatIndex("item") != "1" {
		t.Fatalf("item: %+v", d)
	}
	// Version level: subtitles off for this version only.
	if w := request("PUT", "/api/v1/items/"+item+"/track-preferences", `{"sourceId":"`+source+`","subtitleMode":"off"}`, viewerToken); w.Code != 200 {
		t.Fatalf("version preference: %d %s", w.Code, w.Body.String())
	}
	if d := native("version"); d.Subtitle != nil || d.Basis != "version" || compatIndex("version") != "-1" {
		t.Fatalf("version: %+v", d)
	}
	if w := request("GET", "/api/v1/items/"+item+"/track-preferences", "", viewerToken); w.Code != 200 || !strings.Contains(w.Body.String(), `"sourceId":"`+source+`"`) {
		t.Fatalf("read preferences: %d %s", w.Code, w.Body.String())
	}

	// Decisions over HTTP.
	if w := request("POST", "/api/v1/items/"+item+"/versions/split", `{"sourceId":"`+source+`"}`, adminToken); w.Code != 409 || !strings.Contains(w.Body.String(), "version_merge_incompatible") {
		t.Fatalf("only version split: %d %s", w.Code, w.Body.String())
	}
	second := compatInsert(t, ctx, store, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'film.2160p.mkv','video/x-matroska') RETURNING id::text`, item, library, root)
	if w := request("POST", "/api/v1/items/"+item+"/versions/split", `{"sourceId":"`+second+`","exclude":true}`, viewerToken); w.Code != 403 {
		t.Fatalf("viewer split: %d", w.Code)
	}
	w := request("POST", "/api/v1/items/"+item+"/versions/split", `{"sourceId":"`+second+`","exclude":true}`, adminToken)
	var split struct {
		Data struct {
			ID          string `json:"id"`
			OtherItemID string `json:"otherItemId"`
		} `json:"data"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &split) != nil || split.Data.OtherItemID == "" {
		t.Fatalf("split: %d %s", w.Code, w.Body.String())
	}
	compatInsert(t, ctx, store, `INSERT INTO item_metadata_facts(item_id,field,value,source,updated_at) VALUES($1::uuid,'uniqueIds','[{"type":"tmdb","value":"1"}]','manual',now()),($2::uuid,'uniqueIds','[{"type":"tmdb","value":"2"}]','manual',now()) RETURNING item_id::text`, item, split.Data.OtherItemID)
	if w := request("POST", "/api/v1/items/"+item+"/versions/merge", `{"sourceItemId":"`+split.Data.OtherItemID+`"}`, adminToken); w.Code != 409 || !strings.Contains(w.Body.String(), "version_identity_conflict") {
		t.Fatalf("conflicting merge: %d %s", w.Code, w.Body.String())
	}
	if w := request("PUT", "/api/v1/items/"+item+"/versions/primary", `{"sourceId":"`+source+`"}`, adminToken); w.Code != 200 {
		t.Fatalf("primary: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", "/api/v1/items/"+item+"/sources", "", viewerToken); w.Code != 200 || !strings.Contains(w.Body.String(), `"primary":true`) {
		t.Fatalf("primary in file information: %d %s", w.Code, w.Body.String())
	}
	w = request("GET", "/api/v1/items/"+item+"/versions", "", adminToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"fileName":"film.2160p.mkv"`) || !strings.Contains(w.Body.String(), `"primarySourceId":"`+source+`"`) || strings.Contains(w.Body.String(), "film.2160p.mkv\",\"relative") {
		t.Fatalf("overview: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/api/v1/version-operations/"+split.Data.ID+"/undo", `{}`, adminToken); w.Code != 200 || !strings.Contains(w.Body.String(), `"undoneAt"`) {
		t.Fatalf("undo split: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/api/v1/version-operations/"+split.Data.ID+"/undo", `{}`, adminToken); w.Code != 409 || !strings.Contains(w.Body.String(), "version_undo_unavailable") {
		t.Fatalf("second undo: %d %s", w.Code, w.Body.String())
	}
}
