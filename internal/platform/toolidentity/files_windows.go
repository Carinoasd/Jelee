package toolidentity

import (
	"golang.org/x/sys/windows"
	"os"
	"syscall"
)

func readFlags() int { return os.O_RDONLY }
func safeFileInfo(info os.FileInfo, directory bool) bool {
	if info == nil {
		return false
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	if directory {
		return info.Mode().Type() == os.ModeDir
	}
	return info.Mode().IsRegular()
}
func makePrivate(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
