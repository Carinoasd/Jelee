package toolidentity

import (
	"os"
	"testing"
)

func makeSparse(t *testing.T, file *os.File) { t.Helper() } // ftruncate creates a hole on the native test filesystem.
func preventCleanup(t *testing.T, path string) func() {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission denial requires a non-root Linux test user")
	}
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Chmod(path, 0700); err != nil {
			t.Error(err)
		}
	}
}
