//go:build !linux

package media

// Descriptor counting needs /proc; the goroutine, slot and file-handle-free
// assertions of the leak tests still run elsewhere.
const fdCountingSupported = false

func openDescriptors() int { return 0 }

func openDescriptorsOf(string) int { return 0 }
