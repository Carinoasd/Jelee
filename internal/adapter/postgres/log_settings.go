package postgres

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Logging settings (G46.2, G46.9): runtime level overrides and the
// retention of ordinary log files, in the singleton row of log_settings
// (migration 000085). Every instance reads the row on an interval and
// applies it to its log router; administrators change it through
// /api/v1/admin/logging, and every change is audited.

const logSettingsColumns = `log_days,log_max_total_mb,level_overrides::text,revision,updated_at`

func scanLogSettings(row pgx.Row) (domain.LogSettings, error) {
	var s domain.LogSettings
	var overrides string
	if err := row.Scan(&s.LogDays, &s.LogMaxTotalMB, &overrides, &s.Revision, &s.UpdatedAt); err != nil {
		return domain.LogSettings{}, storageError(err)
	}
	if err := json.Unmarshal([]byte(overrides), &s.Overrides); err != nil {
		return domain.LogSettings{}, domain.ErrDatabase
	}
	if s.Overrides == nil {
		s.Overrides = []domain.LogLevelOverride{}
	}
	return s, nil
}

// requestTrace is the trace ID of the request ctx belongs to, recorded with
// the audit row so it joins the access and security logs.
func requestTrace(ctx context.Context) string {
	if sc, ok := domain.SpanFromContext(ctx); ok {
		return sc.TraceHex()
	}
	return ""
}

// LogSettings reads the stored settings without an actor. It is the
// periodic read of every instance; administrators use AdminLogSettings.
func (s *Store) LogSettings(ctx context.Context) (domain.LogSettings, error) {
	return scanLogSettings(s.Pool.QueryRow(ctx, `SELECT `+logSettingsColumns+` FROM log_settings WHERE singleton`))
}

// AdminLogSettings returns the log settings and the audit retention for an
// administrator.
func (s *Store) AdminLogSettings(ctx context.Context, actor domain.Actor) (domain.LogSettings, domain.AuditRetention, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.LogSettings{}, domain.AuditRetention{}, err
	}
	defer tx.Rollback(ctx)
	settings, err := scanLogSettings(tx.QueryRow(ctx, `SELECT `+logSettingsColumns+` FROM log_settings WHERE singleton`))
	if err != nil {
		return domain.LogSettings{}, domain.AuditRetention{}, err
	}
	retention, err := scanAuditRetention(tx.QueryRow(ctx, `SELECT audit_days,security_days,revision,updated_at FROM audit_retention WHERE singleton`))
	if err != nil {
		return domain.LogSettings{}, domain.AuditRetention{}, err
	}
	return settings, retention, storageError(tx.Commit(ctx))
}

// SetLogLevelOverride stores (or, with reset, removes) the override of one
// scope and audits the change as logging.level_changed. Expired overrides
// are dropped in the same write. Scope and level are validated by the
// caller against the log router's scopes; storage only bounds them.
func (s *Store) SetLogLevelOverride(ctx context.Context, actor domain.Actor, change domain.LogLevelOverride, reset bool) (domain.LogSettings, error) {
	if len(change.Component) > 32 || !reset && change.Level == "" || len(change.Level) > 8 {
		return domain.LogSettings{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.LogSettings{}, err
	}
	defer tx.Rollback(ctx)
	old, err := scanLogSettings(tx.QueryRow(ctx, `SELECT `+logSettingsColumns+` FROM log_settings WHERE singleton FOR UPDATE`))
	if err != nil {
		return domain.LogSettings{}, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return domain.LogSettings{}, storageError(err)
	}
	var before *domain.LogLevelOverride
	next := make([]domain.LogLevelOverride, 0, len(old.Overrides)+1)
	for _, o := range old.Overrides {
		if o.Component == change.Component {
			if o.Active(now) {
				previous := o
				before = &previous
			}
			continue
		}
		if o.Active(now) {
			next = append(next, o)
		}
	}
	if !reset {
		change.ExpiresAt = change.ExpiresAt.UTC()
		next = append(next, change)
	}
	slices.SortFunc(next, func(a, b domain.LogLevelOverride) int {
		switch {
		case a.Component < b.Component:
			return -1
		case a.Component > b.Component:
			return 1
		}
		return 0
	})
	if len(next) > domain.LogOverridesMax {
		return domain.LogSettings{}, domain.ErrInvalid
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return domain.LogSettings{}, domain.ErrInvalid
	}
	updated, err := scanLogSettings(tx.QueryRow(ctx, `UPDATE log_settings SET level_overrides=$1::jsonb,revision=revision+1,updated_at=now() WHERE singleton RETURNING `+logSettingsColumns, string(encoded)))
	if err != nil {
		return domain.LogSettings{}, err
	}
	scope := change.Component
	if scope == "" {
		scope = "global"
	}
	var after *domain.LogLevelOverride
	if !reset {
		after = &change
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "logging.level_changed", Actor: actor, TargetRef: "log_level:" + scope, RequestID: requestTrace(ctx),
		Before: map[string]any{"override": before}, After: map[string]any{"override": after}}); err != nil {
		return domain.LogSettings{}, err
	}
	return updated, storageError(tx.Commit(ctx))
}

