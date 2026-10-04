package postgres

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.OpsMetricsSource = (*Store)(nil)

// opsMetricsSQL reads every shared operational gauge in one statement, so a
// scrape sees one database snapshot. Each part reads an index or a table
// bounded by retention: dead and pending deliveries through partial
// indexes, retained job history (trimmed to its history limit), the single
// developer mode row and the retained consistency runs.
const opsMetricsSQL = `SELECT
 (SELECT count(*) FROM webhook_deliveries WHERE state='dead'),
 (SELECT count(*) FROM webhook_deliveries WHERE state='pending'),
 COALESCE((SELECT max(n) FROM (SELECT count(*) n FROM (
   SELECT library_id,count(*) FILTER(WHERE state='succeeded') OVER (PARTITION BY library_id ORDER BY finished_at DESC,id DESC ROWS UNBOUNDED PRECEDING) ok
   FROM jobs WHERE kind='inventory_scan' AND state IN ('succeeded','failed')) x WHERE ok=0 GROUP BY library_id) y),0),
 (SELECT count(*) FROM (SELECT DISTINCT ON (library_id) state FROM jobs WHERE kind='inventory_scan' AND state IN ('succeeded','failed') ORDER BY library_id,finished_at DESC,id DESC) z WHERE state='failed'),
 COALESCE((SELECT active AND expires_at>clock_timestamp() FROM dev_mode_state WHERE id),false),
 COALESCE((SELECT extract(epoch FROM clock_timestamp()-enabled_at)::float8 FROM dev_mode_state WHERE id AND active AND expires_at>clock_timestamp()),0),
 (SELECT max(finished_at) FROM consistency_runs WHERE state IN ('completed','partial')),
 ARRAY(SELECT COALESCE(sum(t.findings),0)::bigint FROM unnest($1::text[]) WITH ORDINALITY k(check_id,ord)
  LEFT JOIN (SELECT DISTINCT ON (COALESCE(c.library_id,'00000000-0000-0000-0000-000000000000'::uuid),c.check_id) c.check_id,c.findings
   FROM consistency_run_checks c JOIN consistency_runs r ON r.id=c.run_id
   WHERE r.state IN ('completed','partial') AND c.status IN ('ok','findings')
   ORDER BY COALESCE(c.library_id,'00000000-0000-0000-0000-000000000000'::uuid),c.check_id,r.finished_at DESC,r.id DESC) t ON t.check_id=k.check_id
  GROUP BY k.ord ORDER BY k.ord)`

// OpsMetrics reads the shared operational snapshot of the default alert
// rules (G50.6). It takes no lock and writes nothing.
func (s *Store) OpsMetrics(ctx context.Context) (app.OpsMetricsSnapshot, error) {
	if ctx == nil {
		return app.OpsMetricsSnapshot{}, domain.ErrInvalid
	}
	checks := domain.ConsistencyChecks()
	var v app.OpsMetricsSnapshot
	var last *time.Time
	var findings []int64
	err := s.Pool.QueryRow(ctx, opsMetricsSQL, checks[:]).Scan(&v.WebhookDead, &v.WebhookPending, &v.ScanConsecutiveFailures, &v.ScanFailingLibraries,
		&v.DevModeActive, &v.DevModeActiveSeconds, &last, &findings)
	if err != nil {
		return app.OpsMetricsSnapshot{}, storageError(err)
	}
	if len(findings) != len(v.ConsistencyFindings) {
		return app.OpsMetricsSnapshot{}, domain.ErrDatabase
	}
	copy(v.ConsistencyFindings[:], findings)
	if last != nil {
		v.ConsistencyLastFinished = last.UTC()
	}
	return v, nil
}
