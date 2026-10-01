//go:build !linux

package media

import "os"

func readOnlyFlags() int { return os.O_RDONLY }
