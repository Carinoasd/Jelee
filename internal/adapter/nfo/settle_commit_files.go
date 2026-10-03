package nfo

import (
	"context"
	"errors"
	"os"
	"strings"
	"unicode"
)

// ErrRolledBack reports that the target was replaced and then durably restored
// to the prepared rollback object. ErrRollback means that restoration failed or
// could not be attempted safely; the target state then needs recovery.
var ErrRolledBack = errors.New("nfo_rolled_back")

// nfoSettlePhase names a settlement state derived from the filesystem. Phases
// are progress hints for persistence; settlement re-derives state on every call.
type nfoSettlePhase uint8

const (
	// The target is still (or again) the prepared original object.
	nfoSettleUnchanged nfoSettlePhase = iota
	// The newest backup is a link of the original witness, directory synced.
	nfoSettleBackedUp
	// The target is the prepared output object, directory synced.
	nfoSettleReplaced
	// The target is the prepared rollback object, directory synced.
	nfoSettleRolledBack
	// Rollback failed; the target may be the output or an unknown object.
	nfoSettleIndeterminate
)

// nfoSettleProgress is called while the native lock is held, after the phase
// is durable on disk. It must be idempotent: re-invocation reports reached
// phases again. An error can mean an unknown persistence outcome.
type nfoSettleProgress func(context.Context, nfoSettlePhase) error

// The evicted backup keeps the oldest retained backup reachable under a name
// owned by this settlement. It is retained for the future cleanup protocol.
func nfoSettleEvictedName(plan nfoCommitFilePlan) string {
	return strings.TrimSuffix(plan.names()[0], "-original-pin") + "-backup-evicted"
}

// settleNFOCommitFiles atomically replaces the target with prepared ready files.
// It renames only planned names and backup names, never unlinks an object it
// did not link, and retains every witness for a future cleanup protocol.
// Production has no caller yet; it grants no library or job authorization.
//
// With ErrInvalidInput, ErrChanged, ErrFileLock or a context error, nothing
// was modified (or this call restored what it changed) and the target remains
// the original object. ErrReplace before rename also leaves the target original;
// after a replaced checkpoint it is returned with nfoSettleReplaced.
func settleNFOCommitFiles(ctx context.Context, directory *os.Root, files nfoCommitFiles, original, replacement []byte, backups int, progress nfoSettleProgress, ops nfoWriteOperations) (phase nfoSettlePhase, resultErr error) {
	filename := files.plan.filename
	if ctx == nil || directory == nil || backups < 0 || backups > 16 || ops.rename == nil || ops.syncDirectory == nil || !validNFOCommitProgress(files) || files.rollback == (nfoNativeIdentity{}) || !IsNFOName(filename) || strings.ContainsAny(filename, "/\\:") || strings.ContainsFunc(filename, unicode.IsControl) || len(original) == 0 || len(replacement) == 0 || int64(len(original)) > MaxAllowedBytes || int64(len(replacement)) > MaxAllowedBytes {
		return nfoSettleUnchanged, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nfoSettleUnchanged, err
	}
	lock, err := lockNFOFile(ctx, directory, filename)
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
	target, err := nativeIdentityWithin(directory, filename)
	if err != nil {
		return nfoSettleUnchanged, ErrChanged
	}
	switch target {
	case files.plan.target:
	case files.output:
		return resumeNFOSettleReplaced(ctx, directory, files, original, replacement, progress, ops, lock)
	case files.rollback:
		return resumeNFOSettleRolledBack(ctx, directory, files, original, replacement, ops)
	default:
		return nfoSettleUnchanged, ErrChanged
	}
	if err := verifyNFOCommitFiles(ctx, directory, files, original, replacement); err != nil {
		return nfoSettleUnchanged, err
	}
	if ops.checkSource != nil {
		if err := ops.checkSource(ctx); err != nil {
			return nfoSettleUnchanged, err
		}
	}
	rotation, err := rotateNFOSettleBackups(ctx, directory, files, backups, ops, lock)
	if err != nil {
		return nfoSettleUnchanged, err
	}
	// Before the target is renamed, every failure restores rotated backups.
	committed := false
	defer func() {
		if !committed {
			rotation.undo(directory, ops)
		}
	}()
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
	if err := verifyNFOCommitFiles(ctx, directory, files, original, replacement); err != nil {
		return nfoSettleUnchanged, err
	}
	names := files.plan.names()
	if err := ops.rename(directory, names[1], filename); err != nil {
		// Trust the observed target, not the reported error.
		current, idErr := nativeIdentityWithin(directory, filename)
		if idErr == nil && current == files.plan.target {
			return nfoSettleUnchanged, ErrReplace
		}
		committed = true
		if idErr != nil || current != files.output {
			return nfoSettleIndeterminate, ErrRollback
		}
		return rollbackNFOSettle(directory, files, original, replacement, ops)
	}
	// After rename, cancellation cannot skip durable commit or rollback.
	committed = true
	if err := ops.syncDirectory(directory); err != nil {
		return rollbackNFOSettle(directory, files, original, replacement, ops)
	}
	if err := verifyNFOSettled(context.Background(), directory, files, original, replacement); err != nil {
		return rollbackNFOSettle(directory, files, original, replacement, ops)
	}
	if progress != nil {
		if err := progress(ctx, nfoSettleReplaced); err != nil {
			return nfoSettleReplaced, ErrReplace
		}
	}
	return nfoSettleReplaced, nil
}

