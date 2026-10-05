package ocrruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/MoYuanCN/Jelee/tools"
)

// The real-tool tests run the production chain: process.IsolatedToolRunner
// starts this test binary as --internal-media-tool-helper, which verifies
// and confines the pinned tool exactly as the service helper does. Only the
// registration differs: an explicit developer policy of the project's
// pinned files plus this host's glibc, written by the test two levels above
// the runner's private directory (the child's TMPDIR), because the runner
// passes no other environment. Production registration never reads files.

const hostPolicyName = "host-tool-policy.json"

type hostRegistration struct {
	Profile sandbox.ToolProfile
	Policy  sandbox.ToolPolicy
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.ToolHelperCommand {
		os.Exit(sandbox.RunToolHelper(os.Args[2:], readHostRegistration))
	}
	os.Exit(m.Run())
}

func readHostRegistration(mode sandbox.ToolMode) (sandbox.ToolProfile, sandbox.ToolPolicy, bool) {
	private := os.Getenv("TMPDIR")
	if private == "" {
		return sandbox.ToolProfile{}, sandbox.ToolPolicy{}, false
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(private)), hostPolicyName))
	if err != nil {
		return sandbox.ToolProfile{}, sandbox.ToolPolicy{}, false
	}
	var registrations map[sandbox.ToolMode]hostRegistration
	if json.Unmarshal(data, &registrations) != nil {
		return sandbox.ToolProfile{}, sandbox.ToolPolicy{}, false
	}
	registration, ok := registrations[mode]
	return registration.Profile, registration.Policy, ok
}

// hostToolsRoot is the directory whose .tools holds the bootstrapped tools:
// JELEE_OCR_TOOLS_ROOT, or the repository root.
func hostToolsRoot(t *testing.T) string {
	t.Helper()
	if root := os.Getenv("JELEE_OCR_TOOLS_ROOT"); root != "" {
		return root
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func requireHostRuntime(t *testing.T, variable string) {
	t.Helper()
	if os.Getenv(variable) != "true" {
		t.Skip("real tools need " + variable + "=true; host glibc is never trusted implicitly")
	}
}

func skipMissing(t *testing.T, path, hint string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		if os.Getenv("JELEE_REQUIRE_MEDIA_TOOL_TESTS") == "true" {
			t.Fatalf("%s is not installed (%s)", filepath.Base(path), hint)
		}
		t.Skipf("%s is not installed; run %s", filepath.Base(path), hint)
	}
}

func digestFile(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

var hostGlibc = map[string]bool{"ld-linux-x86-64.so.2": true, "libc.so.6": true, "libm.so.6": true, "libdl.so.2": true, "libpthread.so.0": true, "librt.so.1": true, "libgcc_s.so.1": true, "libmvec.so.1": true, "libresolv.so.2": true}

func hostLibrary(t *testing.T, root string, library tools.RuntimeFile) sandbox.PinnedFile {
	t.Helper()
	base := filepath.Base(library.ContainerPath)
	path := filepath.Join(root, filepath.FromSlash(library.Path))
	if hostGlibc[base] {
		path = "/usr/lib/x86_64-linux-gnu/" + base
		if base == "ld-linux-x86-64.so.2" {
			resolved, err := filepath.EvalSymlinks("/lib64/ld-linux-x86-64.so.2")
			if err != nil {
				t.Fatal(err)
			}
			path = resolved
		}
	}
	return sandbox.PinnedFile{Path: path, SHA256: digestFile(t, path)}
}

// hostOCRRegistration is the developer registration of Tesseract with every
// pinned language.
func hostOCRRegistration(t *testing.T, root string) hostRegistration {
	t.Helper()
	spec, err := tools.OCRToolSpec("linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, filepath.FromSlash(spec.Executable.Path))
	skipMissing(t, executable, "make bootstrap-ocr")
	registration := hostRegistration{Profile: sandbox.ToolProfile{Mode: sandbox.ToolOCR, Path: executable}, Policy: sandbox.ToolPolicy{ExecutableSHA256: spec.Executable.SHA256}}
	for _, library := range spec.Libraries {
		registration.Policy.Libraries = append(registration.Policy.Libraries, hostLibrary(t, root, library))
	}
	for _, code := range tools.OCRLanguages {
		file := spec.Languages[code]
		registration.Policy.DataFiles = append(registration.Policy.DataFiles, sandbox.PinnedFile{Path: filepath.Join(root, filepath.FromSlash(file.Path)), SHA256: file.SHA256})
	}
	return registration
}

// hostMatroskaRegistration is the developer registration of mkvmerge or
// mkvextract.
func hostMatroskaRegistration(t *testing.T, root, name string, mode sandbox.ToolMode) hostRegistration {
	t.Helper()
	spec, err := tools.MatroskaToolSpec("linux-amd64", name)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, filepath.FromSlash(spec.Executable.Path))
	skipMissing(t, executable, "make bootstrap-matroska")
	registration := hostRegistration{Profile: sandbox.ToolProfile{Mode: mode, Path: executable}, Policy: sandbox.ToolPolicy{ExecutableSHA256: spec.Executable.SHA256}}
	for _, library := range spec.Libraries {
		registration.Policy.Libraries = append(registration.Policy.Libraries, hostLibrary(t, root, library))
	}
	return registration
}

// hostRunners writes the registrations and returns one isolated runner per
// mode, sharing a private runner root.
func hostRunners(t *testing.T, concurrency int, registrations ...hostRegistration) map[sandbox.ToolMode]*process.IsolatedToolRunner {
	t.Helper()
	base := filepath.Join(t.TempDir(), "host")
	runs := filepath.Join(base, "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		t.Fatal(err)
	}
	byMode := map[sandbox.ToolMode]hostRegistration{}
	for _, registration := range registrations {
		byMode[registration.Profile.Mode] = registration
	}
	data, err := json.Marshal(byMode)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, hostPolicyName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	runners := map[sandbox.ToolMode]*process.IsolatedToolRunner{}
	for mode, registration := range byMode {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		launcher, err := sandbox.NewTool(ctx, registration.Profile, registration.Policy)
		cancel()
		if err != nil {
			t.Fatalf("%s verification: %v", mode, err)
		}
		limits := config(concurrency, runs)
		if mode != sandbox.ToolOCR {
			limits = process.Config{MaxConcurrent: 1, Timeout: 10 * time.Minute, MaxStdoutBytes: 4 << 20, MaxStderrBytes: 64 << 10, TempRoot: runs}
		}
		runner, err := process.NewIsolatedTool(limits, launcher)
		if err != nil {
			t.Fatal(err)
		}
		runners[mode] = runner
	}
	return runners
}
