package buildinfo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultVersionMatchesWebClient(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Version != DefaultVersion {
		t.Fatalf("web/package.json is %q, server default %q: release them together", pkg.Version, DefaultVersion)
	}
}

func TestVersionFallsBackForInvalidStamps(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })
	for stamp, want := range map[string]string{
		"":                                  DefaultVersion,
		"1.2.3":                             "1.2.3",
		"1.2.3-rc.1+build.5":                "1.2.3-rc.1+build.5",
		"v1.2.3":                            DefaultVersion,
		"1.2":                               DefaultVersion,
		"01.2.3":                            DefaultVersion,
		"1.2.3; font-src *":                 DefaultVersion,
		"1.2.3-" + string(make([]byte, 70)): DefaultVersion,
	} {
		version = stamp
		if got := Version(); got != want {
			t.Errorf("stamp %q: %q, want %q", stamp, got, want)
		}
	}
}