// RecordLogLevelRefused audits a refused attempt to lower or switch off a
// mandatory log scope (G46.10) as the security event
// logging.level_change_refused. Nothing else changes.
func (s *Store) RecordLogLevelRefused(ctx context.Context, actor domain.Actor, component, level string) error {
	if len(component) > 32 || len(level) > 16 {
		return domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = appendAudit(ctx, tx, AuditEntry{Event: "logging.level_change_refused", Actor: actor, TargetRef: "log_level:" + component, RequestID: requestTrace(ctx),
		After: map[string]any{"component": component, "level": level, "reason": "mandatory"}}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// SetLogRetention changes the log file retention and the audit retention in
// one transaction. A changed log retention is audited as
// logging.retention_changed, a changed audit retention as
// audit.retention_changed; an unchanged request writes nothing.
func (s *Store) SetLogRetention(ctx context.Context, actor domain.Actor, r domain.LogRetention) (domain.LogSettings, domain.AuditRetention, error) {
	if !domain.ValidLogRetention(r.LogDays, r.LogMaxTotalMB) || !validAuditRetentionDays(r.AuditDays) || !validAuditRetentionDays(r.SecurityDays) {
		return domain.LogSettings{}, domain.AuditRetention{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.LogSettings{}, domain.AuditRetention{}, err
	}
	defer tx.Rollback(ctx)
	settings, err := scanLogSettings(tx.QueryRow(ctx, `SELECT `+logSettingsColumns+` FROM log_settings WHERE singleton FOR UPDATE`))
	if err != nil {
		return domain.LogSettings{}, domain.AuditRetention{}, err
	}
	audit, err := scanAuditRetention(tx.QueryRow(ctx, `SELECT audit_days,security_days,revision,updated_at FROM audit_retention WHERE singleton FOR UPDATE`))
	if err != nil {
		return domain.LogSettings{}, domain.AuditRetention{}, err
	}
	trace := requestTrace(ctx)
	if settings.LogDays != r.LogDays || settings.LogMaxTotalMB != r.LogMaxTotalMB {
		before := map[string]int{"logDays": settings.LogDays, "logMaxTotalMB": settings.LogMaxTotalMB}
		if settings, err = scanLogSettings(tx.QueryRow(ctx, `UPDATE log_settings SET log_days=$1,log_max_total_mb=$2,revision=revision+1,updated_at=now() WHERE singleton RETURNING `+logSettingsColumns,
			r.LogDays, r.LogMaxTotalMB)); err != nil {
			return domain.LogSettings{}, domain.AuditRetention{}, err
		}
		if err = appendAudit(ctx, tx, AuditEntry{Event: "logging.retention_changed", Actor: actor, TargetRef: "log_settings", RequestID: trace,
			Before: before, After: map[string]int{"logDays": settings.LogDays, "logMaxTotalMB": settings.LogMaxTotalMB}}); err != nil {
			return domain.LogSettings{}, domain.AuditRetention{}, err
		}
	}
	if audit.AuditDays != r.AuditDays || audit.SecurityDays != r.SecurityDays {
		old := audit
		if audit, err = scanAuditRetention(tx.QueryRow(ctx, `UPDATE audit_retention SET audit_days=$1,security_days=$2,revision=revision+1,updated_at=now() WHERE singleton RETURNING audit_days,security_days,revision,updated_at`,
			r.AuditDays, r.SecurityDays)); err != nil {
			return domain.LogSettings{}, domain.AuditRetention{}, err
		}
		if err = appendAudit(ctx, tx, AuditEntry{Event: "audit.retention_changed", Actor: actor, TargetRef: "audit_retention", RequestID: trace, Before: old, After: audit}); err != nil {
			return domain.LogSettings{}, domain.AuditRetention{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.LogSettings{}, domain.AuditRetention{}, storageError(err)
	}
	return settings, audit, nil
}
