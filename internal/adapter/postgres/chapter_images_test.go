package postgres

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// chapterTestMetadata is an MP4 with chapters, a main video, a cover
// (container index 1) and the chapter picture track (index 3).
const chapterTestMetadata = `{"format":{"names":["mov","mp4","m4a","3gp","3g2","mj2"]},"streams":[
 {"index":0,"kind":"video","codec":"h264","video":{"width":320,"height":180}},
 {"index":1,"kind":"video","codec":"png","attachedPic":true,"video":{"width":16,"height":16}},
 {"index":2,"kind":"audio","codec":"aac","audio":{}},
 {"index":3,"kind":"video","codec":"mjpeg","attachedPic":true,"video":{"width":64,"height":36}}],
 "chapters":[{"id":0,"startMicros":0,"endMicros":2000000},{"id":1,"startMicros":2000000,"endMicros":4000000},{"id":2,"startMicros":4000000,"endMicros":6000000}]}`

// Without chapters, or in Matroska, a picture stream is only a cover.
const chapterTestNoChapters = `{"format":{"names":["mov","mp4"]},"streams":[{"index":0,"kind":"video","codec":"mjpeg","attachedPic":true,"video":{}}],"chapters":[]}`
const chapterTestMatroska = `{"format":{"names":["matroska","webm"]},"streams":[{"index":0,"kind":"video","codec":"mjpeg","attachedPic":true,"video":{}}],"chapters":[{"id":1,"startMicros":0}]}`

func chapterCandidates(t *testing.T, f probeFixture, l domain.JobLease) map[string]domain.ChapterImageCandidate {
	t.Helper()
	out := map[string]domain.ChapterImageCandidate{}
	after := ""
	for {
		page, err := f.s.NextChapterImageCandidates(f.ctx, l, after, 1)
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

func chapterStored(c domain.ChapterImageCandidate, seed string, indexes ...int) domain.ChapterImageResult {
	result := domain.ChapterImageResult{Candidate: c, Outcome: domain.ChapterImageStored}
	for _, index := range indexes {
		sum := sha256.Sum256([]byte(fmt.Sprint(seed, index)))
		result.Images = append(result.Images, domain.ChapterImageContent{Index: index, Content: domain.ItemImageContent{SHA256: sum[:], Width: 64, Height: 36,
			Format: "jpeg", Bytes: 100, FetchedAt: time.Now().UTC().Truncate(time.Microsecond)}})
	}
	return result
}

func chapterRows(t *testing.T, f probeFixture, item string) map[int]string {
	t.Helper()
	rows, err := f.s.Pool.Query(f.ctx, `SELECT image_index,source_kind||':'||encode(content_sha256,'hex') FROM item_images WHERE item_id=$1::uuid AND image_type='Chapter' AND content_sha256 IS NOT NULL ORDER BY image_index,source_kind`, item)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var index int
		var value string
		if err := rows.Scan(&index, &value); err != nil {
			t.Fatal(err)
		}
		out[index] += value + ";"
	}
	return out
}

func chapterDigest(seed string, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(seed, index)))
	return fmt.Sprintf("embedded:%x;", sum)
}

