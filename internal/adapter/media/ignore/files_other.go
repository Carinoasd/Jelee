//go:build !linux && !windows

package ignoresource

// No weaker pathname-based fallback is used on an unqualified platform.
func openNativeRoot(string) (directory, error) { return nil, ErrUnavailable }
