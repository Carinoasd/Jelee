package postgres

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// partsOf lists the part numbers of the multi-part version whose first part
// is the file at path, in order; nil when the file is no first part.
func (f groupingFixture) partsOf(t *testing.T, path string) []string {
	t.Helper()
	rows, err := f.s.Pool.Query(f.ctx, `SELECT m.relative_path FROM media_source_parts p JOIN media_sources m ON m.id=p.source_id
 WHERE p.version_source_id=(SELECT id FROM media_sources WHERE relative_path=$1) ORDER BY p.part_number`, path)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func (f groupingFixture) partReason(t *testing.T, path string) string {
	t.Helper()
	var reason *string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT part_reason FROM catalog_scan_sources WHERE relative_path=$1`, path).Scan(&reason); err != nil {
		t.Fatal("part reason of", path, err)
	}
	if reason == nil {
		return ""
	}
	return *reason
}

func samePaths(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// G21.1: files of one directory that differ only in their part marker and
// are numbered 1..n become one version with ordered parts; a gap, mixed
// release tags or a single part are not merged and say why; multi-part
// versions and single-file versions of one item coexist; a rerun changes
// nothing; an administrator's version decision freezes the structure; a
// split moves a multi-part version with all its parts.
func TestCatalogSyncMultiPartVersionsPostgres(t *testing.T) {
	heat1, heat2 := "Heat (1995)/Heat (1995) - cd1.mkv", "Heat (1995)/Heat (1995) - cd2.mkv"
	gap1, gap3 := "Gap (2001)/Gap (2001) - Part 1.mkv", "Gap (2001)/Gap (2001) - Part 3.mkv"
	mix1, mix2 := "Mix (2003)/Mix (2003) 720p - cd1.mkv", "Mix (2003)/Mix (2003) 1080p - cd2.mkv"
	up720a, up720b := "Up (2009)/Up (2009) 720p - cd1.mkv", "Up (2009)/Up (2009) 720p - cd2.mkv"
	up1080a, up1080b, up1080c := "Up (2009)/Up (2009) 1080p - cd1.mkv", "Up (2009)/Up (2009) 1080p - cd2.mkv", "Up (2009)/Up (2009) 1080p - cd3.mkv"
	up4k := "Up (2009)/Up (2009) 2160p.mkv"
	ep1, ep2 := "Show/Season 1/Show S01E01 - Part 1.mkv", "Show/Season 1/Show S01E01 - Part 2.mkv"
	solo := "Solo (2018)/Solo (2018) - cd1.mkv"
	f, stop := newGroupingFixture(t, heat1, heat2, gap1, gap3, mix1, mix2, up720a, up720b, up1080a, up1080b, up1080c, up4k, ep1, ep2, solo)
	defer stop()

	// cd1 + cd2: one item, one version in two parts.
	if f.itemOf(t, heat1) != f.itemOf(t, heat2) || !samePaths(f.partsOf(t, heat1), heat1, heat2) || f.partsOf(t, heat2) != nil {
		t.Fatalf("heat parts: %v", f.partsOf(t, heat1))
	}
	if f.partReason(t, heat1) != domain.SourcePartMerged || f.partReason(t, heat2) != domain.SourcePartMerged {
		t.Fatal("heat reasons")
	}
	// Part 1 + Part 3: two versions of one item, not merged.
	if f.itemOf(t, gap1) != f.itemOf(t, gap3) || f.partsOf(t, gap1) != nil || f.partReason(t, gap1) != domain.SourcePartGap || f.partReason(t, gap3) != domain.SourcePartGap {
		t.Fatalf("gap: %v %s", f.partsOf(t, gap1), f.partReason(t, gap1))
	}
	// 720p cd1 + 1080p cd2 only complete each other across release tags.
	if f.partsOf(t, mix1) != nil || f.partReason(t, mix1) != domain.SourcePartMixed || f.partReason(t, mix2) != domain.SourcePartMixed {
		t.Fatalf("mix: %s", f.partReason(t, mix1))
	}
	// One item: a 720p version in two parts, a 1080p version in three and a
	// single-file 2160p version.
	up := f.itemOf(t, up4k)
	if f.itemOf(t, up720a) != up || f.itemOf(t, up1080c) != up || !samePaths(f.partsOf(t, up720a), up720a, up720b) || !samePaths(f.partsOf(t, up1080a), up1080a, up1080b, up1080c) ||
		f.partsOf(t, up4k) != nil || f.partReason(t, up4k) != "" {
		t.Fatalf("up: %v %v", f.partsOf(t, up720a), f.partsOf(t, up1080a))
	}
	if n := syncCount(t, f.jobFixture, `SELECT count(*) FROM media_sources m WHERE m.item_id=$1::uuid AND `+notLaterPartSQL("m"), up); n != 3 {
		t.Fatal("up versions", n)
	}
	// An episode in two parts.
	if f.itemOf(t, ep1) != f.itemOf(t, ep2) || !samePaths(f.partsOf(t, ep1), ep1, ep2) {
		t.Fatalf("episode parts: %v", f.partsOf(t, ep1))
	}
	// A lone part 1 is an ordinary version; an item with a single file is
	// not planned at all.
	if f.partsOf(t, solo) != nil || f.partReason(t, solo) != "" {
		t.Fatal("solo")
	}

	// A rerun plans the same structure and writes nothing.
	var stamp string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT string_agg(source_id::text||created_at::text,',' ORDER BY source_id) FROM media_source_parts`).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	f.resync(t, "parts-rerun")
	var again string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT string_agg(source_id::text||created_at::text,',' ORDER BY source_id) FROM media_source_parts`).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if stamp != again {
		t.Fatal("rerun rewrote parts")
	}

	// An administrator's main version freezes the gap item: its missing
	// part 2 appearing later does not reassemble it.
	gap := f.itemOf(t, gap1)
	var gapSource string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM media_sources WHERE relative_path=$1`, gap1).Scan(&gapSource); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetPrimaryVersion(f.ctx, f.a, gap, gapSource); err != nil {
		t.Fatal(err)
	}
	// Heat gets a third part; nothing holds it, so it is reassembled.
	gap2, heat3 := "Gap (2001)/Gap (2001) - Part 2.mkv", "Heat (1995)/Heat (1995) - cd3.mkv"
	writeSyncFiles(t, syncRootPath(t, f.jobFixture), gap2, heat3)
	f.submit(t, "parts-rescan")
	waitJobsIdle(t, f.jobFixture)
	if f.itemOf(t, gap2) != gap || f.partsOf(t, gap1) != nil || f.partReason(t, gap2) != domain.SourcePartHeld || f.partReason(t, gap1) != domain.SourcePartHeld {
		t.Fatalf("held gap: %v %s", f.partsOf(t, gap1), f.partReason(t, gap2))
	}
	if !samePaths(f.partsOf(t, heat1), heat1, heat2, heat3) {
		t.Fatalf("heat cd3: %v", f.partsOf(t, heat1))
	}

	// A later part is not a version: it cannot be split off or made the
	// main version. Splitting the 720p version moves both of its parts.
	var up720Source, up720Part string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT id::text FROM media_sources WHERE relative_path=$1),(SELECT id::text FROM media_sources WHERE relative_path=$2)`, up720a, up720b).Scan(&up720Source, &up720Part); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SplitVersion(f.ctx, f.a, up, domain.SplitVersionInput{SourceID: up720Part}); err == nil {
		t.Fatal("split a later part")
	}
	if _, err := f.s.SetPrimaryVersion(f.ctx, f.a, up, up720Part); err == nil {
		t.Fatal("later part as main version")
	}
	split, err := f.s.SplitVersion(f.ctx, f.a, up, domain.SplitVersionInput{SourceID: up720Source, Exclude: true})
	if err != nil {
		t.Fatal(err)
	}
	if f.itemOf(t, up720a) != split.OtherItemID || f.itemOf(t, up720b) != split.OtherItemID || !samePaths(f.partsOf(t, up720a), up720a, up720b) ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM item_version_exclusions WHERE item_id=$1::uuid`, up) != 2 {
		t.Fatal("split moved the parts apart")
	}
	// The split item and the item it left are held: a rerun keeps both.
	f.resync(t, "parts-after-split")
	if !samePaths(f.partsOf(t, up720a), up720a, up720b) || !samePaths(f.partsOf(t, up1080a), up1080a, up1080b, up1080c) {
		t.Fatal("rerun after split")
	}
	if _, err = f.s.UndoVersionOperation(f.ctx, f.a, split.ID); err != nil {
		t.Fatal(err)
	}
	if f.itemOf(t, up720a) != up || f.itemOf(t, up720b) != up || syncCount(t, f.jobFixture, `SELECT count(*) FROM item_version_exclusions WHERE item_id=$1::uuid`, up) != 0 {
		t.Fatal("undo split")
	}
}

