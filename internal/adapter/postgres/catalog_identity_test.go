package postgres

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// touchSyncFiles gives files a new modification time so the next scan and
// synchronisation plan them again.
func touchSyncFiles(t *testing.T, root string, stamp time.Time, files ...string) {
	t.Helper()
	for _, name := range files {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(name)), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
}

func identityOf(t *testing.T, f jobFixture, item string) domain.ItemIdentity {
	t.Helper()
	v, err := f.s.ItemIdentity(f.ctx, f.a, item)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func intValue(v *int) int {
	if v == nil {
		return -1
	}
	return *v
}

func locked(v bool) *bool { return &v }

// G21.5: a correction is locked and survives rescans that refresh the file
// name values underneath; unlocking returns the field to the scanner, and
// regrouping keeps the locked item whole.
func TestItemIdentityCorrectionSurvivesRescanAndUnlockReturnsScanValue(t *testing.T) {
	f := newJobFixtureWithin(t, 240*time.Second)
	root := syncRootPath(t, f)
	writeSyncFiles(t, root, syncLibraryFiles...)
	enableAutoSync(t, f)
	stop := startSyncWorker(t, f, 2)
	defer func() { stop() }()
	f.submit(t, "scan-1")
	waitJobsIdle(t, f)
	items := syncedItems(t, f)
	e1, e2 := items["Episode:Breaking Bad S01E01"].id, items["Episode:Breaking Bad S01E02"].id
	season1, season2 := items["Season:Season 1"].id, items["Season:Season 2"].id
	heat := items["Movie:Heat"].id

	v := identityOf(t, f, e2)
	if v.Revision != 1 || v.Kind != "Episode" || v.ParentID != season1 || v.SeasonSource != "scan" || intValue(v.Season) != 1 || intValue(v.Episode) != 2 || intValue(v.EpisodeEnd) != 2 || v.EpisodeLocked || v.UpdatedAt != nil {
		t.Fatalf("scan identity %+v", v)
	}
	// Correct the season, the multi-episode range and the season it is
	// filed under; lock the parent of the control episode without a value.
	v, err := f.s.UpdateItemIdentity(f.ctx, f.a, e2, 1, []domain.ItemIdentityPatch{
		{Field: "episode", HasValue: true, Number: 5, NumberEnd: 6},
		{Field: "season", HasValue: true, Number: 2},
		{Field: "parent", HasValue: true, Parent: season2},
	})
	if err != nil || v.Revision != 2 || v.SeasonSource != "manual" || !v.SeasonLocked || intValue(v.Season) != 2 || intValue(v.Episode) != 5 || intValue(v.EpisodeEnd) != 6 || !v.EpisodeLocked || v.ParentID != season2 || !v.ParentLocked || v.UpdatedAt == nil {
		t.Fatalf("correction %v %+v", err, v)
	}
	if v.Scan == nil || intValue(v.Scan.Episode) != 2 {
		t.Fatalf("scan values hidden %+v", v.Scan)
	}
	if _, err = f.s.UpdateItemIdentity(f.ctx, f.a, e2, 1, []domain.ItemIdentityPatch{{Field: "season", Locked: locked(false)}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	if syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='item.identity_changed' AND target_id=$1::uuid`, e2) != 1 {
		t.Fatal("correction not audited")
	}

	// Rescan: both files changed and their stored file-name numbers are
	// stale, so synchronisation rewrites them. The locked values hold; the
	// unlocked control takes the refreshed scan value.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE catalog_scan_items SET episode=9,episode_end=9 WHERE item_id IN ($1::uuid,$2::uuid)`, e1, e2); err != nil {
		t.Fatal(err)
	}
	touchSyncFiles(t, root, time.Unix(1800000000, 0), "Breaking Bad (2008)/Season 01/Breaking Bad S01E01.mkv", "Breaking Bad (2008)/Season 01/Breaking Bad S01E02.mkv")
	f.submit(t, "scan-2")
	waitJobsIdle(t, f)
	if r := latestSyncReport(t, f); r.Updated != 2 || r.Protected < 1 {
		t.Fatalf("rescan report %+v", r)
	}
	v = identityOf(t, f, e2)
	if v.Revision != 2 || intValue(v.Season) != 2 || intValue(v.Episode) != 5 || intValue(v.EpisodeEnd) != 6 || v.ParentID != season2 || v.EpisodeSource != "manual" || intValue(v.Scan.Episode) != 2 {
		t.Fatalf("rescan overwrote the lock %+v scan %+v", v, v.Scan)
	}
	if c := identityOf(t, f, e1); c.EpisodeSource != "scan" || intValue(c.Episode) != 1 || c.ParentID != season1 {
		t.Fatalf("unlocked control not refreshed %+v", c)
	}

	// A new version of the corrected episode groups into it: the locked item
	// is neither split nor moved back under its file's season.
	writeSyncFiles(t, root, "Breaking Bad (2008)/Season 01/Breaking Bad S01E02 - 1080p.mkv")
	f.submit(t, "scan-3")
	waitJobsIdle(t, f)
	if n := syncCount(t, f, `SELECT count(*) FROM media_sources WHERE item_id=$1::uuid`, e2); n != 2 {
		t.Fatal("new version not grouped into the locked item", n)
	}
	if v = identityOf(t, f, e2); v.ParentID != season2 || intValue(v.Episode) != 5 {
		t.Fatalf("regrouping changed the locked item %+v", v)
	}

	// Unlock: the scan value is effective at once and stays after a rescan.
	if v, err = f.s.UpdateItemIdentity(f.ctx, f.a, e2, 2, []domain.ItemIdentityPatch{{Field: "episode", Locked: locked(false)}, {Field: "season", Locked: locked(false)}}); err != nil || v.EpisodeSource != "scan" || intValue(v.Episode) != 2 || intValue(v.Season) != 1 || v.SeasonLocked || !v.ParentLocked {
		t.Fatalf("unlock %v %+v", err, v)
	}
	touchSyncFiles(t, root, time.Unix(1810000000, 0), "Breaking Bad (2008)/Season 01/Breaking Bad S01E02.mkv")
	f.submit(t, "scan-4")
	waitJobsIdle(t, f)
	if v = identityOf(t, f, e2); v.Revision != 3 || v.EpisodeSource != "scan" || intValue(v.Episode) != 2 || intValue(v.EpisodeEnd) != 2 {
		t.Fatalf("unlocked field did not return to the scan value %+v", v)
	}

	// Kind correction: a Movie becomes an Episode under a season. Its file
	// name still says Movie, so a reparse renames nothing.
	v, err = f.s.UpdateItemIdentity(f.ctx, f.a, heat, 1, []domain.ItemIdentityPatch{{Field: "kind", HasValue: true, Kind: "Episode"}, {Field: "parent", HasValue: true, Parent: season2}, {Field: "episode", HasValue: true, Number: 7, NumberEnd: 7}})
	if err != nil || v.Kind != "Episode" || !v.KindLocked || v.ParentID != season2 || intValue(v.Episode) != 7 || v.Scan == nil || v.Scan.Kind != "Episode" {
		t.Fatalf("kind correction %v %+v", err, v)
	}
	if _, err = f.s.UpdateItemIdentity(f.ctx, f.a, heat, 2, []domain.ItemIdentityPatch{{Field: "kind", HasValue: true, Kind: "HomeVideo"}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("scan item became HomeVideo", err)
	}
	if _, err = f.s.UpdateItemIdentity(f.ctx, f.a, season1, 1, []domain.ItemIdentityPatch{{Field: "kind", HasValue: true, Kind: "Movie"}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("season with children changed kind", err)
	}
	if _, err = f.s.UpdateItemIdentity(f.ctx, f.a, season1, 1, []domain.ItemIdentityPatch{{Field: "parent", HasValue: true, Parent: e1}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("season nested under an episode", err)
	}
	if _, err = f.s.UpdateItemIdentity(f.ctx, f.a, heat, 2, []domain.ItemIdentityPatch{{Field: "parent", HasValue: true, Parent: f.registration.Library.ID}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unknown parent accepted", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE catalog_scan_sources SET parser_version='medianame-v0' WHERE item_id=$1::uuid`, heat); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, "reparse", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	waitJobsIdle(t, f)
	if v = identityOf(t, f, heat); v.Kind != "Episode" || v.ParentID != season2 || intValue(v.Episode) != 7 {
		t.Fatalf("reparse undid the kind correction %+v", v)
	}

	// An accepted disappearance prunes empty scan items but keeps one whose
	// identity is locked, like any other lock.
	stop()
	stop = func() {}
	for _, name := range []string{"Heat (1995).mp4", "Breaking Bad (2008)/Season 01/Breaking Bad S01E01.mkv"} {
		if err = os.Remove(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	lowered := f.policy
	lowered.MissingCountLimit, lowered.MissingPercentLimit = 1, 1
	reviewed := runReviewedScan(t, f, lowered, "scan-5")
	if _, err = f.s.AcceptInventoryMissing(f.ctx, f.a, reviewed.ID, 2, f.policy); err != nil {
		t.Fatal(err)
	}
	stop = startSyncWorker(t, f, 1)
	waitJobsIdle(t, f)
	if syncCount(t, f, `SELECT count(*) FROM items WHERE id=$1::uuid`, heat) != 1 || syncCount(t, f, `SELECT count(*) FROM items WHERE id=$1::uuid`, e1) != 0 {
		t.Fatal("pruning ignored the identity lock or kept the unlocked item")
	}
}

// runReviewedScan runs one scan with thresholds low enough that the missing
// files are held for review.
func runReviewedScan(t *testing.T, f jobFixture, policy domain.JobPolicy, key string) domain.Job {
	t.Helper()
	job, _, err := f.s.SubmitJob(f.ctx, f.a, f.registration.Library.ID, key, domain.JobPriorityManual, policy)
	if err != nil {
		t.Fatal(err)
	}
	stop := startSyncWorker(t, f, 1)
	waitJobsIdle(t, f)
	stop()
	got := f.get(t, job.ID)
	if !got.ReviewRequired || got.Missing != 2 {
		t.Fatalf("scan not held for review %+v", got)
	}
	return got
}

func pendingByPath(t *testing.T, f jobFixture) map[string]domain.CatalogPendingEntry {
	t.Helper()
	rows, err := f.s.ListCatalogPending(f.ctx, f.a, f.registration.Library.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]domain.CatalogPendingEntry{}
	for _, e := range rows {
		result[e.Path] = e
	}
	return result
}

// G21.5: pending files are accepted with corrected values or rejected; a
// rejection holds until the file changes and the accepted identity is locked.
func TestCatalogPendingAcceptAndReject(t *testing.T) {
	f := newJobFixtureWithin(t, 180*time.Second)
	root := syncRootPath(t, f)
	writeSyncFiles(t, root, syncLibraryFiles...)
	enableAutoSync(t, f)
	stop := startSyncWorker(t, f, 2)
	defer stop()
	f.submit(t, "scan-1")
	waitJobsIdle(t, f)
	pending := pendingByPath(t, f)
	n12, n13, clip := pending["Naruto/Naruto - 012.mkv"], pending["Naruto/Naruto - 013.mkv"], pending["random clip.mkv"]
	if len(pending) != 3 || n12.Decision != "pending" || n13.ID == "" || clip.ID == "" {
		t.Fatalf("pending %+v", pending)
	}
	library := f.registration.Library.ID

	// Reject: kept out across a rerun and a parser change, decided again
	// once the file changes.
	r, err := f.s.RejectCatalogPending(f.ctx, f.a, library, n12.ID)
	if err != nil || r.Decision != "rejected" || r.Path != n12.Path {
		t.Fatal("reject", err, r)
	}
	if _, err = f.s.RejectCatalogPending(f.ctx, f.a, library, n12.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("repeated rejection", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE catalog_scan_pending SET parser_version='medianame-v0' WHERE id=$1::uuid`, n12.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitCatalogSync(f.ctx, f.a, library, "rerun", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	waitJobsIdle(t, f)
	if p := pendingByPath(t, f)[n12.Path]; p.Decision != "rejected" || p.ID != n12.ID {
		t.Fatalf("rejection lost on rerun %+v", p)
	}

	// Accept with completed numbers: a series, season and locked episode.
	series, season, episode := "Naruto", 1, 13
	if _, err = f.s.AcceptCatalogPending(f.ctx, f.a, library, n13.ID, domain.CatalogPendingDecision{Kind: "episode", SeriesTitle: &series}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("episode without a season accepted", err)
	}
	a, err := f.s.AcceptCatalogPending(f.ctx, f.a, library, n13.ID, domain.CatalogPendingDecision{Kind: "episode", SeriesTitle: &series, Season: &season, Episode: &episode})
	if err != nil || a.Kind != "Episode" || !a.Created || a.ItemID == "" {
		t.Fatal("accept", err, a)
	}
	v := identityOf(t, f, a.ItemID)
	if !v.KindLocked || !v.EpisodeLocked || intValue(v.Episode) != 13 || intValue(v.Season) != 1 || v.ParentKind != "Season" {
		t.Fatalf("accepted identity %+v", v)
	}
	if _, err = f.s.AcceptCatalogPending(f.ctx, f.a, library, n13.ID, domain.CatalogPendingDecision{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("accepted twice", err)
	}
	// A rejected entry may still be accepted while its file is unchanged;
	// a file changed since the baseline is a conflict until resynchronised.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE catalog_scan_pending SET size=size+1 WHERE id=$1::uuid`, clip.ID); err != nil {
		t.Fatal(err)
	}
	title := "Holiday clip"
	if _, err = f.s.AcceptCatalogPending(f.ctx, f.a, library, clip.ID, domain.CatalogPendingDecision{Kind: "movie", Title: &title}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale pending entry accepted", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE catalog_scan_pending SET size=size-1 WHERE id=$1::uuid`, clip.ID); err != nil {
		t.Fatal(err)
	}
	m, err := f.s.AcceptCatalogPending(f.ctx, f.a, library, clip.ID, domain.CatalogPendingDecision{Kind: "movie", Title: &title})
	if err != nil || m.Kind != "Movie" || !m.Created {
		t.Fatal("accept movie", err, m)
	}
	if syncCount(t, f, `SELECT count(*) FROM items i JOIN item_metadata_fields m ON m.item_id=i.id AND m.field='title' WHERE i.id=$1::uuid AND i.title='Holiday clip' AND m.source='manual'`, m.ItemID) != 1 {
		t.Fatal("accepted movie title not manual")
	}
	if syncCount(t, f, `SELECT count(*) FROM item_parent_links e JOIN item_parent_links s ON s.item_id=e.parent_id JOIN items r ON r.id=s.parent_id WHERE e.item_id=$1::uuid AND r.kind='Series' AND r.title='Naruto'`, a.ItemID) != 1 {
		t.Fatal("accepted episode not under the Naruto series")
	}
	if syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='catalog_sync.pending_accepted'`) != 2 || syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='catalog_sync.pending_rejected' AND target_id=$1::uuid`, n12.ID) != 1 {
		t.Fatal("decisions not audited")
	}

	// Rescan after the files changed: the accepted items keep their locked
	// identity, the rejected file is pending again.
	touchSyncFiles(t, root, time.Unix(1800000000, 0), n12.Path, n13.Path, clip.Path)
	f.submit(t, "scan-2")
	waitJobsIdle(t, f)
	if p := pendingByPath(t, f); len(p) != 1 || p[n12.Path].Decision != "pending" {
		t.Fatalf("changed rejected file not pending again %+v", p)
	}
	if v = identityOf(t, f, a.ItemID); intValue(v.Episode) != 13 || v.Kind != "Episode" {
		t.Fatalf("rescan changed the accepted identity %+v", v)
	}
	if syncCount(t, f, `SELECT count(*) FROM media_sources`) != 8+2 {
		t.Fatal("accepted files duplicated or lost")
	}
}

// The previous schema would let a scan overwrite locked identification and
// turn rejected files into items: retained decisions refuse the downgrade,
// unlocked rows alone do not.
func TestItemIdentityMigrationRefusesRetainedDecisions(t *testing.T) {
	f := newJobFixture(t)
	var item string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Film','Movie') RETURNING id::text`, f.registration.Library.ID).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.UpdateItemIdentity(f.ctx, f.a, item, 1, []domain.ItemIdentityPatch{{Field: "kind", Locked: locked(true)}}); err != nil {
		t.Fatal(err)
	}
	refuseRetainedDowngrade(t, f, "item_identity_corrections", "a locked identity was downgraded")
}

func TestItemIdentityMigrationRoundTripWithoutLocks(t *testing.T) {
	f := newJobFixture(t)
	var item string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Film','Movie') RETURNING id::text`, f.registration.Library.ID).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.UpdateItemIdentity(f.ctx, f.a, item, 1, []domain.ItemIdentityPatch{{Field: "kind", Locked: locked(false)}}); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f, "item_identity_corrections")
	dsn := f.s.Pool.Config().ConnString()
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version >= want {
		t.Fatal("unlocked rows refused the downgrade", version, dirty, err)
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("upgrade again", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM item_identity_corrections`) != 0 || syncCount(t, f, `SELECT count(*) FROM items WHERE id=$1::uuid`, item) != 1 {
		t.Fatal("round trip lost the item or kept the dropped rows")
	}
}
