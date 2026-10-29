package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const nfoExportWorkerActor = "11111111-1111-4111-8111-111111111111"

func nfoExportState(status string, fields domain.NFOItemFields) *domain.NFOItemObservationState {
	return &domain.NFOItemObservationState{Status: status, Selection: domain.NFOItemSelection{Fields: fields}}
}

func nfoExportText(pairs ...string) []domain.NFOTextField {
	var out []domain.NFOTextField
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, domain.NFOTextField{Field: pairs[i], Value: pairs[i+1]})
	}
	return out
}

func nfoExportMeta(fields []domain.ItemMetadataField, facts ...domain.ItemMetadataFact) domain.ItemMetadata {
	return domain.ItemMetadata{ItemID: nfoExportWorkerActor, Kind: "Movie", Revision: 3, Fields: fields, Facts: facts}
}

func TestPlanNFOExportComparesStoredValuesWithTheNFO(t *testing.T) {
	scope := nfoApplyScope(nfoExportWorkerActor, 3)
	manual := func(field, value string) domain.ItemMetadataField {
		return domain.ItemMetadataField{Field: field, Value: value, Source: "manual"}
	}
	edits := func(pairs ...string) []domain.NFOWriteTextEdit {
		var out []domain.NFOWriteTextEdit
		for i := 0; i+1 < len(pairs); i += 2 {
			out = append(out, domain.NFOWriteTextEdit{Field: pairs[i], Value: pairs[i+1]})
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		meta    domain.ItemMetadata
		state   *domain.NFOItemObservationState
		outcome nfoExportOutcome
		edits   []domain.NFOWriteTextEdit
	}{
		{"missing NFO", nfoExportMeta([]domain.ItemMetadataField{manual("title", "T")}), nfoExportState(domain.NFOItemObservedMissing, domain.NFOItemFields{}), nfoExportMissing, nil},
		{"invalid NFO", nfoExportMeta([]domain.ItemMetadataField{manual("title", "T")}), nfoExportState(domain.NFOItemObservedInvalid, domain.NFOItemFields{}), nfoExportInvalid, nil},
		{"no observation state", nfoExportMeta(nil), nil, nfoExportSkipped, nil},
		{"unknown status", nfoExportMeta(nil), nfoExportState("other", domain.NFOItemFields{}), nfoExportSkipped, nil},
		{"equal values", nfoExportMeta([]domain.ItemMetadataField{manual("title", "T"), manual("overview", "P")}), nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{Fields: nfoExportText("title", "T", "overview", "P")}), nfoExportUnchanged, nil},
		{"differing values, fixed field order", nfoExportMeta([]domain.ItemMetadataField{manual("overview", "P2"), manual("title", "T2"), manual("sortTitle", "S"), manual("originalTitle", "O"), manual("tagline", "G"), manual("outline", "L"), manual("mpaa", "R"), manual("certification", "PG")}),
			nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{Fields: nfoExportText("title", "T", "overview", "P")}), nfoExportPrepared,
			edits("title", "T2", "originaltitle", "O", "plot", "P2", "sorttitle", "S", "tagline", "G", "outline", "L", "mpaa", "R", "certification", "PG")},
		{"only the differing field", nfoExportMeta([]domain.ItemMetadataField{manual("title", "T"), manual("overview", "P2")}), nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{Fields: nfoExportText("title", "T", "overview", "P")}), nfoExportPrepared, edits("plot", "P2")},
		{"scanned name is no stored value", nfoExportMeta([]domain.ItemMetadataField{{Field: "title", Value: "Scanned", Source: "existing"}}), nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{Fields: nfoExportText("title", "T")}), nfoExportUnchanged, nil},
		{"an empty value never blanks the NFO", nfoExportMeta([]domain.ItemMetadataField{manual("overview", "")}), nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{Fields: nfoExportText("overview", "P")}), nfoExportUnchanged, nil},
		{"date is not exported", nfoExportMeta([]domain.ItemMetadataField{{Field: "date", Value: "2024-01-02", Source: "manual"}}), nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{}), nfoExportUnchanged, nil},
		{"global lock", nfoExportMeta([]domain.ItemMetadataField{manual("title", "T2")}), nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{Fields: nfoExportText("title", "T"), LockData: true}), nfoExportSkipped, nil},
		{"every differing field locked", nfoExportMeta([]domain.ItemMetadataField{manual("title", "T2"), manual("overview", "P2")}), nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{Fields: nfoExportText("title", "T", "overview", "P"), LockedFields: []string{" Name ", "Overview"}}), nfoExportSkipped, nil},
		{"a locked field is left out of the edit", nfoExportMeta([]domain.ItemMetadataField{manual("title", "T2"), manual("overview", "P2"), manual("sortTitle", "S")}), nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{Fields: nfoExportText("title", "T", "overview", "P"), LockedFields: []string{"SortName"}}), nfoExportPrepared, edits("title", "T2", "plot", "P2")},
		{"episode show title", nfoExportMeta(nil, domain.ItemMetadataFact{Field: "showTitle", Value: json.RawMessage(`"Show"`), Source: "manual"}),
			nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{EpisodeDetails: &domain.NFOEpisodeDetails{ShowTitle: "Old"}}), nfoExportPrepared, edits("showtitle", "Show")},
		{"episode show title equal", nfoExportMeta(nil, domain.ItemMetadataFact{Field: "showTitle", Value: json.RawMessage(`"Show"`), Source: "nfo"}),
			nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{EpisodeDetails: &domain.NFOEpisodeDetails{ShowTitle: "Show"}}), nfoExportUnchanged, nil},
		{"series status without an NFO value", nfoExportMeta(nil, domain.ItemMetadataFact{Field: "seriesStatus", Value: json.RawMessage(`"Ended"`), Source: "manual"}),
			nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{}), nfoExportPrepared, edits("status", "Ended")},
		{"cleared fact", nfoExportMeta(nil, domain.ItemMetadataFact{Field: "seriesStatus", Value: json.RawMessage(`null`), Source: "manual"}),
			nfoExportState(domain.NFOItemObservedValid, domain.NFOItemFields{SeriesDetails: &domain.NFOSeriesDetails{Status: "Ended"}}), nfoExportUnchanged, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome, request := planNFOExport(tc.meta, scope, nfoItemRead{state: tc.state})
			if outcome != tc.outcome || (request != nil) != (tc.outcome == nfoExportPrepared) {
				t.Fatalf("outcome %d request %v, want %d", outcome, request, tc.outcome)
			}
			if request == nil {
				return
			}
			if !reflect.DeepEqual(request.Edits, tc.edits) || request.ItemID != scope.ItemID || request.Revision != scope.Revision || !request.CreateMissing || request.BOM != "preserve" || request.Backups != nfoExportBackups || request.MaxBytes != domain.NFODefaultSourceBytes {
				t.Fatalf("request %+v, want edits %v", *request, tc.edits)
			}
			if err := domain.ValidateNFOWritePrepareRequest(*request); err != nil {
				t.Fatal("the request is not a valid preparation", err)
			}
		})
	}
}

