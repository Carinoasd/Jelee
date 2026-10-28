package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const nfoApplyWorkerActor = "11111111-1111-4111-8111-111111111111"

// nfoApplyItems is a scriptable item repository: what ItemMetadata and the
// apply step answer for each item.
type nfoApplyItems struct {
	ItemMetadataRepository
	resolve map[string]error
	apply   map[string][]error // consumed one per call
	same    map[string]bool    // the apply leaves the stored state as it was
	applied []string
}

func nfoApplyScope(item string, revision int64) domain.NFOItemScope {
	return domain.NFOItemScope{ItemID: item, LibraryID: nfoApplyWorkerActor, SourceID: nfoApplyWorkerActor, RootID: nfoApplyWorkerActor, Kind: "Movie", Revision: revision, Generation: 1, MediaPath: "film.mkv", Source: domain.NFOSource{RootPath: "/trusted", RelativePath: "film.nfo"}}
}

func (r *nfoApplyItems) ItemMetadata(_ context.Context, _ domain.Actor, item string) (domain.ItemMetadata, error) {
	return domain.ItemMetadata{ItemID: item, Kind: "Movie", Revision: 1, Fields: []domain.ItemMetadataField{{Field: "title", Value: "Old", Source: "existing"}}}, nil
}

func (r *nfoApplyItems) ResolveItemNFO(_ context.Context, _ domain.Actor, item string, revision int64) (domain.NFOItemScope, error) {
	if err := r.resolve[item]; err != nil {
		return domain.NFOItemScope{}, err
	}
	return nfoApplyScope(item, revision), nil
}

func (r *nfoApplyItems) ApplyItemNFO(_ context.Context, _ domain.Actor, scope domain.NFOItemScope, _ domain.NFOItemFields) (domain.MetadataApplyResult, error) {
	if queue := r.apply[scope.ItemID]; len(queue) > 0 {
		r.apply[scope.ItemID] = queue[1:]
		if queue[0] != nil {
			return domain.MetadataApplyResult{}, queue[0]
		}
	}
	r.applied = append(r.applied, scope.ItemID)
	field := domain.ItemMetadataField{Field: "title", Value: "NFO", Source: "nfo"}
	if r.same[scope.ItemID] {
		field = domain.ItemMetadataField{Field: "title", Value: "Old", Source: "existing"}
	}
	return domain.MetadataApplyResult{Applied: []string{"title"}, Metadata: domain.ItemMetadata{ItemID: scope.ItemID, Kind: "Movie", Revision: scope.Revision + 1, Fields: []domain.ItemMetadataField{field}}}, nil
}

type nfoApplyExecution struct {
	items    []string
	cursor   string
	counts   domain.NFOApplyCounts
	records  []int
	nextErr  error
	recordFn func(n int)
}

func (r *nfoApplyExecution) NextNFOApplyBatch(_ context.Context, _ domain.JobLease, limit int) (domain.NFOApplyBatch, error) {
	if r.nextErr != nil {
		return domain.NFOApplyBatch{}, r.nextErr
	}
	var out []string
	for _, item := range r.items {
		if item > r.cursor && len(out) < limit {
			out = append(out, item)
		}
	}
	return domain.NFOApplyBatch{Actor: domain.Actor{UserID: nfoApplyWorkerActor, SessionID: nfoApplyWorkerActor}, Items: out}, nil
}

func (r *nfoApplyExecution) RecordNFOApplyBatch(_ context.Context, _ domain.JobLease, cursor string, c domain.NFOApplyCounts) error {
	r.cursor, r.counts = cursor, r.counts.Add(c)
	r.records = append(r.records, c.Applied+c.Unchanged+c.Skipped+c.Failed)
	if r.recordFn != nil {
		r.recordFn(len(r.records))
	}
	return nil
}

func (r *nfoApplyExecution) FinishNFOApply(context.Context, domain.JobLease, string, string) error {
	return nil
}

func nfoApplyWorker(t *testing.T, items *nfoApplyItems, execution *nfoApplyExecution) *NFOApplyWorker {
	t.Helper()
	reader := &nfoApplyWorkerReader{}
	base, err := NewLocalMetadata(items)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := base.WithNFOItemFields(reader)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewNFOApplyWorker(metadata, execution, 0)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

type nfoApplyWorkerReader struct{}

func (nfoApplyWorkerReader) ReadItemFields(context.Context, domain.NFOSource, string) (domain.NFOItemFields, error) {
	return domain.NFOItemFields{Version: domain.NFOItemFieldsVersion, Kind: "Movie", Identity: domain.DefaultNFOIdentity(), Stamp: domain.NFOStamp{Size: 10, SHA256: strings.Repeat("a", 64), FingerprintVersion: domain.NFOFingerprintVersion}, ReadAt: time.Now().UTC(), Fields: []domain.NFOTextField{{Field: "title", Value: "NFO"}}}, nil
}

func nfoApplyIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%08x-1111-4111-8111-111111111111", i+1)
	}
	return ids
}

