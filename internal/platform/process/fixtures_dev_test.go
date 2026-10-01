//go:build jelee_fixture_tools

package process

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFixtureBuildRegistrationRemainsConstrained(t *testing.T) {
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("registration-only fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	config := Config{MaxConcurrent: 1, Timeout: time.Second, MaxStdoutBytes: 100, MaxStderrBytes: 100, TempRoot: t.TempDir()}
	tool := Tool{ID: "ffmpeg", Path: path, Operations: map[string][]string{"generate-video": {"fixed-argv"}}}
	if _, err := NewFixtureRunner(config, tool); err != nil {
		t.Fatal(err)
	}
	tool.Operations["version"] = []string{"-version"}
	if _, err := NewFixtureRunner(config, tool); err != nil {
		t.Fatal("fixture version operation was rejected")
	}
	if _, err := New(config, []Tool{tool}); !errors.Is(err, ErrInvalid) {
		t.Fatal("ordinary constructor accepted ffmpeg in a tagged build")
	}
	tool.Operations = map[string][]string{"arbitrary": {"-version"}}
	if _, err := NewFixtureRunner(config, tool); !errors.Is(err, ErrInvalid) {
		t.Fatal("fixture runner accepted an unregistered operation family")
	}
	tool.ID = "helper"
	if _, err := NewFixtureRunner(config, tool); !errors.Is(err, ErrInvalid) {
		t.Fatal("fixture runner accepted another tool identity")
	}
}