// G21.1 with G20.1: copies in two directories, one of them in two parts,
// are compared by the combined duration of the parts.
func TestCatalogVersionGroupingCountsPartsAsOneVersionPostgres(t *testing.T) {
	a1, a2, b := "A/Heat (1995) - cd1.mkv", "A/Heat (1995) - cd2.mkv", "B/Heat (1995).mkv"
	f, stop := newGroupingFixture(t, a1, a2, b)
	defer stop()
	parted, whole := f.itemOf(t, a1), f.itemOf(t, b)
	if parted == whole || !samePaths(f.partsOf(t, a1), a1, a2) {
		t.Fatal("setup")
	}
	f.tmdb(t, parted, "949")
	f.tmdb(t, whole, "949")
	f.probe(t, a1, 90)
	f.probe(t, a2, 80)
	f.probe(t, b, 170.5)
	f.resync(t, "parts-grouping")
	if f.exists(t, parted) && f.exists(t, whole) {
		t.Fatalf("not merged: %s / %s", f.reason(t, parted), f.reason(t, whole))
	}
	kept := parted
	if !f.exists(t, parted) {
		kept = whole
	}
	if f.itemOf(t, a1) != kept || f.itemOf(t, b) != kept || !samePaths(f.partsOf(t, a1), a1, a2) {
		t.Fatal("merge lost the parts")
	}
}

