package nfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type WritePreparer struct{ budget app.WorkBudget }

func NewWritePreparer(budget app.WorkBudget) (*WritePreparer, error) {
	if budget == nil {
		return nil, domain.ErrInvalid
	}
	return &WritePreparer{budget: budget}, nil
}

var _ app.NFOWritePreparer = (*WritePreparer)(nil)

func (p *WritePreparer) PrepareNFOWrite(ctx context.Context, scope domain.NFOItemScope, request domain.NFOWritePrepareRequest) (domain.NFOWritePreparation, error) {
	if ctx == nil || p == nil || p.budget == nil || !domain.ValidNFOItemScope(scope) || scope.RootGeneration < 1 || domain.ValidateNFOWritePrepareRequest(request) != nil || scope.ItemID != request.ItemID || scope.Revision != request.Revision {
		return domain.NFOWritePreparation{}, domain.ErrInvalid
	}
	if request.Create {
		return p.prepareCreate(ctx, scope, request)
	}
	release, err := p.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	source, firstReceipt, err := readNativePreparationSource(ctx, scope, request.MaxBytes)
	release()
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	prepared, err := p.prepareBytes(ctx, source, scope, request)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	prepared.NativeObservation = firstReceipt
	if err := domain.ValidateNFOWritePreparation(prepared); err != nil {
		return domain.NFOWritePreparation{}, err
	}
	release, err = p.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	defer release()
	last, lastReceipt, err := readNativePreparationSource(ctx, scope, request.MaxBytes)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	if !firstReceipt.Equal(lastReceipt) || !source.nativeObserved || !last.nativeObserved || source.nativeRoot != last.nativeRoot || source.nativeFile != last.nativeFile || !os.SameFile(source.rootInfo, last.rootInfo) || !os.SameFile(source.parentInfo, last.parentInfo) || !os.SameFile(source.fileInfo, last.fileInfo) || source.stamp != last.stamp || !bytes.Equal(source.original, last.original) {
		return domain.NFOWritePreparation{}, ErrChanged
	}
	return prepared, nil
}

func (p *WritePreparer) prepareBytes(ctx context.Context, source *Source, scope domain.NFOItemScope, request domain.NFOWritePrepareRequest) (domain.NFOWritePreparation, error) {
	release, err := p.budget.Acquire(ctx, app.WorkCPU)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	defer release()
	original, err := source.Parse(ctx)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	root := map[string]string{"Movie": "movie", "HomeVideo": "movie", "Series": "tvshow", "Season": "season", "Episode": "episode"}[scope.Kind]
	if scope.Kind == "Episode" && original.Root == "episodedetails" {
		root = "episodedetails"
	}
	if original.Root != root || len(original.Entries) != 1 {
		return domain.NFOWritePreparation{}, ErrInvalidInput
	}
	replacement := original
	opts := TextEditOptions{CreateMissing: request.CreateMissing, BOM: request.BOM, Indent: request.Indent}
	for _, edit := range request.Edits {
		replacement, err = replacement.WithTextOptions(ctx, 0, edit.Field, edit.Value, request.MaxBytes, opts)
		if err != nil {
			return domain.NFOWritePreparation{}, err
		}
	}
	replacement, err = replacement.EnsureID(ctx, 0, request.MaxBytes, opts)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	if err = validateNFOWriteDocuments(ctx, original, replacement); err != nil {
		return domain.NFOWritePreparation{}, err
	}
	stamp := source.Stamp()
	result := domain.NFOWritePreparation{Version: domain.NFOWritePreparationVersion, Request: domain.CloneNFOWritePrepareRequest(request), Scope: scope, Stamp: domain.NFOStamp{Size: stamp.Size, ModifiedUnixNano: stamp.ModifiedUnixNano, SHA256: stamp.SHA256, FingerprintVersion: stamp.FingerprintVersion}, Original: slices.Clone(source.original), Replacement: slices.Clone(replacement.original)}
	return result, domain.ValidateNFOWritePreparation(result)
}

// prepareCreate builds the replacement of a missing NFO from an empty root of
// the item's kind. Its first observation records the parent directory and that
// the NFO name was absent; a name that exists in any form is a conflict, never
// a create. The same three phases as a rewrite apply and nothing is written.
func (p *WritePreparer) prepareCreate(ctx context.Context, scope domain.NFOItemScope, request domain.NFOWritePrepareRequest) (domain.NFOWritePreparation, error) {
	release, err := p.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	firstReceipt, err := readNativePreparationAbsent(ctx, scope)
	release()
	if err != nil {
		return domain.NFOWritePreparation{}, createObservationError(err)
	}
	prepared, err := p.prepareCreateBytes(ctx, scope, request)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	prepared.NativeObservation = firstReceipt
	if err := domain.ValidateNFOWritePreparation(prepared); err != nil {
		return domain.NFOWritePreparation{}, err
	}
	release, err = p.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	defer release()
	lastReceipt, err := readNativePreparationAbsent(ctx, scope)
	if err != nil {
		return domain.NFOWritePreparation{}, createObservationError(err)
	}
	if !firstReceipt.Equal(lastReceipt) {
		return domain.NFOWritePreparation{}, ErrChanged
	}
	return prepared, nil
}

// createObservationError reports an existing target as the 409 conflict it is.
func createObservationError(err error) error {
	if errors.Is(err, ErrTargetAppeared) {
		return domain.ErrConflict
	}
	return err
}

func (p *WritePreparer) prepareCreateBytes(ctx context.Context, scope domain.NFOItemScope, request domain.NFOWritePrepareRequest) (domain.NFOWritePreparation, error) {
	release, err := p.budget.Acquire(ctx, app.WorkCPU)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	defer release()
	replacement, err := createBaseDocument(ctx, scope.Kind)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	// A new document has no element to edit yet, so every edit may add its own.
	opts := TextEditOptions{CreateMissing: true, BOM: request.BOM, Indent: request.Indent}
	for _, edit := range request.Edits {
		replacement, err = replacement.WithTextOptions(ctx, 0, edit.Field, edit.Value, request.MaxBytes, opts)
		if err != nil {
			return domain.NFOWritePreparation{}, err
		}
	}
	replacement, err = replacement.EnsureID(ctx, 0, request.MaxBytes, opts)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	if err = validateNFOWriteDocuments(ctx, replacement); err != nil {
		return domain.NFOWritePreparation{}, err
	}
	// The stamp of nothing: there is no original, only a recorded absence.
	empty := sha256.Sum256(nil)
	result := domain.NFOWritePreparation{Version: domain.NFOWritePreparationVersion, Request: domain.CloneNFOWritePrepareRequest(request), Scope: scope, Stamp: domain.NFOStamp{SHA256: hex.EncodeToString(empty[:]), FingerprintVersion: domain.NFOFingerprintVersion}, Replacement: slices.Clone(replacement.original)}
	return result, domain.ValidateNFOWritePreparation(result)
}
