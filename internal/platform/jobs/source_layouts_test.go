package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// layoutRepository serves numbered pages of targets and records results.
type layoutRepository struct {
	pages    [][]domain.SourceLayoutTarget
	afters   []string
	recorded []domain.SourceLayoutInspection
	nextErr  error
}

func (r *layoutRepository) NextSourceLayoutInspections(_ context.Context, _ domain.JobLease, after string, limit int) ([]domain.SourceLayoutTarget, error) {
	if limit != domain.SourceLayoutInspectionBatch {
		return nil, domain.ErrInvalid
	}
	r.afters = append(r.afters, after)
	if r.nextErr != nil {
		return nil, r.nextErr
	}
	if len(r.afters) > len(r.pages) {
		return nil, nil
	}
	return r.pages[len(r.afters)-1], nil
}

func (r *layoutRepository) RecordSourceLayouts(_ context.Context, _ domain.JobLease, results []domain.SourceLayoutInspection) (int, error) {
	r.recorded = append(r.recorded, results...)
	return len(results), nil
}

type layoutInspector struct {
	failing map[string]bool
	calls   int
	cancel  context.CancelFunc
}

func (i *layoutInspector) InspectSourceLayout(_ context.Context, target domain.SourceLayoutTarget) (domain.SourceLayoutInspection, error) {
	i.calls++
	if i.cancel != nil {
		i.cancel()
	}
	if i.failing[target.SourceID] {
		return domain.SourceLayoutInspection{}, domain.ErrConflict
	}
	return domain.SourceLayoutInspection{SourceID: target.SourceID, Size: target.Size, SourceLayout: domain.SourceLayout{Layout: domain.LayoutMoovAtEnd}}, nil
}

func layoutPage(start, count int) []domain.SourceLayoutTarget {
	page := make([]domain.SourceLayoutTarget, count)
	for i := range page {
		page[i] = domain.SourceLayoutTarget{SourceID: fmt.Sprintf("00000000-0000-4000-8000-%012d", start+i), ContentType: "video/mp4", Size: 10}
	}
	return page
}

func layoutRunner(repository *layoutRepository, inspector *layoutInspector) *Runner {
	options := &CatalogSyncOptions{Repository: &doneSync{}}
	if repository != nil {
		options.Layouts = repository
	}
	if inspector != nil {
		options.LayoutInspector = inspector
	}
	return &Runner{options: Options{DBOperationTimeout: time.Second, PollInterval: time.Millisecond, CatalogSync: options}}
}

func TestSourceLayoutPass(t *testing.T) {
	lease := domain.JobLease{}
	if storage, err := layoutRunner(nil, nil).inspectLayouts(context.Background(), lease); err != nil || storage {
		t.Fatal("unwired pass", err)
	}
	full := layoutPage(0, domain.SourceLayoutInspectionBatch)
	repository := &layoutRepository{pages: [][]domain.SourceLayoutTarget{full, layoutPage(100, 3)}}
	inspector := &layoutInspector{failing: map[string]bool{full[1].SourceID: true}}
	if _, err := layoutRunner(repository, inspector).inspectLayouts(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	// The cursor follows the last target of a full page; a short page ends
	// the walk. A failed read is skipped, not recorded.
	if len(repository.afters) != 2 || repository.afters[0] != "" || repository.afters[1] != full[len(full)-1].SourceID {
		t.Fatalf("cursor %v", repository.afters)
	}
	if len(repository.recorded) != domain.SourceLayoutInspectionBatch+3-1 || inspector.calls != domain.SourceLayoutInspectionBatch+3 {
		t.Fatalf("recorded %d after %d reads", len(repository.recorded), inspector.calls)
	}
	for _, v := range repository.recorded {
		if v.SourceID == full[1].SourceID {
			t.Fatal("failed read recorded")
		}
	}
	// Storage errors fail the job as storage errors.
	broken := &layoutRepository{nextErr: domain.ErrDatabase}
	if storage, err := layoutRunner(broken, &layoutInspector{}).inspectLayouts(context.Background(), lease); !errors.Is(err, domain.ErrDatabase) || !storage {
		t.Fatal("storage error", err, storage)
	}
	// Cancellation stops between reads without recording.
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := &layoutRepository{pages: [][]domain.SourceLayoutTarget{full}}
	if _, err := layoutRunner(cancelled, &layoutInspector{cancel: cancel}).inspectLayouts(ctx, lease); !errors.Is(err, context.Canceled) || len(cancelled.recorded) != 0 {
		t.Fatal("cancelled pass", err, len(cancelled.recorded))
	}
}

func TestSourceLayoutPassIsBoundedPerJob(t *testing.T) {
	pages := make([][]domain.SourceLayoutTarget, 0)
	for i := 0; i*domain.SourceLayoutInspectionBatch < domain.SourceLayoutMaxPerJob+3*domain.SourceLayoutInspectionBatch; i++ {
		pages = append(pages, layoutPage(i*domain.SourceLayoutInspectionBatch, domain.SourceLayoutInspectionBatch))
	}
	repository := &layoutRepository{pages: pages}
	inspector := &layoutInspector{}
	if _, err := layoutRunner(repository, inspector).inspectLayouts(context.Background(), domain.JobLease{}); err != nil {
		t.Fatal(err)
	}
	if inspector.calls < domain.SourceLayoutMaxPerJob || inspector.calls >= domain.SourceLayoutMaxPerJob+domain.SourceLayoutInspectionBatch {
		t.Fatalf("%d reads in one sync", inspector.calls)
	}
}
