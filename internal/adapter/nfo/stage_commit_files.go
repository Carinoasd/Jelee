package nfo

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// StageCommitFiles reconstructs an immutable controlled intent, binds the held
// source, and uses the fenced repository ports for plan/ready persistence.
// Runtime has no caller. It neither renames the target nor grants policy,
// catalog/media authorization, recovery or successful-job completion.
func (w *Writer) StageCommitFiles(ctx context.Context, source *Source, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitFilesRepository) error {
	if w == nil || w.budget == nil || ctx == nil || source == nil || !source.ready || source.rootInfo == nil || source.parentInfo == nil || source.fileInfo == nil || repository == nil || !domain.ValidID(record.Token) || record.JobID != lease.Job.ID || record.Owner != lease.Owner || record.Generation != lease.Generation || record.Sequence < 1 || record.Sequence > 100 {
		return ErrInvalidInput
	}
	task, err := repository.GetNFOWriteTask(ctx, lease, record.Sequence)
	if err != nil {
		return err
	}
	if task.JobID != record.JobID || task.Sequence != record.Sequence {
		return ErrInvalidInput
	}
	// Only the job-owned immutable intent can drive reconstruction. A caller's
	// unrelated preparation, even with the same basename, is never accepted.
	replacement, err := w.rebuildPrepared(ctx, source, task.Preparation)
	if err != nil {
		return err
	}
	releaseCPU, err := w.budget.Acquire(ctx, app.WorkCPU)
	if err != nil {
		return err
	}
	original, err := source.Parse(ctx)
	releaseCPU()
	if err != nil {
		return err
	}
	releaseIO, err := w.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return err
	}
	defer releaseIO()
	root, err := os.OpenRoot(source.rootPath + string(os.PathSeparator) + ".")
	if err != nil {
		return ErrChanged
	}
	defer root.Close()
	parent := filepath.Dir(filepath.FromSlash(source.relative))
	directory, err := root.OpenRoot(parent)
	if err != nil {
		return ErrChanged
	}
	defer directory.Close()
	filename := filepath.Base(filepath.FromSlash(source.relative))
	check := func(checkCtx context.Context) error {
		if err := checkCtx.Err(); err != nil {
			return err
		}
		current, err := os.OpenRoot(source.rootPath + string(os.PathSeparator) + ".")
		if err != nil {
			return ErrChanged
		}
		defer current.Close()
		pathInfo, err := os.Lstat(source.rootPath)
		if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
			return ErrChanged
		}
		for _, observed := range []*os.Root{root, current} {
			info, err := observed.Stat(".")
			if err != nil || !os.SameFile(source.rootInfo, info) {
				return ErrChanged
			}
			parts := strings.Split(source.relative, "/")
			for i := range parts {
				info, err := observed.Lstat(filepath.FromSlash(strings.Join(parts[:i+1], "/")))
				if err != nil || info.Mode()&os.ModeSymlink != 0 || i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !regularNFOFile(info) {
					return ErrChanged
				}
			}
			info, err = observed.Stat(parent)
			if err != nil || !os.SameFile(source.parentInfo, info) {
				return ErrChanged
			}
		}
		info, err := directory.Stat(".")
		if err != nil || !os.SameFile(source.parentInfo, info) {
			return ErrChanged
		}
		_, err = verifyNFOOriginal(checkCtx, directory, filename, source.original, source.fileInfo)
		return err
	}
	if err := check(ctx); err != nil {
		return err
	}
	var token [16]byte
	decoded, err := hex.DecodeString(strings.ReplaceAll(record.Token, "-", ""))
	if err != nil || len(decoded) != len(token) {
		return ErrInvalidInput
	}
	copy(token[:], decoded)
	ports := nfoCommitFilePersistence{
		plan: func(ctx context.Context, plan nfoCommitFilePlan) error {
			if err := check(ctx); err != nil {
				return err
			}
			value := domain.NFOWriteCommitFilePlan{Version: plan.version, TargetName: plan.filename, ParentIdentity: plan.parent.record, TargetIdentity: plan.target.record}
			saved, err := repository.SaveNFOWriteCommitFilePlan(ctx, lease, record.Sequence, record.Token, value)
			if err != nil {
				return err
			}
			if saved != value {
				return ErrChanged
			}
			return nil
		},
		ready: func(ctx context.Context, files nfoCommitFiles) error {
			if err := check(ctx); err != nil {
				return err
			}
			value := domain.NFOWriteCommitFilesReady{OutputIdentity: files.output.record, RollbackIdentity: files.rollback.record}
			saved, err := repository.SaveNFOWriteCommitFilesReady(ctx, lease, record.Sequence, record.Token, value)
			if err != nil {
				return err
			}
			if saved != value {
				return ErrChanged
			}
			return nil
		},
	}
	ops := nativeNFOWriteOperations()
	ops.documentsValidated, ops.checkSource = true, check
	_, err = prepareNFOCommitFiles(ctx, directory, filename, original, replacement, token, ports, ops)
	return err
}
