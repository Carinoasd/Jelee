package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNativeThreadCreationIsBoundedByKernel(t *testing.T) {
	requireNative(t)
	tool := os.Getenv("JELEE_SANDBOX_THREAD_FIXTURE")
	if tool == "" {
		tool = filepath.Join(t.TempDir(), "ffprobe")
		command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", tool, "./testdata/threadlimit") //nolint:staticcheck // SA1019: test helpers build with the toolchain running the test; project wrappers export GOROOT
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOTOOLCHAIN=local")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("compile isolated thread-boundary fixture using pinned Go: %v; %s", err, output)
		}
	}
	policy := Policy{FFprobeSHA256: fileDigest(t, tool)}
	launcher, err := New(context.Background(), Profile{FFprobePath: tool}, policy)
	if err != nil {
		t.Fatal(err)
	}
	helper := fixtureHelper(t)
	requireNativeThreadBudget(t)
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("read-only input"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, helper, "--test-helper", launcher.HelperArguments()[1])
	encoded, _ := json.Marshal(policy)
	command.Env = []string{"JELEE_TEST_SANDBOX_POLICY=" + string(encoded)}
	command.Stdin = file
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("thread resource limit failed: %v; %s", err, output)
	}
	var result struct {
		ThreadsCreated int  `json:"threadsCreated"`
		LimitReached   bool `json:"limitReached"`
	}
	if json.Unmarshal(output, &result) != nil || !result.LimitReached || result.ThreadsCreated < 1 || result.ThreadsCreated >= 128 {
		t.Fatal("kernel thread limit was not exercised")
	}
	t.Logf("kernel returned EAGAIN after %d live fixture threads; all joined", result.ThreadsCreated)
}
