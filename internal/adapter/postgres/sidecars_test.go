package postgres

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Every extension ParseSidecarName accepts, by kind. The schema check must
// hold exactly these.
var sidecarTestFormats = map[string][]string{
	domain.SidecarKindSubtitle: {"srt", "ass", "ssa", "vtt", "webvtt", "ttml", "dfxp", "smi", "sami", "sub", "idx", "sup"},
	domain.SidecarKindAudio:    {"mka", "aac", "m4a", "ac3", "eac3", "ec3", "dts", "dtshd", "thd", "truehd", "mlp", "flac", "alac", "opus", "ogg", "oga", "mp3", "wav"},
}

type sidecarFixture struct {
	jobFixture
	item, source string
}

func newSidecarFixture(t *testing.T) sidecarFixture {
	t.Helper()
	f := newJobFixture(t)
	item := metadataItem(t, f)
	return sidecarFixture{jobFixture: f, item: item, source: sidecarSource(t, f, item, "Movie/Movie.mkv")}
}

func sidecarSource(t *testing.T, f jobFixture, item, relative string) string {
	t.Helper()
	var id string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska') RETURNING id::text`, item, f.registration.Library.ID, f.registration.RootID, relative).Scan(&id); err != nil {
		t.Fatal("insert media source", err)
	}
	return id
}

func sidecarInput(f jobFixture, name string) domain.SidecarTrackInput {
	track, ok := domain.ParseSidecarName("Movie", name, "")
	if !ok {
		panic("test sidecar name not parsed: " + name)
	}
	in := domain.SidecarTrackInput{Track: track, RootID: f.registration.RootID, RelativePath: "Movie/" + name, Size: 1024,
		ModifiedUnixNano: 1_700_000_000_000_000_000}
	if track.Kind == domain.SidecarKindSubtitle && track.Format != "sup" && track.Format != "sub" {
		in.Charset = "UTF-8"
	}
	return in
}

func sidecarUpsert(t *testing.T, f jobFixture, source string, tracks ...domain.SidecarTrackInput) domain.SidecarTrackChanges {
	t.Helper()
	var changes domain.SidecarTrackChanges
	err := pgx.BeginFunc(f.ctx, f.s.Pool, func(tx pgx.Tx) error {
		var err error
		changes, err = UpsertSidecarTracks(f.ctx, tx, source, tracks)
		return err
	})
	if err != nil {
		t.Fatal("upsert sidecar tracks", err)
	}
	return changes
}

func sidecarCount(t *testing.T, f jobFixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(f.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal("count", err)
	}
	return n
}

