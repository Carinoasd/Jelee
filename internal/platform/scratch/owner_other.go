//go:build !linux && !windows

package scratch

import "io/fs"

// Liveness cannot be proven on other platforms, so nothing is ever removed.
func platformStart() (string, bool)       { return "", false }
func platformState(owner) liveness        { return unknown }
func ownedByCurrentUser(fs.FileInfo) bool { return false }
