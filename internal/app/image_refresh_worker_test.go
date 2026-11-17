package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// refreshExecution is an in-memory ImageRefreshExecutionRepository: rows are
// candidates in ID order and recorded results move a cursor.
type refreshExecution struct {
	rows      []domain.ImageRefreshCandidate
	cursor    string
	results   []domain.ImageRefreshResult
	records   int
	nextErr   error
	recordErr error
}

func (r *refreshExecution) NextImageRefreshBatch(_ context.Context, _ domain.JobLease, limit int) ([]domain.ImageRefreshCandidate, error) {
	if r.nextErr != nil {
		return nil, r.nextErr
	}
	var out []domain.ImageRefreshCandidate
	for _, row := range r.rows {
		if row.ImageID > r.cursor && len(out) < limit {
			out = append(out, row)
		}
	}
	return out, nil
}

func (r *refreshExecution) RecordImageRefresh(ctx context.Context, _ domain.JobLease, results []domain.ImageRefreshResult) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if r.recordErr != nil {
		return r.recordErr
	}
	for _, result := range results {
		if !domain.ValidImageRefreshResult(result) || result.Candidate.ImageID <= r.cursor {
			return domain.ErrInvalid
		}
		r.cursor = result.Candidate.ImageID
	}
	r.records++
	r.results = append(r.results, results...)
	return nil
}

func (r *refreshExecution) FinishImageRefresh(context.Context, domain.JobLease, string, string) error {
	return nil
}

type fakeItemImageFetcher struct {
	calls []string
	fetch func(ctx context.Context, url string) (domain.ItemImageContent, error)
}

func (f *fakeItemImageFetcher) FetchItemImage(ctx context.Context, url string) (domain.ItemImageContent, error) {
	f.calls = append(f.calls, url)
	return f.fetch(ctx, url)
}

func refreshRows(n int) []domain.ImageRefreshCandidate {
	rows := make([]domain.ImageRefreshCandidate, n)
	for i := range rows {
		rows[i] = domain.ImageRefreshCandidate{ImageID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), ItemID: "00000000-0000-4000-9000-000000000001",
			URL: fmt.Sprintf("https://img.example/%d.png", i)}
	}
	return rows
}

func refreshLease() domain.JobLease {
	return domain.JobLease{Job: domain.Job{ID: "00000000-0000-4000-a000-000000000001", Kind: domain.JobImageRefresh}}
}

func refreshContent() domain.ItemImageContent {
	return domain.ItemImageContent{SHA256: make([]byte, 32), Format: "png", Bytes: 10, FetchedAt: time.Now()}
}

