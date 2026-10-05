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

// TestProgressHTTPPostgres drives playback progress end to end through the
// real router, sessions, progress buffer and storage: native reports
// (web refused, hidden items answered like missing ones), user data, played
// marks, the resume list, history clearing and the administrator listing;
// then the third-party report, played, user data and resume routes,
// including the item listing's user data.
func TestProgressHTTPPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	adminToken, err := store.Provision(ctx, "progress-admin", access.ClientNative, true)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, err := store.Provision(ctx, "progress-viewer", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	webToken, err := store.Provision(ctx, "progress-web", access.ClientWeb, false)
	if err != nil {
		t.Fatal(err)
	}
	viewer := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='progress-viewer'`)
	web := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='progress-web'`)
	root := t.TempDir()
	visibleLibrary := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Progress Visible') RETURNING id::text`)
	hiddenLibrary := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Progress Hidden') RETURNING id::text`)
	item := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Progress Film','Movie') RETURNING id::text`, visibleLibrary)
	hidden := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Progress Hidden Film','Movie') RETURNING id::text`, hiddenLibrary)
	for _, library := range []string{visibleLibrary, hiddenLibrary} {
		rootID := compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, library, root+"/"+library)
		compatInsert(t, ctx, store, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type)
 SELECT i.id,i.library_id,$2::uuid,'film.mkv','video/x-matroska' FROM items i WHERE i.library_id=$1::uuid RETURNING id::text`, library, rootID)
	}
	for _, user := range []string{viewer, web} {
		compatInsert(t, ctx, store, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid) RETURNING user_id::text`, user, visibleLibrary)
	}
	progress, err := app.NewProgress(store, store, app.ProgressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := leakHandlerWithProgress(t, store, leakConfig(t, dsn, 0), &httpAccountPasswords{}, progress)
	native := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := accountRequest(method, path, body, "")
		if body == "" {
			r.Header.Del("Content-Type")
		}
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, target any) {
		t.Helper()
		if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
			t.Fatalf("decode %d %s: %v", w.Code, w.Body.String(), err)
		}
	}
	rows := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := store.Pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Native reports.
	if w := native(http.MethodPost, "/api/v1/playback/start", webToken, `{"itemId":"`+item+`"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "web_playback_disabled") {
		t.Fatalf("web start: %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{hidden, leakUUID(t)} {
		if w := native(http.MethodPost, "/api/v1/playback/start", viewerToken, `{"itemId":"`+id+`"}`); w.Code != http.StatusNotFound {
			t.Fatalf("hidden or missing start: %d %s", w.Code, w.Body.String())
		}
	}
	if w := native(http.MethodPost, "/api/v1/playback/start", viewerToken, `{"itemId":"`+item+`","videoCodec":"h264"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "transcode_disabled") {
		t.Fatalf("transformation member: %d %s", w.Code, w.Body.String())
	}
	w := native(http.MethodPost, "/api/v1/playback/start", viewerToken, `{"itemId":"`+item+`","positionTicks":0}`)
	var started struct {
		Data struct {
			PlaySessionID         string `json:"playSessionId"`
			ReportIntervalSeconds int    `json:"reportIntervalSeconds"`
		} `json:"data"`
	}
	decode(w, &started)
	if w.Code != http.StatusOK || started.Data.PlaySessionID == "" || started.Data.ReportIntervalSeconds != 10 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	key := started.Data.PlaySessionID
	if w := native(http.MethodPost, "/api/v1/playback/progress", viewerToken, `{"playSessionId":"`+key+`","positionTicks":6000000000}`); w.Code != http.StatusNoContent {
		t.Fatalf("progress: %d %s", w.Code, w.Body.String())
	}
	var data struct {
		Data struct {
			ResumeTicks int64 `json:"resumeTicks"`
			Played      bool  `json:"played"`
			PlayCount   int   `json:"playCount"`
		} `json:"data"`
	}
	decode(native(http.MethodGet, "/api/v1/items/"+item+"/user-data", viewerToken, ""), &data)
	if data.Data.ResumeTicks != 6000000000 {
		t.Fatalf("buffered user data %+v", data)
	}
	if w := native(http.MethodGet, "/api/v1/items/"+hidden+"/user-data", viewerToken, ""); w.Code != http.StatusNotFound {
		t.Fatalf("hidden user data: %d", w.Code)
	}
	for range 2 {
		if w := native(http.MethodPost, "/api/v1/playback/stop", viewerToken, `{"playSessionId":"`+key+`","positionTicks":12000000000}`); w.Code != http.StatusNoContent {
			t.Fatalf("stop: %d %s", w.Code, w.Body.String())
		}
	}
	var resume struct {
		Data struct {
			Items []struct {
				ID       string `json:"id"`
				UserData struct {
					ResumeTicks int64 `json:"resumeTicks"`
				} `json:"userData"`
			} `json:"items"`
			Total int `json:"total"`
		} `json:"data"`
	}
	decode(native(http.MethodGet, "/api/v1/users/me/resume?limit=10", viewerToken, ""), &resume)
	if resume.Data.Total != 1 || resume.Data.Items[0].ID != item || resume.Data.Items[0].UserData.ResumeTicks != 12000000000 {
		t.Fatalf("resume %+v", resume)
	}
	if n := rows(`SELECT count(*) FROM playback_sessions WHERE user_id=$1::uuid AND state='stopped' AND delivery='direct'`, viewer); n != 1 {
		t.Fatal("stopped sessions", n)
	}
	if w := native(http.MethodGet, "/api/v1/playback/sessions", viewerToken, ""); w.Code != http.StatusForbidden {
		t.Fatalf("viewer listed sessions: %d", w.Code)
	}
	if w := native(http.MethodGet, "/api/v1/playback/sessions", adminToken, ""); w.Code != http.StatusOK {
		t.Fatalf("administrator sessions: %d %s", w.Code, w.Body.String())
	}
	decode(native(http.MethodPut, "/api/v1/items/"+item+"/played", viewerToken, `{}`), &data)
	if !data.Data.Played || data.Data.PlayCount != 1 || data.Data.ResumeTicks != 0 {
		t.Fatalf("played %+v", data)
	}
	decode(native(http.MethodDelete, "/api/v1/items/"+item+"/played", viewerToken, ""), &data)
	if data.Data.Played || data.Data.PlayCount != 0 {
		t.Fatalf("unplayed %+v", data)
	}
	if w := native(http.MethodPut, "/api/v1/items/"+hidden+"/played", viewerToken, `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("hidden played: %d", w.Code)
	}
	if w := native(http.MethodDelete, "/api/v1/users/me/playback-history", viewerToken, ""); w.Code != http.StatusNoContent {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if n := rows(`SELECT (SELECT count(*) FROM playback_sessions WHERE user_id=$1::uuid)+(SELECT count(*) FROM user_item_data WHERE user_id=$1::uuid)`, viewer); n != 0 {
		t.Fatal("history not cleared", n)
	}

	// Third-party reports, as a client sends them: extra members, a
	// bit rate ceiling and the play method are client state, not requests.
	itemWire, hiddenWire := leakWire(item), leakWire(hidden)
	compat := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, compatUserRequest(method, target, body, compatTokenAuth(viewerToken)))
		return w
	}
	report := func(path, body string) {
		t.Helper()
		if w := compat(http.MethodPost, path, body); w.Code != http.StatusNoContent || w.Body.Len() != 0 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	report("/compat/Sessions/Playing", `{"ItemId":"`+itemWire+`","PlaySessionId":"abc123","PositionTicks":0,"PlayMethod":"DirectPlay","MaxStreamingBitrate":140000000,"CanSeek":true,"NowPlayingQueue":[{"Id":"`+itemWire+`"}]}`)
	report("/compat/sessions/playing/progress", `{"ItemId":"`+itemWire+`","PlaySessionId":"abc123","PositionTicks":9000000000,"IsPaused":false,"EventName":"timeupdate"}`)
	report("/compat/Sessions/Playing/Ping?playSessionId=abc123", "")
	var dto struct {
		UserData struct {
			PlaybackPositionTicks int64  `json:"PlaybackPositionTicks"`
			Played                bool   `json:"Played"`
			PlayCount             int    `json:"PlayCount"`
			ItemID                string `json:"ItemId"`
		} `json:"UserData"`
	}
	decode(compat(http.MethodGet, "/compat/Items/"+itemWire, ""), &dto)
	if dto.UserData.PlaybackPositionTicks != 9000000000 || dto.UserData.ItemID != itemWire {
		t.Fatalf("item user data %+v", dto)
	}
	report("/compat/Sessions/Playing/Stopped", `{"ItemId":"`+itemWire+`","PlaySessionId":"abc123","PositionTicks":15000000000}`)
	var page struct {
		Items []struct {
			ID       string `json:"Id"`
			UserData struct {
				PlaybackPositionTicks int64 `json:"PlaybackPositionTicks"`
			} `json:"UserData"`
		} `json:"Items"`
		TotalRecordCount int `json:"TotalRecordCount"`
	}
	for _, path := range []string{"/compat/UserItems/Resume", "/compat/Users/" + leakWire(viewer) + "/Items/Resume", "/compat/Items?Recursive=true&IncludeItemTypes=Movie"} {
		decode(compat(http.MethodGet, path, ""), &page)
		if page.TotalRecordCount != 1 || page.Items[0].ID != itemWire || page.Items[0].UserData.PlaybackPositionTicks != 15000000000 {
			t.Fatalf("%s: %+v", path, page)
		}
	}
	// A hidden item's reports are answered alike and record nothing.
	report("/compat/Sessions/Playing", `{"ItemId":"`+hiddenWire+`","PlaySessionId":"h1"}`)
	report("/compat/Sessions/Playing/Stopped", `{"ItemId":"`+hiddenWire+`","PositionTicks":15000000000}`)
	if n := rows(`SELECT count(*) FROM playback_sessions WHERE item_id=$1::uuid`, hidden); n != 0 {
		t.Fatal("hidden item recorded", n)
	}
	if w := compat(http.MethodGet, "/compat/UserItems/"+hiddenWire+"/UserData", ""); w.Code != http.StatusNotFound {
		t.Fatalf("hidden user data: %d", w.Code)
	}
	if w := compat(http.MethodPost, "/compat/UserPlayedItems/"+hiddenWire, ""); w.Code != http.StatusNotFound {
		t.Fatalf("hidden played: %d", w.Code)
	}
	decode(compat(http.MethodPost, "/compat/UserPlayedItems/"+itemWire+"?datePlayed=20261004120000", ""), &dto.UserData)
	if !dto.UserData.Played || dto.UserData.PlayCount != 1 || dto.UserData.PlaybackPositionTicks != 0 {
		t.Fatalf("played %+v", dto.UserData)
	}
	decode(compat(http.MethodDelete, "/compat/Users/"+leakWire(viewer)+"/PlayedItems/"+itemWire, ""), &dto.UserData)
	if dto.UserData.Played || dto.UserData.PlayCount != 0 {
		t.Fatalf("unplayed %+v", dto.UserData)
	}
	// Malformed reports are refused; a report body that is not JSON too.
	if w := compat(http.MethodPost, "/compat/Sessions/Playing", `{"ItemId":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed item: %d", w.Code)
	}
	if w := compat(http.MethodPost, "/compat/Sessions/Playing/Ping", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("ping without id: %d", w.Code)
	}
	stats := progress.Stats()
	t.Logf("progress stats %+v", stats)
	if stats.StopWrites != 2 || stats.StartWrites < 2 {
		t.Fatalf("stats %+v", stats)
	}
}
