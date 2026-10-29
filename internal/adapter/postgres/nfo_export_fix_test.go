package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

// The categories of the fix library. By movie index modulo 11:
//
//	0 valid                                  -> unchanged
//	1 title missing, a stored title          -> fixed (title)
//	2 title missing, no stored title         -> unfixable
//	3 title, no identifier                   -> unchanged (no ID is not an issue; no ID is stamped)
//	4 title and identifier missing, stored   -> fixed (title; the rewrite also adds a Jelee ID)
//	5 title missing, imdbid and tmdbid       -> fixed (title), provider IDs kept
//	6 title missing, a manual jelee uniqueid -> fixed (title), the manual value kept
//	7 invalid XML                            -> unfixable
//	8 invalid date and title missing         -> unfixable (an error issue blocks the file)
//	9 unknown root                           -> unfixable
//	10 no NFO                                -> skipped_missing
type nfoFixItem struct {
	nfoMissingItem
	category int // -1 for the series, episodes and path items
	label    string
	outcome  string // unchanged, fixed, unfixable, missing
	title    string // the stored title, "" when none
	keeps    []string
	hadID    bool
	wantCode []string // issue codes of the validation job before the fix
	failure  domain.NFOFailureCode
}

func nfoFixCategoryBody(category, n int) (body string, title bool, id bool) {
	note, custom := fmt.Sprintf("<!-- note %d -->", n), fmt.Sprintf(`<customtag attr="x%d">keep %d</customtag>`, n, n)
	wrap := func(inner string) string { return "<movie>\n  " + note + "\n" + inner + "  " + custom + "\n</movie>\n" }
	switch category {
	case 0:
		return wrap(fmt.Sprintf("  <title>Valid %d</title>\n  <uniqueid type=\"imdb\" default=\"true\">tt%07d</uniqueid>\n", n, n)), true, true
	case 1, 2:
		return wrap(fmt.Sprintf("  <uniqueid type=\"tmdb\">%d</uniqueid>\n", 1000+n)), false, true
	case 3:
		return wrap(fmt.Sprintf("  <title>Has title %d</title>\n", n)), true, false
	case 4:
		return wrap("  <plot>Only a plot</plot>\n"), false, false
	case 5:
		return wrap(fmt.Sprintf("  <imdbid>tt%07d</imdbid>\n  <tmdbid>%d</tmdbid>\n", n, 2000+n)), false, true
	case 6:
		return wrap(fmt.Sprintf("  <uniqueid type=\"jelee\">manual-%d</uniqueid>\n", n)), false, true
	case 7:
		return "<movie><title>broken", false, false
	case 8:
		return wrap(fmt.Sprintf("  <uniqueid type=\"x\">%d</uniqueid>\n  <premiered>last week</premiered>\n", n)), false, true
	case 9:
		return "<thing>\n  " + note + "\n  <title>Unknown root</title>\n  " + custom + "\n</thing>\n", true, false
	}
	return "", false, false
}

