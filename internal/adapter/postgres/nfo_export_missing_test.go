package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

// nfoMissingItem is one item of the "only missing NFOs" library. existing says
// whether an NFO file was there before the job; stored whether the database
// holds a value the export can write (otherwise a create has nothing to say).
type nfoMissingItem struct {
	nfoExportItem
	kind     string
	existing bool
	stored   bool
	root     string
	wants    []string
	before   os.FileInfo
}

// created reports whether a missing-mode run creates the item's NFO.
func (it nfoMissingItem) created() bool { return !it.existing && it.stored }

func xmlText(s string) string { return strings.ReplaceAll(s, "&", "&amp;") }

// nfoMissingLibrary fills a read-write library with `shows` shows of one
// series, one season and four episodes each, then `movies` movies. By position
// n: every fifth item has an NFO (every other of those an invalid one), and
// every seventh item from the fourth on has no stored value at all. Items with
// an NFO still get stored values that differ from it, so an overwrite would
// show.
func nfoMissingLibrary(t *testing.T, f jobFixture, library string, shows, movies int) []nfoMissingItem {
	t.Helper()
	var root, rootID string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path,id::text FROM library_roots WHERE library_id=$1::uuid`, library).Scan(&root, &rootID); err != nil {
		t.Fatal(err)
	}
	nfoExportEnable(t, f, library)
	var items []nfoMissingItem
	add := func(kind, nfoName, dir, media string) *nfoMissingItem {
		n := len(items)
		var id string
		if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Scanned name',$2) RETURNING id::text`, library, kind).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
		if media == "" {
			if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_directory_sources(item_id,library_id,kind,root_id,relative_path) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5)`, id, library, kind, rootID, dir); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska')`, id, library, rootID, media); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(media)), []byte("synthetic media"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		it := nfoMissingItem{kind: kind, existing: n%5 == 2, stored: n%7 != 3}
		it.id, it.nfo = id, nfoName
		it.path = filepath.Join(root, filepath.FromSlash(nfoName))
		it.root = map[string]string{"Movie": "movie", "Series": "tvshow", "Season": "season", "Episode": "episodedetails"}[kind]
		if it.existing {
			it.body = fmt.Sprintf("<%[1]s>\n  <!-- existing %[2]d -->\n  <title>NFO title %[2]d</title>\n</%[1]s>\n", it.root, n)
			if n%10 == 2 {
				it.body = "<" + it.root + "><title>broken"
			}
			if err := os.WriteFile(it.path, []byte(it.body), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(it.path)
			if err != nil {
				t.Fatal(err)
			}
			it.before = info
		}
		text := func(field, value string) domain.ItemMetadataPatch {
			return domain.ItemMetadataPatch{Field: field, Value: &value}
		}
		var fields []domain.ItemMetadataPatch
		var facts []domain.ItemMetadataFactPatch
		if it.stored {
			title := fmt.Sprintf("%s %d & Co", kind, n)
			fields = append(fields, text("title", title), text("overview", fmt.Sprintf("Plot %d", n)))
			it.wants = append(it.wants, "<title>"+xmlText(title)+"</title>", fmt.Sprintf("<plot>Plot %d</plot>", n))
			switch kind {
			case "Movie":
				fields = append(fields, text("tagline", fmt.Sprintf("Tagline %d", n)))
				it.wants = append(it.wants, fmt.Sprintf("<tagline>Tagline %d</tagline>", n))
			case "Series":
				facts = append(facts, domain.ItemMetadataFactPatch{Field: "seriesStatus", Value: json.RawMessage(`"Ended"`)})
				it.wants = append(it.wants, "<status>Ended</status>")
			case "Episode":
				facts = append(facts, domain.ItemMetadataFactPatch{Field: "showTitle", Value: json.RawMessage(`"Show & Tell"`)})
				it.wants = append(it.wants, "<showtitle>Show &amp; Tell</showtitle>")
			}
			if _, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, id, 1, fields, facts); err != nil {
				t.Fatal(err)
			}
		}
		items = append(items, it)
		return &items[len(items)-1]
	}
	for g := 0; g < shows; g++ {
		show := fmt.Sprintf("Show %02d", g)
		add("Series", show+"/tvshow.nfo", show, "")
		add("Season", show+"/Season 1/season.nfo", show+"/Season 1", "")
		for e := 1; e <= 4; e++ {
			add("Episode", fmt.Sprintf("%s/Season 1/e%02d.nfo", show, e), show+"/Season 1", fmt.Sprintf("%s/Season 1/e%02d.mkv", show, e))
		}
	}
	for m := 0; m < movies; m++ {
		add("Movie", fmt.Sprintf("m%03d.nfo", m), ".", fmt.Sprintf("m%03d.mkv", m))
	}
	return items
}

