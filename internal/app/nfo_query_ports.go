package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Current queries require a trusted local identity and current policy/root
// generations/TTL, never an identity supplied by HTTP. Missing, replaced,
// expired or invalidated observation IDs return ErrNotFound. Every query checks
// live administration through transaction completion. History is read separately.
type NFOQueryRepository interface {
	GetNFOJobSummary(context.Context, domain.Actor, string) (domain.NFOJobSummary, error)
	ListNFOObservations(context.Context, domain.Actor, string, string, int, domain.NFOIdentity) (domain.NFOObservationPage, error)
	GetNFOObservationIssues(context.Context, domain.Actor, string, string, int, int, domain.NFOIdentity) (domain.NFOIssuesPage, error)
}

type ImageQueryRepository interface {
	GetImageJobSummary(context.Context, domain.Actor, string) (domain.ImageJobSummary, error)
}
