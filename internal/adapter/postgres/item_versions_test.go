package postgres

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// versionCount runs a count query against the fixture.
func versionCount(t *testing.T, f jobFixture, query string, args ...any) int {
	t.Helper()
	return syncCount(t, f, query, args...)
}

func versionItemOf(t *testing.T, f jobFixture, source string) string {
	t.Helper()
	var item string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT item_id::text FROM media_sources WHERE id=$1::uuid`, source).Scan(&item); err != nil {
		t.Fatal("source item", err)
	}
	return item
}

func versionExec(t *testing.T, f jobFixture, query string, args ...any) {
	t.Helper()
	if _, err := f.s.Pool.Exec(f.ctx, query, args...); err != nil {
		t.Fatal(query, err)
	}
}

// TestItemVersionSnapshotCoversCascades keeps the merge snapshot complete: a
// new table that cascades from items must be kept for undo, moved before the
// item goes, or knowingly dropped.
func TestItemVersionSnapshotCoversCascades(t *testing.T) {
	f := newJobFixture(t)
	rows, err := f.s.Pool.Query(f.ctx, `SELECT c.conrelid::regclass::text,c.confdeltype::text FROM pg_constraint c
 WHERE c.contype='f' AND c.confrelid IN ('items'::regclass,'item_metadata_state'::regclass) AND c.connamespace=current_schema()::regnamespace`)
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, table := range versionSnapshotTables {
		known[table.table] = true
	}
	for _, table := range append(slices.Clone(versionSnapshotMoved), versionSnapshotDropped...) {
		known[table] = true
	}
	// Probe rows are checked by versionBusy or unlinked; a parent link is
	// refused by checkMergeable (items with children never merge).
	restrict := map[string]bool{"probe_requests": true, "probe_job_state": true, "probe_cache": true, "item_parent_links": true}
	seen := 0
	for rows.Next() {
		var table, action string
		if err = rows.Scan(&table, &action); err != nil {
			t.Fatal(err)
		}
		seen++
		switch action {
		case "c":
			if !known[table] {
				t.Errorf("%s cascades from items but the merge snapshot does not account for it", table)
			}
		case "r", "a":
			if !restrict[table] {
				t.Errorf("%s restricts item deletion; versionBusy must cover it", table)
			}
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil || seen < 15 {
		t.Fatal("foreign keys not read", seen, err)
	}
}

type versionFixture struct {
	jobFixture
	viewer      contentAccessUser
	item        string
	low, high   string
	lowPath     string
	highPath    string
	otherItem   string
	otherSource string
}

// newVersionFixture holds one movie with two versions and a second movie of
// the same title elsewhere, with a viewer who watched both.
func newVersionFixture(t *testing.T) versionFixture {
	t.Helper()
	f := versionFixture{jobFixture: newJobFixture(t)}
	lib := f.registration.Library.ID
	f.viewer = contentAccessPrincipal(t, f.jobFixture, "version-viewer", false)
	versionExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, f.viewer.actor.UserID, lib)
	f.item = detailsItem(t, f.jobFixture, lib, "Heat", "Movie")
	f.lowPath, f.highPath = "Heat (1995)/Heat (1995) 1080p.mkv", "Heat (1995)/Heat (1995) 2160p.mkv"
	f.low = sidecarSource(t, f.jobFixture, f.item, f.lowPath)
	f.high = sidecarSource(t, f.jobFixture, f.item, f.highPath)
	for _, s := range []struct{ id, path string }{{f.low, f.lowPath}, {f.high, f.highPath}} {
		versionExec(t, f.jobFixture, `INSERT INTO catalog_scan_sources(source_id,library_id,item_id,root_id,relative_path,size,modified_unix_nano,parser_version) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,1,1,'test')`,
			s.id, lib, f.item, f.registration.RootID, s.path)
	}
	f.otherItem = detailsItem(t, f.jobFixture, lib, "Heat (copy)", "Movie")
	f.otherSource = sidecarSource(t, f.jobFixture, f.otherItem, "Elsewhere/Heat (1995) REMUX.mkv")
	viewer := f.viewer.actor.UserID
	// The viewer half-watched the first item on its 2160p version, and
	// finished the second item later.
	versionExec(t, f.jobFixture, `INSERT INTO user_item_data(user_id,item_id,resume_ticks,played,play_count,last_played_at,last_source_id,updated_at) VALUES
 ($1::uuid,$2::uuid,600000000,false,0,now()-interval '2 days',$3::uuid,now()-interval '2 days'),
 ($1::uuid,$4::uuid,0,true,1,now()-interval '1 day',$5::uuid,now()-interval '1 day')`, viewer, f.item, f.high, f.otherItem, f.otherSource)
	versionExec(t, f.jobFixture, `INSERT INTO playback_sessions(user_id,play_key,item_id,library_id,source_id,state,started_at,last_report_at,ended_at) VALUES
 ($1::uuid,'k1',$2::uuid,$3::uuid,$4::uuid,'stopped',now()-interval '2 days',now()-interval '2 days',now()-interval '2 days'),
 ($1::uuid,'k2',$5::uuid,$3::uuid,$6::uuid,'stopped',now()-interval '1 day',now()-interval '1 day',now()-interval '1 day')`, viewer, f.item, lib, f.high, f.otherItem, f.otherSource)
	versionExec(t, f.jobFixture, `INSERT INTO watch_stats_daily(user_id,day,item_id,library_id,effective_ms,sessions,views,first_plays,completions) VALUES
 ($1::uuid,DATE '2026-10-01',$2::uuid,$3::uuid,1000,1,1,1,0),($1::uuid,DATE '2026-10-01',$4::uuid,$3::uuid,5000,1,1,1,1)`, viewer, f.item, lib, f.otherItem)
	return f
}

func TestItemVersionSplitMergePrimaryAndUndoPostgres(t *testing.T) {
	f := newVersionFixture(t)
	viewer := f.viewer.actor.UserID

	// Split the 2160p version off, excluding it from the item.
	split, err := f.s.SplitVersion(f.ctx, f.a, f.item, domain.SplitVersionInput{SourceID: f.high, Exclude: true, Title: "Heat 4K"})
	if err != nil || split.Kind != domain.VersionOpSplit || split.ItemID != f.item || split.OtherItemID == "" || !split.Undoable {
		t.Fatalf("split: %+v %v", split, err)
	}
	created := split.OtherItemID
	if versionItemOf(t, f.jobFixture, f.high) != created || versionItemOf(t, f.jobFixture, f.low) != f.item {
		t.Fatal("split did not move exactly the chosen version")
	}
	if versionCount(t, f.jobFixture, `SELECT count(*) FROM items i JOIN item_metadata_fields m ON m.item_id=i.id AND m.field='title' AND m.source='manual' WHERE i.id=$1::uuid AND i.title='Heat 4K' AND i.kind='Movie'`, created) != 1 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM catalog_scan_sources WHERE source_id=$1::uuid AND item_id=$2::uuid AND manual`, f.high, created) != 1 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM item_version_exclusions WHERE item_id=$1::uuid AND relative_path=$2 AND operation_id=$3::uuid`, f.item, f.highPath, split.ID) != 1 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event='item.version_split' AND target_id=$1::uuid`, f.item) != 1 {
		t.Fatal("split state or audit missing")
	}
	// History stays with the original item; the version check accepts it.
	if versionCount(t, f.jobFixture, `SELECT count(*) FROM playback_sessions WHERE item_id=$1::uuid AND source_id=$2::uuid`, f.item, f.high) != 1 {
		t.Fatal("split moved playback history")
	}
	scope := domain.ConsistencyScope{Library: domain.ConsistencyLibrary{ID: f.registration.Library.ID}}
	for _, phase := range []string{"userdata:", "sessions:"} {
		page, err := f.s.ConsistencyPage(f.ctx, domain.ConsistencyVersionCount, scope, phase)
		if err != nil || len(page.Candidates) != 0 || page.Examined == 0 {
			t.Fatalf("%s check after split: %+v %v", phase, page.Candidates, err)
		}
	}
	// The only version cannot be split.
	if _, err = f.s.SplitVersion(f.ctx, f.a, f.item, domain.SplitVersionInput{SourceID: f.low}); !errors.Is(err, domain.ErrVersionMergeIncompatible) {
		t.Fatal("only version split", err)
	}
	// A version of another item is answered like a missing one.
	if _, err = f.s.SplitVersion(f.ctx, f.a, f.item, domain.SplitVersionInput{SourceID: f.otherSource}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign version split", err)
	}

	// Main version.
	primary, err := f.s.SetPrimaryVersion(f.ctx, f.a, f.otherItem, f.otherSource)
	if err != nil || primary.Kind != domain.VersionOpPrimary {
		t.Fatal("primary", err)
	}
	if _, err = f.s.SetPrimaryVersion(f.ctx, f.a, f.otherItem, f.otherSource); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unchanged primary", err)
	}
	if _, err = f.s.SetPrimaryVersion(f.ctx, f.a, f.otherItem, f.low); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign primary", err)
	}

	// Merge the copy into the split-off item. The viewer's data folds in,
	// the copy's hide rule hides the result, statistics add up.
	versionExec(t, f.jobFixture, `INSERT INTO user_item_data(user_id,item_id,resume_ticks,played,play_count,last_played_at,updated_at) VALUES($1::uuid,$2::uuid,300,false,2,now()-interval '3 days',now()-interval '3 days')`, viewer, created)
	versionExec(t, f.jobFixture, `INSERT INTO user_item_access_rules(user_id,item_id,effect) VALUES($1::uuid,$2::uuid,'hide')`, viewer, f.otherItem)
	versionExec(t, f.jobFixture, `INSERT INTO watch_stats_daily(user_id,day,item_id,library_id,effective_ms,sessions,views,first_plays,completions) VALUES($1::uuid,DATE '2026-10-01',$2::uuid,$3::uuid,700,1,1,1,0)`, viewer, created, f.registration.Library.ID)
	versionExec(t, f.jobFixture, `INSERT INTO user_track_preferences(user_id,item_id,audio_language) VALUES($1::uuid,$2::uuid,'ja')`, viewer, f.otherItem)
	versionExec(t, f.jobFixture, `INSERT INTO user_track_preferences(user_id,item_id,source_id,subtitle_mode) VALUES($1::uuid,$2::uuid,$3::uuid,'off')`, viewer, f.otherItem, f.otherSource)
	merge, err := f.s.MergeItems(f.ctx, f.a, created, f.otherItem)
	if err != nil || merge.Kind != domain.VersionOpMerge || merge.OtherItemID != f.otherItem || !slices.Equal(merge.SourceIDs, []string{f.otherSource}) {
		t.Fatalf("merge: %+v %v", merge, err)
	}
	if versionCount(t, f.jobFixture, `SELECT count(*) FROM items WHERE id=$1::uuid`, f.otherItem) != 0 || versionItemOf(t, f.jobFixture, f.otherSource) != created {
		t.Fatal("merge did not absorb the item")
	}
	var played bool
	var plays int
	var resume int64
	var last string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT played,play_count,resume_ticks,last_source_id::text FROM user_item_data WHERE user_id=$1::uuid AND item_id=$2::uuid`, viewer, created).Scan(&played, &plays, &resume, &last); err != nil {
		t.Fatal(err)
	}
	if !played || plays != 3 || resume != 0 || last != f.otherSource {
		t.Fatalf("merged user data: played=%t plays=%d resume=%d last=%s", played, plays, resume, last)
	}
	if versionCount(t, f.jobFixture, `SELECT count(*) FROM user_item_access_rules WHERE user_id=$1::uuid AND item_id=$2::uuid AND effect='hide'`, viewer, created) != 1 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM playback_sessions WHERE item_id=$1::uuid`, created) != 1 ||
		versionCount(t, f.jobFixture, `SELECT effective_ms FROM watch_stats_daily WHERE user_id=$1::uuid AND item_id=$2::uuid`, viewer, created) != 5700 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM user_track_preferences WHERE item_id=$1::uuid AND source_id IS NULL AND audio_language='ja'`, created) != 1 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM user_track_preferences WHERE item_id=$1::uuid AND source_id=$2::uuid`, created, f.otherSource) != 1 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event='item.versions_merged' AND target_id=$1::uuid`, created) != 1 {
		t.Fatal("merge policy not applied")
	}
	// The hide rule now hides the merged item from the viewer.
	if _, err = f.s.ListPlaybackSources(f.ctx, f.viewer.actor, created); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("hide rule lost in merge", err)
	}

	// Undo is newest first: the split waits for the merge.
	if _, err = f.s.UndoVersionOperation(f.ctx, f.a, split.ID); !errors.Is(err, domain.ErrVersionUndoUnavailable) {
		t.Fatal("split undone before the later merge", err)
	}
	overview, err := f.s.VersionOverview(f.ctx, f.a, created)
	if err != nil || len(overview.Operations) != 2 || overview.Operations[0].ID != merge.ID || !overview.Operations[0].Undoable ||
		overview.Operations[1].Undoable || overview.Operations[1].UndoBlocked != domain.VersionUndoBlockedLater {
		t.Fatalf("overview: %+v %v", overview, err)
	}
	undone, err := f.s.UndoVersionOperation(f.ctx, f.a, merge.ID)
	if err != nil || undone.UndoneAt == nil || undone.Undoable {
		t.Fatalf("undo merge: %+v %v", undone, err)
	}
	var title string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT title FROM items WHERE id=$1::uuid`, f.otherItem).Scan(&title); err != nil || title != "Heat (copy)" || versionItemOf(t, f.jobFixture, f.otherSource) != f.otherItem {
		t.Fatal("absorbed item not restored", title, err)
	}
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT played,play_count,resume_ticks FROM user_item_data WHERE user_id=$1::uuid AND item_id=$2::uuid`, viewer, created).Scan(&played, &plays, &resume); err != nil || played || plays != 2 || resume != 300 {
		t.Fatalf("target user data not restored: %t %d %d %v", played, plays, resume, err)
	}
	if versionCount(t, f.jobFixture, `SELECT count(*) FROM user_item_data WHERE user_id=$1::uuid AND item_id=$2::uuid AND played AND play_count=1`, viewer, f.otherItem) != 1 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM user_item_access_rules WHERE item_id=$1::uuid`, created) != 0 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM user_item_access_rules WHERE item_id=$1::uuid AND effect='hide'`, f.otherItem) != 1 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM playback_sessions WHERE item_id=$1::uuid`, f.otherItem) != 1 ||
		versionCount(t, f.jobFixture, `SELECT effective_ms FROM watch_stats_daily WHERE user_id=$1::uuid AND item_id=$2::uuid`, viewer, created) != 700 ||
		versionCount(t, f.jobFixture, `SELECT effective_ms FROM watch_stats_daily WHERE user_id=$1::uuid AND item_id=$2::uuid`, viewer, f.otherItem) != 5000 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM user_track_preferences WHERE item_id=$1::uuid`, created) != 0 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM user_track_preferences WHERE item_id=$1::uuid`, f.otherItem) != 2 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM item_primary_versions WHERE item_id=$1::uuid AND source_id=$2::uuid`, f.otherItem, f.otherSource) != 1 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM catalog_scan_item_aliases`) != 0 {
		t.Fatal("merge undo incomplete")
	}
	if _, err = f.s.UndoVersionOperation(f.ctx, f.a, merge.ID); !errors.Is(err, domain.ErrVersionUndoUnavailable) {
		t.Fatal("merge undone twice", err)
	}

	// Now the split can be undone: the version comes back, the new item and
	// the exclusion go, and its user data folds into the original.
	if _, err = f.s.UndoVersionOperation(f.ctx, f.a, split.ID); err != nil {
		t.Fatal("undo split", err)
	}
	if versionItemOf(t, f.jobFixture, f.high) != f.item || versionCount(t, f.jobFixture, `SELECT count(*) FROM items WHERE id=$1::uuid`, created) != 0 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM item_version_exclusions`) != 0 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM catalog_scan_sources WHERE source_id=$1::uuid AND item_id=$2::uuid AND NOT manual`, f.high, f.item) != 1 ||
		versionCount(t, f.jobFixture, `SELECT play_count FROM user_item_data WHERE user_id=$1::uuid AND item_id=$2::uuid`, viewer, f.item) != 2 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event='item.version_operation_undone'`) != 2 {
		t.Fatal("split undo incomplete")
	}

	// An expired operation cannot be undone.
	versionExec(t, f.jobFixture, `UPDATE item_version_operations SET created_at=now()-interval '31 days',undo_until=now()-interval '1 day' WHERE id=$1::uuid`, primary.ID)
	if _, err = f.s.UndoVersionOperation(f.ctx, f.a, primary.ID); !errors.Is(err, domain.ErrVersionUndoUnavailable) {
		t.Fatal("expired undo", err)
	}
	if _, err = f.s.UndoVersionOperation(f.ctx, f.a, "10000000-0000-4000-8000-000000000099"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("missing operation", err)
	}
}

func TestItemVersionMergeBoundariesPostgres(t *testing.T) {
	f := newVersionFixture(t)
	lib := f.registration.Library.ID
	setIDs := func(item, ids string) {
		t.Helper()
		versionExec(t, f.jobFixture, `INSERT INTO item_metadata_facts(item_id,field,value,source,updated_at) VALUES($1::uuid,'uniqueIds',$2::jsonb,'manual',now())
 ON CONFLICT(item_id,field) DO UPDATE SET value=EXCLUDED.value`, item, ids)
	}
	// G20.5: the NFO/provider identity decides; no override exists.
	setIDs(f.item, `[{"type":"tmdb","value":"949"},{"type":"imdb","value":"tt0113277"}]`)
	setIDs(f.otherItem, `[{"type":"TMDB","value":"1234"}]`)
	if _, err := f.s.MergeItems(f.ctx, f.a, f.item, f.otherItem); !errors.Is(err, domain.ErrVersionIdentityConflict) {
		t.Fatal("conflicting external IDs merged", err)
	}
	if versionCount(t, f.jobFixture, `SELECT count(*) FROM items WHERE id=$1::uuid`, f.otherItem) != 1 {
		t.Fatal("refused merge changed the catalog")
	}
	// Kind, library and series boundaries.
	episode := detailsItem(t, f.jobFixture, lib, "Heat", "Episode")
	sidecarSource(t, f.jobFixture, episode, "Show/S01E01.mkv")
	if _, err := f.s.MergeItems(f.ctx, f.a, f.item, episode); !errors.Is(err, domain.ErrVersionMergeIncompatible) {
		t.Fatal("movie and episode merged", err)
	}
	other, err := f.s.RegisterLibrary(f.ctx, "versions-other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	foreign := detailsItem(t, f.jobFixture, other.Library.ID, "Heat", "Movie")
	if _, err = f.s.MergeItems(f.ctx, f.a, f.item, foreign); !errors.Is(err, domain.ErrVersionMergeIncompatible) {
		t.Fatal("items of two libraries merged", err)
	}
	series := func(title string) (string, string) {
		s := detailsItem(t, f.jobFixture, lib, title, "Series")
		season := detailsItem(t, f.jobFixture, lib, "Season 1", "Season")
		versionExec(t, f.jobFixture, `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) VALUES($1::uuid,$2::uuid,'Season',$3::uuid,'Series')`, season, lib, s)
		return s, season
	}
	_, seasonA := series("Show A")
	_, seasonB := series("Show B")
	episodeOf := func(season, path string) string {
		e := detailsItem(t, f.jobFixture, lib, "Episode", "Episode")
		versionExec(t, f.jobFixture, `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) VALUES($1::uuid,$2::uuid,'Episode',$3::uuid,'Season')`, e, lib, season)
		sidecarSource(t, f.jobFixture, e, path)
		return e
	}
	a1, b1 := episodeOf(seasonA, "A/S01E01.mkv"), episodeOf(seasonB, "B/S01E01.mkv")
	if _, err = f.s.MergeItems(f.ctx, f.a, a1, b1); !errors.Is(err, domain.ErrVersionMergeIncompatible) {
		t.Fatal("episodes of two series merged", err)
	}
	if _, err = f.s.MergeItems(f.ctx, f.a, seasonA, seasonB); !errors.Is(err, domain.ErrVersionMergeIncompatible) {
		t.Fatal("container items merged", err)
	}
	a1b, a2 := episodeOf(seasonA, "A/S01E01.1080p.mkv"), episodeOf(seasonA, "A/S01E02.mkv")
	versionExec(t, f.jobFixture, `INSERT INTO item_metadata_facts(item_id,field,value,source,updated_at) VALUES($1::uuid,'episodeNumber','1','manual',now()),($2::uuid,'episodeNumber','2','manual',now())`, a1, a2)
	if _, err = f.s.MergeItems(f.ctx, f.a, a1, a2); !errors.Is(err, domain.ErrVersionIdentityConflict) {
		t.Fatal("different episode numbers merged", err)
	}
	// An active playback blocks the change.
	versionExec(t, f.jobFixture, `INSERT INTO playback_sessions(user_id,play_key,item_id,library_id,state,started_at,last_report_at) VALUES($1::uuid,'live',$2::uuid,$3::uuid,'active',now(),now())`, f.viewer.actor.UserID, a1b, lib)
	if _, err = f.s.MergeItems(f.ctx, f.a, a1, a1b); !errors.Is(err, domain.ErrVersionItemBusy) {
		t.Fatal("merge during playback", err)
	}
	versionExec(t, f.jobFixture, `UPDATE playback_sessions SET state='stopped',ended_at=now() WHERE play_key='live'`)
	if _, err = f.s.MergeItems(f.ctx, f.a, a1, a1b); err != nil {
		t.Fatal("same episode of the same series", err)
	}
	// Agreeing identities merge, but not while a live share link targets the
	// absorbed item (G48.6): it would vanish with the item.
	setIDs(f.otherItem, `[{"type":"tmdb","value":"949"}]`)
	grant, err := f.s.CreateShare(f.ctx, f.a, domain.ShareInput{ItemID: f.otherItem, ExpiresAt: time.Now().Add(time.Hour), MaxStreams: 1})
	if err != nil {
		t.Fatal("share on the absorbed item", err)
	}
	if _, err = f.s.MergeItems(f.ctx, f.a, f.item, f.otherItem); !errors.Is(err, domain.ErrVersionItemBusy) {
		t.Fatal("merge away a shared item", err)
	}
	if _, err = f.s.RevokeShare(f.ctx, f.a, grant.Share.ID); err != nil {
		t.Fatal("revoke share", err)
	}
	if _, err = f.s.MergeItems(f.ctx, f.a, f.item, f.otherItem); err != nil {
		t.Fatal("matching external IDs", err)
	}
	if _, err = f.s.MergeItems(f.ctx, f.a, f.item, f.item); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("self merge", err)
	}
}

func TestItemVersionPermissionsPostgres(t *testing.T) {
	f := newVersionFixture(t)
	viewer := f.viewer.actor
	// Non-administrators are refused before any lookup.
	for name, call := range map[string]func() error{
		"split": func() error {
			_, err := f.s.SplitVersion(f.ctx, viewer, f.item, domain.SplitVersionInput{SourceID: f.high})
			return err
		},
		"merge":   func() error { _, err := f.s.MergeItems(f.ctx, viewer, f.item, f.otherItem); return err },
		"primary": func() error { _, err := f.s.SetPrimaryVersion(f.ctx, viewer, f.item, f.low); return err },
		"undo":    func() error { _, err := f.s.UndoVersionOperation(f.ctx, viewer, f.item); return err },
		"view":    func() error { _, err := f.s.VersionOverview(f.ctx, viewer, f.item); return err },
	} {
		if err := call(); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("%s by a viewer: %v", name, err)
		}
	}
	// An administrator restricted by content rules cannot see, change or
	// undo on a hidden item, and learns nothing about it.
	split, err := f.s.SplitVersion(f.ctx, f.a, f.item, domain.SplitVersionInput{SourceID: f.high})
	if err != nil {
		t.Fatal(err)
	}
	versionExec(t, f.jobFixture, `UPDATE access_policy SET restrict_admins=true`)
	versionExec(t, f.jobFixture, `UPDATE users SET content_filtered=true WHERE id=$1::uuid`, f.a.UserID)
	versionExec(t, f.jobFixture, `INSERT INTO user_item_access_rules(user_id,item_id,effect) VALUES($1::uuid,$2::uuid,'hide')`, f.a.UserID, f.item)
	hidden := map[string]func() error{
		"split": func() error {
			_, err := f.s.SplitVersion(f.ctx, f.a, f.item, domain.SplitVersionInput{SourceID: f.low})
			return err
		},
		"merge":   func() error { _, err := f.s.MergeItems(f.ctx, f.a, f.otherItem, f.item); return err },
		"primary": func() error { _, err := f.s.SetPrimaryVersion(f.ctx, f.a, f.item, f.low); return err },
		"undo":    func() error { _, err := f.s.UndoVersionOperation(f.ctx, f.a, split.ID); return err },
		"view":    func() error { _, err := f.s.VersionOverview(f.ctx, f.a, f.item); return err },
		"missing": func() error {
			_, err := f.s.VersionOverview(f.ctx, f.a, "10000000-0000-4000-8000-000000000099")
			return err
		},
	}
	for name, call := range hidden {
		if err := call(); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s on a hidden item: %v", name, err)
		}
	}
	if versionItemOf(t, f.jobFixture, f.low) != f.item || versionCount(t, f.jobFixture, `SELECT count(*) FROM item_version_operations WHERE undone_at IS NULL`) != 1 {
		t.Fatal("hidden item changed")
	}
	// Track preferences: hidden and missing items and foreign versions are
	// the same not found.
	versionExec(t, f.jobFixture, `INSERT INTO user_item_access_rules(user_id,item_id,effect) VALUES($1::uuid,$2::uuid,'hide')`, viewer.UserID, f.otherItem)
	versionExec(t, f.jobFixture, `UPDATE users SET content_filtered=true WHERE id=$1::uuid`, viewer.UserID)
	lang := "ja"
	for name, item := range map[string]string{"hidden": f.otherItem, "missing": "10000000-0000-4000-8000-000000000099"} {
		if _, err := f.s.TrackPreferences(f.ctx, viewer, item); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s read: %v", name, err)
		}
		if _, err := f.s.SetTrackPreference(f.ctx, viewer, item, "", domain.TrackPreference{AudioLanguage: &lang}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s write: %v", name, err)
		}
	}
	if _, err := f.s.SetTrackPreference(f.ctx, viewer, f.item, f.otherSource, domain.TrackPreference{AudioLanguage: &lang}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign version preference", err)
	}
	if versionCount(t, f.jobFixture, `SELECT count(*) FROM user_track_preferences`) != 0 {
		t.Fatal("refused preference stored")
	}
	view, err := f.s.SetTrackPreference(f.ctx, viewer, f.item, f.low, domain.TrackPreference{AudioLanguage: &lang})
	if err != nil || len(view.Versions) != 1 || view.Versions[0].SourceID != f.low || view.Item != nil {
		t.Fatalf("version preference: %+v %v", view, err)
	}
}

// TestItemVersionDecisionsSurviveCatalogSyncPostgres runs real scans and
// synchronisations around manual decisions: a reparse of every file and a
// file that disappears and comes back keep the split apart and the merge
// together. Lifting the exclusion is the control that the exclusion, not
// chance, kept the file apart.
func TestItemVersionDecisionsSurviveCatalogSyncPostgres(t *testing.T) {
	f := newJobFixture(t)
	lib := f.registration.Library.ID
	hd, uhd := "Inception (2010)/Inception (2010).mkv", "Inception (2010)/Inception (2010) - 2160p.mkv"
	heatA, heatB := "A/Heat (1995).mkv", "B/Heat (1995).mkv"
	writeSyncFiles(t, syncRootPath(t, f), hd, uhd, heatA, heatB)
	enableAutoSync(t, f)
	stop := startSyncWorker(t, f, 2)
	defer stop()
	f.submit(t, "versions-scan")
	waitJobsIdle(t, f)
	source := func(path string) string {
		t.Helper()
		var id string
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM media_sources WHERE relative_path=$1`, path).Scan(&id); err != nil {
			t.Fatal("source of", path, err)
		}
		return id
	}
	inception := versionItemOf(t, f, source(hd))
	if versionItemOf(t, f, source(uhd)) != inception {
		t.Fatal("versions of one movie were not grouped")
	}
	itemA, itemB := versionItemOf(t, f, source(heatA)), versionItemOf(t, f, source(heatB))
	if itemA == itemB {
		t.Fatal("movies of two folders were grouped")
	}
	split, err := f.s.SplitVersion(f.ctx, f.a, inception, domain.SplitVersionInput{SourceID: source(uhd), Exclude: true, Title: "Inception 4K"})
	if err != nil {
		t.Fatal(err)
	}
	created := split.OtherItemID
	if _, err = f.s.MergeItems(f.ctx, f.a, itemA, itemB); err != nil {
		t.Fatal(err)
	}
	items := versionCount(t, f, `SELECT count(*) FROM items WHERE library_id=$1::uuid`, lib)
	resync := func(key string) {
		t.Helper()
		if _, _, err := f.s.SubmitCatalogSync(f.ctx, f.a, lib, key, domain.JobPriorityManual, f.policy); err != nil {
			t.Fatal(err)
		}
		waitJobsIdle(t, f)
	}
	check := func(stage string) {
		t.Helper()
		if versionItemOf(t, f, source(uhd)) != created || versionItemOf(t, f, source(hd)) != inception ||
			versionItemOf(t, f, source(heatA)) != itemA || versionItemOf(t, f, source(heatB)) != itemA ||
			versionCount(t, f, `SELECT count(*) FROM items WHERE library_id=$1::uuid`, lib) != items ||
			versionCount(t, f, `SELECT count(*) FROM items WHERE id=$1::uuid AND title='Inception 4K'`, created) != 1 {
			t.Fatalf("%s: synchronisation reverted a manual decision", stage)
		}
	}
	// Every tracked file is planned again (new parser version).
	versionExec(t, f, `UPDATE catalog_scan_sources SET parser_version='medianame-v0'`)
	resync("versions-reparse")
	check("reparse")
	// The split file and the absorbed item's file vanish from the catalog
	// and are found again as new files.
	versionExec(t, f, `DELETE FROM media_sources WHERE relative_path IN ($1,$2)`, uhd, heatB)
	resync("versions-reappear")
	check("reappear")
	if versionCount(t, f, `SELECT count(*) FROM catalog_scan_sources WHERE relative_path=$1 AND item_id=$2::uuid`, uhd, created) != 1 {
		t.Fatal("reappeared file not registered on the split item")
	}
	// Control: without the exclusion the file joins its group again.
	overview, err := f.s.VersionOverview(f.ctx, f.a, inception)
	if err != nil || len(overview.Exclusions) != 1 || overview.Exclusions[0].FileName != "Inception (2010) - 2160p.mkv" {
		t.Fatalf("exclusions: %+v %v", overview.Exclusions, err)
	}
	if _, err = f.s.RemoveVersionExclusion(f.ctx, f.a, inception, overview.Exclusions[0].ID); err != nil {
		t.Fatal(err)
	}
	versionExec(t, f, `DELETE FROM catalog_scan_items WHERE item_id=$1::uuid`, created)
	versionExec(t, f, `DELETE FROM media_sources WHERE relative_path=$1`, uhd)
	resync("versions-unexcluded")
	if versionItemOf(t, f, source(uhd)) != inception {
		t.Fatal("control: the file did not rejoin its group without the exclusion")
	}
}