// A previous invocation renamed output onto the target. Complete it without a
// second replacement: verify every retained object, sync and checkpoint again.
func resumeNFOSettleReplaced(ctx context.Context, directory *os.Root, files nfoCommitFiles, original, replacement []byte, progress nfoSettleProgress, ops nfoWriteOperations, lock *nfoFileLock) (nfoSettlePhase, error) {
	if err := verifyNFOSettled(ctx, directory, files, original, replacement); err != nil {
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

// A previous invocation restored the rollback object. The attempt concluded as
// rolled back; only directory durability is re-established.
func resumeNFOSettleRolledBack(ctx context.Context, directory *os.Root, files nfoCommitFiles, original, replacement []byte, ops nfoWriteOperations) (nfoSettlePhase, error) {
	if err := verifyNFORolledBack(ctx, directory, files, original, replacement); err != nil {
		return nfoSettleIndeterminate, err
	}
	if err := ops.syncDirectory(directory); err != nil {
		return nfoSettleIndeterminate, ErrRollback
	}
	return nfoSettleRolledBack, ErrRolledBack
}

// rollbackNFOSettle restores the prepared rollback object only while the target
// is still provably the prepared output; a foreign object is never replaced.
// Output bytes stay reachable through the output witness.
func rollbackNFOSettle(directory *os.Root, files nfoCommitFiles, original, replacement []byte, ops nfoWriteOperations) (nfoSettlePhase, error) {
	names := files.plan.names()
	if current, err := nativeIdentityWithin(directory, files.plan.filename); err != nil || current != files.output {
		return nfoSettleIndeterminate, ErrRollback
	}
	info, err := verifyNFOOriginal(context.Background(), directory, names[3], original, nil)
	if err != nil {
		return nfoSettleIndeterminate, ErrRollback
	}
	if current, err := nativeIdentityWithinInfo(directory, names[3], info); err != nil || current != files.rollback {
		return nfoSettleIndeterminate, ErrRollback
	}
	if err := ops.rename(directory, names[3], files.plan.filename); err != nil {
		return nfoSettleIndeterminate, ErrRollback
	}
	if err := ops.syncDirectory(directory); err != nil {
		return nfoSettleIndeterminate, ErrRollback
	}
	if err := verifyNFORolledBack(context.Background(), directory, files, original, replacement); err != nil {
		return nfoSettleIndeterminate, ErrRollback
	}
	return nfoSettleRolledBack, ErrRolledBack
}

type nfoSettleObject struct {
	name string
	id   nfoNativeIdentity
	data []byte
}

// verifyNFOSettleObjects is read-only. Absent names must not exist at all.
func verifyNFOSettleObjects(ctx context.Context, directory *os.Root, parent nfoNativeIdentity, absent []string, objects []nfoSettleObject) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if current, err := nativeIdentityWithin(directory, "."); err != nil || current != parent {
		return ErrChanged
	}
	for _, name := range absent {
		if _, err := directory.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return ErrChanged
		}
	}
	for _, object := range objects {
		info, err := verifyNFOOriginal(ctx, directory, object.name, object.data, nil)
		if err != nil {
			return err
		}
		if current, err := nativeIdentityWithinInfo(directory, object.name, info); err != nil || current != object.id {
			return ErrChanged
		}
	}
	return nil
}

