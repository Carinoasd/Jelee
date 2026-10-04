package matroska

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// TestExtractionIsOffOnWindows checks the designed Windows behavior: even
// with both runners and a clean, existing cache root, no extractor exists.
func TestExtractionIsOffOnWindows(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	extractor, err := New(&fakeTool{output: identifyJSON}, &fakeTool{}, Config{CacheRoot: cache})
	if err != ErrUnavailable || extractor != nil {
		t.Fatalf("extractor on Windows: %v", err)
	}
	if _, err := extractor.Locate(context.Background(), testSource, domain.ProbeSource{RootPath: t.TempDir(), RelativePath: "Film.mkv"}, KindSubtitle, 1); err != ErrNotFound {
		t.Fatalf("locate without an extractor: %v", err)
	}
	if _, err := extractor.ExtractBitmaps(context.Background(), domain.ProbeSource{}, nil, t.TempDir()); err != ErrUnavailable {
		t.Fatalf("bitmaps without an extractor: %v", err)
	}
}