// TestTrackPreferencesPickDefaultTracksPostgres stores preferences on every
// level and reads the default tracks playback information reports. The
// probed version has embedded en (default) and ja audio and a forced en PGS
// subtitle; external en.srt, ja.forced.ass, a commentary AC3 and an MKA.
func TestTrackPreferencesPickDefaultTracksPostgres(t *testing.T) {
	f := newPlaybackFixture(t)
	reader := imageRepositoryActor(t, f.jobFixture, "tracks-native", access.ClientNative)
	versionExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, reader.UserID, f.registration.Library.ID)
	catalog, err := app.NewCatalog(f.s).WithPlayback(f.s)
	if err == nil {
		catalog, err = catalog.WithVersions(f.s)
	}
	if err != nil {
		t.Fatal(err)
	}
	external := map[string]string{}
	if rows, err := f.s.Pool.Query(f.ctx, `SELECT relative_path,id::text FROM media_sidecar_tracks WHERE source_id=$1::uuid`, f.probed); err == nil {
		for rows.Next() {
			var p, id string
			if rows.Scan(&p, &id) == nil {
				external[p[strings.LastIndex(p, "/")+1:]] = id
			}
		}
		rows.Close()
	}
	if len(external) != 4 {
		t.Fatal("sidecar fixture", external)
	}
	tracks := func(stage string) (domain.DefaultTracks, domain.DefaultTracks) {
		t.Helper()
		sources, err := catalog.PlaybackSources(f.ctx, reader, f.item)
		if err != nil || len(sources) != 4 || sources[0].ID != f.probed {
			t.Fatalf("%s: sources %v", stage, err)
		}
		for _, s := range sources {
			if s.DefaultTracks == nil {
				t.Fatalf("%s: no default tracks", stage)
			}
		}
		return *sources[0].DefaultTracks, *sources[1].DefaultTracks
	}
	embedded := func(sel *domain.TrackSelection, index int) bool {
		return sel != nil && sel.Kind == domain.TrackEmbedded && sel.Index != nil && *sel.Index == index
	}
	ext := func(sel *domain.TrackSelection, name string) bool {
		return sel != nil && sel.Kind == domain.TrackExternal && sel.ID == external[name]
	}
	set := func(source string, p domain.TrackPreference) {
		t.Helper()
		normalized, err := domain.NormalizeTrackPreference(p, source != "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = catalog.SetTrackPreference(f.ctx, reader, f.item, source, normalized); err != nil {
			t.Fatal("set preference", err)
		}
	}
	str := func(v string) *string { return &v }
	yes := true

	// No preference: the account language (en-US) leaves English audio
	// alone and only shows the forced English subtitle.
	probed, _ := tracks("defaults")
	if !embedded(probed.Audio, 1) || !embedded(probed.Subtitle, 3) || probed.Basis != domain.TrackBasisLocale {
		t.Fatalf("defaults: %+v %+v %s", probed.Audio, probed.Subtitle, probed.Basis)
	}
	// Account language Japanese: English audio gets Japanese subtitles if
	// any full one exists; only forced ones do here.
	versionExec(t, f.jobFixture, `UPDATE users SET locale='ja-JP' WHERE id=$1::uuid`, reader.UserID)
	if probed, _ = tracks("locale"); !embedded(probed.Audio, 1) || probed.Subtitle == nil || probed.Basis != domain.TrackBasisLocale {
		t.Fatalf("locale: %+v %+v", probed.Audio, probed.Subtitle)
	}
	// User defaults: Japanese audio, subtitles always in English.
	if _, err = catalog.SetUserTrackPreference(f.ctx, reader, domain.TrackPreference{AudioLanguage: str("jpn"), SubtitleMode: str("always"), SubtitleLanguage: str("en")}); err != nil {
		t.Fatal(err)
	}
	if probed, _ = tracks("user"); !embedded(probed.Audio, 2) || !ext(probed.Subtitle, "Movie.en.srt") || probed.Basis != domain.TrackBasisUser {
		t.Fatalf("user: %+v %+v %s", probed.Audio, probed.Subtitle, probed.Basis)
	}
	// Item level: commentary wins over the language, subtitles off.
	set("", domain.TrackPreference{AudioCommentary: &yes, SubtitleMode: str("off")})
	probed, other := tracks("item")
	if !ext(probed.Audio, "Movie.commentary.ac3") || probed.Subtitle != nil || probed.Basis != domain.TrackBasisItem || other.Basis != domain.TrackBasisItem {
		t.Fatalf("item: %+v %+v %s", probed.Audio, probed.Subtitle, probed.Basis)
	}
	// Version level on the probed version only: a named embedded track and
	// the forced Japanese file; the other versions keep the item level.
	set(f.probed, domain.TrackPreference{AudioTrack: str("e:1"), SubtitleMode: str("always"), SubtitleTrack: str("x:" + external["Movie.ja.forced.ass"])})
	probed, other = tracks("version")
	if !embedded(probed.Audio, 1) || !ext(probed.Subtitle, "Movie.ja.forced.ass") || probed.Basis != domain.TrackBasisVersion || other.Basis != domain.TrackBasisItem || other.Subtitle != nil {
		t.Fatalf("version: %+v %+v %s / %s", probed.Audio, probed.Subtitle, probed.Basis, other.Basis)
	}
	view, err := catalog.TrackPreferences(f.ctx, reader, f.item)
	if err != nil || view.User == nil || view.Item == nil || len(view.Versions) != 1 || *view.User.AudioLanguage != "ja" {
		t.Fatalf("view: %+v %v", view, err)
	}
	// Named tracks belong to versions; invalid values are refused.
	for name, p := range map[string]domain.TrackPreference{"item track": {AudioTrack: str("e:1")}, "mode": {SubtitleMode: str("sometimes")}, "language": {AudioLanguage: str("klingon!")}} {
		if _, err := catalog.SetTrackPreference(f.ctx, reader, f.item, "", p); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	// Clearing a level falls back to the next one.
	set(f.probed, domain.TrackPreference{})
	set("", domain.TrackPreference{})
	if probed, _ = tracks("cleared"); probed.Basis != domain.TrackBasisUser {
		t.Fatalf("cleared: %s", probed.Basis)
	}
	if versionCount(t, f.jobFixture, `SELECT count(*) FROM user_track_preferences WHERE user_id=$1::uuid`, reader.UserID) != 1 {
		t.Fatal("cleared levels left rows")
	}
}

