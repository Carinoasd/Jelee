package nfo

import (
	"context"
	"encoding/hex"
	"os"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// VerifyCommitFiles reads retained pre-commit evidence through an already-held
// parent. It does not establish library/root/media/lease authorization, infer
// that Rename committed, resolve a journal, or modify any file.
func VerifyCommitFiles(ctx context.Context, directory *os.Root, token string, plan domain.NFOWriteCommitFilePlan, ready domain.NFOWriteCommitFilesReady, original, replacement []byte) error {
	if ctx == nil || directory == nil || !domain.ValidID(token) || domain.ValidateNFOWriteCommitFilePlan(plan) != nil || domain.ValidateNFOWriteCommitFilesReady(ready) != nil || len(original) == 0 || len(replacement) == 0 || int64(len(original)) > MaxAllowedBytes || int64(len(replacement)) > MaxAllowedBytes {
		return ErrInvalidInput
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(token, "-", ""))
	if err != nil || len(decoded) != 16 {
		return ErrInvalidInput
	}
	var identity [16]byte
	copy(identity[:], decoded)
	files := nfoCommitFiles{
		plan:   nfoCommitFilePlan{version: plan.Version, token: identity, filename: plan.TargetName, parent: nfoNativeIdentity{plan.ParentIdentity}, target: nfoNativeIdentity{plan.TargetIdentity}},
		output: nfoNativeIdentity{ready.OutputIdentity}, rollback: nfoNativeIdentity{ready.RollbackIdentity},
	}
	return verifyNFOCommitFiles(ctx, directory, files, original, replacement)
}
