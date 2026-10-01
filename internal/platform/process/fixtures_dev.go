//go:build jelee_fixture_tools

package process

import (
	"path/filepath"
	"runtime"
	"strings"
)

// NewFixtureRunner exists only in explicit fixture-generation builds. It is
// absent from ordinary production builds and never enabled by environment.
// The developer generator registers verified ffmpeg and fixed argv; requests
// retain the same no-argv/no-path contract as the production runner.
func NewFixtureRunner(config Config, tool Tool) (*Runner, error) {
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if tool.ID != "ffmpeg" || !strings.EqualFold(filepath.Base(tool.Path), name) {
		return nil, ErrInvalid
	}
	for operation := range tool.Operations {
		switch operation {
		case "version", "generate-video", "generate-audio", "generate-image":
		default:
			return nil, ErrInvalid
		}
	}
	return newRunner(config, []Tool{tool}, true)
}
