package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

func syncRootPath(t *testing.T, f jobFixture) string {
	t.Helper()
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeSyncFiles(t *testing.T, root string, files ...string) {
	t.Helper()
	stamp := time.Unix(1700000000, 0)
	for _, name := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("media:"+name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(full, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
}

// startSyncWorker runs the production runner with catalog synchronisation and
// the requested number of concurrent scan slots.
func startSyncWorker(t *testing.T, f jobFixture, slots int) func() {
	t.Helper()
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval, opts.ScanConcurrency = 1, 100*time.Millisecond, slots
	opts.CatalogImport = &jobs.CatalogImportOptions{Repository: f.s, Verifier: scan.New()}
	opts.CatalogSync = &jobs.CatalogSyncOptions{Repository: f.s}
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

func waitJobsIdle(t *testing.T, f jobFixture) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		var active, failed int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FILTER(WHERE state IN ('queued','running')),count(*) FILTER(WHERE state='failed') FROM jobs`).Scan(&active, &failed); err != nil {
			t.Fatal(err)
		}
		if failed != 0 {
			var detail string
			_ = f.s.Pool.QueryRow(f.ctx, `SELECT string_agg(kind||':'||error_code,',') FROM jobs WHERE state='failed'`).Scan(&detail)
			t.Fatal("job failed:", detail)
		}
		if active == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("jobs did not finish")
}

func latestSyncReport(t *testing.T, f jobFixture) domain.CatalogSyncReport {
	t.Helper()
	var id string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM jobs WHERE kind='catalog_sync' ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	r, err := f.s.GetCatalogSyncReport(f.ctx, f.a, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func syncCount(t *testing.T, f jobFixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(f.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(query, err)
	}
	return n
}

func enableAutoSync(t *testing.T, f jobFixture) {
	t.Helper()
	if v, err := f.s.PutCatalogSyncSettings(f.ctx, f.a, f.registration.Library.ID, true); err != nil || !v.Auto {
		t.Fatal(err)
	}
}

func syncLease(t *testing.T, f jobFixture, owner string) domain.JobLease {
	t.Helper()
	l, err := f.s.ClaimJobWithCapabilities(f.ctx, owner, false, time.Minute, domain.ScanCapabilities{CatalogSync: true})
	if err != nil || l.Job.Kind != domain.JobCatalogSync {
		t.Fatal("claim catalog sync", err, l.Job.Kind)
	}
	return l
}

func driveSync(t *testing.T, f jobFixture, l domain.JobLease, limit int) bool {
	t.Helper()
	for i := 0; i < limit; i++ {
		done, err := f.s.AdvanceCatalogSync(f.ctx, l)
		if err != nil {
			t.Fatal("advance", err)
		}
		if done {
			return true
		}
	}
	return false
}

// --- W6: deletion confirmation -------------------------------------------

func TestCatalogSyncAcceptMissingPublishesReviewedBaseline(t *testing.T) {
	f := newJobFixture(t)
	names := make([]string, 20)
	for i := range names {
		names[i] = fmt.Sprintf("f%02d.mkv", i)
	}
	if j := f.complete(t, "baseline", names, 0); j.ReviewRequired {
		t.Fatal("first baseline required review")
	}
	reviewed := f.complete(t, "shrunk", names[:5], 0)
	if !reviewed.ReviewRequired || reviewed.Missing != 15 {
		t.Fatalf("threshold did not hold baseline: %+v", reviewed)
	}
	baseline := `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`
	if syncCount(t, f, baseline, f.registration.Library.ID) != 20 {
		t.Fatal("review replaced baseline")
	}
	if _, err := f.s.AcceptInventoryMissing(f.ctx, f.a, reviewed.ID, 14, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale confirmation count accepted", err)
	}
	sync, err := f.s.AcceptInventoryMissing(f.ctx, f.a, reviewed.ID, 15, f.policy)
	if err != nil || sync.Kind != domain.JobCatalogSync || sync.State != domain.JobQueued {
		t.Fatal(err, sync)
	}
	if _, err = f.s.AcceptInventoryMissing(f.ctx, f.a, reviewed.ID, 15, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("replayed acceptance was not rejected", err)
	}
	if _, err = f.s.ClaimJob(f.ctx, "plain-worker", false, time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("worker without catalog sync capability claimed it", err)
	}
	first := syncLease(t, f, "accept-worker-1")
	if done := driveSync(t, f, first, 1); done {
		t.Fatal("publication finished in one cleanup batch")
	}
	// Expired owner: the database clock decides; nothing it writes commits.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, first.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.AdvanceCatalogSync(f.ctx, first); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("expired owner advanced publication", err)
	}
	second := syncLease(t, f, "accept-worker-2")
	if second.Generation <= first.Generation {
		t.Fatal("replacement did not fence")
	}
	if _, err = f.s.AdvanceCatalogSync(f.ctx, first); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("replaced owner advanced publication", err)
	}
	if !driveSync(t, f, second, 100) {
		t.Fatal("publication did not finish")
	}
	if err = f.s.FinishCatalogSync(f.ctx, first, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("stale owner finished", err)
	}
	if err = f.s.FinishCatalogSync(f.ctx, second, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if syncCount(t, f, baseline, f.registration.Library.ID) != 5 {
		t.Fatal("accepted baseline not published")
	}
	if syncCount(t, f, `SELECT count(*) FROM inventory_missing_acceptances WHERE job_id=$1::uuid AND published_at IS NOT NULL AND accepted_missing=15`, reviewed.ID) != 1 {
		t.Fatal("acceptance not recorded as published")
	}
	if syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event IN ('job.missing_accepted','inventory.baseline_accepted') AND target_id=$1::uuid`, reviewed.ID) != 2 {
		t.Fatal("acceptance audit missing")
	}
	if _, err = f.s.AcceptInventoryMissing(f.ctx, f.a, reviewed.ID, 15, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("published acceptance replayed", err)
	}
	// The next scan compares against the accepted baseline.
	if next := f.complete(t, "after-accept", names[:5], 0); next.ReviewRequired || next.Missing != 0 {
		t.Fatalf("accepted baseline not used: %+v", next)
	}
}

func TestCatalogSyncAcceptMissingRejectsStaleAndAllowsRetryAfterCancel(t *testing.T) {
	f := newJobFixture(t)
	names := make([]string, 20)
	for i := range names {
		names[i] = fmt.Sprintf("g%02d.mkv", i)
	}
	plain := f.complete(t, "baseline", names, 0)
	if _, err := f.s.AcceptInventoryMissing(f.ctx, f.a, plain.ID, 1, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("non-review job accepted", err)
	}
	reviewed := f.complete(t, "shrunk", names[:4], 0)
	sync, err := f.s.AcceptInventoryMissing(f.ctx, f.a, reviewed.ID, 16, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CancelJob(f.ctx, f.a, sync.ID); err != nil {
		t.Fatal(err)
	}
	if syncCount(t, f, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`, f.registration.Library.ID) != 20 {
		t.Fatal("cancelled acceptance changed baseline")
	}
	// A decision that ended unpublished may be made again.
	again, err := f.s.AcceptInventoryMissing(f.ctx, f.a, reviewed.ID, 16, f.policy)
	if err != nil || again.ID == sync.ID {
		t.Fatal("retry after cancelled acceptance", err)
	}
	if _, err = f.s.CancelJob(f.ctx, f.a, again.ID); err != nil {
		t.Fatal(err)
	}
	// A newer scan makes the reviewed result stale.
	newer := f.complete(t, "newer", names[:4], 0)
	if _, err = f.s.AcceptInventoryMissing(f.ctx, f.a, reviewed.ID, 16, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale reviewed scan accepted", err)
	}
	if !newer.ReviewRequired {
		t.Fatal("newer scan should still require review")
	}
	if _, err = f.s.AcceptInventoryMissing(f.ctx, domain.Actor{UserID: f.a.UserID, SessionID: "00000000-0000-4000-8000-000000000000"}, newer.ID, 16, f.policy); err == nil {
		t.Fatal("unauthenticated acceptance")
	}
}

// --- W7: directory claims ------------------------------------------------

func TestInventoryClaimScanDirectoryRaceRecoveryAndLeaseLoss(t *testing.T) {
	f := newJobFixture(t)
	job := f.submit(t, "claims")
	l := f.claim(t, "claim-worker")
	root, err := f.s.ClaimScanDirectory(f.ctx, l)
	if err != nil || root.Path != "." || root.ClaimToken == "" {
		t.Fatal(err, root)
	}
	children := make([]string, 24)
	for i := range children {
		children[i] = fmt.Sprintf("c%02d", i)
	}
	if err = f.s.SaveScanBatch(f.ctx, l, root, domain.ScanBatch{Directories: children, Done: true}); err != nil {
		t.Fatal(err)
	}
	type claim struct {
		d   domain.ScanDirectory
		err error
	}
	results := make(chan claim, 32)
	for i := 0; i < 32; i++ {
		go func() {
			d, err := f.s.ClaimScanDirectory(f.ctx, l)
			results <- claim{d, err}
		}()
	}
	seen := map[string]domain.ScanDirectory{}
	for i := 0; i < 32; i++ {
		r := <-results
		if errors.Is(r.err, domain.ErrNotFound) {
			continue
		}
		if r.err != nil {
			t.Fatal(r.err)
		}
		if _, dup := seen[r.d.Path]; dup {
			t.Fatal("directory claimed twice", r.d.Path)
		}
		seen[r.d.Path] = r.d
	}
	if len(seen) != len(children) {
		t.Fatalf("claimed %d of %d", len(seen), len(children))
	}
	a, b := seen["c00"], seen["c01"]
	// Another slot's token cannot write into a claimed directory.
	forged := a
	forged.ClaimToken = b.ClaimToken
	if err = f.s.SaveScanBatch(f.ctx, l, forged, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(a, "x.mkv", 1)}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("foreign slot wrote a claimed directory", err)
	}
	if err = f.s.SaveScanBatch(f.ctx, l, a, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(a, "partial.mkv", 9)}}); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, job.ID); got.Files != 1 {
		t.Fatal("partial batch not counted", got.Files)
	}
	// Lease loss: the replacement generation voids every claim and restarts
	// unfinished directories, dropping their partial observations.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.SaveScanBatch(f.ctx, l, b, domain.ScanBatch{Done: true}); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("expired slot saved", err)
	}
	next := f.claim(t, "claim-worker-2")
	if _, err = f.s.ClaimScanDirectory(f.ctx, l); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("stale generation claimed", err)
	}
	recovered, err := f.s.ClaimScanDirectory(f.ctx, next)
	if err != nil || recovered.Path != "c00" || recovered.ClaimToken == a.ClaimToken {
		t.Fatal("recovery did not restart the first unfinished directory", err, recovered)
	}
	if got := f.get(t, job.ID); got.Files != 0 {
		t.Fatal("partial observation survived restart", got.Files)
	}
	if err = f.s.SaveScanBatch(f.ctx, next, a, domain.ScanBatch{Done: true}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old generation token accepted", err)
	}
	// Persisted cancellation stops claims and checkpoints.
	if _, err = f.s.CancelJob(f.ctx, f.a, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ClaimScanDirectory(f.ctx, next); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled job claimed", err)
	}
	if err = f.s.SaveScanBatch(f.ctx, next, recovered, domain.ScanBatch{Done: true}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled job saved", err)
	}
}

type inventoryResult struct {
	entries                     []string
	files, dirs, skipped, bytes int64
	reviewRequired              bool
}

func runInventoryWith(t *testing.T, f jobFixture, slots int, key string) inventoryResult {
	t.Helper()
	stop := startSyncWorker(t, f, slots)
	job := f.submit(t, key)
	waitJobsIdle(t, f)
	stop()
	got := f.get(t, job.ID)
	if got.State != domain.JobSucceeded || got.Attempts != 1 {
		t.Fatalf("scan with %d slots: %+v", slots, got)
	}
	rows, err := f.s.Pool.Query(f.ctx, `SELECT path||'|'||kind||'|'||size FROM job_inventory WHERE job_id=$1::uuid`, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := inventoryResult{files: got.Files, dirs: got.Directories, skipped: got.Skipped, bytes: got.Bytes, reviewRequired: got.ReviewRequired}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		result.entries = append(result.entries, v)
	}
	sort.Strings(result.entries)
	return result
}

func mixedLibrary(count, perDirectory int) []string {
	kinds := []string{"mkv", "nfo", "jpg", "txt", "mp4"}
	files := make([]string, 0, count)
	for i := 0; i < count; i++ {
		dir := fmt.Sprintf("group%03d/sub%02d", i/(perDirectory*4), (i/perDirectory)%4)
		files = append(files, fmt.Sprintf("%s/file%04d.%s", dir, i, kinds[i%len(kinds)]))
	}
	return files
}

func TestInventoryConcurrentScanMatchesSequential(t *testing.T) {
	for _, size := range []int{100, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			files := mixedLibrary(size, 10)
			sequential := newJobFixture(t)
			writeSyncFiles(t, syncRootPath(t, sequential), files...)
			concurrent := newJobFixture(t)
			writeSyncFiles(t, syncRootPath(t, concurrent), files...)
			want := runInventoryWith(t, sequential, 1, "sequential")
			got := runInventoryWith(t, concurrent, 4, "concurrent")
			if len(want.entries) != size || strings.Join(want.entries, "\n") != strings.Join(got.entries, "\n") || want.files != got.files || want.dirs != got.dirs || want.skipped != got.skipped || want.bytes != got.bytes || want.reviewRequired != got.reviewRequired {
				t.Fatalf("concurrent result differs: files %d/%d dirs %d/%d bytes %d/%d", want.files, got.files, want.dirs, got.dirs, want.bytes, got.bytes)
			}
			// An unchanged rescan stays deterministic and keeps the baseline.
			again := runInventoryWith(t, concurrent, 4, "concurrent-again")
			if strings.Join(again.entries, "\n") != strings.Join(want.entries, "\n") || again.reviewRequired {
				t.Fatal("concurrent rescan differs")
			}
			if syncCount(t, concurrent, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`, concurrent.registration.Library.ID) != size {
				t.Fatal("concurrent baseline incomplete")
			}
		})
	}
}

