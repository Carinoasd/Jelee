//go:build jelee_probe_tests && !linux

package runtime

import "os"

// The real soak needs Linux cgroup/proc evidence, including a pollable FIFO.
func openImagesSoakOutput(_ *os.File) (*os.File, error) {
	return nil, errImagesSoakStream
}
