package postgres

import (
	"fmt"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// groupingFixture runs real scans and synchronisations with a probe tool
// identity, so tests can store probed runtimes for synchronised files.
type groupingFixture struct {
	jobFixture
	tool string
}

func newGroupingFixture(t *testing.T, files ...string) (groupingFixture, func()) {
	t.Helper()
	f := groupingFixture{jobFixture: newJobFixtureWithin(t, 240*time.Second)}
	if err := f.s.EnsureProbePolicy(f.ctx, domain.DefaultProbeCachePolicy()); err != nil {
		t.Fatal(err)
	}
	identity, err := f.s.RegisterProbeIdentity(f.ctx, probeTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	f.tool = identity.ID
	writeSyncFiles(t, syncRootPath(t, f.jobFixture), files...)
	enableAutoSync(t, f.jobFixture)
	stop := startSyncWorker(t, f.jobFixture, 2)
	f.submit(t, "grouping-scan")
	waitJobsIdle(t, f.jobFixture)
	return f, stop
}

func (f groupingFixture) itemOf(t *testing.T, path string) string {
	t.Helper()
	var item string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT item_id::text FROM media_sources WHERE relative_path=$1`, path).Scan(&item); err != nil {
		t.Fatal("item of", path, err)
	}
	return item
}

// nfoFact stores a fact as an NFO observation of the item's first version.
func (f groupingFixture) nfoFact(t *testing.T, item, field, value string) {
	t.Helper()
	projection := "provider-identifiers-v1"
	if field != "uniqueIds" {
		projection = "episode-details-v1"
	}
	versionExec(t, f.jobFixture, `INSERT INTO item_metadata_facts(item_id,field,value,source,updated_at,nfo_origin)
 SELECT $1::uuid,$2,$3::jsonb,'nfo',now(),jsonb_build_object('sourceId',m.id::text,'rootId',m.root_id::text,'generation',1,'sha256',repeat('a',64),
  'identityDigest',repeat('b',64),'projection',$4::text,'readAt','2026-10-01T00:00:00Z','locked',false)
 FROM media_sources m WHERE m.item_id=$1::uuid ORDER BY m.id LIMIT 1
 ON CONFLICT(item_id,field) DO UPDATE SET value=EXCLUDED.value,nfo_origin=EXCLUDED.nfo_origin`, item, field, value, projection)
}

func (f groupingFixture) tmdb(t *testing.T, item, id string) {
	t.Helper()
	f.nfoFact(t, item, "uniqueIds", fmt.Sprintf(`[{"type":"tmdb","value":%q}]`, id))
}

// probe stores a ready probe row with the runtime for the file as the
// synchronisation registered it (same size and modification time).
func (f groupingFixture) probe(t *testing.T, path string, minutes float64) {
	t.Helper()
	metadata := fmt.Sprintf(`{"format":{"durationMicros":%d},"streams":[{"index":0,"kind":"video","codec":"h264","video":{}}],"chapters":[]}`, int64(minutes*60e6))
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if err = ensureProbeLibrary(f.ctx, tx, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.ctx, `INSERT INTO probe_cache(root_id,relative_path,library_id,size,modified_unix_nano,fingerprint,fingerprint_version,tool_version_id,library_generation,root_generation,state,metadata,expires_at,charge_bytes)
 SELECT r.id,s.relative_path,r.library_id,s.size,s.modified_unix_nano,sha256(convert_to(s.relative_path,'UTF8')),'edge-sha256-v1',$2::uuid,l.probe_generation,r.probe_generation,'ready',$3::jsonb,
  clock_timestamp()+interval '1 day',2048+octet_length($3::jsonb::text)
 FROM catalog_scan_sources s JOIN library_roots r ON r.id=s.root_id JOIN libraries l ON l.id=r.library_id WHERE s.relative_path=$1
 ON CONFLICT(root_id,relative_path) DO UPDATE SET metadata=EXCLUDED.metadata,charge_bytes=EXCLUDED.charge_bytes`, path, f.tool, metadata); err != nil {
		t.Fatal("seed probe", err)
	}
	if _, err = tx.Exec(f.ctx, `UPDATE probe_cache_quota SET rows_used=(SELECT count(*) FROM probe_cache),bytes_used=(SELECT sum(charge_bytes) FROM probe_cache);
 UPDATE probe_library_quota q SET rows_used=(SELECT count(*) FROM probe_cache c WHERE c.library_id=q.library_id),bytes_used=(SELECT COALESCE(sum(charge_bytes),0) FROM probe_cache c WHERE c.library_id=q.library_id)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func (f groupingFixture) resync(t *testing.T, key string) domain.CatalogSyncReport {
	t.Helper()
	if _, _, err := f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, key, domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	waitJobsIdle(t, f.jobFixture)
	r := latestSyncReport(t, f.jobFixture)
	if r.Phase != "done" {
		t.Fatalf("sync did not finish %+v", r)
	}
	return r
}

func (f groupingFixture) reason(t *testing.T, item string) string {
	t.Helper()
	var reason *string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT grouping_reason FROM catalog_scan_items WHERE item_id=$1::uuid`, item).Scan(&reason); err != nil {
		t.Fatal("reason of", item, err)
	}
	if reason == nil {
		return ""
	}
	return *reason
}

func (f groupingFixture) exists(t *testing.T, item string) bool {
	t.Helper()
	return syncCount(t, f.jobFixture, `SELECT count(*) FROM items WHERE id=$1::uuid`, item) == 1
}

// G20.1, G20.5: copies of one movie in different directories are folded
// into one item once their NFO provider IDs agree and their runtimes are
// close; conflicting IDs, distant runtimes and missing probes keep them
// apart. The automatic merge is an undoable operation, and an undone merge
// is never made again.
func TestCatalogVersionGroupingAcrossDirectoriesPostgres(t *testing.T) {
	matrixA, matrixB := "A/The Matrix (1999).mkv", "B/The Matrix (1999).mkv"
	heatA, heatB := "A/Heat (1995).mkv", "B/Heat (1995).mkv"
	duneA, duneB := "A/Dune (2021).mkv", "B/Dune (2021).mkv"
	upA, upB := "A/Up (2009).mkv", "B/Up (2009).mkv"
	f, stop := newGroupingFixture(t, matrixA, matrixB, heatA, heatB, duneA, duneB, upA, upB)
	defer stop()
	matrix1, matrix2 := f.itemOf(t, matrixA), f.itemOf(t, matrixB)
	heat1, heat2 := f.itemOf(t, heatA), f.itemOf(t, heatB)
	dune1, dune2 := f.itemOf(t, duneA), f.itemOf(t, duneB)
	up1, up2 := f.itemOf(t, upA), f.itemOf(t, upB)
	if matrix1 == matrix2 || heat1 == heat2 || dune1 == dune2 || up1 == up2 {
		t.Fatal("copies in two directories were grouped by the file name alone")
	}
	// No NFO IDs yet: nothing to judge.
	if r := f.resync(t, "grouping-0"); r.Aggregated != 0 {
		t.Fatalf("grouped without NFO IDs %+v", r)
	}

	f.tmdb(t, matrix1, "603")
	f.tmdb(t, matrix2, "603")
	f.probe(t, matrixA, 136)
	f.probe(t, matrixB, 136.6)
	// A shared TMDB ID with conflicting IMDb IDs never merges, whatever
	// the runtime.
	f.nfoFact(t, heat1, "uniqueIds", `[{"type":"tmdb","value":"949"},{"type":"imdb","value":"tt0113277"}]`)
	f.nfoFact(t, heat2, "uniqueIds", `[{"type":"tmdb","value":"949"},{"type":"imdb","value":"tt0000001"}]`)
	f.probe(t, heatA, 170)
	f.probe(t, heatB, 170)
	// Same ID, but an extended cut 20 minutes longer: another cut.
	f.tmdb(t, dune1, "438631")
	f.tmdb(t, dune2, "438631")
	f.probe(t, duneA, 155)
	f.probe(t, duneB, 175)
	// Same ID, one copy not probed yet.
	f.tmdb(t, up1, "14160")
	f.tmdb(t, up2, "14160")
	f.probe(t, upA, 96)

	r := f.resync(t, "grouping-1")
	if r.Aggregated != 1 {
		t.Fatalf("report %+v", r)
	}
	kept, gone := matrix1, matrix2
	if !f.exists(t, kept) {
		kept, gone = gone, kept
	}
	if f.exists(t, gone) || f.itemOf(t, matrixA) != kept || f.itemOf(t, matrixB) != kept {
		t.Fatal("copies with the same NFO ID and runtime were not folded")
	}
	if !f.exists(t, heat1) || !f.exists(t, heat2) || f.reason(t, heat1) != domain.VersionGroupingIDConflict {
		t.Fatal("conflicting NFO IDs merged", f.reason(t, heat1))
	}
	if !f.exists(t, dune1) || !f.exists(t, dune2) || f.reason(t, dune1) != domain.VersionGroupingDuration {
		t.Fatal("distant runtimes merged", f.reason(t, dune1))
	}
	if !f.exists(t, up1) || !f.exists(t, up2) || f.reason(t, up1) != domain.VersionGroupingDurationUnknown {
		t.Fatal("unprobed copy merged", f.reason(t, up1))
	}
	overview, err := f.s.VersionOverview(f.ctx, f.a, kept)
	if err != nil || len(overview.Operations) != 1 {
		t.Fatal("automatic merge not recorded", err, overview.Operations)
	}
	op := overview.Operations[0]
	if !op.Automatic || op.ActorID != "" || op.Kind != domain.VersionOpMerge || op.OtherItemID != gone || !op.Undoable {
		t.Fatalf("automatic operation %+v", op)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event='item.versions_merged' AND target_id=$1::uuid AND after_state->>'automatic'='true' AND after_state->'evidence' ? 'external_id:tmdb'`, kept) != 1 {
		t.Fatal("automatic merge not audited with its evidence")
	}

	// Unchanged evidence is not judged again; a new probe is new evidence.
	f.probe(t, upB, 96.4)
	if r = f.resync(t, "grouping-2"); r.Aggregated != 1 || f.exists(t, up1) == f.exists(t, up2) {
		t.Fatalf("new probe did not fold the copies %+v", r)
	}

	// Undo restores both items; the undone decision is the administrator's
	// and the versions phase does not merge them again.
	if _, err = f.s.UndoVersionOperation(f.ctx, f.a, op.ID); err != nil {
		t.Fatal(err)
	}
	if !f.exists(t, gone) || f.itemOf(t, matrixB) == f.itemOf(t, matrixA) {
		t.Fatal("undo did not restore the absorbed item")
	}
	if r = f.resync(t, "grouping-3"); r.Aggregated != 0 || f.itemOf(t, matrixB) == f.itemOf(t, matrixA) {
		t.Fatalf("undone merge was made again %+v", r)
	}
	if f.reason(t, kept) != domain.VersionGroupingHeldManual {
		t.Fatal("undone merge not held", f.reason(t, kept))
	}
}

// G21.2: NFO episode numbers win over the file name when versions are
// identified; locks (G21.5) and manual version decisions (G20.3) keep items
// out of automatic grouping.
func TestCatalogVersionGroupingEpisodesPostgres(t *testing.T) {
	dir := "Show (2020)/Season 01/"
	misnamed, proper := dir+"Show S01E05.mkv", dir+"Show S01E06.mkv"
	lockedA, lockedB := dir+"Show S01E07.mkv", dir+"Show S01E08.mkv"
	manualA, manualB := dir+"Show S01E09.mkv", dir+"Show S01E10.mkv"
	f, stop := newGroupingFixture(t, misnamed, proper, lockedA, lockedB, manualA, manualB)
	defer stop()
	e5, e6 := f.itemOf(t, misnamed), f.itemOf(t, proper)
	e7, e8 := f.itemOf(t, lockedA), f.itemOf(t, lockedB)
	e9, e10 := f.itemOf(t, manualA), f.itemOf(t, manualB)
	for _, p := range []string{misnamed, proper, lockedA, lockedB, manualA, manualB} {
		f.probe(t, p, 23)
	}
	// The file named E05 is E06 by its NFO; both name the same episode ID.
	f.tmdb(t, e5, "777")
	f.tmdb(t, e6, "777")
	f.nfoFact(t, e6, "seasonNumber", "1")
	f.nfoFact(t, e6, "episodeNumber", "6")
	// Reverse check first: without the NFO numbers the file names differ
	// and the items stay apart.
	if r := f.resync(t, "episodes-1"); r.Aggregated != 0 || f.reason(t, e5) != domain.VersionGroupingNumbers {
		t.Fatalf("file-name numbers ignored %+v %s", r, f.reason(t, e5))
	}
	f.nfoFact(t, e5, "seasonNumber", "1")
	f.nfoFact(t, e5, "episodeNumber", "6")
	if v := identityOf(t, f.jobFixture, e5); v.EpisodeSource != domain.ItemIdentitySourceNFO || intValue(v.Episode) != 6 || intValue(v.Scan.Episode) != 5 {
		t.Fatalf("NFO numbers not effective %+v", v)
	}

	// Locked: E07 and E08 share an ID and NFO numbers, but E07's numbers
	// are an administrator's correction.
	for _, it := range []string{e7, e8} {
		f.tmdb(t, it, "888")
		f.nfoFact(t, it, "seasonNumber", "1")
		f.nfoFact(t, it, "episodeNumber", "8")
	}
	if _, err := f.s.UpdateItemIdentity(f.ctx, f.a, e7, 1, []domain.ItemIdentityPatch{{Field: "episode", HasValue: true, Number: 8, NumberEnd: 8}}); err != nil {
		t.Fatal(err)
	}
	// Manual: E09 and E10 agree too, but an administrator chose E10's main
	// version.
	for _, it := range []string{e9, e10} {
		f.tmdb(t, it, "999")
		f.nfoFact(t, it, "seasonNumber", "1")
		f.nfoFact(t, it, "episodeNumber", "10")
	}
	var e10Source string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM media_sources WHERE item_id=$1::uuid`, e10).Scan(&e10Source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetPrimaryVersion(f.ctx, f.a, e10, e10Source); err != nil {
		t.Fatal(err)
	}

	r := f.resync(t, "episodes-2")
	if r.Aggregated != 1 {
		t.Fatalf("report %+v", r)
	}
	kept := e5
	if !f.exists(t, kept) {
		kept = e6
	}
	if f.itemOf(t, misnamed) != kept || f.itemOf(t, proper) != kept {
		t.Fatal("NFO numbers did not identify the misnamed file")
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event='item.versions_merged' AND target_id=$1::uuid AND after_state->'evidence' ? $2`, kept, domain.VersionGroupingNFONumbers) != 1 {
		t.Fatal("NFO-over-file-name reason not recorded")
	}
	if !f.exists(t, e7) || !f.exists(t, e8) || f.reason(t, e7) != domain.VersionGroupingHeldLocked {
		t.Fatal("locked item grouped automatically", f.reason(t, e7))
	}
	if !f.exists(t, e9) || !f.exists(t, e10) || f.reason(t, e10) != domain.VersionGroupingHeldManual {
		t.Fatal("manually decided item grouped automatically", f.reason(t, e10))
	}
	if v := identityOf(t, f.jobFixture, e7); !v.EpisodeLocked || intValue(v.Episode) != 8 || v.ParentID == "" {
		t.Fatalf("lock changed %+v", v)
	}

	// A new file named E05 beside the misnamed one: its group now points at
	// an item whose NFO says E06, so the file stands apart with the reason.
	extra := dir + "Show S01E05 - 1080p.mkv"
	writeSyncFiles(t, syncRootPath(t, f.jobFixture), extra)
	f.submit(t, "episodes-scan-2")
	waitJobsIdle(t, f.jobFixture)
	alone := f.itemOf(t, extra)
	if alone == kept || f.reason(t, alone) != domain.VersionGroupingNFONumbersDiffer {
		t.Fatal("file joined an item whose NFO names another episode", f.reason(t, alone))
	}
	// Registered versions stay where they are on every later run.
	f.resync(t, "episodes-3")
	if f.itemOf(t, misnamed) != kept || f.itemOf(t, proper) != kept || f.itemOf(t, extra) != alone {
		t.Fatal("a later run moved registered versions")
	}
}

func TestCatalogVersionGroupingMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	var item string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Film','Movie') RETURNING id::text`, f.registration.Library.ID).Scan(&item); err != nil {
		t.Fatal(err)
	}
	versionExec(t, f, `INSERT INTO catalog_scan_items(item_id,library_id,kind,group_digest,parser_version,scan_title,grouping_evidence,grouping_reason) VALUES($1::uuid,$2::uuid,'Movie',sha256('g'::bytea),$3,'Film',sha256('e'::bytea),'alone')`, item, f.registration.Library.ID, domain.CatalogSyncParserVersion)
	versionExec(t, f, `INSERT INTO item_version_operations(library_id,kind,automatic,item_id,other_item_id,undo,undo_until) VALUES($1::uuid,'merge',true,$2::uuid,gen_random_uuid(),'{}'::jsonb,now()+interval '1 day')`, f.registration.Library.ID, item)
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_version_operations(library_id,kind,automatic,actor_id,item_id,undo,undo_until) VALUES($1::uuid,'primary',true,$2::uuid,$3::uuid,'{}'::jsonb,now()+interval '1 day')`, f.registration.Library.ID, f.a.UserID, item); err == nil {
		t.Fatal("an automatic operation other than an actorless merge was accepted")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE catalog_scan_items SET grouping_reason='Bad reason' WHERE item_id=$1::uuid`, item); err == nil {
		t.Fatal("malformed grouping reason accepted")
	}
	want := downgradeAboveMigration(t, f, "catalog_version_grouping")
	dsn := f.s.Pool.Config().ConnString()
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version >= want {
		t.Fatal("automatic merges refused the downgrade", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM item_version_operations WHERE item_id=$1::uuid AND kind='merge'`, item) != 1 {
		t.Fatal("downgrade lost the automatic merge")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("upgrade again", version, dirty, err)
	}
	var evidence []byte
	var automatic bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT c.grouping_evidence,o.automatic FROM catalog_scan_items c JOIN item_version_operations o ON o.item_id=c.item_id WHERE c.item_id=$1::uuid`, item).Scan(&evidence, &automatic); err != nil || evidence != nil || automatic {
		t.Fatal("round trip kept dropped columns", err, evidence, automatic)
	}
}
