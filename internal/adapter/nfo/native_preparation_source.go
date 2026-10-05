package nfo

import (
	"context"
	"os"
	"path/filepath"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type nativeAncestorHandle struct {
	root *os.Root
	info os.FileInfo
	id   nfoNativeIdentity
}

// Verify the persisted first observations without allocating another XML copy.
// The caller still verifies original bytes separately through its held source.
func verifyNativePreparationScope(ctx context.Context, scope domain.NFOItemScope, expected domain.NFONativePreparationReceipt) error {
	if expected.Empty() || domain.ValidateNFONativePreparationReceipt(expected) != nil {
		return ErrInvalidInput
	}
	checkpoint := func(context.Context, string, string, int64) (*Source, error) {
		return &Source{nativeObserved: true, nativeRoot: nfoNativeIdentity{record: expected.RootIdentity()}, nativeFile: nfoNativeIdentity{record: expected.NFOFileIdentity()}}, nil
	}
	_, observed, err := readNativePreparationSourceWith(ctx, scope, 1, checkpoint)
	if err != nil {
		return err
	}
	if !expected.Equal(observed) {
		return ErrChanged
	}
	return nil
}

// readNativePreparationSource keeps the ancestor chain and media handles open
// while original NFO bytes are read. Rechecks are observations, not an atomic
// filesystem snapshot or permission to commit a later filesystem mutation.
func readNativePreparationSource(ctx context.Context, scope domain.NFOItemScope, maxBytes int64) (*Source, domain.NFONativePreparationReceipt, error) {
	return readNativePreparationSourceWith(ctx, scope, maxBytes, readNativeSource)
}

func readNativePreparationSourceWith(ctx context.Context, scope domain.NFOItemScope, maxBytes int64, read func(context.Context, string, string, int64) (*Source, error)) (source *Source, receipt domain.NFONativePreparationReceipt, resultErr error) {
	if ctx == nil || read == nil || maxBytes < 1 || maxBytes > MaxAllowedBytes {
		return nil, receipt, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, receipt, err
	}
	plan, err := planNativePreparationPaths(scope)
	if err != nil {
		return nil, receipt, err
	}
	handles := make(map[string]nativeAncestorHandle, len(plan.ancestors))
	var opened []*os.Root
	var mediaFile *os.File
	defer func() {
		closeFailed := false
		if mediaFile != nil && mediaFile.Close() != nil {
			closeFailed = true
		}
		for i := len(opened) - 1; i >= 0; i-- {
			if opened[i].Close() != nil {
				closeFailed = true
			}
		}
		if closeFailed {
			source, receipt, resultErr = nil, domain.NFONativePreparationReceipt{}, errNativeIdentity
		}
	}()
	ancestors := make([][48]byte, 0, len(plan.ancestors))
	for i, path := range plan.ancestors {
		if err := ctx.Err(); err != nil {
			return nil, receipt, err
		}
		var before os.FileInfo
		var root *os.Root
		if i == 0 {
			before, err = os.Lstat(path)
			if err == nil && nativeDirectoryInfo(before) {
				root, err = os.OpenRoot(path)
			}
		} else {
			parent, ok := handles[filepath.Dir(path)]
			if !ok {
				return nil, receipt, errNativeIdentity
			}
			before, err = parent.root.Lstat(filepath.Base(path))
			if err == nil && nativeDirectoryInfo(before) {
				root, err = parent.root.OpenRoot(filepath.Base(path))
			}
		}
		if err != nil || root == nil {
			return nil, receipt, errNativeIdentity
		}
		opened = append(opened, root)
		id, info, err := observeNativeDirectoryRoot(root)
		if err != nil || !os.SameFile(before, info) {
			return nil, receipt, errNativeIdentity
		}
		handles[path] = nativeAncestorHandle{root: root, info: info, id: id}
		ancestors = append(ancestors, id.record)
	}
	configured, ok := handles[plan.root]
	if !ok {
		return nil, receipt, errNativeIdentity
	}
	var mediaID nfoNativeIdentity
	var mediaInfo os.FileInfo
	if plan.mediaKind == 2 {
		media, ok := handles[plan.media]
		if !ok {
			return nil, receipt, errNativeIdentity
		}
		mediaID, mediaInfo = media.id, media.info
	} else {
		parent, ok := handles[filepath.Dir(plan.media)]
		if !ok {
			return nil, receipt, errNativeIdentity
		}
		mediaFile, mediaID, mediaInfo, err = openNativeRegularFile(parent.root, filepath.Base(plan.media))
		if err != nil {
			return nil, receipt, err
		}
	}
	source, err = read(ctx, scope.Source.RootPath, scope.Source.RelativePath, maxBytes)
	if err != nil {
		return nil, receipt, err
	}
	if source == nil || !source.nativeObserved || source.nativeRoot != configured.id {
		return nil, receipt, ErrChanged
	}
	// The NFO identity in the receipt comes from the handle supplying original
	// bytes. This second handle only verifies the current scoped directory entry.
	nfoParent, ok := handles[filepath.Dir(plan.nfo)]
	if !ok {
		return nil, receipt, errNativeIdentity
	}
	current, currentID, _, err := openNativeRegularFile(nfoParent.root, filepath.Base(plan.nfo))
	if err != nil {
		return nil, receipt, err
	}
	closeErr := current.Close()
	if closeErr != nil {
		return nil, receipt, errNativeIdentity
	}
	if currentID != source.nativeFile {
		return nil, receipt, ErrChanged
	}
	for i, path := range plan.ancestors {
		if err := ctx.Err(); err != nil {
			return nil, receipt, err
		}
		held := handles[path]
		lastID, lastInfo, err := observeNativeDirectoryRoot(held.root)
		if err != nil || lastID != held.id || !os.SameFile(held.info, lastInfo) {
			return nil, receipt, ErrChanged
		}
		var entry os.FileInfo
		if i == 0 {
			entry, err = os.Lstat(path)
		} else {
			entry, err = handles[filepath.Dir(path)].root.Lstat(filepath.Base(path))
		}
		if err != nil || !nativeDirectoryInfo(entry) || !os.SameFile(held.info, entry) {
			return nil, receipt, ErrChanged
		}
	}
	if mediaFile != nil {
		lastID, err := observeNFONativeIdentity(mediaFile)
		if err != nil || lastID != mediaID {
			return nil, receipt, ErrChanged
		}
		parent := handles[filepath.Dir(plan.media)]
		current, currentID, currentInfo, err := openNativeRegularFile(parent.root, filepath.Base(plan.media))
		if err != nil {
			return nil, receipt, err
		}
		closeErr := current.Close()
		if closeErr != nil {
			return nil, receipt, errNativeIdentity
		}
		if currentID != mediaID || !os.SameFile(mediaInfo, currentInfo) {
			return nil, receipt, ErrChanged
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, receipt, err
	}
	receipt, err = domain.NewNFONativePreparationReceipt(configured.id.record, mediaID.record, source.nativeFile.record, ancestors)
	if err != nil {
		return nil, domain.NFONativePreparationReceipt{}, err
	}
	return source, receipt, nil
}

func nativeDirectoryInfo(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0
}

func observeNativeDirectoryRoot(root *os.Root) (nfoNativeIdentity, os.FileInfo, error) {
	file, err := root.OpenFile(".", readOnlyFlags(), 0)
	if err != nil {
		return nfoNativeIdentity{}, nil, errNativeIdentity
	}
	id, observeErr := observeNFONativeIdentity(file)
	info, statErr := file.Stat()
	closeErr := file.Close()
	if observeErr != nil || statErr != nil || closeErr != nil || id.record[2] != 2 || !nativeDirectoryInfo(info) {
		return nfoNativeIdentity{}, nil, errNativeIdentity
	}
	return id, info, nil
}

func openNativeRegularFile(root *os.Root, name string) (*os.File, nfoNativeIdentity, os.FileInfo, error) {
	before, err := root.Lstat(name)
	if err != nil || before == nil || !before.Mode().IsRegular() {
		return nil, nfoNativeIdentity{}, nil, errNativeIdentity
	}
	file, err := root.OpenFile(name, readOnlyFlags(), 0)
	if err != nil {
		return nil, nfoNativeIdentity{}, nil, errNativeIdentity
	}
	id, observeErr := observeNFONativeIdentity(file)
	info, statErr := file.Stat()
	after, entryErr := root.Lstat(name)
	if observeErr != nil || statErr != nil || entryErr != nil || id.record[2] != 1 || info == nil || !info.Mode().IsRegular() || after == nil || !after.Mode().IsRegular() || !os.SameFile(before, info) || !os.SameFile(info, after) {
		_ = file.Close()
		return nil, nfoNativeIdentity{}, nil, errNativeIdentity
	}
	return file, id, info, nil
}
