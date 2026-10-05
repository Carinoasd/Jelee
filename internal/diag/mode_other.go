//go:build !unix

package diag

import "io/fs"

type modeFacts struct {
	checked, ownedBySelf, groupOrOther, otherWrite, sticky bool
}

// Windows ACLs are not represented by permission bits; the check degrades to
// existence and writability.
func inspectMode(fs.FileInfo) modeFacts { return modeFacts{} }

func groupOrOtherAccess(fs.FileInfo) bool { return false }
