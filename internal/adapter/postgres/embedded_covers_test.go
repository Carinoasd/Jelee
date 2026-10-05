package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// coverTestMetadata has an ordinary video stream, an audio stream and the
// attached picture as the second video stream (container index 2).
const coverTestMetadata = `{"format":{"names":["matroska","webm"]},"streams":[
 {"index":0,"kind":"video","codec":"h264","video":{"width":320,"height":180}},
 {"index":1,"kind":"audio","codec":"aac","audio":{}},
 {"index":2,"kind":"video","codec":"png","attachedPic":true,"video":{"width":16,"height":16}}],"chapters":[]}`

const coverTestPlainMetadata = `{"format":{},"streams":[{"index":0,"kind":"video","codec":"h264","video":{}}],"chapters":[]}`

func coverFingerprint(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// seedCoverProbe stores one ready probe row with exact quota counters, the
// way seedPlaybackProbe does, but with a chosen fingerprint and metadata.
func seedCoverProbe(t *testing.T, f probeFixture, relative string, size, mtime int64, fingerprint, metadata string) {
	t.Helper()
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if err := ensureProbeLibrary(f.ctx, tx, f.registration.Library.ID); err != nil {
		t.Fatal("probe library scope", err)
	}
	if _, err := tx.Exec(f.ctx, `INSERT INTO probe_cache(root_id,relative_path,library_id,size,modified_unix_nano,fingerprint,fingerprint_version,tool_version_id,library_generation,root_generation,state,metadata,expires_at,charge_bytes)
 SELECT r.id,$2,r.library_id,$4,$5,decode($6,'hex'),'edge-sha256-v1',$3::uuid,l.probe_generation,r.probe_generation,'ready',$7::jsonb,
  clock_timestamp()+interval '1 day',2048+octet_length($7::jsonb::text)
 FROM library_roots r JOIN libraries l ON l.id=r.library_id WHERE r.id=$1::uuid
 ON CONFLICT(root_id,relative_path) DO UPDATE SET size=EXCLUDED.size,modified_unix_nano=EXCLUDED.modified_unix_nano,fingerprint=EXCLUDED.fingerprint,
  metadata=EXCLUDED.metadata,charge_bytes=EXCLUDED.charge_bytes`,
		f.registration.RootID, relative, f.identity.ID, size, mtime, fingerprint, metadata); err != nil {
		t.Fatal("seed probe row", err)
	}
	if _, err := tx.Exec(f.ctx, `UPDATE probe_cache_quota SET rows_used=(SELECT count(*) FROM probe_cache),bytes_used=(SELECT sum(charge_bytes) FROM probe_cache);
 UPDATE probe_library_quota q SET rows_used=(SELECT count(*) FROM probe_cache c WHERE c.library_id=q.library_id),bytes_used=(SELECT COALESCE(sum(charge_bytes),0) FROM probe_cache c WHERE c.library_id=q.library_id)`); err != nil {
		t.Fatal("derive probe counters", err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func coverItem(t *testing.T, f probeFixture, relative, metadata string) string {
	t.Helper()
	item := metadataItem(t, f.jobFixture)
	sidecarSource(t, f.jobFixture, item, relative)
	seedCoverProbe(t, f, relative, 1000, 7, coverFingerprint(relative), metadata)
	return item
}

func coverSyncLease(t *testing.T, f probeFixture, key string) domain.JobLease {
	t.Helper()
	if _, _, err := f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, key, domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal("submit catalog sync", err)
	}
	return syncLease(t, f.jobFixture, "cover-"+key)
}

func coverCandidates(t *testing.T, f probeFixture, l domain.JobLease) map[string]domain.EmbeddedCoverCandidate {
	t.Helper()
	out := map[string]domain.EmbeddedCoverCandidate{}
	after := ""
	for {
		page, err := f.s.NextEmbeddedCoverCandidates(f.ctx, l, after, 1)
		if err != nil {
			t.Fatal("candidates", err)
		}
		for _, c := range page.Candidates {
			out[c.ItemID] = c
		}
		if page.Next == "" {
			return out
		}
		after = page.Next
	}
}

func coverStored(c domain.EmbeddedCoverCandidate, seed string) domain.EmbeddedCoverResult {
	sum := sha256.Sum256([]byte(seed))
	return domain.EmbeddedCoverResult{Candidate: c, Outcome: domain.EmbeddedCoverStored, Content: &domain.ItemImageContent{SHA256: sum[:], Width: 16, Height: 16,
		Format: "png", Bytes: int64(len(seed)), FetchedAt: time.Now().UTC().Truncate(time.Microsecond)}}
}

func TestEmbeddedCoverCandidatesPriorityLockAndFingerprint(t *testing.T) {
	f := newProbeFixture(t)
	cover := coverItem(t, f, "Cover/Cover.mkv", coverTestMetadata)
	plain := coverItem(t, f, "Plain/Plain.mkv", coverTestPlainMetadata)
	local := coverItem(t, f, "Local/Local.mkv", coverTestMetadata)
	locked := coverItem(t, f, "Locked/Locked.mkv", coverTestMetadata)
	remote := coverItem(t, f, "Remote/Remote.mkv", coverTestMetadata)
	double := coverItem(t, f, "Double/Double.mkv", coverTestMetadata)
	sidecarSource(t, f.jobFixture, double, "Double/Double.Part2.mkv")
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path)
 VALUES($1::uuid,$2::uuid,'Primary',0,'local',$3::uuid,'Local/poster.jpg')`, local, f.registration.Library.ID, f.registration.RootID)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,locked)
 VALUES($1::uuid,$2::uuid,'Primary',0,'embedded',$3::uuid,'Locked/Locked.mkv',true)`, locked, f.registration.Library.ID, f.registration.RootID)
	// A remote reference without fetched content is not usable and does not block.
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,remote_url)
 VALUES($1::uuid,$2::uuid,'Primary',0,'remote','https://example.invalid/poster.jpg')`, remote, f.registration.Library.ID)
	l := coverSyncLease(t, f, "covers-1")

	got := coverCandidates(t, f, l)
	if len(got) != 2 || got[cover].ItemID != cover || got[remote].ItemID != remote {
		t.Fatalf("candidate set: %d", len(got))
	}
	c := got[cover]
	if c.StreamIndex != 2 || c.VideoIndex != 1 || c.Stamp.Size != 1000 || c.Stamp.ModifiedUnixNano != 7 || c.Stamp.Fingerprint != coverFingerprint("Cover/Cover.mkv") || c.RelativePath != "Cover/Cover.mkv" || c.RootPath == "" {
		t.Fatal("candidate binding", c.StreamIndex, c.VideoIndex)
	}
	for _, id := range []string{plain, local, locked, double} {
		if _, ok := got[id]; ok {
			t.Fatal("blocked or coverless item offered")
		}
	}

	// A stale stamp writes nothing.
	stale := c
	stale.Stamp.Fingerprint = coverFingerprint("other")
	if outcome, err := f.s.RecordEmbeddedCover(f.ctx, l, coverStored(stale, "png-bytes")); err != nil || outcome != domain.EmbeddedCoverChanged {
		t.Fatal("stale stamp", outcome, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM item_images WHERE source_kind='embedded' AND NOT locked`)+syncCount(t, f.jobFixture, `SELECT count(*) FROM item_embedded_cover_attempts`) != 0 {
		t.Fatal("stale stamp wrote a row")
	}
	outcome, err := f.s.RecordEmbeddedCover(f.ctx, l, coverStored(c, "png-bytes"))
	if err != nil || outcome != domain.EmbeddedCoverStored {
		t.Fatal("store", outcome, err)
	}
	var sum []byte
	var kind, rel string
	var size, mtime int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT content_sha256,source_kind,relative_path,source_size,source_mtime_unix_nano FROM item_images WHERE item_id=$1::uuid AND image_type='Primary'`, cover).Scan(&sum, &kind, &rel, &size, &mtime); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("png-bytes"))
	if string(sum) != string(want[:]) || kind != "embedded" || rel != "Cover/Cover.mkv" || size != 1000 || mtime != 7 {
		t.Fatal("embedded row")
	}
	// Recording the same result again is idempotent.
	if outcome, err = f.s.RecordEmbeddedCover(f.ctx, l, coverStored(c, "png-bytes")); err != nil || outcome != domain.EmbeddedCoverStored {
		t.Fatal("repeat", outcome, err)
	}
	// Remembered refusals and the stored picture stop the item from being offered.
	if outcome, err = f.s.RecordEmbeddedCover(f.ctx, l, domain.EmbeddedCoverResult{Candidate: got[remote], Outcome: domain.EmbeddedCoverTooLarge}); err != nil || outcome != domain.EmbeddedCoverTooLarge {
		t.Fatal("too large", outcome, err)
	}
	if again := coverCandidates(t, f, l); len(again) != 0 {
		t.Fatal("unchanged fingerprint offered again", len(again))
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM item_images WHERE item_id=$1::uuid AND source_kind='embedded'`, remote) != 0 {
		t.Fatal("refusal created an image row")
	}

	// A changed file (new probe stamp) is offered again.
	seedCoverProbe(t, f, "Cover/Cover.mkv", 1001, 8, coverFingerprint("changed"), coverTestMetadata)
	again := coverCandidates(t, f, l)
	if len(again) != 1 || again[cover].Stamp.Fingerprint != coverFingerprint("changed") {
		t.Fatal("changed file not offered", len(again))
	}
	// A higher-priority image that appears before recording wins.
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path)
 VALUES($1::uuid,$2::uuid,'Primary',0,'nfo',$3::uuid,'Cover/folder.jpg')`, cover, f.registration.Library.ID, f.registration.RootID)
	if outcome, err = f.s.RecordEmbeddedCover(f.ctx, l, coverStored(again[cover], "new-bytes")); err != nil || outcome != domain.EmbeddedCoverSkippedPriority {
		t.Fatal("priority", outcome, err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT content_sha256 FROM item_images WHERE item_id=$1::uuid AND source_kind='embedded'`, cover).Scan(&sum); err != nil || string(sum) != string(want[:]) {
		t.Fatal("skipped attempt replaced the stored picture")
	}
	// A locked row of any source wins as well, even over a lower priority.
	imageRepositoryExec(t, f.jobFixture, `DELETE FROM item_images WHERE item_id=$1::uuid AND source_kind='nfo'`, cover)
	imageRepositoryExec(t, f.jobFixture, `UPDATE item_images SET locked=true WHERE item_id=$1::uuid AND source_kind='embedded'`, cover)
	if again = coverCandidates(t, f, l); len(again) != 0 {
		t.Fatal("locked slot offered")
	}
	if outcome, err = f.s.RecordEmbeddedCover(f.ctx, l, coverStored(c, "png-bytes")); err != nil || outcome != domain.EmbeddedCoverChanged {
		t.Fatal("old stamp after change", outcome, err)
	}
	refreshed := c
	refreshed.Stamp.Size, refreshed.Stamp.ModifiedUnixNano, refreshed.Stamp.Fingerprint = 1001, 8, coverFingerprint("changed")
	if outcome, err = f.s.RecordEmbeddedCover(f.ctx, l, coverStored(refreshed, "new-bytes")); err != nil || outcome != domain.EmbeddedCoverSkippedLocked {
		t.Fatal("locked", outcome, err)
	}

	// Invalid input, a foreign lease and a non-catalog-sync lease are refused.
	if _, err := f.s.RecordEmbeddedCover(f.ctx, l, domain.EmbeddedCoverResult{Candidate: c, Outcome: domain.EmbeddedCoverStored}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("stored without content accepted", err)
	}
	foreign := l
	foreign.Generation++
	if _, err := f.s.NextEmbeddedCoverCandidates(f.ctx, foreign, "", 1); err == nil {
		t.Fatal("stale lease paged candidates")
	}
	if _, err := f.s.NextEmbeddedCoverCandidates(f.ctx, l, "", domain.EmbeddedCoverBatch+1); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unbounded page accepted", err)
	}
	if text := c.String() + c.GoString(); strings.Contains(text, "Cover") {
		t.Fatal("candidate diagnostics expose paths")
	}
}

func TestEmbeddedCoverMigrationDowngrade(t *testing.T) {
	f := newProbeFixture(t)
	item := coverItem(t, f, "Cover/Cover.mkv", coverTestMetadata)
	l := coverSyncLease(t, f, "covers-down")
	c := coverCandidates(t, f, l)[item]
	if outcome, err := f.s.RecordEmbeddedCover(f.ctx, l, coverStored(c, "png-bytes")); err != nil || outcome != domain.EmbeddedCoverStored {
		t.Fatal(outcome, err)
	}
	previous := migrationVersion(t, "embedded_covers") - 1
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	for err == nil && !dirty && version > previous {
		version, dirty, err = Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down")
	}
	if err != nil || dirty || version != previous {
		t.Fatal("downgrade", version, dirty, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM item_images WHERE source_kind='embedded'`) != 1 ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM pg_tables WHERE schemaname=current_schema() AND tablename='item_embedded_cover_attempts'`) != 0 {
		t.Fatal("downgrade must keep image rows and drop only the attempt memo")
	}
	if version, dirty, err = Migrate(f.ctx, f.s.Pool.Config().ConnString(), "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("upgrade", version, dirty, err)
	}
}
