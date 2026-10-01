//go:build !linux

package nfo

import "os"

func readOnlyFlags() int { return os.O_RDONLY }
