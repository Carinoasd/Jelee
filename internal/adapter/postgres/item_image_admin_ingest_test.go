//go:build linux || windows

package postgres

import (
	"runtime"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// G40.10/G40.11 with scan ingestion: what the administrator API writes is
// manual and survives rescans; what it deletes is only a reference, so a
// library file that is still there is found again by the next scan.
func TestItemImageAdminChangesAndScanIngestion(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f := newJobFixtureWithin(t, 4*time.Minute)
	root := syncRootPath(t, f)
	const movie = "Heat (1995)/Heat (1995).mkv"
	for _, name := range []string{movie, "Heat (1995)/poster.jpg", "Heat (1995)/fanart.jpg"} {
		writeSidecarFile(t, root, name, []byte("file "+name))
	}
	before := imageChecksums(t, root)
	enableAutoSync(t, f)
	stop := startImageIngestWorker(t, f)
	defer stop()
	scan := func(key string) map[string]ingestedImage {
		t.Helper()
		f.submit(t, key)
		waitJobsIdle(t, f)
		return ingestedImages(t, f)
	}
	rows := scan("admin-scan-1")
	item := ingestItemAt(t, f, movie)
	poster, backdrop := movie+"|Primary/0/local", movie+"|Backdrop/0/local"
	if rows[poster].manual || rows[backdrop].manual {
		t.Fatal("scan rows are manual")
	}

	// An upload is a manual, locked row; the scan row stays as a fallback
	// and later scans leave both alone.
	change, err := f.s.UploadItemImage(f.ctx, f.a, itemImageUploadInput(item, "admin-upload"), true)
	if err != nil || !change.Image.Manual || !change.Image.Locked {
		t.Fatal("upload is not a manual locked row", err)
	}
	rows = scan("admin-scan-2")
	upload := movie + "|Primary/0/upload"
	if v := rows[upload]; !v.manual || !v.locked || rows[poster].id == "" || rows[poster].locked {
		t.Fatalf("rescan changed the upload or the scan row: %+v %+v", v, rows[poster])
	}
	if v, err := f.s.ResolveItemImage(f.ctx, f.a, item, "Primary", 0); err != nil || v.SourceKind != domain.ImageSourceUpload {
		t.Fatal("upload does not win after a rescan", err)
	}

	// Deleting a scan row removes only the reference: the file is still
	// there and named, so the next scan adds it again as a scan row.
	if _, err := f.s.DeleteItemImages(f.ctx, f.a, item, "Backdrop", 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := ingestedImages(t, f)[backdrop]; ok {
		t.Fatal("scan row not deleted")
	}
	rows = scan("admin-scan-3")
	if v, ok := rows[backdrop]; !ok || v.manual || v.locked {
		t.Fatalf("the next scan did not find the library file again: %+v", v)
	}

	// Deleting the upload leaves the scan row serving the slot; the stored
	// original and the library file are untouched.
	if change, err := f.s.DeleteItemImages(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceUpload); err != nil || len(change.Removed) != 1 || !change.Removed[0].Manual {
		t.Fatal("delete upload", err)
	}
	rows = scan("admin-scan-4")
	if _, ok := rows[upload]; ok {
		t.Fatal("a deleted upload came back")
	}
	if v, err := f.s.ResolveItemImage(f.ctx, f.a, item, "Primary", 0); err != nil || v.SourceKind != domain.ImageSourceLocal {
		t.Fatal("scan row does not serve after the upload is deleted", err)
	}
	after := imageChecksums(t, root)
	if len(after) != len(before) {
		t.Fatal("library image files changed in number")
	}
	for name, sum := range before {
		if after[name] != sum {
			t.Fatal("library image file changed", name)
		}
	}
}