func nfoApplyLease() domain.JobLease {
	return domain.JobLease{Job: domain.Job{ID: nfoApplyWorkerActor, Kind: domain.JobNFOApply}}
}

func TestNFOApplyWorkerClassifiesItemsAndCheckpointsInBatches(t *testing.T) {
	ids := nfoApplyIDs(120)
	items := &nfoApplyItems{
		resolve: map[string]error{ids[1]: domain.ErrMetadataUnavailable, ids[2]: domain.ErrNotFound},
		apply: map[string][]error{
			ids[3]: {domain.ErrConflict},                     // retried once, then applied
			ids[4]: {domain.ErrConflict, domain.ErrConflict}, // second conflict is a failure
			ids[5]: {domain.ErrInvalid},
		},
		same: map[string]bool{ids[6]: true},
	}
	execution := &nfoApplyExecution{items: ids}
	if err := nfoApplyWorker(t, items, execution).Run(context.Background(), nfoApplyLease()); err != nil {
		t.Fatal(err)
	}
	want := domain.NFOApplyCounts{Applied: 120 - 2 - 2 - 1, Unchanged: 1, Skipped: 2, Failed: 2}
	if execution.counts != want || execution.cursor != ids[119] || fmt.Sprint(execution.records) != "[50 50 20]" {
		t.Fatalf("counts %+v cursor %s records %v, want %+v", execution.counts, execution.cursor, execution.records, want)
	}
}

func TestNFOApplyWorkerResumesAfterTheCursor(t *testing.T) {
	ids := nfoApplyIDs(70)
	items := &nfoApplyItems{}
	execution := &nfoApplyExecution{items: ids, cursor: ids[49]}
	if err := nfoApplyWorker(t, items, execution).Run(context.Background(), nfoApplyLease()); err != nil {
		t.Fatal(err)
	}
	if len(items.applied) != 20 || items.applied[0] != ids[50] || execution.counts.Applied != 20 {
		t.Fatal("run did not continue after the cursor", len(items.applied))
	}
}

func TestNFOApplyWorkerStopsOnNonItemErrorsAndKeepsFinishedItems(t *testing.T) {
	for _, stop := range []error{domain.ErrDatabase, domain.ErrJobLeaseLost, domain.ErrForbidden, domain.ErrUnauthenticated} {
		t.Run(stop.Error(), func(t *testing.T) {
			ids := nfoApplyIDs(10)
			items := &nfoApplyItems{resolve: map[string]error{ids[3]: stop}}
			execution := &nfoApplyExecution{items: ids}
			err := nfoApplyWorker(t, items, execution).Run(context.Background(), nfoApplyLease())
			if !errors.Is(err, stop) || execution.cursor != ids[2] || execution.counts.Applied != 3 {
				t.Fatalf("err=%v cursor=%s counts=%+v", err, execution.cursor, execution.counts)
			}
		})
	}
	execution := &nfoApplyExecution{items: nfoApplyIDs(3), nextErr: context.Canceled}
	if err := nfoApplyWorker(t, &nfoApplyItems{}, execution).Run(context.Background(), nfoApplyLease()); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation reported as", err)
	}
}

func TestNFOApplyWorkerCancellationBetweenItemsRecordsFinishedItems(t *testing.T) {
	ids := nfoApplyIDs(120)
	ctx, cancel := context.WithCancel(context.Background())
	items := &nfoApplyItems{}
	execution := &nfoApplyExecution{items: ids}
	worker := nfoApplyWorker(t, items, execution)
	// Cancel while the second batch runs: the reader is the only hook into
	// the middle of a batch.
	nfoApplyCancelReads = 0
	worker.metadata.nfoFields = nfoApplyCancelReader{after: 60 * 2, cancel: cancel}
	err := worker.Run(ctx, nfoApplyLease())
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation reported as", err)
	}
	if execution.counts.Applied < 50 || execution.counts.Applied >= 120 || execution.cursor != ids[execution.counts.Applied-1] {
		t.Fatalf("finished items not recorded exactly: %+v cursor=%s", execution.counts, execution.cursor)
	}
}

type nfoApplyCancelReader struct {
	after  int
	cancel context.CancelFunc
}

var nfoApplyCancelReads int

func (r nfoApplyCancelReader) ReadItemFields(ctx context.Context, source domain.NFOSource, kind string) (domain.NFOItemFields, error) {
	nfoApplyCancelReads++
	if nfoApplyCancelReads == r.after {
		r.cancel()
	}
	return nfoApplyWorkerReader{}.ReadItemFields(ctx, source, kind)
}

func TestNewNFOApplyWorkerRefusesUnusableInputs(t *testing.T) {
	local, _ := NewLocalMetadata(&nfoApplyItems{})
	withReader, _ := local.WithNFOItemFields(nfoApplyWorkerReader{})
	for name, call := range map[string]func() error{
		"nil metadata":  func() error { _, err := NewNFOApplyWorker(nil, &nfoApplyExecution{}, 0); return err },
		"no reader":     func() error { _, err := NewNFOApplyWorker(local, &nfoApplyExecution{}, 0); return err },
		"no repository": func() error { _, err := NewNFOApplyWorker(withReader, nil, 0); return err },
		"negative":      func() error { _, err := NewNFOApplyWorker(withReader, &nfoApplyExecution{}, -1); return err },
		"no apply method": func() error {
			m := *withReader
			m.items = nfoApplyPlainItems{}
			_, err := NewNFOApplyWorker(&m, &nfoApplyExecution{}, 0)
			return err
		},
	} {
		if err := call(); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal(name, "accepted", err)
		}
	}
	if err := (&NFOApplyWorker{}).Run(context.Background(), domain.JobLease{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("run accepted another job kind")
	}
}

