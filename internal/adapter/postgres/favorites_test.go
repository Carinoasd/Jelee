package postgres

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func favoriteNames(f contentAccessFixture, page domain.FavoritePage) []string {
	names := []string{}
	for _, e := range page.Items {
		names = append(names, f.names[e.Item.ID])
	}
	return names
}

func (f contentAccessFixture) favorites(t *testing.T, u contentAccessUser, q domain.FavoriteQuery) domain.FavoritePage {
	t.Helper()
	if q.Limit == 0 {
		q.Limit = 50
	}
	page, err := f.s.ListFavorites(f.ctx, u.principal.UserID, q)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func (f contentAccessFixture) markFavorite(t *testing.T, u contentAccessUser, names ...string) {
	t.Helper()
	for _, name := range names {
		d, err := f.s.SetFavorite(f.ctx, u.principal.UserID, f.items[name], true)
		if err != nil || !d.IsFavorite || d.ItemID != f.items[name] {
			t.Fatalf("mark %s: %+v %v", name, d, err)
		}
	}
}

// TestFavoritesVisibilityPostgres checks marking, the list and every read
// that reports the mark against the unified filter (G24.2, G48.3): a hidden
// or ungranted item cannot be marked, is never listed or counted and has no
// user data, while its mark survives until access returns.
func TestFavoritesVisibilityPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	viewer, peer := f.viewer, f.peer
	f.markFavorite(t, viewer, "movie-g", "series-pg", "episode-pg", "movie-pg13")
	// The resume points of the fixture stay: marking changes no progress.
	d, err := f.s.SetFavorite(f.ctx, viewer.principal.UserID, f.items["movie-r"], true)
	if err != nil || !d.IsFavorite || d.ResumeTicks != 600000000 || d.Played {
		t.Fatalf("mark keeps progress: %+v %v", d, err)
	}
	// A library the viewer has no grant for: answered like a missing item.
	for _, id := range []string{f.items["movie-other"], "00000000-0000-4000-8000-0000000000f1"} {
		for _, mark := range []bool{true, false} {
			if _, err := f.s.SetFavorite(f.ctx, viewer.principal.UserID, id, mark); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("ungranted or missing %s mark=%t: %v", id, mark, err)
			}
		}
	}
	if n := syncCount(t, f.jobFixture, `SELECT count(*) FROM user_favorites WHERE user_id=$1::uuid`, viewer.principal.UserID); n != 5 {
		t.Fatalf("stored %d favourites", n)
	}

	// Newest first, with paging and a total.
	page := f.favorites(t, viewer, domain.FavoriteQuery{})
	if want := []string{"movie-r", "movie-pg13", "episode-pg", "series-pg", "movie-g"}; !slices.Equal(favoriteNames(f, page), want) || page.Total != 5 {
		t.Fatalf("list %v total %d", favoriteNames(f, page), page.Total)
	}
	if page.Items[0].FavoritedAt.IsZero() || page.Items[0].FavoritedAt.Before(page.Items[4].FavoritedAt) {
		t.Fatal("favourite times out of order")
	}
	if p := f.favorites(t, viewer, domain.FavoriteQuery{Offset: 1, Limit: 2}); !slices.Equal(favoriteNames(f, p), []string{"movie-pg13", "episode-pg"}) || p.Total != 5 {
		t.Fatalf("page %v %d", favoriteNames(f, p), p.Total)
	}
	if p := f.favorites(t, viewer, domain.FavoriteQuery{Offset: 9, Limit: 2}); len(p.Items) != 0 || p.Total != 5 {
		t.Fatalf("past the end %+v", p)
	}
	if p := f.favorites(t, viewer, domain.FavoriteQuery{Kinds: []string{"Series", "Episode"}}); !slices.Equal(favoriteNames(f, p), []string{"episode-pg", "series-pg"}) || p.Total != 2 {
		t.Fatalf("kinds %v", favoriteNames(f, p))
	}
	if p := f.favorites(t, viewer, domain.FavoriteQuery{LibraryID: f.other.Library.ID}); len(p.Items) != 0 || p.Total != 0 {
		t.Fatal("other library listed")
	}
	if episode := f.favorites(t, viewer, domain.FavoriteQuery{Kinds: []string{"Episode"}}); len(episode.Items) != 1 || episode.Items[0].Item.ParentID != f.items["season-pg"] {
		t.Fatalf("episode parent %+v", episode)
	}
	if _, err := f.s.ListFavorites(f.ctx, viewer.principal.UserID, domain.FavoriteQuery{Limit: 0}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid query accepted")
	}

	// Marks are per user.
	if p := f.favorites(t, peer, domain.FavoriteQuery{}); len(p.Items) != 0 {
		t.Fatalf("peer sees %v", favoriteNames(f, p))
	}

	// Marking again keeps the first time.
	before := page.Items[4].FavoritedAt
	f.markFavorite(t, viewer, "movie-g")
	if p := f.favorites(t, viewer, domain.FavoriteQuery{}); !p.Items[4].FavoritedAt.Equal(before) || f.names[p.Items[4].Item.ID] != "movie-g" {
		t.Fatal("marking again moved the favourite")
	}

	// Every read reports the mark of visible items.
	data, err := f.s.UserItemData(f.ctx, viewer.principal.UserID, f.ids("movie-g", "movie-16", "series-pg"))
	if err != nil || !data[f.items["movie-g"]].IsFavorite || data[f.items["movie-16"]].IsFavorite || !data[f.items["series-pg"]].IsFavorite {
		t.Fatalf("user data %+v %v", data, err)
	}
	if details, err := f.s.GetItemDetails(f.ctx, viewer.principal.UserID, f.items["series-pg"]); err != nil || !details.IsFavorite {
		t.Fatalf("details %+v %v", details.IsFavorite, err)
	}
	if details, err := f.s.GetItemDetails(f.ctx, peer.principal.UserID, f.items["series-pg"]); err != nil || details.IsFavorite {
		t.Fatal("peer details report the viewer's favourite")
	}
	if played, err := f.s.SetPlayed(f.ctx, viewer.principal.UserID, f.items["movie-g"], true, time.Now()); err != nil || !played.IsFavorite {
		t.Fatalf("played mark %+v %v", played, err)
	}
	resume, err := f.s.ListResume(f.ctx, viewer.principal.UserID, domain.ResumeQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range resume.Items {
		if e.Data.IsFavorite != slices.Contains([]string{"movie-pg13", "episode-pg", "movie-r"}, f.names[e.Item.ID]) {
			t.Fatalf("resume %s favourite=%t", f.names[e.Item.ID], e.Data.IsFavorite)
		}
	}
	yes, no := true, false
	browse := func(u contentAccessUser, favorite *bool) []string {
		t.Helper()
		page, err := f.s.BrowseItems(f.ctx, u.principal.UserID, domain.BrowseQuery{Scope: domain.BrowseAll, Limit: 100, Favorite: favorite})
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, item := range page.Items {
			names = append(names, f.names[item.ID])
		}
		slices.Sort(names)
		return names
	}
	if got := browse(viewer, &yes); !slices.Equal(got, []string{"episode-pg", "movie-g", "movie-pg13", "movie-r", "series-pg"}) {
		t.Fatalf("browse favourites %v", got)
	}
	if got := browse(viewer, &no); slices.ContainsFunc(got, func(n string) bool {
		return slices.Contains([]string{"episode-pg", "movie-g", "movie-pg13", "movie-r", "series-pg"}, n)
	}) ||
		len(got)+5 != len(browse(viewer, nil)) {
		t.Fatalf("browse others %v", got)
	}

	// Hiding an item takes its mark out of every read; the row stays.
	f.hideFrom(t, viewer, "movie-pg13", "series-pg")
	page = f.favorites(t, viewer, domain.FavoriteQuery{})
	// Hiding a series hides its seasons and episodes too.
	if want := []string{"movie-r", "movie-g"}; !slices.Equal(favoriteNames(f, page), want) || page.Total != 2 {
		t.Fatalf("after hiding %v total %d", favoriteNames(f, page), page.Total)
	}
	if got := browse(viewer, &yes); !slices.Equal(got, []string{"movie-g", "movie-r"}) {
		t.Fatalf("browse after hiding %v", got)
	}
	for _, mark := range []bool{true, false} {
		if _, err := f.s.SetFavorite(f.ctx, viewer.principal.UserID, f.items["movie-pg13"], mark); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("hidden item mark=%t: %v", mark, err)
		}
	}
	if data, err := f.s.UserItemData(f.ctx, viewer.principal.UserID, f.ids("movie-pg13")); err != nil || len(data) != 0 {
		t.Fatalf("hidden user data %+v %v", data, err)
	}
	if _, err := f.s.GetItemDetails(f.ctx, viewer.principal.UserID, f.items["series-pg"]); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("hidden details readable")
	}
	if n := syncCount(t, f.jobFixture, `SELECT count(*) FROM user_favorites WHERE user_id=$1::uuid`, viewer.principal.UserID); n != 5 {
		t.Fatalf("hiding removed marks: %d", n)
	}
	// A restored grant brings the mark back.
	if _, err := f.s.SetItemAccessRule(f.ctx, f.a, viewer.principal.UserID, f.items["movie-pg13"], domain.ItemAccessAllow); err != nil {
		t.Fatal(err)
	}
	if page = f.favorites(t, viewer, domain.FavoriteQuery{}); !slices.Contains(favoriteNames(f, page), "movie-pg13") {
		t.Fatalf("restored access %v", favoriteNames(f, page))
	}

	// Unmarking is idempotent.
	for range 2 {
		if d, err := f.s.SetFavorite(f.ctx, viewer.principal.UserID, f.items["movie-r"], false); err != nil || d.IsFavorite {
			t.Fatalf("unmark %+v %v", d, err)
		}
	}
	if slices.Contains(favoriteNames(f, f.favorites(t, viewer, domain.FavoriteQuery{})), "movie-r") {
		t.Fatal("unmarked item listed")
	}

	// The personal data export lists visible favourites only.
	var exported []string
	if err := f.s.ExportUserData(f.ctx, viewer.actor, viewer.principal.UserID, func(domain.UserDataExportHeader) error { return nil }, func(r domain.UserDataRecord) error {
		if r.Type == "favorite" {
			var row struct {
				ItemID string `json:"itemId"`
			}
			if err := json.Unmarshal(r.Data, &row); err != nil {
				return err
			}
			exported = append(exported, f.names[row.ItemID])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(exported)
	if !slices.Equal(exported, []string{"movie-g", "movie-pg13"}) {
		t.Fatalf("exported favourites %v", exported)
	}

	// Rows go with their item and with their user.
	imageRepositoryExec(t, f.jobFixture, `DELETE FROM items WHERE id=$1::uuid`, f.items["movie-g"])
	if n := syncCount(t, f.jobFixture, `SELECT count(*) FROM user_favorites WHERE item_id=$1::uuid`, f.items["movie-g"]); n != 0 {
		t.Fatal("favourite outlived its item")
	}
	if err := f.s.DeleteUser(f.ctx, f.a, viewer.principal.UserID); err != nil {
		t.Fatal(err)
	}
	if n := syncCount(t, f.jobFixture, `SELECT count(*) FROM user_favorites WHERE user_id=$1::uuid`, viewer.principal.UserID); n != 0 {
		t.Fatal("deleting the user kept favourites")
	}
}

// TestFavoritesFollowVersionMergePostgres checks that a merge marks the
// receiving item for every user who marked either item, and that undo
// restores both items' marks as they were (G20.5).
func TestFavoritesFollowVersionMergePostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	f.markFavorite(t, f.viewer, "movie-pg13")
	f.markFavorite(t, f.peer, "movie-g", "movie-pg13")
	op, err := f.s.MergeItems(f.ctx, f.a, f.items["movie-g"], f.items["movie-pg13"])
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []contentAccessUser{f.viewer, f.peer} {
		if got := favoriteNames(f, f.favorites(t, u, domain.FavoriteQuery{})); !slices.Equal(got, []string{"movie-g"}) {
			t.Fatalf("after merge %v", got)
		}
	}
	if _, err = f.s.UndoVersionOperation(f.ctx, f.a, op.ID); err != nil {
		t.Fatal(err)
	}
	if got := favoriteNames(f, f.favorites(t, f.viewer, domain.FavoriteQuery{})); !slices.Equal(got, []string{"movie-pg13"}) {
		t.Fatalf("viewer after undo %v", got)
	}
	got := favoriteNames(f, f.favorites(t, f.peer, domain.FavoriteQuery{}))
	slices.Sort(got)
	if !slices.Equal(got, []string{"movie-g", "movie-pg13"}) {
		t.Fatalf("peer after undo %v", got)
	}
}

// TestFavoritesMigrationRoundTrip checks that stored favourites block the
// downgrade and that an empty table round-trips with the ledger constraint.
func TestFavoritesMigrationRoundTrip(t *testing.T) {
	f := newContentAccessFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	f.markFavorite(t, f.viewer, "movie-g")
	want := downgradeAboveMigration(t, f.jobFixture, "user_favorites")
	if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
		t.Fatal("downgraded with a favourite")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=false`, want); err != nil {
		t.Fatal(err)
	}
	if n := syncCount(t, f.jobFixture, `SELECT count(*) FROM user_favorites`); n != 1 {
		t.Fatal("refused downgrade lost the favourite")
	}
	imageRepositoryExec(t, f.jobFixture, `DELETE FROM user_favorites`)
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if n := syncCount(t, f.jobFixture, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='user_favorites'`); n != 0 {
		t.Fatal("downgrade kept the table")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO legacy_import_map(kind,source_key,target_id,target_user,created) VALUES('favorite','k',$1::uuid,$1::uuid,false)`, f.viewer.principal.UserID); err == nil {
		t.Fatal("downgraded ledger accepts favourites")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	f.markFavorite(t, f.viewer, "movie-g")
}
