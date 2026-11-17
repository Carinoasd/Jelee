//go:build !windows

package process

import (
	"os"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// startConfinedChild exists only on Windows. Linux confinement is applied
// by the sandbox helper inside the child instead (docs/media-sandbox.md).
func startConfinedChild(string, []string, string, []string, *os.File, *os.File, *os.File, sandbox.Confinement) (child, error) {
	return nil, ErrUnsupported
}
