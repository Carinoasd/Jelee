//go:build linux || windows

package nfo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeSourceBindsOriginalToHeldRootAndNFO(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "film.nfo")
	body := []byte("<movie><title>owned</title></movie>")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal("create owned source")
	}
	source, err := readNativeSource(context.Background(), root, "film.nfo", 1024)
	if err != nil || source == nil || !source.nativeObserved || !bytes.Equal(source.original, body) {
		t.Fatal("native source did not bind original bytes")
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal("open owned root")
	}
	defer directory.Close()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("open owned source")
	}
	defer file.Close()
	rootID, err := observeNFONativeIdentity(directory)
	if err != nil {
		t.Fatal("observe owned root")
	}
	fileID, err := observeNFONativeIdentity(file)
	if err != nil {
		t.Fatal("observe owned source")
	}
	if source.nativeRoot != rootID || source.nativeFile != fileID {
		t.Fatal("native source observation differs from retained original object")
	}
	access := diskSourceAccess()
	captures := 0
	access.observeNative = func(r sourceRoot, f sourceFile) (nfoNativeIdentity, nfoNativeIdentity, error) {
		captures++
		a, b, e := observeNativeSourceHandles(r, f)
		if captures == 2 {
			b.record[32] ^= 1
		}
		return a, b, e
	}
	if changed, err := readSource(context.Background(), root, "film.nfo", 1024, access); err != ErrChanged || changed != nil || captures != 2 {
		t.Fatal("native identity changed while reading but source accepted")
	}
}
