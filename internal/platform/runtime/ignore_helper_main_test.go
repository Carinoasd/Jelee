//go:build !jelee_probe_tests

package runtime

import (
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignorehelper"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == legacyignorehelper.Command {
		os.Exit(legacyignorehelper.Main())
	}
	os.Exit(m.Run())
}
