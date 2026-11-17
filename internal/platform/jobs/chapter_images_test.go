package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type chapterRepository struct {
	pages     [][]domain.ChapterImageCandidate
	afters    []string
	recorded  []domain.ChapterImageResult
	pageErr   error
	recordErr error
}

func (r *chapterRepository) NextChapterImageCandidates(_ context.Context, _ domain.JobLease, after string, limit int) (domain.ChapterImagePage, error) {
	if r.pageErr != nil {
		return domain.ChapterImagePage{}, r.pageErr
	}
	if limit != domain.ChapterImageBatch {
		return domain.ChapterImagePage{}, domain.ErrInvalid
	}
	r.afters = append(r.afters, after)
	index := len(r.afters) - 1
	if index >= len(r.pages) {
		return domain.ChapterImagePage{}, nil
	}
	page := domain.ChapterImagePage{Candidates: r.pages[index]}
	if index+1 < len(r.pages) {
		page.Next = fmt.Sprintf("cursor-%d", index)
	}
	return page, nil
}

func (r *chapterRepository) RecordChapterImages(_ context.Context, _ domain.JobLease, result domain.ChapterImageResult) (domain.ChapterImageRecord, error) {
	if r.recordErr != nil {
		return domain.ChapterImageRecord{}, r.recordErr
	}
	if !domain.ValidChapterImageResult(result) {
		return domain.ChapterImageRecord{}, domain.ErrInvalid
	}
	r.recorded = append(r.recorded, result)
	return domain.ChapterImageRecord{Outcome: result.Outcome, Stored: len(result.Images)}, nil
}

type chapterExtractor struct {
	results map[string]error
	images  map[string][]domain.ChapterImage
	calls   int
	hook    func()
}

func chapterJobPicture(t testing.TB, index int, data []byte) domain.ChapterImage {
	t.Helper()
	return domain.ChapterImage{Index: index, Data: data, SHA256: sha256.Sum256(data), Format: "png", Width: 4, Height: 3}
}

func (e *chapterExtractor) ExtractChapterImages(ctx context.Context, c domain.ChapterImageCandidate) ([]domain.ChapterImage, error) {
	e.calls++
	if e.hook != nil {
		e.hook()
	}
	for key, err := range e.results {
		if coverJobID(key) == c.ItemID {
			return nil, err
		}
	}
	for key, images := range e.images {
		if coverJobID(key) == c.ItemID {
			return images, ctx.Err()
		}
	}
	data := []byte("chapter-" + c.ItemID)
	return []domain.ChapterImage{{Index: 0, Data: data, SHA256: sha256.Sum256(data), Format: "png", Width: 4, Height: 3},
		{Index: 2, Data: append(data, '2'), SHA256: sha256.Sum256(append(data, '2')), Format: "png", Width: 4, Height: 3}}, ctx.Err()
}

func chapterJobCandidate(key string) domain.ChapterImageCandidate {
	return domain.ChapterImageCandidate{ItemID: coverJobID(key), LibraryID: "22222222-2222-4222-8222-222222222222", RootID: "33333333-3333-4333-8333-333333333333",
		RootPath: "/media", RelativePath: "Movie/Movie.mp4", Streams: []domain.ChapterImageStream{{StreamIndex: 1, VideoIndex: 1}},
		Stamp: domain.ProbeStamp{Size: 1, Fingerprint: strings.Repeat("a", 64), FingerprintVersion: domain.ProbeFingerprintVersion}}
}

func chapterRunner(options *ChapterImageOptions) *Runner {
	return &Runner{options: Options{DBOperationTimeout: time.Second, PollInterval: time.Millisecond, CatalogSync: &CatalogSyncOptions{ChapterImages: options}}}
}

