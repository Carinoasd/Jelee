package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

func TestIsolatedFactoryAcceptsOnlyTheSealedLauncherRegistration(t *testing.T) {
	path := os.Getenv("JELEE_SANDBOX_TEST_HELPER")
	if path == "" {
		path = filepath.Join(t.TempDir(), "ffprobe")
		command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", path, "../sandbox/testdata/helper") //nolint:staticcheck // SA1019: test helpers build with the toolchain running the test; project wrappers export GOROOT
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build sealed launcher fixture: %v; %s", err, output)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := sandbox.New(context.Background(), sandbox.Profile{FFprobePath: path}, sandbox.Policy{FFprobeSHA256: hex.EncodeToString(hash.Sum(nil))})
	if err != nil {
		t.Fatal(err)
	}
	args := launcher.HelperArguments()
	args[0], args[1] = "untrusted command", "untrusted descriptor"
	config := Config{MaxConcurrent: 1, Timeout: time.Second, MaxStdoutBytes: 1024, MaxStderrBytes: 1024, TempRoot: t.TempDir()}
	runner, err := NewIsolatedFFprobe(config, launcher)
	if err != nil {
		t.Fatal(err)
	}
	tool := runner.runner.tools["ffprobe"]
	if len(runner.runner.tools) != 1 || len(tool.Operations) != 1 || tool.Path != launcher.Executable() || tool.Operations["metadata"][0] != sandbox.HelperCommand || tool.Operations["metadata"][1] != launcher.HelperArguments()[1] {
		t.Fatal("factory registry differed from the sealed launcher")
	}
	config.MaxConcurrent = 0
	if _, err := NewIsolatedFFprobe(config, launcher); err != ErrInvalid {
		t.Fatal("sealed factory bypassed configuration bounds")
	}
}

func TestIsolatedCoverFactoryRegistersOnlySealedCoverReads(t *testing.T) {
	path := os.Getenv("JELEE_SANDBOX_TEST_HELPER")
	if path == "" {
		path = filepath.Join(t.TempDir(), "ffprobe")
		command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", path, "../sandbox/testdata/helper") //nolint:staticcheck // SA1019: test helpers build with the toolchain running the test; project wrappers export GOROOT
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build sealed launcher fixture: %v; %s", err, output)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := sandbox.New(context.Background(), sandbox.Profile{FFprobePath: path}, sandbox.Policy{FFprobeSHA256: hex.EncodeToString(hash.Sum(nil))})
	if err != nil {
		t.Fatal(err)
	}
	config := Config{MaxConcurrent: 1, Timeout: time.Second, MaxStdoutBytes: 1024, MaxStderrBytes: 1024, TempRoot: t.TempDir()}
	runner, err := NewIsolatedFFprobeCover(config, launcher)
	if err != nil {
		t.Fatal(err)
	}
	tool := runner.runner.tools["ffprobe"]
	if len(runner.runner.tools) != 1 || len(tool.Operations) != sandbox.CoverMaxVideoIndex+1 || tool.Path != launcher.Executable() {
		t.Fatal("cover registry differs from the sealed launcher")
	}
	for stream := 0; stream <= sandbox.CoverMaxVideoIndex; stream++ {
		args := tool.Operations[CoverOperation(stream)]
		if len(args) != 2 || args[1] != launcher.CoverHelperArguments(stream)[1] {
			t.Fatal("cover operation not sealed", stream)
		}
	}
	if _, ok := tool.Operations["metadata"]; ok {
		t.Fatal("cover runner can run the metadata probe")
	}
	stdin, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	for _, operation := range []string{"metadata", "cover-16", ""} {
		if _, err := runner.Run(context.Background(), Request{Tool: "ffprobe", Operation: operation, Stdin: stdin}); err != ErrInvalid {
			t.Fatal("unregistered operation accepted", operation)
		}
	}
	metadata, err := NewIsolatedFFprobe(config, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.Run(context.Background(), Request{Tool: "ffprobe", Operation: CoverOperation(0), Stdin: stdin}); err != ErrInvalid {
		t.Fatal("metadata runner can run a cover read")
	}
	if _, err := NewIsolatedFFprobeCover(config, nil); err != ErrInvalid {
		t.Fatal("nil launcher accepted")
	}
	config.MaxConcurrent = 0
	if _, err := NewIsolatedFFprobeCover(config, launcher); err != ErrInvalid {
		t.Fatal("cover factory bypassed configuration bounds")
	}
	if CoverOperation(-1) != "" || CoverOperation(sandbox.CoverMaxVideoIndex+1) != "" || CoverOperation(3) != "cover-3" {
		t.Fatal("cover operation names")
	}
}

func TestIsolatedToolFactoryAcceptsOnlyTheSealedLauncher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mkvextract")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", path, "../sandbox/testdata/helper") //nolint:staticcheck // SA1019: test helpers build with the toolchain running the test; project wrappers export GOROOT
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build sealed launcher fixture: %v; %s", err, output)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := sandbox.NewTool(context.Background(), sandbox.ToolProfile{Mode: sandbox.ToolExtract, Path: path}, sandbox.ToolPolicy{ExecutableSHA256: hex.EncodeToString(hash.Sum(nil))})
	if err != nil {
		t.Fatal(err)
	}
	config := Config{MaxConcurrent: 1, Timeout: time.Second, MaxStdoutBytes: 1024, MaxStderrBytes: 1024, TempRoot: t.TempDir()}
	runner, err := NewIsolatedTool(config, launcher)
	if err != nil {
		t.Fatal(err)
	}
	tool := runner.runner.tools["mkvextract"]
	if len(runner.runner.tools) != 1 || len(tool.Operations) != 1 || tool.Path != launcher.Executable() || tool.Operations["run"][0] != sandbox.ToolHelperCommand {
		t.Fatal("factory registry differed from the sealed launcher")
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	collect := func(string) error { return nil }
	for name, request := range map[string]ToolRequest{
		"no stdin":         {Extraction: sandbox.Extraction{Tracks: []int{1}}, Collect: collect},
		"no collect":       {Stdin: input, Extraction: sandbox.Extraction{Tracks: []int{1}}},
		"empty extraction": {Stdin: input, Collect: collect},
		"unsorted":         {Stdin: input, Extraction: sandbox.Extraction{Tracks: []int{2, 1}}, Collect: collect},
		"track bound":      {Stdin: input, Extraction: sandbox.Extraction{Tracks: []int{sandbox.MaxExtractTrackID + 1}}, Collect: collect},
	} {
		if _, err := runner.Run(context.Background(), request); err != ErrInvalid {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	if runner.Stats().Started != 0 {
		t.Fatal("a refused request started a process")
	}
	config.MaxConcurrent = 0
	if _, err := NewIsolatedTool(config, launcher); err != ErrInvalid {
		t.Fatal("sealed factory bypassed configuration bounds")
	}
}
