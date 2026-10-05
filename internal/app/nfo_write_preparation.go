package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type NFOWritePreparationRepository interface {
	NFOItemScopeRepository
	FindNFOWritePreparation(context.Context, domain.Actor, string, string) (domain.NFOWritePreparation, error)
	SaveNFOWritePreparation(context.Context, domain.Actor, string, domain.NFOWritePreparation) (domain.NFOWritePreparation, bool, error)
}

// Preparer only observes and edits owned bytes. It must not write the source.
type NFOWritePreparer interface {
	PrepareNFOWrite(context.Context, domain.NFOItemScope, domain.NFOWritePrepareRequest) (domain.NFOWritePreparation, error)
}

type NFOWritePreparations struct {
	repository NFOWritePreparationRepository
	preparer   NFOWritePreparer
}

func NewNFOWritePreparations(repository NFOWritePreparationRepository, preparer NFOWritePreparer) (*NFOWritePreparations, error) {
	if repository == nil || preparer == nil {
		return nil, domain.ErrInvalid
	}
	return &NFOWritePreparations{repository: repository, preparer: preparer}, nil
}

// Exact replay returns the first persisted output, including its generated ID.
// Repository authorization also applies to replay. No DB transaction spans I/O.
func (s *NFOWritePreparations) Prepare(ctx context.Context, actor domain.Actor, key string, request domain.NFOWritePrepareRequest) (domain.NFOWritePreparation, bool, error) {
	if ctx == nil || s == nil {
		return domain.NFOWritePreparation{}, false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.NFOWritePreparation{}, false, err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.NFOWritePreparation{}, false, domain.ErrUnauthenticated
	}
	if len(key) < 1 || len(key) > 128 || stringsInvalidPreparationKey(key) {
		return domain.NFOWritePreparation{}, false, domain.ErrInvalid
	}
	request = domain.CloneNFOWritePrepareRequest(request)
	digest, err := domain.NFOWriteRequestDigest(request)
	if err != nil {
		return domain.NFOWritePreparation{}, false, err
	}
	old, err := s.repository.FindNFOWritePreparation(ctx, actor, key, digest)
	if err == nil {
		return domain.CloneNFOWritePreparation(old), true, nil
	}
	if err != domain.ErrNotFound {
		return domain.NFOWritePreparation{}, false, err
	}
	scope, err := s.repository.ResolveItemNFO(ctx, actor, request.ItemID, request.Revision)
	if err != nil {
		return domain.NFOWritePreparation{}, false, err
	}
	prepared, err := s.preparer.PrepareNFOWrite(ctx, scope, request)
	if err != nil {
		return domain.NFOWritePreparation{}, false, err
	}
	if domain.ValidateNFOWritePreparation(prepared) != nil || prepared.Scope != scope {
		return domain.NFOWritePreparation{}, false, domain.ErrInvalid
	}
	actualDigest, _ := domain.NFOWriteRequestDigest(prepared.Request)
	if actualDigest != digest {
		return domain.NFOWritePreparation{}, false, domain.ErrInvalid
	}
	result, replay, err := s.repository.SaveNFOWritePreparation(ctx, actor, key, domain.CloneNFOWritePreparation(prepared))
	if err != nil {
		return domain.NFOWritePreparation{}, false, err
	}
	return domain.CloneNFOWritePreparation(result), replay, nil
}

func stringsInvalidPreparationKey(key string) bool {
	for _, r := range key {
		if r < 33 || r > 126 {
			return true
		}
	}
	return false
}
