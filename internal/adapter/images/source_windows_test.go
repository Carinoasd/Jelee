package images

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"golang.org/x/sys/windows"
)

func imageSourceTestBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	imageTestDACL(t, base, imageTestPrivateACL(t))
	return base
}

func imageTestUser(t *testing.T) *windows.SID {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal("read image fixture user identity")
	}
	return user.User.Sid
}

func imageTestPrivateACL(t *testing.T) string {
	t.Helper()
	return "D:P(A;OICI;FA;;;" + imageTestUser(t).String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
}

func imageTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal("parse owned image fixture permissions")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		t.Fatal("read owned image fixture permissions")
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatalf("set owned image fixture permissions: %v", err)
	}
}

func TestImageSourceWindowsDescriptorRejectsUnsafeGrants(t *testing.T) {
	user := imageTestUser(t)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal("read system identity")
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal("read administrator identity")
	}
	owner := "O:" + user.String()
	private := imageTestPrivateACL(t)
	for _, tc := range []struct {
		name, sddl string
		want       bool
	}{
		{"private", owner + private, true},
		{"public_read", owner + private + "(A;OICI;GR;;;WD)", false},
		{"public_write", owner + private + "(A;OICI;GW;;;WD)", false},
		{"public_inherited_child", owner + private + "(A;OIIO;GR;;;WD)", false},
		{"deny_does_not_hide_grant", owner + "D:P(D;;GA;;;WD)(A;;GA;;;WD)", false},
		{"denial_only", owner + private + "(D;;GW;;;WD)", true},
		{"missing_dacl", owner, false},
		{"null_dacl", owner + "D:NO_ACCESS_CONTROL", false},
		{"untrusted_owner", "O:WD" + private, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatal("parse descriptor fixture")
			}
			if privateImageDescriptor(descriptor, user, system, administrators) != tc.want {
				t.Fatal("image descriptor permission decision differs")
			}
		})
	}
	if privateImageDescriptor(nil, user, system, administrators) {
		t.Fatal("nil descriptor accepted")
	}
}

func TestImageSourceWindowsRejectsSharedScratchWithoutChangingACL(t *testing.T) {
	for _, grant := range []string{"(A;OICI;GR;;;WD)", "(A;OICI;GW;;;WD)", "(A;OIIO;GR;;;WD)"} {
		t.Run(grant, func(t *testing.T) {
			source, scratch := imageSourceFixture(t)
			imageWrite(t, filepath.Join(source.RootPath, "movie", "poster.jpg"), "private image contents")
			imageTestDACL(t, scratch, imageTestPrivateACL(t)+grant)
			before, err := windows.GetNamedSecurityInfo(scratch, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal("read scratch ACL before staging")
			}
			staged, err := stageLocalPrimary(context.Background(), source, scratch, 1024)
			if staged != nil {
				_ = staged.Close()
				t.Fatal("shared scratch was accepted")
			}
			if !errors.Is(err, domain.ErrImageUnavailable) {
				t.Fatal("shared scratch rejection was not safe")
			}
			after, err := windows.GetNamedSecurityInfo(scratch, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil || before.String() != after.String() {
				t.Fatal("staging changed operator-owned ACL")
			}
			imageScratchEmpty(t, scratch)
		})
	}
}

func TestImageSourceWindowsSecurityFollowsHeldHandle(t *testing.T) {
	base := imageSourceTestBase(t)
	original := filepath.Join(base, "held")
	if os.Mkdir(original, 0700) != nil {
		t.Fatal("create held image fixture")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal("open owned image fixture root")
	}
	defer root.Close()
	// Root.Open permits sharing deletion, unlike os.Open on Windows. Keep
	// that handle alive across a path replacement to exercise object binding.
	held, err := root.Open("held")
	if err != nil {
		t.Fatal("open held image directory")
	}
	defer held.Close()
	if privateImageObject(held, true) != nil {
		t.Fatal("private held image directory rejected")
	}
	if os.Rename(original, filepath.Join(base, "moved")) != nil || os.Mkdir(original, 0700) != nil {
		t.Fatal("replace owned image directory path")
	}
	imageTestDACL(t, original, imageTestPrivateACL(t)+"(A;OICI;GR;;;WD)")
	if privateImageObject(held, true) != nil {
		t.Fatal("security check followed replacement path")
	}
	replacement, err := root.Open("held")
	if err != nil {
		t.Fatal("open replacement image directory")
	}
	defer replacement.Close()
	if !errors.Is(privateImageObject(replacement, true), domain.ErrImageUnavailable) {
		t.Fatal("public replacement image directory accepted")
	}
}
