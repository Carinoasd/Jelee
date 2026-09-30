package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcessHelper(t *testing.T) {
	index := -1
	for i, argument := range os.Args {
		if argument == "--helper-process" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	args := os.Args[index+1:]
	switch args[0] {
	case "echo":
		_ = json.NewEncoder(os.Stdout).Encode(args[1:])
	case "environment":
		_ = json.NewEncoder(os.Stdout).Encode(os.Environ())
	case "stdin":
		_, err := os.Stdin.Seek(2, io.SeekStart)
		if err != nil {
			os.Exit(7)
		}
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "extra-file":
		fd, _ := strconv.ParseUint(args[1], 10, 64)
		file := os.NewFile(uintptr(fd), "untrusted inherited descriptor")
		data := make([]byte, 128)
		n, _ := file.Read(data)
		_ = json.NewEncoder(os.Stdout).Encode(strings.Contains(string(data[:n]), "PRIVATE_FD_CONTENT"))
	case "flood", "stderr-flood":
		output := os.Stdout
		if args[0] == "stderr-flood" {
			output = os.Stderr
		}
		block := []byte(strings.Repeat("x", 32768))
		for {
			if _, err := output.Write(block); err != nil {
				os.Exit(8)
			}
		}
	case "dual-flood":
		go func() {
			for {
				_, _ = os.Stdout.Write([]byte(strings.Repeat("x", 32768)))
			}
		}()
		for {
			_, _ = os.Stderr.Write([]byte(strings.Repeat("y", 32768)))
		}
	case "sleep":
		time.Sleep(30 * time.Second)
	case "failure":
		fmt.Fprint(os.Stdout, "/private/source/secret.mkv")
		fmt.Fprint(os.Stderr, "secret-token /private/source/secret.mkv")
		os.Exit(12)
	case "tree", "tree-exit", "child", "grandchild":
		file, err := os.OpenFile(args[1], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(9)
		}
		_, _ = fmt.Fprintln(file, os.Getpid())
		_ = file.Close()
		if args[0] != "grandchild" {
			next := "child"
			if args[0] == "child" {
				next = "grandchild"
			}
			executable, _ := os.Executable()
			command := exec.Command(executable, "-test.run=^TestProcessHelper$", "--", "--helper-process", next, args[1])
			command.Stdout, command.Stderr = os.Stdout, os.Stderr
			if command.Start() != nil {
				os.Exit(10)
			}
			if args[0] == "tree-exit" {
				for attempt := 0; attempt < 500; attempt++ {
					data, _ := os.ReadFile(args[1])
					if len(strings.Fields(string(data))) == 3 {
						os.Exit(0)
					}
					time.Sleep(10 * time.Millisecond)
				}
				os.Exit(11)
			}
		}
		time.Sleep(30 * time.Second)
	default:
		os.Exit(13)
	}
	os.Exit(0)
}

func helperArgs(args ...string) []string {
	return append([]string{"-test.run=^TestProcessHelper$", "--", "--helper-process"}, args...)
}

func testRunner(t *testing.T, timeout time.Duration, operations map[string][]string) *Runner {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := newRunner(Config{MaxConcurrent: 1, Timeout: timeout, MaxStdoutBytes: 65536, MaxStderrBytes: 65536, TempRoot: t.TempDir()}, []Tool{{ID: "helper", Path: executable, Operations: operations}}, true)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func assertClean(t *testing.T, r *Runner) {
	t.Helper()
	entries, err := os.ReadDir(r.config.TempRoot)
	if err != nil || len(entries) != 0 || len(r.slots) != 0 {
		t.Fatalf("leaked temporary data or slot: entries=%d error=%v slots=%d", len(entries), err, len(r.slots))
	}
}

func TestFixedArgumentsAreLiteralAndRegistryIsCopied(t *testing.T) {
	values := []string{"", "a b", `quote"end`, `backslash\`, "$(touch SHOULD_NOT_EXIST)", "; rm -rf /", "& echo escaped", "影片測試", "line\nbreak"}
	arguments := helperArgs(append([]string{"echo"}, values...)...)
	operations := map[string][]string{"echo": arguments}
	r := testRunner(t, 5*time.Second, operations)
	arguments[len(arguments)-1] = "mutated"
	operations["echo"] = helperArgs("failure")
	result, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	var actual []string
	if json.Unmarshal(result.Stdout, &actual) != nil || strings.Join(actual, "\x00") != strings.Join(values, "\x00") {
		t.Fatalf("argv changed: %q", actual)
	}
	public, _ := json.Marshal(result)
	if string(public) != "{}" {
		t.Fatal("raw output is serializable")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", result, result), "SHOULD_NOT_EXIST") {
		t.Fatal("raw output escaped default formatting")
	}
	assertClean(t, r)
}

func TestEnvironmentDoesNotInheritCredentials(t *testing.T) {
	t.Setenv("JELEE_DATABASE_URL", "private-database-credential")
	t.Setenv("LD_PRELOAD", "private-loader-path")
	t.Setenv("FFREPORT", "private-report-path")
	r := testRunner(t, 5*time.Second, map[string][]string{"env": helperArgs("environment")})
	result, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "env"})
	if err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{"JELEE_DATABASE_URL", "LD_PRELOAD", "FFREPORT", "private-"} {
		if strings.Contains(string(result.Stdout), denied) {
			t.Fatal("inherited forbidden environment")
		}
	}
	if !strings.Contains(string(result.Stdout), "TMPDIR=") {
		t.Fatal("missing isolated temporary directory")
	}
	assertClean(t, r)
}

func TestOpenedStdinRemainsSeekableAndBorrowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	r := testRunner(t, 5*time.Second, map[string][]string{"stdin": helperArgs("stdin")})
	result, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "stdin", Stdin: file})
	if err != nil || string(result.Stdout) != "23456789" {
		t.Fatalf("seekable stdin: %q %v", result.Stdout, err)
	}
	if _, err = file.Stat(); err != nil {
		t.Fatal("borrowed input was closed")
	}
	assertClean(t, r)
}

func TestOutputFloodsAreBoundedAndReaped(t *testing.T) {
	for _, mode := range []string{"flood", "stderr-flood", "dual-flood"} {
		t.Run(mode, func(t *testing.T) {
			r := testRunner(t, 5*time.Second, map[string][]string{"flood": helperArgs(mode)})
			result, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "flood"})
			if !errors.Is(err, ErrOutputLimit) || len(result.Stdout) != 0 {
				t.Fatalf("output limit: %v bytes=%d", err, len(result.Stdout))
			}
			assertClean(t, r)
		})
	}
}

func TestSilentTimeoutCancellationBusyAndSafeExit(t *testing.T) {
	r := testRunner(t, 300*time.Millisecond, map[string][]string{"sleep": helperArgs("sleep"), "failure": helperArgs("failure")})
	start := time.Now()
	_, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "sleep"})
	if !errors.Is(err, ErrTimeout) || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout: %v", err)
	}
	assertClean(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, Request{Tool: "helper", Operation: "sleep"}); done <- err }()
	deadline := time.Now().Add(time.Second)
	for len(r.slots) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_, err = r.Run(context.Background(), Request{Tool: "helper", Operation: "sleep"})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancel: %v", err)
	}
	failureRunner := testRunner(t, 5*time.Second, map[string][]string{"failure": helperArgs("failure")})
	result, err := failureRunner.Run(context.Background(), Request{Tool: "helper", Operation: "failure"})
	if !errors.Is(err, ErrExit) || len(result.Stdout) != 0 || strings.Contains(err.Error(), "private") {
		t.Fatalf("safe exit: %v", err)
	}
	assertClean(t, r)
	assertClean(t, failureRunner)
}

func TestDescendantsDieOnCancelAndNormalParentExit(t *testing.T) {
	for _, mode := range []string{"tree", "tree-exit"} {
		t.Run(mode, func(t *testing.T) {
			pidsPath := filepath.Join(t.TempDir(), "pids")
			r := testRunner(t, 10*time.Second, map[string][]string{"tree": helperArgs(mode, pidsPath)})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := r.Run(ctx, Request{Tool: "helper", Operation: "tree"}); done <- err }()
			var pids []string
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				data, _ := os.ReadFile(pidsPath)
				pids = strings.Fields(string(data))
				if len(pids) == 3 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if len(pids) != 3 {
				cancel()
				<-done
				t.Fatal("helper descendants did not initialize")
			}
			if mode == "tree" {
				cancel()
			}
			err := <-done
			if mode == "tree" && !errors.Is(err, ErrCancelled) || mode == "tree-exit" && err != nil {
				t.Fatalf("tree result: %v", err)
			}
			for _, raw := range pids {
				pid, _ := strconv.Atoi(raw)
				deadline := time.Now().Add(3 * time.Second)
				for processAlive(pid) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if processAlive(pid) {
					t.Fatalf("descendant still running: %d", pid)
				}
			}
			assertClean(t, r)
		})
	}
}

func TestRepeatedRunsDoNotLeakHandlesOrGoroutines(t *testing.T) {
	previous := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(previous)
	r := testRunner(t, 5*time.Second, map[string][]string{"echo": helperArgs("echo", "ok")})
	// Warm the Go runtime's OS-thread/event pool before measuring persistent
	// handles; first blocking Win32 waits may initialize additional runtime Ms.
	for i := 0; i < 25; i++ {
		if _, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "echo"}); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	before := resourceCount()
	goroutines := runtime.NumGoroutine()
	for i := 0; i < 25; i++ {
		if _, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "echo"}); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	t.Logf("resources before=%d after=%d; goroutines before=%d after=%d", before, resourceCount(), goroutines, runtime.NumGoroutine())
	if after := resourceCount(); after > before+2 {
		t.Fatalf("resource leak before=%d after=%d", before, after)
	}
	if runtime.NumGoroutine() > goroutines+1 {
		t.Fatal("worker goroutine leak")
	}
	assertClean(t, r)
}

func TestInvalidConfigurationAndRequestsFailClosed(t *testing.T) {
	r := testRunner(t, time.Second, map[string][]string{"echo": helperArgs("echo")})
	for _, request := range []Request{{}, {Tool: "helper", Operation: "unregistered"}, {Tool: "ffmpeg", Operation: "echo"}} {
		if _, err := r.Run(context.Background(), request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid request: %v", err)
		}
	}
	if _, err := New(r.config, []Tool{{ID: "ffmpeg", Path: r.tools["helper"].Path, Operations: r.tools["helper"].Operations}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("production accepted ffmpeg")
	}
	for _, adjust := range []func(*Config){func(c *Config) { c.MaxConcurrent = 0 }, func(c *Config) { c.MaxConcurrent = 9 }, func(c *Config) { c.Timeout = 0 }, func(c *Config) { c.MaxStdoutBytes = 17 << 20 }, func(c *Config) { c.MaxStderrBytes = 2 << 20 }, func(c *Config) { c.TempRoot = "relative" }} {
		cfg := r.config
		adjust(&cfg)
		if _, err := newRunner(cfg, []Tool{r.tools["helper"]}, true); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid configuration accepted")
		}
	}
	if _, err := r.Run(nil, Request{Tool: "helper", Operation: "echo"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil context")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Run(ctx, Request{Tool: "helper", Operation: "echo"}); !errors.Is(err, ErrCancelled) {
		t.Fatal("cancelled context")
	}
	read, write, _ := os.Pipe()
	defer read.Close()
	defer write.Close()
	if _, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "echo", Stdin: read}); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted nonseekable pipe")
	}
	writable, err := os.CreateTemp(t.TempDir(), "writable-")
	if err != nil {
		t.Fatal(err)
	}
	defer writable.Close()
	if _, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "echo", Stdin: writable}); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted writable stdin handle")
	}
	assertClean(t, r)
}

func TestOrdinaryGoOpenedFileIsNotInherited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(path, []byte("PRIVATE_FD_CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	r := testRunner(t, 5*time.Second, map[string][]string{"extra": helperArgs("extra-file", strconv.FormatUint(uint64(file.Fd()), 10))})
	result, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "extra"})
	if err != nil || strings.TrimSpace(string(result.Stdout)) != "false" {
		t.Fatalf("extra descriptor inherited: err=%v", err)
	}
	assertClean(t, r)
}

func TestMissingExecutableHasSafeErrorAndReleasesResources(t *testing.T) {
	name := "ffprobe"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("registration only"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{MaxConcurrent: 1, Timeout: time.Second, MaxStdoutBytes: 100, MaxStderrBytes: 100, TempRoot: t.TempDir()}, []Tool{{ID: "ffprobe", Path: path, Operations: map[string][]string{"version": {"-version"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	result, err := r.Run(context.Background(), Request{Tool: "ffprobe", Operation: "version"})
	if !errors.Is(err, ErrStart) || strings.Contains(err.Error(), path) || len(result.Stdout) != 0 {
		t.Fatalf("startup failure: %v", err)
	}
	assertClean(t, r)
}

func TestRegistrationBoundsAndDuplicateIdentity(t *testing.T) {
	executable, _ := os.Executable()
	config := Config{MaxConcurrent: 1, Timeout: time.Second, MaxStdoutBytes: 100, MaxStderrBytes: 100, TempRoot: t.TempDir()}
	base := Tool{ID: "helper", Path: executable, Operations: map[string][]string{"echo": {}}}
	for _, tool := range []Tool{
		{ID: "Bad Name", Path: executable, Operations: base.Operations},
		{ID: "helper", Path: "relative", Operations: base.Operations},
		{ID: "helper", Path: executable, Operations: map[string][]string{}},
		{ID: "helper", Path: executable, Operations: map[string][]string{"bad operation": {}}},
		{ID: "helper", Path: executable, Operations: map[string][]string{"echo": {strings.Repeat("a", 4097)}}},
		{ID: "helper", Path: executable, Operations: map[string][]string{"echo": {"has\x00nul"}}},
		{ID: "helper", Path: executable, Operations: map[string][]string{"echo": make([]string, 65)}},
	} {
		if _, err := newRunner(config, []Tool{tool}, true); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid registry accepted")
		}
	}
	if _, err := newRunner(config, []Tool{base, base}, true); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate tool accepted")
	}
}