// The parts schema downgrades without refusal (parts are derived from file
// names) and the next synchronisation after upgrading assembles them again.
func TestMediaSourcePartsMigrationRoundTrip(t *testing.T) {
	cd1, cd2 := "Heat (1995)/Heat (1995) - cd1.mkv", "Heat (1995)/Heat (1995) - cd2.mkv"
	f, stop := newGroupingFixture(t, cd1, cd2)
	if !samePaths(f.partsOf(t, cd1), cd1, cd2) {
		stop()
		t.Fatal("setup")
	}
	stop()
	dsn := f.s.Pool.Config().ConnString()
	want := downgradeAboveMigration(t, f.jobFixture, "media_source_parts")
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version >= want {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='media_source_parts'`) != 0 ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND (table_name,column_name) IN (('catalog_scan_sources','part_reason'),('catalog_sync_requests','parts_cursor'),('catalog_sync_requests','parts_done'))`) != 0 {
		t.Fatal("downgrade left parts objects")
	}
	// Both files stay versions of the item.
	if syncCount(t, f.jobFixture, `SELECT count(DISTINCT item_id) FROM media_sources WHERE relative_path IN ($1,$2)`, cd1, cd2) != 1 {
		t.Fatal("downgrade moved the files")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	stop = startSyncWorker(t, f.jobFixture, 2)
	defer stop()
	f.resync(t, "parts-after-upgrade")
	if !samePaths(f.partsOf(t, cd1), cd1, cd2) {
		t.Fatal("parts not rebuilt")
	}
}