// --- W8: scan to catalog synchronisation ---------------------------------

var syncLibraryFiles = []string{
	"Inception (2010)/Inception (2010).mkv",
	"Inception (2010)/Inception (2010) - 2160p.mkv",
	"Inception (2010)/Inception (2010).nfo",
	"Heat (1995).mp4",
	"Heat (1995).nfo",
	"Breaking Bad (2008)/tvshow.nfo",
	"Breaking Bad (2008)/Season 01/Breaking Bad S01E01.mkv",
	"Breaking Bad (2008)/Season 01/Breaking Bad S01E02.mkv",
	"Breaking Bad (2008)/Season 01/Breaking Bad S01E02.nfo",
	"Breaking Bad (2008)/Season 01/Breaking Bad S01E03-E04.mkv",
	"Breaking Bad (2008)/Season 02/Breaking Bad S02E01.mkv",
	"Breaking Bad (2008)/Specials/Breaking Bad S00E01.mkv",
	"Breaking Bad (2008)/Season 01/folder.jpg",
	"Naruto/Naruto - 012.mkv",
	"Naruto/Naruto - 013.mkv",
	"random clip.mkv",
}

type syncedItem struct {
	id, kind, title, parent, source string
	sources                         int
}

func syncedItems(t *testing.T, f jobFixture) map[string]syncedItem {
	t.Helper()
	rows, err := f.s.Pool.Query(f.ctx, `SELECT i.id::text,i.kind,i.title,COALESCE((SELECT parent_id::text FROM item_parent_links p WHERE p.item_id=i.id),''),COALESCE((SELECT source FROM item_metadata_fields m WHERE m.item_id=i.id AND m.field='title'),''),(SELECT count(*) FROM media_sources s WHERE s.item_id=i.id) FROM items i WHERE i.library_id=$1::uuid`, f.registration.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	items := map[string]syncedItem{}
	for rows.Next() {
		var v syncedItem
		if err = rows.Scan(&v.id, &v.kind, &v.title, &v.parent, &v.source, &v.sources); err != nil {
			t.Fatal(err)
		}
		if _, dup := items[v.kind+":"+v.title]; dup {
			t.Fatal("duplicate item", v.kind, v.title)
		}
		items[v.kind+":"+v.title] = v
	}
	return items
}

func TestCatalogSyncBuildsStructureIdempotentAndRespectsPriority(t *testing.T) {
	f := newJobFixture(t)
	writeSyncFiles(t, syncRootPath(t, f), syncLibraryFiles...)
	enableAutoSync(t, f)
	stop := startSyncWorker(t, f, 3)
	defer stop()
	f.submit(t, "first-scan")
	waitJobsIdle(t, f)
	items := syncedItems(t, f)
	expect := map[string]int{
		"Movie:Inception": 2, "Movie:Heat": 1, "Series:Breaking Bad": 0,
		"Season:Season 1": 0, "Season:Season 2": 0, "Season:Specials": 0,
		"Episode:Breaking Bad S01E01": 1, "Episode:Breaking Bad S01E02": 1, "Episode:Breaking Bad S01E03-E04": 1,
		"Episode:Breaking Bad S02E01": 1, "Episode:Breaking Bad S00E01": 1,
	}
	if len(items) != len(expect) {
		t.Fatalf("items %v", items)
	}
	for key, sources := range expect {
		v, ok := items[key]
		if !ok || v.sources != sources || v.source != "scan" {
			t.Fatalf("%s: %+v", key, v)
		}
	}
	series := items["Series:Breaking Bad"].id
	for _, season := range []string{"Season:Season 1", "Season:Season 2", "Season:Specials"} {
		if items[season].parent != series {
			t.Fatal("season not under series", season)
		}
	}
	if items["Episode:Breaking Bad S01E03-E04"].parent != items["Season:Season 1"].id || items["Episode:Breaking Bad S00E01"].parent != items["Season:Specials"].id || items["Episode:Breaking Bad S02E01"].parent != items["Season:Season 2"].id {
		t.Fatal("episode parents wrong")
	}
	if syncCount(t, f, `SELECT count(*) FROM catalog_scan_items WHERE kind='Episode' AND episode=3 AND episode_end=4`) != 1 {
		t.Fatal("multi-episode range not recorded")
	}
	if syncCount(t, f, `SELECT count(*) FROM item_directory_sources WHERE relative_path IN ('Breaking Bad (2008)','Breaking Bad (2008)/Season 01','Breaking Bad (2008)/Season 02','Breaking Bad (2008)/Specials')`) != 4 {
		t.Fatal("series/season folders not registered for NFO")
	}
	pending, err := f.s.ListCatalogPending(f.ctx, f.a, f.registration.Library.ID, "", 100)
	if err != nil || len(pending) != 3 {
		t.Fatal("pending", err, pending)
	}
	for _, p := range pending {
		if p.Reason != "confidence" || p.Confidence == "high" {
			t.Fatal("pending entry", p)
		}
		if strings.HasPrefix(p.Path, "Naruto/") && (p.Absolute == 0 || p.Kind != "episode") {
			t.Fatal("absolute episode not described", p)
		}
	}
	first := latestSyncReport(t, f)
	if first.Created != 8 || first.Examined != 11 || first.Pending != 3 || first.Phase != "done" {
		t.Fatalf("first report %+v", first)
	}

	// Rerunning the same baseline creates nothing.
	if _, _, err = f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, "rerun", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	waitJobsIdle(t, f)
	if again := syncedItems(t, f); len(again) != len(items) {
		t.Fatal("rerun duplicated structure")
	}
	if r := latestSyncReport(t, f); r.Created != 0 || r.Pending != 0 || r.Unchanged != 11 {
		t.Fatalf("rerun report %+v", r)
	}

	// A manual lock wins over a newer file-name value; an unlocked scan value
	// is refreshed (the control proves the refresh path writes at all).
	inception, heat := items["Movie:Inception"].id, items["Movie:Heat"].id
	locked := true
	value := "Inception (Director's Pick)"
	if _, err = f.s.UpdateItemMetadata(f.ctx, f.a, inception, 2, []domain.ItemMetadataPatch{{Field: "title", Value: &value, Locked: &locked}}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE catalog_scan_items SET scan_title='Stale' WHERE item_id IN ($1::uuid,$2::uuid)`, inception, heat); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE item_metadata_fields SET value='Stale' WHERE item_id=$1::uuid AND field='title'`, heat); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE items SET title='Stale' WHERE id=$1::uuid`, heat); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE catalog_scan_sources SET parser_version='medianame-v0' WHERE item_id IN ($1::uuid,$2::uuid)`, inception, heat); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, "reparse", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	waitJobsIdle(t, f)
	after := syncedItems(t, f)
	if v := after["Movie:"+value]; v.id != inception || v.source != "manual" {
		t.Fatal("locked manual title overwritten", after)
	}
	if v := after["Movie:Heat"]; v.id != heat || v.source != "scan" {
		t.Fatal("unlocked scan title not refreshed", after)
	}
	if r := latestSyncReport(t, f); r.Protected < 1 || r.Updated != 3 {
		t.Fatalf("reparse report %+v", r)
	}
}

