package postgres

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	"github.com/jackc/pgx/v5"
)

// Developer mode storage (G45): the single shared session row, one-time
// enable tokens and the audit trail of every transition. It implements
// devmode.Store.

var _ devmode.Store = (*Store)(nil)

// devAuditEvents maps state machine events to audit events. Every developer
// mode record is a security event.
var devAuditEvents = map[devmode.EventKind]string{
	devmode.EventEnabled:            "devmode.enabled",
	devmode.EventDisabled:           "devmode.disabled",
	devmode.EventExpired:            "devmode.expired",
	devmode.EventDenied:             "devmode.denied",
	devmode.EventToggleChanged:      "devmode.toggle_changed",
	devmode.EventDangerousConfirmed: "devmode.dangerous_confirmed",
}

const devStateColumns = `active,enabled_at,expires_at,source,toggles,version`

func scanDevRecord(row pgx.Row) (devmode.Record, error) {
	var rec devmode.Record
	var enabled, expires *time.Time
	var toggles []string
	if err := row.Scan(&rec.Active, &enabled, &expires, &rec.Source, &toggles, &rec.Version); err != nil {
		return devmode.Record{}, storageError(err)
	}
	if enabled != nil {
		rec.EnabledAt = enabled.UTC()
	}
	if expires != nil {
		rec.ExpiresAt = expires.UTC()
	}
	for _, t := range toggles {
		rec.Toggles = append(rec.Toggles, devmode.Toggle(t))
	}
	return rec, nil
}

// LoadDevSession reads the shared session.
func (s *Store) LoadDevSession(ctx context.Context) (devmode.Record, error) {
	return scanDevRecord(s.Pool.QueryRow(ctx, `SELECT `+devStateColumns+` FROM dev_mode_state WHERE id`))
}

// UpdateDevSession runs fn under the row lock and stores its result and
// events in one transaction (see devmode.Store).
func (s *Store) UpdateDevSession(ctx context.Context, actor devmode.Actor, fn devmode.Mutation) (devmode.Record, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return devmode.Record{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	cur, err := scanDevRecord(tx.QueryRow(ctx, `SELECT `+devStateColumns+` FROM dev_mode_state WHERE id FOR UPDATE`))
	if err != nil {
		return devmode.Record{}, err
	}
	next, events, fnErr := fn(cur, devTokens{tx: tx})
	stored := cur
	if next.Version != cur.Version {
		if next.Version != cur.Version+1 {
			return devmode.Record{}, domain.ErrDatabase
		}
		toggles := make([]string, len(next.Toggles))
		for i, t := range next.Toggles {
			toggles[i] = string(t)
		}
		var enabled, expires *time.Time
		if next.Active {
			enabled, expires = &next.EnabledAt, &next.ExpiresAt
		}
		if stored, err = scanDevRecord(tx.QueryRow(ctx, `UPDATE dev_mode_state SET active=$1,enabled_at=$2,expires_at=$3,source=$4,toggles=$5,version=$6,updated_at=now() WHERE id RETURNING `+devStateColumns,
			next.Active, enabled, expires, next.Source, toggles, next.Version)); err != nil {
			return devmode.Record{}, err
		}
	}
	for _, e := range events {
		if err = appendAudit(ctx, tx, devAuditEntry(actor, e)); err != nil {
			return devmode.Record{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return devmode.Record{}, storageError(err)
	}
	return stored, fnErr
}

// IssueDevToken stores a token digest and audits the issuance. Expired
// tokens are purged on the way.
func (s *Store) IssueDevToken(ctx context.Context, actor devmode.Actor, digest [32]byte, issuedAt, expiresAt time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM dev_mode_tokens WHERE expires_at<=$1`, issuedAt); err != nil {
		return storageError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO dev_mode_tokens(digest,issued_at,expires_at) VALUES($1,$2,$3)`, digest[:], issuedAt, expiresAt); err != nil {
		return storageError(err)
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "devmode.token_issued", Actor: domain.Actor(actor), TargetRef: "dev_mode",
		After: map[string]any{"expiresAt": expiresAt.UTC().Format(time.RFC3339), "entry": "loopback"}}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

type devTokens struct{ tx pgx.Tx }

func (d devTokens) RedeemToken(ctx context.Context, digest [32]byte, now time.Time) (bool, error) {
	tag, err := d.tx.Exec(ctx, `UPDATE dev_mode_tokens SET redeemed_at=$2 WHERE digest=$1 AND redeemed_at IS NULL AND expires_at>$2`, digest[:], now)
	if err != nil {
		return false, storageError(err)
	}
	return tag.RowsAffected() == 1, nil
}

// devAuditEntry records time (the row's own), source, reason, the
// configuration diff and the affected toggles (G45.2). Events never carry
// tokens.
func devAuditEntry(actor devmode.Actor, e devmode.Event) AuditEntry {
	after := map[string]any{"source": e.Source}
	if e.Reason != "" {
		after["reason"] = e.Reason
	}
	if e.Alert {
		after["alert"] = true
	}
	if len(e.Missing) > 0 {
		missing := make([]string, len(e.Missing))
		for i, m := range e.Missing {
			missing[i] = string(m)
		}
		after["missing"] = missing
	}
	if e.ConfigDiff != "" {
		after["configDiff"] = e.ConfigDiff
	}
	if !e.EnabledAt.IsZero() {
		after["enabledAt"] = e.EnabledAt.UTC().Format(time.RFC3339)
		after["expiresAt"] = e.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if e.Toggle != "" {
		after["toggle"] = string(e.Toggle)
		after["value"] = e.Value
	}
	if e.Operation != "" {
		after["operation"] = string(e.Operation)
	}
	if len(e.Restored) > 0 {
		restored := make([]string, len(e.Restored))
		for i, t := range e.Restored {
			restored[i] = string(t)
		}
		after["restored"] = restored
	}
	return AuditEntry{Event: devAuditEvents[e.Kind], Actor: domain.Actor(actor), TargetRef: "dev_mode", After: after}
}
