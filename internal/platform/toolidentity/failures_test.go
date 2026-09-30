package toolidentity

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOversizedSparseFilesRefusedBeforeHashOrExecution(t *testing.T) {
	for _, kind := range []string{"binary", "license"} {
		t.Run(kind, func(t *testing.T) {
			root, spec := identityFixture(t)
			relative, maximum := spec.ExecutablePath, maxExecutableBytes
			if kind == "license" {
				relative, maximum = spec.Licenses[0].Path, maxLicenseBytes
			}
			path := filepath.Join(root, relative)
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			makeSparse(t, file)
			if err := file.Truncate(maximum + 1); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			got := diagnose(context.Background(), root, spec, func(context.Context, string, string) ([]byte, error) {
				t.Fatal("oversized file reached execution")
				return nil, nil
			})
			if got.Reason != "size_limit" || got.State != "unavailable" {
				t.Fatalf("got %+v", got)
			}
			assertNoTemporary(t, root)
		})
	}
}

func TestInvalidProjectAndTemporaryParentAreSafeFailures(t *testing.T) {
	root, spec := identityFixture(t)
	never := func(context.Context, string, string) ([]byte, error) {
		t.Fatal("unsafe project executed")
		return nil, nil
	}
	for _, project := range []string{filepath.Join(root, "missing"), root + "\n", root + "\x00"} {
		if got := diagnose(context.Background(), project, spec, never); got.Reason != "unsafe_path" {
			t.Fatalf("got %+v", got)
		}
	}
	path := filepath.Join(root, ".testdata")
	if err := os.WriteFile(path, []byte("existing ordinary file"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := diagnose(context.Background(), root, spec, never); got.Reason != "unsafe_path" {
		t.Fatalf("got %+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing ordinary file" {
		t.Fatal("temporary-parent conflict changed existing content")
	}
	if err := makePrivate(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing private directory accepted")
	}
	if safeFileInfo(nil, true) || safeFileInfo(nil, false) {
		t.Fatal("missing file info accepted")
	}
	if _, err := runVersion(context.Background(), filepath.Join(root, "absent"), root); err == nil {
		t.Fatal("missing registered executable accepted")
	}
}

type noProgressReader struct{}

func (noProgressReader) Read([]byte) (int, error) { return 0, nil }

func TestHashCopyRealIOFailuresAndDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "closed")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if hash, err := hashCopy(context.Background(), file, io.Discard, 64); err == nil || hash != "" {
		t.Fatal("closed source was accepted")
	}
	if hash, err := hashCopy(context.Background(), strings.NewReader("source"), file, 64); err == nil || hash != "" {
		t.Fatal("closed destination was accepted")
	}
	if hash, err := hashCopy(context.Background(), noProgressReader{}, io.Discard, 64); !errors.Is(err, io.ErrNoProgress) || hash != "" {
		t.Fatal("no-progress reader was accepted")
	}
	if reason := copyReason(context.Background()); reason != "read_failed" {
		t.Fatal("read failure was not sanitized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); writer.CloseWithError(ctx.Err()) })
	var output bytes.Buffer
	hash, err := hashCopy(ctx, reader, &output, 64)
	if !stop() {
		<-done
	}
	if !errors.Is(err, context.DeadlineExceeded) || hash != "" || output.Len() != 0 || copyReason(ctx) != "timeout" {
		t.Fatal("deadline during read did not produce a safe empty failure")
	}
	if executionReason(ctx, errors.New("private diagnostic")) != "timeout" {
		t.Fatal("execution deadline lost priority")
	}
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if hash, err := hashCopy(cancelled, strings.NewReader("secret"), &output, 64); !errors.Is(err, context.Canceled) || hash != "" || copyReason(cancelled) != "cancelled" {
		t.Fatal("pre-cancelled copy was not refused")
	}
}

func TestCleanupFailureOverridesVerifiedResult(t *testing.T) {
	root, spec := identityFixture(t)
	var restore func()
	var owned string
	defer func() {
		if restore != nil {
			restore()
		}
		if owned != "" {
			os.RemoveAll(owned)
		}
	}()
	got := diagnose(context.Background(), root, spec, func(_ context.Context, _ string, temp string) ([]byte, error) {
		owned = temp
		restore = preventCleanup(t, temp)
		return []byte("ffprobe version test-1.0 Copyright synthetic"), nil
	})
	if got.State != "unavailable" || got.Reason != "cleanup_failed" || got.Capability != "disabled_sandbox" {
		t.Fatalf("cleanup error incorrectly reported success: %+v", got)
	}
	restore()
	restore = nil
	if err := os.RemoveAll(owned); err != nil {
		t.Fatal(err)
	}
	assertNoTemporary(t, root)
}
