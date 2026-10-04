// Package tools holds the embedded tool manifest and its typed accessors.
package tools

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// MatroskaToolSpecification is the pinned identity of one production
// mkvtoolnix or MediaInfo executable (E4). On linux-amd64 Libraries is the
// exact dependency closure with fixed image paths; on Windows it lists the
// files installed beside the executable, without image paths.
type MatroskaToolSpecification struct {
	Name        string
	Tool        string
	Version     string
	Platform    string
	InstallPath string
	Executable  RuntimeFile
	Libraries   []RuntimeFile
	Licenses    []RuntimeFile
}

// matroskaExecutables are the only names this accessor returns. mkvpropedit
// is pinned in the manifest but never production-allowed.
var matroskaExecutables = map[string]string{"mkvmerge": "mkvtoolnix", "mkvextract": "mkvtoolnix", "mediainfo": "mediainfo"}

type matroskaFile struct {
	Path          string `json:"path"`
	Source        string `json:"source"`
	Destination   string `json:"destination"`
	Kind          string `json:"kind"`
	SHA256        string `json:"sha256"`
	ContainerPath string `json:"containerPath"`
}

type matroskaManifest struct {
	MatroskaTools struct {
		SchemaVersion   int
		Optional        bool
		RuntimePackages []struct {
			Name  string
			Files []matroskaFile
		} `json:"runtimePackages"`
		Tools map[string]struct {
			Version   string
			Platforms map[string]struct {
				InstallPath string `json:"installPath"`
				Executables map[string]struct {
					matroskaFile
					ProductionAllowed bool     `json:"productionAllowed"`
					Closure           []string `json:"closure"`
				} `json:"executables"`
				Libraries       []matroskaFile `json:"libraries"`
				LicenseFiles    []matroskaFile `json:"licenseFiles"`
				LicenseTexts    []matroskaFile `json:"licenseTexts"`
				RuntimePackages []string       `json:"runtimePackages"`
			}
		}
	} `json:"matroskaTools"`
}

var errMatroskaManifest = errors.New("tool_manifest_invalid")