func TestChapterImageCandidatesLocksManualAndStaleRows(t *testing.T) {
	f := newProbeFixture(t)
	film := coverItem(t, f, "Film/Film.mp4", chapterTestMetadata)
	flat := coverItem(t, f, "Flat/Flat.mp4", chapterTestNoChapters)
	mkv := coverItem(t, f, "Mkv/Mkv.mkv", chapterTestMatroska)
	plain := coverItem(t, f, "Plain/Plain.mp4", coverTestPlainMetadata)
	double := coverItem(t, f, "Double/Double.mp4", chapterTestMetadata)
	sidecarSource(t, f.jobFixture, double, "Double/Double.Part2.mp4")
	l := coverSyncLease(t, f, "chapters-1")

	got := chapterCandidates(t, f, l)
	if len(got) != 1 {
		t.Fatalf("candidate set: %d", len(got))
	}
	c := got[film]
	if fmt.Sprint(c.Streams) != "[{1 1} {3 2}]" || c.Stamp.Size != 1000 || c.Stamp.Fingerprint != coverFingerprint("Film/Film.mp4") || c.RelativePath != "Film/Film.mp4" {
		t.Fatal("candidate binding", c.Streams)
	}
	for _, id := range []string{flat, mkv, plain, double} {
		if _, ok := got[id]; ok {
			t.Fatal("chapterless, Matroska, pictureless or multi-file item offered")
		}
	}
	// A locked row in chapter 1 and a manual one in chapter 2 must survive.
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,locked,manual)
 VALUES($1::uuid,$2::uuid,'Chapter',1,'local',$3::uuid,'Film/chapter1.jpg',true,true)`, film, f.registration.Library.ID, f.registration.RootID)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,manual,content_sha256,format,byte_size,fetched_at)
 VALUES($1::uuid,$2::uuid,'Chapter',2,'embedded',$3::uuid,'Film/Film.mp4',true,decode(repeat('ab',32),'hex'),'jpeg',5,now())`, film, f.registration.Library.ID, f.registration.RootID)

	stale := c
	stale.Stamp.Fingerprint = coverFingerprint("other")
	if record, err := f.s.RecordChapterImages(f.ctx, l, chapterStored(stale, "v1", 0, 1, 2)); err != nil || record.Outcome != domain.ChapterImageChanged {
		t.Fatal("stale stamp", record, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM item_chapter_image_attempts`) != 0 || len(chapterRows(t, f, film)) != 1 {
		t.Fatal("stale stamp wrote a row")
	}
	record, err := f.s.RecordChapterImages(f.ctx, l, chapterStored(c, "v1", 0, 1, 2))
	if err != nil || record != (domain.ChapterImageRecord{Outcome: domain.ChapterImageStored, Stored: 1, Skipped: 2}) {
		t.Fatal("store", record, err)
	}
	rows := chapterRows(t, f, film)
	if rows[0] != chapterDigest("v1", 0) || rows[2] != "embedded:"+repeatHex("ab", 32)+";" || len(rows) != 2 {
		t.Fatalf("chapter rows %v", rows)
	}
	var kind, rel string
	var size, mtime int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT source_kind,relative_path,source_size,source_mtime_unix_nano FROM item_images WHERE item_id=$1::uuid AND image_type='Chapter' AND image_index=0`, film).Scan(&kind, &rel, &size, &mtime); err != nil ||
		kind != "embedded" || rel != "Film/Film.mp4" || size != 1000 || mtime != 7 {
		t.Fatal("embedded chapter row", err)
	}
	if record, err = f.s.RecordChapterImages(f.ctx, l, chapterStored(c, "v1", 0, 1, 2)); err != nil || record.Stored != 1 || record.Removed != 0 {
		t.Fatal("repeat is not idempotent", record, err)
	}
	if again := chapterCandidates(t, f, l); len(again) != 0 {
		t.Fatal("unchanged fingerprint offered again", len(again))
	}

	// The file changes and now has pictures for chapters 3 and 4 only: the
	// old embedded chapter 0 is removed, the manual and locked rows stay.
	seedCoverProbe(t, f, "Film/Film.mp4", 1001, 8, coverFingerprint("changed"), chapterTestMetadata)
	again := chapterCandidates(t, f, l)
	if len(again) != 1 {
		t.Fatal("changed file not offered")
	}
	if record, err = f.s.RecordChapterImages(f.ctx, l, chapterStored(again[film], "v2", 3, 4)); err != nil || record != (domain.ChapterImageRecord{Outcome: domain.ChapterImageStored, Stored: 2, Removed: 1}) {
		t.Fatal("changed file", record, err)
	}
	rows = chapterRows(t, f, film)
	if _, ok := rows[0]; ok || rows[3] != chapterDigest("v2", 3) || rows[4] != chapterDigest("v2", 4) || rows[2] == "" ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM item_images WHERE item_id=$1::uuid AND image_type='Chapter' AND image_index=1 AND locked`, film) != 1 {
		t.Fatalf("after change %v", rows)
	}
	// A higher-priority usable source in chapter 4 makes the embedded row go.
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path)
 VALUES($1::uuid,$2::uuid,'Chapter',4,'nfo',$3::uuid,'Film/chapter4.jpg')`, film, f.registration.Library.ID, f.registration.RootID)
	seedCoverProbe(t, f, "Film/Film.mp4", 1002, 9, coverFingerprint("changed-again"), chapterTestMetadata)
	again = chapterCandidates(t, f, l)
	if record, err = f.s.RecordChapterImages(f.ctx, l, chapterStored(again[film], "v3", 3, 4)); err != nil || record != (domain.ChapterImageRecord{Outcome: domain.ChapterImageStored, Stored: 1, Skipped: 1, Removed: 1}) {
		t.Fatal("priority", record, err)
	}
	// A deterministic refusal removes every unlocked, non-manual embedded
	// chapter row and is remembered.
	seedCoverProbe(t, f, "Film/Film.mp4", 1003, 10, coverFingerprint("no-pictures"), chapterTestMetadata)
	again = chapterCandidates(t, f, l)
	if record, err = f.s.RecordChapterImages(f.ctx, l, domain.ChapterImageResult{Candidate: again[film], Outcome: domain.ChapterImageAbsent}); err != nil ||
		record != (domain.ChapterImageRecord{Outcome: domain.ChapterImageAbsent, Removed: 1}) {
		t.Fatal("absent", record, err)
	}
	if rows = chapterRows(t, f, film); len(rows) != 1 || rows[2] == "" ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM item_chapter_image_attempts WHERE item_id=$1::uuid AND outcome='absent' AND image_count=0`, film) != 1 {
		t.Fatalf("after refusal %v", rows)
	}
	if len(chapterCandidates(t, f, l)) != 0 {
		t.Fatal("remembered refusal offered again")
	}

	if _, err := f.s.RecordChapterImages(f.ctx, l, domain.ChapterImageResult{Candidate: c, Outcome: domain.ChapterImageStored}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("stored without pictures accepted", err)
	}
	foreign := l
	foreign.Generation++
	if _, err := f.s.NextChapterImageCandidates(f.ctx, foreign, "", 1); err == nil {
		t.Fatal("stale lease paged candidates")
	}
	if _, err := f.s.RecordChapterImages(f.ctx, foreign, chapterStored(c, "v1", 0)); err == nil {
		t.Fatal("stale lease recorded")
	}
	if _, err := f.s.NextChapterImageCandidates(f.ctx, l, "", domain.ChapterImageBatch+1); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unbounded page accepted", err)
	}
}

func repeatHex(pair string, n int) string {
	out := ""
	for range n {
		out += pair
	}
	return out
}

func TestChapterImageMigrationDowngrade(t *testing.T) {
	f := newProbeFixture(t)
	item := coverItem(t, f, "Film/Film.mp4", chapterTestMetadata)
	l := coverSyncLease(t, f, "chapters-down")
	c := chapterCandidates(t, f, l)[item]
	if record, err := f.s.RecordChapterImages(f.ctx, l, chapterStored(c, "v1", 0, 1)); err != nil || record.Stored != 2 {
		t.Fatal(record, err)
	}
	previous := migrationVersion(t, "chapter_images") - 1
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	for err == nil && !dirty && version > previous {
		version, dirty, err = Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down")
	}
	if err != nil || dirty || version > previous {
		t.Fatal("downgrade", version, dirty, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM item_images WHERE image_type='Chapter' AND source_kind='embedded'`) != 2 ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM pg_tables WHERE schemaname=current_schema() AND tablename='item_chapter_image_attempts'`) != 0 {
		t.Fatal("downgrade must keep image rows and drop only the attempt memo")
	}
	if version, dirty, err = Migrate(f.ctx, f.s.Pool.Config().ConnString(), "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("upgrade", version, dirty, err)
	}
}
