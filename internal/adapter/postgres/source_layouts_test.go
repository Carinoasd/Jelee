//go:build linux || windows

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

// startLayoutSyncWorker runs the production runner with catalog sync and the
// container layout pass, as runtime wires them.
func startLayoutSyncWorker(t *testing.T, f jobFixture) func() {
	t.Helper()
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval, opts.ScanConcurrency = 1, 100*time.Millisecond, 2
	opts.CatalogSync = &jobs.CatalogSyncOptions{Repository: f.s, Layouts: f.s, LayoutInspector: scan.LayoutInspector{}}
	ignoreScanner := scan.NewIgnoreScanner()
	opts.Ignore = &jobs.IgnoreOptions{Repository: f.s, Scanner: ignoreScanner, Observer: ignoreScanner}
	runner, err := jobs.New(f.s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}
}

func layoutBox(kind string, payload int) []byte {
	out := make([]byte, 8+payload)
	binary.BigEndian.PutUint32(out, uint32(8+payload))
	copy(out[4:], kind)
	return out
}

// seeklessMatroska is an EBML header and a segment holding a Void element
// and one Cluster, without any Cues.
func seeklessMatroska() []byte {
	header := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x80 | 11, 0x42, 0x82, 0x80 | 8}
	header = append(header, "matroska"...)
	body := append([]byte{0xEC, 0x82, 0, 0}, 0x1F, 0x43, 0xB6, 0x75, 0x80|8)
	body = append(body, make([]byte, 8)...)
	return append(append(header, 0x18, 0x53, 0x80, 0x67, 0x80|byte(len(body))), body...)
}

type storedLayout struct {
	layout, version      string
	offset, length, size int64
	checked              time.Time
}

func storedLayouts(t *testing.T, f jobFixture) map[string]storedLayout {
	t.Helper()
	rows, err := f.s.Pool.Query(f.ctx, `SELECT m.relative_path,l.layout,l.reader_version,l.index_offset,l.index_length,l.size,l.checked_at
 FROM media_source_layouts l JOIN media_sources m ON m.id=l.source_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]storedLayout{}
	for rows.Next() {
		var path string
		var v storedLayout
		if err = rows.Scan(&path, &v.layout, &v.version, &v.offset, &v.length, &v.size, &v.checked); err != nil {
			t.Fatal(err)
		}
		out[path] = v
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func itemLayout(t *testing.T, f jobFixture, actor domain.Actor, source string) (domain.ContainerLayout, []string) {
	t.Helper()
	var item string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT item_id::text FROM media_sources WHERE id=$1::uuid`, source).Scan(&item); err != nil {
		t.Fatal(err)
	}
	records, err := f.s.ListItemSources(f.ctx, actor, item)
	if err != nil || len(records) != 1 {
		t.Fatal("item sources", len(records), err)
	}
	built := domain.BuildPlaybackSource(records[0])
	return built.Layout, built.LayoutHints
}

