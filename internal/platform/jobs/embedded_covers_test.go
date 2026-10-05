package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type coverRepository struct {
	pages      [][]domain.EmbeddedCoverCandidate
	afters     []string
	recorded   []domain.EmbeddedCoverResult
	pageErr    error
	recordErr  error
	recordHook func(domain.EmbeddedCoverResult)
}

func (r *coverRepository) NextEmbeddedCoverCandidates(_ context.Context, _ domain.JobLease, after string, limit int) (domain.EmbeddedCoverPage, error) {
	if r.pageErr != nil {
		return domain.EmbeddedCoverPage{}, r.pageErr
	}
	if limit != domain.EmbeddedCoverBatch {
		return domain.EmbeddedCoverPage{}, domain.ErrInvalid
	}
	r.afters = append(r.afters, after)
	index := len(r.afters) - 1
	if index >= len(r.pages) {
		return domain.EmbeddedCoverPage{}, nil
	}
	page := domain.EmbeddedCoverPage{Candidates: r.pages[index]}
	if index+1 < len(r.pages) {
		page.Next = fmt.Sprintf("cursor-%d", index)
	}
	return page, nil
}

func (r *coverRepository) RecordEmbeddedCover(_ context.Context, _ domain.JobLease, result domain.EmbeddedCoverResult) (string, error) {
	if r.recordErr != nil {
		return "", r.recordErr
	}
	r.recorded = append(r.recorded, result)
	if r.recordHook != nil {
		r.recordHook(result)
	}
	return result.Outcome, nil
}

type coverExtractor struct {
	results map[string]error
	data    map[string][]byte
	calls   int
	hook    func()
}

func (e *coverExtractor) ExtractCover(ctx context.Context, c domain.EmbeddedCoverCandidate) (domain.EmbeddedCover, error) {
	e.calls++
	if e.hook != nil {
		e.hook()
	}
	key := c.ItemID
	for k := range e.results {
		if coverJobID(k) == c.ItemID {
			key = k
		}
	}
	for k := range e.data {
		if coverJobID(k) == c.ItemID {
			key = k
		}
	}
	if err := e.results[key]; err != nil {
		return domain.EmbeddedCover{}, err
	}
	data := e.data[key]
	if data == nil {
		data = []byte("picture-" + key)
	}
	return domain.EmbeddedCover{Data: data, SHA256: sha256.Sum256(data), Format: "png", Width: 4, Height: 3}, ctx.Err()
}

type coverStore struct {
	err     error
	corrupt bool
	puts    int
}

func (s *coverStore) PutOriginal(_ context.Context, input io.Reader, limit int64) ([32]byte, int64, error) {
	s.puts++
	if s.err != nil {
		return [32]byte{}, 0, s.err
	}
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil || int64(len(data)) > limit {
		return [32]byte{}, 0, errors.New("limit")
	}
	if s.corrupt {
		data = append(data, 0)
	}
	return sha256.Sum256(data), int64(len(data)), nil
}

// coverJobCandidate keeps a readable key in the fake maps and a valid UUID
// in the candidate: the key is the UUID's last group.
func coverJobCandidate(key string) domain.EmbeddedCoverCandidate {
	return domain.EmbeddedCoverCandidate{ItemID: coverJobID(key), LibraryID: "22222222-2222-4222-8222-222222222222", RootID: "33333333-3333-4333-8333-333333333333",
		RootPath: "/media", RelativePath: "Movie/Movie.mkv", StreamIndex: 1, VideoIndex: 1,
		Stamp: domain.ProbeStamp{Size: 1, Fingerprint: strings.Repeat("a", 64), FingerprintVersion: domain.ProbeFingerprintVersion}}
}

func coverJobID(key string) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", []byte(key)[:min(6, len(key))])
}

func coverRunner(options *EmbeddedCoverOptions) *Runner {
	return &Runner{options: Options{DBOperationTimeout: time.Second, PollInterval: time.Millisecond, CatalogSync: &CatalogSyncOptions{EmbeddedCovers: options}}}
}

