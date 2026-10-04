package tools

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
)

// OCRToolSpecification is the pinned identity of the optional Tesseract
// runtime (G15.6). Libraries is the exact dependency closure with fixed
// image paths (glibc from RuntimeSpec); Languages maps each shipped
// language code to its traineddata file. Linux amd64 only.
type OCRToolSpecification struct {
	Version       string
	DebianVersion string
	Platform      string
	InstallPath   string
	Executable    RuntimeFile
	Libraries     []RuntimeFile
	// TessdataPath is the project-relative directory of the language data
	// and TessdataContainerPath its image directory.
	TessdataPath          string
	TessdataContainerPath string
	Languages             map[string]RuntimeFile
	Licenses              []RuntimeFile
}

// OCRLanguages are the language codes the manifest must pin, in a fixed
// order. Configuration may only select among them.
var OCRLanguages = []string{"eng", "chi_tra", "chi_sim", "jpn"}

var errOCRManifest = errors.New("tool_manifest_invalid")

var ocrLanguageCode = regexp.MustCompile(`\A[a-z]{3}(_[a-z]{3,4})?\z`)

type ocrFile struct {
	Source        string `json:"source"`
	Destination   string `json:"destination"`
	Kind          string `json:"kind"`
	SHA256        string `json:"sha256"`
	ContainerPath string `json:"containerPath"`
}

type ocrManifest struct {
	OCRTools struct {
		SchemaVersion int
		Optional      bool
		Packages      []struct {
			Name  string
			Files []ocrFile
		}
		Tools map[string]struct {
			Version       string
			DebianVersion string `json:"debianVersion"`
			Platforms     map[string]struct {
				InstallPath string `json:"installPath"`
				Executable  struct {
					Path          string   `json:"path"`
					ContainerPath string   `json:"containerPath"`
					Closure       []string `json:"closure"`
				} `json:"executable"`
				TessdataPath          string `json:"tessdataPath"`
				TessdataContainerPath string `json:"tessdataContainerPath"`
				Languages             map[string]struct {
					Package string `json:"package"`
				} `json:"languages"`
			}
		}
	} `json:"ocrTools"`
}

// maxOCRClosure bounds the Tesseract dependency closure. Debian's build
// links libcurl and libarchive with their TLS, Kerberos and LDAP stacks, so
// it is larger than the other tools' closures.
const maxOCRClosure = 64