func TestChapterImagePassOutcomes(t *testing.T) {
	repository := &chapterRepository{pages: [][]domain.ChapterImageCandidate{
		{chapterJobCandidate("a"), chapterJobCandidate("b"), chapterJobCandidate("c")},
		{chapterJobCandidate("d"), chapterJobCandidate("e"), chapterJobCandidate("f")},
	}}
	extractor := &chapterExtractor{results: map[string]error{
		"b": domain.ErrChapterImagesTooLarge, "c": domain.ErrChapterImagesAbsent, "d": domain.ErrChapterImagesInvalid,
		"e": domain.ErrProbeSourceChanged, "f": context.DeadlineExceeded,
	}}
	store := &coverStore{}
	storage, err := chapterRunner(&ChapterImageOptions{Repository: repository, Extractor: extractor, Store: store}).extractChapterImages(context.Background(), domain.JobLease{})
	if err != nil || storage {
		t.Fatal("pass", err)
	}
	want := map[string]string{coverJobID("a"): domain.ChapterImageStored, coverJobID("b"): domain.ChapterImageTooLarge, coverJobID("c"): domain.ChapterImageAbsent, coverJobID("d"): domain.ChapterImageInvalid}
	if len(repository.recorded) != len(want) || extractor.calls != 6 || store.puts != 2 || len(repository.afters) != 2 || repository.afters[1] != "cursor-0" {
		t.Fatal("pass shape", len(repository.recorded), extractor.calls, store.puts, repository.afters)
	}
	for _, r := range repository.recorded {
		if want[r.Candidate.ItemID] != r.Outcome {
			t.Fatal("outcome", r.Candidate.ItemID, r.Outcome)
		}
	}
	stored := repository.recorded[0]
	picture := []byte("chapter-" + coverJobID("a"))
	sum := sha256.Sum256(picture)
	if len(stored.Images) != 2 || stored.Images[0].Index != 0 || stored.Images[1].Index != 2 || !bytes.Equal(stored.Images[0].Content.SHA256, sum[:]) ||
		stored.Images[0].Content.Bytes != int64(len(picture)) || stored.Images[0].Content.FetchedAt.IsZero() {
		t.Fatal("stored content")
	}
}

