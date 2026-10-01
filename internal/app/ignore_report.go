package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type IgnoreReportRepository interface {
	GetIgnoreReport(context.Context, domain.Actor, string, int, string) (domain.IgnoreReport, error)
}

func (j *Jobs) IgnoreReport(ctx context.Context, a domain.Actor, id string, limit int, cursor string) (domain.IgnoreReport, error) {
	if ctx == nil || !validTarget(a, id) || limit < 1 || limit > 100 || len(cursor) > 4096 {
		return domain.IgnoreReport{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.IgnoreReport{}, err
	}
	repo, ok := j.repository.(IgnoreReportRepository)
	if !ok {
		return domain.IgnoreReport{}, domain.ErrIgnoreUnavailable
	}
	return repo.GetIgnoreReport(ctx, a, id, limit, cursor)
}