// nfoFixLibrary fills a read-write library with `cycles` rounds of the eleven
// movie categories, one series, an episode that needs a repair and one that
// does not. Every NFO is written by the test; stored titles are manual.
func nfoFixLibrary(t *testing.T, f jobFixture, library string, cycles int) []*nfoFixItem {
	t.Helper()
	var root, rootID string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path,id::text FROM library_roots WHERE library_id=$1::uuid`, library).Scan(&root, &rootID); err != nil {
		t.Fatal(err)
	}
	nfoExportEnable(t, f, library)
	var items []*nfoFixItem
	add := func(kind, nfoName, dir, media, body, stored string) *nfoFixItem {
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
		it := &nfoFixItem{category: -1, title: stored}
		it.id, it.nfo, it.kind, it.root = id, nfoName, kind, map[string]string{"Movie": "movie", "Series": "tvshow", "Season": "season", "Episode": "episodedetails"}[kind]
		it.path = filepath.Join(root, filepath.FromSlash(nfoName))
		it.body, it.existing = body, body != ""
		if body != "" {
			if err := os.WriteFile(it.path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(it.path)
			if err != nil {
				t.Fatal(err)
			}
			it.before = info
		}
		if stored != "" {
			value := stored
			if _, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, id, 1, []domain.ItemMetadataPatch{{Field: "title", Value: &value}}, nil); err != nil {
				t.Fatal(err)
			}
		}
		items = append(items, it)
		return it
	}
	for n := 0; n < cycles*11; n++ {
		category := n % 11
		body, hasTitle, hasID := nfoFixCategoryBody(category, n)
		stored := ""
		if category != 0 && category != 2 && category != 7 && category != 9 {
			stored = fmt.Sprintf("Stored %d & Co", n)
		}
		it := add("Movie", fmt.Sprintf("m%03d.nfo", n), ".", fmt.Sprintf("m%03d.mkv", n), body, stored)
		it.category, it.label, it.hadID = category, fmt.Sprintf("movie %d (category %d)", n, category), hasID
		it.keeps = []string{fmt.Sprintf("<!-- note %d -->", n), fmt.Sprintf(`<customtag attr="x%d">keep %d</customtag>`, n, n)}
		switch category {
		case 0:
			it.outcome = "unchanged"
		case 1:
			it.outcome, it.wantCode = "fixed", []string{"nfo_title_missing"}
		case 2:
			it.outcome, it.wantCode = "unfixable", []string{"nfo_title_missing"}
		case 3:
			// A title without an identifier is no issue: a fix never stamps an ID by itself.
			it.outcome = "unchanged"
		case 4:
			// The title repair is a rewrite, which also adds the Jelee ID (EnsureID).
			it.outcome, it.wantCode = "fixed", []string{"nfo_title_missing"}
		case 5, 6:
			it.outcome, it.wantCode = "fixed", []string{"nfo_title_missing"}
			if category == 5 {
				it.keeps = append(it.keeps, fmt.Sprintf("<imdbid>tt%07d</imdbid>", n), fmt.Sprintf("<tmdbid>%d</tmdbid>", 2000+n))
			} else {
				it.keeps = append(it.keeps, fmt.Sprintf("<uniqueid type=\"jelee\">manual-%d</uniqueid>", n))
			}
		case 7:
			it.outcome, it.failure = "unfixable", domain.NFOFailureInvalidXML
		case 8:
			it.outcome, it.wantCode = "unfixable", []string{"nfo_invalid_date", "nfo_title_missing"}
		case 9:
			it.outcome, it.wantCode = "unfixable", []string{"nfo_unknown_root"}
		case 10:
			it.outcome = "missing"
		}
		_ = hasTitle
	}
	show := "Show 00"
	series := add("Series", show+"/tvshow.nfo", show, "", "<tvshow>\n  <!-- series note -->\n  <uniqueid type=\"tvdb\">81189</uniqueid>\n  <customtag attr=\"s\">keep series</customtag>\n</tvshow>\n", "The Show & Co")
	series.label, series.outcome, series.hadID, series.wantCode = "series", "fixed", true, []string{"nfo_title_missing"}
	series.keeps = []string{"<!-- series note -->", `<customtag attr="s">keep series</customtag>`, `<uniqueid type="tvdb">81189</uniqueid>`}
	episodeA := add("Episode", show+"/Season 1/e01.nfo", show+"/Season 1", show+"/Season 1/e01.mkv", "<episodedetails>\n  <!-- ep note -->\n  <uniqueid type=\"tvdb\">1</uniqueid>\n  <season>1</season>\n  <episode>1</episode>\n  <customtag attr=\"e\">keep episode</customtag>\n</episodedetails>\n", "Pilot & Co")
	episodeA.label, episodeA.outcome, episodeA.hadID, episodeA.wantCode = "episode needing a title", "fixed", true, []string{"nfo_title_missing"}
	episodeA.keeps = []string{"<!-- ep note -->", `<customtag attr="e">keep episode</customtag>`, `<uniqueid type="tvdb">1</uniqueid>`}
	episodeB := add("Episode", show+"/Season 1/e02.nfo", show+"/Season 1", show+"/Season 1/e02.mkv", "<episodedetails>\n  <title>Second</title>\n  <uniqueid type=\"tvdb\">2</uniqueid>\n  <season>1</season>\n  <episode>2</episode>\n</episodedetails>\n", "Second")
	episodeB.label, episodeB.outcome = "clean episode", "unchanged"
	return items
}

func nfoFixWant(items []*nfoFixItem) domain.NFOExportCounts {
	var c domain.NFOExportCounts
	for _, it := range items {
		switch it.outcome {
		case "unchanged":
			c.Unchanged++
		case "fixed":
			c.Fixed++
		case "unfixable":
			c.Unfixable++
		case "missing":
			c.SkippedMissing++
		}
	}
	return c
}

func nfoFixMissingItems(items []*nfoFixItem) []nfoMissingItem {
	out := make([]nfoMissingItem, len(items))
	for i, it := range items {
		out[i] = it.nfoMissingItem
	}
	return out
}

func nfoFixExportItems(items []*nfoFixItem) []nfoExportItem {
	out := make([]nfoExportItem, len(items))
	for i, it := range items {
		out[i] = it.nfoExportItem
	}
	return out
}

var nfoFixGeneratedID = regexp.MustCompile(`<uniqueid type="jelee">[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}</uniqueid>`)

// requireNFOFixFiles checks every file against the outcome of its item: fixed
// files differ from the original only by the repair, keep every unknown tag,
// comment and identifier and have a backup equal to the original; every other
// file is byte-identical, with the same inode and time and no backup. upTo
// limits the check to items up to that ID (items after it must be untouched).
func requireNFOFixFiles(t *testing.T, items []*nfoFixItem, upTo string) {
	t.Helper()
	for _, it := range items {
		_, bakErr := os.Lstat(it.path + ".jelee.bak")
		data, err := os.ReadFile(it.path)
		if it.outcome != "fixed" || upTo != "" && it.id > upTo {
			if it.outcome == "missing" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s: an NFO was created", it.label)
				}
			} else if info, statErr := os.Stat(it.path); err != nil || string(data) != it.body || statErr != nil || !os.SameFile(it.before, info) || !info.ModTime().Equal(it.before.ModTime()) {
				t.Fatalf("%s: the file was touched: %q %v", it.label, data, err)
			}
			if !errors.Is(bakErr, os.ErrNotExist) {
				t.Fatalf("%s: has a backup", it.label)
			}
			continue
		}
		got := string(data)
		backup, err2 := os.ReadFile(it.path + ".jelee.bak")
		if err != nil || bakErr != nil || err2 != nil || string(backup) != it.body || got == it.body {
			t.Fatalf("%s: not rewritten with a backup of the original: %v %v", it.label, err, bakErr)
		}
		for _, keep := range it.keeps {
			if !strings.Contains(got, keep) {
				t.Fatalf("%s: lost %s:\n%s", it.label, keep, got)
			}
		}
		if !strings.Contains(got, "<title>"+xmlText(it.title)+"</title>") && it.hadTitle() {
			t.Fatalf("%s: title not set:\n%s", it.label, got)
		}
		generated := len(nfoFixGeneratedID.FindAllString(got, -1))
		if it.hadID && generated != 0 || !it.hadID && generated != 1 || strings.Count(got, "<uniqueid")+strings.Count(got, "<imdbid")+strings.Count(got, "<tmdbid") != strings.Count(it.body, "<uniqueid")+strings.Count(it.body, "<imdbid")+strings.Count(it.body, "<tmdbid")+generated {
			t.Fatalf("%s: identifiers changed (generated %d):\n%s", it.label, generated, got)
		}
		document, readErr := nfo.Read(context.Background(), bytes.NewReader(data), domain.NFODefaultSourceBytes)
		if readErr != nil {
			t.Fatalf("%s: the repaired file is unreadable: %v", it.label, readErr)
		}
		for _, issue := range document.Issues {
			if issue.Code == "nfo_title_missing" || issue.Severity == "error" {
				t.Fatalf("%s: still has %+v", it.label, issue)
			}
		}
	}
}

// hadTitle reports whether a repair of the item sets its title: the title was
// missing from the original.
func (it *nfoFixItem) hadTitle() bool {
	for _, code := range it.wantCode {
		if code == "nfo_title_missing" {
			return true
		}
	}
	return false
}

func submitNFOFix(t *testing.T, f jobFixture, library, key string) domain.Job {
	t.Helper()
	return submitNFOExportMode(t, f, library, key, domain.NFOExportFix)
}

// nfoFixObservation is what the validation job stored for one file.
type nfoFixObservation struct {
	status  string
	failure domain.NFOFailureCode
	entries int
	errors  int64
	codes   []string
}

// nfoValidationObservations reads every current observation of the library
// and its issues, by root-relative path.
func nfoValidationObservations(t *testing.T, f jobFixture, library string) map[string]nfoFixObservation {
	t.Helper()
	identity := domain.DefaultNFOIdentity()
	out := map[string]nfoFixObservation{}
	cursor := ""
	for {
		page, err := f.s.ListNFOObservations(f.ctx, f.a, library, cursor, domain.NFOObservationPageMax, identity)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			o := nfoFixObservation{status: item.Status, failure: item.FailureCode, entries: item.Entries, errors: item.ErrorCount}
			if item.IssueCount > 0 {
				issues, err := f.s.GetNFOObservationIssues(f.ctx, f.a, library, item.ID, 0, domain.NFOIssuesPageMax, identity)
				if err != nil {
					t.Fatal(err)
				}
				for _, issue := range issues.Issues {
					o.codes = append(o.codes, issue.Code)
				}
			}
			out[item.Path] = o
		}
		if page.NextCursor == "" {
			return out
		}
		cursor = page.NextCursor
	}
}

// runNFOValidation submits the library validation job and runs it with the
// real scanner and the given reader on its own runner.
func runNFOValidation(t *testing.T, f jobFixture, library, key string, reader app.NFOReader) domain.Job {
	t.Helper()
	identity := reader.Identity()
	if err := f.s.EnsureNFOCachePolicy(f.ctx, domain.DefaultNFOCachePolicy()); err != nil {
		t.Fatal(err)
	}
	job, _, err := f.s.SubmitScanWithStages(f.ctx, f.a, library, key, domain.JobPriorityManual, domain.ScanIntent{NFO: true}, f.policy, nil, &identity)
	if err != nil {
		t.Fatal("submit validation", err)
	}
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval = 1, 100*time.Millisecond
	opts.NFO = &jobs.NFOOptions{Repository: f.s, Reader: reader, MaxConcurrent: 1}
	runner, err := jobs.New(f.s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal("new runner", err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal("start runner", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	done := waitNFOExportJob(t, f, job.ID)
	if done.State != domain.JobSucceeded {
		t.Fatal("validation job did not succeed", done)
	}
	return done
}

func nfoFixReader(t *testing.T) *nfo.SummaryReader {
	t.Helper()
	reader, err := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

// requireNFOFixObservations checks the stored validation of every file of the
// library against the expectations of its item, before or after the fix.
func requireNFOFixObservations(t *testing.T, got map[string]nfoFixObservation, items []*nfoFixItem, afterFix bool) {
	t.Helper()
	for _, it := range items {
		if it.category == -2 {
			continue // the path items are checked by their test
		}
		o, ok := got[it.nfo]
		if it.outcome == "missing" {
			if ok {
				t.Fatalf("%s: observed although it has no NFO", it.label)
			}
			continue
		}
		if !ok {
			t.Fatalf("%s: has no observation", it.label)
		}
		want := it.wantCode
		if afterFix && it.outcome == "fixed" {
			want = nil
		}
		if o.failure != it.failure {
			t.Fatalf("%s: failure %q, want %q", it.label, o.failure, it.failure)
		}
		have := map[string]bool{}
		for _, code := range o.codes {
			have[code] = true
		}
		if len(have) != len(want) {
			t.Fatalf("%s (afterFix=%t): issue codes %v, want %v", it.label, afterFix, o.codes, want)
		}
		for _, code := range want {
			if !have[code] {
				t.Fatalf("%s (afterFix=%t): issue codes %v, want %v", it.label, afterFix, o.codes, want)
			}
		}
	}
}

func TestNFOExportFixAdmission(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	submit := func(key, mode string) (domain.Job, bool, error) {
		return f.s.SubmitNFOExportModeJob(f.ctx, f.a, library, key, domain.JobPriorityManual, mode, f.policy)
	}
	// Fix needs the same write permission as every export.
	if _, _, err := submit("off", domain.NFOExportFix); !errors.Is(err, domain.ErrNFOWriteDisabled) {
		t.Fatal("a library without NFO policy accepted a fix", err)
	}
	nfoExportEnable(t, f, library)
	if _, err := f.s.SetNFOWritePolicy(f.ctx, f.a, library, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := submit("no-write", domain.NFOExportFix); !errors.Is(err, domain.ErrNFOWriteDisabled) {
		t.Fatal("a read-only library without the write permission accepted a fix", err)
	}
	if _, err := f.s.SetNFOWritePolicy(f.ctx, f.a, library, true); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"", "FIX", "repair", "fix "} {
		if _, _, err := submit("bad-mode", mode); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("mode %q accepted: %v", mode, err)
		}
	}
	first, replayed, err := submit("once", domain.NFOExportFix)
	if err != nil || replayed {
		t.Fatal(err, replayed)
	}
	state, err := f.s.GetNFOExportState(f.ctx, f.a, first.ID)
	if err != nil || state.Mode != domain.NFOExportFix || state.Fixed != 0 || state.Unfixable != 0 {
		t.Fatal("mode not stored", state, err)
	}
	if replay, replayed, err := submit("once", domain.NFOExportFix); err != nil || !replayed || replay.ID != first.ID {
		t.Fatal("replay", err, replayed)
	}
	for _, mode := range []string{domain.NFOExportAll, domain.NFOExportMissing} {
		if _, _, err = submit("once", mode); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("a fix key reused for mode %s: %v", mode, err)
		}
	}
	if _, _, err = submit("busy", domain.NFOExportFix); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("a second active job was accepted", err)
	}
	var audited int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='nfo.export_submitted' AND target_id=$1 AND after_state->>'mode'='fix'`, first.ID).Scan(&audited); err != nil || audited != 1 {
		t.Fatal("the mode is not in the audit event", audited, err)
	}
}

func TestNFOExportFixMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	nfoExportEnable(t, f, f.registration.Library.ID)
	job := submitNFOExportMode(t, f, f.registration.Library.ID, "round-trip", domain.NFOExportMissing)
	want := downgradeAboveMigration(t, f, "nfo_export_fix")
	jobMetricMigration(t, f, "down", want-1)
	var removed bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='nfo_export_job_state' AND column_name IN ('fixed','unfixable'))`).Scan(&removed); err != nil || !removed {
		t.Fatal("an empty downgrade left the counters", err)
	}
	// The older mode constraint is back: fix is no longer a mode.
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_export_job_state SET mode='fix' WHERE job_id=$1::uuid`, job.ID); err == nil {
		t.Fatal("the downgraded schema accepts the fix mode")
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("round-trip schema is not ready", err)
	}
	state, err := f.s.GetNFOExportState(f.ctx, f.a, job.ID)
	if err != nil || state.Mode != domain.NFOExportMissing || state.Fixed != 0 || state.Unfixable != 0 {
		t.Fatal("the job did not survive the round trip", state, err)
	}
	for _, set := range []string{"mode='other'", "fixed=-1", "unfixable=-1", "fixed=1", "unfixable=1", "mode='fix',written=1"} {
		if _, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_export_job_state SET `+set+` WHERE job_id=$1::uuid`, job.ID); err == nil {
			t.Fatal("the state accepts", set)
		}
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_export_job_state SET mode='fix',fixed=4,unfixable=2,unchanged=3 WHERE job_id=$1::uuid`, job.ID); err != nil {
		t.Fatal("a fix-mode state with counters is refused", err)
	}
}

