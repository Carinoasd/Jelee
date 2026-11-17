package media

import (
	"os"
	"path/filepath"
	"strings"
)

const fdCountingSupported = true

// openDescriptors counts this process's open descriptors, or -1.
func openDescriptors() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// openDescriptorsOf counts descriptors that refer to path.
func openDescriptorsOf(path string) int {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	n := 0
	for _, entry := range entries {
		target, err := os.Readlink("/proc/self/fd/" + entry.Name())
		if err == nil && strings.TrimSuffix(target, " (deleted)") == resolved {
			n++
		}
	}
	return n
}