func TestCatalogSyncResumeIncrementalThresholdAndAcceptance(t *testing.T) {
	f := newJobFixture(t)
	root := syncRootPath(t, f)
	files := make([]string, 400)
	for i := range files {
		files[i] = fmt.Sprintf("Film %03d (2001).mkv", i)
	}
	writeSyncFiles(t, root, files...)
	stop := startSyncWorker(t, f, 2)
	f.submit(t, "scan-1")
	waitJobsIdle(t, f)
	stop()
	if syncCount(t, f, `SELECT count(*) FROM jobs WHERE kind='catalog_sync'`) != 0 || syncCount(t, f, `SELECT count(*) FROM items`) != 0 {
		t.Fatal("synchronisation ran without opt-in")
	}

	// Manual sync driven batch by batch: a lost lease and a cancellation keep
	// committed batches, and the follow-up run neither repeats nor duplicates.
	job, _, err := f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, "manual-1", domain.JobPriorityManual, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	l := syncLease(t, f, "sync-a")
	driveSync(t, f, l, 3)
	created := syncCount(t, f, `SELECT count(*) FROM catalog_scan_sources`)
	if created != 3*32 {
		t.Fatal("batches did not checkpoint", created)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.AdvanceCatalogSync(f.ctx, l); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("expired sync owner advanced", err)
	}
	l2 := syncLease(t, f, "sync-b")
	driveSync(t, f, l2, 2)
	if got := syncCount(t, f, `SELECT count(*) FROM catalog_scan_sources`); got != 5*32 {
		t.Fatal("resume did not continue from checkpoint", got)
	}
	if _, err = f.s.CancelJob(f.ctx, f.a, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.AdvanceCatalogSync(f.ctx, l2); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled sync advanced", err)
	}
	if err = f.s.FinishCatalogSync(f.ctx, l2, domain.JobCancelled, ""); err != nil {
		t.Fatal(err)
	}
	enableAutoSync(t, f)
	if _, _, err = f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, "manual-2", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	stop = startSyncWorker(t, f, 2)
	defer func() { stop() }()
	waitJobsIdle(t, f)
	if syncCount(t, f, `SELECT count(*) FROM items WHERE kind='Movie'`) != 400 || syncCount(t, f, `SELECT count(*) FROM media_sources`) != 400 {
		t.Fatal("resumed sync incomplete or duplicated")
	}
	if r := latestSyncReport(t, f); r.Created != 400-5*32 || r.Unchanged != 5*32 {
		t.Fatalf("follow-up report %+v", r)
	}

	// 100 changed files: only they are processed.
	later := time.Unix(1800000000, 0)
	for _, name := range files[:100] {
		full := filepath.Join(root, name)
		if err = os.WriteFile(full, []byte("changed content "+name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err = os.Chtimes(full, later, later); err != nil {
			t.Fatal(err)
		}
	}
	f.submit(t, "scan-2")
	waitJobsIdle(t, f)
	if r := latestSyncReport(t, f); r.Updated != 100 || r.Unchanged != 300 || r.Created != 0 || r.Examined != 400 {
		t.Fatalf("incremental report %+v", r)
	}

	// Threshold: a mass disappearance is held for review and touches nothing.
	for _, name := range files[:15] {
		if err = os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	reviewed := f.submit(t, "scan-3")
	waitJobsIdle(t, f)
	if got := f.get(t, reviewed.ID); !got.ReviewRequired || got.Missing != 15 {
		t.Fatalf("threshold not applied %+v", got)
	}
	if syncCount(t, f, `SELECT count(*) FROM media_sources`) != 400 || syncCount(t, f, `SELECT count(*) FROM catalog_scan_sources WHERE missing_since IS NOT NULL`) != 0 {
		t.Fatal("unconfirmed disappearance changed the catalog")
	}
	// Accepting the review removes only those sources and their empty items.
	if _, err = f.s.AcceptInventoryMissing(f.ctx, f.a, reviewed.ID, 15, f.policy); err != nil {
		t.Fatal(err)
	}
	waitJobsIdle(t, f)
	if syncCount(t, f, `SELECT count(*) FROM media_sources`) != 385 || syncCount(t, f, `SELECT count(*) FROM items`) != 385 {
		t.Fatal("accepted removal incomplete")
	}
	if r := latestSyncReport(t, f); r.Mode != "accept" || r.Removed != 15 {
		t.Fatalf("accept report %+v", r)
	}
	// Below the threshold a vanished file is only marked missing.
	for _, name := range files[15:18] {
		if err = os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	f.submit(t, "scan-4")
	waitJobsIdle(t, f)
	if syncCount(t, f, `SELECT count(*) FROM media_sources`) != 385 || syncCount(t, f, `SELECT count(*) FROM catalog_scan_sources WHERE missing_since IS NOT NULL`) != 3 {
		t.Fatal("missing files were removed without confirmation")
	}
	if r := latestSyncReport(t, f); r.MarkedMissing != 3 || r.Removed != 0 {
		t.Fatalf("mark report %+v", r)
	}
}
