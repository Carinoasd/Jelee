package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// SessionActive reports whether a session may keep streaming: it is not
// revoked or expired and its user is neither disabled nor deleted. Running
// streams poll it (G07.4), so a revocation made through any instance stops
// the streams every instance serves. Malformed IDs are simply inactive.
func (s *Store) SessionActive(ctx context.Context, userID, sessionID string) (bool, error) {
	if !domain.ValidID(userID) || !domain.ValidID(sessionID) {
		return false, nil
	}
	var active bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.id=$2::uuid AND s.user_id=$1::uuid AND s.revoked_at IS NULL AND s.expires_at>now() AND NOT u.disabled AND u.deleted_at IS NULL AND `+guestLiveSQL+`)`, userID, sessionID).Scan(&active)
	if err != nil {
		return false, storageError(err)
	}
	return active, nil
}

// GetDeliveryLimits reads a user's direct delivery overrides. Administrators only.
func (s *Store) GetDeliveryLimits(ctx context.Context, actor domain.Actor, userID string) (domain.DeliveryLimits, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.DeliveryLimits{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(userID) {
		return domain.DeliveryLimits{}, domain.ErrNotFound
	}
	limits, err := scanDeliveryLimits(tx.QueryRow(ctx, `SELECT max_streams,max_kbps FROM users WHERE id=$1::uuid AND deleted_at IS NULL`, userID))
	if err != nil {
		return limits, err
	}
	return limits, storageError(tx.Commit(ctx))
}

// SetDeliveryLimits replaces a user's direct delivery overrides and audits
// the change. An unchanged value is a no-op without an audit row.
func (s *Store) SetDeliveryLimits(ctx context.Context, actor domain.Actor, userID string, limits domain.DeliveryLimits) (domain.DeliveryLimits, error) {
	if !limits.Valid() {
		return domain.DeliveryLimits{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.DeliveryLimits{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(userID) {
		return domain.DeliveryLimits{}, domain.ErrNotFound
	}
	old, err := scanDeliveryLimits(tx.QueryRow(ctx, `SELECT max_streams,max_kbps FROM users WHERE id=$1::uuid AND deleted_at IS NULL FOR UPDATE`, userID))
	if err != nil {
		return old, err
	}
	if old.Equal(limits) {
		return old, storageError(tx.Commit(ctx))
	}
	updated, err := scanDeliveryLimits(tx.QueryRow(ctx, `UPDATE users SET max_streams=$2,max_kbps=$3 WHERE id=$1::uuid RETURNING max_streams,max_kbps`, userID, limits.MaxStreams, limits.MaxKbps))
	if err != nil {
		return updated, err
	}
	if err = auditAccount(ctx, tx, actor, "user.delivery_limits_changed", userID, deliveryLimitsAudit(old), deliveryLimitsAudit(updated)); err != nil {
		return domain.DeliveryLimits{}, err
	}
	return updated, storageError(tx.Commit(ctx))
}

func scanDeliveryLimits(row interface{ Scan(...any) error }) (domain.DeliveryLimits, error) {
	var limits domain.DeliveryLimits
	if err := row.Scan(&limits.MaxStreams, &limits.MaxKbps); err != nil {
		return domain.DeliveryLimits{}, storageError(err)
	}
	return limits, nil
}

// deliveryLimitsAudit records cleared overrides as explicit nulls so the audit
// row distinguishes "follows the server-wide value" from "unlimited" (0).
func deliveryLimitsAudit(l domain.DeliveryLimits) map[string]any {
	return map[string]any{"maxStreams": l.MaxStreams, "maxKbps": l.MaxKbps}
}