// nfoMissingWant is what a missing-mode run counts for the items.
func nfoMissingWant(items []nfoMissingItem) domain.NFOExportCounts {
	var c domain.NFOExportCounts
	for _, it := range items {
		switch {
		case it.existing:
			c.SkippedExisting++
		case it.stored:
			c.Written++
		default:
			c.Unchanged++
		}
	}
	return c
}

func nfoMissingItems(items []nfoMissingItem) []nfoExportItem {
	out := make([]nfoExportItem, len(items))
	for i, it := range items {
		out[i] = it.nfoExportItem
	}
	return out
}

// requireNFOMissingFiles checks the files of every item up to upTo ("" for all)
// and that the ones beyond it were not touched; skip names items whose file a
// test replaced on purpose.
func requireNFOMissingFiles(t *testing.T, items []nfoMissingItem, upTo string, skip map[string]bool) {
	t.Helper()
	for i, it := range items {
		if skip[it.id] {
			continue
		}
		_, bakErr := os.Lstat(it.path + ".jelee.bak")
		if !errors.Is(bakErr, os.ErrNotExist) {
			t.Fatalf("item %d (%s) has a backup", i, it.kind)
		}
		data, err := os.ReadFile(it.path)
		switch {
		case it.existing:
			info, statErr := os.Stat(it.path)
			if err != nil || string(data) != it.body || statErr != nil || !os.SameFile(it.before, info) || !info.ModTime().Equal(it.before.ModTime()) {
				t.Fatalf("item %d (%s) had an NFO and it was touched: %q %v", i, it.kind, data, err)
			}
		case !it.created() || (upTo != "" && it.id > upTo):
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("item %d (%s) got an NFO it should not have: %q", i, it.kind, data)
			}
		default:
			got := string(data)
			if err != nil || !strings.HasPrefix(got, "<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?>\n<"+it.root+">") || !strings.Contains(got, `<uniqueid type="jelee"`) {
				t.Fatalf("item %d (%s) was not created: %q %v", i, it.kind, got, err)
			}
			for _, want := range it.wants {
				if !strings.Contains(got, want) {
					t.Fatalf("item %d (%s) lacks %s: %s", i, it.kind, want, got)
				}
			}
			document, readErr := nfo.Read(context.Background(), bytes.NewReader(data), domain.NFODefaultSourceBytes)
			if readErr != nil || document.Root != it.root {
				t.Fatalf("item %d (%s) created an unreadable NFO: %v", i, it.kind, readErr)
			}
		}
	}
}

