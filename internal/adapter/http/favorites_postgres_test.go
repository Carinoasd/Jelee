package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
)

// TestFavoritesHTTPPostgres drives favourites (G24.2) through the real
// router and storage: the native mark, unmark and list routes with the list
// contract, the details and user data members, and the third-party favourite
// routes, user data and IsFavorite / Filters=IsFavorite listings. Hidden and
// missing items are answered alike and never listed.
func TestFavoritesHTTPPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	viewerToken, err := store.Provision(ctx, "favorite-viewer", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	viewer := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='favorite-viewer'`)
	visibleLibrary := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Favorite Visible') RETURNING id::text`)
	hiddenLibrary := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Favorite Hidden') RETURNING id::text`)
	film := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Favorite Film','Movie') RETURNING id::text`, visibleLibrary)
	other := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Other Film','Movie') RETURNING id::text`, visibleLibrary)
	show := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Favorite Show','Series') RETURNING id::text`, visibleLibrary)
	hidden := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Hidden Favorite Film','Movie') RETURNING id::text`, hiddenLibrary)
	compatInsert(t, ctx, store, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid) RETURNING user_id::text`, viewer, visibleLibrary)
	// A mark stored before access was withdrawn stays out of every read.
	compatInsert(t, ctx, store, `INSERT INTO user_favorites(user_id,item_id,library_id) VALUES($1::uuid,$2::uuid,$3::uuid) RETURNING item_id::text`, viewer, hidden, hiddenLibrary)
	progress, err := app.NewProgress(store, store, app.ProgressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := leakHandlerWithProgress(t, store, leakConfig(t, dsn, 0), &httpAccountPasswords{}, progress)
	native := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := accountRequest(method, path, body, "")
		if body == "" {
			r.Header.Del("Content-Type")
		}
		r.Header.Set("Authorization", "Bearer "+viewerToken)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, target any) {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	type userData struct {
		Data struct {
			ItemID     string `json:"itemId"`
			IsFavorite bool   `json:"isFavorite"`
		} `json:"data"`
	}

	// Marking: hidden and missing items are answered like missing ones.
	for _, id := range []string{hidden, leakUUID(t)} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			body := `{}`
			if method == http.MethodDelete {
				body = ""
			}
			if w := native(method, "/api/v1/items/"+id+"/favorite", body); w.Code != http.StatusNotFound {
				t.Fatalf("%s hidden or missing: %d %s", method, w.Code, w.Body.String())
			}
		}
	}
	for _, id := range []string{film, show, other} {
		var d userData
		decode(native(http.MethodPut, "/api/v1/items/"+id+"/favorite", `{}`), &d)
		if !d.Data.IsFavorite || d.Data.ItemID != id {
			t.Fatalf("mark %+v", d)
		}
	}
	var d userData
	decode(native(http.MethodDelete, "/api/v1/items/"+other+"/favorite", ""), &d)
	if d.Data.IsFavorite {
		t.Fatal("unmark kept the favourite")
	}
	decode(native(http.MethodGet, "/api/v1/items/"+film+"/user-data", ""), &d)
	if !d.Data.IsFavorite {
		t.Fatal("user data lacks the favourite")
	}
	var details struct {
		Data struct {
			IsFavorite bool `json:"isFavorite"`
		} `json:"data"`
	}
	decode(native(http.MethodGet, "/api/v1/items/"+show+"/details", ""), &details)
	if !details.Data.IsFavorite {
		t.Fatal("details lack the favourite")
	}
	decode(native(http.MethodGet, "/api/v1/items/"+other+"/details", ""), &details)
	if details.Data.IsFavorite {
		t.Fatal("details report an unmarked item")
	}

	// The list: newest first, offset cursor, filters and projection.
	type listPage struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			Total      int              `json:"total"`
			NextCursor string           `json:"nextCursor"`
		} `json:"data"`
	}
	ids := func(p listPage) []string {
		out := []string{}
		for _, item := range p.Data.Items {
			out = append(out, item["id"].(string))
		}
		return out
	}
	// A fresh page each time: decoding reuses the maps of an earlier one.
	list := func(query string) listPage {
		t.Helper()
		var p listPage
		decode(native(http.MethodGet, "/api/v1/users/me/favorites?"+query, ""), &p)
		return p
	}
	page := list("limit=1")
	if !slices.Equal(ids(page), []string{show}) || page.Data.Total != 2 || page.Data.NextCursor != "o.1" || page.Data.Items[0]["favoritedAt"] == nil {
		t.Fatalf("first page %+v", page)
	}
	page = list("limit=1&cursor=" + page.Data.NextCursor)
	if !slices.Equal(ids(page), []string{film}) || page.Data.NextCursor != "" {
		t.Fatalf("second page %+v", page)
	}
	page = list("type=Movie,HomeVideo&fields=title")
	if !slices.Equal(ids(page), []string{film}) || len(page.Data.Items[0]) != 2 || page.Data.Items[0]["title"] != "Favorite Film" {
		t.Fatalf("filtered projection %+v", page)
	}
	for _, query := range []string{"libraryId=" + hiddenLibrary, "libraryId=" + leakUUID(t)} {
		page = list(query)
		if len(page.Data.Items) != 0 || page.Data.Total != 0 {
			t.Fatalf("%s listed %+v", query, page)
		}
	}
	for _, query := range []string{"q=film", "sort=name", "order=asc", "type=Audio", "libraryId=x", "limit=501", "cursor=abc"} {
		if w := native(http.MethodGet, "/api/v1/users/me/favorites?"+query, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", query, w.Code)
		}
	}

	// The third-party layer.
	compat := func(method, target string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, compatUserRequest(method, target, "", compatTokenAuth(viewerToken)))
		return w
	}
	var dto struct {
		IsFavorite bool   `json:"IsFavorite"`
		ItemID     string `json:"ItemId"`
	}
	decodeCompat := func(w *httptest.ResponseRecorder, target any) {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	decodeCompat(compat(http.MethodPost, "/compat/UserFavoriteItems/"+leakWire(other)), &dto)
	if !dto.IsFavorite || dto.ItemID != leakWire(other) {
		t.Fatalf("compat mark %+v", dto)
	}
	decodeCompat(compat(http.MethodDelete, "/compat/Users/"+leakWire(viewer)+"/FavoriteItems/"+leakWire(other)), &dto)
	if dto.IsFavorite {
		t.Fatal("compat unmark")
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		for _, path := range []string{"/compat/UserFavoriteItems/" + leakWire(hidden), "/compat/Users/" + leakWire(viewer) + "/FavoriteItems/" + leakWire(leakUUID(t))} {
			if w := compat(method, path); w.Code != http.StatusNotFound {
				t.Fatalf("%s %s: %d", method, path, w.Code)
			}
		}
	}
	if n := compatInsert(t, ctx, store, `SELECT count(*)::text FROM user_favorites WHERE user_id=$1::uuid`, viewer); n != "3" {
		t.Fatalf("stored favourites %s", n)
	}
	type compatPage struct {
		Items []struct {
			ID       string `json:"Id"`
			UserData struct {
				IsFavorite bool `json:"IsFavorite"`
			} `json:"UserData"`
		} `json:"Items"`
		TotalRecordCount int `json:"TotalRecordCount"`
	}
	listed := func(target string) compatPage {
		t.Helper()
		var p compatPage
		decodeCompat(compat(http.MethodGet, target), &p)
		return p
	}
	wires := func(p compatPage, favorite bool) []string {
		out := []string{}
		for _, item := range p.Items {
			if item.UserData.IsFavorite != favorite {
				t.Fatalf("%s IsFavorite=%t", item.ID, item.UserData.IsFavorite)
			}
			out = append(out, item.ID)
		}
		slices.Sort(out)
		return out
	}
	favorites := []string{leakWire(film), leakWire(show)}
	slices.Sort(favorites)
	for _, target := range []string{"/compat/Items?Recursive=true&Filters=IsFavorite", "/compat/Users/" + leakWire(viewer) + "/Items?Recursive=true&IsFavorite=true&Filters=IsPlayed,IsFavoriteOrLikes",
		"/compat/Items?Ids=" + leakWire(film) + "," + leakWire(other) + "," + leakWire(show) + "," + leakWire(hidden) + "&Filters=IsFavorite"} {
		if p := listed(target); !slices.Equal(wires(p, true), favorites) || p.TotalRecordCount != 2 {
			t.Fatalf("%s: %+v", target, p)
		}
	}
	if p := listed("/compat/Items?Recursive=true&IsFavorite=false"); !slices.Equal(wires(p, false), []string{leakWire(other)}) || p.TotalRecordCount != 1 {
		t.Fatalf("not favourites %+v", p)
	}
	for _, target := range []string{"/compat/Items?Recursive=true&IsFavorite=false&Filters=IsFavorite", "/compat/Items?Filters=IsFavorite", "/compat/Items?ParentId=" + leakWire(hiddenLibrary) + "&Recursive=true&Filters=IsFavorite"} {
		if p := listed(target); len(p.Items) != 0 || p.TotalRecordCount != 0 {
			t.Fatalf("%s: %+v", target, p)
		}
	}
	if w := compat(http.MethodGet, "/compat/Items?IsFavorite=maybe"); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed IsFavorite: %d", w.Code)
	}
	// Item user data reports the mark for series too.
	var item struct {
		UserData struct {
			IsFavorite bool `json:"IsFavorite"`
		} `json:"UserData"`
	}
	decodeCompat(compat(http.MethodGet, "/compat/Items/"+leakWire(show)), &item)
	if !item.UserData.IsFavorite {
		t.Fatal("series user data lacks the favourite")
	}
	decodeCompat(compat(http.MethodGet, "/compat/UserItems/"+leakWire(film)+"/UserData"), &dto)
	if !dto.IsFavorite {
		t.Fatal("compat user data lacks the favourite")
	}
}
