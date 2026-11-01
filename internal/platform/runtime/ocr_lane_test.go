package runtime

import (
	"context"
	"errors"
	"image"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/adapter/subtitleocr"
	"github.com/MoYuanCN/Jelee/internal/domain"
	jobworker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

// blockingExtractor holds every OCR job in extraction until it is cancelled.
type blockingExtractor struct{ started chan struct{} }

func (e blockingExtractor) ExtractBitmaps(ctx context.Context, _ domain.ProbeSource, _ *probe.Input, _ string) ([]matroska.BitmapTrack, error) {
	select {
	case e.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type idleRecognizer struct{}

func (idleRecognizer) Recognize(context.Context, *image.Gray, []string) (string, error) {
	return "", errors.New("not used")
}

func ocrPrivateDir(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// testOCRService builds a real OCR service over fakes; OCR does not run on
// Windows.
func testOCRService(t *testing.T, extractor subtitleocr.Extractor) *subtitleocr.Service {
	t.Helper()
	service, err := subtitleocr.New(extractor, idleRecognizer{}, subtitleocr.Config{CacheRoot: ocrPrivateDir(t, "cache"), WorkRoot: ocrPrivateDir(t, "work"),
		Languages: []string{"eng"}, PicturesPerMinute: 60, Concurrency: 1, QueueSize: 2, Identity: "recognizer-a"})
	if errors.Is(err, subtitleocr.ErrUnavailable) {
		t.Skip("subtitle OCR is unavailable on this platform")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

// The OCR service's queue goes on the background lane, once, as the
// cluster-wide subtitle_ocr job; without OCR nothing is registered.
func TestRegisterOCRPutsTheJobOnTheLane(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	budget, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 2, Queue: 4})
	if err != nil {
		t.Fatal(err)
	}
	l := newLifetime(logger)
	if err = l.registerOCR(&postgres.Store{}, budget, nil); err != nil || l.background != nil {
		t.Fatal("no OCR service registered a job")
	}
	service := testOCRService(t, blockingExtractor{started: make(chan struct{}, 1)})
	if err = l.registerOCR(&postgres.Store{}, budget, service); err != nil || l.background == nil || !slices.Equal(l.background.Kinds(), []string{domain.JobSubtitleOCR}) {
		t.Fatalf("OCR job not registered: %v", err)
	}
	if err = l.registerOCR(&postgres.Store{}, budget, service); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second registration: %v", err)
	}
	// A cluster-wide job needs the store's lock.
	if err = newLifetime(logger).registerOCR(nil, budget, service); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("registered without a lock: %v", err)
	}
}

// Shutdown stops the OCR job with the other background jobs: the job in
// progress is cancelled and joined before the pool and the OCR service
// (with its scratch directory) close, as webhook delivery is.
func TestOCRJobJoinsBeforeItsServiceCloses(t *testing.T) {
	l, a, closed, _ := testLifetime(t, nil, http.NotFoundHandler())
	extractor := blockingExtractor{started: make(chan struct{}, 1)}
	service := testOCRService(t, extractor)
	lane, err := jobworker.NewBackground(jobworker.BackgroundOptions{Locker: lifetimeLocker{}, Logger: l.logger})
	if err != nil {
		t.Fatal(err)
	}
	l.background = lane
	if err = l.registerOCR(nil, nil, service); err != nil {
		t.Fatal(err)
	}
	group := func() [4]uint64 {
		return lane.BackgroundJobMetrics().Groups[domain.BackgroundJobKindIndex(domain.JobSubtitleOCR)].Outcomes
	}
	closedOCR := false
	l.closeMatroska = func() error {
		if closed.Load() != 0 || service.Stats().Running != 0 || group()[2] != 1 {
			t.Errorf("OCR closed before its job joined: pool=%d stats=%+v outcomes=%v", closed.Load(), service.Stats(), group())
		}
		closedOCR = true
		return service.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(t.TempDir(), "Film.mkv")
	if err = os.WriteFile(media, []byte("matroska stand-in"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Locate(ctx, "00000000-0000-4000-8000-0000000000d1", domain.ProbeSource{RootPath: filepath.Dir(media), RelativePath: "Film.mkv"}, 0); !errors.Is(err, subtitleocr.ErrPending) {
		t.Fatal(err)
	}
	select {
	case <-extractor.started:
	case <-ctx.Done():
		t.Fatal("OCR job did not start on the lane")
	}
	if err = a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !closedOCR || closed.Load() != 1 {
		t.Fatalf("closed OCR=%t pool=%d", closedOCR, closed.Load())
	}
	if stats := service.Stats(); stats.Failed != 0 || stats.Completed != 0 {
		t.Fatalf("cancelled job counted: %+v", stats)
	}
}