// exportObservation is a reader answer with a fixed state.
type exportObservation struct {
	state domain.NFOItemObservationState
}

func (o exportObservation) Selection() domain.NFOItemSelection { return o.state.Selection }
func (o exportObservation) Recheck(context.Context) (NFOItemObservation, error) {
	return o, nil
}
func (o exportObservation) State() domain.NFOItemObservationState { return o.state }

// exportReader answers by item through the scope's media path: every item has
// its own file name "<item>.mkv".
type exportReader struct {
	nfoApplyWorkerReader
	status map[string]string
	values map[string][]domain.NFOTextField
}

func (r exportReader) ObserveItemNFO(_ context.Context, scope domain.NFOItemScope) (NFOItemObservation, error) {
	relative := scope.Source.RelativePath
	identity := domain.DefaultNFOIdentity()
	stamp := domain.NFOStamp{Size: 10, SHA256: strings.Repeat("a", 64), FingerprintVersion: domain.NFOFingerprintVersion}
	now := time.Now().UTC()
	switch r.status[scope.ItemID] {
	case domain.NFOItemObservedMissing:
		return exportObservation{domain.NFOItemObservationState{Status: domain.NFOItemObservedMissing, Selection: domain.NFOItemSelection{CandidateDigest: domain.NFOCandidateDigest([]string{})}, Identity: identity, ReadAt: now}}, nil
	case domain.NFOItemObservedInvalid:
		return exportObservation{domain.NFOItemObservationState{Status: domain.NFOItemObservedInvalid, Selection: domain.NFOItemSelection{RelativePath: relative, CandidateDigest: domain.NFOCandidateDigest([]string{relative})}, Identity: identity, Stamp: stamp, ReadAt: now}}, nil
	}
	// A valid NFO holds at least one field; the filler is one the database never has.
	values := r.values[scope.ItemID]
	if len(values) == 0 {
		values = nfoExportText("certification", "filler")
	}
	fields := domain.NFOItemFields{Version: domain.NFOItemMovieFieldsVersion, Kind: "Movie", Identity: identity, Stamp: stamp, ReadAt: now, Fields: values}
	return exportObservation{domain.NFOItemObservationState{Status: domain.NFOItemObservedValid, Selection: domain.NFOItemSelection{RelativePath: relative, CandidateDigest: domain.NFOCandidateDigest([]string{relative}), Fields: fields}, Identity: identity, Stamp: stamp, ReadAt: now}}, nil
}

