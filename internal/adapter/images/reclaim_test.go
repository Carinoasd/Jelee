package images

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func largeImagePreflightFixture(t *testing.T) (domain.LocalImageSource, string) {
	t.Helper()
	source, scratch := imageSourceFixture(t)
	path := processorTestPNG(t, source, color.NRGBA{R: 255, A: 255})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The controlled decoder owns the payload; only preflight needs large bounds.
	binary.BigEndian.PutUint32(data[16:20], 4096)
	binary.BigEndian.PutUint32(data[20:24], 2560)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return source, scratch
}

func TestLargeImageReclaimsFailedDecodeBeforeReleasingBudget(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "decode_error"
		if cancelled {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			// Only the accepted preflight is needed: the controlled decoder fails
			// before creating a bitmap. The reservation must still cover reclamation.
			source, scratch := largeImagePreflightFixture(t)
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
			options := processorTestOptions(scratch)
			options.Budget = budget
			p := processorTestNew(t, options)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.decode = func(context.Context, io.ReadSeeker, inspectedImage) (image.Image, error) {
				if cancelled {
					cancel()
					return nil, context.Canceled
				}
				return nil, domain.ErrImageUnavailable
			}
			var calls atomic.Int32
			p.reclaim = func() {
				calls.Add(1)
				if budget.Stats() != (resources.Stats{CPU: 1, Total: 1}) || p.Stats().Active != 1 {
					t.Error("reclamation lost CPU or memory reservation")
				}
			}
			result, err := p.Render(ctx, source, domain.ImageRequest{Width: 1024, Height: 1024})
			want := domain.ErrImageUnavailable
			if cancelled {
				want = context.Canceled
			}
			if err != want || result.Body != nil || calls.Load() != 1 {
				t.Fatalf("reclaim before error return: err=%v calls=%d", err, calls.Load())
			}
			if p.Stats().Active != 0 || budget.Stats() != (resources.Stats{}) {
				t.Fatal("failed decode leaked admission")
			}
		})
	}
}

func TestLargeImageCancellationAndShutdownJoinReclamation(t *testing.T) {
	source, scratch := largeImagePreflightFixture(t)
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
	options := processorTestOptions(scratch)
	options.Budget, options.MaxConcurrent = budget, 1
	p := processorTestNew(t, options)
	p.decode = func(context.Context, io.ReadSeeker, inspectedImage) (image.Image, error) {
		return nil, domain.ErrImageUnavailable
	}
	entered, unblock := make(chan struct{}), make(chan struct{})
	var unblockOnce sync.Once
	defer unblockOnce.Do(func() { close(unblock) })
	p.reclaim = func() { close(entered); <-unblock }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		result, err := p.Render(ctx, source, domain.ImageRequest{Width: 1024, Height: 1024})
		if result.Body != nil {
			_ = result.Body.Close()
		}
		returned <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reclamation did not start")
	}
	cancel()
	if _, err := p.Render(context.Background(), source, domain.ImageRequest{}); err != domain.ErrImageBusy {
		t.Fatal("reclamation released the image slot")
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShutdown()
	if err := p.Shutdown(shutdown); err != context.DeadlineExceeded {
		t.Fatal("shutdown abandoned active reclamation")
	}
	select {
	case err := <-returned:
		t.Fatalf("cancelled render abandoned reclamation: %v", err)
	default:
	}
	if budget.Stats() != (resources.Stats{CPU: 1, Total: 1}) || p.Stats().Active != 1 {
		t.Fatal("reclamation lost CPU or memory admission")
	}
	unblockOnce.Do(func() { close(unblock) })
	select {
	case err := <-returned:
		if err != context.Canceled {
			t.Fatalf("cancelled render error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("render did not join completed reclamation")
	}
	if budget.Stats() != (resources.Stats{}) || p.Stats().Active != 0 {
		t.Fatal("joined reclamation leaked admission")
	}
}

func TestSmallImageAndCacheHitAvoidFullReclamation(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	processorTestPNG(t, source, color.NRGBA{R: 255, A: 255})
	p := processorTestNew(t, processorTestOptions(scratch))
	p.reclaim = func() { t.Error("small image invoked process-wide reclamation") }
	for i := 0; i < 2; i++ {
		result, err := p.Render(context.Background(), source, domain.ImageRequest{Width: 16, Height: 16})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, result.Body); err != nil {
			t.Fatal(err)
		}
		if err := result.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if p.Stats().Decodes != 1 || p.Stats().CacheHits != 1 {
		t.Fatal("cache hit performed new processing")
	}
}
