package toolidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/tools"
)

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func identityFixture(t *testing.T) (string, tools.FFprobeSpecification) {
	t.Helper()
	root := t.TempDir()
	name := "ffprobe"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := ".tools/media/test/bin/" + name
	license := ".tools/media/test/LICENSE"
	if err := os.MkdirAll(filepath.Join(root, ".tools/media/test/bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, executable), []byte("binary fixture"), 0500); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, license), []byte("license fixture"), 0400); err != nil {
		t.Fatal(err)
	}
	return root, tools.FFprobeSpecification{Platform: runtime.GOOS + "-" + runtime.GOARCH, VendorVersion: "test-1.0", InstallPath: ".tools/media/test", ExecutablePath: executable, SHA256: digest([]byte("binary fixture")), Licenses: []tools.LicenseFile{{Path: license, SHA256: digest([]byte("license fixture"))}}}
}
func assertNoTemporary(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".testdata"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("temporary snapshot leaked")
	}
}

func TestVerifiedSnapshotAndNoMutableManifestTrust(t *testing.T) {
	root, spec := identityFixture(t)
	private := t.TempDir()
	if err := makePrivate(private); err != nil {
		t.Fatalf("private directory ACL: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools/manifest.json"), []byte(`{"executables":{"ffprobe":"malicious"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	called := 0
	got := diagnose(context.Background(), root, spec, func(ctx context.Context, exe, temp string) ([]byte, error) {
		called++
		if exe == filepath.Join(root, spec.ExecutablePath) || filepath.Dir(exe) != temp || !strings.HasPrefix(temp, filepath.Join(root, ".testdata")+string(os.PathSeparator)) {
			t.Fatal("execution did not use private project snapshot")
		}
		data, err := os.ReadFile(exe)
		if err != nil || string(data) != "binary fixture" {
			t.Fatal("snapshot differs from verified source")
		}
		info, err := os.Stat(temp)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
			t.Fatal("snapshot directory not private")
		}
		return []byte("ffprobe version test-1.0 Copyright example\nsecret raw details"), nil
	})
	if called != 1 || got.State != "verified" || got.Capability != "disabled_sandbox" || got.Reason != "verified" {
		t.Fatalf("diagnostic: %+v", got)
	}
	encoded, _ := json.Marshal(got)
	for _, private := range []string{root, "snapshot", "Copyright", "secret", "binary fixture"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatal("unsafe diagnostic")
		}
	}
	assertNoTemporary(t, root)
	original, err := os.ReadFile(filepath.Join(root, spec.ExecutablePath))
	if err != nil || string(original) != "binary fixture" {
		t.Fatal("installed source changed")
	}
}

func TestIdentityFailuresNeverExecuteUnverifiedBytes(t *testing.T) {
	cases := []struct {
		name, reason string
		change       func(*testing.T, string, *tools.FFprobeSpecification)
	}{
		{"missing_tool", "missing_tool", func(t *testing.T, root string, s *tools.FFprobeSpecification) {
			t.Helper()
			if err := os.Remove(filepath.Join(root, s.ExecutablePath)); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing_license", "missing_license", func(t *testing.T, root string, s *tools.FFprobeSpecification) {
			t.Helper()
			if err := os.Remove(filepath.Join(root, s.Licenses[0].Path)); err != nil {
				t.Fatal(err)
			}
		}},
		{"tampered_binary", "hash_mismatch", func(_ *testing.T, _ string, s *tools.FFprobeSpecification) { s.SHA256 = strings.Repeat("0", 64) }},
		{"tampered_license", "license_hash_mismatch", func(_ *testing.T, _ string, s *tools.FFprobeSpecification) {
			s.Licenses[0].SHA256 = strings.Repeat("0", 64)
		}},
		{"unsafe_relative", "unsafe_path", func(_ *testing.T, _ string, s *tools.FFprobeSpecification) { s.ExecutablePath = "../escape" }},
		{"directory_tool", "unsafe_path", func(_ *testing.T, _ string, s *tools.FFprobeSpecification) {
			s.ExecutablePath = ".tools/media/test/bin"
		}},
		{"empty_tool", "size_limit", func(t *testing.T, root string, s *tools.FFprobeSpecification) {
			t.Helper()
			path := filepath.Join(root, s.ExecutablePath)
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, spec := identityFixture(t)
			tc.change(t, root, &spec)
			got := diagnose(context.Background(), root, spec, func(context.Context, string, string) ([]byte, error) {
				t.Fatal("unverified execution")
				return nil, nil
			})
			if got.Reason != tc.reason {
				t.Fatalf("got %+v", got)
			}
			assertNoTemporary(t, root)
		})
	}
}

func TestSafeVersionAndProcessFailureClassifications(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		output       string
		err          error
	}{
		{"wrong", "version_mismatch", "ffprobe version test-1.01 Copyright private/path", nil},
		{"empty", "version_mismatch", "", nil}, {"wrong_tool", "version_mismatch", "ffmpeg version test-1.0 Copyright", nil},
		{"flood", "output_limit", "", process.ErrOutputLimit}, {"deadline", "timeout", "", process.ErrTimeout}, {"cancel", "cancelled", "", process.ErrCancelled}, {"raw_error", "execution_failed", "", errors.New("/private/path password=secret")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, spec := identityFixture(t)
			got := diagnose(context.Background(), root, spec, func(context.Context, string, string) ([]byte, error) { return []byte(tc.output), tc.err })
			if got.Reason != tc.reason {
				t.Fatalf("got %+v", got)
			}
			assertNoTemporary(t, root)
		})
	}
}

func TestIdentityCancellationAndFilesystemBoundaries(t *testing.T) {
	root, spec := identityFixture(t)
	never := func(context.Context, string, string) ([]byte, error) {
		t.Fatal("unexpected execution")
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := diagnose(ctx, root, spec, never); got.Reason != "cancelled" {
		t.Fatalf("got %+v", got)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if got := diagnose(ctx, root, spec, never); got.Reason != "timeout" {
		t.Fatalf("got %+v", got)
	}
	if got := diagnose(nil, root, spec, never); got.Reason != "invalid_context" {
		t.Fatalf("got %+v", got)
	}
	if got := diagnose(context.Background(), "relative", spec, never); got.Reason != "unsafe_path" {
		t.Fatalf("got %+v", got)
	}
	for _, component := range []string{"project", "ancestor", "leaf", "temporary"} {
		t.Run(component, func(t *testing.T) {
			root, spec := identityFixture(t)
			target := t.TempDir()
			link := ""
			switch component {
			case "project":
				link = filepath.Join(t.TempDir(), "project")
				target = root
				root = link
			case "ancestor":
				link = filepath.Join(root, ".tools/media/test/bin")
				if err := os.Remove(filepath.Join(root, spec.ExecutablePath)); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			case "leaf":
				link = filepath.Join(root, spec.ExecutablePath)
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			case "temporary":
				link = filepath.Join(root, ".testdata")
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skip("host does not permit test symlink")
			}
			if got := diagnose(context.Background(), root, spec, never); got.Reason != "unsafe_path" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

type cancelReader struct{ cancel context.CancelFunc }

func (r cancelReader) Read(p []byte) (int, error) { r.cancel(); copy(p, "source"); return 6, nil }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestHashCopyIsBoundedAndCancellationAware(t *testing.T) {
	var output bytes.Buffer
	got, err := hashCopy(context.Background(), strings.NewReader("1234"), &output, 4)
	if err != nil || got != digest([]byte("1234")) || output.String() != "1234" {
		t.Fatal("inclusive limit failed")
	}
	output.Reset()
	if _, err := hashCopy(context.Background(), strings.NewReader("12345"), &output, 4); err == nil || output.Len() != 0 {
		t.Fatal("overflow copied bytes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := hashCopy(ctx, cancelReader{cancel}, &output, 10); !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatal("cancelled read copied bytes")
	}
	if _, err := hashCopy(context.Background(), strings.NewReader("1234"), shortWriter{}, 4); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write ignored")
	}
}

func TestPinnedInstalledFFprobe(t *testing.T) {
	if os.Getenv("JELEE_REQUIRE_MEDIA_TOOL_TESTS") != "true" {
		t.Skip("set JELEE_REQUIRE_MEDIA_TOOL_TESTS=true to require the real pinned optional tool")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	if override := os.Getenv("JELEE_MEDIA_TOOL_TEST_PROJECT"); override != "" {
		root = override
	}
	got := Diagnose(context.Background(), root)
	if got.State != "verified" || got.Capability != "disabled_sandbox" {
		t.Fatalf("pinned diagnostic failed: %+v", got)
	}
	t.Logf("verified tool=%s platform=%s version=%s capability=%s", got.Tool, got.Platform, got.ExpectedVersion, got.Capability)
}
