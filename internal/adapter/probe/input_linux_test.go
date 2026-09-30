//go:build linux

package probe

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestLinuxFIFORefusedWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe.mp4"), 0600); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EPERM) {
			t.Skip("filesystem cannot create FIFO; use native Linux filesystem")
		}
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if input, err := Open(ctx, Source{RootPath: root, RelativePath: "pipe.mp4"}); input != nil || err != ErrUnavailable {
		t.Fatalf("FIFO input was not refused immediately: %v", err)
	}
	if _, err := Open(ctx, Source{RootPath: filepath.Join(root, "pipe.mp4"), RelativePath: "child"}); err != ErrUnavailable {
		t.Fatal("FIFO root not rejected")
	}
}

func TestLinuxUnreadableInputFailsWithoutPermissionChange(t *testing.T) {
	root := t.TempDir()
	name := writeInput(t, root, "unreadable.mp4", []byte("protected"))
	if err := os.Chmod(name, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(name, 0600)
	if _, err := os.ReadFile(name); !errors.Is(err, fs.ErrPermission) {
		t.Skip("current identity or filesystem does not enforce mode 000")
	}
	if input, err := Open(context.Background(), Source{RootPath: root, RelativePath: "unreadable.mp4"}); input != nil || err != ErrUnavailable {
		t.Fatalf("unreadable file did not fail closed: %v", err)
	}
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != 0 {
		t.Fatal("source permissions changed")
	}
}

func TestLinuxConcurrentSourceReplacementCannotOpenOutsideRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	name := writeInput(t, root, "movie.mp4", []byte("inside original"))
	secret := writeInput(t, outside, "private.mp4", []byte("outside secret"))
	parked := filepath.Join(root, "parked.mp4")
	var stop atomic.Bool
	var exchanges atomic.Int64
	done := make(chan error, 1)
	go func() {
		for !stop.Load() {
			if err := os.Rename(name, parked); err != nil {
				done <- err
				return
			}
			if err := os.Symlink(secret, name); err != nil {
				_ = os.Rename(parked, name)
				done <- err
				return
			}
			if err := os.Remove(name); err != nil {
				done <- err
				return
			}
			if err := os.Rename(parked, name); err != nil {
				done <- err
				return
			}
			exchanges.Add(1)
		}
		done <- nil
	}()
	defer func() {
		stop.Store(true)
		if err := <-done; err != nil {
			t.Fatal("replacement setup failed: ", err)
		}
		if exchanges.Load() == 0 {
			t.Fatal("replacement race was not exercised")
		}
		t.Logf("completed %d source/symlink replacement cycles", exchanges.Load())
	}()
	for range 300 {
		input, err := Open(context.Background(), Source{RootPath: root, RelativePath: "movie.mp4"})
		if err == ErrUnavailable {
			continue
		}
		if err != nil {
			t.Fatal("unexpected opening error: ", err)
		}
		data, err := io.ReadAll(input.Stdin())
		_ = input.Close()
		if err != nil || string(data) != "inside original" {
			t.Fatal("outside bytes escaped or pinned input changed")
		}
	}
}
