package nfo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeSourceCaptureFailureDoesNotFallBack(t *testing.T) {
	root := t.TempDir()
	body := []byte("<movie/>")
	if err := os.WriteFile(filepath.Join(root, "film.nfo"), body, 0600); err != nil {
		t.Fatal("create owned source")
	}
	for _, phase := range []int{1, 2} {
		access := diskSourceAccess()
		calls := 0
		access.observeNative = func(sourceRoot, sourceFile) (nfoNativeIdentity, nfoNativeIdentity, error) {
			calls++
			if calls == phase {
				return nfoNativeIdentity{}, nfoNativeIdentity{}, errNativeIdentity
			}
			return nfoNativeIdentity{}, nfoNativeIdentity{}, nil
		}
		if source, err := readSource(context.Background(), root, "film.nfo", 1024, access); err != errNativeIdentity || source != nil || calls != phase {
			t.Fatal("native capture failure fell back or exposed partial source")
		}
	}
	// Platform support is deliberately absent from the general readonly path.
	source, err := ReadSource(context.Background(), root, "film.nfo", 1024)
	if err != nil || source.nativeObserved || !bytes.Equal(source.original, body) {
		t.Fatal("readonly source incorrectly requires native capture")
	}
}
