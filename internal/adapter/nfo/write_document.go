package nfo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"
)

var (
	ErrReplace  = errors.New("nfo_replace_failed")
	ErrRollback = errors.New("nfo_rollback_failed")
)

// These ports are invocation-local and private; production callers cannot
// replace file sync or rename. Tests exercise failures before and after commit.
type nfoWriteOperations struct {
	syncFile           func(*os.File) error
	rename             func(*os.Root, string, string) error
	syncDirectory      func(*os.Root) error
	checkSource        func(context.Context) error
	submitted          func()
	documentsValidated bool
}

func nativeNFOWriteOperations() nfoWriteOperations {
	return nfoWriteOperations{
		syncFile:      func(f *os.File) error { return f.Sync() },
		rename:        func(r *os.Root, from, to string) error { return r.Rename(from, to) },
		syncDirectory: syncNFODirectory,
	}
}

// replaceNFODocument operates only within a held, authorized parent directory.
// It replaces an existing regular NFO whose full bytes still match original.
// Library policy, item/root identity and field authorization belong to the
// future application writer; this private primitive grants no read-write mode.
func replaceNFODocument(ctx context.Context, directory *os.Root, filename string, original, replacement *Document, backups int) error {
	return replaceNFODocumentWithOperations(ctx, directory, filename, original, replacement, backups, nativeNFOWriteOperations())
}

func replaceNFODocumentWithOperations(ctx context.Context, directory *os.Root, filename string, original, replacement *Document, backups int, ops nfoWriteOperations) (resultErr error) {
	if ctx == nil || directory == nil || original == nil || replacement == nil || backups < 0 || backups > 16 || ops.syncFile == nil || ops.rename == nil || ops.syncDirectory == nil {
		return ErrInvalidInput
	}
	if !ops.documentsValidated {
		if err := validateNFOWriteDocuments(ctx, original, replacement); err != nil {
			return err
		}
	}
	lock, err := lockNFOFile(ctx, directory, filename)
	if err != nil {
		return err
	}
	defer func() {
		if err := lock.Close(); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	if ops.checkSource != nil {
		if err := ops.checkSource(ctx); err != nil {
			return err
		}
	}
	info, err := verifyNFOOriginal(ctx, directory, filename, original.original, nil)
	if err != nil {
		return err
	}
	// No-op writes keep the original inode and do not rotate backups.
	if bytes.Equal(original.original, replacement.original) {
		return nil
	}
	for i := 0; backups > 0 && i < 16; i++ {
		backupInfo, err := directory.Lstat(nfoBackupName(filename, i))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrReplace
		}
		if err == nil && !regularNFOFile(backupInfo) {
			return ErrReplace
		}
	}
	staged, stagedInfo, err := stageNFODocument(ctx, directory, replacement.original, info.Mode().Perm(), ops)
	if err != nil {
		return err
	}
	defer removeOwnedNFOStage(directory, staged, stagedInfo)
	rollback, rollbackInfo, err := stageNFODocument(ctx, directory, original.original, info.Mode().Perm(), ops)
	if err != nil {
		return err
	}
	// Keep recovery bytes if rollback fails, rather than deleting the last copy.
	retainRollback := false
	defer func() {
		if !retainRollback {
			removeOwnedNFOStage(directory, rollback, rollbackInfo)
		}
	}()
	if _, err = verifyNFOOriginal(ctx, directory, filename, original.original, info); err != nil {
		return err
	}
	if backups > 0 {
		backup, backupInfo, err := stageNFODocument(ctx, directory, original.original, info.Mode().Perm(), ops)
		if err != nil {
			return err
		}
		defer removeOwnedNFOStage(directory, backup, backupInfo)
		if ops.checkSource != nil {
			if err := ops.checkSource(ctx); err != nil {
				return err
			}
		}
		if !lock.check() {
			return ErrFileLock
		}
		if _, err := verifyNFOOriginal(ctx, directory, backup, original.original, backupInfo); err != nil {
			return err
		}
		for i := backups - 2; i >= 0; i-- {
			from := nfoBackupName(filename, i)
			if _, err := directory.Lstat(from); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return ErrReplace
			}
			if err := ops.rename(directory, from, nfoBackupName(filename, i+1)); err != nil {
				return ErrReplace
			}
		}
		if err := ops.rename(directory, backup, nfoBackupName(filename, 0)); err != nil {
			return ErrReplace
		}
		for i := backups; i < 16; i++ {
			if err := directory.Remove(nfoBackupName(filename, i)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return ErrReplace
			}
		}
		if err := ops.syncDirectory(directory); err != nil {
			return ErrReplace
		}
	}
	if _, err := verifyNFOOriginal(ctx, directory, filename, original.original, info); err != nil {
		return err
	}
	if !lock.check() {
		return ErrFileLock
	}
	if _, err := verifyNFOOriginal(ctx, directory, staged, replacement.original, stagedInfo); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ops.checkSource != nil {
		if err := ops.checkSource(ctx); err != nil {
			return err
		}
	}
	if err := ops.rename(directory, staged, filename); err != nil {
		return ErrReplace
	}
	// After rename, cancellation cannot skip durable commit or rollback.
	if err := ops.syncDirectory(directory); err == nil {
		return nil
	}
	current, err := verifyNFOOriginal(context.Background(), directory, filename, replacement.original, stagedInfo) //nolint:contextcheck // cleanup after an irrevocable step must finish even when the caller's context is cancelled
	if err != nil || !os.SameFile(stagedInfo, current) {
		retainRollback = true
		return ErrRollback
	}
	if _, err := verifyNFOOriginal(context.Background(), directory, rollback, original.original, rollbackInfo); err != nil { //nolint:contextcheck // cleanup after an irrevocable step must finish even when the caller's context is cancelled
		retainRollback = true
		return ErrRollback
	}
	if err := ops.rename(directory, rollback, filename); err != nil {
		retainRollback = true
		return ErrRollback
	}
	if err := ops.syncDirectory(directory); err != nil {
		return ErrRollback
	}
	return ErrReplace
}

