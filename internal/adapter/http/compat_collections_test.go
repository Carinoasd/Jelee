package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
)

type compatContainerItem struct {
	Name           string `json:"Name"`
	ID             string `json:"Id"`
	Type           string `json:"Type"`
	CollectionType string `json:"CollectionType"`
	ChildCount     *int   `json:"ChildCount"`
	PlaylistItemID string `json:"PlaylistItemId"`
	IsFolder       bool   `json:"IsFolder"`
}

type compatContainerPage struct {
	Items            []compatContainerItem `json:"Items"`
	TotalRecordCount int                   `json:"TotalRecordCount"`
}

// TestCompatCollectionsPostgres drives the collection and playlist module
// through the real tables and the native collection service: under every
// hiding mechanism the viewer's BoxSet and Playlist listings, contents and
// counts leave the hidden item out, a collection with only hidden members
// looks like a missing item, and the playlist changes follow the native
// owner rules (a public playlist of another user is read-only, a private
// one does not exist for them, hidden items are refused like missing ones).
func TestCompatCollectionsPostgres(t *testing.T) {
	for _, mechanism := range leakMechanisms {
		t.Run(mechanism, func(t *testing.T) {
			ctx, store, dsn := leakStore(t)
			f := leakFixture(t, ctx, store)
			var hiddenOnly string
			if err := store.Pool.QueryRow(ctx, `SELECT id::text FROM collections WHERE name='Hidden Only Collection Qx7'`).Scan(&hiddenOnly); err != nil {
				t.Fatal(err)
			}
			leakHideBy(t, ctx, store, &f, mechanism)
			handler := leakHandler(t, store, leakConfig(t, dsn, 0))
			do := func(method, target, token, body string) *httptest.ResponseRecorder {
				t.Helper()
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, compatUserRequest(method, target, body, compatTokenAuth(token)))
				return w
			}
			noMarkers := func(name string, w *httptest.ResponseRecorder) {
				t.Helper()
				for _, marker := range f.markers {
					if strings.Contains(w.Body.String(), marker) {
						t.Fatalf("%s: leaks hidden marker %q: %s", name, marker, w.Body.String())
					}
				}
			}
			page := func(target, token string) compatContainerPage {
				t.Helper()
				w := do(http.MethodGet, target, token, "")
				if w.Code != http.StatusOK {
					t.Fatalf("%s: %d %q", target, w.Code, w.Body.String())
				}
				if token == f.viewerToken {
					noMarkers(target, w)
				}
				var p compatContainerPage
				if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
					t.Fatal(err)
				}
				return p
			}
			empty := func(name string, w *httptest.ResponseRecorder, status int) {
				t.Helper()
				if w.Code != status || w.Body.Len() != 0 {
					t.Fatalf("%s: %d %q, want empty %d", name, w.Code, w.Body.String(), status)
				}
				noMarkers(name, w)
			}
			if f.guest {
				// Share guests use the native API only.
				empty("guest", do(http.MethodGet, "/compat/Playlists/"+leakWire(f.playlist)+"/Items", f.viewerToken, ""), http.StatusUnauthorized)
				empty("guest boxsets", do(http.MethodGet, "/compat/Items?IncludeItemTypes=BoxSet", f.viewerToken, ""), http.StatusUnauthorized)
				return
			}

			// Collections: the mixed one counts only the visible member;
			// the hidden-only one is not listed.
			boxsets := page("/compat/Items?IncludeItemTypes=BoxSet&Recursive=true", f.viewerToken)
			if boxsets.TotalRecordCount != 1 || len(boxsets.Items) != 1 || boxsets.Items[0].ID != leakWire(f.collection) || boxsets.Items[0].Type != "BoxSet" || *boxsets.Items[0].ChildCount != 1 {
				t.Fatalf("viewer boxsets: %+v", boxsets)
			}
			if control := page("/compat/Items?IncludeItemTypes=BoxSet&Recursive=true", f.adminToken); control.TotalRecordCount != 2 || control.Items[1].ID != leakWire(f.collection) || *control.Items[1].ChildCount != 2 {
				t.Fatalf("administrator boxsets: %+v", control)
			}
			members := page("/compat/Items?ParentId="+leakWire(f.collection), f.viewerToken)
			if members.TotalRecordCount != 1 || members.Items[0].ID != leakWire(f.visibleItem) {
				t.Fatalf("viewer members: %+v", members)
			}
			if p := page("/compat/Items?ParentId="+leakWire(hiddenOnly)+"&Recursive=true", f.viewerToken); p.TotalRecordCount != 0 {
				t.Fatalf("hidden-only members: %+v", p)
			}
			if p := page("/compat/Items?Ids="+leakWire(hiddenOnly)+","+leakWire(f.collection), f.viewerToken); p.TotalRecordCount != 1 || p.Items[0].ID != leakWire(f.collection) {
				t.Fatalf("ids: %+v", p)
			}
			hidden := do(http.MethodGet, "/compat/Items/"+leakWire(hiddenOnly), f.viewerToken, "")
			missing := do(http.MethodGet, "/compat/Items/"+leakWire(leakUUID(t)), f.viewerToken, "")
			empty("hidden-only collection", hidden, http.StatusNotFound)
			empty("missing collection", missing, http.StatusNotFound)
			if detail := do(http.MethodGet, "/compat/Items/"+leakWire(f.collection), f.viewerToken, ""); detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"ChildCount":1`) {
				t.Fatalf("collection detail: %d %s", detail.Code, detail.Body.String())
			} else {
				noMarkers("collection detail", detail)
			}

			// Playlists: the viewer's public playlist lists the visible entry
			// only, with its entry identifier.
			entries := page("/compat/Playlists/"+leakWire(f.playlist)+"/Items", f.viewerToken)
			var visibleEntry, hiddenEntry string
			if err := store.Pool.QueryRow(ctx, `SELECT (SELECT id::text FROM playlist_items WHERE playlist_id=$1::uuid AND item_id=$2::uuid),(SELECT id::text FROM playlist_items WHERE playlist_id=$1::uuid AND item_id=$3::uuid)`,
				f.playlist, f.visibleItem, f.item[1]).Scan(&visibleEntry, &hiddenEntry); err != nil {
				t.Fatal(err)
			}
			if entries.TotalRecordCount != 1 || entries.Items[0].ID != leakWire(f.visibleItem) || entries.Items[0].PlaylistItemID != leakWire(visibleEntry) {
				t.Fatalf("viewer entries: %+v", entries)
			}
			if control := page("/compat/Playlists/"+leakWire(f.playlist)+"/Items", f.adminToken); control.TotalRecordCount != 2 {
				t.Fatalf("administrator entries: %+v", control)
			}
			if p := page("/compat/Items?IncludeItemTypes=Playlist", f.viewerToken); p.TotalRecordCount != 1 || *p.Items[0].ChildCount != 1 || p.Items[0].Type != "Playlist" {
				t.Fatalf("viewer playlists: %+v", p)
			}
			views := page("/compat/UserViews", f.viewerToken)
			var folders []string
			for _, v := range views.Items {
				folders = append(folders, v.CollectionType)
			}
			if !slices.Contains(folders, "boxsets") || !slices.Contains(folders, "playlists") {
				t.Fatalf("virtual folders: %+v", views)
			}
			// A hidden item is refused like a missing one, and a hidden
			// entry cannot be moved or removed through the layer.
			refusedHidden := do(http.MethodPost, "/compat/Playlists/"+leakWire(f.playlist)+"/Items?Ids="+leakWire(f.item[1]), f.viewerToken, "")
			refusedMissing := do(http.MethodPost, "/compat/Playlists/"+leakWire(f.playlist)+"/Items?Ids="+leakWire(leakUUID(t)), f.viewerToken, "")
			empty("add hidden", refusedHidden, http.StatusNotFound)
			empty("add missing", refusedMissing, http.StatusNotFound)
			empty("move hidden", do(http.MethodPost, "/compat/Playlists/"+leakWire(f.playlist)+"/Items/"+leakWire(hiddenEntry)+"/Move/0", f.viewerToken, ""), http.StatusNotFound)
			if w := do(http.MethodDelete, "/compat/Playlists/"+leakWire(f.playlist)+"/Items?EntryIds="+leakWire(hiddenEntry)+","+leakWire(f.item[1]), f.viewerToken, ""); w.Code != http.StatusNoContent {
				t.Fatalf("remove hidden: %d", w.Code)
			}
			var count int
			if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM playlist_items WHERE playlist_id=$1::uuid`, f.playlist).Scan(&count); err != nil || count != 2 {
				t.Fatalf("hidden entry changed: %d %v", count, err)
			}
			if mechanism != "library_grant" {
				return
			}
			compatPlaylistChanges(ctx, t, store, f, do, page)
		})
	}
}

