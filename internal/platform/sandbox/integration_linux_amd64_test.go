package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNativeSandboxDeniesAmbientAccessAndPreservesFDInput(t *testing.T) {
	requireNative(t)
	helper := fixtureHelper(t)
	requireNativeThreadBudget(t)
	hash := fileDigest(t, helper)
	policy := Policy{FFprobeSHA256: hash}
	launcher, err := New(context.Background(), Profile{FFprobePath: helper}, policy)
	if err != nil {
		t.Fatalf("verified fixture preparation: %v", err)
	}
	root := t.TempDir()
	private := filepath.Join(root, "private.txt")
	if err := os.WriteFile(private, []byte("PRIVATE_EXTRA_FD synthetic secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(private); err != nil {
		t.Fatal("test identity cannot read the unsandboxed control")
	}
	writePath := filepath.Join(root, "forbidden-output")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// This disposable same-UID sibling is the only signal-attack target.
	sibling := exec.CommandContext(ctx, helper, "--signal-control")
	siblingInput, err := sibling.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	siblingOutput, err := sibling.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		siblingInput.Close()
		_ = sibling.Process.Kill()
		_ = sibling.Wait()
	}()
	siblingReader := bufio.NewReader(siblingOutput)
	if ready, err := siblingReader.ReadString('\n'); err != nil || ready != "ready\n" {
		t.Fatal("signal control did not initialize")
	}
	data, _ := json.Marshal(map[string]any{"OutsideFile": private, "WriteFile": writePath, "Directory": root, "SiblingPID": sibling.Process.Pid})
	source := filepath.Join(root, "source.json")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	extra, err := os.Open(private)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	encodedPolicy, _ := json.Marshal(policy)
	command := exec.CommandContext(ctx, helper, "--test-helper", launcher.HelperArguments()[1])
	command.Env = []string{"JELEE_TEST_SANDBOX_POLICY=" + string(encodedPolicy), "PRIVATE_TEST_SECRET=synthetic-untrusted-environment"}
	command.Stdin = input
	command.ExtraFiles = []*os.File{extra}
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox fixture failed: %v; diagnostics=%s", err, output)
	}
	var checks map[string]bool
	if json.Unmarshal(output, &checks) != nil || len(checks) != 25 {
		t.Fatalf("incomplete sandbox checks: %s", output)
	}
	siblingInput.Close()
	var receivedSignals int
	if err := json.NewDecoder(siblingReader).Decode(&receivedSignals); err != nil || receivedSignals != 0 {
		t.Fatal("sandbox signalled the disposable sibling")
	}
	if err := sibling.Wait(); err != nil {
		t.Fatal("signal control failed")
	}
	for name, passed := range checks {
		if !passed {
			t.Errorf("sandbox boundary failed: %s", name)
		}
	}
	if current := fileDigest(t, source); current != hexDigest(data) {
		t.Fatal("preopened source bytes changed")
	}
	if _, err := os.Stat(writePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sandbox created a forbidden file")
	}
	t.Logf("verified %d actual file/network/FD/syscall/environment/resource checks", len(checks))
	t.Run("wrong_digest", func(t *testing.T) {
		if _, err := New(context.Background(), Profile{FFprobePath: helper}, Policy{FFprobeSHA256: strings.Repeat("0", 64)}); err != ErrUnavailable {
			t.Fatal("tool digest mismatch accepted")
		}
	})
	t.Run("extra_dependency", func(t *testing.T) {
		expanded := Policy{FFprobeSHA256: hash, Libraries: []PinnedFile{{Path: helper, SHA256: hash}}}
		if _, err := New(context.Background(), Profile{FFprobePath: helper}, expanded); err != ErrUnavailable {
			t.Fatal("unneeded file accepted as dependency")
		}
	})
	t.Run("cancelled_verification", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := New(ctx, Profile{FFprobePath: helper}, policy); err != context.Canceled {
			t.Fatal("cancelled verification proceeded")
		}
	})
	t.Run("writable_input", func(t *testing.T) {
		writable, err := os.OpenFile(source, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer writable.Close()
		assertHelperUnavailable(t, helper, launcher, encodedPolicy, writable, nil)
		if fileDigest(t, source) != hexDigest(data) {
			t.Fatal("rejected writable input was changed")
		}
	})
	t.Run("pipe_input", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		writer.Close()
		assertHelperUnavailable(t, helper, launcher, encodedPolicy, reader, nil)
	})
	t.Run("regular_output", func(t *testing.T) {
		output, err := os.Create(filepath.Join(root, "output-must-remain-empty"))
		if err != nil {
			t.Fatal(err)
		}
		defer output.Close()
		assertHelperUnavailable(t, helper, launcher, encodedPolicy, input, output)
		info, err := output.Stat()
		if err != nil || info.Size() != 0 {
			t.Fatal("rejected output file was written")
		}
	})
	t.Run("tool_replaced_after_registration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ffprobe")
		copyFixture(t, helper, path)
		registration, err := New(context.Background(), Profile{FFprobePath: path}, policy)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replaced after registration"), 0700); err != nil {
			t.Fatal(err)
		}
		assertHelperUnavailable(t, helper, registration, encodedPolicy, input, nil)
	})
}

