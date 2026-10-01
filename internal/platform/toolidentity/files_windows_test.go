package toolidentity

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// A directory junction does not require developer-mode symlink privilege.
// Layout: https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_reparse_data_buffer
func junction(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Mkdir(link, 0700); err != nil {
		t.Fatal(err)
	}
	path, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	substitute, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		t.Fatal(err)
	}
	printName, err := windows.UTF16FromString(target)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 16+2*(len(substitute)+len(printName)))
	binary.LittleEndian.PutUint32(data, windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(data[4:], uint16(len(data)-8))
	binary.LittleEndian.PutUint16(data[10:], uint16(2*(len(substitute)-1)))
	binary.LittleEndian.PutUint16(data[12:], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(data[14:], uint16(2*(len(printName)-1)))
	for i, v := range append(substitute, printName...) {
		binary.LittleEndian.PutUint16(data[16+i*2:], v)
	}
	var returned uint32
	if err := windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &data[0], uint32(len(data)), nil, 0, &returned, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Errorf("junction cleanup: %v", err)
		}
	})
}

func TestWindowsJunctionsRefused(t *testing.T) {
	for _, component := range []string{"project", "ancestor", "temporary"} {
		t.Run(component, func(t *testing.T) {
			root, spec := identityFixture(t)
			target := t.TempDir()
			link := ""
			switch component {
			case "project":
				link = filepath.Join(t.TempDir(), "project")
				target = root
				root = link
			case "ancestor":
				link = filepath.Join(root, ".tools/media/test/bin")
				if err := os.Remove(filepath.Join(root, spec.ExecutablePath)); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			case "temporary":
				link = filepath.Join(root, ".testdata")
			}
			junction(t, link, target)
			info, err := os.Lstat(link)
			if err != nil {
				t.Fatal(err)
			}
			if safeFileInfo(info, true) {
				t.Fatal("junction accepted as regular directory")
			}
			got := diagnose(context.Background(), root, spec, func(context.Context, string, string) ([]byte, error) {
				t.Fatal("junction reached execution")
				return nil, nil
			})
			if got.Reason != "unsafe_path" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func makeSparse(t *testing.T, file *os.File) {
	t.Helper()
	var returned uint32
	if err := windows.DeviceIoControl(windows.Handle(file.Fd()), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &returned, nil); err != nil {
		t.Fatal(err)
	}
}

func preventCleanup(t *testing.T, path string) func() {
	t.Helper()
	name, err := windows.UTF16PtrFromString(filepath.Join(path, "held-open"))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Error(err)
		}
	}
}
