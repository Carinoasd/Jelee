package tools

import (
	"io/fs"
	"strings"
	"testing"
)

func TestFFprobeEmbeddedIdentityAndFreshValues(t *testing.T) {
	for platform, hash := range map[string]string{"windows-amd64": "f0d36ecbbdd3bcfac3efa078c96c7271c2e68b3810595552ac3b7f17e9a65c52", "linux-amd64": "a5bd5e9f8d74ab2c6d7d9e2e2738ff6d67bf9613e7143e143ba8ebe9b06385fb"} {
		t.Run(platform, func(t *testing.T) {
			spec, err := FFprobeSpec(platform)
			if err != nil {
				t.Fatal(err)
			}
			if spec.SHA256 != hash || spec.Platform != platform || spec.VendorVersion == "" || len(spec.SourceRevision) != 40 || spec.SourceURL == "" || spec.UpstreamVersion == "" || !fs.ValidPath(spec.InstallPath) || !fs.ValidPath(spec.ExecutablePath) || !strings.HasPrefix(spec.ExecutablePath, spec.InstallPath+"/") || !strings.Contains(spec.ExecutablePath, "/bin/ffprobe") || len(spec.Licenses) != 1 || !fs.ValidPath(spec.Licenses[0].Path) || len(spec.Licenses[0].SHA256) != 64 {
				t.Fatal("embedded identity differs from reviewed pins")
			}
			spec.Licenses[0].SHA256 = "mutated"
			spec.SHA256 = "mutated"
			again, err := FFprobeSpec(platform)
			if err != nil || again.SHA256 != hash || again.Licenses[0].SHA256 == "mutated" {
				t.Fatal("caller mutated embedded identity")
			}
		})
	}
	for _, platform := range []string{"", "linux-arm64", "ffmpeg", "../../linux-amd64"} {
		if _, err := FFprobeSpec(platform); err == nil {
			t.Fatalf("unexpected platform allowed: %q", platform)
		}
	}
}
