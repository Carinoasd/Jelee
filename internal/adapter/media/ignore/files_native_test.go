//go:build linux || windows

package ignoresource

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeHandleIdentityReadonlyAndAbsence(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	path := filepath.Join(root, ".jeleeignore")
	original := []byte("*.tmp\n!keep.tmp\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal("fixture write failed")
	}
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal("fixture mkdir failed")
	}
	d, err := openNativeRoot(root)
	if err != nil {
		t.Fatal("native root failed:", err)
	}
	defer d.Close()
	rootInfo, err := d.Stat()
	if err != nil || rootInfo.kind != nodeDirectory || rootInfo.identity == (fileIdentity{}) {
		t.Fatal("invalid held root identity")
	}
	again, err := openNativeRoot(root)
	if err != nil {
		t.Fatal("root reopen failed:", err)
	}
	againInfo, statErr := again.Stat()
	closeErr := again.Close()
	if statErr != nil || closeErr != nil || againInfo.identity != rootInfo.identity {
		t.Fatal("reopened root lost identity")
	}
	f, err := d.OpenRule()
	if err != nil {
		t.Fatal("native source failed:", err)
	}
	before, err := f.Stat()
	if err != nil || before.kind != nodeRegular || before.size != int64(len(original)) || before.identity == rootInfo.identity {
		t.Fatal("invalid held source metadata")
	}
	if n, err := f.(*nativeFile).file.Write([]byte("bad")); err == nil || n != 0 {
		t.Fatal("source handle permits writing")
	}
	got, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("native read changed bytes")
	}
	after, err := f.Stat()
	if err != nil || before != after {
		t.Fatal("readonly operation changed source identity or metadata")
	}
	if f.Close() != nil || f.Close() != nil {
		t.Fatal("source close was not idempotent")
	}
	if state, err := f.Stat(); err != ErrRead || state != (fileState{}) {
		t.Fatal("closed Stat did not fail safely")
	}
	if n, err := f.Read(make([]byte, 1)); err != ErrRead || n != 0 {
		t.Fatal("closed Read did not fail safely")
	}
	child, err := d.OpenDirectory("child")
	if err != nil {
		t.Fatal("native child failed:", err)
	}
	defer child.Close()
	if missing, err := child.OpenRule(); err != errAbsent || missing != nil {
		t.Fatal("missing fixed leaf was not explicit absence")
	}
	if missing, err := d.OpenDirectory("absent"); err == nil || errors.Is(err, errAbsent) || missing != nil {
		t.Fatal("missing directory was misreported as absent rule")
	}
	if err := os.Mkdir(filepath.Join(root, "child", ".jeleeignore"), 0700); err != nil {
		t.Fatal("fixture mkdir failed")
	}
	if invalid, err := child.OpenRule(); err != ErrUnsafe || invalid != nil {
		t.Fatal("directory accepted as regular rule")
	}
	if d.Close() != nil || d.Close() != nil {
		t.Fatal("directory close was not idempotent")
	}
	if opened, err := d.OpenRule(); err != ErrRead || opened != nil {
		t.Fatal("closed directory reopened a source")
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("original content changed")
	}
}

func TestNativeRejectsUnsafeNamesAndMissingRoot(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	d, err := openNativeRoot(root)
	if err != nil {
		t.Fatal("native root failed:", err)
	}
	defer d.Close()
	for _, name := range []string{"", ".", "..", "/absolute", "a/b", `a\b`, "a:b", "a\x00b", "a\nb", string([]byte{0xff})} {
		if child, err := d.OpenDirectory(name); err != ErrInvalid || child != nil {
			t.Fatal("unsafe component accepted")
		}
	}
	for _, path := range []string{"", ".", "relative", root + "\x00"} {
		if opened, err := openNativeRoot(path); err != ErrInvalid || opened != nil {
			t.Fatal("invalid root accepted")
		}
	}
	if opened, err := openNativeRoot(filepath.Join(root, "missing")); err != ErrRead || opened != nil {
		t.Fatal("missing root leaked a partial handle or absence")
	}
	path := filepath.Join(root, "regular")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal("fixture write failed")
	}
	if opened, err := openNativeRoot(path); err != ErrUnsafe || opened != nil {
		t.Fatal("regular file accepted as root")
	}
}
