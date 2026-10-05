package tools

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestOCRSpecPinsExactLinuxClosureAndLanguages(t *testing.T) {
	spec, err := OCRToolSpec("linux-amd64")
	if err != nil || spec.Version != "5.5.0" || spec.Executable.ContainerPath != "/usr/lib/jelee/tesseract/tesseract" || !strings.HasPrefix(spec.Executable.Path, ".tools/ocr/tesseract/") {
		t.Fatalf("%v %+v", err, spec.Executable)
	}
	if len(spec.Libraries) != 57 {
		t.Fatalf("closure has %d libraries", len(spec.Libraries))
	}
	seen := map[string]bool{}
	for _, library := range spec.Libraries {
		if library.ContainerPath == "" || seen[library.ContainerPath] || !digestValid(library.SHA256) || !strings.HasPrefix(library.Path, ".tools/") {
			t.Fatalf("library %+v", library)
		}
		seen[library.ContainerPath] = true
	}
	for _, required := range []string{"/lib64/ld-linux-x86-64.so.2", "/lib/x86_64-linux-gnu/libc.so.6", "/lib/x86_64-linux-gnu/libresolv.so.2", "/usr/lib/jelee/tesseract/lib/libtesseract.so.5", "/usr/lib/jelee/tesseract/lib/libleptonica.so.6"} {
		if !seen[required] {
			t.Fatalf("closure misses %s", required)
		}
	}
	if len(spec.Languages) != len(OCRLanguages) || spec.TessdataContainerPath != "/usr/lib/jelee/tesseract/tessdata" {
		t.Fatalf("languages %+v", spec.Languages)
	}
	for _, code := range OCRLanguages {
		file := spec.Languages[code]
		if file.ContainerPath != spec.TessdataContainerPath+"/"+code+".traineddata" || file.Path != spec.TessdataPath+"/"+code+".traineddata" || !digestValid(file.SHA256) {
			t.Fatalf("%s %+v", code, file)
		}
	}
	if len(spec.Licenses) < 40 {
		t.Fatalf("only %d notices", len(spec.Licenses))
	}
	for _, platform := range []string{"windows-amd64", "linux-arm64"} {
		if _, err := OCRToolSpec(platform); err == nil || err.Error() != "tool_platform_unsupported" {
			t.Fatalf("%s accepted", platform)
		}
	}
}

func TestOCRImageFilesExcludeSharedRuntime(t *testing.T) {
	files, err := OCRImageFiles()
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := OCRToolSpec("linux-amd64")
	// Executable, 52 own libraries plus libresolv, four models, notices.
	if len(files) != 1+53+len(OCRLanguages)+len(spec.Licenses) {
		t.Fatalf("image files %d", len(files))
	}
	for _, file := range files {
		if strings.HasPrefix(file.ContainerPath, "/lib64/") || file.ContainerPath == "/lib/x86_64-linux-gnu/libc.so.6" || strings.Contains(file.ContainerPath, "ffmpeg") {
			t.Fatalf("unexpected image file %s", file.ContainerPath)
		}
	}
}

func TestOCRManifestSourcesAreDebianHTTPS(t *testing.T) {
	var manifest struct {
		OCRTools struct {
			Optional bool
			Decision string
			Packages []struct {
				Name, URL, SHA256, License, Version string
				SizeBytes                           int64
			}
			Tools map[string]struct{ License, SourceURL string }
		} `json:"ocrTools"`
	}
	if err := json.Unmarshal(embeddedManifest, &manifest); err != nil || !manifest.OCRTools.Optional || !strings.Contains(manifest.OCRTools.Decision, "never installed by default") {
		t.Fatal("OCR tools must stay optional")
	}
	for _, pkg := range manifest.OCRTools.Packages {
		u, err := url.Parse(pkg.URL)
		if err != nil || u.Scheme != "https" || u.Host != "deb.debian.org" || u.RawQuery != "" || !strings.HasPrefix(u.Path, "/debian/pool/main/") {
			t.Fatalf("%s source %q", pkg.Name, pkg.URL)
		}
		if !digestValid(pkg.SHA256) || pkg.SizeBytes < 1 || pkg.License == "" || pkg.Version == "" {
			t.Fatalf("%s identity or license", pkg.Name)
		}
	}
	tool := manifest.OCRTools.Tools["tesseract"]
	if !strings.HasPrefix(tool.License, "Apache-2.0") || !strings.HasPrefix(tool.SourceURL, "https://deb.debian.org/") {
		t.Fatal("tesseract license or source record")
	}
}
