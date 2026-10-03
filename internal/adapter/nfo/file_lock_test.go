//go:build windows || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func lockTestRoot(t *testing.T, path string) *os.Root {
	t.Helper()
	dir, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func TestNFOFileLockCancellationRecoveryAndIndependentTargets(t *testing.T) {
	dir := lockTestRoot(t, t.TempDir())
	first, err := lockNFOFile(context.Background(), dir, "movie.nfo")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := lockNFOFile(ctx, dir, "movie.nfo"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock: %v", err)
	}
	other, err := lockNFOFile(ctx, dir, "episode.nfo")
	if !errors.Is(err, context.DeadlineExceeded) || other != nil {
		t.Fatal("expired context acquired lock")
	}
	other, err = lockNFOFile(context.Background(), dir, "episode.nfo")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal("close not idempotent")
	}
	recovered, err := lockNFOFile(context.Background(), dir, "movie.nfo")
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNFOFileLockSerializesOneHundredCriticalSections(t *testing.T) {
	path := t.TempDir()
	dir := lockTestRoot(t, path)
	if err := os.WriteFile(filepath.Join(path, "counter"), []byte("0"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock, err := lockNFOFile(ctx, dir, "movie.nfo")
			if err != nil {
				t.Error(err)
				return
			}
			defer func() {
				if err := lock.Close(); err != nil {
					t.Error(err)
				}
			}()
			data, err := dir.ReadFile("counter")
			if err != nil {
				t.Error(err)
				return
			}
			n, err := strconv.Atoi(string(data))
			if err != nil {
				t.Error("interleaved critical section")
				return
			}
			if err := dir.WriteFile("counter", []byte(strconv.Itoa(n+1)), 0600); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data, err := dir.ReadFile("counter")
	if err != nil || string(data) != "100" {
		t.Fatalf("lost update: %q %v", data, err)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 2 {
		t.Fatal("sidecar was removed or duplicated")
	}
}

func TestNFOFileLockCrossProcess(t *testing.T) {
	path := t.TempDir()
	dir := lockTestRoot(t, path)
	lock, err := lockNFOFile(context.Background(), dir, "movie.nfo")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	run := func(expect string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestNFOFileLockProcessHelper$")
		cmd.Env = append(os.Environ(), "JELEE_NFO_LOCK_TEST_DIR="+path)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), expect) {
			t.Fatalf("child lock: %v %s", err, out)
		}
	}
	run("lock_deadline")
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	run("lock_acquired")
}

func TestNFOFileLockProcessHelper(t *testing.T) {
	path := os.Getenv("JELEE_NFO_LOCK_TEST_DIR")
	if path == "" {
		return
	}
	dir := lockTestRoot(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	lock, err := lockNFOFile(ctx, dir, "movie.nfo")
	if errors.Is(err, context.DeadlineExceeded) {
		t.Log("lock_deadline")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("lock_acquired")
}

func TestNFOFileLockRejectsInvalidSidecarAndInputs(t *testing.T) {
	path := t.TempDir()
	dir := lockTestRoot(t, path)
	for _, name := range []string{"../movie.nfo", "movie.nfo\n", "movie.mkv", "C:movie.nfo"} {
		if _, err := lockNFOFile(context.Background(), dir, name); err != ErrInvalidInput {
			t.Fatal("invalid name accepted")
		}
	}
	if _, err := lockNFOFile(nil, dir, "movie.nfo"); err != ErrInvalidInput {
		t.Fatal("nil context accepted")
	}
	if _, err := lockNFOFile(context.Background(), nil, "movie.nfo"); err != ErrInvalidInput {
		t.Fatal("nil root accepted")
	}
	digest := sha256.Sum256([]byte(lockNameKey("movie.nfo")))
	sidecar := filepath.Join(path, ".jelee-nfo-"+hex.EncodeToString(digest[:])+".lock")
	if err := os.WriteFile(sidecar, []byte("user content"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockNFOFile(context.Background(), dir, "movie.nfo"); err != ErrFileLock {
		t.Fatal("nonempty sidecar accepted")
	}
	content, _ := os.ReadFile(sidecar)
	if string(content) != "user content" {
		t.Fatal("existing file changed")
	}
	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sidecar, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := lockNFOFile(context.Background(), dir, "movie.nfo"); err != ErrFileLock {
		t.Fatal("directory accepted")
	}
}

func TestNFOFileLockWindowsCaseAliases(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows filename case aliases")
	}
	dir := lockTestRoot(t, t.TempDir())
	lock, err := lockNFOFile(context.Background(), dir, "Movie.NFO")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := lockNFOFile(ctx, dir, "movie.nfo"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("case alias bypassed lock")
	}
}