// requireNoTokenFiles waits for the cleanup of every token name next to the NFOs.
func requireNoTokenFiles(t *testing.T, items []nfoMissingItem) {
	t.Helper()
	dirs := map[string]bool{}
	for _, it := range items {
		dirs[filepath.Dir(it.path)] = true
	}
	deadline := time.Now().Add(60 * time.Second)
	for dir := range dirs {
		for len(tokenNames(t, dir)) != 0 {
			if time.Now().After(deadline) {
				t.Fatal("token names were not cleaned", dir, tokenNames(t, dir))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func submitNFOExportMode(t *testing.T, f jobFixture, library, key, mode string) domain.Job {
	t.Helper()
	job, replay, err := f.s.SubmitNFOExportModeJob(f.ctx, f.a, library, key, domain.JobPriorityManual, mode, f.policy)
	if err != nil || replay || job.Kind != domain.JobNFOExport || job.State != domain.JobQueued {
		t.Fatal("submit nfo export", mode, err, replay, job)
	}
	return job
}

func nfoMissingEntries(t *testing.T, f jobFixture, job string) int {
	t.Helper()
	var entries int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_entries e JOIN jobs j ON j.id=e.job_id WHERE j.export_job_id=$1::uuid`, job).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestNFOExportMissingCreatesOnlyAbsentNFOs(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoMissingLibrary(t, f, f.registration.Library.ID, 8, 56)
	if len(items) < 100 {
		t.Fatal("the library is too small", len(items))
	}
	want := nfoMissingWant(items)
	if want.Written < 33 || want.SkippedExisting < 15 || want.Unchanged < 5 {
		t.Fatal("the mix does not cover every outcome", want)
	}
	job := submitNFOExportMode(t, f, f.registration.Library.ID, "missing-1", domain.NFOExportMissing)
	stop := startNFOExportRunner(t, f, metadata, f.s)
	defer stop()
	done := waitNFOExportJob(t, f, job.ID)
	if done.State != domain.JobSucceeded || done.ErrorCode != "" {
		t.Fatal("job did not succeed", done)
	}
	state := requireNFOExportCounts(t, f, job.ID, want)
	if state.Mode != domain.NFOExportMissing || state.Chunks < (want.Written+domain.NFOExportChunkSize-1)/domain.NFOExportChunkSize || state.ChildJobID != nil || state.CursorItemID == nil {
		t.Fatalf("state after the run: %+v", state)
	}
	if done.Files != int64(want.Written+want.Unchanged) || done.Skipped != int64(want.SkippedExisting) {
		t.Fatalf("job progress files=%d skipped=%d", done.Files, done.Skipped)
	}
	children := nfoExportChildren(t, f, job.ID)
	if len(children) != state.Chunks || nfoMissingEntries(t, f, job.ID) != want.Written {
		t.Fatal("children or entries", len(children), nfoMissingEntries(t, f, job.ID))
	}
	for i, child := range children {
		if child.State != domain.JobSucceeded || child.Key != domain.NFOExportChildKey(job.ID, i+1) {
			t.Fatal("child", i, child)
		}
	}
	created, existing := map[string]bool{}, map[string]bool{}
	for _, it := range items {
		created[it.kind] = created[it.kind] || it.created()
		existing[it.kind] = existing[it.kind] || it.existing
	}
	for _, kind := range []string{"Movie", "Series", "Season", "Episode"} {
		if !created[kind] || !existing[kind] {
			t.Fatal("a kind lacks created or existing items", kind, created, existing)
		}
	}
	requireNFOMissingFiles(t, items, "", nil)
	requireNFOExportClean(t, f, job.ID)
	requireNoTokenFiles(t, items)
	var prepared int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_preparations`).Scan(&prepared); err != nil || prepared != 0 {
		t.Fatal("preparations were left", prepared, err)
	}
	// Every NFO exists now: a second run finds nothing to create and writes nothing.
	second := submitNFOExportMode(t, f, f.registration.Library.ID, "missing-2", domain.NFOExportMissing)
	if finished := waitNFOExportJob(t, f, second.ID); finished.State != domain.JobSucceeded {
		t.Fatal("second export", finished)
	}
	again := requireNFOExportCounts(t, f, second.ID, domain.NFOExportCounts{SkippedExisting: want.SkippedExisting + want.Written, Unchanged: want.Unchanged})
	if again.Chunks != 0 || len(nfoExportChildren(t, f, second.ID)) != 0 {
		t.Fatal("an export of created files submitted writes", again)
	}
	requireNFOMissingFiles(t, items, "", nil)
	// The all mode still does what it did: it writes the stored values into
	// the NFOs that exist, backing the originals up.
	all := submitNFOExport(t, f, f.registration.Library.ID, "all-after")
	if finished := waitNFOExportJob(t, f, all.ID); finished.State != domain.JobSucceeded {
		t.Fatal("all-mode export", finished)
	}
	if got, err := f.s.GetNFOExportState(f.ctx, f.a, all.ID); err != nil || got.Mode != domain.NFOExportAll || got.SkippedExisting != 0 || got.Failed != 0 {
		t.Fatal("all-mode state", got, err)
	}
}

func TestNFOExportMissingThenApplyRoundTripsMetadata(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoMissingLibrary(t, f, f.registration.Library.ID, 3, 22)
	stop := startNFOExportRunner(t, f, metadata, f.s)
	defer stop()
	// Import first so the NFOs that exist agree with the database; only the
	// items without an NFO are new to the export.
	imported := submitNFOApply(t, f, f.registration.Library.ID, "missing-import")
	if done := waitNFOApplyJob(t, f, imported.ID); done.State != domain.JobSucceeded {
		t.Fatal("import", done)
	}
	exported := nfoMissingItems(items)
	before, beforeIDs := nfoExportSnapshot(t, f, exported)
	job := submitNFOExportMode(t, f, f.registration.Library.ID, "missing-round-trip", domain.NFOExportMissing)
	if done := waitNFOExportJob(t, f, job.ID); done.State != domain.JobSucceeded {
		t.Fatal("export", done)
	}
	requireNFOExportCounts(t, f, job.ID, nfoMissingWant(items))
	applied := submitNFOApply(t, f, f.registration.Library.ID, "missing-apply")
	if done := waitNFOApplyJob(t, f, applied.ID); done.State != domain.JobSucceeded {
		t.Fatal("apply", done)
	}
	after, afterIDs := nfoExportSnapshot(t, f, exported)
	for i, it := range items {
		if after[i] != before[i] {
			t.Fatalf("item %d (%s) differs after export and apply:\n before: %s\n after:  %s", i, it.kind, before[i], after[i])
		}
		// The one addition: the identifier the controlled writer put into the new file.
		wantID := 0
		if it.created() {
			wantID = 1
		}
		if beforeIDs[i] != 0 || afterIDs[i] != wantID {
			t.Fatalf("item %d (%s): generated identifiers %d before, %d after, want %d after", i, it.kind, beforeIDs[i], afterIDs[i], wantID)
		}
	}
}

// nfoMissingForeign writes a file nobody planned at the target of the first
// entry of the export job's chunk once, and remembers it.
type nfoMissingForeign struct {
	t     *testing.T
	items []nfoMissingItem
	done  atomic.Bool
	body  []byte
	id    string
	info  os.FileInfo
}

func (x *nfoMissingForeign) appear(entry func() (root, relative string, err error)) {
	if !x.done.CompareAndSwap(false, true) {
		return
	}
	root, relative, err := entry()
	if err != nil {
		x.t.Error(err)
		return
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	element := ""
	for _, it := range x.items {
		if it.path == path {
			x.id, element = it.id, it.root
		}
	}
	x.body = []byte("<" + element + ">\n  <title>appeared while the export ran</title>\n</" + element + ">\n")
	if x.id == "" || os.WriteFile(path, x.body, 0o600) != nil {
		x.t.Error("cannot place the foreign file", path)
		return
	}
	x.info, _ = os.Stat(path)
}

func (x *nfoMissingForeign) require(t *testing.T) map[string]bool {
	t.Helper()
	if !x.done.Load() || x.id == "" || x.info == nil {
		t.Fatal("the foreign file was never placed")
	}
	for _, it := range x.items {
		if it.id != x.id {
			continue
		}
		info, err := os.Stat(it.path)
		data, _ := os.ReadFile(it.path)
		if err != nil || !os.SameFile(x.info, info) || !bytes.Equal(data, x.body) || !info.ModTime().Equal(x.info.ModTime()) {
			t.Fatal("the file that appeared was replaced or modified")
		}
		if _, err = os.Lstat(it.path + ".jelee.bak"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the file that appeared was backed up")
		}
	}
	return map[string]bool{x.id: true}
}

func TestNFOExportMissingNeverOverwritesAFileThatAppears(t *testing.T) {
	for _, tc := range []string{"before the child ran", "after the backup phase"} {
		t.Run(tc, func(t *testing.T) {
			f := newJobFixture(t)
			metadata := nfoApplyTestMetadata(t, f)
			items := nfoMissingLibrary(t, f, f.registration.Library.ID, 4, 30)
			want := nfoMissingWant(items)
			foreign := &nfoMissingForeign{t: t, items: items}
			job := submitNFOExportMode(t, f, f.registration.Library.ID, "appear", domain.NFOExportMissing)
			repo := &hookedNFOExport{Store: f.s}
			write := app.NFOWriteExecutionRepository(f.s)
			if tc == "before the child ran" {
				// The first child is queued and the export still holds the only worker.
				repo.afterSubmit = func(n int, lease domain.JobLease) {
					if n == 1 {
						foreign.appear(func() (string, string, error) {
							var root, relative string
							err := f.s.Pool.QueryRow(f.ctx, `SELECT e.root_path,e.relative_path FROM nfo_write_entries e JOIN jobs j ON j.id=e.job_id WHERE j.export_job_id=$1::uuid ORDER BY e.sequence LIMIT 1`, lease.Job.ID).Scan(&root, &relative)
							return root, relative, err
						})
					}
				}
			} else {
				// The first entry's backup phase is durable: the file appears before the link.
				write = &phaseHookStore{Store: f.s, phase: domain.NFOWriteCommitBackedUp, after: nil}
				hook := write.(*phaseHookStore)
				hook.after = func() {
					foreign.appear(func() (string, string, error) {
						var root, relative string
						err := f.s.Pool.QueryRow(f.ctx, `SELECT e.root_path,e.relative_path FROM nfo_write_entries e JOIN jobs j ON j.id=e.job_id JOIN nfo_write_commit_journal w ON w.job_id=e.job_id AND w.sequence=e.sequence JOIN nfo_write_commit_settlements s ON s.token=w.token AND s.phase=1 WHERE j.export_job_id=$1::uuid ORDER BY e.sequence LIMIT 1`, job.ID).Scan(&root, &relative)
						return root, relative, err
					})
				}
			}
			stop := startNFOExportRunnerWith(t, f, metadata, repo, write)
			defer stop()
			done := waitNFOExportJob(t, f, job.ID)
			if done.State != domain.JobSucceeded {
				t.Fatal("job did not succeed", done)
			}
			skip := foreign.require(t)
			// The create that lost is a skipped existing item, not a write and not a failure.
			want.Written--
			want.SkippedExisting++
			state := requireNFOExportCounts(t, f, job.ID, want)
			if state.Failed != 0 || state.ChildJobID != nil {
				t.Fatal("state", state)
			}
			failed := 0
			for _, child := range nfoExportChildren(t, f, job.ID) {
				if child.State == domain.JobFailed {
					failed++
				}
			}
			if failed != 1 {
				t.Fatal("exactly the child that met the foreign file fails", nfoExportChildren(t, f, job.ID))
			}
			requireNFOMissingFiles(t, items, "", skip)
			requireNFOExportClean(t, f, job.ID)
			requireNoTokenFiles(t, items)
		})
	}
}

// A child that ends without writing and without a file at its targets is a
// failure, whatever the reason: its entries are failed, the export goes on.
func TestNFOExportMissingCountsAChildThatWroteNothingAsFailed(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoMissingLibrary(t, f, f.registration.Library.ID, 4, 30)
	want := nfoMissingWant(items)
	job := submitNFOExportMode(t, f, f.registration.Library.ID, "child-cancelled", domain.NFOExportMissing)
	repo := &hookedNFOExport{Store: f.s}
	repo.afterSubmit = func(n int, lease domain.JobLease) {
		if n != 1 {
			return
		}
		// Only the child is cancelled; the export carries on with the next chunk.
		for _, child := range nfoExportChildren(t, f, lease.Job.ID) {
			if _, err := f.s.CancelJob(f.ctx, f.a, child.ID); err != nil {
				t.Error(err)
			}
		}
	}
	stop := startNFOExportRunner(t, f, metadata, repo)
	defer stop()
	if done := waitNFOExportJob(t, f, job.ID); done.State != domain.JobSucceeded {
		t.Fatal("job did not succeed", done)
	}
	children := nfoExportChildren(t, f, job.ID)
	var firstEntries int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_entries WHERE job_id=$1::uuid`, children[0].ID).Scan(&firstEntries); err != nil || len(children) < 2 || children[0].State != domain.JobCancelled || firstEntries < 1 {
		t.Fatal("the first child was not cancelled", children, firstEntries, err)
	}
	want.Written -= firstEntries
	want.Failed = firstEntries
	requireNFOExportCounts(t, f, job.ID, want)
	// The cancelled chunk wrote nothing; every other item is as in a full run.
	chunk := map[string]bool{}
	rows, err := f.s.Pool.Query(f.ctx, `SELECT item_id::text FROM nfo_write_entries WHERE job_id=$1::uuid`, children[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		chunk[id] = true
	}
	rows.Close()
	for i, it := range items {
		_, statErr := os.Lstat(it.path)
		switch {
		case chunk[it.id] && statErr == nil:
			t.Fatalf("item %d of the cancelled child has an NFO", i)
		case !chunk[it.id] && it.created() && statErr != nil:
			t.Fatalf("item %d outside the cancelled child was not created", i)
		}
	}
	requireNFOExportClean(t, f, job.ID)
}

func TestNFOExportMissingCancellation(t *testing.T) {
	t.Run("child cancelled before it ran", func(t *testing.T) {
		f := newJobFixture(t)
		metadata := nfoApplyTestMetadata(t, f)
		items := nfoMissingLibrary(t, f, f.registration.Library.ID, 4, 30)
		job := submitNFOExportMode(t, f, f.registration.Library.ID, "cancel-early", domain.NFOExportMissing)
		repo := &hookedNFOExport{Store: f.s, afterSubmit: func(n int, lease domain.JobLease) {
			if n == 1 {
				if _, err := f.s.CancelJob(f.ctx, f.a, lease.Job.ID); err != nil {
					t.Error(err)
				}
			}
		}}
		stop := startNFOExportRunner(t, f, metadata, repo)
		defer stop()
		done := waitNFOExportJob(t, f, job.ID)
		children := nfoExportChildren(t, f, job.ID)
		if done.State != domain.JobCancelled || len(children) != 1 || children[0].State != domain.JobCancelled {
			t.Fatal("job not cancelled or later chunks were submitted", done, children)
		}
		entries := nfoMissingEntries(t, f, job.ID)
		state, err := f.s.GetNFOExportState(f.ctx, f.a, job.ID)
		if err != nil || state.Chunks != 1 || state.ChildJobID != nil || state.Written != 0 || entries < 1 || state.Failed != entries {
			t.Fatalf("state after cancellation: %+v %d %v", state, entries, err)
		}
		// Nothing was created; the existing files are as they were.
		requireNFOMissingFiles(t, items, "00000000-0000-0000-0000-000000000000", nil)
		requireNFOExportClean(t, f, job.ID)
	})
	t.Run("cancelled between chunks", func(t *testing.T) {
		f := newJobFixture(t)
		metadata := nfoApplyTestMetadata(t, f)
		items := nfoMissingLibrary(t, f, f.registration.Library.ID, 6, 40)
		job := submitNFOExportMode(t, f, f.registration.Library.ID, "cancel-late", domain.NFOExportMissing)
		repo := &hookedNFOExport{Store: f.s}
		var cancelled atomic.Bool
		repo.afterNextStep = func(_ int, lease domain.JobLease, step domain.NFOExportStep) {
			// The first child has finished and is folded in: stop before the next chunk.
			if !step.Waiting && step.Settle == nil && step.Chunk == 1 && cancelled.CompareAndSwap(false, true) {
				if _, err := f.s.CancelJob(f.ctx, f.a, lease.Job.ID); err != nil {
					t.Error(err)
				}
			}
		}
		stop := startNFOExportRunner(t, f, metadata, repo)
		defer stop()
		done := waitNFOExportJob(t, f, job.ID)
		children := nfoExportChildren(t, f, job.ID)
		if done.State != domain.JobCancelled || len(children) != 1 || children[0].State != domain.JobSucceeded {
			t.Fatal("expected a cancelled job with one finished child", done, children)
		}
		entries := nfoMissingEntries(t, f, job.ID)
		state, err := f.s.GetNFOExportState(f.ctx, f.a, job.ID)
		if err != nil || state.CursorItemID == nil || state.Chunks != 1 || entries < 1 || state.Written != entries || state.Failed != 0 || state.ChildJobID != nil {
			t.Fatalf("state after cancellation: %+v %d %v", state, entries, err)
		}
		// The first chunk is written; nothing after its cursor is touched.
		requireNFOMissingFiles(t, items, *state.CursorItemID, nil)
		var created int
		for _, it := range items {
			if it.created() && it.id <= *state.CursorItemID {
				created++
			}
		}
		if created != entries {
			t.Fatal("entries differ from the created files before the cursor", created, entries)
		}
		requireNFOExportClean(t, f, job.ID)
	})
}

func TestNFOExportMissingResumesAfterLeaseLoss(t *testing.T) {
	f := newJobFixture(t)
	metadata := nfoApplyTestMetadata(t, f)
	oracleLibrary, err := f.s.RegisterLibrary(f.ctx, "oracle", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oracleItems := nfoMissingLibrary(t, f, oracleLibrary.Library.ID, 4, 56)
	stopOracle := startNFOExportRunner(t, f, metadata, f.s)
	oracleJob := submitNFOExportMode(t, f, oracleLibrary.Library.ID, "oracle", domain.NFOExportMissing)
	if done := waitNFOExportJob(t, f, oracleJob.ID); done.State != domain.JobSucceeded {
		t.Fatal("uninterrupted export", done)
	}
	stopOracle()
	oracleState := requireNFOExportCounts(t, f, oracleJob.ID, nfoMissingWant(oracleItems))
	if oracleState.Chunks < 3 {
		t.Fatal("the library needs at least three chunks", oracleState)
	}
	want := nfoExportFiles(t, nfoMissingItems(oracleItems))
	for _, tc := range []struct {
		name string
		key  string
		hook func(h *hookedNFOExport, expire func(domain.JobLease))
	}{
		{"after the child was recorded", "after", func(h *hookedNFOExport, expire func(domain.JobLease)) {
			h.afterSubmit = func(n int, lease domain.JobLease) {
				if n == 2 {
					expire(lease)
				}
			}
		}},
		{"before the child was recorded", "before", func(h *hookedNFOExport, expire func(domain.JobLease)) {
			h.beforeSubmit = func(n int, lease domain.JobLease) {
				if n == 2 {
					expire(lease)
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registration, err := f.s.RegisterLibrary(f.ctx, "resume-"+tc.key, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			items := nfoMissingLibrary(t, f, registration.Library.ID, 4, 56)
			job := submitNFOExportMode(t, f, registration.Library.ID, "resume-"+tc.key, domain.NFOExportMissing)
			repo := &hookedNFOExport{Store: f.s}
			var expired atomic.Int32
			tc.hook(repo, func(lease domain.JobLease) {
				// The owner stalls past its lease, once.
				expired.Add(1)
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, lease.Job.ID); err != nil {
					t.Error(err)
				}
			})
			stop := startNFOExportRunner(t, f, metadata, repo)
			defer stop()
			done := waitNFOExportJob(t, f, job.ID)
			// The expiry fires from a hook that runs exactly once per submission
			// number; a later claim may also be late under load, so the number of
			// attempts is not asserted beyond "claimed again".
			if done.State != domain.JobSucceeded || done.Attempts < 2 || expired.Load() != 1 {
				t.Fatal("job did not resume and succeed", done, expired.Load())
			}
			state := requireNFOExportCounts(t, f, job.ID, nfoMissingWant(items))
			children := nfoExportChildren(t, f, job.ID)
			if state.Chunks < 3 || len(children) != state.Chunks {
				t.Fatalf("a child was duplicated or lost: %d chunks, %+v", state.Chunks, children)
			}
			for i, child := range children {
				if child.State != domain.JobSucceeded || child.Key != domain.NFOExportChildKey(job.ID, i+1) {
					t.Fatal("child", i, child)
				}
			}
			if entries := nfoMissingEntries(t, f, job.ID); entries != state.Written {
				t.Fatal("an entry was duplicated or lost", entries, state.Written)
			}
			got := nfoExportFiles(t, nfoMissingItems(items))
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("item %d differs from an uninterrupted run:\n got:  %s\n want: %s", i, got[i], want[i])
				}
			}
			requireNFOMissingFiles(t, items, "", nil)
			requireNFOExportClean(t, f, job.ID)
		})
	}
}

func TestNFOExportMissingAdmission(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	submit := func(key, mode string) (domain.Job, bool, error) {
		return f.s.SubmitNFOExportModeJob(f.ctx, f.a, library, key, domain.JobPriorityManual, mode, f.policy)
	}
	if _, _, err := submit("off", domain.NFOExportMissing); !errors.Is(err, domain.ErrNFOWriteDisabled) {
		t.Fatal("library without NFO policy accepted a missing-mode export", err)
	}
	nfoExportEnable(t, f, library)
	for _, mode := range []string{"", "MISSING", "none", "missing "} {
		if _, _, err := submit("bad-mode", mode); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("mode %q accepted: %v", mode, err)
		}
	}
	first, replayed, err := submit("once", domain.NFOExportMissing)
	if err != nil || replayed {
		t.Fatal("first submission", err)
	}
	state, err := f.s.GetNFOExportState(f.ctx, f.a, first.ID)
	if err != nil || state.Mode != domain.NFOExportMissing || state.SkippedExisting != 0 {
		t.Fatal("mode not stored", state, err)
	}
	replay, replayed, err := submit("once", domain.NFOExportMissing)
	if err != nil || !replayed || replay.ID != first.ID {
		t.Fatal("replay with the same mode", err, replayed, replay)
	}
	if _, _, err = submit("once", domain.NFOExportAll); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("key reused with another mode accepted", err)
	}
	if _, _, err = f.s.SubmitNFOExportJob(f.ctx, f.a, library, "once", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("the all-mode entry point accepted a missing-mode key", err)
	}
	if _, _, err = f.s.SubmitNFOExportModeJob(f.ctx, f.a, library, "once", domain.JobPriorityBackground, domain.NFOExportMissing, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("key reused with another priority accepted", err)
	}
	if _, _, err = submit("busy", domain.NFOExportMissing); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("second active job accepted", err)
	}
	// Writes revoked after the job was queued: the replay still answers.
	if _, err = f.s.SetNFOWritePolicy(f.ctx, f.a, library, false); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err = submit("once", domain.NFOExportMissing); err != nil || !replayed {
		t.Fatal("replay after the write permission was revoked", err)
	}
	// The other direction: a key made by an all-mode export.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',finished_at=clock_timestamp() WHERE id=$1::uuid`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetNFOWritePolicy(f.ctx, f.a, library, true); err != nil {
		t.Fatal(err)
	}
	all := submitNFOExport(t, f, library, "all-first")
	if _, _, err = submit("all-first", domain.NFOExportMissing); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("an all-mode key was reused for a missing-mode export", err)
	}
	if got, err := f.s.GetNFOExportState(f.ctx, f.a, all.ID); err != nil || got.Mode != domain.NFOExportAll {
		t.Fatal("default mode", got, err)
	}
	var audited int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='nfo.export_submitted' AND target_id=$1 AND after_state->>'mode'='missing'`, first.ID).Scan(&audited); err != nil || audited != 1 {
		t.Fatal("the mode is not in the audit event", audited, err)
	}
}

func TestNFOExportMissingOverHTTP(t *testing.T) {
	f := newJobFixture(t)
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	lib := f.registration.Library.ID
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoMissingLibrary(t, f, lib, 1, 10)
	if _, err := f.s.SetNFOWritePolicy(f.ctx, f.a, lib, false); err != nil {
		t.Fatal(err)
	}
	jobs, err := app.NewJobsWithScanStages(f.s, f.policy, f.s, app.ScanServices{NFOAdmin: f.s, NFOQueries: f.s, Images: f.s, NFOAvailable: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if jobs, err = app.NewJobsWithNFOWrite(jobs, f.s); err != nil {
		t.Fatal(err)
	}
	if jobs, err = app.NewJobsWithNFOExport(jobs, f.s); err != nil {
		t.Fatal(err)
	}
	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(f.s, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Resources: config.DefaultResourcesConfig(), Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test", MaxConnections: 16, MaxStreams: 2, RequestTimeoutSeconds: 5, EnableAccounts: true, Accounts: config.DefaultAccountsConfig(), EnableJobs: true, Jobs: config.DefaultJobsConfig()}
	handler, err := httpapi.NewWithImages(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, jobs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, key string) (int, http.Header, []byte) {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+grant.Token)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, w.Header(), w.Body.Bytes()
	}
	data := func(raw []byte, target any) {
		t.Helper()
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &envelope) != nil || json.Unmarshal(envelope.Data, target) != nil {
			t.Fatalf("response is not an envelope: %s", raw)
		}
	}
	path := "/api/v1/libraries/" + lib + "/nfo/export"
	if status, _, raw := call("POST", path, `{"mode":"bogus"}`, "m-1"); status != 400 || !strings.Contains(string(raw), "invalid_request") {
		t.Fatalf("invalid mode: %d %s", status, raw)
	}
	if status, _, raw := call("POST", path, `{"mode":"missing"}`, "m-1"); status != 409 || !strings.Contains(string(raw), `"nfo_write_disabled"`) {
		t.Fatalf("missing mode with writes disabled: %d %s", status, raw)
	}
	if _, err = f.s.SetNFOWritePolicy(f.ctx, f.a, lib, true); err != nil {
		t.Fatal(err)
	}
	status, header, raw := call("POST", path, `{"mode":"missing"}`, "m-1")
	var job, again domain.Job
	data(raw, &job)
	if status != 202 || header.Get("Location") != "/api/v1/jobs/"+job.ID || job.Kind != domain.JobNFOExport {
		t.Fatalf("submit: %d %s", status, raw)
	}
	status, header, raw = call("POST", path, `{"mode":"missing"}`, "m-1")
	data(raw, &again)
	if status != 200 || header.Get("Idempotency-Replayed") != "true" || again.ID != job.ID {
		t.Fatalf("replay: %d %s", status, raw)
	}
	for _, body := range []string{`{}`, `{"mode":"all"}`} {
		if status, _, raw = call("POST", path, body, "m-1"); status != 409 || !strings.Contains(string(raw), `"conflict"`) {
			t.Fatalf("key reused with another mode %s: %d %s", body, status, raw)
		}
	}
	stop := startNFOExportRunner(t, f, metadata, f.s)
	defer stop()
	deadline := time.Now().Add(120 * time.Second)
	for {
		_, _, raw = call("GET", "/api/v1/jobs/"+job.ID, "", "")
		data(raw, &job)
		if job.State == domain.JobSucceeded {
			break
		}
		if job.State == domain.JobFailed || job.State == domain.JobCancelled || time.Now().After(deadline) {
			t.Fatalf("job did not succeed: %s", raw)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var state domain.NFOExportJobState
	status, _, raw = call("GET", "/api/v1/jobs/"+job.ID+"/nfo-export", "", "")
	data(raw, &state)
	var public map[string]json.RawMessage
	data(raw, &public)
	if status != 200 || state.Mode != domain.NFOExportMissing || nfoExportCounts(state) != nfoMissingWant(items) || string(public["mode"]) != `"missing"` || public["skippedExisting"] == nil {
		t.Fatalf("state: %d %s", status, raw)
	}
	if strings.Contains(string(raw), items[0].path) {
		t.Fatalf("state leaks a path: %s", raw)
	}
	requireNFOMissingFiles(t, items, "", nil)
}

func TestNFOExportMissingMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	nfoExportEnable(t, f, f.registration.Library.ID)
	// An all-mode job is not retained state of this migration.
	job := submitNFOExport(t, f, f.registration.Library.ID, "round-trip")
	want := downgradeAboveMigration(t, f, "nfo_export_missing")
	jobMetricMigration(t, f, "down", want-1)
	var removed bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='nfo_export_job_state' AND column_name IN ('mode','skipped_existing'))`).Scan(&removed); err != nil || !removed {
		t.Fatal("empty downgrade left the mode columns", err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("round-trip schema is not ready", err)
	}
	state, err := f.s.GetNFOExportState(f.ctx, f.a, job.ID)
	if err != nil || state.Mode != domain.NFOExportAll || state.SkippedExisting != 0 {
		t.Fatal("the job did not survive the round trip as an all-mode export", state, err)
	}
	// The constraints hold on the migrated schema.
	for _, set := range []string{"mode='other'", "skipped_existing=-1", "skipped_existing=1"} {
		if _, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_export_job_state SET `+set+` WHERE job_id=$1::uuid`, job.ID); err == nil {
			t.Fatal("the state accepts", set)
		}
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_export_job_state SET mode='missing',skipped_existing=3 WHERE job_id=$1::uuid`, job.ID); err != nil {
		t.Fatal("a missing-mode state with skipped existing items is refused", err)
	}
}

func TestNFOExportMissingMigrationRefusesRetainedData(t *testing.T) {
	f := newJobFixture(t)
	nfoExportEnable(t, f, f.registration.Library.ID)
	job := submitNFOExportMode(t, f, f.registration.Library.ID, "retained-missing", domain.NFOExportMissing)
	want := downgradeAboveMigration(t, f, "nfo_export_missing")
	nfoMigrationDenied(t, f, "000091_nfo_export_missing.down.sql")
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("retained missing-mode export downgraded")
	}
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != want-1 || !dirty {
		t.Fatal("refused downgrade lost its dirty status", version, dirty, err)
	}
	var kept int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_export_job_state WHERE job_id=$1::uuid AND mode='missing'`, job.ID).Scan(&kept); err != nil || kept != 1 {
		t.Fatal("refused downgrade removed the state row", kept, err)
	}
}
