//go:build linux || windows

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

// startImageIngestWorker runs the production runner with catalog sync and
// the image ingestion pass, as runtime wires them.
func startImageIngestWorker(t *testing.T, f jobFixture) func() {
	t.Helper()
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval, opts.ScanConcurrency = 1, 100*time.Millisecond, 2
	opts.CatalogSync = &jobs.CatalogSyncOptions{Repository: f.s, Sidecars: f.s, Inspector: scan.SidecarInspector{}, Images: f.s}
	runner, err := jobs.New(f.s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}
}

type ingestedImage struct {
	id, ref        string
	locked, manual bool
	content        bool
	updated        time.Time
}

// ingestedImages maps "<item folder or video>|Type/index/kind" to its row.
// An item is named by its first media file or its registered folder.
func ingestedImages(t *testing.T, f jobFixture) map[string]ingestedImage {
	t.Helper()
	rows, err := f.s.Pool.Query(f.ctx, `SELECT COALESCE((SELECT m.relative_path FROM media_sources m WHERE m.item_id=g.item_id ORDER BY m.relative_path COLLATE "C" LIMIT 1),
 (SELECT d.relative_path FROM item_directory_sources d WHERE d.item_id=g.item_id)),g.image_type,g.image_index,g.source_kind,g.id::text,
 COALESCE(g.relative_path,g.remote_url),g.locked,g.manual,g.content_sha256 IS NOT NULL,g.updated_at FROM item_images g`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]ingestedImage{}
	for rows.Next() {
		var item, typ, kind string
		var index int
		var v ingestedImage
		if err = rows.Scan(&item, &typ, &index, &kind, &v.id, &v.ref, &v.locked, &v.manual, &v.content, &v.updated); err != nil {
			t.Fatal(err)
		}
		out[fmt.Sprintf("%s|%s/%d/%s", item, typ, index, kind)] = v
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func expectIngested(t *testing.T, label string, got map[string]ingestedImage, want map[string]string) {
	t.Helper()
	for key, ref := range want {
		if got[key].ref != ref {
			t.Errorf("%s: %s = %q, want %q", label, key, got[key].ref, ref)
		}
	}
	for key, v := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("%s: unexpected row %s = %q", label, key, v.ref)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
}

func ingestItemAt(t *testing.T, f jobFixture, relative string) string {
	t.Helper()
	var id string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT COALESCE((SELECT item_id FROM media_sources WHERE relative_path=$1),(SELECT item_id FROM item_directory_sources WHERE relative_path=$1))::text`, relative).Scan(&id); err != nil {
		t.Fatal("item at", relative, err)
	}
	return id
}

// seedNFOArt stores an art fact as an NFO import would: with the NFO origin
// naming the media file or folder source the NFO was read from.
func seedNFOArt(t *testing.T, f jobFixture, item, source, projection string, art []domain.NFOArtwork) {
	t.Helper()
	raw, err := json.Marshal(art)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("ab", 32)
	origin := &domain.NFOItemOrigin{SourceID: source, RootID: f.registration.RootID, Generation: 1, SHA256: digest, IdentityDigest: digest,
		Projection: projection, ReadAt: time.Now().UTC().Truncate(time.Microsecond)}
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if err = writeItemMetadataFact(f.ctx, tx, item, domain.ItemMetadataFact{Field: "art", Value: raw, Source: "nfo", NFOOrigin: origin}, time.Now().UTC()); err != nil {
		t.Fatal("seed art fact", err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func directorySourceOf(t *testing.T, f jobFixture, relative string) string {
	t.Helper()
	var id string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM item_directory_sources WHERE relative_path=$1`, relative).Scan(&id); err != nil {
		t.Fatal("directory source of", relative, err)
	}
	return id
}

// imageChecksums records every image file below root; ingestion must never
// change or remove one.
func imageChecksums(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !domain.ValidItemImageFileName(p) {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func catalogSyncJobs(t *testing.T, f jobFixture) int {
	t.Helper()
	return syncCount(t, f, `SELECT count(*) FROM jobs WHERE kind='catalog_sync' AND state='succeeded'`)
}

// G40.2 / G40.3: the catalog sync records the image files next to movies,
// series, seasons and episodes by their G40.2 names, and the NFO art of each
// item as NFO (inside the root) or remote (URL, never fetched) references.
// Rescans are idempotent, follow vanished files, and never touch locked or
// manual rows or any user file.
func TestCatalogSyncIngestsItemImages(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f := newJobFixtureWithin(t, 4*time.Minute)
	root := syncRootPath(t, f)
	files := []string{
		// A movie folder: <video>-poster beats poster, fanartN is a gallery.
		"Inception (2010)/Inception (2010).mkv",
		"Inception (2010)/poster.jpg", "Inception (2010)/Inception (2010)-poster.png", "Inception (2010)/fanart.jpg",
		"Inception (2010)/fanart1.jpg", "Inception (2010)/clearlogo.png", "Inception (2010)/custom-logo.png",
		"Inception (2010)/extrafanart/fanart2.jpg",
		// Two movies in one folder: shared names are not taken.
		"Collection/Alien (1979).mkv", "Collection/Aliens (1986).mkv",
		"Collection/poster.jpg", "Collection/fanart.jpg", "Collection/Alien (1979)-poster.jpg",
		// A movie in the root folder.
		"Heat (1995).mkv", "Heat (1995)-poster.jpg", "landscape.jpg", "heat-logo.png",
		// A series with season folders, season images in the series folder
		// and episode thumbs.
		"Breaking Bad (2008)/poster.jpg", "Breaking Bad (2008)/fanart.jpg",
		"Breaking Bad (2008)/season01-poster.jpg", "Breaking Bad (2008)/season02-poster.jpg",
		"Breaking Bad (2008)/season02-banner.jpg", "Breaking Bad (2008)/season-all-banner.jpg",
		"Breaking Bad (2008)/season-specials-poster.jpg", "Breaking Bad (2008)/season02-nfo.jpg",
		"Breaking Bad (2008)/Season 01/folder.jpg",
		"Breaking Bad (2008)/Season 01/Breaking Bad S01E01.mkv", "Breaking Bad (2008)/Season 01/Breaking Bad S01E01-thumb.jpg",
		"Breaking Bad (2008)/Season 01/Breaking Bad S01E02.mkv", "Breaking Bad (2008)/Season 01/thumbs/e02.jpg",
		"Breaking Bad (2008)/Season 01/Breaking Bad S01E03.mkv",
		"Breaking Bad (2008)/Season 02/Breaking Bad S02E01.mkv",
		"Breaking Bad (2008)/Specials/Breaking Bad S00E01.mkv",
	}
	for i, name := range files {
		writeSidecarFile(t, root, name, []byte(fmt.Sprintf("file-%d-%s", i, name)))
	}
	before := imageChecksums(t, root)
	enableAutoSync(t, f)
	stop := startImageIngestWorker(t, f)
	defer func() { stop() }()
	f.submit(t, "image-scan-1")
	waitJobsIdle(t, f)

	const (
		inception = "Inception (2010)/Inception (2010).mkv"
		heat      = "Heat (1995).mkv"
		show      = "Breaking Bad (2008)"
		season1   = "Breaking Bad (2008)/Season 01"
		season2   = "Breaking Bad (2008)/Season 02"
		specials  = "Breaking Bad (2008)/Specials"
		e01       = "Breaking Bad (2008)/Season 01/Breaking Bad S01E01.mkv"
		e02       = "Breaking Bad (2008)/Season 01/Breaking Bad S01E02.mkv"
		e03       = "Breaking Bad (2008)/Season 01/Breaking Bad S01E03.mkv"
	)
	local := map[string]string{
		inception + "|Primary/0/local":                "Inception (2010)/Inception (2010)-poster.png",
		inception + "|Backdrop/0/local":               "Inception (2010)/fanart.jpg",
		inception + "|Backdrop/1/local":               "Inception (2010)/fanart1.jpg",
		inception + "|ClearLogo/0/local":              "Inception (2010)/clearlogo.png",
		"Collection/Alien (1979).mkv|Primary/0/local": "Collection/Alien (1979)-poster.jpg",
		heat + "|Primary/0/local":                     "Heat (1995)-poster.jpg",
		heat + "|Landscape/0/local":                   "landscape.jpg",
		show + "|Primary/0/local":                     "Breaking Bad (2008)/poster.jpg",
		show + "|Backdrop/0/local":                    "Breaking Bad (2008)/fanart.jpg",
		season1 + "|Primary/0/local":                  "Breaking Bad (2008)/Season 01/folder.jpg",
		season1 + "|Banner/0/local":                   "Breaking Bad (2008)/season-all-banner.jpg",
		season2 + "|Primary/0/local":                  "Breaking Bad (2008)/season02-poster.jpg",
		season2 + "|Banner/0/local":                   "Breaking Bad (2008)/season02-banner.jpg",
		specials + "|Primary/0/local":                 "Breaking Bad (2008)/season-specials-poster.jpg",
		specials + "|Banner/0/local":                  "Breaking Bad (2008)/season-all-banner.jpg",
		e01 + "|Thumb/0/local":                        "Breaking Bad (2008)/Season 01/Breaking Bad S01E01-thumb.jpg",
	}
	first := ingestedImages(t, f)
	expectIngested(t, "first scan", first, local)
	for key, v := range first {
		if v.locked || v.manual || v.content {
			t.Fatalf("scan row %s locked=%t manual=%t content=%t", key, v.locked, v.manual, v.content)
		}
	}

	// NFO art as an NFO import stores it, a lock and two manual rows.
	seedNFOArt(t, f, ingestItemAt(t, f, inception), sourceOf(t, f, inception), domain.NFOItemMovieFieldsVersion, []domain.NFOArtwork{
		{Kind: "poster", Location: "https://images.invalid/inception.jpg"},
		{Kind: "fanart", Location: "extrafanart/fanart2.jpg"},
		{Kind: "fanart", Location: "https://images.invalid/inception-fanart.jpg"},
		{Kind: "clearlogo", Location: "../../escape.png"},
	})
	two := 2
	seedNFOArt(t, f, ingestItemAt(t, f, show), directorySourceOf(t, f, show), domain.NFOItemSeriesFieldsVersion, []domain.NFOArtwork{
		{Kind: "poster", Location: "https://images.invalid/series.jpg"},
		{Kind: "poster", Location: "season02-nfo.jpg", Season: &two},
	})
	seedNFOArt(t, f, ingestItemAt(t, f, e02), sourceOf(t, f, e02), domain.NFOItemEpisodeFieldsVersion, []domain.NFOArtwork{{Kind: "poster", Location: "thumbs/e02.jpg"}})
	seedNFOArt(t, f, ingestItemAt(t, f, e03), sourceOf(t, f, e03), domain.NFOItemEpisodeFieldsVersion, []domain.NFOArtwork{{Kind: "thumb", Location: "https://images.invalid/e03.jpg"}})
	inceptionItem, heatItem := ingestItemAt(t, f, inception), ingestItemAt(t, f, heat)
	if _, err := f.s.SetItemImageLock(f.ctx, f.a, inceptionItem, "Backdrop", 0, domain.ImageSourceLocal, true); err != nil {
		t.Fatal("lock", err)
	}
	manual := domain.ItemImageInput{ItemID: inceptionItem, Type: "ClearLogo", SourceKind: domain.ImageSourceLocal, RootID: f.registration.RootID,
		RelativePath: "Inception (2010)/custom-logo.png", Manual: true}
	if _, err := f.s.UpsertItemImage(f.ctx, f.a, manual); err != nil {
		t.Fatal("manual replace", err)
	}
	manual = domain.ItemImageInput{ItemID: heatItem, Type: "Logo", SourceKind: domain.ImageSourceLocal, RootID: f.registration.RootID, RelativePath: "heat-logo.png", Manual: true}
	if _, err := f.s.UpsertItemImage(f.ctx, f.a, manual); err != nil {
		t.Fatal("manual add", err)
	}

	syncs := catalogSyncJobs(t, f)
	f.submit(t, "image-scan-2")
	waitJobsIdle(t, f)
	if catalogSyncJobs(t, f) != syncs+1 {
		t.Fatal("the rescan did not run a catalog sync")
	}
	want := map[string]string{
		inception + "|Primary/0/remote":  "https://images.invalid/inception.jpg",
		inception + "|Backdrop/0/nfo":    "Inception (2010)/extrafanart/fanart2.jpg",
		inception + "|Backdrop/1/remote": "https://images.invalid/inception-fanart.jpg",
		inception + "|ClearLogo/0/local": "Inception (2010)/custom-logo.png",
		heat + "|Logo/0/local":           "heat-logo.png",
		show + "|Primary/0/remote":       "https://images.invalid/series.jpg",
		season2 + "|Primary/0/nfo":       "Breaking Bad (2008)/season02-nfo.jpg",
		e02 + "|Thumb/0/nfo":             "Breaking Bad (2008)/Season 01/thumbs/e02.jpg",
		e03 + "|Thumb/0/remote":          "https://images.invalid/e03.jpg",
	}
	for key, ref := range local {
		if _, ok := want[key]; !ok {
			want[key] = ref
		}
	}
	second := ingestedImages(t, f)
	expectIngested(t, "nfo art", second, want)
	for _, key := range []string{inception + "|Primary/0/remote", inception + "|Backdrop/1/remote", show + "|Primary/0/remote", e03 + "|Thumb/0/remote"} {
		if second[key].content {
			t.Fatal("a URL was fetched during the sync", key)
		}
	}
	if v := second[inception+"|ClearLogo/0/local"]; !v.manual || v.locked {
		t.Fatalf("manual row replaced by the scan %+v", v)
	}
	if !second[inception+"|Backdrop/0/local"].locked {
		t.Fatal("lock lost")
	}

	// An unchanged rescan writes nothing.
	f.submit(t, "image-scan-3")
	waitJobsIdle(t, f)
	third := ingestedImages(t, f)
	for key, v := range second {
		if third[key] != v {
			t.Fatalf("unchanged rescan rewrote %s\nbefore %+v\nafter  %+v", key, v, third[key])
		}
	}
	if len(third) != len(second) {
		t.Fatal("unchanged rescan changed the row count", len(second), len(third))
	}

	// Files vanish: scan rows go, locked and manual rows stay, a season
	// banner falls back to season-all, an NFO file reference goes.
	removeSidecarFile(t, root, "Inception (2010)/fanart.jpg", "Inception (2010)/fanart1.jpg", "Breaking Bad (2008)/season02-banner.jpg",
		"Breaking Bad (2008)/Season 01/thumbs/e02.jpg", "heat-logo.png")
	f.submit(t, "image-scan-4")
	waitJobsIdle(t, f)
	fourth := ingestedImages(t, f)
	delete(want, inception+"|Backdrop/1/local")
	delete(want, e02+"|Thumb/0/nfo")
	want[season2+"|Banner/0/local"] = "Breaking Bad (2008)/season-all-banner.jpg"
	expectIngested(t, "vanished files", fourth, want)
	if v := fourth[inception+"|Backdrop/0/local"]; !v.locked || v != third[inception+"|Backdrop/0/local"] {
		t.Fatalf("locked row changed %+v", v)
	}
	if v := fourth[heat+"|Logo/0/local"]; !v.manual || v != third[heat+"|Logo/0/local"] {
		t.Fatalf("manual row changed %+v", v)
	}
	if fourth[season2+"|Banner/0/local"].id != third[season2+"|Banner/0/local"].id {
		t.Fatal("the season banner row was replaced instead of updated")
	}

	// No user file was changed or removed besides the ones the test removed.
	after := imageChecksums(t, root)
	for _, gone := range []string{"Inception (2010)/fanart.jpg", "Inception (2010)/fanart1.jpg", "Breaking Bad (2008)/season02-banner.jpg",
		"Breaking Bad (2008)/Season 01/thumbs/e02.jpg", "heat-logo.png"} {
		delete(before, gone)
	}
	if len(after) != len(before) {
		t.Fatal("image files changed in number", len(before), len(after))
	}
	for name, sum := range before {
		if after[name] != sum {
			t.Fatal("image file changed", name)
		}
	}

	// The fenced page API: only a finished sync ingests, bounds are
	// checked, and a replaced baseline makes the page stale.
	stop()
	if _, _, err := f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, "image-direct", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	l := syncLease(t, f, "image-direct")
	if _, err := f.s.IngestItemImages(f.ctx, l, "", domain.ItemImageIngestBatch); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("ingestion before the sync finished", err)
	}
	for _, bad := range []struct {
		after string
		limit int
	}{{"", 0}, {"", domain.ItemImageIngestBatch + 1}, {"not-an-id", 1}} {
		if _, err := f.s.IngestItemImages(f.ctx, l, bad.after, bad.limit); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("invalid page accepted", bad, err)
		}
	}
	if !driveSync(t, f, l, 100) {
		t.Fatal("sync did not finish")
	}
	items, after2 := 0, ""
	for {
		page, err := f.s.IngestItemImages(f.ctx, l, after2, 3)
		if err != nil {
			t.Fatal(err)
		}
		if page.Created+page.Updated+page.Removed != 0 || page.Stale {
			t.Fatalf("an unchanged library was rewritten %+v", page)
		}
		items += page.Items
		if page.Next == "" {
			break
		}
		after2 = page.Next
	}
	if want := syncCount(t, f, `SELECT count(*) FROM items WHERE library_id=$1::uuid`, f.registration.Library.ID); items != want {
		t.Fatal("pages missed items", items, want)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid`, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	if page, err := f.s.IngestItemImages(f.ctx, l, "", 3); err != nil || !page.Stale || page.Items != 0 || page.Next != "" {
		t.Fatalf("stale baseline page %+v %v", page, err)
	}
	if got := ingestedImages(t, f); len(got) != len(fourth) {
		t.Fatal("a stale page wrote", len(fourth), len(got))
	}
}
