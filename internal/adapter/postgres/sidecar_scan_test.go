//go:build linux || windows

package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"golang.org/x/text/encoding/japanese"
)

// startSidecarSyncWorker runs the production runner with catalog sync, the
// sidecar inspection pass and ignore-mode scans, as runtime wires them.
func startSidecarSyncWorker(t *testing.T, f jobFixture) func() {
	t.Helper()
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval, opts.ScanConcurrency = 1, 100*time.Millisecond, 2
	opts.CatalogSync = &jobs.CatalogSyncOptions{Repository: f.s, Sidecars: f.s, Inspector: scan.SidecarInspector{}}
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

func writeSidecarFile(t *testing.T, root, name string, content []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 0)
	if err := os.Chtimes(full, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func removeSidecarFile(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
}

type scannedTrack struct {
	id, video, kind, format, language, title, charset string
	forced, sdh, commentary                           bool
	fingerprint                                       int
	updated                                           time.Time
}

// scannedTracks maps a sidecar path to its row and the path of its video.
func scannedTracks(t *testing.T, f jobFixture) map[string]scannedTrack {
	t.Helper()
	rows, err := f.s.Pool.Query(f.ctx, `SELECT t.relative_path,t.id::text,m.relative_path,t.kind,t.format,COALESCE(t.language,''),COALESCE(t.title,''),
 COALESCE(t.charset,''),t.forced,t.sdh,t.commentary,COALESCE(octet_length(t.fingerprint),0),t.updated_at
 FROM media_sidecar_tracks t JOIN media_sources m ON m.id=t.source_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]scannedTrack{}
	for rows.Next() {
		var path string
		var v scannedTrack
		if err = rows.Scan(&path, &v.id, &v.video, &v.kind, &v.format, &v.language, &v.title, &v.charset, &v.forced, &v.sdh, &v.commentary, &v.fingerprint, &v.updated); err != nil {
			t.Fatal(err)
		}
		out[path] = v
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func sidecarAudits(t *testing.T, f jobFixture) []string {
	t.Helper()
	rows, err := f.s.Pool.Query(f.ctx, `SELECT m.relative_path||' '||l.after_state::text FROM audit_logs l JOIN media_sources m ON m.id=l.target_id WHERE l.event='media.sidecars_changed' ORDER BY l.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func sidecarRoute(t *testing.T, f jobFixture, principal access.Principal, source string, kind media.TrackKind, track string) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := media.NewHandler(f.s, media.Options{MaxConcurrent: 2, WriteTimeout: 5 * time.Second, WriteError: func(w http.ResponseWriter, _ *http.Request, err error) {
		if errors.Is(err, media.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/track", nil)
	r = r.WithContext(access.WithPrincipal(r.Context(), principal))
	w := httptest.NewRecorder()
	handler.ServeTrack(w, r, source, kind, track)
	return w
}

func sourceOf(t *testing.T, f jobFixture, relative string) string {
	t.Helper()
	var id string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM media_sources WHERE relative_path=$1`, relative).Scan(&id); err != nil {
		t.Fatal("source of", relative, err)
	}
	return id
}

// G15.3 / G16.2: a scan pairs external subtitle and audio files with their
// video, the catalog sync stores them in the same transaction as the source,
// a bounded read adds charset and fingerprint, and the direct delivery route
// serves them. Rescans follow additions, removals and renames; a vanished
// video takes its tracks along.
func TestCatalogSyncPairsSidecarTracksAndFollowsRescans(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f := newJobFixture(t)
	root := syncRootPath(t, f)
	jaText, err := japanese.ShiftJIS.NewEncoder().String("1\r\n00:00:01,000 --> 00:00:02,000\r\n一体何を言っているんだ？全然わからないよ。\r\n\r\n2\r\n00:00:03,000 --> 00:00:04,000\r\n暗くなる前にここを離れなければならない。\r\n\r\n3\r\n00:00:05,000 --> 00:00:06,000\r\n心配しないで、きっと大丈夫だから。\r\n\r\n4\r\n00:00:07,000 --> 00:00:08,000\r\nこんなに美しい景色は初めて見た。\r\n")
	if err != nil {
		t.Fatal(err)
	}
	ja := []byte(jaText)
	files := map[string][]byte{
		"Inception (2010)/Inception (2010).mkv":                      []byte("video-inception"),
		"Inception (2010)/Inception (2010).en.srt":                   []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nHello.\r\n"),
		"Inception (2010)/Subs/Inception (2010).ja.forced.srt":       ja,
		"Inception (2010)/Audio/Inception (2010).ja.commentary.flac": bytes.Repeat([]byte("fLaC"), 50000),
		// Not tracks: audio in a subtitle folder and an unrelated name.
		"Inception (2010)/Subs/Inception (2010).en.flac": []byte("audio-in-subs"),
		"Inception (2010)/Notes.en.srt":                  []byte("notes"),
		"Heat (1995).mkv":                                []byte("video-heat"),
		"Heat (1995).zh-Hans.Director.ass":               []byte("[Script Info]\r\nTitle: Heat\r\n"),
		"Subs/Heat (1995).en.srt":                        []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nHeat.\r\n"),
	}
	for i := 0; i < 10; i++ {
		files[fmt.Sprintf("Fillers/Film %c (2000).mkv", 'A'+i)] = []byte(fmt.Sprintf("video-filler-%d", i))
		files[fmt.Sprintf("Fillers/Film %c (2000).en.srt", 'A'+i)] = []byte(fmt.Sprintf("1\r\n00:00:01,000 --> 00:00:02,000\r\nFilm %d\r\n", i))
	}
	for name, content := range files {
		writeSidecarFile(t, root, name, content)
	}
	enableAutoSync(t, f)
	stop := startSidecarSyncWorker(t, f)
	defer func() { stop() }()
	f.submit(t, "sidecar-scan-1")
	waitJobsIdle(t, f)

	// Sidecars never become items, sources or pending entries.
	if n := syncCount(t, f, `SELECT count(*) FROM media_sources WHERE relative_path ~* '\.(srt|ass|flac)$'`); n != 0 {
		t.Fatal("a sidecar file was registered as a source", n)
	}
	if n := syncCount(t, f, `SELECT count(*) FROM catalog_scan_pending WHERE relative_path ~* '\.(srt|ass|flac)$'`); n != 0 {
		t.Fatal("a sidecar file became a pending entry", n)
	}
	if n := syncCount(t, f, `SELECT count(*) FROM items WHERE kind='Movie'`); n != 12 {
		t.Fatal("movies", n)
	}
	tracks := scannedTracks(t, f)
	if len(tracks) != 15 {
		t.Fatalf("tracks %d: %v", len(tracks), tracks)
	}
	en := tracks["Inception (2010)/Inception (2010).en.srt"]
	jaForced := tracks["Inception (2010)/Subs/Inception (2010).ja.forced.srt"]
	commentary := tracks["Inception (2010)/Audio/Inception (2010).ja.commentary.flac"]
	heatAss := tracks["Heat (1995).zh-Hans.Director.ass"]
	heatSrt := tracks["Subs/Heat (1995).en.srt"]
	switch {
	case en.video != "Inception (2010)/Inception (2010).mkv" || en.kind != "subtitle" || en.language != "en" || en.charset != "UTF-8":
		t.Fatalf("english subtitle %+v", en)
	case jaForced.language != "ja" || !jaForced.forced || jaForced.charset != "Shift_JIS" || jaForced.format != "srt":
		t.Fatalf("forced japanese subtitle %+v", jaForced)
	case commentary.kind != "audio" || commentary.format != "flac" || !commentary.commentary || commentary.charset != "":
		t.Fatalf("commentary audio %+v", commentary)
	case heatAss.video != "Heat (1995).mkv" || heatAss.language != "zh-Hans" || heatAss.title != "Director" || heatAss.format != "ass":
		t.Fatalf("root-level subtitle %+v", heatAss)
	case heatSrt.video != "Heat (1995).mkv" || heatSrt.language != "en":
		t.Fatalf("root Subs folder subtitle %+v", heatSrt)
	}
	for path, v := range tracks {
		if v.fingerprint != domain.SidecarFingerprintBytes {
			t.Fatal("track not inspected", path)
		}
	}
	if _, found := tracks["Inception (2010)/Subs/Inception (2010).en.flac"]; found {
		t.Fatal("audio inside a subtitle folder became a track")
	}
	audits := sidecarAudits(t, f)
	if len(audits) != 12 {
		t.Fatalf("sidecar audits %v", audits)
	}
	for _, a := range audits {
		if strings.HasPrefix(a, "Inception (2010)/Inception (2010).mkv ") && (!strings.Contains(a, `"added": 3`) || !strings.Contains(a, `"audio": 1`) || !strings.Contains(a, `"subtitles": 2`)) {
			t.Fatal("inception audit", a)
		}
		if strings.Contains(a, ".srt") || strings.Contains(a, "Notes") {
			t.Fatal("audit carries a file name", a)
		}
	}

	// The direct delivery route serves the original bytes of a scanned track.
	reader := trackPrincipal(t, f, "sidecar-reader", access.ClientNative, true)
	inception := sourceOf(t, f, "Inception (2010)/Inception (2010).mkv")
	w := sidecarRoute(t, f, reader, inception, media.TrackSubtitle, jaForced.id)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), ja) || w.Header().Get("Content-Type") != "application/x-subrip; charset=Shift_JIS" {
		t.Fatalf("subtitle route status=%d type=%q", w.Code, w.Header().Get("Content-Type"))
	}
	if w = sidecarRoute(t, f, reader, inception, media.TrackAudio, commentary.id); w.Code != http.StatusOK || w.Body.Len() != 200000 {
		t.Fatalf("audio route status=%d bytes=%d", w.Code, w.Body.Len())
	}

	// An unchanged rescan writes nothing.
	f.submit(t, "sidecar-scan-2")
	waitJobsIdle(t, f)
	again := scannedTracks(t, f)
	for path, v := range tracks {
		if again[path] != v {
			t.Fatal("unchanged rescan rewrote", path)
		}
	}
	if n := len(sidecarAudits(t, f)); n != 12 {
		t.Fatal("unchanged rescan audited", n)
	}

	// Remove, rename and add sidecars next to unchanged videos.
	removeSidecarFile(t, root, "Inception (2010)/Inception (2010).en.srt")
	if err = os.Rename(filepath.Join(root, "Subs", "Heat (1995).en.srt"), filepath.Join(root, "Subs", "Heat (1995).en.sdh.srt")); err != nil {
		t.Fatal(err)
	}
	writeSidecarFile(t, root, "Inception (2010)/Inception (2010).fr.ass", []byte("[Script Info]\r\nTitle: Inception\r\n"))
	f.submit(t, "sidecar-scan-3")
	waitJobsIdle(t, f)
	changed := scannedTracks(t, f)
	if len(changed) != 15 {
		t.Fatalf("tracks after changes %d", len(changed))
	}
	if _, found := changed["Inception (2010)/Inception (2010).en.srt"]; found {
		t.Fatal("removed subtitle kept")
	}
	if _, found := changed["Subs/Heat (1995).en.srt"]; found {
		t.Fatal("renamed subtitle kept under its old name")
	}
	if v := changed["Subs/Heat (1995).en.sdh.srt"]; v.video != "Heat (1995).mkv" || !v.sdh || v.fingerprint != domain.SidecarFingerprintBytes {
		t.Fatalf("renamed subtitle %+v", v)
	}
	if v := changed["Inception (2010)/Inception (2010).fr.ass"]; v.language != "fr" || v.fingerprint != domain.SidecarFingerprintBytes {
		t.Fatalf("added subtitle %+v", v)
	}
	if changed["Inception (2010)/Subs/Inception (2010).ja.forced.srt"] != jaForced {
		t.Fatal("untouched track rewritten")
	}
	audits = sidecarAudits(t, f)
	if len(audits) != 14 {
		t.Fatalf("audits after changes %v", audits)
	}
	for _, a := range audits[12:] {
		if !strings.Contains(a, `"added": 1`) || !strings.Contains(a, `"removed": 1`) || !strings.Contains(a, `"updated": 0`) {
			t.Fatal("change audit", a)
		}
	}
	if r := latestSyncReport(t, f); r.Unchanged != 12 || r.Created != 0 || r.Updated != 0 {
		t.Fatalf("source counters moved for sidecar-only changes %+v", r)
	}
	if w = sidecarRoute(t, f, reader, inception, media.TrackSubtitle, en.id); w.Code != http.StatusNotFound {
		t.Fatal("removed track still served", w.Code)
	}

	// A video that vanished below the review threshold is only marked
	// missing, and its tracks go with it.
	heat := sourceOf(t, f, "Heat (1995).mkv")
	removeSidecarFile(t, root, "Heat (1995).mkv", "Heat (1995).zh-Hans.Director.ass", "Subs/Heat (1995).en.sdh.srt")
	f.submit(t, "sidecar-scan-4")
	waitJobsIdle(t, f)
	if n := syncCount(t, f, `SELECT count(*) FROM media_sidecar_tracks WHERE source_id=$1::uuid`, heat); n != 0 {
		t.Fatal("tracks of a missing video kept", n)
	}
	if n := syncCount(t, f, `SELECT count(*) FROM catalog_scan_sources WHERE source_id=$1::uuid AND missing_since IS NOT NULL`, heat); n != 1 {
		t.Fatal("missing video not marked", n)
	}
	if a := sidecarAudits(t, f); len(a) != 15 || !strings.HasPrefix(a[14], "Heat (1995).mkv ") || !strings.Contains(a[14], `"removed": 2`) {
		t.Fatalf("missing video audit %v", a)
	}

	// An accepted mass removal deletes the sources; their tracks cascade.
	for i := 0; i < 10; i++ {
		removeSidecarFile(t, root, fmt.Sprintf("Fillers/Film %c (2000).mkv", 'A'+i), fmt.Sprintf("Fillers/Film %c (2000).en.srt", 'A'+i))
	}
	reviewed := f.submit(t, "sidecar-scan-5")
	waitJobsIdle(t, f)
	if got := f.get(t, reviewed.ID); !got.ReviewRequired || got.Missing != 20 {
		t.Fatalf("mass removal not held for review %+v", got)
	}
	if _, err = f.s.AcceptInventoryMissing(f.ctx, f.a, reviewed.ID, 20, f.policy); err != nil {
		t.Fatal(err)
	}
	waitJobsIdle(t, f)
	if n := syncCount(t, f, `SELECT count(*) FROM media_sidecar_tracks t JOIN media_sources m ON m.id=t.source_id WHERE m.relative_path LIKE 'Fillers/%'`); n != 0 {
		t.Fatal("tracks of removed sources kept", n)
	}
	if n := syncCount(t, f, `SELECT count(*) FROM media_sidecar_tracks`); n != 3 {
		t.Fatal("remaining tracks", n)
	}
}

// Files an ignore rule excludes are kept in the baseline with their old
// observation; they must not be paired as tracks.
func TestCatalogSyncSidecarsHonourIgnoreRules(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f := newJobFixture(t)
	root := syncRootPath(t, f)
	for _, name := range []string{"Show (2020).mkv", "Show (2020).en.srt", "Show (2020).ja.srt"} {
		writeSidecarFile(t, root, name, []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\n"+name+"\r\n"))
	}
	enableAutoSync(t, f)
	stop := startSidecarSyncWorker(t, f)
	defer stop()
	f.submit(t, "ignore-sidecar-1")
	waitJobsIdle(t, f)
	if tracks := scannedTracks(t, f); len(tracks) != 2 {
		t.Fatalf("tracks before ignore %v", tracks)
	}
	writeSidecarFile(t, root, ".jeleeignore", []byte("*.ja.srt\n"))
	ignoreSubmit(t, f, "ignore-sidecar-2")
	waitJobsIdle(t, f)
	if _, _, err := f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, "ignore-sidecar-sync", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	waitJobsIdle(t, f)
	if n := syncCount(t, f, `SELECT count(*) FROM library_inventory_baseline WHERE path='Show (2020).ja.srt'`); n != 1 {
		t.Fatal("fixture: the excluded file should stay in the baseline", n)
	}
	tracks := scannedTracks(t, f)
	if _, found := tracks["Show (2020).ja.srt"]; found || len(tracks) != 1 {
		t.Fatalf("ignored sidecar still a track %v", tracks)
	}
	if _, found := tracks["Show (2020).en.srt"]; !found {
		t.Fatal("included sidecar lost")
	}
}

// The database owner function and the Go pairing helper agree.
func TestSidecarOwnerFunctionMatchesDomain(t *testing.T) {
	f := newJobFixture(t)
	for _, p := range []string{"Movie.mkv", "Subs/Movie.en.srt", "A/B/Movie.mkv", "A/B/Subs/x.srt", "A/B/SUBTITLES/x.srt", "A/B/sub/x.srt",
		"A/B/Subtitle/x.srt", "A/B/Audio/x.flac", "A/B/audios/x.flac", "A/B/Subs/C/x.srt", "A/MySubs/x.srt", "A/Subsx/x.srt", "A/ſubs/x.srt",
		"Subs/Subs/x.srt", "A/Audio/Subs/x.srt", "Ä/Ü/x.srt"} {
		var got string
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT inventory_sidecar_owner($1)`, p).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := domain.SidecarOwnerDirectory(p); got != want {
			t.Errorf("owner of %q: database %q, domain %q", p, got, want)
		}
	}
	// The pairing lookup can use the owner index instead of reading the
	// whole snapshot (the tiny fixture table would otherwise be scanned).
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err = tx.Exec(f.ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(f.ctx, `EXPLAIN (FORMAT TEXT) SELECT b.path FROM unnest($1::uuid[],$2::text[]) AS o(root_id,owner)
 JOIN library_inventory_baseline_data b ON b.library_id=$3::uuid AND b.snapshot_id=1 AND b.root_id=o.root_id
  AND inventory_sidecar_owner(b.path)=o.owner AND b.kind IN ('video','other')`, []string{f.registration.RootID}, []string{"A"}, f.registration.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	rows.Close()
	if !strings.Contains(plan.String(), "library_inventory_sidecar_owner_idx") {
		t.Fatal("pairing lookup does not use the owner index:\n" + plan.String())
	}
}
