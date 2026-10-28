package nfo

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.NFOWriteCommitFileCleaner = (*Writer)(nil)

// nfoCleanupOperations are the only mutations cleanup performs. Tests replace
// them to interrupt the pass between removals.
type nfoCleanupOperations struct {
	remove        func(*os.Root, string) error
	syncDirectory func(*os.Root) error
}

func nativeNFOCleanupOperations() nfoCleanupOperations {
	return nfoCleanupOperations{
		remove:        func(r *os.Root, name string) error { return r.Remove(name) },
		syncDirectory: syncNFODirectory,
	}
}

// CleanNFOWriteCommitFiles removes the objects a resolved token retained: the
// five commit names of every allocated attempt namespace and the backup its
// rotation evicted. A name is removed only when its native identity equals the
// durable record for that name; an evicted backup only when it is the
// original of an earlier settled plan for the same file. Anything else is
// kept and counted. The target and the .jelee.bak series are never touched.
// The pass is idempotent: names already absent count as removed.
func (w *Writer) CleanNFOWriteCommitFiles(ctx context.Context, input domain.NFOWriteCommitCleanupInput) (domain.NFOWriteCommitCleanupOutcome, int, error) {
	return w.cleanCommitFiles(ctx, input, nativeNFOCleanupOperations())
}

func (w *Writer) cleanCommitFiles(ctx context.Context, input domain.NFOWriteCommitCleanupInput, ops nfoCleanupOperations) (outcome domain.NFOWriteCommitCleanupOutcome, retained int, resultErr error) {
	if w == nil || w.budget == nil || ctx == nil || ops.remove == nil || ops.syncDirectory == nil {
		return 0, 0, ErrInvalidInput
	}
	plans, err := nfoCleanupPlans(input)
	if err != nil {
		return 0, 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	release, err := w.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return 0, 0, err
	}
	defer release()
	// A missing library root may be an unmounted volume: retry later rather
	// than release capacity for files that may come back.
	if info, err := os.Lstat(input.RootPath); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0, ErrChanged
	}
	root, err := os.OpenRoot(filepath.Clean(input.RootPath) + string(os.PathSeparator) + ".")
	if err != nil {
		return 0, 0, ErrChanged
	}
	defer func() { _ = root.Close() }()
	parts := strings.Split(input.RelativePath, "/")
	for i := range parts[:len(parts)-1] {
		info, err := root.Lstat(filepath.FromSlash(strings.Join(parts[:i+1], "/")))
		if errors.Is(err, os.ErrNotExist) || err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return domain.NFOWriteCommitCleanupParentGone, 0, nil
		}
		if err != nil {
			return 0, 0, ErrChanged
		}
	}
	directory, err := root.OpenRoot(filepath.Dir(filepath.FromSlash(input.RelativePath)))
	if err != nil {
		return 0, 0, ErrChanged
	}
	defer func() { _ = directory.Close() }()
	parent := plans[0].parent
	if current, err := nativeIdentityWithin(directory, "."); err != nil || current != parent {
		return domain.NFOWriteCommitCleanupParentGone, 0, nil
	}
	lock, err := lockNFOFile(ctx, directory, plans[0].filename)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if err := lock.Close(); err != nil && resultErr == nil {
			outcome, retained, resultErr = 0, 0, err
		}
	}()
	if current, err := nativeIdentityWithin(directory, "."); err != nil || current != parent {
		return domain.NFOWriteCommitCleanupParentGone, 0, nil
	}
	evicted := make(map[nfoNativeIdentity]bool, len(input.EvictedIdentities))
	for _, record := range input.EvictedIdentities {
		identity, err := parseNFONativeIdentity(record[:])
		if err != nil || record[2] != 1 {
			return 0, 0, ErrInvalidInput
		}
		evicted[identity] = true
	}
	removed := false
	remove := func(name string, owned func(nfoNativeIdentity) bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := directory.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return ErrChanged
		}
		if !regularNFOFile(info) {
			retained++
			return nil
		}
		current, err := nativeIdentityWithinInfo(directory, name, info)
		if err != nil {
			return ErrChanged
		}
		if !owned(current) {
			retained++
			return nil
		}
		if !lock.check() {
			return ErrFileLock
		}
		if err := ops.remove(directory, name); err != nil {
			return ErrReplace
		}
		removed = true
		return nil
	}
	for _, plan := range plans {
		names := plan.names()
		expected := [5]nfoNativeIdentity{plan.target, plan.output, plan.output, plan.rollback, plan.rollback}
		for i, name := range names {
			want := expected[i]
			if err := remove(name, func(current nfoNativeIdentity) bool { return want != (nfoNativeIdentity{}) && current == want }); err != nil {
				return 0, 0, err
			}
		}
		if err := remove(nfoSettleEvictedName(plan.nfoCommitFilePlan), func(current nfoNativeIdentity) bool { return evicted[current] }); err != nil {
			return 0, 0, err
		}
	}
	if removed {
		if err := ops.syncDirectory(directory); err != nil {
			return 0, 0, ErrReplace
		}
	}
	if retained > domain.NFOWriteCommitCleanupMaxRetained {
		return 0, 0, ErrInvalidInput
	}
	if retained > 0 {
		return domain.NFOWriteCommitCleanupRetained, retained, nil
	}
	return domain.NFOWriteCommitCleanupRemoved, 0, nil
}

