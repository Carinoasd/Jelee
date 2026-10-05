//go:build unix

package diag

import (
	"io/fs"
	"os"
	"syscall"
)

type modeFacts struct {
	checked, ownedBySelf, groupOrOther, otherWrite, sticky bool
}

func inspectMode(info fs.FileInfo) modeFacts {
	m := modeFacts{checked: true, ownedBySelf: true}
	perm := info.Mode()
	m.groupOrOther = perm.Perm()&0o077 != 0
	m.otherWrite = perm.Perm()&0o002 != 0
	m.sticky = perm&fs.ModeSticky != 0
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		m.ownedBySelf = int(st.Uid) == os.Geteuid()
	}
	return m
}

func groupOrOtherAccess(info fs.FileInfo) bool { return info.Mode().Perm()&0o077 != 0 }
