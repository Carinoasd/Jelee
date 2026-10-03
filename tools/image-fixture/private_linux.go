package main

import (
	"os"
	"syscall"
)

func privateFixtureRoot(info os.FileInfo) bool {
	state, ok := info.Sys().(*syscall.Stat_t)
	return ok && state.Uid == uint32(os.Geteuid()) && info.IsDir() && info.Mode().Perm() == 0700
}