type nfoApplyPlainItems struct{ ItemMetadataRepository }

func TestNFOApplyChangedComparesStoredState(t *testing.T) {
	base := domain.ItemMetadata{Kind: "Movie", Fields: []domain.ItemMetadataField{{Field: "title", Value: "A", Source: "nfo"}}, Facts: []domain.ItemMetadataFact{{Field: "year", Value: []byte("2024"), Source: "nfo"}}}
	same := domain.CloneItemMetadata(base)
	same.Revision = 9
	if nfoApplyChanged(base, same) {
		t.Fatal("a rewrite of the same values counted as a change")
	}
	for name, mutate := range map[string]func(*domain.ItemMetadata){
		"value":  func(m *domain.ItemMetadata) { m.Fields[0].Value = "B" },
		"source": func(m *domain.ItemMetadata) { m.Fields[0].Source = "existing" },
		"lock":   func(m *domain.ItemMetadata) { m.Fields[0].NFOOrigin = &domain.NFOItemOrigin{Locked: true} },
		"fact":   func(m *domain.ItemMetadata) { m.Facts[0].Value = []byte("2025") },
		"kind":   func(m *domain.ItemMetadata) { m.Kind = "HomeVideo" },
		"added":  func(m *domain.ItemMetadata) { m.Fields = append(m.Fields, domain.ItemMetadataField{Field: "overview"}) },
	} {
		changed := domain.CloneItemMetadata(base)
		mutate(&changed)
		if !nfoApplyChanged(base, changed) {
			t.Fatal(name, "change missed")
		}
	}
}

type nfoApplyJobsFake struct{ calls int }

func (f *nfoApplyJobsFake) SubmitNFOApplyJob(_ context.Context, _ domain.Actor, library, _, priority string, _ domain.JobPolicy) (domain.Job, bool, error) {
	f.calls++
	return domain.Job{ID: library, Kind: domain.JobNFOApply, Priority: priority, State: domain.JobQueued}, false, nil
}

func (f *nfoApplyJobsFake) GetNFOApplyState(_ context.Context, _ domain.Actor, job string) (domain.NFOApplyJobState, error) {
	f.calls++
	return domain.NFOApplyJobState{JobID: job}, nil
}

func TestJobsSubmitNFOApplyValidatesBeforeTheRepository(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	actor := domain.Actor{UserID: id, SessionID: id}
	repo := &nfoApplyJobsFake{}
	base := newJobService(t, jobRepositoryFake{})
	if _, err := NewJobsWithNFOApply(nil, repo); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil base accepted")
	}
	if _, err := NewJobsWithNFOApply(base, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil repository accepted")
	}
	if _, _, err := base.SubmitNFOApply(context.Background(), actor, id, "k", domain.JobPriorityManual); !errors.Is(err, domain.ErrNFOReaderUnavailable) {
		t.Fatal("missing repository", err)
	}
	if _, err := base.NFOApplyState(context.Background(), actor, id); !errors.Is(err, domain.ErrNFOReaderUnavailable) {
		t.Fatal("missing repository for state", err)
	}
	jobs, err := NewJobsWithNFOApply(base, repo)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, call := range map[string]func() error{
		"bad actor": func() error {
			_, _, err := jobs.SubmitNFOApply(context.Background(), domain.Actor{}, id, "k", "manual")
			return err
		},
		"bad library": func() error {
			_, _, err := jobs.SubmitNFOApply(context.Background(), actor, "x", "k", "manual")
			return err
		},
		"bad key": func() error {
			_, _, err := jobs.SubmitNFOApply(context.Background(), actor, id, "", "manual")
			return err
		},
		"bad priority": func() error {
			_, _, err := jobs.SubmitNFOApply(context.Background(), actor, id, "k", "urgent")
			return err
		},
		"bad job": func() error { _, err := jobs.NFOApplyState(context.Background(), actor, "x"); return err },
	} {
		if err := call(); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal(name, "accepted", err)
		}
	}
	if _, _, err := jobs.SubmitNFOApply(canceled, actor, id, "k", "manual"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled context", err)
	}
	if repo.calls != 0 {
		t.Fatal("invalid input reached the repository")
	}
	if job, _, err := jobs.SubmitNFOApply(context.Background(), actor, id, "k", "background"); err != nil || job.Priority != "background" {
		t.Fatal("valid submit", err)
	}
	if state, err := jobs.NFOApplyState(context.Background(), actor, id); err != nil || state.JobID != id {
		t.Fatal("valid state", err)
	}
}
