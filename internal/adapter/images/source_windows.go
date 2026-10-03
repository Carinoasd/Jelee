package images

import (
	"os"
	"syscall"
	"unsafe"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"golang.org/x/sys/windows"
)

// Validate the handle used for I/O, without changing operator-owned ACLs.
// Administrators and SYSTEM are trusted like the privileged Unix root account.
func privateImageObject(file *os.File, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return domain.ErrImageUnavailable
	}
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || attributes.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return domain.ErrImageUnavailable
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return domain.ErrImageUnavailable
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return domain.ErrImageUnavailable
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return domain.ErrImageUnavailable
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return domain.ErrImageUnavailable
	}
	var descriptor *windows.SECURITY_DESCRIPTOR
	var securityErr error
	err = raw.Control(func(fd uintptr) {
		descriptor, securityErr = windows.GetSecurityInfo(windows.Handle(fd), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	})
	if err != nil || securityErr != nil || !privateImageDescriptor(descriptor, user.User.Sid, system, administrators) {
		return domain.ErrImageUnavailable
	}
	return nil
}

func privateImageDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, trusted ...*windows.SID) bool {
	if descriptor == nil {
		return false
	}
	allowed := func(sid *windows.SID) bool {
		if sid == nil || !sid.IsValid() {
			return false
		}
		for _, identity := range trusted {
			if identity != nil && identity.IsValid() && sid.Equals(identity) {
				return true
			}
		}
		return false
	}
	owner, _, err := descriptor.Owner()
	if err != nil || !allowed(owner) {
		return false
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount > 128 {
		return false // Missing/null DACL grants unrestricted access.
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, i, &ace) != nil || ace == nil || uintptr(ace.Header.AceSize) < unsafe.Sizeof(*ace) {
			return false
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
			return false // Object/callback grants need different layouts/semantics.
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || uintptr(sid.Len())+unsafe.Offsetof(ace.SidStart) > uintptr(ace.Header.AceSize) {
			return false
		}
		// Check inheritance-only ACEs too: a private directory must not create
		// files that inherit a public read/write grant. Denials never cancel
		// out an unsafe grant for the purposes of this conservative policy.
		if ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE && ace.Mask != 0 && !allowed(sid) {
			return false
		}
	}
	return true
}
