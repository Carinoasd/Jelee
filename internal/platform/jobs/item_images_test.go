package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type ingestRepository struct {
	pages  []domain.ItemImageIngestPage
	afters []string
	err    error
	cancel context.CancelFunc
}

func (r *ingestRepository) IngestItemImages(_ context.Context, _ domain.JobLease, after string, limit int) (domain.ItemImageIngestPage, error) {
	if limit != domain.ItemImageIngestBatch {
		return domain.ItemImageIngestPage{}, domain.ErrInvalid
	}
	r.afters = append(r.afters, after)
	if r.cancel != nil {
		r.cancel()
	}
	if r.err != nil {
		return domain.ItemImageIngestPage{}, r.err
	}
	if len(r.afters) > len(r.pages) {
		return domain.ItemImageIngestPage{}, errors.New("read past the last page")
	}
	return r.pages[len(r.afters)-1], nil
}

type doneSync struct{ finished bool }

func (s *doneSync) AdvanceCatalogSync(context.Context, domain.JobLease) (bool, error) {
	return true, nil
}
func (s *doneSync) FinishCatalogSync(context.Context, domain.JobLease, string, string) error {
	s.finished = true
	return nil
}

func ingestRunner(repository *ingestRepository) *Runner {
	options := &CatalogSyncOptions{Repository: &doneSync{}}
	if repository != nil {
		options.Images = repository
	}
	return &Runner{options: Options{DBOperationTimeout: time.Second, PollInterval: time.Millisecond, CatalogSync: options}}
}

func TestItemImageIngestPass(t *testing.T) {
	lease := domain.JobLease{}
	if err := ingestRunner(nil).ingestItemImages(context.Background(), lease); err != nil {
		t.Fatal("unwired pass", err)
	}
	// Pages follow the cursor until the library is walked.
	repository := &ingestRepository{pages: []domain.ItemImageIngestPage{{Next: "a", Items: 64}, {Next: "b", Items: 64}, {Items: 3}}}
	if err := ingestRunner(repository).ingestItemImages(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if len(repository.afters) != 3 || repository.afters[0] != "" || repository.afters[1] != "a" || repository.afters[2] != "b" {
		t.Fatal("cursor", repository.afters)
	}
	// A replaced baseline ends the pass without an error.
	repository = &ingestRepository{pages: []domain.ItemImageIngestPage{{Next: "a"}, {Next: "b", Stale: true}}}
	if err := ingestRunner(repository).ingestItemImages(context.Background(), lease); err != nil || len(repository.afters) != 2 {
		t.Fatal("stale page", err, repository.afters)
	}
	// Repository errors fail the job as storage errors.
	sentinel := errors.New("lease lost")
	repository = &ingestRepository{err: sentinel}
	if err, storage := ingestRunner(repository).executeCatalogSync(context.Background(), lease); !errors.Is(err, sentinel) || !storage {
		t.Fatal("repository error", err, storage)
	}
	// Cancellation between pages stops the pass.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repository = &ingestRepository{pages: []domain.ItemImageIngestPage{{Next: "a"}, {}}, cancel: cancel}
	if err := ingestRunner(repository).ingestItemImages(ctx, lease); !errors.Is(err, context.Canceled) || len(repository.afters) != 1 {
		t.Fatal("cancelled pass", err, repository.afters)
	}
	// A finished sync runs the pass.
	repository = &ingestRepository{pages: []domain.ItemImageIngestPage{{}}}
	if err, _ := ingestRunner(repository).executeCatalogSync(context.Background(), lease); err != nil || len(repository.afters) != 1 {
		t.Fatal("sync did not ingest", err, repository.afters)
	}
}
