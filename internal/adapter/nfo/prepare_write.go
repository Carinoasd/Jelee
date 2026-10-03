package nfo

import (
	"bytes"
	"context"
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
	release, err := p.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	source, err := ReadSource(ctx, scope.Source.RootPath, scope.Source.RelativePath, request.MaxBytes)
	release()
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	prepared, err := p.prepareBytes(ctx, source, scope, request)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	release, err = p.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	defer release()
	last, err := ReadSource(ctx, scope.Source.RootPath, scope.Source.RelativePath, request.MaxBytes)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	if !os.SameFile(source.rootInfo, last.rootInfo) || !os.SameFile(source.parentInfo, last.parentInfo) || !os.SameFile(source.fileInfo, last.fileInfo) || source.stamp != last.stamp || !bytes.Equal(source.original, last.original) {
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
