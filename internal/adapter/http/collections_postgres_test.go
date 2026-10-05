package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// TestCollectionsAndPlaylistsHTTPPostgres drives collections and playlists
// through the real store (G02.1): administrators manage collections,
// viewers manage their own playlists, and an item the caller may not see
// is answered like a missing one wherever a body names it (G48.3).
func TestCollectionsAndPlaylistsHTTPPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	adminToken, err := store.Provision(ctx, "collections-admin", access.ClientNative, true)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, err := store.Provision(ctx, "collections-viewer", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	viewer := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='collections-viewer'`)
	library := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Collection Films') RETURNING id::text`)
	secret := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Secret Films') RETURNING id::text`)
	visible := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Visible Film','Movie') RETURNING id::text`, library)
	second := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Second Film','Movie') RETURNING id::text`, library)
	hidden := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Secret Film Zq9','Movie') RETURNING id::text`, secret)
	compatInsert(t, ctx, store, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid) RETURNING user_id::text`, viewer, library)
	for _, mode := range []int{0, http.StatusForbidden} {
		handler := leakHandler(t, store, leakConfig(t, dsn, mode))
		hiddenStatus := http.StatusNotFound
		if mode != 0 {
			hiddenStatus = mode
		}
		request := func(method, path, body, token string) *httptest.ResponseRecorder {
			t.Helper()
			var r *http.Request
			if body != "" {
				r = httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
			} else {
				r = httptest.NewRequest(method, "http://localhost"+path, nil)
			}
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if token == viewerToken && (strings.Contains(w.Body.String(), "Secret Film Zq9") || strings.Contains(w.Body.String(), hidden)) {
				t.Fatalf("%s %s leaks the hidden item: %s", method, path, w.Body.String())
			}
			return w
		}
		var collection struct {
			Data domain.CollectionView `json:"data"`
		}
		w := request("POST", "/api/v1/collections", `{"name":" Films ","overview":"","nfoName":null}`, adminToken)
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &collection) != nil || collection.Data.Collection.Name != "Films" {
			t.Fatalf("create collection: %d %s", w.Code, w.Body.String())
		}
		cid := collection.Data.Collection.ID
		if w = request("POST", "/api/v1/collections", `{"name":"Mine"}`, viewerToken); w.Code != http.StatusForbidden {
			t.Fatalf("viewer created a collection: %d", w.Code)
		}
		if w = request("POST", "/api/v1/collections", `{"name":"   "}`, adminToken); w.Code != http.StatusBadRequest {
			t.Fatalf("blank name: %d %s", w.Code, w.Body.String())
		}
		if w = request("POST", "/api/v1/collections/"+cid+"/items", `{"itemIds":["`+visible+`","`+hidden+`"]}`, adminToken); w.Code != http.StatusOK {
			t.Fatalf("add collection items: %d %s", w.Code, w.Body.String())
		}
		w = request("GET", "/api/v1/collections/"+cid, "", viewerToken)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), visible) || !strings.Contains(w.Body.String(), `"itemCount":1`) {
			t.Fatalf("viewer collection: %d %s", w.Code, w.Body.String())
		}
		if w = request("GET", "/api/v1/collections?limit=0", "", viewerToken); w.Code != http.StatusBadRequest {
			t.Fatalf("limit 0: %d", w.Code)
		}
		if w = request("DELETE", "/api/v1/collections/"+cid+"/items/"+visible, "", adminToken); w.Code != http.StatusOK {
			t.Fatalf("remove collection item: %d %s", w.Code, w.Body.String())
		}
		// Only the hidden member is left: the viewer no longer sees the
		// collection at all.
		if w = request("GET", "/api/v1/collections/"+cid, "", viewerToken); w.Code != hiddenStatus {
			t.Fatalf("viewer read a collection without visible members: %d", w.Code)
		}
		if w = request("GET", "/api/v1/collections", "", viewerToken); w.Code != http.StatusOK || strings.Contains(w.Body.String(), cid) {
			t.Fatalf("viewer listed a collection without visible members: %d %s", w.Code, w.Body.String())
		}
		if w = request("POST", "/api/v1/collections/nfo-sync", `{}`, adminToken); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"created":0`) {
			t.Fatalf("nfo sync: %d %s", w.Code, w.Body.String())
		}
		if w = request("DELETE", "/api/v1/collections/"+cid, "", adminToken); w.Code != http.StatusNoContent {
			t.Fatalf("delete collection: %d %s", w.Code, w.Body.String())
		}

		var playlist struct {
			Data domain.PlaylistView `json:"data"`
		}
		w = request("POST", "/api/v1/playlists", `{"name":"Evening","public":false}`, viewerToken)
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &playlist) != nil || !playlist.Data.Playlist.Owned {
			t.Fatalf("create playlist: %d %s", w.Code, w.Body.String())
		}
		pid := playlist.Data.Playlist.ID
		if w = request("POST", "/api/v1/playlists/"+pid+"/items", `{"itemIds":["`+visible+`","`+hidden+`"]}`, viewerToken); w.Code != hiddenStatus {
			t.Fatalf("hidden item appended: %d %s", w.Code, w.Body.String())
		}
		missing := "00000000-0000-4000-8000-00000000000a"
		if w = request("POST", "/api/v1/playlists/"+pid+"/items", `{"itemIds":["`+missing+`"]}`, viewerToken); w.Code != hiddenStatus {
			t.Fatalf("missing item appended: %d %s", w.Code, w.Body.String())
		}
		w = request("POST", "/api/v1/playlists/"+pid+"/items", `{"itemIds":["`+visible+`","`+second+`"]}`, viewerToken)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &playlist) != nil || len(playlist.Data.Entries) != 2 {
			t.Fatalf("append: %d %s", w.Code, w.Body.String())
		}
		first, last := playlist.Data.Entries[0].EntryID, playlist.Data.Entries[1].EntryID
		w = request("POST", "/api/v1/playlists/"+pid+"/entries/"+last+"/move", `{"beforeEntryId":"`+first+`"}`, viewerToken)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &playlist) != nil || playlist.Data.Entries[0].EntryID != last {
			t.Fatalf("move: %d %s", w.Code, w.Body.String())
		}
		if w = request("POST", "/api/v1/playlists/"+pid+"/entries/"+last+"/move", `{"beforeEntryId":null}`, viewerToken); w.Code != http.StatusOK {
			t.Fatalf("move to end: %d %s", w.Code, w.Body.String())
		}
		if w = request("GET", "/api/v1/playlists/"+pid, "", adminToken); w.Code != hiddenStatus {
			t.Fatalf("administrator read a private playlist: %d", w.Code)
		}
		if w = request("PUT", "/api/v1/playlists/"+pid, `{"name":"Evening","public":true}`, viewerToken); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"public":true`) {
			t.Fatalf("publish: %d %s", w.Code, w.Body.String())
		}
		if w = request("GET", "/api/v1/playlists", "", adminToken); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), pid) || !strings.Contains(w.Body.String(), `"owned":false`) {
			t.Fatalf("administrator list of a public playlist: %d %s", w.Code, w.Body.String())
		}
		if w = request("PUT", "/api/v1/playlists/"+pid, `{"name":"Taken"}`, adminToken); w.Code != http.StatusForbidden {
			t.Fatalf("administrator changed another user's playlist: %d", w.Code)
		}
		if w = request("DELETE", "/api/v1/playlists/"+pid+"/entries/"+first, "", viewerToken); w.Code != http.StatusOK || strings.Contains(w.Body.String(), first) {
			t.Fatalf("remove entry: %d %s", w.Code, w.Body.String())
		}
		if w = request("DELETE", "/api/v1/playlists/"+pid, "", viewerToken); w.Code != http.StatusNoContent {
			t.Fatalf("delete playlist: %d %s", w.Code, w.Body.String())
		}
		if w = request("GET", "/api/v1/playlists/"+pid, "", viewerToken); w.Code != hiddenStatus {
			t.Fatalf("deleted playlist read: %d", w.Code)
		}
	}
}
