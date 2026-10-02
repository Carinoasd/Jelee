//go:build !linux

package main

import "os"

// The controlled workload and its fixture filesystem run on native Linux.
func privateFixtureRoot(os.FileInfo) bool { return false }