func digestValid(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

func containerPathValid(value string) bool {
	return strings.HasPrefix(value, "/") && runtimePath(value[1:])
}

// MatroskaToolSpec returns fresh values from the embedded manifest for one
// production executable. Paths are project relative (".tools/...").
func MatroskaToolSpec(platform, name string) (MatroskaToolSpecification, error) {
	toolName, ok := matroskaExecutables[name]
	if !ok {
		return MatroskaToolSpecification{}, errMatroskaManifest
	}
	var manifest matroskaManifest
	if err := json.Unmarshal(embeddedManifest, &manifest); err != nil || manifest.MatroskaTools.SchemaVersion != 1 || !manifest.MatroskaTools.Optional {
		return MatroskaToolSpecification{}, errMatroskaManifest
	}
	tool, ok := manifest.MatroskaTools.Tools[toolName]
	if !ok {
		return MatroskaToolSpecification{}, errMatroskaManifest
	}
	p, ok := tool.Platforms[platform]
	if !ok {
		return MatroskaToolSpecification{}, errors.New("tool_platform_unsupported")
	}
	executable, ok := p.Executables[name]
	if !ok || !executable.ProductionAllowed || !runtimePath(p.InstallPath) || !runtimePath(executable.Path) || !digestValid(executable.SHA256) {
		return MatroskaToolSpecification{}, errMatroskaManifest
	}
	install := ".tools/" + p.InstallPath
	spec := MatroskaToolSpecification{Name: name, Tool: toolName, Version: tool.Version, Platform: platform, InstallPath: install,
		Executable: RuntimeFile{Path: install + "/" + executable.Path, ContainerPath: executable.ContainerPath, SHA256: executable.SHA256}}
	files := make(map[string]RuntimeFile)
	addFile := func(file matroskaFile, path string, library bool) error {
		if !runtimePath(path) || !digestValid(file.SHA256) || file.ContainerPath != "" && !containerPathValid(file.ContainerPath) {
			return errMatroskaManifest
		}
		value := RuntimeFile{Path: install + "/" + path, ContainerPath: file.ContainerPath, SHA256: file.SHA256}
		if library {
			if file.ContainerPath != "" {
				if _, duplicate := files[file.ContainerPath]; duplicate {
					return errMatroskaManifest
				}
				files[file.ContainerPath] = value
			} else {
				spec.Libraries = append(spec.Libraries, value)
			}
			return nil
		}
		spec.Licenses = append(spec.Licenses, value)
		return nil
	}
	for _, library := range p.Libraries {
		if addFile(library, library.Path, true) != nil {
			return MatroskaToolSpecification{}, errMatroskaManifest
		}
	}
	for _, license := range p.LicenseFiles {
		if addFile(license, license.Path, false) != nil {
			return MatroskaToolSpecification{}, errMatroskaManifest
		}
	}
	for _, license := range p.LicenseTexts {
		if addFile(license, license.Destination, false) != nil {
			return MatroskaToolSpecification{}, errMatroskaManifest
		}
	}
	for _, name := range p.RuntimePackages {
		found := false
		for _, pkg := range manifest.MatroskaTools.RuntimePackages {
			if pkg.Name != name {
				continue
			}
			found = true
			for _, file := range pkg.Files {
				if addFile(file, "runtime/"+file.Destination, file.Kind == "elf") != nil {
					return MatroskaToolSpecification{}, errMatroskaManifest
				}
			}
		}
		if !found {
			return MatroskaToolSpecification{}, errMatroskaManifest
		}
	}
	if platform != "linux-amd64" {
		if executable.ContainerPath != "" || len(executable.Closure) != 0 || len(files) != 0 {
			return MatroskaToolSpecification{}, errMatroskaManifest
		}
		return spec, nil
	}
	// Linux: the closure names container paths only. glibc comes from the
	// pinned media runtime, the rest from this tool's own files.
	if !containerPathValid(executable.ContainerPath) || len(executable.Closure) < 1 || len(executable.Closure) > 32 || len(spec.Libraries) != 0 {
		return MatroskaToolSpecification{}, errMatroskaManifest
	}
	runtime, err := RuntimeSpec(platform)
	if err != nil {
		return MatroskaToolSpecification{}, errMatroskaManifest
	}
	for _, library := range runtime.Libraries {
		if _, duplicate := files[library.ContainerPath]; duplicate {
			return MatroskaToolSpecification{}, errMatroskaManifest
		}
		files[library.ContainerPath] = library
	}
	seen := make(map[string]bool, len(executable.Closure))
	for _, path := range executable.Closure {
		library, ok := files[path]
		if !ok || seen[path] {
			return MatroskaToolSpecification{}, errMatroskaManifest
		}
		seen[path] = true
		spec.Libraries = append(spec.Libraries, library)
	}
	return spec, nil
}

// MatroskaImageFiles lists every file the production image carries for the
// Matroska tools: the production executables, their non-glibc libraries and
// all license notices, each with its image path. glibc and its notices are
// part of RuntimeSpec and are not repeated.
func MatroskaImageFiles() ([]RuntimeFile, error) {
	runtime, err := RuntimeSpec("linux-amd64")
	if err != nil {
		return nil, errMatroskaManifest
	}
	shared := make(map[string]bool)
	for _, file := range append(append([]RuntimeFile(nil), runtime.Libraries...), runtime.Licenses...) {
		shared[file.ContainerPath] = true
	}
	byPath := make(map[string]RuntimeFile)
	var ordered []RuntimeFile
	for _, name := range []string{"mkvmerge", "mkvextract", "mediainfo"} {
		spec, err := MatroskaToolSpec("linux-amd64", name)
		if err != nil {
			return nil, err
		}
		for _, file := range append(append([]RuntimeFile{spec.Executable}, spec.Libraries...), spec.Licenses...) {
			if file.ContainerPath == "" || shared[file.ContainerPath] {
				continue
			}
			if previous, ok := byPath[file.ContainerPath]; ok {
				if previous.SHA256 != file.SHA256 {
					return nil, errMatroskaManifest
				}
				continue
			}
			byPath[file.ContainerPath] = file
			ordered = append(ordered, file)
		}
	}
	return ordered, nil
}
