package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
)

// TestPlaybackSelectionHTTPPostgres drives G20.2 and G20.4 end to end: the
// version preference reorders native and compat playback information, a
// declaration only excludes, and a track switch reported during playback
// (compat stream indexes or native references) becomes the session choice
// that later playback information starts with, until it expires, a
// preference replaces it or the history is cleared.
func TestPlaybackSelectionHTTPPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	viewerToken, err := store.Provision(ctx, "selection-viewer", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	viewer := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='selection-viewer'`)
	library := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Selection Films') RETURNING id::text`)
	root := compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, library, t.TempDir())
	item := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Selection Film','Movie') RETURNING id::text`, library)
	uhd := compatInsert(t, ctx, store, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'Film.2160p.HDR.mkv','video/x-matroska') RETURNING id::text`, item, library, root)
	fhd := compatInsert(t, ctx, store, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'Film.1080p.mp4','video/mp4') RETURNING id::text`, item, library, root)
	tracks := map[string]string{}
	for _, language := range []string{"en", "ja"} {
		tracks[language] = compatInsert(t, ctx, store, `INSERT INTO media_sidecar_tracks(source_id,library_id,root_id,relative_path,kind,format,language,languages,size,modified_unix_nano) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'subtitle','srt',$5,ARRAY[$5],10,0) RETURNING id::text`,
			fhd, library, root, "Film.1080p."+language+".srt", language)
	}
	compatInsert(t, ctx, store, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid) RETURNING user_id::text`, viewer, library)
	progress, err := app.NewProgress(store, store, app.ProgressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := leakHandlerWithProgress(t, store, leakConfig(t, dsn, 0), &httpAccountPasswords{}, progress)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body != "" {
			r = httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
		} else {
			r = httptest.NewRequest(method, "http://localhost"+path, nil)
		}
		if strings.HasPrefix(path, "/compat/") {
			q := r.URL.Query()
			q.Set("ApiKey", viewerToken)
			r.URL.RawQuery = q.Encode()
		} else {
			r.Header.Set("Authorization", "Bearer "+viewerToken)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	type nativeSource struct {
		ID            string `json:"id"`
		DefaultTracks struct {
			Subtitle *struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"subtitle"`
			Basis string `json:"basis"`
		} `json:"defaultTracks"`
	}
	type nativeInfo struct {
		Data struct {
			Sources   []nativeSource `json:"sources"`
			Selection struct {
				DefaultSourceID string   `json:"defaultSourceId"`
				Reason          string   `json:"reason"`
				Preferences     []string `json:"preferences"`
			} `json:"selection"`
		} `json:"data"`
	}
	native := func(stage string) nativeInfo {
		t.Helper()
		w := request("GET", "/api/v1/items/"+item+"/playback", "")
		var body nativeInfo
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Data.Sources) != 2 {
			t.Fatalf("%s: %d %s", stage, w.Code, w.Body.String())
		}
		return body
	}
	type compatStream struct {
		Index      int    `json:"Index"`
		Type       string `json:"Type"`
		Language   string `json:"Language"`
		IsExternal bool   `json:"IsExternal"`
	}
	type compatSource struct {
		ID                         string         `json:"Id"`
		DefaultSubtitleStreamIndex *int           `json:"DefaultSubtitleStreamIndex"`
		MediaStreams               []compatStream `json:"MediaStreams"`
	}
	compatInfo := func(stage, query string) []compatSource {
		t.Helper()
		w := request("GET", "/compat/Items/"+leakWire(item)+"/PlaybackInfo"+query, "")
		var body struct {
			MediaSources []compatSource `json:"MediaSources"`
			ErrorCode    string         `json:"ErrorCode"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("%s compat: %d %s", stage, w.Code, w.Body.String())
		}
		return body.MediaSources
	}
	rows := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := store.Pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// G20.2: without a preference the better version leads.
	info := native("no preference")
	if info.Data.Sources[0].ID != uhd || info.Data.Selection.DefaultSourceID != uhd || info.Data.Selection.Reason != "quality_score" {
		t.Fatalf("no preference: %+v", info.Data.Selection)
	}
	if sources := compatInfo("no preference", ""); len(sources) != 2 || sources[0].ID != leakWire(uhd) {
		t.Fatalf("compat order %+v", sources)
	}
	if w := request("PUT", "/api/v1/users/me/version-preferences", `{"maxHeight":1080,"dynamicRange":"sdr"}`); w.Code != 200 {
		t.Fatalf("set version preference: %d %s", w.Code, w.Body.String())
	}
	info = native("SDR up to 1080p")
	if info.Data.Sources[0].ID != fhd || info.Data.Selection.Reason != "resolution_preference" || strings.Join(info.Data.Selection.Preferences, ",") != "resolution,dynamic_range" {
		t.Fatalf("preference order: %+v", info.Data.Selection)
	}
	if sources := compatInfo("SDR up to 1080p", ""); len(sources) != 2 || sources[0].ID != leakWire(fhd) {
		t.Fatalf("compat preference order %+v", sources)
	}
	// A declaration only excludes; the guard still refuses conversions.
	w := request("POST", "/compat/Items/"+leakWire(item)+"/PlaybackInfo", `{"DeviceProfile":{"DirectPlayProfiles":[{"Container":"mkv","Type":"Video"}]}}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Id":"`+leakWire(uhd)+`"`) || strings.Contains(w.Body.String(), leakWire(fhd)) {
		t.Fatalf("mkv-only client: %d %s", w.Code, w.Body.String())
	}
	w = request("POST", "/compat/Items/"+leakWire(item)+"/PlaybackInfo", `{"DeviceProfile":{"DirectPlayProfiles":[{"Container":"avi","Type":"Video"}]}}`)
	if w.Code != 200 || w.Body.String() != `{"MediaSources":[],"ErrorCode":"NoCompatibleStream","JeleeUnsupportedReasons":["ContainerNotSupported"]}` {
		t.Fatalf("nothing playable stays NoCompatibleStream: %d %s", w.Code, w.Body.String())
	}
	if w = request("GET", "/compat/Items/"+leakWire(item)+"/PlaybackInfo?VideoCodec=h264", ""); w.Code != http.StatusConflict {
		t.Fatalf("transformation accepted: %d", w.Code)
	}
	w = request("POST", "/api/v1/items/"+item+"/playback/check", `{"containers":["mkv"]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reason":"none_playable"`) {
		t.Fatalf("unprobed versions are never confirmed: %d %s", w.Code, w.Body.String())
	}

	// G20.4: a compat client switches to the Japanese subtitle.
	external := -1
	for _, s := range compatInfo("streams", "")[0].MediaStreams {
		if s.IsExternal && s.Type == "Subtitle" && strings.HasPrefix(s.Language, "ja") {
			external = s.Index
		}
	}
	if external < 0 {
		t.Fatal("Japanese external subtitle not listed")
	}
	report := func(path, body string) {
		t.Helper()
		if w := request("POST", path, body); w.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	start := `{"ItemId":"` + leakWire(item) + `","MediaSourceId":"` + leakWire(fhd) + `","PlaySessionId":"sel-1","PositionTicks":0`
	report("/compat/Sessions/Playing", start+`}`)
	report("/compat/Sessions/Playing/Progress", `{"ItemId":"`+leakWire(item)+`","PlaySessionId":"sel-1","PositionTicks":600000000,"SubtitleStreamIndex":`+selectionIndex(external)+`}`)
	if n := rows(`SELECT count(*) FROM session_track_selections WHERE user_id=$1::uuid AND item_id=$2::uuid AND source_id=$3::uuid AND play_key='sel-1' AND subtitle_track=$4`, viewer, item, fhd, "x:"+tracks["ja"]); n != 1 {
		t.Fatal("session choice not stored", n)
	}
	info = native("session choice")
	if s := info.Data.Sources[0]; s.ID != fhd || s.DefaultTracks.Basis != "session" || s.DefaultTracks.Subtitle == nil || s.DefaultTracks.Subtitle.ID != tracks["ja"] {
		t.Fatalf("session choice not applied: %+v", s)
	}
	if info.Data.Sources[1].DefaultTracks.Basis == "session" {
		t.Fatal("session choice applied to another version")
	}
	if s := compatInfo("session choice", "")[0]; s.DefaultSubtitleStreamIndex == nil || *s.DefaultSubtitleStreamIndex != external {
		t.Fatalf("compat resume index %+v", s)
	}
	// Turning subtitles off in the same session.
	report("/compat/Sessions/Playing/Stopped", `{"ItemId":"`+leakWire(item)+`","PlaySessionId":"sel-1","PositionTicks":900000000,"SubtitleStreamIndex":-1}`)
	if s := compatInfo("subtitles off", "")[0]; s.DefaultSubtitleStreamIndex == nil || *s.DefaultSubtitleStreamIndex != -1 {
		t.Fatalf("compat off %+v", s)
	}
	// A native client names the English file.
	if w := request("POST", "/api/v1/playback/start", `{"itemId":"`+item+`","sourceId":"`+fhd+`","subtitleTrack":"x:`+tracks["en"]+`"}`); w.Code != 200 {
		t.Fatalf("native start: %d %s", w.Code, w.Body.String())
	}
	if s := native("native choice").Data.Sources[0]; s.DefaultTracks.Basis != "session" || s.DefaultTracks.Subtitle == nil || s.DefaultTracks.Subtitle.ID != tracks["en"] {
		t.Fatalf("native choice: %+v", s)
	}
	if w := request("POST", "/api/v1/playback/progress", `{"itemId":"`+item+`","subtitleTrack":"on"}`); w.Code != 400 {
		t.Fatalf("malformed track: %d", w.Code)
	}
	// Expired choices are ignored.
	if _, err := store.Pool.Exec(ctx, `UPDATE session_track_selections SET updated_at=clock_timestamp()-interval '31 days' WHERE user_id=$1::uuid`, viewer); err != nil {
		t.Fatal(err)
	}
	if s := native("expired").Data.Sources[0]; s.DefaultTracks.Basis == "session" {
		t.Fatalf("expired choice applied: %+v", s)
	}
	// A stored preference of the item replaces the choice.
	if _, err := store.Pool.Exec(ctx, `UPDATE session_track_selections SET updated_at=clock_timestamp() WHERE user_id=$1::uuid`, viewer); err != nil {
		t.Fatal(err)
	}
	if w := request("PUT", "/api/v1/items/"+item+"/track-preferences", `{"subtitleMode":"off"}`); w.Code != 200 {
		t.Fatalf("item preference: %d %s", w.Code, w.Body.String())
	}
	if n := rows(`SELECT count(*) FROM session_track_selections WHERE user_id=$1::uuid`, viewer); n != 0 {
		t.Fatal("preference kept the session choice", n)
	}
	// Clearing the history removes choices too.
	report("/compat/Sessions/Playing", strings.Replace(start, "sel-1", "sel-2", 1)+`,"AudioStreamIndex":5,"SubtitleStreamIndex":`+selectionIndex(external)+`}`)
	if n := rows(`SELECT count(*) FROM session_track_selections WHERE user_id=$1::uuid`, viewer); n != 1 {
		t.Fatal("choice not recorded again", n)
	}
	if w := request("DELETE", "/api/v1/users/me/playback-history", ""); w.Code != http.StatusNoContent {
		t.Fatalf("clear: %d", w.Code)
	}
	if n := rows(`SELECT count(*) FROM session_track_selections WHERE user_id=$1::uuid`, viewer); n != 0 {
		t.Fatal("history clearing kept session choices", n)
	}
	if w := request("PUT", "/api/v1/users/me/version-preferences", `{}`); w.Code != 200 || rows(`SELECT count(*) FROM user_version_preferences WHERE user_id=$1::uuid`, viewer) != 0 {
		t.Fatalf("empty version preference kept: %d", w.Code)
	}
}

func selectionIndex(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}
