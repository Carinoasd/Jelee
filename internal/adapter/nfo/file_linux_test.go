//go:build linux

package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestReadFileRejectsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe.nfo"), 0o600); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS) {
			t.Skip("test filesystem does not support named pipes; native Linux filesystem verification required")
		}
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := ReadFile(context.Background(), root, "pipe.nfo", DefaultMaxBytes); finished <- err }()
	select {
	case err := <-finished:
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("FIFO accepted: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO blocked before cancellation could apply")
	}
}

func TestReadFileResistsSymlinkReplacement(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.nfo"), []byte(`<movie><title>Inside</title></movie>`), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "private.nfo")
	if err := os.WriteFile(secret, []byte(`<movie><title>Secret outside root</title></movie>`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	failures := make(chan error, 1)
	var worker sync.WaitGroup
	worker.Add(1)
	go func() {
		defer worker.Done()
		first := true
		for ctx.Err() == nil {
			for _, target := range []string{"inside.nfo", secret} {
				next := filepath.Join(root, "next.nfo")
				if err := os.Symlink(target, next); err != nil {
					failures <- err
					return
				}
				if err := os.Rename(next, filepath.Join(root, "current.nfo")); err != nil {
					failures <- err
					return
				}
				if first {
					close(started)
					first = false
				}
			}
		}
	}()
	defer func() { cancel(); worker.Wait() }()
	select {
	case <-started:
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("replacement did not start")
	}
	for i := 0; i < 100; i++ {
		document, err := ReadFile(context.Background(), root, "current.nfo", DefaultMaxBytes)
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatalf("unexpected error: %v", err)
		}
		if err == nil && document.Metadata.Title != "Inside" {
			t.Fatal("symlink replacement exposed another root")
		}
	}
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
}
