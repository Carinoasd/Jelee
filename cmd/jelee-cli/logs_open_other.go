//go:build !windows

package main

import "os"

// openSharedLogFile opens a log file for reading. Elsewhere than Windows an open
// file never prevents the server from renaming it during rotation.
func openSharedLogFile(path string) (*os.File, error) {
	return os.Open(path) //nolint:gosec // G304: the configured log file and its rotated backups
}
