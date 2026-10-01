package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

func TestIsolatedProcessHelper(t *testing.T) {
	for index, arg := range os.Args {
		if arg != "--isolated-process-test" || index+1 >= len(os.Args) {
			continue
		}
		mode := os.Args[index+1]
		switch mode {
		case "stdout", "stderr":
			writer := os.Stdout
			if mode == "stderr" {
				writer = os.Stderr
			}
			for {
				if _, err := io.WriteString(writer, strings.Repeat("x", 32768)); err != nil {
					os.Exit(1)
				}
			}
		case "sleep":
			time.Sleep(30 * time.Second)
		case "stdin":
			_, _ = os.Stdin.Seek(0, io.SeekStart)
			_, _ = io.Copy(os.Stdout, os.Stdin)
		default:
			code, err := strconv.Atoi(mode)
			if err != nil {
				os.Exit(1)
			}
			fmt.Fprint(os.Stdout, "private metadata must be discarded")
			fmt.Fprint(os.Stderr, "private diagnostic must be discarded")
			os.Exit(code)
		}
		os.Exit(0)
	}
}

func isolatedFixture(t *testing.T, mode string, timeout time.Duration) (*IsolatedRunner, *os.File) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := Config{MaxConcurrent: 1, Timeout: timeout, MaxStdoutBytes: 1024, MaxStderrBytes: 1024, TempRoot: t.TempDir()}
	args := []string{"-test.run=^TestIsolatedProcessHelper$", "--", "--isolated-process-test", mode}
	core, err := newRunner(config, []Tool{{ID: "ffprobe", Path: executable, Operations: map[string][]string{"metadata": args}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("original input"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return &IsolatedRunner{runner: core}, file
}

func TestIsolatedExitClassificationDiscardsEveryFailureOutput(t *testing.T) {
	for code, expected := range map[string]error{"64": ErrSandboxInvalid, "78": ErrSandboxUnavailable, "1": ErrExit, "2": ErrUnexpectedExit, "127": ErrUnexpectedExit, "255": ErrUnexpectedExit} {
		t.Run(code, func(t *testing.T) {
			runner, file := isolatedFixture(t, code, 5*time.Second)
			result, err := runner.Run(context.Background(), Request{Tool: "ffprobe", Operation: "metadata", Stdin: file})
			if !errors.Is(err, expected) || len(result.Stdout) != 0 || result.StdoutBytes != 0 || result.StderrBytes != 0 {
				t.Fatalf("failed output or wrong classification: %v", err)
			}
			assertClean(t, runner.runner)
		})
	}
	if isolatedExitError(-1) != ErrUnexpectedExit {
		t.Fatal("signal termination was misclassified as rejected media")
	}
}

func TestIsolatedRequestsRequireSourceAndExactOperation(t *testing.T) {
	runner, file := isolatedFixture(t, "stdin", 5*time.Second)
	for _, request := range []Request{{}, {Tool: "ffprobe", Operation: "metadata"}, {Tool: "ffmpeg", Operation: "metadata", Stdin: file}, {Tool: "ffprobe", Operation: "version", Stdin: file}} {
		if _, err := runner.Run(context.Background(), request); err != ErrInvalid {
			t.Fatal("isolated factory accepted an alternate operation or absent source")
		}
	}
	result, err := runner.Run(context.Background(), Request{Tool: "ffprobe", Operation: "metadata", Stdin: file})
	if err != nil || string(result.Stdout) != "original input" {
		t.Fatalf("inherited input failed: %v", err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal("borrowed source was closed")
	}
	writable, err := os.OpenFile(file.Name(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writable.Close()
	if _, err := runner.Run(context.Background(), Request{Tool: "ffprobe", Operation: "metadata", Stdin: writable}); err != ErrInvalid {
		t.Fatal("writable source accepted")
	}
	assertClean(t, runner.runner)
}

func TestIsolatedBoundsCancellationAndQuotaRelease(t *testing.T) {
	for _, mode := range []string{"stdout", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			runner, file := isolatedFixture(t, mode, 5*time.Second)
			result, err := runner.Run(context.Background(), Request{Tool: "ffprobe", Operation: "metadata", Stdin: file})
			if err != ErrOutputLimit || len(result.Stdout) != 0 {
				t.Fatalf("unbounded isolated output: %v", err)
			}
			assertClean(t, runner.runner)
		})
	}
	runner, file := isolatedFixture(t, "sleep", 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	request := Request{Tool: "ffprobe", Operation: "metadata", Stdin: file}
	go func() { _, err := runner.Run(ctx, request); done <- err }()
	deadline := time.Now().Add(time.Second)
	for len(runner.runner.slots) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := runner.Run(context.Background(), request); err != ErrBusy {
		t.Fatalf("isolated quota failed: %v", err)
	}
	cancel()
	if err := <-done; err != ErrCancelled {
		t.Fatalf("cancellation was misclassified as media or sandbox failure: %v", err)
	}
	assertClean(t, runner.runner)
	timed, source := isolatedFixture(t, "sleep", 100*time.Millisecond)
	if result, err := timed.Run(context.Background(), Request{Tool: "ffprobe", Operation: "metadata", Stdin: source}); err != ErrTimeout || len(result.Stdout) != 0 {
		t.Fatalf("isolated deadline failed: %v", err)
	}
	assertClean(t, timed.runner)
}

func TestIsolatedFactoryCannotRegisterAnArbitraryProgram(t *testing.T) {
	config := Config{MaxConcurrent: 1, Timeout: time.Second, MaxStdoutBytes: 1024, MaxStderrBytes: 1024, TempRoot: t.TempDir()}
	for _, launcher := range []*sandbox.Launcher{nil, {}} {
		if _, err := NewIsolatedFFprobe(config, launcher); err != ErrInvalid {
			t.Fatal("unverified launcher accepted")
		}
	}
	executable, _ := os.Executable()
	if _, err := New(config, []Tool{{ID: "ffprobe", Path: executable, Operations: map[string][]string{"metadata": {sandbox.HelperCommand, "descriptor"}}}}); err != ErrInvalid {
		t.Fatal("ordinary constructor accepted an arbitrary program")
	}
	var absent *IsolatedRunner
	if _, err := absent.Run(context.Background(), Request{}); err != ErrInvalid {
		t.Fatal("nil runner accepted")
	}
}