// exportItems answers the database side: item metadata is "DB <item>" for
// every field unless a scripted error says otherwise.
type exportItems struct {
	ItemMetadataRepository
	metadataErr map[string]error
	resolveErr  map[string]error
	revisions   map[string][]int64 // revision per call, then the last one repeats
	calls       map[string]int
	scanned     map[string]bool // items whose title is only the scanned name
}

func (r *exportItems) ItemMetadata(_ context.Context, _ domain.Actor, item string) (domain.ItemMetadata, error) {
	if err := r.metadataErr[item]; err != nil {
		return domain.ItemMetadata{}, err
	}
	revision := int64(1)
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	if list := r.revisions[item]; len(list) > 0 {
		revision = list[min(r.calls[item], len(list)-1)]
	}
	r.calls[item]++
	source := "manual"
	if r.scanned[item] {
		source = "existing"
	}
	return domain.ItemMetadata{ItemID: item, Kind: "Movie", Revision: revision, Fields: []domain.ItemMetadataField{{Field: "title", Value: "DB " + item, Source: source}}}, nil
}

func (r *exportItems) ResolveItemNFO(_ context.Context, _ domain.Actor, item string, revision int64) (domain.NFOItemScope, error) {
	if err := r.resolveErr[item]; err != nil {
		return domain.NFOItemScope{}, err
	}
	if list := r.revisions[item]; len(list) > 0 && revision != list[min(r.calls[item]-1, len(list)-1)] {
		return domain.NFOItemScope{}, domain.ErrConflict
	}
	return nfoApplyScope(item, revision), nil
}

type exportPreparer struct {
	keys     []string
	requests []domain.NFOWritePrepareRequest
	errs     map[string][]error // by item, consumed one per call
	failOn   func(n int) error  // by call number
	onCall   func(n int)
}

func (p *exportPreparer) Prepare(_ context.Context, _ domain.Actor, key string, request domain.NFOWritePrepareRequest) (domain.NFOWritePreparation, bool, error) {
	p.keys = append(p.keys, key)
	p.requests = append(p.requests, request)
	if p.onCall != nil {
		p.onCall(len(p.keys))
	}
	if queue := p.errs[request.ItemID]; len(queue) > 0 {
		p.errs[request.ItemID] = queue[1:]
		if queue[0] != nil {
			return domain.NFOWritePreparation{}, false, queue[0]
		}
	}
	if p.failOn != nil {
		if err := p.failOn(len(p.keys)); err != nil {
			return domain.NFOWritePreparation{}, false, err
		}
	}
	return domain.NFOWritePreparation{ID: "prep-" + request.ItemID}, false, nil
}

