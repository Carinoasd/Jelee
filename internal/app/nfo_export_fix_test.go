package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func fixChecked(codes []string, mutate ...func(*domain.NFOItemValidation)) domain.NFOItemValidation {
	v := domain.NFOItemValidation{Status: domain.NFOItemValidationChecked, IssueCodes: codes}
	v.Summary = domain.NFOValidationSummary{SchemaVersion: 1, Status: domain.NFOStatusValid, Encoding: "UTF-8", Root: "movie", Entries: 1, WarningCount: int64(len(codes)), IssueCount: int64(len(codes))}
	for _, f := range mutate {
		f(&v)
	}
	return v
}

func TestPlanNFOExportFixRepairsOnlyWhatIsSafe(t *testing.T) {
	scope := nfoApplyScope(nfoExportWorkerActor, 3)
	stored := nfoExportMeta([]domain.ItemMetadataField{{Field: "title", Value: "Stored & Title", Source: "manual"}})
	scanned := nfoExportMeta([]domain.ItemMetadataField{{Field: "title", Value: "Scanned", Source: "existing"}})
	empty := nfoExportMeta([]domain.ItemMetadataField{{Field: "title", Value: "", Source: "manual"}})
	none := nfoExportMeta(nil)
	title, id := []string{"nfo_title_missing"}, []string{"nfo_id_missing"}
	both := []string{"nfo_id_missing", "nfo_title_missing"}
	edit := []domain.NFOWriteTextEdit{{Field: "title", Value: "Stored & Title"}}
	withSummary := func(f func(*domain.NFOValidationSummary)) func(*domain.NFOItemValidation) {
		return func(v *domain.NFOItemValidation) { f(&v.Summary) }
	}
	for _, tc := range []struct {
		name    string
		meta    domain.ItemMetadata
		v       domain.NFOItemValidation
		scope   *domain.NFOItemScope
		outcome nfoExportOutcome
		edits   []domain.NFOWriteTextEdit
	}{
		{name: "no NFO is skipped_missing, never created", meta: stored, v: domain.NFOItemValidation{Status: domain.NFOItemValidationMissing}, outcome: nfoExportMissing},
		{name: "illegal path is unfixable", meta: stored, v: domain.NFOItemValidation{Status: domain.NFOItemValidationPath, PathCode: domain.NFOPathSymlink}, outcome: nfoExportUnfixable},
		{name: "unknown status", meta: stored, v: domain.NFOItemValidation{Status: "other"}, outcome: nfoExportSkipped},
		{name: "clean file is unchanged", meta: stored, v: fixChecked(nil), outcome: nfoExportUnchanged},
		{name: "title missing with a stored title", meta: stored, v: fixChecked(title), outcome: nfoExportPrepared, edits: edit},
		{name: "title and id missing with a stored title", meta: stored, v: fixChecked(both), outcome: nfoExportPrepared, edits: edit},
		{name: "title missing, the title is only the scanned name", meta: scanned, v: fixChecked(title), outcome: nfoExportUnfixable},
		{name: "title missing, the stored title is empty", meta: empty, v: fixChecked(title), outcome: nfoExportUnfixable},
		{name: "title missing, nothing stored", meta: none, v: fixChecked(title), outcome: nfoExportUnfixable},
		{name: "title and id missing, no stored title: the id alone", meta: scanned, v: fixChecked(both), outcome: nfoExportPrepared},
		{name: "id missing: an edit-less write adds the id", meta: stored, v: fixChecked(id), outcome: nfoExportPrepared},
		{name: "other warnings only", meta: stored, v: fixChecked([]string{"nfo_season_missing", "nfo_episode_missing"}), outcome: nfoExportUnfixable},
		{name: "other warnings do not block a repair", meta: stored, v: fixChecked([]string{"nfo_conflicting_id", "nfo_title_missing"}), outcome: nfoExportPrepared, edits: edit},
		{name: "error issue blocks everything", meta: stored, v: fixChecked(both, withSummary(func(s *domain.NFOValidationSummary) { s.ErrorCount, s.IssueCount, s.Status = 1, 3, domain.NFOStatusInvalid })), outcome: nfoExportUnfixable},
		{name: "parse failure", meta: stored, v: fixChecked(nil, withSummary(func(s *domain.NFOValidationSummary) {
			s.FailureCode, s.Status, s.Encoding, s.Root, s.Entries = domain.NFOFailureInvalidXML, domain.NFOStatusInvalid, "unknown", "unknown", 0
		})), outcome: nfoExportUnfixable},
		{name: "guessed encoding is never rewritten", meta: stored, v: fixChecked(append([]string{"nfo_encoding_guessed"}, both...), withSummary(func(s *domain.NFOValidationSummary) { s.EncodingGuessed = true })), outcome: nfoExportUnfixable},
		{name: "unknown root", meta: stored, v: fixChecked(append([]string{"nfo_unknown_root"}, both...)), outcome: nfoExportUnfixable},
		{name: "wrapper", meta: stored, v: fixChecked(append([]string{"nfo_wrapper_fields_ignored"}, both...)), outcome: nfoExportUnfixable},
		{name: "wrapper kind", meta: stored, v: fixChecked(append([]string{"nfo_kind_unknown"}, both...)), outcome: nfoExportUnfixable},
		{name: "several entries", meta: stored, v: fixChecked(both, withSummary(func(s *domain.NFOValidationSummary) { s.Entries = 2 })), outcome: nfoExportUnfixable},
		{name: "root is not the item's kind", meta: stored, v: fixChecked(both, withSummary(func(s *domain.NFOValidationSummary) { s.Root = "tvshow" })), outcome: nfoExportUnfixable},
		{name: "lockdata", meta: stored, v: fixChecked(both, func(v *domain.NFOItemValidation) { v.LockData = true }), outcome: nfoExportUnfixable},
		{name: "title locked, id still repaired", meta: stored, v: fixChecked(both, func(v *domain.NFOItemValidation) { v.LockedFields = []string{"Name"} }), outcome: nfoExportPrepared},
		{name: "title locked, only the title is missing", meta: stored, v: fixChecked(title, func(v *domain.NFOItemValidation) { v.LockedFields = []string{"Name"} }), outcome: nfoExportUnfixable},
		{name: "id locked and missing: the writer would refuse", meta: stored, v: fixChecked(both, func(v *domain.NFOItemValidation) { v.LockedFields = []string{"ProviderIds"} }), outcome: nfoExportUnfixable},
		{name: "an unrelated lock changes nothing", meta: stored, v: fixChecked(title, func(v *domain.NFOItemValidation) { v.LockedFields = []string{"Genres", "Cast"} }), outcome: nfoExportPrepared, edits: edit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := scope
			if tc.scope != nil {
				s = *tc.scope
			}
			outcome, request := planNFOExportFix(tc.meta, s, tc.v)
			if outcome != tc.outcome || (request != nil) != (tc.outcome == nfoExportPrepared) {
				t.Fatalf("outcome %d request %v, want %d", outcome, request, tc.outcome)
			}
			if request == nil {
				return
			}
			if !reflect.DeepEqual(request.Edits, tc.edits) && !(len(request.Edits) == 0 && len(tc.edits) == 0) {
				t.Fatalf("edits %v, want %v", request.Edits, tc.edits)
			}
			// The request is an ordinary rewrite with a backup: it never creates a file.
			if request.ItemID != s.ItemID || request.Revision != s.Revision || request.Create || !request.CreateMissing || request.Backups != nfoExportBackups || request.BOM != "preserve" || request.MaxBytes != domain.NFODefaultSourceBytes {
				t.Fatalf("request %+v", *request)
			}
			if err := domain.ValidateNFOWritePrepareRequest(*request); err != nil {
				t.Fatal("not a valid preparation", err)
			}
		})
	}
}