func TestChapterImagePassStopsAndBounds(t *testing.T) {
	lease := domain.JobLease{}
	if storage, err := chapterRunner(nil).extractChapterImages(context.Background(), lease); err != nil || storage {
		t.Fatal("disabled pass")
	}
	repository := &chapterRepository{pages: [][]domain.ChapterImageCandidate{{chapterJobCandidate("a"), chapterJobCandidate("b")}}}
	extractor := &chapterExtractor{results: map[string]error{"a": domain.ErrProbeRuntimeUnavailable}}
	if _, err := chapterRunner(&ChapterImageOptions{Repository: repository, Extractor: extractor, Store: &coverStore{}}).extractChapterImages(context.Background(), lease); err != nil || extractor.calls != 1 || len(repository.recorded) != 0 {
		t.Fatal("unavailable tool did not stop the pass", err, extractor.calls)
	}
	off := &chapterExtractor{}
	if _, err := chapterRunner(&ChapterImageOptions{Repository: repository, Extractor: off, Store: &coverStore{}, Available: func() bool { return false }}).extractChapterImages(context.Background(), lease); err != nil || off.calls != 0 {
		t.Fatal("unavailable capability ran reads")
	}
	// A failed store write of any picture leaves the file unremembered.
	for _, store := range []*coverStore{{err: errors.New("disk")}, {corrupt: true}} {
		repository = &chapterRepository{pages: [][]domain.ChapterImageCandidate{{chapterJobCandidate("a")}}}
		if _, err := chapterRunner(&ChapterImageOptions{Repository: repository, Extractor: &chapterExtractor{}, Store: store}).extractChapterImages(context.Background(), lease); err != nil || len(repository.recorded) != 0 {
			t.Fatal("store failure recorded", err)
		}
	}
	// Pictures that fail their own validation are remembered as invalid.
	for name, images := range map[string][]domain.ChapterImage{
		"oversized": {chapterJobPicture(t, 0, make([]byte, domain.ChapterImageMaxBytes+1))},
		"unordered": {chapterJobPicture(t, 3, []byte("x")), chapterJobPicture(t, 1, []byte("y"))},
		"empty":     {},
	} {
		repository = &chapterRepository{pages: [][]domain.ChapterImageCandidate{{chapterJobCandidate("a")}}}
		bad := &chapterExtractor{images: map[string][]domain.ChapterImage{"a": images}}
		if _, err := chapterRunner(&ChapterImageOptions{Repository: repository, Extractor: bad, Store: &coverStore{}}).extractChapterImages(context.Background(), lease); err != nil || len(repository.recorded) != 1 || repository.recorded[0].Outcome != domain.ChapterImageInvalid {
			t.Fatal("invalid pictures outcome", name, err)
		}
	}
	sentinel := errors.New("lease lost")
	for _, repository := range []*chapterRepository{{pageErr: sentinel}, {pages: [][]domain.ChapterImageCandidate{{chapterJobCandidate("a")}}, recordErr: sentinel}} {
		if storage, err := chapterRunner(&ChapterImageOptions{Repository: repository, Extractor: &chapterExtractor{}, Store: &coverStore{}}).extractChapterImages(context.Background(), lease); !errors.Is(err, sentinel) || !storage {
			t.Fatal("repository error", err, storage)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	repository = &chapterRepository{pages: [][]domain.ChapterImageCandidate{{chapterJobCandidate("a")}}}
	cancelling := &chapterExtractor{hook: cancel}
	if storage, err := chapterRunner(&ChapterImageOptions{Repository: repository, Extractor: cancelling, Store: &coverStore{}}).extractChapterImages(ctx, lease); !errors.Is(err, context.Canceled) || storage || len(repository.recorded) != 0 {
		t.Fatal("cancellation", err)
	}
	// At most ChapterImageMaxPerJob files are read in one job.
	var pages [][]domain.ChapterImageCandidate
	for i := 0; i*domain.ChapterImageBatch <= domain.ChapterImageMaxPerJob; i++ {
		page := make([]domain.ChapterImageCandidate, domain.ChapterImageBatch)
		for j := range page {
			page[j] = chapterJobCandidate(fmt.Sprintf("%d-%d", i, j))
		}
		pages = append(pages, page)
	}
	counting := &chapterExtractor{}
	repository = &chapterRepository{pages: pages}
	if _, err := chapterRunner(&ChapterImageOptions{Repository: repository, Extractor: counting, Store: &coverStore{}, FileTimeout: time.Hour}).extractChapterImages(context.Background(), lease); err != nil || counting.calls != domain.ChapterImageMaxPerJob {
		t.Fatal("per-job bound", err, counting.calls)
	}
}

// The chapter pass runs after the cover pass, once the batches are done, and
// a failing cover pass storage error ends the sync before it.
func TestCatalogSyncEndsWithTheChapterImagePass(t *testing.T) {
	sync := &coverSyncRepository{}
	covers := &coverRepository{pages: [][]domain.EmbeddedCoverCandidate{{coverJobCandidate("a")}}}
	chapters := &chapterRepository{pages: [][]domain.ChapterImageCandidate{{chapterJobCandidate("a")}}}
	r := chapterRunner(&ChapterImageOptions{Repository: chapters, Extractor: &chapterExtractor{}, Store: &coverStore{}})
	r.options.CatalogSync.Repository = sync
	r.options.CatalogSync.EmbeddedCovers = &EmbeddedCoverOptions{Repository: covers, Extractor: &coverExtractor{}, Store: &coverStore{}}
	if err, storage := r.executeCatalogSync(context.Background(), domain.JobLease{}); err != nil || storage || len(covers.recorded) != 1 || len(chapters.recorded) != 1 {
		t.Fatal("catalog sync did not run both passes", err)
	}
	sentinel := errors.New("lease lost")
	sync = &coverSyncRepository{}
	chapters = &chapterRepository{pages: [][]domain.ChapterImageCandidate{{chapterJobCandidate("a")}}}
	r = chapterRunner(&ChapterImageOptions{Repository: chapters, Extractor: &chapterExtractor{}, Store: &coverStore{}})
	r.options.CatalogSync.Repository = sync
	r.options.CatalogSync.EmbeddedCovers = &EmbeddedCoverOptions{Repository: &coverRepository{pageErr: sentinel}, Extractor: &coverExtractor{}, Store: &coverStore{}}
	if err, storage := r.executeCatalogSync(context.Background(), domain.JobLease{}); !errors.Is(err, sentinel) || !storage || len(chapters.afters) != 0 {
		t.Fatal("cover storage error did not end the sync", err, storage)
	}
}
