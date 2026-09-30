//go:build !linux && !windows

package probe

func supportedPlatform() bool { return false }
func readFlags() int          { return 0 }