func TestNFOFixRootMatchesTheItemKind(t *testing.T) {
	for kind, roots := range map[string][]string{"Movie": {"movie"}, "HomeVideo": {"movie"}, "Series": {"tvshow"}, "Season": {"season"}, "Episode": {"episode", "episodedetails"}} {
		for _, root := range []string{"movie", "tvshow", "season", "episode", "episodedetails", "wrapper", "unknown"} {
			want := false
			for _, r := range roots {
				want = want || r == root
			}
			if nfoFixRootMatches(kind, root) != want {
				t.Errorf("%s/%s", kind, root)
			}
		}
	}
	if nfoFixRootMatches("Collection", "movie") {
		t.Fatal("an unsupported kind matched")
	}
}

func TestNFOFixIDLockedNamesTheWriterLocks(t *testing.T) {
	for _, lock := range []string{"id", "UniqueId", "uniqueids", " ProviderIds ", "imdbid", "TMDBID", "tvdbid"} {
		if !nfoFixIDLocked([]string{"Genres", lock}) {
			t.Errorf("%q is an identifier lock", lock)
		}
	}
	for _, lock := range []string{"", "Name", "Overview", "Genres", "idx"} {
		if nfoFixIDLocked([]string{lock}) {
			t.Errorf("%q is not an identifier lock", lock)
		}
	}
	if nfoFixIDLocked(nil) {
		t.Fatal("no locks")
	}
}

// fixReader answers validations by item.
type fixReader struct {
	nfoApplyWorkerReader
	results map[string]domain.NFOItemValidation
	err     map[string]error
	calls   *int
}

func (r fixReader) ValidateItemNFO(_ context.Context, scope domain.NFOItemScope) (domain.NFOItemValidation, error) {
	if r.calls != nil {
		*r.calls++
	}
	if err := r.err[scope.ItemID]; err != nil {
		return domain.NFOItemValidation{}, err
	}
	return r.results[scope.ItemID], nil
}

// fixStep is an execution whose steps carry the fix mode.
type fixStep struct{ exportExecution }