func TestEmbeddedCoverPassOutcomes(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e", "f"}
	repository := &coverRepository{pages: [][]domain.EmbeddedCoverCandidate{
		{coverJobCandidate(ids[0]), coverJobCandidate(ids[1]), coverJobCandidate(ids[2])},
		{coverJobCandidate(ids[3]), coverJobCandidate(ids[4]), coverJobCandidate(ids[5])},
	}}
	extractor := &coverExtractor{results: map[string]error{
		"b": domain.ErrEmbeddedCoverTooLarge, "c": domain.ErrEmbeddedCoverAbsent, "d": domain.ErrEmbeddedCoverInvalid,
		"e": domain.ErrProbeSourceChanged, "f": context.DeadlineExceeded,
	}}
	store := &coverStore{}
	storage, err := coverRunner(&EmbeddedCoverOptions{Repository: repository, Extractor: extractor, Store: store}).extractEmbeddedCovers(context.Background(), domain.JobLease{})
	if err != nil || storage {
		t.Fatal("pass", err)
	}
	want := map[string]string{"a": domain.EmbeddedCoverStored, "b": domain.EmbeddedCoverTooLarge, "c": domain.EmbeddedCoverAbsent, "d": domain.EmbeddedCoverInvalid}
	if len(repository.recorded) != len(want) || extractor.calls != 6 || store.puts != 1 || len(repository.afters) != 2 || repository.afters[1] != "cursor-0" {
		t.Fatal("pass shape", len(repository.recorded), extractor.calls, store.puts, repository.afters)
	}
	for _, r := range repository.recorded {
		key := ""
		for k := range want {
			if coverJobID(k) == r.Candidate.ItemID {
				key = k
			}
		}
		if want[key] != r.Outcome {
			t.Fatal("outcome", r.Candidate.ItemID, r.Outcome)
		}
	}
	stored := repository.recorded[0]
	picture := []byte("picture-" + coverJobID("a"))
	sum := sha256.Sum256(picture)
	if stored.Content == nil || !bytes.Equal(stored.Content.SHA256, sum[:]) || stored.Content.Format != "png" || stored.Content.Bytes != int64(len(picture)) || stored.Content.FetchedAt.IsZero() || !domain.ValidEmbeddedCoverResult(stored) {
		t.Fatal("stored content")
	}
}

