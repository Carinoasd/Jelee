package process

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

func directFixture(t *testing.T, confinement sandbox.Confinement, operations map[string][]string) (*IsolatedRunner, error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return newDirectFFprobe(Config{MaxConcurrent: 1, Timeout: 5 * time.Second, MaxStdoutBytes: 1024, MaxStderrBytes: 1024, TempRoot: t.TempDir()}, executable, confinement, operations)
}

func TestDirectRegistrationRequiresCompleteConfinement(t *testing.T) {
	arguments := map[string][]string{"metadata": {"-test.run=^TestIsolatedProcessHelper$", "--", "--isolated-process-test", "0"}}
	weak := sandbox.MetadataConfinement()
	weak.ActiveProcesses = 0
	for name, input := range map[string]struct {
		confinement sandbox.Confinement
		operations  map[string][]string
	}{
		"zero":       {sandbox.Confinement{}, arguments},
		"weakened":   {weak, arguments},
		"no ops":     {sandbox.MetadataConfinement(), nil},
		"empty argv": {sandbox.MetadataConfinement(), map[string][]string{"metadata": nil}},
	} {
		if runner, err := directFixture(t, input.confinement, input.operations); err != ErrInvalid || runner != nil {
			t.Fatalf("%s: registered %v", name, err)
		}
	}
	runner, err := directFixture(t, sandbox.MetadataConfinement(), arguments)
	if err != nil || runner.runner.confinement == nil || !runner.runner.confinement.Valid() || !runner.direct {
		t.Fatalf("direct runner not confined: %v", err)
	}
	// The registered confinement is a copy: later changes cannot weaken it.
	runner.runner.confinement.DenyOnlyGroups[0] = "changed"
	if sandbox.MetadataConfinement().DenyOnlyGroups[0] == "changed" {
		t.Fatal("registration shared confinement state")
	}
}

// A direct runner never falls back to an unconfined start: off Windows the
// confined start is unsupported and no child is created at all.
func TestDirectRunnerNeverStartsUnconfinedOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows runs the confined child; see child_confined_windows_test.go")
	}
	runner, err := directFixture(t, sandbox.MetadataConfinement(), map[string][]string{"metadata": {"-test.run=^TestIsolatedProcessHelper$", "--", "--isolated-process-test", "0"}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	result, err := runner.Run(context.Background(), Request{Tool: "ffprobe", Operation: "metadata", Stdin: input})
	if err != ErrUnsupported || result.Stdout != nil || runner.Stats().Started != 0 {
		t.Fatalf("unconfined start: %v %+v", err, runner.Stats())
	}
	assertClean(t, runner.runner)
	if _, err := runner.Run(context.Background(), Request{Tool: "ffprobe", Operation: "cover-0", Stdin: input}); err != ErrInvalid {
		t.Fatal("unregistered operation accepted")
	}
}

func TestDirectExitClassification(t *testing.T) {
	if directExitError(1) != ErrExit {
		t.Fatal("ffprobe rejection misclassified")
	}
	// Without a helper, 64 and 78 are the tool's own exits, never sandbox verdicts.
	for _, code := range []int{2, 64, 78, 127, -1, 0x705} {
		if directExitError(code) != ErrUnexpectedExit {
			t.Fatalf("code %d misclassified", code)
		}
	}
}

func TestIsolatedFactoriesRefuseHelperLaunchersWithoutDescriptor(t *testing.T) {
	// A zero launcher is neither a helper (no descriptor) nor direct.
	empty := &sandbox.Launcher{}
	if runner, err := NewIsolatedFFprobe(Config{}, empty); err != ErrInvalid || runner != nil {
		t.Fatal("empty launcher registered")
	}
	if runner, err := NewIsolatedFFprobeCover(Config{}, empty); err != ErrInvalid || runner != nil {
		t.Fatal("empty cover launcher registered")
	}
}
