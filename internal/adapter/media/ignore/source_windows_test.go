//go:build windows

package ignoresource

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
	"golang.org/x/sys/windows"
)

// The test creates a mount-point reparse buffer directly. No shell, privilege
// change, global setting or external executable is needed for a local junction.
func sourceJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Mkdir(link, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Error("junction cleanup failed")
		}
	})
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal("junction fixture open failed")
	}
	defer windows.CloseHandle(handle)
	substitute := utf16.Encode([]rune(`\??\` + target))
	printName := utf16.Encode([]rune(target))
	buffer := make([]byte, 16+2*(len(substitute)+1+len(printName)+1))
	binary.LittleEndian.PutUint32(buffer, 0xA0000003)
	binary.LittleEndian.PutUint16(buffer[4:], uint16(len(buffer)-8))
	binary.LittleEndian.PutUint16(buffer[10:], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(buffer[12:], uint16(2*(len(substitute)+1)))
	binary.LittleEndian.PutUint16(buffer[14:], uint16(2*len(printName)))
	for i, v := range substitute {
		binary.LittleEndian.PutUint16(buffer[16+2*i:], v)
	}
	for i, v := range printName {
		binary.LittleEndian.PutUint16(buffer[16+2*(len(substitute)+1+i):], v)
	}
	var returned uint32
	if err = windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
			t.Skip("host cannot create a local junction; actual reparse case not exercised")
		}
		t.Fatal("junction fixture creation failed")
	}
	stat, err := os.Lstat(link)
	if err != nil || stat.Mode()&os.ModeIrregular == 0 && stat.Mode()&os.ModeSymlink == 0 {
		t.Fatal("fixture is not an actual reparse point")
	}
}

func TestSourceWindowsRejectsRealJunctions(t *testing.T) {
	for _, boundary := range []string{"root", "root-ancestor", "parent-inside", "parent-outside", "rule-leaf"} {
		t.Run(boundary, func(t *testing.T) {
			base := filepath.Clean(t.TempDir())
			root := filepath.Join(base, "library")
			outside := filepath.Join(base, "outside")
			writeRule(t, root, ".jeleeignore", "*.tmp\n")
			writeRule(t, root, "dir/.jeleeignore", "!keep.tmp\n")
			writeRule(t, outside, ".jeleeignore", "!keep.tmp\n")
			candidate := "dir/keep.tmp"
			link, target := "", ""
			switch boundary {
			case "root":
				link = filepath.Join(base, "alias")
				target = root
				root = link
			case "root-ancestor":
				link = filepath.Join(base, "alias")
				target = base
				root = filepath.Join(link, "library")
			case "parent-inside":
				link = filepath.Join(root, "alias")
				target = filepath.Join(root, "dir")
				candidate = "alias/keep.tmp"
			case "parent-outside":
				link = filepath.Join(root, "alias")
				target = outside
				candidate = "alias/keep.tmp"
			case "rule-leaf":
				link = filepath.Join(root, ".jeleeignore")
				target = outside
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			}
			sourceJunction(t, link, target)
			h := &diskSourceHarness{}
			o, err := NewResolver().evaluate(context.Background(), root, candidate, ignore.File, ignore.Options{}, h.access())
			zeroObservation(t, o, err, ErrUnsafe)
			h.closed(t)
		})
	}
}

func TestSourceWindowsSymlinkIsNotAbsence(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	link := filepath.Join(root, ".jeleeignore")
	if err := os.Symlink(filepath.Join(root, "missing"), link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skip("Windows symlink creation privilege is unavailable; real junction cases are separate")
		}
		t.Fatal("symlink fixture failed")
	}
	o, err := NewResolver().Evaluate(context.Background(), root, "file.tmp", ignore.File, ignore.Options{})
	zeroObservation(t, o, err, ErrUnsafe)
}

func TestSourceWindowsHostAliasAndPatternCaseRemainSeparate(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, ".JELEEIGNORE", "*.TMP\n")
	writeRule(t, root, "Case/.jeleeignore", "!keep.TMP\n")
	actual, err := os.Stat(filepath.Join(root, "Case"))
	if err != nil {
		t.Fatal(err)
	}
	alias, err := os.Stat(filepath.Join(root, "case"))
	if err != nil || !os.SameFile(actual, alias) {
		t.Skip("test filesystem does not expose case aliases; exact source lookup covered on Linux")
	}
	r := NewResolver()
	h := &diskSourceHarness{}
	first := evaluateDisk(t, r, h, root, "case/keep.TMP")
	if first.Match.Outcome != ignore.Include || first.Match.Source != "case/.jeleeignore" || h.counts.fullReads.Load() != 4 {
		t.Fatal("host alias lookup lost caller-relative source provenance")
	}
	sensitive := evaluateDisk(t, r, &diskSourceHarness{}, root, "case/keep.tmp")
	if sensitive.Match != (ignore.Match{}) {
		t.Fatal("host filename alias incorrectly folded rule matching")
	}
	folded, err := r.Evaluate(context.Background(), root, "case/keep.tmp", ignore.File, ignore.Options{Case: ignore.CaseASCIIInsensitive})
	if err != nil || folded.Match.Outcome != ignore.Include || folded.Match.Source != "case/.jeleeignore" {
		t.Fatal("explicit pattern case mode not honored")
	}
	if first.Token() == folded.Token() {
		t.Fatal("source cache token omitted pattern case mode")
	}
}
