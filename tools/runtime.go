package tools

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
)

// RuntimeFile binds a project-local file to its fixed path in the Linux image.
// The container path must be passed directly to the sandbox policy; it must not
// be resolved from PATH or replaced by the host's corresponding shared library.
type RuntimeFile struct {
	Path          string
	ContainerPath string
	SHA256        string
}

type RuntimeSpecification struct {
	Platform    string
	InstallPath string
	Libraries   []RuntimeFile
	Licenses    []RuntimeFile
}

// RuntimeSpec returns a fresh copy of the embedded experimental Linux runtime
// identity. It does not install files, inspect the host, or enable probing.
func RuntimeSpec(platform string) (RuntimeSpecification, error) {
	if platform != "linux-amd64" {
		return RuntimeSpecification{}, errors.New("tool_platform_unsupported")
	}
	var manifest struct {
		MediaRuntime struct {
			SchemaVersion    int
			Platform         string
			Optional         bool
			ExperimentalOnly bool
			ReadyToExecute   bool
			InstallPath      string
			Packages         []struct {
				Files []struct{ Destination, Kind, SHA256 string }
			}
			LicenseTexts []struct{ Destination, SHA256 string }
		}
	}
	invalid := func() (RuntimeSpecification, error) {
		return RuntimeSpecification{}, errors.New("tool_runtime_manifest_invalid")
	}
	if err := json.Unmarshal(embeddedManifest, &manifest); err != nil {
		return invalid()
	}
	runtime := manifest.MediaRuntime
	if runtime.SchemaVersion != 1 || runtime.Platform != platform || !runtime.Optional || !runtime.ExperimentalOnly || !runtime.ReadyToExecute || !runtimePath(runtime.InstallPath) || !strings.HasPrefix(runtime.InstallPath, "media-runtime/linux-amd64/") {
		return invalid()
	}
	allowed := map[string]bool{
		"lib64/ld-linux-x86-64.so.2":           false,
		"lib/x86_64-linux-gnu/libc.so.6":       false,
		"lib/x86_64-linux-gnu/libm.so.6":       false,
		"lib/x86_64-linux-gnu/libmvec.so.1":    false,
		"lib/x86_64-linux-gnu/libdl.so.2":      false,
		"lib/x86_64-linux-gnu/libpthread.so.0": false,
		"lib/x86_64-linux-gnu/librt.so.1":      false,
		"lib/x86_64-linux-gnu/libgcc_s.so.1":   false,
	}
	spec := RuntimeSpecification{Platform: platform, InstallPath: ".tools/" + runtime.InstallPath}
	seen := make(map[string]bool)
	add := func(destination, hash string, library bool) bool {
		decoded, err := hex.DecodeString(hash)
		if !runtimePath(destination) || seen[destination] || err != nil || len(decoded) != 32 || strings.ToLower(hash) != hash {
			return false
		}
		seen[destination] = true
		file := RuntimeFile{Path: spec.InstallPath + "/" + destination, ContainerPath: "/" + destination, SHA256: hash}
		if library {
			if _, ok := allowed[destination]; !ok {
				return false
			}
			allowed[destination] = true
			spec.Libraries = append(spec.Libraries, file)
		} else {
			if !strings.HasPrefix(destination, "licenses/runtime/") {
				return false
			}
			spec.Licenses = append(spec.Licenses, file)
		}
		return true
	}
	for _, p := range runtime.Packages {
		for _, file := range p.Files {
			if file.Kind != "elf" && file.Kind != "notice" || !add(file.Destination, file.SHA256, file.Kind == "elf") {
				return invalid()
			}
		}
	}
	for _, file := range runtime.LicenseTexts {
		if !add(file.Destination, file.SHA256, false) {
			return invalid()
		}
	}
	if len(spec.Libraries) != 8 || len(spec.Licenses) != 6 {
		return invalid()
	}
	return spec, nil
}

func runtimePath(value string) bool {
	return value != "." && fs.ValidPath(value) && !strings.ContainsAny(value, "\\:\x00")
}
