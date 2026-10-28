package nfo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"unicode"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ErrTargetAppeared reports that the NFO name to create exists (or appeared
// after the preparation observed it absent). Nothing at that name was touched;
// the entry is rejected and its token concluded without a target.
var ErrTargetAppeared = errors.New("nfo_target_appeared")

// A create has no original and no rollback object. It stages the replacement as
// the token's output (and one pin of it), then settles by linking the output to
// the absent target name. The target name is created by link(2) alone: the link
// fails when the name exists, so no existing object can be replaced.

// nfoCreateFilesSound is the plan of a create: version 2, no target identity.
func nfoCreateFilesSound(files nfoCommitFiles) bool {
	return files.plan.create() && files.plan.target == (nfoNativeIdentity{}) && files.rollback == (nfoNativeIdentity{}) && validNFOCommitProgress(files)
}

// nfoCreateTargetState is what the target name is under the held parent.
type nfoCreateTargetState uint8

const (
	nfoCreateTargetAbsent nfoCreateTargetState = iota
	// nfoCreateTargetOurs is the token's own output object.
	nfoCreateTargetOurs
	// nfoCreateTargetForeign is any other object, a symlink included.
	nfoCreateTargetForeign
)

// observeNFOCreateTarget never follows or modifies the name. An observation
// that is neither absence nor an object is an error: it is not interpreted.
func observeNFOCreateTarget(directory *os.Root, filename string, output nfoNativeIdentity) (nfoCreateTargetState, error) {
	if _, err := directory.Lstat(filename); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nfoCreateTargetAbsent, nil
		}
		return nfoCreateTargetForeign, ErrChanged
	}
	if current, err := nativeIdentityWithin(directory, filename); err == nil && current == output {
		return nfoCreateTargetOurs, nil
	}
	return nfoCreateTargetForeign, nil
}

