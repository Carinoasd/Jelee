package tools

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestMatroskaSpecPinsExactLinuxClosures(t *testing.T) {
	for name, count := range map[string]int{"mkvmerge": 32, "mkvextract": 32, "mediainfo": 8} {
		spec, err := MatroskaToolSpec("linux-amd64", name)
		if err != nil || len(spec.Libraries) != count || !strings.HasPrefix(spec.Executable.ContainerPath, "/usr/lib/jelee/") || !strings.HasPrefix(spec.Executable.Path, ".tools/matroska/") {
			t.Fatalf("%s: %v %d %+v", name, err, len(spec.Libraries), spec.Executable)
		}
		seen := map[string]bool{}
		for _, library := range spec.Libraries {
			if library.ContainerPath == "" || seen[library.ContainerPath] || !digestValid(library.SHA256) || !strings.HasPrefix(library.Path, ".tools/") {
				t.Fatalf("%s library %+v", name, library)
			}
			seen[library.ContainerPath] = true
		}
		if !seen["/lib64/ld-linux-x86-64.so.2"] || !seen["/lib/x86_64-linux-gnu/libstdc++.so.6"] {
			t.Fatalf("%s closure misses the loader or libstdc++", name)
		}
		if len(spec.Licenses) == 0 {
			t.Fatalf("%s has no license notice", name)
		}
	}
	for _, platform := range []string{"linux-amd64", "windows-amd64"} {
		if _, err := MatroskaToolSpec(platform, "mkvpropedit"); err == nil {
			t.Fatal("mkvpropedit is production-allowed")
		}
	}
	if _, err := MatroskaToolSpec("linux-arm64", "mkvmerge"); err == nil || err.Error() != "tool_platform_unsupported" {
		t.Fatal("unpinned platform accepted")
	}
	windows, err := MatroskaToolSpec("windows-amd64", "mediainfo")
	if err != nil || windows.Executable.ContainerPath != "" || !strings.HasSuffix(windows.Executable.Path, ".exe") || len(windows.Libraries) != 1 {
		t.Fatalf("windows mediainfo %v %+v", err, windows)
	}
}

func TestMatroskaImageFilesExcludeEditorsAndSharedRuntime(t *testing.T) {
	files, err := MatroskaImageFiles()
	if err != nil || len(files) != 33 {
		t.Fatalf("image files %v %d", err, len(files))
	}
	for _, file := range files {
		if strings.Contains(file.ContainerPath, "mkvpropedit") || strings.Contains(file.ContainerPath, "ffmpeg") || strings.HasPrefix(file.ContainerPath, "/lib64/") || file.ContainerPath == "/lib/x86_64-linux-gnu/libc.so.6" {
			t.Fatalf("unexpected image file %s", file.ContainerPath)
		}
	}
}

func TestMatroskaManifestSourcesAreOfficialHTTPS(t *testing.T) {
	var manifest struct {
		MatroskaTools struct {
			Optional        bool
			RuntimePackages []struct{ URL, SHA256 string } `json:"runtimePackages"`
			Tools           map[string]struct {
				Version   string
				License   string
				Platforms map[string]struct {
					URL          string                 `json:"url"`
					SHA256       string                 `json:"sha256"`
					SizeBytes    int64                  `json:"sizeBytes"`
					LicenseTexts []struct{ URL string } `json:"licenseTexts"`
				}
			}
		} `json:"matroskaTools"`
	}
	if err := json.Unmarshal(embeddedManifest, &manifest); err != nil || !manifest.MatroskaTools.Optional {
		t.Fatal("matroska tools must stay optional")
	}
	hosts := map[string]bool{"mkvtoolnix.download": true, "mediaarea.net": true, "codeberg.org": true, "deb.debian.org": true}
	check := func(raw string) {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || !hosts[u.Host] || u.RawQuery != "" {
			t.Fatalf("source %q is not an official HTTPS URL", raw)
		}
	}
	for _, pkg := range manifest.MatroskaTools.RuntimePackages {
		check(pkg.URL)
		if !digestValid(pkg.SHA256) {
			t.Fatal("package digest")
		}
	}
	for name, tool := range manifest.MatroskaTools.Tools {
		if tool.Version == "" || tool.License == "" || len(tool.Platforms) != 2 {
			t.Fatalf("%s lacks version, license or platforms", name)
		}
		for _, platform := range tool.Platforms {
			check(platform.URL)
			if !digestValid(platform.SHA256) || platform.SizeBytes < 1 {
				t.Fatalf("%s archive identity", name)
			}
			for _, text := range platform.LicenseTexts {
				check(text.URL)
			}
		}
	}
	if !strings.HasPrefix(manifest.MatroskaTools.Tools["mkvtoolnix"].License, "GPL-2.0") || !strings.HasPrefix(manifest.MatroskaTools.Tools["mediainfo"].License, "BSD-2-Clause") {
		t.Fatal("license records")
	}
}
