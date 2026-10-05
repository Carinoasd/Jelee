package media

import (
	"syscall"
	"time"
)

// processCPU returns the user plus system CPU time of the test process.
func processCPU() (time.Duration, bool) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, false
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano()), true
}