// prepareNFOCreateFiles stages the output of a create. The order mirrors the
// rewrite staging: the plan is saved before any file effect (the lock sidecar
// included), then under the native lock the parent identity and the absence of
// the target are checked again before the output is created with O_EXCL,
// written and synced, pinned by a hardlink, and its identity and the single
// checkpoint are saved. A target that exists at any of those checks refuses
// the whole stage with ErrTargetAppeared and nothing is created.
func prepareNFOCreateFiles(ctx context.Context, directory *os.Root, filename string, replacement *Document, token [16]byte, persist nfoCommitFilePersistence, ops nfoWriteOperations) (result *nfoCommitFiles, resultErr error) {
	if persist.attempt > maxNFOCommitAttempts || ctx == nil || directory == nil || replacement == nil || token == ([16]byte{}) || !IsNFOName(filename) || strings.ContainsAny(filename, "/\\:") || strings.ContainsFunc(filename, unicode.IsControl) || persist.plan == nil || persist.ready == nil || persist.progress == nil || ops.syncFile == nil || ops.syncDirectory == nil {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ops.documentsValidated {
		if err := validateNFOWriteDocuments(ctx, replacement); err != nil {
			return nil, err
		}
	}
	parent, err := nativeIdentityWithin(directory, ".")
	if err != nil {
		return nil, err
	}
	plan := nfoCommitFilePlan{version: domain.NFOWriteCommitCreatePlanVersion, token: token, filename: filename, parent: parent, attempt: persist.attempt}
	if persist.resume != nil {
		saved := persist.resume
		if saved.plan != plan || !nfoCreateFilesSound(*saved) {
			return nil, ErrChanged
		}
	}
	// A target present now needs no plan and no lock: refuse before any effect.
	if state, err := observeNFOCreateTarget(directory, filename, nfoNativeIdentity{}); err != nil || state != nfoCreateTargetAbsent {
		if err == nil {
			err = ErrTargetAppeared
		}
		return nil, err
	}
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
	if state, err := observeNFOCreateTarget(directory, filename, nfoNativeIdentity{}); err != nil || state != nfoCreateTargetAbsent {
		if err == nil {
			err = ErrTargetAppeared
		}
		return nil, err
	}
	if ops.checkSource != nil {
		if err := ops.checkSource(ctx); err != nil {
			return nil, err
		}
	}
	names := plan.names()
	owned := make(map[string]os.FileInfo, 2)
	retain := persist.resume != nil
	defer func() {
		if !retain {
			for name, identity := range owned {
				removeOwnedNFOStage(directory, name, identity)
			}
		}
	}()
	var outputID nfoNativeIdentity
	if persist.resume != nil {
		if err := verifyNFOCreateFiles(ctx, directory, *persist.resume, replacement.original); err != nil {
			return nil, err
		}
		outputID = persist.resume.output
	} else {
		file, err := directory.OpenFile(names[1], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, ErrReplace
		}
		if ops.created != nil {
			ops.created()
		}
		created, err := stageNFOCreateOutput(ctx, file, replacement.original, ops)
		if err != nil {
			// The file was created by this call under an exclusive open.
			if info, statErr := directory.Lstat(names[1]); statErr == nil && regularNFOFile(info) {
				owned[names[1]] = info
			}
			return nil, err
		}
		owned[names[1]] = created
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := directory.Link(names[1], names[2]); err != nil {
			return nil, ErrReplace
		}
		// If this observation itself fails, retain the planned name; never unlink
		// an object for which creation ownership cannot be established.
		pin, err := directory.Lstat(names[2])
		if err != nil || !regularNFOFile(pin) {
			return nil, ErrChanged
		}
		owned[names[2]] = pin
		if !os.SameFile(created, pin) {
			return nil, ErrChanged
		}
		outputID, err = nativeIdentityWithin(directory, names[1])
		if err != nil {
			return nil, err
		}
	}
	files := &nfoCommitFiles{plan: plan, output: outputID}
	if !nfoCreateFilesSound(*files) {
		return nil, ErrChanged
	}
	if err := verifyNFOCreateFiles(ctx, directory, *files, replacement.original); err != nil {
		return nil, err
	}
	if ops.checkSource != nil {
		if err := ops.checkSource(ctx); err != nil {
			return nil, err
		}
	}
	if !lock.check() {
		return nil, ErrFileLock
	}
	if err := ops.syncDirectory(directory); err != nil {
		return nil, ErrReplace
	}
	// Even an error can follow a committed checkpoint. Preserve the witnesses
	// before the callback.
	retain = true
	if err := persist.progress(ctx, *files); err != nil {
		return nil, ErrReplace
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !lock.check() {
		return nil, ErrFileLock
	}
	if err := persist.ready(ctx, *files); err != nil {
		return files, ErrReplace
	}
	if err := ctx.Err(); err != nil {
		return files, err
	}
	return files, nil
}

// stageNFOCreateOutput writes and syncs the exclusively created output file.
func stageNFOCreateOutput(ctx context.Context, file *os.File, data []byte, ops nfoWriteOperations) (os.FileInfo, error) {
	defer func() { _ = file.Close() }()
	created, err := file.Stat()
	if err != nil || !regularNFOFile(created) {
		return nil, ErrReplace
	}
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return created, err
		}
		chunk := min(len(data), 32<<10)
		n, err := file.Write(data[:chunk])
		if err != nil || n != chunk {
			return created, ErrReplace
		}
		data = data[n:]
	}
	if err := ops.syncFile(file); err != nil {
		return created, ErrReplace
	}
	final, err := file.Stat()
	if err != nil || !regularNFOFile(final) || !os.SameFile(created, final) || file.Close() != nil {
		return created, ErrReplace
	}
	return final, nil
}

// verifyNFOCreateFiles is read-only: the parent is the planned directory, the
// target name and every name a create never uses are absent, and the output and
// its pin are the recorded object with the replacement bytes.
func verifyNFOCreateFiles(ctx context.Context, directory *os.Root, files nfoCommitFiles, replacement []byte) error {
	if ctx == nil || directory == nil || !nfoCreateFilesSound(files) || len(replacement) == 0 || int64(len(replacement)) > MaxAllowedBytes {
		return ErrInvalidInput
	}
	// A target that exists is the reportable case, found before anything else.
	if _, err := directory.Lstat(files.plan.filename); err == nil {
		return ErrTargetAppeared
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ErrChanged
	}
	names := files.plan.names()
	return verifyNFOSettleObjects(ctx, directory, files.plan.parent, []string{files.plan.filename, names[0], names[3], names[4]}, []nfoSettleObject{
		{names[1], files.output, replacement},
		{names[2], files.output, replacement},
	})
}

// verifyNFOCreated is verifyNFOCreateFiles after the link: the target is the
// output object too, so all three names are one object.
func verifyNFOCreated(ctx context.Context, directory *os.Root, files nfoCommitFiles, replacement []byte) error {
	if ctx == nil || directory == nil || !nfoCreateFilesSound(files) || len(replacement) == 0 || int64(len(replacement)) > MaxAllowedBytes {
		return ErrInvalidInput
	}
	names := files.plan.names()
	return verifyNFOSettleObjects(ctx, directory, files.plan.parent, []string{names[0], names[3], names[4]}, []nfoSettleObject{
		{files.plan.filename, files.output, replacement},
		{names[1], files.output, replacement},
		{names[2], files.output, replacement},
	})
}