// nfoCleanupPlan is one attempt namespace with the identities recorded for it.
// A zero identity was never recorded and proves nothing.
type nfoCleanupPlan struct {
	nfoCommitFilePlan
	output, rollback nfoNativeIdentity
}

func nfoCleanupPlans(input domain.NFOWriteCommitCleanupInput) ([]nfoCleanupPlan, error) {
	filename := input.Plan.TargetName
	if !domain.ValidID(input.Lease.Token) || input.Plan.Version != domain.NFOWriteCommitPlanVersion && input.Plan.Version != domain.NFOWriteCommitCreatePlanVersion || !IsNFOName(filename) || strings.ContainsAny(filename, "/\\:") || strings.ContainsFunc(filename, unicode.IsControl) || input.RootPath == "" || !filepath.IsAbs(input.RootPath) || input.RelativePath == "" || strings.HasPrefix(input.RelativePath, "/") || path.Base(input.RelativePath) != filename || len(input.Namespaces) == 0 || len(input.Namespaces) > int(maxNFOCommitAttempts)+1 {
		return nil, ErrInvalidInput
	}
	for _, part := range strings.Split(input.RelativePath, "/") {
		if part == "" || part == "." || part == ".." {
			return nil, ErrInvalidInput
		}
	}
	var token [16]byte
	decoded, err := hex.DecodeString(strings.ReplaceAll(input.Lease.Token, "-", ""))
	if err != nil || len(decoded) != len(token) {
		return nil, ErrInvalidInput
	}
	copy(token[:], decoded)
	parent, err := parseNFONativeIdentity(input.Plan.ParentIdentity[:])
	if err != nil || input.Plan.ParentIdentity[2] != 2 {
		return nil, ErrInvalidInput
	}
	// A create planned an absent target: it has no identity, so the original
	// witness name proves nothing and is never removed on its behalf.
	create := input.Plan.Version == domain.NFOWriteCommitCreatePlanVersion
	var target nfoNativeIdentity
	if create {
		if input.Plan.TargetIdentity != ([48]byte{}) {
			return nil, ErrInvalidInput
		}
	} else {
		target, err = parseNFONativeIdentity(input.Plan.TargetIdentity[:])
		if err != nil || input.Plan.TargetIdentity[2] != 1 {
			return nil, ErrInvalidInput
		}
	}
	identity := func(record [48]byte) (nfoNativeIdentity, error) {
		if record == ([48]byte{}) {
			return nfoNativeIdentity{}, nil
		}
		parsed, err := parseNFONativeIdentity(record[:])
		if err != nil || record[2] != 1 || parsed == target {
			return nfoNativeIdentity{}, ErrInvalidInput
		}
		return parsed, nil
	}
	seen := make(map[uint8]bool, len(input.Namespaces))
	plans := make([]nfoCleanupPlan, 0, len(input.Namespaces))
	for _, namespace := range input.Namespaces {
		if namespace.Attempt > maxNFOCommitAttempts || seen[namespace.Attempt] {
			return nil, ErrInvalidInput
		}
		seen[namespace.Attempt] = true
		output, err := identity(namespace.OutputIdentity)
		if err != nil {
			return nil, err
		}
		rollback, err := identity(namespace.RollbackIdentity)
		if err != nil || rollback != (nfoNativeIdentity{}) && (rollback == output || create) {
			return nil, ErrInvalidInput
		}
		plans = append(plans, nfoCleanupPlan{nfoCommitFilePlan: nfoCommitFilePlan{version: input.Plan.Version, token: token, filename: filename, parent: parent, target: target, attempt: namespace.Attempt}, output: output, rollback: rollback})
	}
	return plans, nil
}