func TestSidecarSchemaConstraints(t *testing.T) {
	f := newSidecarFixture(t)
	other, err := f.s.RegisterLibrary(f.ctx, "foreign", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib, root := f.registration.Library.ID, f.registration.RootID
	insert := `INSERT INTO media_sidecar_tracks(source_id,library_id,root_id,relative_path,kind,format,language,languages,title,charset,size,modified_unix_nano,fingerprint,sdh,commentary)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,COALESCE($8::text[],'{}'),$9,$10,$11,0,$12,$13,$14)`
	type row struct {
		name, library, root, path, kind, format string
		language                                *string
		languages                               []string
		rawLanguages                            string
		title, charset                          *string
		size                                    int64
		fingerprint                             []byte
		sdh, commentary                         bool
		want                                    string
	}
	str := func(s string) *string { return &s }
	digest := sha256.Sum256([]byte("x"))
	cases := []row{
		{name: "kind", kind: "video", format: "srt", path: "a.srt", want: "23514"},
		{name: "unknown subtitle format", kind: "subtitle", format: "txt", path: "a.txt", want: "23514"},
		{name: "audio format as subtitle", kind: "subtitle", format: "mka", path: "a.mka", want: "23514"},
		{name: "subtitle format as audio", kind: "audio", format: "srt", path: "a.srt", want: "23514"},
		{name: "format not extension", kind: "subtitle", format: "srt", path: "a.ass", want: "23514"},
		{name: "upper format", kind: "subtitle", format: "SRT", path: "a.SRT", want: "23514"},
		{name: "path parent", kind: "subtitle", format: "srt", path: "../a.srt", want: "23514"},
		{name: "path inner parent", kind: "subtitle", format: "srt", path: "Movie/../../a.srt", want: "23514"},
		{name: "path absolute", kind: "subtitle", format: "srt", path: "/etc/a.srt", want: "23514"},
		{name: "path backslash", kind: "subtitle", format: "srt", path: `Movie\a.srt`, want: "23514"},
		{name: "path colon", kind: "subtitle", format: "srt", path: "C:a.srt", want: "23514"},
		{name: "path control", kind: "subtitle", format: "srt", path: "a\n.srt", want: "23514"},
		{name: "path double slash", kind: "subtitle", format: "srt", path: "Movie//a.srt", want: "23514"},
		{name: "path too long", kind: "subtitle", format: "srt", path: strings.Repeat("a", 1021) + ".srt", want: "23514"},
		{name: "foreign root", root: other.RootID, kind: "subtitle", format: "srt", path: "a.srt", want: "23503"},
		{name: "foreign library", library: other.Library.ID, root: other.RootID, kind: "subtitle", format: "srt", path: "a.srt", want: "23503"},
		{name: "language shape", kind: "subtitle", format: "srt", path: "a.srt", language: str("EN"), languages: []string{"EN"}, want: "23514"},
		{name: "language too long", kind: "subtitle", format: "srt", path: "a.srt", language: str("en-" + strings.Repeat("x", 40)), languages: []string{"en"}, want: "23514"},
		{name: "language without list", kind: "subtitle", format: "srt", path: "a.srt", language: str("en"), want: "23514"},
		{name: "list without language", kind: "subtitle", format: "srt", path: "a.srt", languages: []string{"en"}, want: "23514"},
		{name: "language not first", kind: "subtitle", format: "srt", path: "a.srt", language: str("en"), languages: []string{"ja", "en"}, want: "23514"},
		{name: "bad list element", kind: "subtitle", format: "srt", path: "a.srt", language: str("en"), languages: []string{"en", "x,y"}, want: "23514"},
		{name: "null list element", kind: "subtitle", format: "srt", path: "a.srt", language: str("en"), rawLanguages: "{en,NULL}", want: "23514"},
		{name: "list not one based", kind: "subtitle", format: "srt", path: "a.srt", language: str("en"), rawLanguages: "[0:1]={ja,en}", want: "23514"},
		{name: "list two dimensional", kind: "subtitle", format: "srt", path: "a.srt", language: str("en"), rawLanguages: "{{en},{ja}}", want: "23514"},
		{name: "too many languages", kind: "subtitle", format: "srt", path: "a.srt", language: str("en"), languages: []string{"en", "ja", "ko", "fr", "de", "es", "it", "pt", "ru"}, want: "23514"},
		{name: "empty title", kind: "subtitle", format: "srt", path: "a.srt", title: str(""), want: "23514"},
		{name: "long title", kind: "subtitle", format: "srt", path: "a.srt", title: str(strings.Repeat("t", 256)), want: "23514"},
		{name: "control title", kind: "subtitle", format: "srt", path: "a.srt", title: str("a\tb"), want: "23514"},
		{name: "unknown charset", kind: "subtitle", format: "srt", path: "a.srt", charset: str("latin1"), want: "23514"},
		{name: "audio charset", kind: "audio", format: "mka", path: "a.mka", charset: str("UTF-8"), want: "23514"},
		{name: "negative size", kind: "subtitle", format: "srt", path: "a.srt", size: -1, want: "23514"},
		{name: "short fingerprint", kind: "subtitle", format: "srt", path: "a.srt", fingerprint: []byte{1}, want: "23514"},
		{name: "valid full", kind: "subtitle", format: "srt", path: "Movie/Subs/Movie.zh-Hant-TW.SRT", language: str("zh-Hant-TW"),
			languages: []string{"zh-Hant-TW", "en", "es-419"}, title: str("Director's cut"), charset: str("Big5"), fingerprint: digest[:], sdh: true, commentary: true},
		{name: "valid bare", kind: "audio", format: "flac", path: "Movie/Audio/Movie.flac", sdh: true},
	}
	for kind, formats := range sidecarTestFormats {
		for _, format := range formats {
			if _, ok := domain.ParseSidecarName("Movie", "Movie."+format, ""); !ok {
				t.Fatal("parser does not accept test format", format)
			}
			cases = append(cases, row{name: "valid " + format, kind: kind, format: format, path: "Movie/Movie." + format})
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			library, r := lib, root
			if c.library != "" {
				library = c.library
			}
			if c.root != "" {
				r = c.root
			}
			var languages any = c.languages
			if c.rawLanguages != "" {
				languages = c.rawLanguages
			}
			_, err := f.s.Pool.Exec(f.ctx, insert, f.source, library, r, c.path, c.kind, c.format, c.language, languages, c.title, c.charset, c.size, c.fingerprint, c.sdh, c.commentary)
			if got := itemImageSQLState(err); got != c.want || c.want == "" && err != nil {
				t.Fatalf("constraint state=%q want %q err=%v", got, c.want, err)
			}
		})
	}
	// One row per (source, root, path); the same file may belong to another
	// source of the same library.
	if _, err := f.s.Pool.Exec(f.ctx, insert, f.source, lib, root, "Movie/Movie.srt", "subtitle", "srt", nil, nil, nil, nil, 0, nil, false, false); itemImageSQLState(err) != "23505" {
		t.Fatal("duplicate sidecar file accepted", err)
	}
	second := sidecarSource(t, f.jobFixture, f.item, "Movie/Movie.Part2.mkv")
	if _, err := f.s.Pool.Exec(f.ctx, insert, second, lib, root, "Movie/Movie.srt", "subtitle", "srt", nil, nil, nil, nil, 0, nil, false, false); err != nil {
		t.Fatal("same file under another source rejected", err)
	}
	// Source, item and root deletion cascade.
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM media_sources WHERE id=$1::uuid`, second); err != nil {
		t.Fatal(err)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE source_id=$1::uuid`, second); n != 0 {
		t.Fatal("source delete kept sidecars", n)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE source_id=$1::uuid`, f.source); n == 0 {
		t.Fatal("cascade removed another source's sidecars")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM items WHERE id=$1::uuid`, f.item); err != nil {
		t.Fatal(err)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks`); n != 0 {
		t.Fatal("item delete kept sidecars", n)
	}
	item := metadataItem(t, f.jobFixture)
	third := sidecarSource(t, f.jobFixture, item, "Other/Other.mkv")
	sidecarUpsert(t, f.jobFixture, third, sidecarInput(f.jobFixture, "Movie.en.srt"))
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM library_roots WHERE id=$1::uuid`, f.registration.RootID); err != nil {
		t.Fatal(err)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks`); n != 0 {
		t.Fatal("root delete kept sidecars", n)
	}
}

func TestSidecarUpsertReplaceAndIdempotence(t *testing.T) {
	f := newSidecarFixture(t)
	en, ja := sidecarInput(f.jobFixture, "Movie.en.forced.srt"), sidecarInput(f.jobFixture, "Movie.chs&eng.ass")
	audio := sidecarInput(f.jobFixture, "Movie.ja.commentary.default.mka")
	if c := sidecarUpsert(t, f.jobFixture, f.source, en, ja, audio); c != (domain.SidecarTrackChanges{Added: 3}) {
		t.Fatal("initial changes", c)
	}
	list, err := f.s.ListSidecarTracks(f.ctx, f.a, f.source)
	if err != nil || len(list) != 3 {
		t.Fatal("list after insert", len(list), err)
	}
	if list[0].Track.Kind != domain.SidecarKindSubtitle || list[2].Track.Kind != domain.SidecarKindAudio {
		t.Fatal("subtitles are not listed before audio")
	}
	byPath := map[string]domain.SidecarTrackRecord{}
	for _, row := range list {
		byPath[row.RelativePath] = row
	}
	got := byPath["Movie/Movie.chs&eng.ass"]
	if got.Track.Language != "zh-Hans" || len(got.Track.Languages) != 2 || got.Track.Languages[1] != "en" || got.Charset != "UTF-8" ||
		got.SourceID != f.source || got.LibraryID != f.registration.Library.ID || got.Size != 1024 {
		t.Fatal("stored track does not round-trip", got.Track, got.Charset)
	}
	mka := byPath["Movie/Movie.ja.commentary.default.mka"]
	if !mka.Track.Commentary || !mka.Track.Default || mka.Track.Forced || mka.Charset != "" || mka.Track.Languages[0] != "ja" {
		t.Fatal("audio flags", mka.Track)
	}
	if !byPath["Movie/Movie.en.forced.srt"].Track.Forced {
		t.Fatal("forced flag lost")
	}
	if text := fmt.Sprintf("%v %#v", got, got); strings.Contains(text, "Movie") {
		t.Fatal("sidecar diagnostics expose paths")
	}
	// Same set again: nothing written, nothing audited.
	if c := sidecarUpsert(t, f.jobFixture, f.source, audio, en, ja); c.Total() != 0 {
		t.Fatal("idempotent replacement changed rows", c)
	}
	again, _ := f.s.ListSidecarTracks(f.ctx, f.a, f.source)
	for i := range again {
		if again[i].ID != list[i].ID || !again[i].UpdatedAt.Equal(list[i].UpdatedAt) {
			t.Fatal("idempotent replacement rewrote a row")
		}
	}
	if n := itemImageAuditCount(t, f.jobFixture, "media.sidecars_changed", f.source); n != 1 {
		t.Fatal("audit rows after idempotent replacement", n)
	}
	// Change one, drop one, add one.
	ja.Size, ja.Charset = 2048, "GB18030"
	digest := sha256.Sum256([]byte("ja"))
	ja.Fingerprint = digest[:]
	vtt := sidecarInput(f.jobFixture, "Movie.ko.vtt")
	if c := sidecarUpsert(t, f.jobFixture, f.source, ja, audio, vtt); c != (domain.SidecarTrackChanges{Added: 1, Updated: 1, Removed: 1}) {
		t.Fatal("mixed changes", c)
	}
	after, _ := f.s.ListSidecarTracks(f.ctx, f.a, f.source)
	paths := map[string]domain.SidecarTrackRecord{}
	for _, row := range after {
		paths[row.RelativePath] = row
	}
	changed := paths["Movie/Movie.chs&eng.ass"]
	if len(after) != 3 || changed.ID != got.ID || changed.Size != 2048 || changed.Charset != "GB18030" || len(changed.Fingerprint) != 32 ||
		!changed.CreatedAt.Equal(got.CreatedAt) || changed.UpdatedAt.Before(got.UpdatedAt) {
		t.Fatal("update did not keep identity or apply values")
	}
	if _, ok := paths["Movie/Movie.en.forced.srt"]; ok {
		t.Fatal("dropped file kept")
	}
	var state string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT after_state::text FROM audit_logs WHERE event='media.sidecars_changed' AND target_id=$1::uuid ORDER BY id DESC LIMIT 1`, f.source).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state, `"added": 1`) || !strings.Contains(state, `"removed": 1`) || !strings.Contains(state, `"audio": 1`) || strings.Contains(state, "Movie") {
		t.Fatal("audit state", state)
	}
	// An empty set clears the source.
	if c := sidecarUpsert(t, f.jobFixture, f.source); c != (domain.SidecarTrackChanges{Removed: 3}) {
		t.Fatal("clear", c)
	}
	if list, err := f.s.ListSidecarTracks(f.ctx, f.a, f.source); err != nil || list == nil || len(list) != 0 {
		t.Fatal("visible source without sidecars is not an empty list", err)
	}
	if c := sidecarUpsert(t, f.jobFixture, f.source); c.Total() != 0 {
		t.Fatal("clearing an empty set changed rows", c)
	}
}

