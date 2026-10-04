package ocrruntime

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/MoYuanCN/Jelee/tools"
)

func TestRegistrationComesOnlyFromTheEmbeddedManifest(t *testing.T) {
	profile, policy, err := registration()
	if err != nil || profile.Mode != sandbox.ToolOCR || profile.Path != "/usr/lib/jelee/tesseract/tesseract" || !policy.RequireProtectedFiles {
		t.Fatalf("%v %+v", err, profile)
	}
	if len(policy.Libraries) != 57 || len(policy.Libraries) > sandbox.MaxOCRLibraries || len(policy.DataFiles) != len(tools.OCRLanguages) {
		t.Fatalf("policy sizes %d %d", len(policy.Libraries), len(policy.DataFiles))
	}
	for _, file := range append(policy.Libraries, policy.DataFiles...) {
		if !strings.HasPrefix(file.Path, "/") || len(file.SHA256) != 64 {
			t.Fatalf("file %+v", file)
		}
	}
	for index, file := range policy.DataFiles {
		if file.Path != "/usr/lib/jelee/tesseract/tessdata/"+tools.OCRLanguages[index]+sandbox.OCRDataSuffix {
			t.Fatalf("data file %s", file.Path)
		}
	}
	_, _, err = Registration()
	if (runtime.GOOS == "linux" && runtime.GOARCH == "amd64") != (err == nil) {
		t.Fatal("registration platform gate")
	}
}

func TestIdentityBindsRecognizerAndLanguages(t *testing.T) {
	english, err := Identity([]string{"eng"})
	if err != nil || len(english) != 64 {
		t.Fatalf("%v %q", err, english)
	}
	again, _ := Identity([]string{"eng"})
	both, _ := Identity([]string{"eng", "chi_tra"})
	swapped, _ := Identity([]string{"chi_tra", "eng"})
	if again != english || both == english || swapped == both {
		t.Fatal("identity is not stable or not bound to the language list")
	}
	for _, languages := range [][]string{nil, {"fra"}} {
		if _, err := Identity(languages); err != ErrUnavailable {
			t.Fatalf("%q accepted", languages)
		}
	}
}

func TestMissingShippedFilesDisableOCR(t *testing.T) {
	// The development host has no /usr/lib/jelee: the recognizer and every
	// language report missing (or unsupported off linux-amd64).
	statuses := Diagnose(context.Background())
	if len(statuses) == 0 || statuses[0].Name != "tesseract" || statuses[0].Version != "5.5.0" {
		t.Fatalf("%+v", statuses)
	}
	for _, status := range statuses {
		if status.State != "missing" && status.State != "platform_unsupported" {
			t.Fatalf("%+v", status)
		}
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" && len(statuses) != 1+len(tools.OCRLanguages) {
		t.Fatalf("languages not reported: %+v", statuses)
	}
	for _, concurrency := range []int{0, MaxConcurrency + 1} {
		if _, err := New(context.Background(), concurrency, t.TempDir()); err != ErrUnavailable {
			t.Fatalf("concurrency %d accepted", concurrency)
		}
	}
	if _, err := New(context.Background(), 1, t.TempDir()); err != ErrUnavailable {
		t.Fatal("registered without shipped files")
	}
	if ToolHelper([]string{"not-a-descriptor"}) == 0 {
		t.Fatal("helper accepted a malformed descriptor")
	}
	if c := config(2, "/tmp"); c.MaxConcurrent != 2 || c.MaxStdoutBytes != 64<<10 {
		t.Fatal("limits")
	}
	if _, err := fileDigest("/nonexistent/eng.traineddata"); err == nil {
		t.Fatal("missing file hashed")
	}
}
