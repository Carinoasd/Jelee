//go:build linux

package media

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAdviseWillNeedReachesKernel gives real advice on a regular file and on
// a closed one; neither may fail loudly or change the file.
func TestAdviseWillNeedReachesKernel(t *testing.T) {
	if !prefetchSupported {
		t.Fatal("Linux must support read-ahead advice")
	}
	path := filepath.Join(t.TempDir(), "media")
	if err := os.WriteFile(path, []byte("original bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	adviseWillNeed(file, 0, 8)
	adviseWillNeed(file, 0, 0)
	_ = file.Close()
	adviseWillNeed(file, 0, 8)
	if data, err := os.ReadFile(path); err != nil || string(data) != "original bytes" {
		t.Fatal("advice changed the file")
	}
}
