package httpapi

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// httpCollectionRepository records collection and playlist calls; hiddenID
// is answered like a missing collection, playlist or item.
type httpCollectionRepository struct {
	calls    []string
	hiddenID string
	input    domain.CollectionInput
	playlist domain.PlaylistInput
	ids      []string
	before   string
	cursor   string
	limit    int
}

func (r *httpCollectionRepository) record(name string, ids ...string) error {
	r.calls = append(r.calls, name)
	for _, id := range ids {
		if id == r.hiddenID {
			return domain.ErrNotFound
		}
	}
	return nil
}

func (r *httpCollectionRepository) collection(name string, ids ...string) (domain.CollectionView, error) {
	return domain.CollectionView{Items: []domain.CollectionMember{}}, r.record(name, ids...)
}

func (r *httpCollectionRepository) playlistView(name string, ids ...string) (domain.PlaylistView, error) {
	return domain.PlaylistView{Entries: []domain.PlaylistEntry{}}, r.record(name, ids...)
}

func (r *httpCollectionRepository) ListCollections(_ context.Context, _ domain.Actor, cursor string, limit int) (domain.CollectionPage, error) {
	r.cursor, r.limit = cursor, limit
	return domain.CollectionPage{Collections: []domain.Collection{}}, r.record("list-collections")
}
func (r *httpCollectionRepository) Collection(_ context.Context, _ domain.Actor, id string) (domain.CollectionView, error) {
	return r.collection("collection", id)
}
func (r *httpCollectionRepository) CreateCollection(_ context.Context, _ domain.Actor, in domain.CollectionInput) (domain.CollectionView, error) {
	r.input = in
	return r.collection("create-collection")
}
func (r *httpCollectionRepository) UpdateCollection(_ context.Context, _ domain.Actor, id string, in domain.CollectionInput) (domain.CollectionView, error) {
	r.input = in
	return r.collection("update-collection", id)
}
func (r *httpCollectionRepository) DeleteCollection(_ context.Context, _ domain.Actor, id string) error {
	return r.record("delete-collection", id)
}
func (r *httpCollectionRepository) AddCollectionItems(_ context.Context, _ domain.Actor, id string, items []string) (domain.CollectionView, error) {
	r.ids = items
	return r.collection("add-collection-items", append([]string{id}, items...)...)
}
func (r *httpCollectionRepository) RemoveCollectionItem(_ context.Context, _ domain.Actor, id, item string) (domain.CollectionView, error) {
	return r.collection("remove-collection-item", id, item)
}
func (r *httpCollectionRepository) SyncNFOCollections(context.Context, domain.Actor) (domain.CollectionNFOSync, error) {
	return domain.CollectionNFOSync{Created: 2}, r.record("sync")
}
func (r *httpCollectionRepository) ListPlaylists(_ context.Context, _ domain.Actor, cursor string, limit int) (domain.PlaylistPage, error) {
	r.cursor, r.limit = cursor, limit
	return domain.PlaylistPage{Playlists: []domain.Playlist{}}, r.record("list-playlists")
}
func (r *httpCollectionRepository) Playlist(_ context.Context, _ domain.Actor, id string) (domain.PlaylistView, error) {
	return r.playlistView("playlist", id)
}
func (r *httpCollectionRepository) CreatePlaylist(_ context.Context, _ domain.Actor, in domain.PlaylistInput) (domain.PlaylistView, error) {
	r.playlist = in
	return r.playlistView("create-playlist")
}
func (r *httpCollectionRepository) UpdatePlaylist(_ context.Context, _ domain.Actor, id string, in domain.PlaylistInput) (domain.PlaylistView, error) {
	r.playlist = in
	return r.playlistView("update-playlist", id)
}
func (r *httpCollectionRepository) DeletePlaylist(_ context.Context, _ domain.Actor, id string) error {
	return r.record("delete-playlist", id)
}
func (r *httpCollectionRepository) AddPlaylistItems(_ context.Context, _ domain.Actor, id string, items []string) (domain.PlaylistView, error) {
	r.ids = items
	return r.playlistView("add-playlist-items", append([]string{id}, items...)...)
}
func (r *httpCollectionRepository) RemovePlaylistEntry(_ context.Context, _ domain.Actor, id, entry string) (domain.PlaylistView, error) {
	return r.playlistView("remove-entry", id, entry)
}
func (r *httpCollectionRepository) MovePlaylistEntry(_ context.Context, _ domain.Actor, id, entry, before string) (domain.PlaylistView, error) {
	r.before = before
	return r.playlistView("move-entry", id, entry)
}

