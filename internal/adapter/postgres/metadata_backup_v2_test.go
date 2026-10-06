package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// resealMetadataDoc rewrites record lines of a document and recomputes the
// trailer digest, as a producer with different data would have written it.
func resealMetadataDoc(t *testing.T, doc []byte, edit func(kind, line string) string) []byte {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(string(doc), "\n"), "\n")
	var trailer domain.MetadataBackupTrailer
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &trailer); err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	var out strings.Builder
	for i, line := range lines[:len(lines)-1] {
		if i > 0 {
			kind := strings.TrimPrefix(line, `{"kind":"`)
			line = edit(kind[:strings.IndexByte(kind, '"')], line)
		}
		digest.Write([]byte(line + "\n"))
		out.WriteString(line + "\n")
	}
	trailer.SHA256 = hex.EncodeToString(digest.Sum(nil))
	end, err := json.Marshal(trailer)
	if err != nil {
		t.Fatal(err)
	}
	out.Write(end)
	out.WriteByte('\n')
	return []byte(out.String())
}

// TestMetadataBackupClassifiesEveryTablePostgres is the database side of
// the classification guard: the tables a fully migrated schema holds.
func TestMetadataBackupClassifiesEveryTablePostgres(t *testing.T) {
	ctx, s, _ := accountTestStore(t)
	rows, err := s.Pool.Query(ctx, `SELECT c.relname FROM pg_class c WHERE c.relnamespace=current_schema()::regnamespace AND c.relkind IN ('r','p')`)
	if err != nil {
		t.Fatal(err)
	}
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables[name] = true
	}
	rows.Close()
	if rows.Err() != nil || len(tables) < 100 {
		t.Fatalf("tables: %d %v", len(tables), rows.Err())
	}
	checkMetadataBackupClassification(t, tables)
	if source := migrationTables(t); len(source) != len(tables) {
		t.Fatalf("migration replay finds %d tables, the database holds %d", len(source), len(tables))
	}
}

