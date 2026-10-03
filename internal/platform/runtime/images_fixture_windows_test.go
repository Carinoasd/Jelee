package runtime

import (
	"testing"

	"golang.org/x/sys/windows"
)

func imagesIntegrationScratch(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal("read image integration fixture identity")
	}
	// Only this test's new directory is changed. Children inherit a private
	// DACL; no operator-configured path or ancestor is modified.
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		t.Fatal("parse image integration fixture permissions")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		t.Fatal("read image integration fixture permissions")
	}
	if windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil) != nil {
		t.Fatal("set owned image integration fixture permissions")
	}
	return directory
}
