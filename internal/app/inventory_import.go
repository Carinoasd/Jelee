package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type InventoryImportRepository interface {
	ResolveAuthorizedInventoryImport(context.Context, domain.Actor, string, string) (domain.InventoryImportSource, error)
	PutInventoryVideo(context.Context, domain.Actor, domain.InventoryImportSource, domain.InventoryImportInput) (domain.InventoryImportResult, error)
}

type InventoryImportVerifier interface {
	VerifyInventoryImport(context.Context, domain.InventoryImportSource) error
}

// NewJobsWithInventoryImport binds the trusted reader before serving requests.
func NewJobsWithInventoryImport(base *Jobs, repository InventoryImportRepository, verifier InventoryImportVerifier) (*Jobs, error) {
	if base == nil || repository == nil || verifier == nil {
		return nil, domain.ErrInvalid
	}
	value := *base
	value.importRepository, value.importVerifier = repository, verifier
	return &value, nil
}

func (j *Jobs) ImportInventory(ctx context.Context, actor domain.Actor, job, entry string, input domain.InventoryImportInput) (domain.InventoryImportResult, error) {
	if ctx == nil || !validTarget(actor, job) || !domain.ValidID(entry) || !domain.ValidInventoryImportInput(input) {
		return domain.InventoryImportResult{}, domain.ErrInvalid
	}
	if j == nil || j.importRepository == nil || j.importVerifier == nil {
		return domain.InventoryImportResult{}, domain.ErrMetadataUnavailable
	}
	source, err := j.importRepository.ResolveAuthorizedInventoryImport(ctx, actor, job, entry)
	if err != nil {
		return domain.InventoryImportResult{}, err
	}
	if err := j.importVerifier.VerifyInventoryImport(ctx, source); err != nil {
		return domain.InventoryImportResult{}, err
	}
	return j.importRepository.PutInventoryVideo(ctx, actor, source, input)
}