// Collections map by ID, then by their NFO name; a playlist whose ID the
// target gives another owner is a conflict; settings documents that the API
// would refuse are conflicts as well. Conflicts write nothing unless
// skipped, and a skipped settings record keeps the target's value.
func TestMetadataBackupCollectionsPlaylistsSettingsPostgres(t *testing.T) {
	ctx, source, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	_, target, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	seedMetadataBackup(t, ctx, source, 0)
	doc, _ := exportMetadataDoc(t, ctx, source, false)
	const linked, other = "e0000000-0000-4000-8000-000000000001", "e0000000-0000-4000-8000-000000000002"
	bkExec(t, ctx, target, "linked collection", `INSERT INTO collections(id,name,nfo_name) VALUES ($1,'Old name',' saga ')`, linked)
	bkExec(t, ctx, target, "other owner", `INSERT INTO users(id,name) VALUES ($1,'someone')`, other)
	bkExec(t, ctx, target, "taken playlist", `INSERT INTO playlists(id,owner_id,name) VALUES ($1,$2,'Theirs')`, bkPlaylist, other)
	// The document also carries settings the API refuses.
	bad := resealMetadataDoc(t, doc, func(kind, line string) string {
		switch kind {
		case "site_appearance":
			return strings.Replace(line, `"default_theme": "dark"`, `"default_theme": "neon"`, 1)
		case "audit_retention":
			return strings.Replace(line, `"audit_days": 400`, `"audit_days": 3`, 1)
		case "user_preference":
			return strings.Replace(line, `"id": "resume"`, `"id": "<script>"`, 1)
		}
		return line
	})
	if string(bad) == string(doc) || strings.Count(string(bad), "neon")+strings.Count(string(bad), "<script>")+strings.Count(string(bad), `"audit_days": 3`) != 3 {
		t.Fatal("document edits did not apply")
	}
	snapshot := func() int64 {
		return bkCount(t, ctx, target, `SELECT (SELECT count(*) FROM collections)*1000+(SELECT count(*) FROM playlists)*100+(SELECT count(*) FROM user_preferences)*10
 +(SELECT revision FROM site_appearance WHERE id)+(SELECT count(*) FROM audit_logs WHERE event='metadata.imported')`)
	}
	before := snapshot()
	for _, dry := range []bool{false, true} {
		report, err := importMetadataDoc(ctx, target, bad, domain.MetadataImportOptions{DryRun: dry})
		if !errors.Is(err, domain.ErrMetadataBackupConflict) || report.Committed {
			t.Fatalf("dry=%t: err=%v committed=%t", dry, err, report.Committed)
		}
		for _, key := range []string{"playlist:playlist_owner_mismatch", "site_appearance:settings_invalid", "audit_retention:settings_invalid", "user_preference:settings_invalid"} {
			if report.ConflictTotals[key] != 1 {
				t.Fatalf("dry=%t: conflict %s: %v", dry, key, report.ConflictTotals)
			}
		}
		if report.ConflictTotals["collection:collection_nfo_name_taken"] != 0 || report.ConflictTotals["site_plugins:settings_invalid"] != 0 {
			t.Fatalf("dry=%t: spurious conflicts %v", dry, report.ConflictTotals)
		}
	}
	if snapshot() != before {
		t.Fatal("conflicting import wrote rows")
	}
	// Dry run with --skip-conflicts reports what would happen and rolls back.
	dry, err := importMetadataDoc(ctx, target, bad, domain.MetadataImportOptions{DryRun: true, SkipConflicts: true})
	if err != nil || dry.Committed || dry.Kinds["collection"].Updated != 1 || snapshot() != before {
		t.Fatalf("dry run: %v %+v", err, dry.Kinds["collection"])
	}
	report, err := importMetadataDoc(ctx, target, bad, domain.MetadataImportOptions{SkipConflicts: true})
	if err != nil || !report.Committed {
		t.Fatalf("skip-conflicts import: %v %v", err, report.ConflictTotals)
	}
	checkMetadataReportBalanced(t, report)
	if k := report.Kinds["collection"]; k.Inserted != 1 || k.Updated != 1 {
		t.Fatalf("collections: %+v", *k)
	}
	if k := report.Kinds["playlist_item"]; k.Skipped["unresolved_reference"] != 3 {
		t.Fatalf("entries of the conflicting playlist: %+v", *k)
	}
	checks := []struct {
		label string
		sql   string
		want  int64
	}{
		{"collection linked by NFO name takes the exported name", `SELECT count(*) FROM collections WHERE id='` + linked + `' AND name='Saga 系列'`, 1},
		{"no duplicate NFO-linked collection", `SELECT count(*) FROM collections`, 2},
		{"members land on the linked collection", `SELECT count(*) FROM collection_items WHERE collection_id='` + linked + `' AND item_id='` + bkMovie + `'`, 1},
		{"conflicting playlist untouched", `SELECT count(*) FROM playlists WHERE id='` + bkPlaylist + `' AND owner_id='` + other + `' AND name='Theirs'`, 1},
		{"no entries added to it", `SELECT count(*) FROM playlist_items`, 0},
		{"site appearance kept", `SELECT count(*) FROM site_appearance WHERE default_theme='system' AND revision=0`, 1},
		{"valid plugin document applied", `SELECT count(*) FROM site_plugins WHERE settings ? 'acme.clock' AND revision=1`, 1},
		{"retention kept", `SELECT count(*) FROM audit_retention WHERE audit_days=365 AND security_days=365`, 1},
		{"invalid layout skipped, valid preference applied", `SELECT count(*) FROM user_preferences`, 1},
	}
	for _, c := range checks {
		if got := bkCount(t, ctx, target, c.sql); got != c.want {
			t.Errorf("%s: got %d want %d", c.label, got, c.want)
		}
	}
}

