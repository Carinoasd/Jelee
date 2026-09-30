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
		command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", path, "../sandbox/testdata/helper")
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
