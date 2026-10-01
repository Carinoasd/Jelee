//go:build linux || windows

package postgres

import (
	"os"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/legacyignorehelper"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == legacyignorehelper.Command {
		os.Exit(legacyignorehelper.Main())
	}
	os.Exit(m.Run())
}
