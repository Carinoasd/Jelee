package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestConsistencyProberReadsMetadataInsideTheRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Movie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Movie", "a.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := ConsistencyProber{}
	ctx := context.Background()
	if ok, err := p.FileExists(ctx, root, "Movie/a.mkv"); err != nil || !ok {
		t.Fatalf("present file: %t %v", ok, err)
	}
	if ok, err := p.FileExists(ctx, root, "Movie/gone.mkv"); err != nil || ok {
		t.Fatalf("absent file: %t %v", ok, err)
	}
	if ok, err := p.FileExists(ctx, root, "Movie"); err != nil || ok {
		t.Fatalf("a directory is not a media file: %t %v", ok, err)
	}
	for name, relative := range map[string]string{"escape": "../a.mkv", "absolute": "/etc/passwd", "root": ".", "control": "a\x00b"} {
		if _, err := p.FileExists(ctx, root, relative); err == nil {
			t.Fatalf("%s path accepted", name)
		}
	}
	if _, err := p.FileExists(ctx, "relative/root", "a.mkv"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("relative root accepted", err)
	}
	if _, err := p.FileExists(ctx, filepath.Join(root, "missing-root"), "a.mkv"); !errors.Is(err, domain.ErrScanUnavailable) {
		t.Fatal("missing root is not unavailable", err)
	}
	if _, err := p.FileExists(nil, root, "Movie/a.mkv"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil context accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.FileExists(cancelled, root, "Movie/a.mkv"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled probe ran", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if ok, err := p.FileExists(ctx, root, "link/secret.mkv"); ok || err == nil {
		t.Fatalf("probe followed a link out of the root: %t %v", ok, err)
	}
}
