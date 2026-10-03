package nfo

import (
	"context"
	"errors"
	"os"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Progress carries first observations of complete retained witness pairs. Its
// shape is private evidence, never filesystem authorization or a commit result.
func validNFOCommitProgress(files nfoCommitFiles) bool {
	plan := domain.NFOWriteCommitFilePlan{Version: files.plan.version, TargetName: files.plan.filename, ParentIdentity: files.plan.parent.record, TargetIdentity: files.plan.target.record}
	if files.plan.token == ([16]byte{}) || domain.ValidateNFOWriteCommitFilePlan(plan) != nil || !domain.ValidNFONativeIdentity(files.output.record, 1) || files.output.record[1] != plan.TargetIdentity[1] || files.output == files.plan.target {
		return false
	}
	if files.rollback == (nfoNativeIdentity{}) {
		return true
	}
	return domain.ValidNFONativeIdentity(files.rollback.record, 1) && files.rollback.record[1] == plan.TargetIdentity[1] && files.rollback != files.output && files.rollback != files.plan.target
}

func verifyNFOCommitProgress(ctx context.Context, directory *os.Root, files nfoCommitFiles, original, replacement []byte) error {
	if ctx == nil || directory == nil || !validNFOCommitProgress(files) {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if current, err := nativeIdentityWithin(directory, "."); err != nil || current != files.plan.parent {
		return ErrChanged
	}
	names := files.plan.names()
	candidates := []struct {
		name string
		id   nfoNativeIdentity
		data []byte
	}{
		{files.plan.filename, files.plan.target, original},
		{names[0], files.plan.target, original},
		{names[1], files.output, replacement},
		{names[2], files.output, replacement},
	}
	if files.rollback != (nfoNativeIdentity{}) {
		candidates = append(candidates, struct {
			name string
			id   nfoNativeIdentity
			data []byte
		}{names[3], files.rollback, original}, struct {
			name string
			id   nfoNativeIdentity
			data []byte
		}{names[4], files.rollback, original})
	} else {
		// The output checkpoint cannot authorize adoption of any later object,
		// even one with exactly the intended bytes or a matching basename.
		for _, name := range names[3:] {
			if _, err := directory.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				return ErrChanged
			}
		}
	}
	for _, candidate := range candidates {
		info, err := verifyNFOOriginal(ctx, directory, candidate.name, candidate.data, nil)
		if err != nil {
			return err
		}
		if current, err := nativeIdentityWithinInfo(directory, candidate.name, info); err != nil || current != candidate.id {
			return ErrChanged
		}
	}
	return nil
}
