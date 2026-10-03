package nfo

import (
	"context"
	"os"
)

// readNativeSource binds native observations to the very handle that supplies
// original bytes. Ordinary readonly reads do not require platform support.
// This is still a checkpoint observation, not filesystem execution authority.
func readNativeSource(ctx context.Context, rootAbs, relative string, maxBytes int64) (*Source, error) {
	access := diskSourceAccess()
	access.observeNative = observeNativeSourceHandles
	return readSource(ctx, rootAbs, relative, maxBytes, access)
}

func observeNativeSourceHandles(root sourceRoot, source sourceFile) (nfoNativeIdentity, nfoNativeIdentity, error) {
	disk, ok := root.(diskSourceRoot)
	file, fileOK := source.(*os.File)
	if !ok || !fileOK || disk.Root == nil {
		return nfoNativeIdentity{}, nfoNativeIdentity{}, errNativeIdentity
	}
	// Open the directory through the already-held os.Root, never its path again.
	dir, err := disk.Root.OpenFile(".", readOnlyFlags(), 0)
	if err != nil {
		return nfoNativeIdentity{}, nfoNativeIdentity{}, errNativeIdentity
	}
	rootID, rootErr := observeNFONativeIdentity(dir)
	closeErr := dir.Close()
	if rootErr != nil || closeErr != nil || rootID.record[2] != 2 {
		return nfoNativeIdentity{}, nfoNativeIdentity{}, errNativeIdentity
	}
	fileID, err := observeNFONativeIdentity(file)
	if err != nil || fileID.record[2] != 1 || rootID.record[1] != fileID.record[1] {
		return nfoNativeIdentity{}, nfoNativeIdentity{}, errNativeIdentity
	}
	return rootID, fileID, nil
}
