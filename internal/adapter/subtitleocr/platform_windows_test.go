package subtitleocr

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

type windowsRunner struct{ calls int }

func (r *windowsRunner) Run(context.Context, process.ToolRequest) (process.Result, error) {
	r.calls++
	return process.Result{Stdout: []byte("text")}, nil
}

type windowsExtractor struct{}

func (windowsExtractor) ExtractBitmaps(context.Context, domain.ProbeSource, *probe.Input, string) ([]matroska.BitmapTrack, error) {
	return nil, nil
}

type windowsRecognizer struct{}

func (windowsRecognizer) Recognize(context.Context, *image.Gray, []string) (string, error) {
	return "text", nil
}

func cleanDirectory(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestOCRIsOffOnWindows checks the designed Windows behavior: even with
// working collaborators and clean, existing directories, neither the
// recognizer nor the service exists.
func TestOCRIsOffOnWindows(t *testing.T) {
	runner := &windowsRunner{}
	recognizer, err := NewRecognizer(runner, cleanDirectory(t))
	if err != ErrUnavailable || recognizer != nil {
		t.Fatalf("recognizer on Windows: %v", err)
	}
	service, err := New(windowsExtractor{}, windowsRecognizer{}, Config{CacheRoot: cleanDirectory(t), WorkRoot: cleanDirectory(t), Identity: "recognizer-a",
		Languages: []string{"eng"}, PicturesPerMinute: 60, Concurrency: 1, QueueSize: 1})
	if err != ErrUnavailable || service != nil {
		t.Fatalf("service on Windows: %v", err)
	}
	if _, err := service.Locate(context.Background(), "00000000-0000-4000-8000-000000000001", domain.ProbeSource{RootPath: t.TempDir(), RelativePath: "Film.mkv"}, 3); err != ErrNotFound {
		t.Fatalf("locate without a service: %v", err)
	}
	if service.Stats() != (Stats{}) || service.Close() != nil || runner.calls != 0 {
		t.Fatal("absent service")
	}
}