func TestNFOExportFixMigrationRefusesRetainedData(t *testing.T) {
	f := newJobFixture(t)
	nfoExportEnable(t, f, f.registration.Library.ID)
	job := submitNFOFix(t, f, f.registration.Library.ID, "retained-fix")
	want := downgradeAboveMigration(t, f, "nfo_export_fix")
	nfoMigrationDenied(t, f, "000092_nfo_export_fix.down.sql")
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("a retained fix-mode export downgraded")
	}
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != want-1 || !dirty {
		t.Fatal("the refused downgrade lost its dirty status", version, dirty, err)
	}
	var kept int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_export_job_state WHERE job_id=$1::uuid AND mode='fix'`, job.ID).Scan(&kept); err != nil || kept != 1 {
		t.Fatal("the refused downgrade removed the state row", kept, err)
	}
}

// nfoMutatingReader changes the file system once, just before the validation
// job looks at its first NFO: after the inventory, so the inventory still
// holds the regular files the test replaced.
type nfoMutatingReader struct {
	*nfo.SummaryReader
	once   sync.Once
	mutate func()
}

func (r *nfoMutatingReader) InspectNFOPath(ctx context.Context, source domain.NFOSource) (string, error) {
	r.once.Do(r.mutate)
	return r.SummaryReader.InspectNFOPath(ctx, source)
}

var _ app.NFOPathInspector = (*nfoMutatingReader)(nil)

func TestNFOExportFixOverHTTP(t *testing.T) {
	f := newJobFixture(t)
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	lib := f.registration.Library.ID
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoFixLibrary(t, f, lib, 1)
	if _, err := f.s.SetNFOWritePolicy(f.ctx, f.a, lib, false); err != nil {
		t.Fatal(err)
	}
	jobsService, err := app.NewJobsWithScanStages(f.s, f.policy, f.s, app.ScanServices{NFOAdmin: f.s, NFOQueries: f.s, Images: f.s, NFOAvailable: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if jobsService, err = app.NewJobsWithNFOWrite(jobsService, f.s); err != nil {
		t.Fatal(err)
	}
	if jobsService, err = app.NewJobsWithNFOExport(jobsService, f.s); err != nil {
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
	handler, err := httpapi.NewWithImages(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, jobsService, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, key string) (int, []byte) {
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
		return w.Code, w.Body.Bytes()
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
	if status, raw := call("POST", path, `{"mode":"repair"}`, "f-1"); status != 400 || !strings.Contains(string(raw), "invalid_request") {
		t.Fatalf("invalid mode: %d %s", status, raw)
	}
	if status, raw := call("POST", path, `{"mode":"fix"}`, "f-1"); status != 409 || !strings.Contains(string(raw), `"nfo_write_disabled"`) {
		t.Fatalf("fix with writes disabled: %d %s", status, raw)
	}
	if _, err = f.s.SetNFOWritePolicy(f.ctx, f.a, lib, true); err != nil {
		t.Fatal(err)
	}
	status, raw := call("POST", path, `{"mode":"fix"}`, "f-1")
	var job domain.Job
	data(raw, &job)
	if status != 202 || job.Kind != domain.JobNFOExport {
		t.Fatalf("submit: %d %s", status, raw)
	}
	if status, raw = call("POST", path, `{"mode":"missing"}`, "f-1"); status != 409 || !strings.Contains(string(raw), `"conflict"`) {
		t.Fatalf("key reused with another mode: %d %s", status, raw)
	}
	stop := startNFOExportRunner(t, f, metadata, f.s)
	defer stop()
	deadline := time.Now().Add(120 * time.Second)
	for {
		_, raw = call("GET", "/api/v1/jobs/"+job.ID, "", "")
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
	status, raw = call("GET", "/api/v1/jobs/"+job.ID+"/nfo-export", "", "")
	data(raw, &state)
	var public map[string]json.RawMessage
	data(raw, &public)
	if status != 200 || state.Mode != domain.NFOExportFix || nfoExportCounts(state) != nfoFixWant(items) || string(public["mode"]) != `"fix"` || public["fixed"] == nil || public["unfixable"] == nil {
		t.Fatalf("state: %d %s", status, raw)
	}
	if strings.Contains(string(raw), items[0].path) {
		t.Fatalf("state leaks a path: %s", raw)
	}
	requireNFOFixFiles(t, items, "")
}
