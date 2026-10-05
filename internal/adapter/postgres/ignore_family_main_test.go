//go:build linux || windows

package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/legacyignorehelper"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// coverHelperPolicyFile is written by the embedded cover real-tool test into
// the runner's private TempRoot, the parent of the child's TMPDIR. The
// sandboxed child has no other channel: the runner clears its environment.
const coverHelperPolicyFile = "sandbox-policy.json"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == legacyignorehelper.Command {
		os.Exit(legacyignorehelper.Main())
	}
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperCommand {
		os.Exit(coverTestHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// coverTestHelper is the test binary acting as the sandbox helper with the
// explicit developer profile's pinned policy; production uses proberuntime.
func coverTestHelper(argv []string) int {
	var policy sandbox.Policy
	data, err := os.ReadFile(filepath.Join(filepath.Dir(os.Getenv("TMPDIR")), coverHelperPolicyFile))
	if err != nil || json.Unmarshal(data, &policy) != nil {
		return sandbox.ExitInvalid
	}
	return sandbox.RunHelper(argv, policy)
}
