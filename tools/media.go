package tools

import (
	_ "embed"
	"encoding/json"
	"errors"
)

// The trust source is part of the built executable. Changes to the installed
// manifest or installation record cannot alter the expected runtime identity.
//
//go:embed manifest.json
var embeddedManifest []byte

type LicenseFile struct {
	Path   string
	SHA256 string
}

// FFprobeSpecification contains project-relative paths and pinned provenance.
// These fields are internal registration data, not a public diagnostic response.
type FFprobeSpecification struct {
	Platform        string
	VendorVersion   string
	UpstreamVersion string
	SourceRevision  string
	SourceURL       string
	InstallPath     string
	ExecutablePath  string
	SHA256          string
	Licenses        []LicenseFile
}

// FFprobeSpec returns fresh values from the embedded manifest. There is no
// ffmpeg accessor or arbitrary tool-name parameter in the runtime API.
func FFprobeSpec(platform string) (FFprobeSpecification, error) {
	var manifest struct {
		MediaTools struct {
			Platforms map[string]struct {
				VendorVersion   string        `json:"vendorVersion"`
				UpstreamVersion string        `json:"upstreamVersion"`
				SourceRevision  string        `json:"sourceRevision"`
				SourceURL       string        `json:"sourceURL"`
				InstallPath     string        `json:"installPath"`
				LicenseFiles    []LicenseFile `json:"licenseFiles"`
				Executables     map[string]struct {
					Path              string
					SHA256            string
					ProductionAllowed bool
				} `json:"executables"`
			} `json:"platforms"`
		} `json:"mediaTools"`
	}
	if err := json.Unmarshal(embeddedManifest, &manifest); err != nil {
		return FFprobeSpecification{}, errors.New("tool_manifest_invalid")
	}
	p, ok := manifest.MediaTools.Platforms[platform]
	if !ok {
		return FFprobeSpecification{}, errors.New("tool_platform_unsupported")
	}
	e, ok := p.Executables["ffprobe"]
	if !ok || !e.ProductionAllowed {
		return FFprobeSpecification{}, errors.New("tool_manifest_invalid")
	}
	licenses := append([]LicenseFile(nil), p.LicenseFiles...)
	for i := range licenses {
		licenses[i].Path = ".tools/" + p.InstallPath + "/" + licenses[i].Path
	}
	return FFprobeSpecification{Platform: platform, VendorVersion: p.VendorVersion, UpstreamVersion: p.UpstreamVersion, SourceRevision: p.SourceRevision, SourceURL: p.SourceURL, InstallPath: ".tools/" + p.InstallPath, ExecutablePath: ".tools/" + p.InstallPath + "/" + e.Path, SHA256: e.SHA256, Licenses: licenses}, nil
}