func TestSidecarUpsertRejectsWithoutPoisoningTransaction(t *testing.T) {
	f := newSidecarFixture(t)
	good := sidecarInput(f.jobFixture, "Movie.en.srt")
	sidecarUpsert(t, f.jobFixture, f.source, good)
	other, err := f.s.RegisterLibrary(f.ctx, "foreign", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	foreign := sidecarInput(f.jobFixture, "Movie.ja.srt")
	foreign.RootID = other.RootID
	badLanguage := sidecarInput(f.jobFixture, "Movie.fr.srt")
	badLanguage.Track.Languages = []string{"de"}
	audioCharset := sidecarInput(f.jobFixture, "Movie.flac")
	audioCharset.Charset = "UTF-8"
	wrongExt := sidecarInput(f.jobFixture, "Movie.de.srt")
	wrongExt.RelativePath = "Movie/Movie.de.ass"
	escape := sidecarInput(f.jobFixture, "Movie.it.srt")
	escape.RelativePath = "../Movie.it.srt"
	tooMany := make([]domain.SidecarTrackInput, domain.SidecarTracksPerSource+1)
	for i := range tooMany {
		tooMany[i] = sidecarInput(f.jobFixture, fmt.Sprintf("Movie.t%d.srt", i))
	}
	for name, tracks := range map[string][]domain.SidecarTrackInput{
		"foreign root":    {good, foreign},
		"bad languages":   {badLanguage},
		"audio charset":   {audioCharset},
		"wrong extension": {wrongExt},
		"path escape":     {escape},
		"duplicate":       {good, good},
		"too many":        tooMany,
	} {
		err := pgx.BeginFunc(f.ctx, f.s.Pool, func(tx pgx.Tx) error {
			if _, err := UpsertSidecarTracks(f.ctx, tx, f.source, tracks); err != domain.ErrInvalid {
				t.Errorf("%s: %v", name, err)
			}
			// The caller's transaction stays usable after a rejected set.
			var one int
			return tx.QueryRow(f.ctx, `SELECT 1`).Scan(&one)
		})
		if err != nil {
			t.Fatal(name, "poisoned caller transaction", err)
		}
	}
	if list, err := f.s.ListSidecarTracks(f.ctx, f.a, f.source); err != nil || len(list) != 1 || list[0].RelativePath != "Movie/Movie.en.srt" {
		t.Fatal("rejected sets changed the stored set", len(list), err)
	}
	err = pgx.BeginFunc(f.ctx, f.s.Pool, func(tx pgx.Tx) error {
		if _, err := UpsertSidecarTracks(f.ctx, tx, "10000000-0000-4000-8000-000000000099", []domain.SidecarTrackInput{good}); err != domain.ErrNotFound {
			t.Error("missing source", err)
		}
		if _, err := UpsertSidecarTracks(f.ctx, tx, "invalid", nil); err != domain.ErrInvalid {
			t.Error("invalid source id", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Writes roll back with the caller.
	err = pgx.BeginFunc(f.ctx, f.s.Pool, func(tx pgx.Tx) error {
		if _, err := UpsertSidecarTracks(f.ctx, tx, f.source, nil); err != nil {
			return err
		}
		return context.Canceled
	})
	if err != context.Canceled {
		t.Fatal(err)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks`); n != 1 {
		t.Fatal("caller rollback did not undo replacement", n)
	}
}

func TestSidecarAuthorizationFilter(t *testing.T) {
	f := newSidecarFixture(t)
	sidecarUpsert(t, f.jobFixture, f.source, sidecarInput(f.jobFixture, "Movie.en.srt"))
	list, err := f.s.ListSidecarTracks(f.ctx, f.a, f.source)
	if err != nil || len(list) != 1 {
		t.Fatal("admin list", err)
	}
	track := list[0].ID
	location, err := f.s.ResolveSidecarTrack(f.ctx, f.a, track)
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err != nil || location.RootPath != root || location.RelativePath != "Movie/Movie.en.srt" || location.Format != "srt" ||
		location.Kind != domain.SidecarKindSubtitle || location.SourceID != f.source || location.Charset != "UTF-8" || location.Size != 1024 {
		t.Fatal("admin resolve", err)
	}
	if text := fmt.Sprintf("%v %#v", location, location); strings.Contains(text, root) || strings.Contains(text, "Movie") {
		t.Fatal("location diagnostics expose paths")
	}
	for _, kind := range []access.ClientKind{access.ClientWeb, access.ClientNative} {
		reader := imageRepositoryActor(t, f.jobFixture, "sidecar-"+string(kind), kind)
		denied := func(stage string) {
			t.Helper()
			if v, err := f.s.ListSidecarTracks(f.ctx, reader, f.source); v != nil || err != domain.ErrNotFound {
				t.Fatalf("%s %s: list %v", kind, stage, err)
			}
			if v, err := f.s.ResolveSidecarTrack(f.ctx, reader, track); v.ID != "" || err != domain.ErrNotFound {
				t.Fatalf("%s %s: resolve %v", kind, stage, err)
			}
		}
		denied("without grant")
		imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, reader.UserID, f.registration.Library.ID)
		if v, err := f.s.ListSidecarTracks(f.ctx, reader, f.source); err != nil || len(v) != 1 {
			t.Fatal(kind, "granted list", err)
		}
		if v, err := f.s.ResolveSidecarTrack(f.ctx, reader, track); err != nil || v.RelativePath != "Movie/Movie.en.srt" {
			t.Fatal(kind, "granted resolve", err)
		}
		imageRepositoryExec(t, f.jobFixture, `DELETE FROM library_acl WHERE user_id=$1::uuid`, reader.UserID)
		denied("after grant removal")
		imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, reader.UserID, f.registration.Library.ID)
		imageRepositoryExec(t, f.jobFixture, `UPDATE users SET disabled=true WHERE id=$1::uuid`, reader.UserID)
		denied("disabled user")
		imageRepositoryExec(t, f.jobFixture, `UPDATE users SET disabled=false WHERE id=$1::uuid`, reader.UserID)
		imageRepositoryExec(t, f.jobFixture, `UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, reader.SessionID)
		denied("expired session")
		imageRepositoryExec(t, f.jobFixture, `UPDATE sessions SET expires_at=clock_timestamp()+interval '1 hour',revoked_at=clock_timestamp() WHERE id=$1::uuid`, reader.SessionID)
		denied("revoked session")
	}
	// A session of another user does not borrow a granted user's access.
	granted := imageRepositoryActor(t, f.jobFixture, "sidecar-granted", access.ClientNative)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, granted.UserID, f.registration.Library.ID)
	stranger := imageRepositoryActor(t, f.jobFixture, "sidecar-stranger", access.ClientNative)
	mixed := domain.Actor{UserID: granted.UserID, SessionID: stranger.SessionID}
	if _, err := f.s.ResolveSidecarTrack(f.ctx, mixed, track); err != domain.ErrNotFound {
		t.Fatal("foreign session resolved", err)
	}
	// Missing and malformed identifiers look the same as invisible ones.
	for _, id := range []string{"invalid", "10000000-0000-4000-8000-000000000099"} {
		if _, err := f.s.ListSidecarTracks(f.ctx, f.a, id); err != domain.ErrNotFound {
			t.Fatal("missing source listed", err)
		}
		if _, err := f.s.ResolveSidecarTrack(f.ctx, f.a, id); err != domain.ErrNotFound {
			t.Fatal("missing track resolved", err)
		}
	}
	if _, err := f.s.ResolveSidecarTrack(f.ctx, f.a, f.source); err != domain.ErrNotFound {
		t.Fatal("source id resolved as track", err)
	}
	if _, err := f.s.ListSidecarTracks(nil, f.a, f.source); err != domain.ErrInvalid {
		t.Fatal("nil context", err)
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := f.s.ResolveSidecarTrack(cancelled, f.a, track); err != context.Canceled {
		t.Fatal("resolve ignored cancellation", err)
	}
}

func TestSidecarMigrationDownRefusesRetainedTracks(t *testing.T) {
	f := newSidecarFixture(t)
	sidecarUpsert(t, f.jobFixture, f.source, sidecarInput(f.jobFixture, "Movie.en.srt"))
	want := downgradeAboveMigration(t, f.jobFixture, "media_sidecar_tracks")
	nfoMigrationDenied(t, f.jobFixture, fmt.Sprintf("%06d_media_sidecar_tracks.down.sql", want))
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("retained sidecar tracks downgraded")
	}
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != want-1 || !dirty {
		t.Fatal("refused downgrade lost dirty status", version, dirty, err)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks`); n != 1 {
		t.Fatal("refused downgrade removed data", n)
	}
}

func TestSidecarMigrationRoundTrip(t *testing.T) {
	f := newSidecarFixture(t)
	sidecarUpsert(t, f.jobFixture, f.source, sidecarInput(f.jobFixture, "Movie.en.srt"))
	sidecarUpsert(t, f.jobFixture, f.source)
	jobMetricMigration(t, f.jobFixture, "down", downgradeAboveMigration(t, f.jobFixture, "media_sidecar_tracks")-1)
	var removed bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('media_sidecar_tracks') IS NULL AND NOT EXISTS(
 SELECT 1 FROM pg_constraint WHERE conname='media_sources_id_library_key')`).Scan(&removed); err != nil || !removed {
		t.Fatal("downgrade left sidecar objects", err)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event='media.sidecars_changed'`); n != 2 {
		t.Fatal("downgrade removed audit history", n)
	}
	jobMetricMigration(t, f.jobFixture, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("store not ready after round trip", err)
	}
	if c := sidecarUpsert(t, f.jobFixture, f.source, sidecarInput(f.jobFixture, "Movie.en.srt")); c.Added != 1 {
		t.Fatal("upsert after round trip", c)
	}
}

func TestSidecarUpsertSerializesPerSource(t *testing.T) {
	f := newSidecarFixture(t)
	first, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(f.ctx)
	if c, err := UpsertSidecarTracks(f.ctx, first, f.source, []domain.SidecarTrackInput{sidecarInput(f.jobFixture, "Movie.en.srt")}); err != nil || c.Added != 1 {
		t.Fatal("first replacement", c, err)
	}
	type result struct {
		changes domain.SidecarTrackChanges
		err     error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		r.err = pgx.BeginFunc(f.ctx, f.s.Pool, func(tx pgx.Tx) error {
			var err error
			r.changes, err = UpsertSidecarTracks(f.ctx, tx, f.source, []domain.SidecarTrackInput{sidecarInput(f.jobFixture, "Movie.en.srt"), sidecarInput(f.jobFixture, "Movie.ja.srt")})
			return err
		})
		done <- r
	}()
	// The second replacement waits on the source lock until the first commits,
	// then sees its row instead of colliding with it.
	select {
	case r := <-done:
		t.Fatal("second replacement did not wait", r.changes, r.err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := first.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || r.changes != (domain.SidecarTrackChanges{Added: 1}) {
		t.Fatal("serialized replacement", r.changes, r.err)
	}
	if n := sidecarCount(t, f.jobFixture, `SELECT count(*) FROM media_sidecar_tracks WHERE source_id=$1::uuid`, f.source); n != 2 {
		t.Fatal("rows after serialized replacements", n)
	}
}