type exportSubmit struct {
	chunk        int
	cursor       string
	counts       domain.NFOExportCounts
	preparations []string
}

type exportExecution struct {
	items     []string
	cursor    string
	chunk     int
	counts    domain.NFOExportCounts
	waiting   bool
	nextErr   error
	submits   []exportSubmit
	records   []string
	submitErr error
	steps     int
}

func (r *exportExecution) NextNFOExportStep(_ context.Context, _ domain.JobLease, limit int) (domain.NFOExportStep, error) {
	r.steps++
	if r.nextErr != nil {
		return domain.NFOExportStep{}, r.nextErr
	}
	if r.waiting {
		return domain.NFOExportStep{Waiting: true}, nil
	}
	step := domain.NFOExportStep{Actor: domain.Actor{UserID: nfoExportWorkerActor, SessionID: nfoExportWorkerActor}, Chunk: r.chunk}
	for _, item := range r.items {
		if item > r.cursor && len(step.Items) < limit {
			step.Items = append(step.Items, item)
		}
	}
	return step, nil
}

func (r *exportExecution) RecordNFOExportBatch(_ context.Context, _ domain.JobLease, cursor string, c domain.NFOExportCounts) error {
	r.cursor, r.counts = cursor, r.counts.Add(c)
	r.records = append(r.records, cursor)
	return nil
}

func (r *exportExecution) SubmitNFOExportChunk(_ context.Context, _ domain.JobLease, _ domain.Actor, chunk int, cursor string, c domain.NFOExportCounts, preparations []string) error {
	if r.submitErr != nil {
		return r.submitErr
	}
	r.submits = append(r.submits, exportSubmit{chunk, cursor, c, append([]string(nil), preparations...)})
	r.cursor, r.chunk, r.counts = cursor, chunk, r.counts.Add(c)
	return nil
}

func (r *exportExecution) SettleNFOExportChild(context.Context, domain.JobLease, int) error {
	return nil
}
func (r *exportExecution) YieldNFOExport(context.Context, domain.JobLease) error { return nil }
func (r *exportExecution) FinishNFOExport(context.Context, domain.JobLease, string, string) error {
	return nil
}

func exportWorker(t *testing.T, items *exportItems, reader exportReader, preparer *exportPreparer, execution *exportExecution) *NFOExportWorker {
	t.Helper()
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
	return worker
}

func exportLease() domain.JobLease {
	return domain.JobLease{Job: domain.Job{ID: "22222222-2222-4222-8222-222222222222", Kind: domain.JobNFOExport}}
}

func TestNFOExportWorkerSubmitsChunksOfSixteenAndYields(t *testing.T) {
	ids := nfoApplyIDs(40)
	execution := &exportExecution{items: ids}
	preparer := &exportPreparer{}
	worker := exportWorker(t, &exportItems{}, exportReader{values: map[string][]domain.NFOTextField{}}, preparer, execution)
	for chunk := 1; chunk <= 2; chunk++ {
		if err := worker.Run(context.Background(), exportLease()); !errors.Is(err, ErrNFOExportYield) {
			t.Fatal("run did not yield", chunk, err)
		}
		got := execution.submits[chunk-1]
		if got.chunk != chunk || len(got.preparations) != domain.NFOExportChunkSize || got.cursor != ids[chunk*16-1] || got.counts != (domain.NFOExportCounts{}) {
			t.Fatalf("chunk %d: %+v", chunk, got)
		}
	}
	// Eight items remain; the last chunk is short.
	if err := worker.Run(context.Background(), exportLease()); !errors.Is(err, ErrNFOExportYield) {
		t.Fatal(err)
	}
	if last := execution.submits[2]; last.chunk != 3 || len(last.preparations) != 8 || last.cursor != ids[39] {
		t.Fatalf("last chunk %+v", last)
	}
	if err := worker.Run(context.Background(), exportLease()); err != nil {
		t.Fatal("nothing left, run should end", err)
	}
	if len(execution.records) != 0 {
		t.Fatal("nothing to record without unchanged items", execution.records)
	}
	// The preparation keys name the export job, the chunk and the item.
	for i, key := range preparer.keys {
		if want := domain.NFOExportPreparationKey(exportLease().Job.ID, i/16+1, ids[i]); key != want {
			t.Fatalf("key %d is %q, want %q", i, key, want)
		}
	}
}