func verifyNFOSettled(ctx context.Context, directory *os.Root, files nfoCommitFiles, original, replacement []byte) error {
	names := files.plan.names()
	return verifyNFOSettleObjects(ctx, directory, files.plan.parent, []string{names[1]}, []nfoSettleObject{
		{files.plan.filename, files.output, replacement},
		{names[0], files.plan.target, original},
		{names[2], files.output, replacement},
		{names[3], files.rollback, original},
		{names[4], files.rollback, original},
	})
}

func verifyNFORolledBack(ctx context.Context, directory *os.Root, files nfoCommitFiles, original, replacement []byte) error {
	names := files.plan.names()
	return verifyNFOSettleObjects(ctx, directory, files.plan.parent, []string{names[1], names[3]}, []nfoSettleObject{
		{files.plan.filename, files.rollback, original},
		{names[0], files.plan.target, original},
		{names[2], files.output, replacement},
		{names[4], files.rollback, original},
	})
}

// nfoSettleRotation records the backup changes made by one invocation so that
// a failure before the target rename can restore them in reverse order.
type nfoSettleRotation struct {
	renames [][2]string
	linked  string
	target  nfoNativeIdentity
}

// rotateNFOSettleBackups shifts the .jelee.bak series by rename only and links
// the original witness as the newest backup; original bytes are never rewritten.
// The backup evicted past the retention count moves to a settlement-owned name
// instead of being unlinked. Backups beyond the count are left untouched.
func rotateNFOSettleBackups(ctx context.Context, directory *os.Root, files nfoCommitFiles, backups int, ops nfoWriteOperations, lock *nfoFileLock) (*nfoSettleRotation, error) {
	rotation := &nfoSettleRotation{target: files.plan.target}
	if backups == 0 {
		return rotation, nil
	}
	filename := files.plan.filename
	newest := nfoBackupName(filename, 0)
	// A previous invocation completed this phase: the newest backup is already
	// the original object, which only this settlement links there.
	if current, err := nativeIdentityWithin(directory, newest); err == nil && current == files.plan.target {
		return rotation, nil
	}
	present := make([]bool, backups)
	for i := range present {
		info, err := directory.Lstat(nfoBackupName(filename, i))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !regularNFOFile(info) {
			return nil, ErrReplace
		}
		present[i] = true
	}
	// A retained eviction from an unfinished rotation is never overwritten.
	evicted := nfoSettleEvictedName(files.plan)
	if _, err := directory.Lstat(evicted); !errors.Is(err, os.ErrNotExist) {
		return nil, ErrChanged
	}
	if !lock.check() {
		return nil, ErrFileLock
	}
	complete := false
	defer func() {
		if !complete {
			rotation.undo(directory, ops)
		}
	}()
	move := func(from, to string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := directory.Lstat(to); !errors.Is(err, os.ErrNotExist) {
			return ErrChanged
		}
		if err := ops.rename(directory, from, to); err != nil {
			return ErrReplace
		}
		rotation.renames = append(rotation.renames, [2]string{from, to})
		return nil
	}
	if present[backups-1] {
		if err := move(nfoBackupName(filename, backups-1), evicted); err != nil {
			return nil, err
		}
	}
	for i := backups - 2; i >= 0; i-- {
		if present[i] {
			if err := move(nfoBackupName(filename, i), nfoBackupName(filename, i+1)); err != nil {
				return nil, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := directory.Lstat(newest); !errors.Is(err, os.ErrNotExist) {
		return nil, ErrChanged
	}
	if err := directory.Link(files.plan.names()[0], newest); err != nil {
		return nil, ErrReplace
	}
	rotation.linked = newest
	if current, err := nativeIdentityWithin(directory, newest); err != nil || current != files.plan.target {
		return nil, ErrChanged
	}
	if err := ops.syncDirectory(directory); err != nil {
		return nil, ErrReplace
	}
	complete = true
	return rotation, nil
}

// undo reverses recorded changes and stops at the first name it cannot prove,
// so a partial undo shifts backups but never overwrites or loses one.
func (r *nfoSettleRotation) undo(directory *os.Root, ops nfoWriteOperations) bool {
	if r == nil || r.linked == "" && len(r.renames) == 0 {
		return true
	}
	if r.linked != "" {
		// The link is a name this settlement created for the still-linked original.
		if current, err := nativeIdentityWithin(directory, r.linked); err != nil || current != r.target || directory.Remove(r.linked) != nil {
			return false
		}
		r.linked = ""
	}
	for i := len(r.renames) - 1; i >= 0; i-- {
		from, to := r.renames[i][0], r.renames[i][1]
		if _, err := directory.Lstat(from); !errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err := ops.rename(directory, to, from); err != nil {
			return false
		}
		r.renames = r.renames[:i]
	}
	return ops.syncDirectory(directory) == nil
}

// abortNFOCommitFiles concludes prepared files without replacement. It never
// performs a forward Rename: a target that is still the original stays as is,
// a target holding the prepared output is restored from the rollback object,
// and a completed rollback only re-establishes directory durability. Rotated
// backups are kept; the newest one then links the unchanged original.
// It returns nfoSettleRolledBack with nil once the target is not the output.
func abortNFOCommitFiles(ctx context.Context, directory *os.Root, files nfoCommitFiles, original, replacement []byte, ops nfoWriteOperations) (phase nfoSettlePhase, resultErr error) {
	filename := files.plan.filename
	if ctx == nil || directory == nil || ops.rename == nil || ops.syncDirectory == nil || !validNFOCommitProgress(files) || files.rollback == (nfoNativeIdentity{}) || !IsNFOName(filename) || strings.ContainsAny(filename, "/\\:") || strings.ContainsFunc(filename, unicode.IsControl) || len(original) == 0 || len(replacement) == 0 || int64(len(original)) > MaxAllowedBytes || int64(len(replacement)) > MaxAllowedBytes {
		return nfoSettleUnchanged, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nfoSettleUnchanged, err
	}
	lock, err := lockNFOFile(ctx, directory, filename)
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
	target, err := nativeIdentityWithin(directory, filename)
	if err != nil {
		return nfoSettleIndeterminate, ErrChanged
	}
	switch target {
	case files.plan.target:
		// This token never renamed the target; its content is not ours to judge.
		if err := ops.syncDirectory(directory); err != nil {
			return nfoSettleIndeterminate, ErrRollback
		}
		return nfoSettleRolledBack, nil
	case files.output:
		if !lock.check() {
			return nfoSettleIndeterminate, ErrFileLock
		}
		if err := verifyNFOSettled(ctx, directory, files, original, replacement); err != nil {
			return nfoSettleIndeterminate, err
		}
		if phase, err := rollbackNFOSettle(directory, files, original, replacement, ops); phase != nfoSettleRolledBack {
			return phase, err
		}
		return nfoSettleRolledBack, nil
	case files.rollback:
		if phase, err := resumeNFOSettleRolledBack(ctx, directory, files, original, replacement, ops); phase != nfoSettleRolledBack {
			return phase, err
		}
		return nfoSettleRolledBack, nil
	default:
		return nfoSettleIndeterminate, ErrChanged
	}
}