// Every fetch is recorded on its own; failures are recorded with their
// retry delay and never stop the run; deferred rows are not fetched.
func TestImageRefreshWorkerIsolatesFailures(t *testing.T) {
	rows := refreshRows(domain.ImageRefreshBatchSize + 5)
	rows[1].Deferred = true
	rows[3].Failures = 2
	repo := &refreshExecution{rows: rows}
	fetcher := &fakeItemImageFetcher{fetch: func(_ context.Context, url string) (domain.ItemImageContent, error) {
		switch url {
		case rows[2].URL:
			return domain.ItemImageContent{}, &domain.ImageFetchFailure{Reason: domain.ImageFetchNotFound, Status: 404, Permanent: true}
		case rows[3].URL:
			return domain.ItemImageContent{}, &domain.ImageFetchFailure{Reason: domain.ImageFetchUnavailable, Status: 503}
		}
		return refreshContent(), nil
	}}
	w, err := NewImageRefreshWorker(fetcher, repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(context.Background(), refreshLease()); err != nil {
		t.Fatal(err)
	}
	if len(repo.results) != len(rows) || len(fetcher.calls) != len(rows)-1 || repo.cursor != rows[len(rows)-1].ImageID {
		t.Fatalf("results=%d fetches=%d", len(repo.results), len(fetcher.calls))
	}
	if !repo.results[1].Deferred || repo.results[2].Failure == nil || repo.results[2].Failures != 1 || repo.results[2].RetryDelay != domain.ImageFetchPermanentRetryDelay ||
		repo.results[3].Failures != 3 || repo.results[3].RetryDelay != 4*domain.ImageFetchTransientRetryDelay || repo.results[4].Content == nil {
		t.Fatalf("classification: %+v", repo.results[:5])
	}
	// The deferred row rides with the next fetch: one record per fetch.
	if repo.records != len(rows)-1 {
		t.Fatal("records per fetch", repo.records)
	}
}

// A cancelled fetch stops the run after recording what finished, so the
// resumed run continues after the last recorded row.
func TestImageRefreshWorkerCancelsAndResumes(t *testing.T) {
	rows := refreshRows(6)
	repo := &refreshExecution{rows: rows}
	ctx, cancel := context.WithCancel(context.Background())
	fetcher := &fakeItemImageFetcher{fetch: func(c context.Context, url string) (domain.ItemImageContent, error) {
		if url == rows[3].URL {
			cancel()
			return domain.ItemImageContent{}, c.Err()
		}
		return refreshContent(), nil
	}}
	w, err := NewImageRefreshWorker(fetcher, repo, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(ctx, refreshLease()); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled run", err)
	}
	if repo.cursor != rows[2].ImageID || len(repo.results) != 3 {
		t.Fatal("cancelled run cursor", repo.cursor, len(repo.results))
	}
	fetcher.calls = nil
	fetcher.fetch = func(context.Context, string) (domain.ItemImageContent, error) { return refreshContent(), nil }
	if err := w.Run(context.Background(), refreshLease()); err != nil {
		t.Fatal(err)
	}
	if len(fetcher.calls) != 3 || fetcher.calls[0] != rows[3].URL || len(repo.results) != 6 {
		t.Fatal("resume repeated or skipped rows", fetcher.calls)
	}
}

func TestImageRefreshWorkerStops(t *testing.T) {
	if _, err := NewImageRefreshWorker(nil, nil, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
	repo := &refreshExecution{rows: refreshRows(3)}
	w, _ := NewImageRefreshWorker(nil, repo, 0)
	if err := w.Run(context.Background(), refreshLease()); !errors.Is(err, domain.ErrImageUnavailable) {
		t.Fatal("worker without fetcher", err)
	}
	if err := w.Run(context.Background(), domain.JobLease{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("other kind", err)
	}
	// An error that is not about one image stops the run but keeps the rows
	// finished before it.
	boom := errors.New("boom")
	fetcher := &fakeItemImageFetcher{fetch: func(_ context.Context, url string) (domain.ItemImageContent, error) {
		if url == repo.rows[1].URL {
			return domain.ItemImageContent{}, boom
		}
		return refreshContent(), nil
	}}
	w, _ = NewImageRefreshWorker(fetcher, repo, 0)
	if err := w.Run(context.Background(), refreshLease()); !errors.Is(err, boom) || repo.cursor != repo.rows[0].ImageID {
		t.Fatal("unexpected fetch error", err, repo.cursor)
	}
	// A failure with an unknown reason is not trusted as per-image.
	fetcher.fetch = func(context.Context, string) (domain.ItemImageContent, error) {
		return domain.ItemImageContent{}, &domain.ImageFetchFailure{Reason: "made_up"}
	}
	if err := w.Run(context.Background(), refreshLease()); err == nil {
		t.Fatal("unknown failure reason accepted")
	}
	repo.recordErr = domain.ErrJobLeaseLost
	fetcher.fetch = func(context.Context, string) (domain.ItemImageContent, error) { return refreshContent(), nil }
	if err := w.Run(context.Background(), refreshLease()); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("lost lease", err)
	}
	repo.nextErr = context.Canceled
	if err := w.Run(context.Background(), refreshLease()); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel requested", err)
	}
}

// fakeImageRefreshJobs records admission calls.
type fakeImageRefreshJobs struct{ submits int }

func (f *fakeImageRefreshJobs) SubmitImageRefreshJob(context.Context, domain.Actor, string, string, string, domain.JobPolicy) (domain.Job, bool, error) {
	f.submits++
	return domain.Job{Kind: domain.JobImageRefresh}, false, nil
}

func (f *fakeImageRefreshJobs) GetImageRefreshState(context.Context, domain.Actor, string) (domain.ImageRefreshJobState, error) {
	return domain.ImageRefreshJobState{}, nil
}

func TestJobsImageRefreshAdmission(t *testing.T) {
	actor := domain.Actor{UserID: "00000000-0000-4000-8000-000000000001", SessionID: "00000000-0000-4000-8000-000000000002"}
	library := "00000000-0000-4000-8000-000000000003"
	base := &Jobs{}
	if _, _, err := base.SubmitImageRefresh(context.Background(), actor, library, "k", domain.JobPriorityManual); !errors.Is(err, domain.ErrImageUnavailable) {
		t.Fatal("no repository", err)
	}
	if _, err := base.ImageRefreshState(context.Background(), actor, library); !errors.Is(err, domain.ErrImageUnavailable) {
		t.Fatal("no repository state", err)
	}
	if _, err := NewJobsWithImageRefresh(nil, nil, true); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
	repo := &fakeImageRefreshJobs{}
	off, _ := NewJobsWithImageRefresh(base, repo, false)
	if _, _, err := off.SubmitImageRefresh(context.Background(), actor, library, "k", domain.JobPriorityManual); !errors.Is(err, domain.ErrImageUnavailable) || repo.submits != 0 {
		t.Fatal("local-only admission", err)
	}
	if _, err := off.ImageRefreshState(context.Background(), actor, library); err != nil {
		t.Fatal("state stays readable", err)
	}
	on, _ := NewJobsWithImageRefresh(base, repo, true)
	if _, _, err := on.SubmitImageRefresh(context.Background(), actor, library, "k", "urgent"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("priority", err)
	}
	if _, _, err := on.SubmitImageRefresh(context.Background(), actor, library, "k", domain.JobPriorityBackground); err != nil || repo.submits != 1 {
		t.Fatal("admission", err)
	}
}