// A playlist already in the target is the owner's live copy; preferences
// keep whichever side changed last; site documents are normalized like an
// administrator save.
func TestMetadataBackupKeepsLiveUserDataPostgres(t *testing.T) {
	ctx, source, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	_, target, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	seedMetadataBackup(t, ctx, source, 0)
	doc, _ := exportMetadataDoc(t, ctx, source, false)
	upper := resealMetadataDoc(t, doc, func(kind, line string) string {
		if kind == "site_appearance" {
			return strings.Replace(line, `"fonts.example.com"`, `"FONTS.Example.com"`, 1)
		}
		return line
	})
	if !strings.Contains(string(upper), "FONTS.Example.com") {
		t.Fatal("document edit did not apply")
	}
	if _, err := importMetadataDoc(ctx, target, upper, domain.MetadataImportOptions{}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM site_appearance WHERE font_hosts=ARRAY['fonts.example.com']`); n != 1 {
		t.Fatal("font host not normalized")
	}
	// The owner reorders and removes entries and changes the theme later.
	bkExec(t, ctx, target, "remove entry", `DELETE FROM playlist_items WHERE id=$1`, bkEntry2)
	bkExec(t, ctx, target, "move entry", `UPDATE playlist_items SET position=7 WHERE id=$1`, bkEntry3)
	bkExec(t, ctx, target, "rename", `UPDATE playlists SET name='Mine' WHERE id=$1`, bkPlaylist)
	bkExec(t, ctx, target, "theme", `UPDATE user_preferences SET theme='light',updated_at='2026-12-01T00:00:00Z' WHERE user_id=$1`, bkKid)
	report, err := importMetadataDoc(ctx, target, doc, domain.MetadataImportOptions{})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	checkMetadataReportBalanced(t, report)
	if k := report.Kinds["playlist_item"]; k.Unchanged != 1 || k.Skipped["playlist_kept"] != 2 || k.Inserted != 0 {
		t.Fatalf("entries of a kept playlist: %+v", *k)
	}
	if k := report.Kinds["user_preference"]; k.Updated != 0 || k.Unchanged != 2 {
		t.Fatalf("preferences: %+v", *k)
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM playlist_items`); n != 2 {
		t.Fatalf("kept playlist holds %d entries", n)
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM playlists WHERE name='Mine'`); n != 1 {
		t.Fatal("kept playlist renamed back")
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM user_preferences WHERE user_id=$1 AND theme='light'`, bkKid); n != 1 {
		t.Fatal("newer preference rewound")
	}
	// The normalized font host differs from nothing: the site document is unchanged.
	if k := report.Kinds["site_appearance"]; k.Updated != 0 {
		t.Fatalf("site appearance rewritten: %+v", *k)
	}
}

// Imports cannot grow collections and playlists past the bounds the API
// enforces.
func TestMetadataBackupPlaylistLimitPostgres(t *testing.T) {
	ctx, source, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	_, target, _ := accountTestStoreWithTimeout(t, 2*time.Minute)
	seedMetadataBackup(t, ctx, source, 0)
	doc, _ := exportMetadataDoc(t, ctx, source, false)
	bkExec(t, ctx, target, "kid", `INSERT INTO users(id,name) VALUES ($1,'Kid')`, bkKid)
	bkExec(t, ctx, target, "full", `INSERT INTO playlists(owner_id,name) SELECT $1,'p'||g FROM generate_series(1,$2) g`, bkKid, domain.PlaylistsPerUserMax)
	report, err := importMetadataDoc(ctx, target, doc, domain.MetadataImportOptions{})
	if !errors.Is(err, domain.ErrMetadataBackupConflict) || report.Committed || report.ConflictTotals["playlist:playlist_limit"] != 1 {
		t.Fatalf("limit: %v %v", err, report.ConflictTotals)
	}
	if n := bkCount(t, ctx, target, `SELECT count(*) FROM playlists`); n != int64(domain.PlaylistsPerUserMax) {
		t.Fatalf("playlists after refused import: %d", n)
	}
}
