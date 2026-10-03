package nfo

import (
	"context"
	"encoding/hex"
	"os"
	"strings"
	"unicode"
)

// Both records are private preparation evidence. Neither authorizes target
// Rename, library writes, settlement, or recovery. Production has no caller yet.
type nfoCommitFilePlan struct {
	version        uint8
	token          [16]byte
	filename       string
	parent, target nfoNativeIdentity
}

type nfoCommitFiles struct {
	plan             nfoCommitFilePlan
	output, rollback nfoNativeIdentity
}

func (nfoCommitFilePlan) String() string   { return "nfo commit file plan (redacted)" }
func (nfoCommitFilePlan) GoString() string { return "nfo commit file plan (redacted)" }
func (nfoCommitFiles) String() string      { return "nfo commit files (redacted)" }
func (nfoCommitFiles) GoString() string    { return "nfo commit files (redacted)" }

func (p nfoCommitFilePlan) names() [5]string {
	base := ".jelee-nfo-commit-" + hex.EncodeToString(p.token[:])
	return [5]string{base + "-original-pin", base + "-output", base + "-output-pin", base + "-rollback", base + "-rollback-pin"}
}

type nfoCommitFilePersistence struct {
	// Callbacks must return only after committing their short database transaction.
	// An error can mean an unknown commit outcome. No caller receives write power.
	plan  func(context.Context, nfoCommitFilePlan) error
	ready func(context.Context, nfoCommitFiles) error
}

