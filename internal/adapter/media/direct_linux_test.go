//go:build linux

package media

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
)

func TestFIFOIsRejectedWithoutBlocking(t *testing.T) {
	source := Source{Root: t.TempDir(), RelativePath: "fifo"}
	if err := syscall.Mkfifo(filepath.Join(source.Root, source.RelativePath), 0o600); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS) {
			t.Skip("test filesystem does not support named pipes; run on a native Linux filesystem to verify FIFO rejection")
		}
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); assertSourceHidden(t, source) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("FIFO blocked delivery handler")
	}
}

func TestConcurrentSymlinkReplacementCannotEscapeRoot(t *testing.T) {
	source, _ := fixture(t)
	source.RelativePath = "current"
	outside := t.TempDir()
	secret := filepath.Join(outside, "private")
	if err := os.WriteFile(secret, []byte("secret-outside-root"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	swapped := make(chan struct{})
	swapErrors := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		first := true
		for ctx.Err() == nil {
			for _, target := range []string{"original.mkv", secret} {
				next := filepath.Join(source.Root, "next")
				_ = os.Remove(next)
				if err := os.Symlink(target, next); err != nil {
					swapErrors <- err
					return
				}
				if err := os.Rename(next, filepath.Join(source.Root, "current")); err != nil {
					swapErrors <- err
					return
				}
				if first {
					close(swapped)
					first = false
				}
			}
		}
	}()
	defer func() { cancel(); workers.Wait() }()
	select {
	case <-swapped:
	case err := <-swapErrors:
		t.Fatalf("could not exercise symlink replacement: %v", err)
	case <-time.After(time.Second):
		t.Fatal("symlink replacement did not start")
	}
	handler := testHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }), 1)
	for i := 0; i < 100; i++ {
		r := nativeRequest("GET", "/stream")
		r.Header.Set("Range", "bytes=0-63")
		w := httptest.NewRecorder()
		handler.ServeSource(w, r, "source")
		if strings.Contains(w.Body.String(), "secret-outside-root") || (w.Code != 404 && w.Code != 206) {
			t.Fatalf("root escape response: status=%d body=%q", w.Code, w.Body.String())
		}
	}
	select {
	case err := <-swapErrors:
		t.Fatalf("symlink replacement stopped early: %v", err)
	default:
	}
}