// G10.6 / G10.10: the catalog sync reads each MP4/MOV and Matroska source's
// layout once per file stamp, file information reports it with advice, and
// delivery keeps the original bytes while using the recorded index only as
// read-ahead advice. Nothing writes to the media files.
func TestCatalogSyncRecordsSourceLayouts(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f := newJobFixture(t)
	root := syncRootPath(t, f)
	ftyp := layoutBox("ftyp", 16)
	late := bytes.Join([][]byte{ftyp, layoutBox("mdat", 5000), layoutBox("moov", 300)}, nil)
	files := map[string][]byte{
		"Faststart (2001)/Faststart (2001).mp4": bytes.Join([][]byte{ftyp, layoutBox("moov", 300), layoutBox("mdat", 5000)}, nil),
		"Late (2002)/Late (2002).mp4":           late,
		"Seekless (2003)/Seekless (2003).mkv":   seeklessMatroska(),
		"Broken (2004)/Broken (2004).mov":       []byte("not a quicktime file"),
		"Old (2005)/Old (2005).avi":             []byte("RIFF....AVI "),
	}
	digests := map[string][32]byte{}
	for name, content := range files {
		writeSidecarFile(t, root, name, content)
		digests[name] = sha256.Sum256(content)
	}
	enableAutoSync(t, f)
	stop := startLayoutSyncWorker(t, f)
	defer func() { stop() }()
	f.submit(t, "layout-scan-1")
	waitJobsIdle(t, f)

	layouts := storedLayouts(t, f)
	if len(layouts) != 4 {
		t.Fatalf("layouts %v", layouts)
	}
	want := map[string]storedLayout{
		"Faststart (2001)/Faststart (2001).mp4": {layout: "moov_before_mdat", offset: 24, length: 308},
		"Late (2002)/Late (2002).mp4":           {layout: "moov_at_end", offset: 24 + 5008, length: 308},
		"Seekless (2003)/Seekless (2003).mkv":   {layout: "no_cues"},
		"Broken (2004)/Broken (2004).mov":       {layout: "unknown"},
	}
	for path, w := range want {
		got := layouts[path]
		if got.layout != w.layout || got.offset != w.offset || got.length != w.length || got.version != domain.SourceLayoutVersion || got.size != int64(len(files[path])) {
			t.Fatalf("%s: %+v", path, got)
		}
	}

	principal := trackPrincipal(t, f, "layout-reader", access.ClientNative, true)
	actor := domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID}
	lateSource := sourceOf(t, f, "Late (2002)/Late (2002).mp4")
	if layout, hints := itemLayout(t, f, actor, lateSource); layout != domain.LayoutMoovAtEnd || len(hints) != 1 || hints[0] != domain.LayoutHintFaststart {
		t.Fatal("late layout", layout, hints)
	}
	if layout, hints := itemLayout(t, f, actor, sourceOf(t, f, "Seekless (2003)/Seekless (2003).mkv")); layout != domain.LayoutNoCues || len(hints) != 1 || hints[0] != domain.LayoutHintNoSeekIndex {
		t.Fatal("seekless layout", layout, hints)
	}
	if layout, hints := itemLayout(t, f, actor, sourceOf(t, f, "Old (2005)/Old (2005).avi")); layout != "" || hints != nil {
		t.Fatal("AVI layout", layout, hints)
	}

	// Delivery resolves the recorded index with its stamp and sends the
	// original bytes.
	resolved, err := f.s.Resolve(f.ctx, principal, lateSource)
	info, statErr := os.Stat(filepath.Join(root, "Late (2002)", "Late (2002).mp4"))
	if err != nil || statErr != nil || resolved.SeekIndex != (media.SeekIndex{Offset: 24 + 5008, Length: 308, Size: int64(len(late)), ModifiedUnixNano: info.ModTime().UnixNano()}) {
		t.Fatalf("resolved index %+v %v %v", resolved.SeekIndex, err, statErr)
	}
	handler, err := media.NewHandler(f.s, media.Options{MaxConcurrent: 2, WriteTimeout: 5 * time.Second, PrefetchBytes: 64 << 10,
		WriteError: func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusInternalServerError) }})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/stream", nil)
	r = r.WithContext(access.WithPrincipal(r.Context(), principal))
	w := httptest.NewRecorder()
	handler.ServeSource(w, r, lateSource)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), late) {
		t.Fatal("delivery changed the bytes", w.Code, w.Body.Len())
	}

	// An unchanged rescan reads nothing again.
	f.submit(t, "layout-scan-2")
	waitJobsIdle(t, f)
	again := storedLayouts(t, f)
	for path, v := range layouts {
		if again[path] != v {
			t.Fatal("unchanged rescan rewrote", path)
		}
	}

	// The administrator remuxes the late file; until the next sync the old
	// layout is no longer reported, after it the new one is.
	fast := bytes.Join([][]byte{ftyp, layoutBox("moov", 300), layoutBox("mdat", 5000)}, nil)
	full := filepath.Join(root, "Late (2002)", "Late (2002).mp4")
	if err = os.WriteFile(full, fast, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE catalog_scan_sources SET size=size+1 WHERE source_id=$1::uuid`, lateSource); err != nil {
		t.Fatal(err)
	}
	if layout, _ := itemLayout(t, f, actor, lateSource); layout != "" {
		t.Fatal("layout of another stamp reported", layout)
	}
	if resolved, err = f.s.Resolve(f.ctx, principal, lateSource); err != nil || resolved.SeekIndex.Size != int64(len(late)) {
		t.Fatal("resolve must return the stamp so delivery can reject it", resolved.SeekIndex, err)
	}
	stamp := time.Unix(1700000500, 0)
	if err = os.Chtimes(full, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	// An older reader version is read again as well.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE media_source_layouts SET reader_version='layout-v0' WHERE source_id=$1::uuid`, sourceOf(t, f, "Seekless (2003)/Seekless (2003).mkv")); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "layout-scan-3")
	waitJobsIdle(t, f)
	changed := storedLayouts(t, f)
	if v := changed["Late (2002)/Late (2002).mp4"]; v.layout != "moov_before_mdat" || v.size != int64(len(fast)) || v.offset != 24 {
		t.Fatalf("remuxed file %+v", v)
	}
	if v := changed["Seekless (2003)/Seekless (2003).mkv"]; v.version != domain.SourceLayoutVersion || !v.checked.After(layouts["Seekless (2003)/Seekless (2003).mkv"].checked) {
		t.Fatalf("old reader version kept %+v", v)
	}
	if changed["Faststart (2001)/Faststart (2001).mp4"] != layouts["Faststart (2001)/Faststart (2001).mp4"] {
		t.Fatal("untouched source rewritten")
	}
	if layout, hints := itemLayout(t, f, actor, lateSource); layout != domain.LayoutMoovBeforeMdat || hints != nil {
		t.Fatal("remuxed layout", layout, hints)
	}

	// Only the administrator's own remux changed a file.
	for name, digest := range digests {
		if name == "Late (2002)/Late (2002).mp4" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || sha256.Sum256(data) != digest {
			t.Fatal("layout reading changed", name)
		}
	}

	// Rows go with their source.
	if _, err = f.s.Pool.Exec(f.ctx, `DELETE FROM items WHERE id=(SELECT item_id FROM media_sources WHERE id=$1::uuid)`, lateSource); err != nil {
		t.Fatal(err)
	}
	if n := syncCount(t, f, `SELECT count(*) FROM media_source_layouts WHERE source_id=$1::uuid`, lateSource); n != 0 {
		t.Fatal("layout outlived its source", n)
	}
}