// prepareNFOCommitFiles records the bounded names before creating any sidecar,
// stage, or hardlink. It holds the native target lock while checking originals
// and creating retained witnesses. It never renames the target or rotates backups.
// Successful preparation retains all five names for future guarded settlement.
func prepareNFOCommitFiles(ctx context.Context, directory *os.Root, filename string, original, replacement *Document, token [16]byte, persist nfoCommitFilePersistence, ops nfoWriteOperations) (result *nfoCommitFiles, resultErr error) {
	if ctx == nil || directory == nil || token == ([16]byte{}) || !IsNFOName(filename) || strings.ContainsAny(filename, "/\\:") || strings.ContainsFunc(filename, unicode.IsControl) || persist.plan == nil || persist.ready == nil || ops.syncFile == nil || ops.syncDirectory == nil {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ops.documentsValidated {
		if err := validateNFOWriteDocuments(ctx, original, replacement); err != nil {
			return nil, err
		}
	}
	parent, err := nativeIdentityWithin(directory, ".")
	if err != nil {
		return nil, err
	}
	info, err := verifyNFOOriginal(ctx, directory, filename, original.original, nil)
	if err != nil {
		return nil, err
	}
	target, err := nativeIdentityWithinInfo(directory, filename, info)
	if err != nil {
		return nil, err
	}
	plan := nfoCommitFilePlan{version: 1, token: token, filename: filename, parent: parent, target: target}
	if err := persist.plan(ctx, plan); err != nil {
		return nil, ErrReplace
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ops.checkSource != nil {
		if err := ops.checkSource(ctx); err != nil {
			return nil, err
		}
	}
	lock, err := lockNFOFile(ctx, directory, filename)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := lock.Close(); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	if current, err := nativeIdentityWithin(directory, "."); err != nil || current != parent {
		return nil, ErrChanged
	}
	if _, err := verifyNFOOriginal(ctx, directory, filename, original.original, info); err != nil {
		return nil, err
	}
	if current, err := nativeIdentityWithin(directory, filename); err != nil || current != target {
		return nil, ErrChanged
	}
	if ops.checkSource != nil {
		if err := ops.checkSource(ctx); err != nil {
			return nil, err
		}
	}
	names := plan.names()
	owned := make(map[string]os.FileInfo, len(names))
	retain := false
	defer func() {
		if !retain {
			for name, identity := range owned {
				removeOwnedNFOStage(directory, name, identity)
			}
		}
	}()
	link := func(from, to string, expected os.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := directory.Link(from, to); err != nil {
			return ErrReplace
		}
		// If this observation itself fails, retain the planned name; never unlink
		// an object for which creation ownership cannot be established.
		actual, err := directory.Lstat(to)
		if err != nil || !regularNFOFile(actual) {
			return ErrChanged
		}
		owned[to] = actual
		if !os.SameFile(expected, actual) {
			return ErrChanged
		}
		return nil
	}
	if err := link(filename, names[0], info); err != nil {
		return nil, err
	}
	stage := func(name string, data []byte) (os.FileInfo, error) {
		file, err := directory.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return nil, ErrReplace
		}
		defer file.Close()
		created, err := file.Stat()
		if err != nil || !regularNFOFile(created) {
			return nil, ErrReplace
		}
		owned[name] = created
		for len(data) > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			chunk := min(len(data), 32<<10)
			n, err := file.Write(data[:chunk])
			if err != nil || n != chunk {
				return nil, ErrReplace
			}
			data = data[n:]
		}
		if err := ops.syncFile(file); err != nil {
			return nil, ErrReplace
		}
		final, err := file.Stat()
		if err != nil || !regularNFOFile(final) || !os.SameFile(created, final) || file.Close() != nil {
			return nil, ErrReplace
		}
		return final, nil
	}
	output, err := stage(names[1], replacement.original)
	if err != nil {
		return nil, err
	}
	if err := link(names[1], names[2], output); err != nil {
		return nil, err
	}
	rollback, err := stage(names[3], original.original)
	if err != nil {
		return nil, err
	}
	if err := link(names[3], names[4], rollback); err != nil {
		return nil, err
	}
	outputID, err := nativeIdentityWithin(directory, names[1])
	if err != nil {
		return nil, err
	}
	rollbackID, err := nativeIdentityWithin(directory, names[3])
	if err != nil {
		return nil, err
	}
	files := &nfoCommitFiles{plan: plan, output: outputID, rollback: rollbackID}
	if err := verifyNFOCommitFiles(ctx, directory, *files, original.original, replacement.original); err != nil {
		return nil, err
	}
	if !lock.check() {
		return nil, ErrFileLock
	}
	if err := ops.syncDirectory(directory); err != nil {
		return nil, ErrReplace
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Keep all recovery evidence before attempting persistence: a timeout or
	// error after the DB committed must not destroy the records' witnesses.
	retain = true
	if err := persist.ready(ctx, *files); err != nil {
		return files, ErrReplace
	}
	if err := ctx.Err(); err != nil {
		return files, err
	}
	return files, nil
}

func nativeIdentityWithin(directory *os.Root, name string) (nfoNativeIdentity, error) {
	return nativeIdentityWithinInfo(directory, name, nil)
}

func nativeIdentityWithinInfo(directory *os.Root, name string, expected os.FileInfo) (nfoNativeIdentity, error) {
	info, err := directory.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !regularNFOFile(info) {
		return nfoNativeIdentity{}, ErrChanged
	}
	file, err := directory.OpenFile(name, readOnlyFlags(), 0)
	if err != nil {
		return nfoNativeIdentity{}, ErrChanged
	}
	defer file.Close()
	held, err := file.Stat()
	if err != nil || !os.SameFile(info, held) || expected != nil && !sameSourceInfo(expected, held) {
		return nfoNativeIdentity{}, ErrChanged
	}
	identity, err := observeNFONativeIdentity(file)
	if err != nil {
		return nfoNativeIdentity{}, err
	}
	current, err := directory.Lstat(name)
	if err != nil || !os.SameFile(held, current) || current.Mode()&os.ModeSymlink != 0 {
		return nfoNativeIdentity{}, ErrChanged
	}
	return identity, nil
}

// Verification is read-only and requires every planned pre-commit artifact.
// A missing stage after Rename is not interpreted as a successful commit.
func verifyNFOCommitFiles(ctx context.Context, directory *os.Root, files nfoCommitFiles, original, replacement []byte) error {
	if ctx == nil || directory == nil {
		return ErrInvalidInput
	}
	if files.plan.version != 1 || files.plan.token == ([16]byte{}) || !IsNFOName(files.plan.filename) || strings.ContainsAny(files.plan.filename, "/\\:") || strings.ContainsFunc(files.plan.filename, unicode.IsControl) {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if current, err := nativeIdentityWithin(directory, "."); err != nil || current != files.plan.parent {
		return ErrChanged
	}
	names := files.plan.names()
	for _, candidate := range []struct {
		name string
		id   nfoNativeIdentity
		data []byte
	}{{files.plan.filename, files.plan.target, original}, {names[0], files.plan.target, original}, {names[1], files.output, replacement}, {names[2], files.output, replacement}, {names[3], files.rollback, original}, {names[4], files.rollback, original}} {
		observed, err := verifyNFOOriginal(ctx, directory, candidate.name, candidate.data, nil)
		if err != nil {
			return err
		}
		if current, err := nativeIdentityWithinInfo(directory, candidate.name, observed); err != nil || current != candidate.id {
			return ErrChanged
		}
	}
	return nil
}
