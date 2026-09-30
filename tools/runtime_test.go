package tools

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

func TestRuntimeEmbeddedIdentityAndFreshValues(t *testing.T) {
	spec, err := RuntimeSpec("linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Platform != "linux-amd64" || !fs.ValidPath(spec.InstallPath) || len(spec.Libraries) != 8 || len(spec.Licenses) != 6 {
		t.Fatal("runtime identity incomplete")
	}
	found := false
	for _, file := range append(spec.Libraries, spec.Licenses...) {
		if !strings.HasPrefix(file.Path, spec.InstallPath+"/") || !fs.ValidPath(file.Path) || !strings.HasPrefix(file.ContainerPath, "/") || len(file.SHA256) != 64 {
			t.Fatal("runtime file identity invalid")
		}
		if file.ContainerPath == "/lib64/ld-linux-x86-64.so.2" {
			found = file.SHA256 == "c8438e4fde1934e61c88311633f00949ff645d5c04cdb8671fa3d78164d2f307"
		}
	}
	if !found {
		t.Fatal("loader differs from archive-verified pin")
	}
	spec.Libraries[0].SHA256 = "mutated"
	spec.Licenses[0].Path = "mutated"
	again, err := RuntimeSpec("linux-amd64")
	if err != nil || again.Libraries[0].SHA256 == "mutated" || again.Licenses[0].Path == "mutated" {
		t.Fatal("caller changed embedded trust identity")
	}
	for _, platform := range []string{"", "linux-arm64", "windows-amd64", "../../linux-amd64"} {
		if _, err := RuntimeSpec(platform); err == nil {
			t.Fatal("unsupported runtime platform accepted")
		}
	}
}

func TestRuntimeRejectsIncompleteOrUnsafeEmbeddedIdentity(t *testing.T) {
	// These subtests deliberately replace the embedded trust document. Keep
	// them sequential: no tools package test may read it in a parallel subtest.
	original := embeddedManifest
	defer func() { embeddedManifest = original }()
	assertRejected := func(t *testing.T) {
		t.Helper()
		spec, err := RuntimeSpec("linux-amd64")
		if err == nil || err.Error() != "tool_runtime_manifest_invalid" || !reflect.DeepEqual(spec, RuntimeSpecification{}) {
			t.Fatal("invalid runtime identity returned usable paths or a non-fixed error")
		}
	}
	for name, input := range map[string]string{
		"truncated":  `{"mediaRuntime":`,
		"null":       `null`,
		"missing":    `{}`,
		"array":      `[]`,
		"wrong-type": `{"mediaRuntime":"private manifest content"}`,
	} {
		t.Run(name, func(t *testing.T) {
			embeddedManifest = []byte(input)
			defer func() { embeddedManifest = original }()
			assertRejected(t)
		})
	}
	fileOfKind := func(runtime map[string]any, kind string) map[string]any {
		for _, item := range runtime["packages"].([]any) {
			for _, entry := range item.(map[string]any)["files"].([]any) {
				file := entry.(map[string]any)
				if file["kind"] == kind {
					return file
				}
			}
		}
		t.Fatal("valid fixture lacks required file kind")
		return nil
	}
	firstLicense := func(runtime map[string]any) map[string]any {
		return runtime["licenseTexts"].([]any)[0].(map[string]any)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"not-ready", func(r map[string]any) { r["readyToExecute"] = false }},
		{"missing-ready", func(r map[string]any) { delete(r, "readyToExecute") }},
		{"ready-wrong-type", func(r map[string]any) { r["readyToExecute"] = "true" }},
		{"wrong-schema", func(r map[string]any) { r["schemaVersion"] = 2 }},
		{"wrong-platform", func(r map[string]any) { r["platform"] = "windows-amd64" }},
		{"not-optional", func(r map[string]any) { r["optional"] = false }},
		{"not-experimental", func(r map[string]any) { r["experimentalOnly"] = false }},
		{"host-install-path", func(r map[string]any) { r["installPath"] = "usr/lib" }},
		{"install-traversal", func(r map[string]any) { r["installPath"] = "media-runtime/linux-amd64/../host" }},
		{"absolute-install", func(r map[string]any) { r["installPath"] = "/media-runtime/linux-amd64/runtime" }},
		{"install-drive", func(r map[string]any) { r["installPath"] = "media-runtime/linux-amd64/C:runtime" }},
		{"library-traversal", func(r map[string]any) { fileOfKind(r, "elf")["destination"] = "../libc.so.6" }},
		{"library-backslash", func(r map[string]any) { fileOfKind(r, "elf")["destination"] = `lib64\ld-linux-x86-64.so.2` }},
		{"unrelated-dso", func(r map[string]any) { fileOfKind(r, "elf")["destination"] = "lib/x86_64-linux-gnu/libssl.so.3" }},
		{"unsupported-file-kind", func(r map[string]any) { fileOfKind(r, "elf")["kind"] = "symlink" }},
		{"notice-outside-license-root", func(r map[string]any) { fileOfKind(r, "notice")["destination"] = "lib/x86_64-linux-gnu/notice" }},
		{"notice-hash-short", func(r map[string]any) { fileOfKind(r, "notice")["sha256"] = "abc" }},
		{"license-absolute", func(r map[string]any) { firstLicense(r)["destination"] = "/licenses/runtime/GPL.txt" }},
		{"license-traversal", func(r map[string]any) { firstLicense(r)["destination"] = "licenses/runtime/../GPL.txt" }},
		{"license-nul", func(r map[string]any) { firstLicense(r)["destination"] = "licenses/runtime/GPL\x00.txt" }},
		{"license-double-separator", func(r map[string]any) { firstLicense(r)["destination"] = "licenses/runtime//GPL.txt" }},
		{"license-outside-root", func(r map[string]any) { firstLicense(r)["destination"] = "licenses/other/GPL.txt" }},
		{"license-hash-invalid", func(r map[string]any) { firstLicense(r)["sha256"] = strings.Repeat("z", 64) }},
		{"license-hash-uppercase", func(r map[string]any) {
			firstLicense(r)["sha256"] = strings.ToUpper(firstLicense(r)["sha256"].(string))
		}},
		{"library-hash-missing", func(r map[string]any) { delete(fileOfKind(r, "elf"), "sha256") }},
		{"library-hash-short", func(r map[string]any) { fileOfKind(r, "elf")["sha256"] = strings.Repeat("0", 62) }},
		{"library-hash-long", func(r map[string]any) { fileOfKind(r, "elf")["sha256"] = strings.Repeat("0", 66) }},
		{"duplicate-library-across-packages", func(r map[string]any) {
			r["packages"] = append(r["packages"].([]any), map[string]any{"files": []any{fileOfKind(r, "elf")}})
		}},
		{"duplicate-license-across-sources", func(r map[string]any) {
			firstLicense(r)["destination"] = fileOfKind(r, "notice")["destination"]
		}},
		{"missing-library", func(r map[string]any) {
			p := r["packages"].([]any)[0].(map[string]any)
			p["files"] = p["files"].([]any)[1:]
		}},
		{"missing-package-notice", func(r map[string]any) {
			p := r["packages"].([]any)[2].(map[string]any)
			p["files"] = []any{}
		}},
		{"missing-full-license", func(r map[string]any) { r["licenseTexts"] = r["licenseTexts"].([]any)[1:] }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(original, &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(document["mediaRuntime"].(map[string]any))
			var err error
			embeddedManifest, err = json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { embeddedManifest = original }()
			assertRejected(t)
		})
	}
	if _, err := RuntimeSpec("linux-amd64"); err != nil {
		t.Fatal("mutation tests did not restore embedded identity")
	}
}