func TestNFOExportWorkerClassifiesItemsAndRecordsThoseNeedingNoWrite(t *testing.T) {
	ids := nfoApplyIDs(12)
	// Item 0 is already equal, 1 has no NFO, 2 an invalid one, 3 and 4 cannot
	// be resolved, 5 conflicts twice, 6 fails to prepare, 7 is prepared.
	items := &exportItems{
		metadataErr: map[string]error{ids[4]: domain.ErrNotFound},
		resolveErr:  map[string]error{ids[3]: domain.ErrMetadataUnavailable},
		revisions:   map[string][]int64{ids[5]: {1, 2, 3}},
	}
	reader := exportReader{
		status: map[string]string{ids[1]: domain.NFOItemObservedMissing, ids[2]: domain.NFOItemObservedInvalid},
		values: map[string][]domain.NFOTextField{ids[0]: nfoExportText("title", "DB "+ids[0])},
	}
	for _, id := range ids[8:] {
		reader.values[id] = nfoExportText("title", "DB "+id)
	}
	preparer := &exportPreparer{errs: map[string][]error{ids[5]: {domain.ErrConflict, domain.ErrConflict}, ids[6]: {domain.ErrInvalid}}}
	execution := &exportExecution{items: ids}
	worker := exportWorker(t, items, reader, preparer, execution)
	if err := worker.Run(context.Background(), exportLease()); !errors.Is(err, ErrNFOExportYield) {
		t.Fatal("run did not yield", err)
	}
	// The conflict of item 5 is retried once with fresh state, then failed.
	want := domain.NFOExportCounts{Unchanged: 1 + 4, SkippedMissing: 1, SkippedInvalid: 1, SkippedOther: 2, Failed: 2}
	if len(execution.submits) != 1 || execution.submits[0].counts != want || !reflect.DeepEqual(execution.submits[0].preparations, []string{"prep-" + ids[7]}) || execution.submits[0].cursor != ids[11] {
		t.Fatalf("submit %+v, want counts %+v", execution.submits, want)
	}
}

func TestNFOExportWorkerRecordsAnExportWithoutWrites(t *testing.T) {
	ids := nfoApplyIDs(120)
	reader := exportReader{values: map[string][]domain.NFOTextField{}, status: map[string]string{}}
	for i, id := range ids {
		if i%2 == 0 {
			reader.values[id] = nfoExportText("title", "DB "+id)
		} else {
			reader.status[id] = domain.NFOItemObservedMissing
		}
	}
	execution := &exportExecution{items: ids}
	if err := exportWorker(t, &exportItems{}, reader, &exportPreparer{}, execution).Run(context.Background(), exportLease()); err != nil {
		t.Fatal(err)
	}
	if execution.counts != (domain.NFOExportCounts{Unchanged: 60, SkippedMissing: 60}) || len(execution.records) != 3 || len(execution.submits) != 0 {
		t.Fatalf("counts %+v records %d", execution.counts, len(execution.records))
	}
}

func TestNFOExportWorkerYieldsWhileItsChildIsOutstanding(t *testing.T) {
	execution := &exportExecution{items: nfoApplyIDs(5), waiting: true}
	preparer := &exportPreparer{}
	err := exportWorker(t, &exportItems{}, exportReader{}, preparer, execution).Run(context.Background(), exportLease())
	if !errors.Is(err, ErrNFOExportYield) || len(preparer.keys) != 0 || len(execution.submits) != 0 {
		t.Fatal("a waiting run must only yield", err)
	}
}