func assertHelperUnavailable(t *testing.T, helper string, launcher *Launcher, policy []byte, input *os.File, output *os.File) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, helper, "--test-helper", launcher.HelperArguments()[1])
	command.Env = []string{"JELEE_TEST_SANDBOX_POLICY=" + string(policy)}
	command.Stdin = input
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if output != nil {
		command.Stdout = output
	}
	err := command.Run()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != ExitUnavailable || stdout.Len() != 0 || stderr.String() != "media_sandbox_unavailable\n" {
		t.Fatalf("unsafe helper did not fail closed with fixed diagnostics: exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func copyFixture(t *testing.T, source, target string) {
	t.Helper()
	reader, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(writer, reader); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func requireNative(t *testing.T) {
	t.Helper()
	abi, _, errno := unix.RawSyscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if os.Getuid() == 0 || os.Geteuid() == 0 || errno != 0 || abi < 3 {
		if os.Getenv("JELEE_REQUIRE_SANDBOX_TEST") == "true" {
			t.Fatal("native sandbox tests require non-root Linux amd64 and Landlock ABI >= 3")
		}
		t.Skip("native sandbox unavailable; production must disable the capability")
	}
}

func requireNativeThreadBudget(t *testing.T) {
	t.Helper()
	processes, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal("cannot check native test UID thread budget")
	}
	total := 0
	for _, process := range processes {
		if _, err := strconv.Atoi(process.Name()); err != nil {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", process.Name(), "status"))
		if err != nil {
			continue // A process may exit while its status is being read.
		}
		uid, threads := -1, 0
		for _, line := range strings.Split(string(status), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "Uid:" {
				uid, _ = strconv.Atoi(fields[1])
			}
			if len(fields) == 2 && fields[0] == "Threads:" {
				threads, _ = strconv.Atoi(fields[1])
			}
		}
		if uid == os.Getuid() {
			total += threads
		}
	}
	if total > 96 {
		if os.Getenv("JELEE_REQUIRE_SANDBOX_TEST") == "true" {
			t.Fatal("sandbox fixture needs an isolated UID with thread budget below NPROC=128")
		}
		t.Skip("shared test UID has insufficient thread budget; isolated native container proof required")
	}
}

func fixtureHelper(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("JELEE_SANDBOX_TEST_HELPER"); configured != "" {
		return configured
	}
	path := filepath.Join(t.TempDir(), "ffprobe")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", path, "./testdata/helper") //nolint:staticcheck // SA1019: test helpers build with the toolchain running the test; project wrappers export GOROOT
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOTOOLCHAIN=local")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile project-local attack fixture: %v; %s", err, output)
	}
	return path
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func hexDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