// settleNFOCreateFiles links the staged output to the absent target. backedUp
// tells that the durable phase 1 exists: only then may the target already be
// the output (a link of an earlier call). The target is never opened for
// writing, renamed over or removed here except through rollbackNFOCreate, and
// that only for the token's own output.
//
// ErrTargetAppeared means the name exists as another object (found before or
// refused by the link): nothing was changed. With ErrInvalidInput, ErrChanged,
// ErrFileLock, a context error or ErrReplace before the link nothing was
// created and the target is still absent. After the link a failure is undone
// by rollbackNFOCreate (ErrRolledBack) or leaves an indeterminate target
// (ErrRollback, ErrChanged) that needs a person.
func settleNFOCreateFiles(ctx context.Context, directory *os.Root, files nfoCommitFiles, replacement []byte, backedUp bool, progress nfoSettleProgress, ops nfoWriteOperations) (phase nfoSettlePhase, resultErr error) {
	if ctx == nil || directory == nil || ops.link == nil || ops.syncDirectory == nil || !nfoCreateFilesSound(files) || len(replacement) == 0 || int64(len(replacement)) > MaxAllowedBytes {
		return nfoSettleUnchanged, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nfoSettleUnchanged, err
	}
	lock, err := lockNFOFile(ctx, directory, files.plan.filename)
	if err != nil {
		return nfoSettleUnchanged, err
	}
	defer func() {
		if err := lock.Close(); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	if current, err := nativeIdentityWithin(directory, "."); err != nil || current != files.plan.parent {
		return nfoSettleUnchanged, ErrChanged
	}
	state, err := observeNFOCreateTarget(directory, files.plan.filename, files.output)
	if err != nil {
		return nfoSettleUnchanged, err
	}
	switch state {
	case nfoCreateTargetForeign:
		return nfoSettleUnchanged, ErrTargetAppeared
	case nfoCreateTargetOurs:
		// Only this protocol links the output, and only after the durable phase.
		if !backedUp {
			return nfoSettleUnchanged, ErrChanged
		}
		return resumeNFOCreated(ctx, directory, files, replacement, progress, ops, lock)
	}
	if err := verifyNFOCreateFiles(ctx, directory, files, replacement); err != nil {
		return nfoSettleUnchanged, err
	}
	if ops.checkSource != nil {
		if err := ops.checkSource(ctx); err != nil {
			return nfoSettleUnchanged, err
		}
	}
	// The durable phase 1 precedes the link: without it this token never linked.
	if progress != nil {
		if err := progress(ctx, nfoSettleBackedUp); err != nil {
			return nfoSettleUnchanged, ErrReplace
		}
	}
	if err := ctx.Err(); err != nil {
		return nfoSettleUnchanged, err
	}
	if ops.checkSource != nil {
		if err := ops.checkSource(ctx); err != nil {
			return nfoSettleUnchanged, err
		}
	}
	if !lock.check() {
		return nfoSettleUnchanged, ErrFileLock
	}
	if err := verifyNFOCreateFiles(ctx, directory, files, replacement); err != nil {
		return nfoSettleUnchanged, err
	}
	if err := ops.link(directory, files.plan.names()[1], files.plan.filename); err != nil {
		// Trust the observed target, not the reported error.
		after, obsErr := observeNFOCreateTarget(directory, files.plan.filename, files.output)
		switch {
		case obsErr != nil:
			return nfoSettleUnchanged, obsErr
		case after == nfoCreateTargetAbsent:
			return nfoSettleUnchanged, ErrReplace
		case after == nfoCreateTargetForeign:
			return nfoSettleUnchanged, ErrTargetAppeared
		}
	}
	// After the link, cancellation cannot skip durable commit or rollback.
	if err := ops.syncDirectory(directory); err != nil {
		return rollbackNFOCreate(directory, files, replacement, ops) //nolint:contextcheck // cleanup after an irrevocable step must finish even when the caller's context is cancelled
	}
	if err := verifyNFOCreated(context.Background(), directory, files, replacement); err != nil { //nolint:contextcheck // cleanup after an irrevocable step must finish even when the caller's context is cancelled
		return rollbackNFOCreate(directory, files, replacement, ops) //nolint:contextcheck // cleanup after an irrevocable step must finish even when the caller's context is cancelled
	}
	if progress != nil {
		if err := progress(ctx, nfoSettleReplaced); err != nil {
			return nfoSettleReplaced, ErrReplace
		}
	}
	return nfoSettleReplaced, nil
}

// A previous invocation linked the output. Complete it without another link:
// verify every object, sync and checkpoint again.
func resumeNFOCreated(ctx context.Context, directory *os.Root, files nfoCommitFiles, replacement []byte, progress nfoSettleProgress, ops nfoWriteOperations, lock *nfoFileLock) (nfoSettlePhase, error) {
	if err := verifyNFOCreated(ctx, directory, files, replacement); err != nil {
		return nfoSettleIndeterminate, err
	}
	if !lock.check() {
		return nfoSettleIndeterminate, ErrFileLock
	}
	if err := ops.syncDirectory(directory); err != nil {
		return nfoSettleReplaced, ErrReplace
	}
	if progress != nil {
		if err := progress(ctx, nfoSettleReplaced); err != nil {
			return nfoSettleReplaced, ErrReplace
		}
	}
	return nfoSettleReplaced, nil
}

// rollbackNFOCreate removes the target name only while it is provably the
// token's output: the same native identity and exactly the replacement bytes.
// An object that was replaced or edited in place is left alone (ErrChanged).
// The caller holds the native lock. Output and pin remain for cleanup.
func rollbackNFOCreate(directory *os.Root, files nfoCommitFiles, replacement []byte, ops nfoWriteOperations) (nfoSettlePhase, error) {
	filename := files.plan.filename
	if current, err := nativeIdentityWithin(directory, filename); err != nil || current != files.output {
		return nfoSettleIndeterminate, ErrChanged
	}
	if _, err := verifyNFOOriginal(context.Background(), directory, filename, replacement, nil); err != nil {
		return nfoSettleIndeterminate, ErrChanged
	}
	if err := directory.Remove(filename); err != nil {
		return nfoSettleIndeterminate, ErrRollback
	}
	if err := ops.syncDirectory(directory); err != nil {
		return nfoSettleIndeterminate, ErrRollback
	}
	if _, err := directory.Lstat(filename); !errors.Is(err, fs.ErrNotExist) {
		return nfoSettleIndeterminate, ErrRollback
	}
	return nfoSettleRolledBack, ErrRolledBack
}

// abortNFOCreateFiles concludes a create without linking. An absent target
// stays absent, the token's own unchanged output is unlinked from the target
// name, and anything else (a foreign object, or the output edited in place) is
// refused with ErrChanged and left untouched. It returns nfoSettleRolledBack
// with nil once the target is not the token's output.
func abortNFOCreateFiles(ctx context.Context, directory *os.Root, files nfoCommitFiles, replacement []byte, ops nfoWriteOperations) (phase nfoSettlePhase, resultErr error) {
	if ctx == nil || directory == nil || ops.syncDirectory == nil || !nfoCreateFilesSound(files) || len(replacement) == 0 || int64(len(replacement)) > MaxAllowedBytes {
		return nfoSettleUnchanged, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nfoSettleUnchanged, err
	}
	lock, err := lockNFOFile(ctx, directory, files.plan.filename)
	if err != nil {
		return nfoSettleUnchanged, err
	}
	defer func() {
		if err := lock.Close(); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	if current, err := nativeIdentityWithin(directory, "."); err != nil || current != files.plan.parent {
		return nfoSettleIndeterminate, ErrChanged
	}
	state, err := observeNFOCreateTarget(directory, files.plan.filename, files.output)
	if err != nil {
		return nfoSettleIndeterminate, err
	}
	switch state {
	case nfoCreateTargetAbsent:
		// This token never linked, or an earlier abort already unlinked it.
		if err := ops.syncDirectory(directory); err != nil {
			return nfoSettleIndeterminate, ErrRollback
		}
		return nfoSettleRolledBack, nil
	case nfoCreateTargetOurs:
		if !lock.check() {
			return nfoSettleIndeterminate, ErrFileLock
		}
		if phase, err := rollbackNFOCreate(directory, files, replacement, ops); phase != nfoSettleRolledBack { //nolint:contextcheck // cleanup after an irrevocable step must finish even when the caller's context is cancelled
			return phase, err
		}
		return nfoSettleRolledBack, nil
	}
	return nfoSettleIndeterminate, ErrChanged
}