func TestCollectionRoutesAuthorizationBodiesAndErrors(t *testing.T) {
	admin, viewer := strings.Repeat("a", 43), strings.Repeat("n", 43)
	backend := &fakeBackend{auth: func(_ context.Context, token string) (access.Principal, error) {
		switch token {
		case admin:
			return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative, Admin: true}, nil
		case viewer:
			return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative}, nil
		}
		return access.Principal{}, domain.ErrUnauthenticated
	}}
	hidden := "77777777-7777-4777-8777-777777777777"
	repo := &httpCollectionRepository{hiddenID: hidden}
	if _, err := app.NewCatalog(&fakeRepository{}).WithCollections(nil); err == nil {
		t.Fatal("nil collection repository accepted")
	}
	catalog, err := app.NewCatalog(&fakeRepository{}).WithCollections(repo)
	if err != nil {
		t.Fatal(err)
	}
	cfg := validConfig()
	cfg.EnableCatalog = true
	handler, err := New(cfg, backend, catalog, &fakeResolver{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	collection := "/api/v1/collections/" + libraryID
	playlist := "/api/v1/playlists/" + libraryID
	adminOnly := []struct{ method, path, body string }{
		{"POST", "/api/v1/collections", `{"name":"Saga"}`},
		{"POST", "/api/v1/collections/nfo-sync", `{}`},
		{"PUT", collection, `{"name":"Saga","overview":"x","nfoName":"Saga"}`},
		{"DELETE", collection, ""},
		{"POST", collection + "/items", `{"itemIds":["` + itemID + `"]}`},
		{"DELETE", collection + "/items/" + itemID, ""},
	}
	for _, d := range adminOnly {
		if w := playbackHTTPRequest(handler, d.method, d.path, d.body, viewer); w.Code != 403 {
			t.Fatalf("%s %s by a viewer: %d %s", d.method, d.path, w.Code, w.Body.String())
		}
	}
	if len(repo.calls) != 0 {
		t.Fatal("viewer reached storage", repo.calls)
	}
	ok := []struct {
		method, path, body, token string
		status                    int
	}{
		{"GET", "/api/v1/collections?limit=5&cursor=" + itemID, "", viewer, 200},
		{"GET", collection, "", viewer, 200},
		{"POST", "/api/v1/collections", `{"name":"  Saga  ","nfoName":" Star "}`, admin, 201},
		{"POST", "/api/v1/collections/nfo-sync", `{}`, admin, 200},
		{"PUT", collection, `{"name":"Saga","overview":"line\nbreak","nfoName":null}`, admin, 200},
		{"POST", collection + "/items", `{"itemIds":["` + itemID + `","` + sourceID + `"]}`, admin, 200},
		{"DELETE", collection + "/items/" + itemID, "", admin, 200},
		{"DELETE", collection, "", admin, 204},
		{"GET", "/api/v1/playlists", "", viewer, 200},
		{"POST", "/api/v1/playlists", `{"name":" Evening ","public":true}`, viewer, 201},
		{"GET", playlist, "", viewer, 200},
		{"PUT", playlist, `{"name":"Night"}`, viewer, 200},
		{"POST", playlist + "/items", `{"itemIds":["` + itemID + `","` + itemID + `"]}`, viewer, 200},
		{"POST", playlist + "/entries/" + itemID + "/move", `{"beforeEntryId":"` + sourceID + `"}`, viewer, 200},
		{"POST", playlist + "/entries/" + itemID + "/move", `{"beforeEntryId":null}`, viewer, 200},
		{"DELETE", playlist + "/entries/" + itemID, "", viewer, 200},
		{"DELETE", playlist, "", viewer, 204},
	}
	for _, d := range ok {
		w := playbackHTTPRequest(handler, d.method, d.path, d.body, d.token)
		if w.Code != d.status || d.status != 204 && !strings.Contains(w.Body.String(), `"data"`) {
			t.Fatalf("%s %s: %d %s", d.method, d.path, w.Code, w.Body.String())
		}
	}
	if repo.input.Name != "Saga" || repo.input.NFOName != nil || repo.playlist.Name != "Night" || repo.playlist.Public || repo.before != "" || len(repo.ids) != 2 {
		t.Fatalf("inputs not passed through: %+v %+v %q %v", repo.input, repo.playlist, repo.before, repo.ids)
	}
	if repo.cursor != "" || repo.limit != collectionPageDefault {
		t.Fatalf("default page: %q %d", repo.cursor, repo.limit)
	}
	refused := []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/v1/collections?limit=201", "", 400},
		{"GET", "/api/v1/collections?cursor=x", "", 400},
		{"GET", "/api/v1/playlists?other=1", "", 400},
		{"GET", "/api/v1/collections/x", "", 404},
		{"GET", "/api/v1/collections/" + hidden, "", 404},
		{"GET", "/api/v1/playlists/" + hidden, "", 404},
		{"POST", "/api/v1/collections", `{"name":"   "}`, 400},
		{"POST", "/api/v1/collections", `{"name":"a","nfoName":""}`, 400},
		{"POST", "/api/v1/collections", `{"name":"a","x":1}`, 400},
		{"POST", "/api/v1/collections", `{"name":"a","overview":"\u0000"}`, 400},
		{"POST", collection + "/items", `{"itemIds":[]}`, 400},
		{"POST", collection + "/items", `{"itemIds":["x"]}`, 400},
		{"POST", collection + "/items", `{"itemIds":["` + hidden + `"]}`, 404},
		{"DELETE", collection + "/items/x", "", 404},
		{"POST", "/api/v1/playlists", `{"name":""}`, 400},
		{"POST", "/api/v1/playlists", `{"name":"a\tb"}`, 400},
		{"PUT", playlist, `{"name":"` + strings.Repeat("a", domain.CollectionNameMax+1) + `"}`, 400},
		{"POST", playlist + "/items", `{"itemIds":["` + hidden + `"]}`, 404},
		{"POST", playlist + "/entries/" + itemID + "/move", `{"beforeEntryId":""}`, 400},
		{"POST", playlist + "/entries/" + itemID + "/move", `{"beforeEntryId":"` + itemID + `"}`, 400},
		{"POST", playlist + "/entries/" + itemID + "/move", `{}`, 200},
		{"DELETE", playlist + "/entries/x", "", 404},
		{"DELETE", "/api/v1/playlists/x", "", 404},
		{"DELETE", "/api/v1/collections/x", "", 404},
	}
	for _, d := range refused {
		if w := playbackHTTPRequest(handler, d.method, d.path, d.body, admin); w.Code != d.status {
			t.Fatalf("%s %s %s: %d %s", d.method, d.path, d.body, w.Code, w.Body.String())
		}
	}
	// Without the repository every route answers 503 like other unwired
	// catalog features.
	bare, err := New(cfg, backend, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if w := playbackHTTPRequest(bare, "GET", "/api/v1/playlists", "", viewer); w.Code != 503 {
		t.Fatalf("unwired playlists: %d", w.Code)
	}
}

func TestCollectionServiceRejectsInvalidCalls(t *testing.T) {
	repo := &httpCollectionRepository{}
	catalog, err := app.NewCatalog(&fakeRepository{}).WithCollections(repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	actor := domain.Actor{UserID: userID, SessionID: sessionID}
	if _, err = catalog.ListPlaylists(ctx, domain.Actor{}, "", 1); err != domain.ErrUnauthenticated {
		t.Fatal("anonymous list", err)
	}
	if _, err = catalog.ListCollections(ctx, actor, "", 0); err != domain.ErrInvalid {
		t.Fatal("zero limit", err)
	}
	for name, err := range map[string]error{
		"collection": func() error { _, err := catalog.Collection(ctx, actor, "x"); return err }(),
		"update": func() error {
			_, err := catalog.UpdateCollection(ctx, actor, "x", domain.CollectionInput{Name: "a"})
			return err
		}(),
		"delete":   catalog.DeleteCollection(ctx, actor, "x"),
		"add":      func() error { _, err := catalog.AddCollectionItems(ctx, actor, "x", []string{itemID}); return err }(),
		"remove":   func() error { _, err := catalog.RemoveCollectionItem(ctx, actor, libraryID, "x"); return err }(),
		"playlist": func() error { _, err := catalog.Playlist(ctx, actor, "x"); return err }(),
		"update list": func() error {
			_, err := catalog.UpdatePlaylist(ctx, actor, "x", domain.PlaylistInput{Name: "a"})
			return err
		}(),
		"delete list": catalog.DeletePlaylist(ctx, actor, "x"),
		"append":      func() error { _, err := catalog.AddPlaylistItems(ctx, actor, "x", []string{itemID}); return err }(),
		"entry":       func() error { _, err := catalog.RemovePlaylistEntry(ctx, actor, libraryID, "x"); return err }(),
		"move":        func() error { _, err := catalog.MovePlaylistEntry(ctx, actor, libraryID, "x", ""); return err }(),
	} {
		if err != domain.ErrNotFound {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err = catalog.CreatePlaylist(ctx, actor, domain.PlaylistInput{Name: strings.Repeat("é", 600)}); err != domain.ErrInvalid {
		t.Fatal("long name", err)
	}
	if _, err = catalog.AddPlaylistItems(ctx, actor, libraryID, make([]string, domain.MembershipBatchMax+1)); err != domain.ErrInvalid {
		t.Fatal("large batch", err)
	}
	if len(repo.calls) != 0 {
		t.Fatal("invalid calls reached storage", repo.calls)
	}
}
