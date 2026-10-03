//go:build !windows && !linux

package nfo

import "os"

func observeNFONativeIdentity(*os.File) (nfoNativeIdentity, error) {
	return nfoNativeIdentity{}, errNativeIdentity
}