func (r *fixStep) NextNFOExportStep(ctx context.Context, lease domain.JobLease, limit int) (domain.NFOExportStep, error) {
	step, err := r.exportExecution.NextNFOExportStep(ctx, lease, limit)
	step.Mode = domain.NFOExportFix
	return step, err
}

func TestNFOExportWorkerFixModeRepairsAndCountsItems(t *testing.T) {
	ids := nfoApplyIDs(12)
	title, id := []string{"nfo_title_missing"}, []string{"nfo_id_missing"}
	items := &exportItems{scanned: map[string]bool{ids[7]: true}, resolveErr: map[string]error{ids[9]: domain.ErrMetadataUnavailable}}
	calls := 0
	reader := fixReader{calls: &calls, results: map[string]domain.NFOItemValidation{
		ids[0]:  fixChecked(nil),
		ids[1]:  fixChecked(title),
		ids[2]:  fixChecked(id),
		ids[3]:  {Status: domain.NFOItemValidationMissing},
		ids[4]:  {Status: domain.NFOItemValidationPath, PathCode: domain.NFOPathCaseCollision},
		ids[5]:  fixChecked(nil, func(v *domain.NFOItemValidation) { v.Summary.FailureCode = domain.NFOFailureInvalidXML }),
		ids[6]:  fixChecked(title, func(v *domain.NFOItemValidation) { v.LockData = true }),
		ids[7]:  fixChecked(title),
		ids[8]:  fixChecked([]string{"nfo_season_missing"}),
		ids[10]: fixChecked(title),
		ids[11]: fixChecked(nil),
	}}
	preparer := &exportPreparer{}
	execution := &fixStep{exportExecution{items: ids}}
	base, err := NewLocalMetadata(items)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := base.WithNFOItemFields(reader)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewNFOExportWorker(metadata, preparer, execution, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(context.Background(), exportLease()); !errors.Is(err, ErrNFOExportYield) {
		t.Fatal("run did not yield", err)
	}
	want := domain.NFOExportCounts{Unchanged: 2, SkippedMissing: 1, SkippedOther: 1, Unfixable: 5}
	if len(execution.submits) != 1 || execution.submits[0].counts != want {
		t.Fatalf("submits %+v, want counts %+v", execution.submits, want)
	}
	if !reflect.DeepEqual(execution.submits[0].preparations, []string{"prep-" + ids[1], "prep-" + ids[2], "prep-" + ids[10]}) {
		t.Fatalf("prepared %v", execution.submits[0].preparations)
	}
	for _, key := range preparer.keys {
		if !strings.HasPrefix(key, domain.NFOExportPreparationKeyPrefix(exportLease().Job.ID, 1)) {
			t.Fatalf("preparation key %q", key)
		}
	}
	for _, request := range preparer.requests {
		switch request.ItemID {
		case ids[2]:
			if len(request.Edits) != 0 {
				t.Fatalf("the id repair edits %v", request.Edits)
			}
		default:
			if !reflect.DeepEqual(request.Edits, []domain.NFOWriteTextEdit{{Field: "title", Value: "DB " + request.ItemID}}) {
				t.Fatalf("%s: edits %v", request.ItemID, request.Edits)
			}
		}
	}
	if calls != 11 {
		t.Fatalf("the NFO of %d items was validated, want 11 (one is unresolvable)", calls)
	}
}

func TestNFOExportWorkerFixModeNeedsAValidatingReaderAndRetriesConflicts(t *testing.T) {
	ids := nfoApplyIDs(3)
	items := &exportItems{}
	// A reader that cannot validate leaves every item unreadable: skipped, and never a repair.
	execution := &fixStep{exportExecution{items: ids}}
	base, err := NewLocalMetadata(items)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := base.WithNFOItemFields(nfoApplyWorkerReader{})
	if err != nil {
		t.Fatal(err)
	}
	preparer := &exportPreparer{}
	worker, err := NewNFOExportWorker(metadata, preparer, execution, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(context.Background(), exportLease()); err != nil {
		t.Fatal(err)
	}
	if execution.counts != (domain.NFOExportCounts{SkippedOther: 3}) || len(preparer.keys) != 0 {
		t.Fatalf("counts %+v prepared %v", execution.counts, preparer.keys)
	}
	// A file that changes while it is validated is retried once, then failed.
	execution = &fixStep{exportExecution{items: ids}}
	reader := fixReader{err: map[string]error{ids[0]: domain.ErrNFOSourceChanged, ids[1]: domain.ErrNFOReaderUnavailable}, results: map[string]domain.NFOItemValidation{ids[2]: fixChecked(nil)}}
	if metadata, err = base.WithNFOItemFields(reader); err != nil {
		t.Fatal(err)
	}
	if worker, err = NewNFOExportWorker(metadata, preparer, execution, 0); err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(context.Background(), exportLease()); err != nil {
		t.Fatal(err)
	}
	if execution.counts != (domain.NFOExportCounts{Failed: 1, SkippedOther: 1, Unchanged: 1}) {
		t.Fatalf("counts %+v", execution.counts)
	}
}