// OCRToolSpec returns fresh values from the embedded manifest. Paths are
// project relative (".tools/...").
func OCRToolSpec(platform string) (OCRToolSpecification, error) {
	var manifest ocrManifest
	if err := json.Unmarshal(embeddedManifest, &manifest); err != nil || manifest.OCRTools.SchemaVersion != 1 || !manifest.OCRTools.Optional {
		return OCRToolSpecification{}, errOCRManifest
	}
	tool, ok := manifest.OCRTools.Tools["tesseract"]
	if !ok || len(manifest.OCRTools.Tools) != 1 {
		return OCRToolSpecification{}, errOCRManifest
	}
	p, ok := tool.Platforms[platform]
	if !ok || platform != "linux-amd64" {
		return OCRToolSpecification{}, errors.New("tool_platform_unsupported")
	}
	if !runtimePath(p.InstallPath) || !strings.HasPrefix(p.InstallPath, "ocr/tesseract/") || !runtimePath(p.TessdataPath) || !containerPathValid(p.TessdataContainerPath) ||
		!containerPathValid(p.Executable.ContainerPath) || len(p.Executable.Closure) < 1 || len(p.Executable.Closure) > maxOCRClosure {
		return OCRToolSpecification{}, errOCRManifest
	}
	install := ".tools/" + p.InstallPath
	spec := OCRToolSpecification{Version: tool.Version, DebianVersion: tool.DebianVersion, Platform: platform, InstallPath: install,
		TessdataPath: install + "/" + p.TessdataPath, TessdataContainerPath: p.TessdataContainerPath, Languages: make(map[string]RuntimeFile)}
	libraries := make(map[string]RuntimeFile)
	packages := make(map[string][]ocrFile)
	executables := 0
	for _, pkg := range manifest.OCRTools.Packages {
		packages[pkg.Name] = pkg.Files
		for _, file := range pkg.Files {
			if !runtimePath(file.Destination) || !digestValid(file.SHA256) || !containerPathValid(file.ContainerPath) {
				return OCRToolSpecification{}, errOCRManifest
			}
			value := RuntimeFile{Path: install + "/" + file.Destination, ContainerPath: file.ContainerPath, SHA256: file.SHA256}
			switch file.Kind {
			case "executable":
				if file.Destination != p.Executable.Path || file.ContainerPath != p.Executable.ContainerPath {
					return OCRToolSpecification{}, errOCRManifest
				}
				executables++
				spec.Executable = value
			case "elf":
				if _, duplicate := libraries[file.ContainerPath]; duplicate {
					return OCRToolSpecification{}, errOCRManifest
				}
				libraries[file.ContainerPath] = value
			case "notice":
				spec.Licenses = append(spec.Licenses, value)
			case "data":
				// Language data is resolved below through its package.
			default:
				return OCRToolSpecification{}, errOCRManifest
			}
		}
	}
	if executables != 1 || len(p.Languages) != len(OCRLanguages) {
		return OCRToolSpecification{}, errOCRManifest
	}
	for _, code := range OCRLanguages {
		language, ok := p.Languages[code]
		if !ok || !ocrLanguageCode.MatchString(code) {
			return OCRToolSpecification{}, errOCRManifest
		}
		var found []RuntimeFile
		for _, file := range packages[language.Package] {
			if file.Kind == "data" {
				found = append(found, RuntimeFile{Path: install + "/" + file.Destination, ContainerPath: file.ContainerPath, SHA256: file.SHA256})
			}
		}
		name := "/" + code + ".traineddata"
		if len(found) != 1 || found[0].Path != spec.TessdataPath+name || found[0].ContainerPath != p.TessdataContainerPath+name {
			return OCRToolSpecification{}, errOCRManifest
		}
		spec.Languages[code] = found[0]
	}
	runtime, err := RuntimeSpec(platform)
	if err != nil {
		return OCRToolSpecification{}, errOCRManifest
	}
	for _, library := range runtime.Libraries {
		if _, duplicate := libraries[library.ContainerPath]; duplicate {
			return OCRToolSpecification{}, errOCRManifest
		}
		libraries[library.ContainerPath] = library
	}
	seen := make(map[string]bool, len(p.Executable.Closure))
	for _, path := range p.Executable.Closure {
		library, ok := libraries[path]
		if !ok || seen[path] {
			return OCRToolSpecification{}, errOCRManifest
		}
		seen[path] = true
		spec.Libraries = append(spec.Libraries, library)
	}
	return spec, nil
}

// OCRImageFiles lists every file an OCR-enabled image adds: the executable,
// its non-glibc libraries (libresolv included), the language data and the
// notices, each with its image path. glibc and its notices are part of
// RuntimeSpec and are not repeated.
func OCRImageFiles() ([]RuntimeFile, error) {
	runtime, err := RuntimeSpec("linux-amd64")
	if err != nil {
		return nil, errOCRManifest
	}
	spec, err := OCRToolSpec("linux-amd64")
	if err != nil {
		return nil, err
	}
	shared := make(map[string]bool)
	for _, file := range slices.Concat(runtime.Libraries, runtime.Licenses) {
		shared[file.ContainerPath] = true
	}
	files := []RuntimeFile{spec.Executable}
	for _, library := range spec.Libraries {
		if !shared[library.ContainerPath] {
			files = append(files, library)
		}
	}
	for _, code := range OCRLanguages {
		files = append(files, spec.Languages[code])
	}
	files = append(files, spec.Licenses...)
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		if seen[file.ContainerPath] || shared[file.ContainerPath] {
			return nil, errOCRManifest
		}
		seen[file.ContainerPath] = true
	}
	return files, nil
}