func TestEmbeddedCoverPassStopsAndBounds(t *testing.T) {
	lease := domain.JobLease{}
	// Disabled wiring is a no-op.
	if storage, err := coverRunner(nil).extractEmbeddedCovers(context.Background(), lease); err != nil || storage {
		t.Fatal("disabled pass")
	}
	// An unavailable tool ends the pass; so does a capability that turned off.
	repository := &coverRepository{pages: [][]domain.EmbeddedCoverCandidate{{coverJobCandidate("a"), coverJobCandidate("b")}}}
	extractor := &coverExtractor{results: map[string]error{"a": domain.ErrProbeRuntimeUnavailable}}
	if _, err := coverRunner(&EmbeddedCoverOptions{Repository: repository, Extractor: extractor, Store: &coverStore{}}).extractEmbeddedCovers(context.Background(), lease); err != nil || extractor.calls != 1 || len(repository.recorded) != 0 {
		t.Fatal("unavailable tool did not stop the pass", err, extractor.calls)
	}
	off := &coverExtractor{}
	if _, err := coverRunner(&EmbeddedCoverOptions{Repository: repository, Extractor: off, Store: &coverStore{}, Available: func() bool { return false }}).extractEmbeddedCovers(context.Background(), lease); err != nil || off.calls != 0 {
		t.Fatal("unavailable capability ran extractions")
	}
	// Store failures and digest disagreements are not remembered.
	for _, store := range []*coverStore{{err: errors.New("disk")}, {corrupt: true}} {
		repository = &coverRepository{pages: [][]domain.EmbeddedCoverCandidate{{coverJobCandidate("a")}}}
		if _, err := coverRunner(&EmbeddedCoverOptions{Repository: repository, Extractor: &coverExtractor{}, Store: store}).extractEmbeddedCovers(context.Background(), lease); err != nil || len(repository.recorded) != 0 {
			t.Fatal("store failure recorded", err)
		}
	}
	// A picture that fails its own validation is remembered as invalid.
	repository = &coverRepository{pages: [][]domain.EmbeddedCoverCandidate{{coverJobCandidate("a")}}}
	bad := &coverExtractor{data: map[string][]byte{"a": make([]byte, domain.EmbeddedCoverMaxBytes+1)}}
	if _, err := coverRunner(&EmbeddedCoverOptions{Repository: repository, Extractor: bad, Store: &coverStore{}}).extractEmbeddedCovers(context.Background(), lease); err != nil || len(repository.recorded) != 1 || repository.recorded[0].Outcome != domain.EmbeddedCoverInvalid {
		t.Fatal("invalid picture outcome", err)
	}
	// Repository errors fail the job as storage errors.
	sentinel := errors.New("lease lost")
	for _, repository := range []*coverRepository{{pageErr: sentinel}, {pages: [][]domain.EmbeddedCoverCandidate{{coverJobCandidate("a")}}, recordErr: sentinel}} {
		if storage, err := coverRunner(&EmbeddedCoverOptions{Repository: repository, Extractor: &coverExtractor{}, Store: &coverStore{}}).extractEmbeddedCovers(context.Background(), lease); !errors.Is(err, sentinel) || !storage {
			t.Fatal("repository error", err, storage)
		}
	}
	// Cancellation during an extraction ends the pass without recording.
	ctx, cancel := context.WithCancel(context.Background())
	repository = &coverRepository{pages: [][]domain.EmbeddedCoverCandidate{{coverJobCandidate("a")}}}
	cancelling := &coverExtractor{hook: cancel}
	if storage, err := coverRunner(&EmbeddedCoverOptions{Repository: repository, Extractor: cancelling, Store: &coverStore{}}).extractEmbeddedCovers(ctx, lease); !errors.Is(err, context.Canceled) || storage || len(repository.recorded) != 0 {
		t.Fatal("cancellation", err)
	}
	if _, err := coverRunner(&EmbeddedCoverOptions{Repository: repository, Extractor: cancelling, Store: &coverStore{}}).extractEmbeddedCovers(ctx, lease); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled context started a pass", err)
	}
	// At most EmbeddedCoverMaxPerJob extractions start in one job.
	var pages [][]domain.EmbeddedCoverCandidate
	for i := 0; i*domain.EmbeddedCoverBatch <= domain.EmbeddedCoverMaxPerJob; i++ {
		page := make([]domain.EmbeddedCoverCandidate, domain.EmbeddedCoverBatch)
		for j := range page {
			page[j] = coverJobCandidate(fmt.Sprintf("%d-%d", i, j))
		}
		pages = append(pages, page)
	}
	counting := &coverExtractor{}
	repository = &coverRepository{pages: pages}
	if _, err := coverRunner(&EmbeddedCoverOptions{Repository: repository, Extractor: counting, Store: &coverStore{}, FileTimeout: time.Hour}).extractEmbeddedCovers(context.Background(), lease); err != nil || counting.calls != domain.EmbeddedCoverMaxPerJob {
		t.Fatal("per-job bound", err, counting.calls)
	}
}

type coverSyncRepository struct{ advances int }

func (r *coverSyncRepository) AdvanceCatalogSync(context.Context, domain.JobLease) (bool, error) {
	r.advances++
	return r.advances > 1, nil
}
func (r *coverSyncRepository) FinishCatalogSync(context.Context, domain.JobLease, string, string) error {
	return nil
}

// The cover pass runs once the synchronisation batches are done.
func TestCatalogSyncEndsWithTheEmbeddedCoverPass(t *testing.T) {
	sync := &coverSyncRepository{}
	repository := &coverRepository{pages: [][]domain.EmbeddedCoverCandidate{{coverJobCandidate("a")}}}
	r := coverRunner(&EmbeddedCoverOptions{Repository: repository, Extractor: &coverExtractor{}, Store: &coverStore{}})
	r.options.CatalogSync.Repository = sync
	if err, storage := r.executeCatalogSync(context.Background(), domain.JobLease{}); err != nil || storage || sync.advances != 2 || len(repository.recorded) != 1 {
		t.Fatal("catalog sync did not end with the cover pass", err)
	}
}
