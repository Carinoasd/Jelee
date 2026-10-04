package legacydb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/legacydb/legacydbtest"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	userA = "a1000000-0000-4000-8000-000000000001"
	userB = "a1000000-0000-4000-8000-000000000002"
	lib   = "b2000000-0000-4000-8000-000000000001"
	item1 = "c3000000-0000-4000-8000-000000000001"
	item2 = "c3000000-0000-4000-8000-000000000002"
)

func build(t *testing.T, spec legacydbtest.Spec) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "上游 jellyfin.db")
	if err := legacydbtest.Build(context.Background(), path, spec); err != nil {
		t.Fatal(err)
	}
	return path
}

func open(t *testing.T, path string) *Source {
	t.Helper()
	s, err := Open(context.Background(), path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSourceReadsEveryCategory(t *testing.T) {
	rating, bitrate := 16, int64(4000)
	path := build(t, legacydbtest.Spec{
		Users: []legacydbtest.User{
			{ID: userA, Name: "Ålice", Password: "$PBKDF2$x", MaxRating: &rating, Bitrate: &bitrate, MaxSessions: 3, Permissions: map[int]bool{0: true, 2: false}, Preferences: map[int]string{5: lib}},
			{ID: userB, Name: "bob"},
		},
		Libraries: []legacydbtest.Library{{ID: lib, Name: "Movies", CollectionType: "movies", Locations: []string{"/m/a", "", "/m/b"}}},
		Items: []legacydbtest.Item{
			{ID: item1, Type: legacydbtest.TypeMovie, Path: "/m/a/電影.mkv"},
			{ID: item2, Type: legacydbtest.TypeEpisode, Virtual: true, Extra: true},
		},
		UserData: []legacydbtest.UserData{
			{UserID: userA, ItemID: item1, Key: "k1", Position: 10, LastPlayed: "2024-05-06 07:08:09.1234567"},
			{UserID: userA, ItemID: item1, Key: "k2", Played: true, PlayCount: 2, LastPlayed: "2025-05-06T07:08:09Z", Favorite: false},
			{UserID: userA, ItemID: item1, Key: "k3", Favorite: true},
			{UserID: userB, ItemID: item2, Position: 3, LastPlayed: "not a date"},
			{UserID: "a1000000-0000-4000-8000-0000000000ff", ItemID: "c3000000-0000-4000-8000-0000000000ff"},
		},
	})
	data, _ := os.ReadFile(path)
	sum := sha256.Sum256(data)
	s := open(t, path)
	info := s.Info()
	if info.SHA256 != hex.EncodeToString(sum[:]) || info.Size != int64(len(data)) || info.File != "上游 jellyfin.db" || !info.Tested || info.Migrations != len(legacydbtest.Migrations) ||
		info.LatestMigration != TestedMigration || info.WALSHA256 != "" {
		t.Fatalf("info %+v", info)
	}
	want := map[string]int64{CountUsers: 2, CountPermissions: 2, CountPreferences: 1, CountBaseItems: 4, CountCollectionFolders: 1, CountLocations: 2, CountUserData: 5}
	for k, v := range want {
		if info.Counts[k] != v {
			t.Fatalf("count %s=%d want %d", k, info.Counts[k], v)
		}
	}
	ctx := context.Background()
	users, cursor, err := s.Users(ctx, "", 1)
	if err != nil || len(users) != 1 || cursor != strings.ToUpper(userA) {
		t.Fatalf("users %v %q %+v", err, cursor, users)
	}
	u := users[0]
	if u.ID != userA || u.Name != "Ålice" || !u.HasPassword || *u.MaxParentalRating != 16 || *u.RemoteBitrate != 4000 || u.MaxActiveSessions != 3 ||
		!u.Permission(0) || u.Permission(2) || len(u.Permissions) != 2 || u.Preference(5)[0] != lib {
		t.Fatalf("user %+v", u)
	}
	users, cursor, err = s.Users(ctx, cursor, 10)
	if err != nil || len(users) != 1 || users[0].ID != userB || users[0].HasPassword || users[0].MaxParentalRating != nil {
		t.Fatalf("second page %v %+v", err, users)
	}
	if users, next, err := s.Users(ctx, cursor, 10); err != nil || len(users) != 0 || next != cursor {
		t.Fatalf("end %v %q", err, next)
	}
	libraries, _, err := s.Libraries(ctx, "", 10)
	if err != nil || len(libraries) != 1 || libraries[0].ID != lib || libraries[0].CollectionType != "movies" || strings.Join(libraries[0].Locations, ",") != "/m/a,/m/b" {
		t.Fatalf("libraries %v %+v", err, libraries)
	}
	var items []domain.LegacyItem
	for c := ""; ; {
		page, next, err := s.Items(ctx, c, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		items, c = append(items, page...), next
	}
	if len(items) != 4 || items[0].ID != domain.LegacyPlaceholderItemID || items[2].ID != item1 || items[2].Path != "/m/a/電影.mkv" || !items[3].Virtual || !items[3].Extra {
		t.Fatalf("items %+v", items)
	}
	// Three rows of one user and item merge into the one played last; the
	// group is never split, even by a batch limit inside it.
	data1, cursor, err := s.UserData(ctx, "", 2)
	if err != nil || len(data1) != 1 {
		t.Fatalf("user data %v %+v", err, data1)
	}
	d := data1[0]
	if d.Rows != 3 || !d.Played || d.PlayCount != 2 || d.Position != 0 || !d.Favorite || d.LastPlayed == nil || !d.LastPlayed.Equal(time.Date(2025, 5, 6, 7, 8, 9, 0, time.UTC)) ||
		d.ItemType != legacydbtest.TypeMovie || !d.UserExists || d.Key() != userA+"|"+item1 {
		t.Fatalf("merged %+v", d)
	}
	rest, cursor, err := s.UserData(ctx, cursor, 10)
	if err != nil || len(rest) != 2 {
		t.Fatalf("rest %v %+v", err, rest)
	}
	if rest[0].UserID != userB || rest[0].LastPlayed != nil || rest[0].ItemType != legacydbtest.TypeEpisode || !rest[0].UserExists {
		t.Fatalf("unparsable date row %+v", rest[0])
	}
	if rest[1].UserExists || rest[1].ItemType != "" {
		t.Fatalf("orphan row %+v", rest[1])
	}
	if more, _, err := s.UserData(ctx, cursor, 10); err != nil || len(more) != 0 {
		t.Fatalf("end %v %+v", err, more)
	}
	// The source is untouched and the copy goes away on Close.
	after, _ := os.ReadFile(path)
	if string(after) != string(data) {
		t.Fatal("source modified")
	}
	dir := s.dir
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) || s.Close() != nil {
		t.Fatal("work directory left behind")
	}
}

func TestSourceCopiesCompanionFiles(t *testing.T) {
	path := build(t, legacydbtest.Spec{Users: []legacydbtest.User{{ID: userA, Name: "a"}}})
	// An empty WAL beside the database is copied but not reported.
	if err := os.WriteFile(path+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if s := open(t, path); s.Info().WALSHA256 != "" || s.Info().Counts[CountUsers] != 1 {
		t.Fatalf("empty wal: %+v", s.Info())
	}
	// A non-empty companion is hashed into the report; garbage in it is
	// ignored by SQLite recovery like it would be upstream.
	if err := os.WriteFile(path+"-wal", []byte("not a wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := open(t, path); len(s.Info().WALSHA256) != 64 {
		t.Fatalf("wal: %+v", s.Info())
	}
}

func TestSourceRefusesUnsupportedFiles(t *testing.T) {
	ctx := context.Background()
	cases := map[string]string{}
	cases["missing"] = filepath.Join(t.TempDir(), "absent.db")
	notSQLite := filepath.Join(t.TempDir(), "text.db")
	if err := os.WriteFile(notSQLite, []byte(strings.Repeat("not a database ", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	cases["not_sqlite"] = notSQLite
	cases["library_db_before_10_11"] = build(t, legacydbtest.Spec{LibraryDB: true})
	cases["schema_before_10_11"] = build(t, legacydbtest.Spec{Migrations: []string{"20200613202153_AddUsers"}})
	cases["missing_column_UserData_PlaybackPositionTicks"] = build(t, legacydbtest.Spec{OmitColumn: "UserData.PlaybackPositionTicks"})
	for want, path := range cases {
		_, err := Open(ctx, path, t.TempDir())
		if !errors.Is(err, domain.ErrLegacySourceUnsupported) || want != "missing" && want != "not_sqlite" && !strings.HasSuffix(err.Error(), want) {
			t.Fatalf("%s: %v", want, err)
		}
	}
	// A newer schema is read and reported as untested.
	newer := build(t, legacydbtest.Spec{Migrations: append(append([]string{}, legacydbtest.Migrations...), "20991231000000_Future")})
	if s := open(t, newer); s.Info().Tested || s.Info().LatestMigration != "20991231000000_Future" {
		t.Fatalf("newer: %+v", s.Info())
	}
	// Optional user columns may be missing.
	old := build(t, legacydbtest.Spec{OmitColumn: "Users.MaxParentalRatingScore", Users: []legacydbtest.User{{ID: userA, Name: "a"}}})
	if users, _, err := open(t, old).Users(ctx, "", 5); err != nil || len(users) != 1 || users[0].MaxParentalRating != nil {
		t.Fatalf("old users %v %+v", err, users)
	}
	// The work directory must exist.
	if _, err := Open(ctx, newer, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing work directory accepted")
	}
}

func TestCopyRefusesAChangingSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := copyHashed(src, filepath.Join(dir, "a")); err != nil {
		t.Fatal(err)
	}
	// The destination exists already.
	if _, _, err := copyHashed(src, filepath.Join(dir, "a")); err == nil {
		t.Fatal("overwrote a copy")
	}
	if _, _, err := copyHashed(filepath.Join(dir, "absent"), filepath.Join(dir, "b")); !errors.Is(err, domain.ErrLegacySourceUnsupported) {
		t.Fatal(err)
	}
}

func TestParseTimeAndMerge(t *testing.T) {
	for in, ok := range map[string]bool{"2025-01-02 03:04:05": true, "2025-01-02T03:04:05.1Z": true, "2025-01-02 03:04:05+08:00": true, "0001-01-01 00:00:00": false, "x": false} {
		if _, got := parseTime(in); got != ok {
			t.Fatalf("%s: %t", in, got)
		}
	}
	if v, _ := parseTime("2025-01-02 03:04:05+08:00"); v.Hour() != 19 {
		t.Fatalf("offset not honoured: %v", v)
	}
	at := time.Unix(100, 0)
	a := domain.LegacyUserData{Rows: 1, Position: 5}
	b := domain.LegacyUserData{Rows: 1, Played: true}
	if m := mergeUserData(a, b); !m.Played || m.Rows != 2 {
		t.Fatalf("played wins a tie: %+v", m)
	}
	c := domain.LegacyUserData{Rows: 1, LastPlayed: &at, Favorite: true}
	if m := mergeUserData(b, c); m.Played || m.LastPlayed == nil || !m.Favorite {
		t.Fatalf("dated row wins: %+v", m)
	}
	if m := mergeUserData(c, b); m.LastPlayed == nil {
		t.Fatalf("dated row kept: %+v", m)
	}
	if sqlitePath(`C:\x\y.db`) != "/C:/x/y.db" && filepath.Separator == '\\' {
		t.Fatal("windows URI path")
	}
}
