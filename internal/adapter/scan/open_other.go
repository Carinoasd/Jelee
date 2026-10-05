//go:build !linux && !windows

package scan

import "os"

func directoryFlags() int { return os.O_RDONLY }

// Other operating systems have not been qualified for this filesystem adapter.
// In particular os.Root on js does not promise race-safe traversal containment.
func supportedPlatform() bool { return false }

func fileFlags() int { return os.O_RDONLY }
