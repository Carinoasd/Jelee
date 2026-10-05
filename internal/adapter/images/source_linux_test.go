package images

import (
	"os"
	"testing"
)

func imageSourceTestBase(t *testing.T) string {
	t.Helper()
	// Native Linux permissions are required even when the Go toolchain's
	// TMPDIR points at a Windows-mounted build cache.
	base, err := os.MkdirTemp("/tmp", "jelee-image-source-")
	if err != nil {
		t.Fatal("create private image fixture")
	}
	t.Cleanup(func() {
		if os.RemoveAll(base) != nil {
			t.Error("remove owned image fixture")
		}
	})
	return base
}
