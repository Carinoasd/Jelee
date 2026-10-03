//go:build !windows && !linux

package nfo

import (
	"os"
	"testing"
)

func TestNFONativeIdentityUnsupportedPlatform(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "owned")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := observeNFONativeIdentity(file); err != errNativeIdentity {
		t.Fatal("unsupported native identity must fail closed")
	}
}