// compatPlaylistChanges checks the owner rules and the order of playlist
// changes through the layer, with a second user granted the visible library.
func compatPlaylistChanges(ctx context.Context, t *testing.T, store *postgres.Store, f leakIDs, do func(method, target, token, body string) *httptest.ResponseRecorder, page func(target, token string) compatContainerPage) {
	t.Helper()
	otherToken, err := store.Provision(ctx, "compat-playlist-other", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) SELECT id,$1::uuid FROM users WHERE name='compat-playlist-other'`, f.visibleLibrary); err != nil {
		t.Fatal(err)
	}
	playlist := "/compat/Playlists/" + leakWire(f.playlist) + "/Items"
	// Another user reads the public playlist but cannot change it.
	if p := page(playlist, otherToken); p.TotalRecordCount != 1 {
		t.Fatalf("other reads public: %+v", p)
	}
	for _, change := range []struct{ method, target string }{
		{http.MethodPost, playlist + "?Ids=" + leakWire(f.visibleItem)},
		{http.MethodDelete, playlist + "?EntryIds=" + leakWire(f.visibleItem)},
		{http.MethodPost, playlist + "/" + leakWire(f.visibleItem) + "/Move/0"},
	} {
		if w := do(change.method, change.target, otherToken, ""); w.Code != http.StatusForbidden || w.Body.Len() != 0 {
			t.Fatalf("other changes public %s %s: %d", change.method, change.target, w.Code)
		}
	}
	// Private: another user and the administrator get the hidden status.
	if _, err = store.Pool.Exec(ctx, `UPDATE playlists SET public=false WHERE id=$1::uuid`, f.playlist); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{otherToken, f.adminToken} {
		for _, w := range []*httptest.ResponseRecorder{do(http.MethodGet, playlist, token, ""), do(http.MethodPost, playlist+"?Ids="+leakWire(f.visibleItem), token, "")} {
			if w.Code != http.StatusNotFound || w.Body.Len() != 0 {
				t.Fatalf("private playlist of another user: %d", w.Code)
			}
		}
	}

	// The owner creates a playlist with an item, appends, moves and removes.
	w := do(http.MethodPost, "/compat/Playlists", f.viewerToken, `{"Name":"Compat Road Trip","Ids":["`+leakWire(f.visibleItem)+`"],"MediaType":"Video"}`)
	var created struct {
		ID string `json:"Id"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &created) != nil || len(created.ID) != 32 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var owner string
	var public bool
	if err = store.Pool.QueryRow(ctx, `SELECT owner_id::text,public FROM playlists WHERE replace(id::text,'-','')=$1`, created.ID).Scan(&owner, &public); err != nil || owner != f.viewer || public {
		t.Fatalf("created row: %s %v %v", owner, public, err)
	}
	// A hidden item fails the creation and leaves no playlist behind.
	if w := do(http.MethodPost, "/compat/Playlists?name=Compat%20Hidden&ids="+leakWire(f.item[1]), f.viewerToken, ""); w.Code != http.StatusNotFound || w.Body.Len() != 0 {
		t.Fatalf("create with hidden: %d %s", w.Code, w.Body.String())
	}
	var leftover int
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM playlists WHERE name='Compat Hidden'`).Scan(&leftover); err != nil || leftover != 0 {
		t.Fatalf("leftover playlist: %d %v", leftover, err)
	}
	mine := "/compat/Playlists/" + created.ID + "/Items"
	if w := do(http.MethodPost, mine+"?Ids="+leakWire(f.visibleItem)+","+leakWire(f.visibleItem), f.viewerToken, ""); w.Code != http.StatusNoContent {
		t.Fatalf("append: %d", w.Code)
	}
	entries := page(mine, f.viewerToken)
	if entries.TotalRecordCount != 3 {
		t.Fatalf("entries: %+v", entries)
	}
	first, last := entries.Items[0].PlaylistItemID, entries.Items[2].PlaylistItemID
	if w := do(http.MethodPost, mine+"/"+last+"/Move/0", f.viewerToken, ""); w.Code != http.StatusNoContent {
		t.Fatalf("move: %d", w.Code)
	}
	if moved := page(mine, f.viewerToken); moved.Items[0].PlaylistItemID != last || moved.Items[1].PlaylistItemID != first {
		t.Fatalf("moved order: %+v", moved)
	}
	if w := do(http.MethodPost, mine+"/"+last+"/Move/99", f.viewerToken, ""); w.Code != http.StatusNoContent {
		t.Fatalf("move to end: %d", w.Code)
	}
	if moved := page(mine, f.viewerToken); moved.Items[2].PlaylistItemID != last {
		t.Fatalf("moved to end: %+v", moved)
	}
	if w := do(http.MethodDelete, mine+"?EntryIds="+first, f.viewerToken, ""); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d", w.Code)
	}
	if left := page(mine, f.viewerToken); left.TotalRecordCount != 2 || slices.ContainsFunc(left.Items, func(i compatContainerItem) bool { return i.PlaylistItemID == first }) {
		t.Fatalf("after remove: %+v", left)
	}
	// Writing as another user is refused before any change, also for the
	// administrator.
	if w := do(http.MethodPost, mine+"?Ids="+leakWire(f.visibleItem)+"&UserId="+leakWire(f.viewer), f.adminToken, ""); w.Code != http.StatusForbidden {
		t.Fatalf("admin writes as owner: %d", w.Code)
	}
}
