package postgres

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.IgnoreAdmissionRepository = (*Store)(nil)

func (s *Store) SubmitScanWithIgnoreCapability(ctx context.Context, a domain.Actor, library, key, priority string, intent domain.ScanIntent, p domain.JobPolicy, probe *domain.ProbeIdentity, nfo *domain.NFOIdentity, available bool) (domain.Job, bool, error) {
	return s.submitScanJobWithIgnore(ctx, a, library, "", key, priority, intent, p, probe, nfo, available)
}
func (s *Store) RetryScanWithIgnoreCapability(ctx context.Context, a domain.Actor, parent, key string, p domain.JobPolicy, probe *domain.ProbeIdentity, nfo *domain.NFOIdentity, available bool) (domain.Job, bool, error) {
	if !domain.ValidID(parent) {
		return domain.Job{}, false, domain.ErrNotFound
	}
	return s.submitScanJobWithIgnore(ctx, a, "", parent, key, "", domain.ScanIntent{}, p, probe, nfo, available)
}