func validateNFOWriteDocuments(ctx context.Context, documents ...*Document) error {
	for _, document := range documents {
		if document == nil || len(document.original) == 0 || int64(len(document.original)) > MaxAllowedBytes {
			return ErrInvalidInput
		}
		parsed, err := parseOriginal(ctx, document.original)
		if err != nil {
			return err
		}
		for _, issue := range parsed.Issues {
			if issue.Severity == "error" {
				return ErrInvalidInput
			}
		}
	}
	return nil
}

func nfoBackupName(filename string, index int) string {
	name := filename + ".jelee.bak"
	if index > 0 {
		name += "." + strconv.Itoa(index)
	}
	return name
}

func verifyNFOOriginal(ctx context.Context, directory *os.Root, filename string, expected []byte, identity os.FileInfo) (os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pathInfo, err := directory.Lstat(filename)
	if err != nil || !regularNFOFile(pathInfo) {
		return nil, ErrChanged
	}
	file, err := directory.OpenFile(filename, readOnlyFlags(), 0)
	if err != nil {
		return nil, ErrChanged
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !regularNFOFile(before) || !os.SameFile(before, pathInfo) || identity != nil && !sameSourceInfo(identity, before) || before.Size() != int64(len(expected)) {
		return nil, ErrChanged
	}
	data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx, file}, int64(len(expected))+1))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	after, statErr := file.Stat()
	current, pathErr := directory.Lstat(filename)
	if readErr != nil || statErr != nil || pathErr != nil || !regularNFOFile(current) || !sameSourceInfo(before, after) || !sameSourceInfo(before, current) || !bytes.Equal(data, expected) {
		return nil, ErrChanged
	}
	if err := file.Close(); err != nil {
		return nil, ErrReplace
	}
	return before, nil
}

func stageNFODocument(ctx context.Context, directory *os.Root, data []byte, mode os.FileMode, ops nfoWriteOperations) (name string, info os.FileInfo, err error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", nil, ErrReplace
	}
	name = ".jelee-nfo-stage-" + hex.EncodeToString(random[:])
	file, err := directory.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return "", nil, ErrReplace
	}
	info, err = file.Stat()
	if err != nil {
		_ = file.Close()
		return "", nil, ErrReplace
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			removeOwnedNFOStage(directory, name, info)
		}
	}()
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return name, info, err
		}
		chunk := len(data)
		if chunk > 32<<10 {
			chunk = 32 << 10
		}
		n, err := file.Write(data[:chunk])
		if err != nil || n != chunk {
			return name, info, ErrReplace
		}
		data = data[n:]
	}
	if err := ctx.Err(); err != nil {
		return name, info, err
	}
	if err := ops.syncFile(file); err != nil {
		return name, info, ErrReplace
	}
	finalInfo, err := file.Stat()
	if err != nil || !regularNFOFile(finalInfo) || !os.SameFile(info, finalInfo) {
		return name, info, ErrReplace
	}
	info = finalInfo
	if err := file.Close(); err != nil {
		return name, info, ErrReplace
	}
	complete = true
	return name, info, nil
}

func removeOwnedNFOStage(directory *os.Root, name string, info os.FileInfo) {
	if info == nil {
		return
	}
	current, err := directory.Lstat(name)
	if err == nil && regularNFOFile(current) && os.SameFile(info, current) {
		_ = directory.Remove(name)
	}
}