func TestSourceLayoutRepositoryRefusesInvalidCalls(t *testing.T) {
	f := newJobFixture(t)
	lease := domain.JobLease{}
	if _, err := f.s.NextSourceLayoutInspections(f.ctx, lease, "", 1); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid lease accepted", err)
	}
	if _, err := f.s.RecordSourceLayouts(f.ctx, lease, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid lease accepted", err)
	}
	// The table's checks mirror ValidSourceLayoutInspection.
	for name, statement := range map[string]string{
		"layout":         `INSERT INTO media_source_layouts(source_id,size,modified_unix_nano,reader_version,layout) VALUES(gen_random_uuid(),1,1,'v','faststart')`,
		"past the end":   `INSERT INTO media_source_layouts(source_id,size,modified_unix_nano,reader_version,layout,index_offset,index_length) VALUES(gen_random_uuid(),10,1,'v','moov_at_end',5,6)`,
		"offset only":    `INSERT INTO media_source_layouts(source_id,size,modified_unix_nano,reader_version,layout,index_offset) VALUES(gen_random_uuid(),10,1,'v','moov_at_end',5)`,
		"unknown source": `INSERT INTO media_source_layouts(source_id,size,modified_unix_nano,reader_version,layout) VALUES(gen_random_uuid(),10,1,'v','unknown')`,
	} {
		if _, err := f.s.Pool.Exec(f.ctx, statement); err == nil {
			t.Error(name, "accepted")
		}
	}
}

// The layout cache holds no administrator decision: the downgrade drops it
// and the next catalog sync rebuilds it after an upgrade.
func TestSourceLayoutMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	jobMetricMigration(t, f, "down", downgradeAboveMigration(t, f, "media_source_layouts")-1)
	var removed bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('media_source_layouts') IS NULL`).Scan(&removed); err != nil || !removed {
		t.Fatal("downgrade left the layout table", err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("store not ready after round trip", err)
	}
	if n := syncCount(t, f, `SELECT count(*) FROM media_source_layouts`); n != 0 {
		t.Fatal("rows after round trip", n)
	}
}
