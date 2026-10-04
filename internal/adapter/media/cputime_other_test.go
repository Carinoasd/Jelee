//go:build !linux

package media

import "time"

func processCPU() (time.Duration, bool) { return 0, false }