func TestItemVersionsMigrationRoundTrip(t *testing.T) {
	f := newVersionFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	if _, err := f.s.SplitVersion(f.ctx, f.a, f.item, domain.SplitVersionInput{SourceID: f.high, Exclude: true}); err != nil {
		t.Fatal(err)
	}
	lang := "en"
	if _, err := f.s.SetTrackPreference(f.ctx, f.viewer.actor, f.item, "", domain.TrackPreference{SubtitleLanguage: &lang}); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f.jobFixture, "item_versions")
	// An exclusion would be silently ignored by schema 77: refused.
	if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
		t.Fatal("downgrade dropped retained exclusions")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "status"); err != nil || !dirty || version >= want {
		t.Fatalf("refused downgrade state: %d %t %v", version, dirty, err)
	}
	// Recover from the refused step like an operator would: force the
	// version back, lift the exclusion, downgrade again.
	versionExec(t, f.jobFixture, `UPDATE schema_migrations SET version=$1,dirty=false`, want)
	versionExec(t, f.jobFixture, `DELETE FROM item_version_exclusions`)
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version >= want {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if versionCount(t, f.jobFixture, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('item_version_operations','item_version_exclusions','catalog_scan_item_aliases','item_primary_versions','user_track_preferences')`) != 0 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='catalog_scan_sources' AND column_name='manual'`) != 0 {
		t.Fatal("downgrade left schema 78 objects")
	}
	// The split itself is ordinary catalog data and stays.
	if versionItemOf(t, f.jobFixture, f.high) == f.item || versionCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event='item.version_split'`) != 1 {
		t.Fatal("downgrade lost older data")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if _, err := f.s.MergeItems(f.ctx, f.a, f.item, versionItemOf(t, f.jobFixture, f.high)); err != nil {
		t.Fatal("merge after round trip", err)
	}
}

// TestItemVersionUndoMergeAfterCatalogChangesPostgres undoes a merge after
// the absorbed version's file was removed from the catalog: kept rows that
// point at it are restored without the reference instead of failing.
func TestItemVersionUndoMergeAfterCatalogChangesPostgres(t *testing.T) {
	f := newVersionFixture(t)
	if _, err := f.s.SetPrimaryVersion(f.ctx, f.a, f.otherItem, f.otherSource); err != nil {
		t.Fatal(err)
	}
	merge, err := f.s.MergeItems(f.ctx, f.a, f.item, f.otherItem)
	if err != nil {
		t.Fatal(err)
	}
	versionExec(t, f.jobFixture, `DELETE FROM media_sources WHERE id=$1::uuid`, f.otherSource)
	// A later play of the target is kept by the undo.
	versionExec(t, f.jobFixture, `UPDATE user_item_data SET play_count=play_count+1,updated_at=now() WHERE item_id=$1::uuid`, f.item)
	if _, err = f.s.UndoVersionOperation(f.ctx, f.a, merge.ID); err != nil {
		t.Fatal("undo after the version was removed", err)
	}
	if versionCount(t, f.jobFixture, `SELECT count(*) FROM user_item_data WHERE item_id=$1::uuid AND played AND last_source_id IS NULL`, f.otherItem) != 1 ||
		versionCount(t, f.jobFixture, `SELECT count(*) FROM item_primary_versions WHERE item_id=$1::uuid`, f.otherItem) != 0 ||
		versionCount(t, f.jobFixture, `SELECT play_count FROM user_item_data WHERE item_id=$1::uuid`, f.item) != 2 {
		t.Fatal("restored rows not adjusted or later play lost")
	}
}
