package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeLateExecFailureExportsActualChildCoverage(t *testing.T) {
	helper, destination := os.Getenv("JELEE_SANDBOX_COVERAGE_HELPER"), os.Getenv("JELEE_SANDBOX_COVERAGE_DIR")
	if helper == "" || destination == "" {
		if os.Getenv("JELEE_REQUIRE_SANDBOX_TEST") == "true" {
			t.Fatal("required native run must supply instrumented helper and coverage destination")
		}
		t.Skip("child coverage requires the explicit instrumented native test fixture")
	}
	requireNative(t)
	requireNativeThreadBudget(t)
	// Valid ELF identity/header, no executable segments: verification can inspect
	// the approved bytes, but the kernel cannot execute this image. This reaches
	// the real final exec after all irreversible restrictions, without a hook.
	header := make([]byte, 64)
	copy(header, "\x7fELF")
	header[4], header[5], header[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(header[16:], 2)
	binary.LittleEndian.PutUint16(header[18:], 62)
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, header, 0700); err != nil {
		t.Fatal(err)
	}
	policy := Policy{FFprobeSHA256: hexDigest(header)}
	captureLateFailureCoverage(t, helper, destination, path, policy, "1")
	if profilePath := os.Getenv("JELEE_SANDBOX_REAL_PROFILE"); profilePath != "" {
		var fixture struct {
			Profile Profile
			Policy  Policy
		}
		data, err := os.ReadFile(profilePath)
		if err != nil || json.Unmarshal(data, &fixture) != nil {
			t.Fatal("invalid explicit dependency fixture profile")
		}
		// Copy a verified fixture with bounded streaming, then disable PT_LOAD
		// headers in the copy. Interpreter/dependency metadata stays intact;
		// the kernel refuses this copy after actual dependency policy setup.
		dynamicPath := filepath.Join(t.TempDir(), "ffprobe")
		copyFixture(t, fixture.Profile.FFprobePath, dynamicPath)
		file, err := os.OpenFile(dynamicPath, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		var header [64]byte
		if _, err := file.ReadAt(header[:], 0); err != nil {
			file.Close()
			t.Fatal(err)
		}
		offset := int64(binary.LittleEndian.Uint64(header[32:40]))
		entrySize, entries := int64(binary.LittleEndian.Uint16(header[54:56])), int(binary.LittleEndian.Uint16(header[56:58]))
		if entrySize != 56 || entries < 1 || entries > 128 {
			file.Close()
			t.Fatal("unexpected fixture program header layout")
		}
		changed := 0
		for index := 0; index < entries; index++ {
			var kind [4]byte
			position := offset + int64(index)*entrySize
			if _, err := file.ReadAt(kind[:], position); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if binary.LittleEndian.Uint32(kind[:]) == 1 {
				if _, err := file.WriteAt([]byte{0, 0, 0, 0}, position); err != nil {
					file.Close()
					t.Fatal(err)
				}
				changed++
			}
		}
		file.Close()
		if changed == 0 {
			t.Fatal("fixture had no loadable segments")
		}
		fixture.Policy.FFprobeSHA256 = fileDigest(t, dynamicPath)
		fixture.Policy.RequireProtectedFiles = false // Only the deliberately invalid local test copy.
		captureLateFailureCoverage(t, helper, destination, dynamicPath, fixture.Policy, "2")
	}
}

func captureLateFailureCoverage(t *testing.T, helper, destination, path string, policy Policy, id string) {
	t.Helper()
	if _, err := New(context.Background(), Profile{FFprobePath: path}, policy); err != nil {
		t.Fatalf("late-failure fixture rejected before execution: %v", err)
	}
	data, _ := json.Marshal(descriptor{Version: 1, Mode: "metadata", FFprobePath: path})
	encodedPolicy, _ := json.Marshal(policy)
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, helper, "--test-helper-coverage", base64.RawURLEncoding.EncodeToString(data))
	command.Env = []string{"JELEE_TEST_SANDBOX_POLICY=" + string(encodedPolicy)}
	command.Stdin = input
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != ExitUnavailable || !bytes.Contains(stderr.Bytes(), []byte("media_sandbox_unavailable\n")) {
		t.Fatalf("late exec did not fail closed: %v; %s", err, stderr.Bytes())
	}
	var coverage struct{ Meta, Counters []byte }
	if json.Unmarshal(stdout.Bytes(), &coverage) != nil || len(coverage.Meta) < 56 || len(coverage.Counters) < 32 || !bytes.Equal(coverage.Meta[:4], []byte{0, 'c', 'v', 'm'}) {
		t.Fatal("missing actual child coverage snapshot")
	}
	// Go 1.27.1 MetaFileHeader stores the 16-byte hash at offset 24. Preserve
	// the original bytes emitted by runtime/coverage; do not rewrite counters.
	hash := hex.EncodeToString(coverage.Meta[24:40])
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"covmeta." + hash: coverage.Meta, "covcounters." + hash + ".1." + id: coverage.Counters} {
		if err := os.WriteFile(filepath.Join(destination, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("captured original atomic runtime counters after kernel exec refusal; no successful exec is inferred")
}