func TestNFOExportWorkerStopsOnErrorsThatAreNotTheItems(t *testing.T) {
	ids := nfoApplyIDs(8)
	for _, tc := range []struct {
		name string
		err  error
	}{{"database", domain.ErrDatabase}, {"lease", domain.ErrJobLeaseLost}, {"forbidden", domain.ErrForbidden}, {"capacity at the start", domain.ErrNFOCacheCapacity}} {
		t.Run(tc.name, func(t *testing.T) {
			execution := &exportExecution{items: ids}
			preparer := &exportPreparer{failOn: func(n int) error {
				if n == 3 {
					return tc.err
				}
				return nil
			}}
			err := exportWorker(t, &exportItems{}, exportReader{}, preparer, execution).Run(context.Background(), exportLease())
			// Two prepared items are abandoned, not submitted or counted: a
			// resume replays their preparations by key.
			if !errors.Is(err, tc.err) && !(errors.Is(tc.err, domain.ErrNFOCacheCapacity) && errors.Is(err, ErrNFOExportYield)) {
				t.Fatal("run did not stop with the error", err)
			}
			if errors.Is(tc.err, domain.ErrNFOCacheCapacity) {
				// With two preparations ready the full store ends the chunk there.
				if len(execution.submits) != 1 || len(execution.submits[0].preparations) != 2 || execution.submits[0].cursor != ids[1] {
					t.Fatalf("a full preparation store did not end the chunk: %+v", execution.submits)
				}
				return
			}
			if len(execution.submits) != 0 || len(execution.records) != 0 {
				t.Fatal("an abandoned chunk was recorded", execution.submits, execution.records)
			}
		})
	}
	t.Run("capacity before any preparation", func(t *testing.T) {
		execution := &exportExecution{items: ids}
		preparer := &exportPreparer{failOn: func(int) error { return domain.ErrNFOCacheCapacity }}
		if err := exportWorker(t, &exportItems{}, exportReader{}, preparer, execution).Run(context.Background(), exportLease()); !errors.Is(err, domain.ErrNFOCacheCapacity) {
			t.Fatal(err)
		}
	})
	t.Run("storage error from the step", func(t *testing.T) {
		execution := &exportExecution{items: ids, nextErr: domain.ErrJobLeaseLost}
		if err := exportWorker(t, &exportItems{}, exportReader{}, &exportPreparer{}, execution).Run(context.Background(), exportLease()); !errors.Is(err, domain.ErrJobLeaseLost) {
			t.Fatal(err)
		}
	})
	t.Run("submit refused", func(t *testing.T) {
		execution := &exportExecution{items: ids, submitErr: domain.ErrNFOWriteDisabled}
		if err := exportWorker(t, &exportItems{}, exportReader{}, &exportPreparer{}, execution).Run(context.Background(), exportLease()); !errors.Is(err, domain.ErrNFOWriteDisabled) {
			t.Fatal(err)
		}
	})
}

func TestNFOExportWorkerCancellationAbandonsTheOpenChunk(t *testing.T) {
	ids := nfoApplyIDs(30)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	preparer := &exportPreparer{onCall: func(n int) {
		if n == 5 {
			cancel()
		}
	}}
	execution := &exportExecution{items: ids}
	err := exportWorker(t, &exportItems{}, exportReader{}, preparer, execution).Run(ctx, exportLease())
	if !errors.Is(err, context.Canceled) || len(execution.submits) != 0 || len(preparer.keys) != 5 {
		t.Fatal("cancellation must stop at once without a child", err, len(preparer.keys))
	}
}

func TestNFOExportWorkerRejectsWrongJobKindAndMissingParts(t *testing.T) {
	execution := &exportExecution{}
	worker := exportWorker(t, &exportItems{}, exportReader{}, &exportPreparer{}, execution)
	if err := worker.Run(context.Background(), domain.JobLease{Job: domain.Job{Kind: domain.JobNFOApply}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := NewNFOExportWorker(nil, &exportPreparer{}, execution, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
	base, _ := NewLocalMetadata(&exportItems{})
	if _, err := NewNFOExportWorker(base, &exportPreparer{}, execution, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("a metadata service without NFO fields was accepted", err)
	}
}
