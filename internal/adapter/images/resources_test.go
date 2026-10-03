package images

import (
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

type recordingBudget struct {
	budget  *resources.Budget
	classes []app.WorkClass
}

func (b *recordingBudget) Acquire(ctx context.Context, class app.WorkClass) (func(), error) {
	b.classes = append(b.classes, class)
	return b.budget.Acquire(ctx, class)
}

func TestImageSharedBudgetPhasesAndBodyReservation(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	processorTestPNG(t, source, color.NRGBA{R: 255, A: 255})
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
	recorded := &recordingBudget{budget: budget}
	opts := processorTestOptions(scratch)
	opts.Budget = recorded
	p := processorTestNew(t, opts)
	decode := p.decode
	p.decode = func(ctx context.Context, r io.ReadSeeker, info inspectedImage) (image.Image, error) {
		if s := budget.Stats(); s != (resources.Stats{CPU: 1, Total: 1}) {
			t.Errorf("decode without CPU: %+v", s)
		}
		return decode(ctx, r, info)
	}
	for _, hit := range []bool{false, true} {
		recorded.classes = nil
		result, err := p.Render(context.Background(), source, domain.ImageRequest{Width: 16, Height: 16})
		if err != nil {
			t.Fatal(err)
		}
		want := []app.WorkClass{app.WorkIO, app.WorkCPU, app.WorkIO}
		if hit {
			want = []app.WorkClass{app.WorkIO, app.WorkIO}
		}
		if !reflect.DeepEqual(recorded.classes, want) {
			t.Errorf("phase sequence: %v", recorded.classes)
		}
		if budget.Stats() != (resources.Stats{}) {
			t.Error("response body retained shared processing permit")
		}
		if p.Stats().Active != 1 {
			t.Error("response lost memory reservation")
		}
		if _, err := io.Copy(io.Discard, result.Body); err != nil {
			t.Error(err)
		}
		if err := result.Body.Close(); err != nil {
			t.Error(err)
		}
		if p.Stats().Active != 0 {
			t.Fatal("body leaked memory reservation")
		}
	}
}

func TestImageSharedBudgetQueueFullAndCancellation(t *testing.T) {
	for _, queue := range []int{0, 1} {
		source, scratch := imageSourceFixture(t)
		processorTestPNG(t, source, color.NRGBA{A: 255})
		budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: queue})
		held, err := budget.Acquire(context.Background(), app.WorkCPU)
		if err != nil {
			t.Fatal(err)
		}
		defer held()
		opts := processorTestOptions(scratch)
		opts.Budget = budget
		p := processorTestNew(t, opts)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			result, err := p.Render(ctx, source, domain.ImageRequest{Width: 16, Height: 16})
			if result.Body != nil {
				result.Body.Close()
			}
			done <- err
		}()
		if queue == 1 {
			timer := time.NewTimer(3 * time.Second)
			tick := time.NewTicker(time.Millisecond)
			for budget.Stats().Waiting != 1 {
				select {
				case <-timer.C:
					t.Fatal("image did not queue")
				case <-tick.C:
				}
			}
			timer.Stop()
			tick.Stop()
			cancel()
		}
		select {
		case err := <-done:
			want := domain.ErrImageBusy
			if queue == 1 {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Errorf("result %v want %v", err, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("image did not finish")
		}
		cancel()
		held()
		if budget.Stats() != (resources.Stats{}) || p.Stats().Active != 0 {
			t.Fatal("image cancellation leaked resources")
		}
	}
}
