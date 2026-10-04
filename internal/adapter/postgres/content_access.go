package postgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Content access administration (G48.1, G48.4, G48.7 audit). This file
// writes and lists the rule tables; only visibility.go reads them to
// authorize content.

// contentAccessTarget locks a live (not deleted) target user.
func contentAccessTarget(ctx context.Context, tx pgx.Tx, userID string) error {
	u, err := userInTransaction(ctx, tx, userID)
	if err != nil {
		return err
	}
	if u.DeletedAt != nil {
		return domain.ErrNotFound
	}
	return nil
}

func readContentAccess(ctx context.Context, tx pgx.Tx, userID string) (domain.ContentAccessView, error) {
	var v domain.ContentAccessView
	var ceiling *int16
	if err := tx.QueryRow(ctx, `SELECT parental_rating_max,block_unrated,ARRAY(SELECT tag FROM user_blocked_tags WHERE user_id=$1::uuid ORDER BY tag)
 FROM users WHERE id=$1::uuid`, userID).Scan(&ceiling, &v.BlockUnrated, &v.BlockedTags); err != nil {
		return domain.ContentAccessView{}, storageError(err)
	}
	if ceiling != nil {
		level := int(*ceiling)
		v.ParentalRatingMax = &level
	}
	rows, err := tx.Query(ctx, `SELECT r.item_id::text,i.library_id::text,i.kind,i.title,r.effect,r.created_at FROM user_item_access_rules r
 JOIN items i ON i.id=r.item_id WHERE r.user_id=$1::uuid ORDER BY r.created_at,r.item_id LIMIT $2`, userID, domain.ItemAccessRulesMax)
	if err != nil {
		return domain.ContentAccessView{}, storageError(err)
	}
	defer rows.Close()
	v.Rules = make([]domain.ItemAccessRule, 0)
	for rows.Next() {
		var r domain.ItemAccessRule
		var effect string
		if err = rows.Scan(&r.ItemID, &r.LibraryID, &r.Kind, &r.Title, &effect, &r.CreatedAt); err != nil {
			return domain.ContentAccessView{}, storageError(err)
		}
		r.Effect, r.CreatedAt = domain.ItemAccessEffect(effect), r.CreatedAt.UTC()
		v.Rules = append(v.Rules, r)
	}
	return v, storageError(rows.Err())
}

// refreshContentFiltered recomputes the filtered flag after a restriction
// was removed, so an unrestricted user skips the rule lookups again.
func refreshContentFiltered(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `UPDATE users SET content_filtered=parental_rating_max IS NOT NULL
 OR EXISTS(SELECT 1 FROM user_item_access_rules WHERE user_id=$1::uuid) OR EXISTS(SELECT 1 FROM user_blocked_tags WHERE user_id=$1::uuid)
 WHERE id=$1::uuid`, userID)
	return storageError(err)
}

// contentAccessAudit is the audited state of a user's restrictions; item
// rules are audited by their own events.
func contentAccessAudit(c domain.ContentAccess) map[string]any {
	return map[string]any{"parentalRatingMax": c.ParentalRatingMax, "blockUnrated": c.BlockUnrated, "blockedTags": c.BlockedTags}
}

func sameContentAccess(a, b domain.ContentAccess) bool {
	sameCeiling := a.ParentalRatingMax == nil && b.ParentalRatingMax == nil || a.ParentalRatingMax != nil && b.ParentalRatingMax != nil && *a.ParentalRatingMax == *b.ParentalRatingMax
	sameUnrated := a.BlockUnrated == nil && b.BlockUnrated == nil || a.BlockUnrated != nil && b.BlockUnrated != nil && *a.BlockUnrated == *b.BlockUnrated
	return sameCeiling && sameUnrated && slices.Equal(a.BlockedTags, b.BlockedTags)
}

// GetContentAccess reads a user's content restrictions and item rules.
// Administrators only.
func (s *Store) GetContentAccess(ctx context.Context, actor domain.Actor, userID string) (domain.ContentAccessView, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ContentAccessView{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(userID) {
		return domain.ContentAccessView{}, domain.ErrNotFound
	}
	if err = contentAccessTarget(ctx, tx, userID); err != nil {
		return domain.ContentAccessView{}, err
	}
	v, err := readContentAccess(ctx, tx, userID)
	if err != nil {
		return v, err
	}
	return v, storageError(tx.Commit(ctx))
}

// SetContentAccess replaces a user's rating ceiling, unrated override and
// blocked tags, and audits user.content_access_changed. An unchanged value
// is a no-op without an audit row. Blocked tags are stored trimmed and in
// the database's lower case, the form the filter compares.
func (s *Store) SetContentAccess(ctx context.Context, actor domain.Actor, userID string, in domain.ContentAccess) (domain.ContentAccessView, error) {
	if !in.Valid() {
		return domain.ContentAccessView{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ContentAccessView{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(userID) {
		return domain.ContentAccessView{}, domain.ErrNotFound
	}
	if err = contentAccessTarget(ctx, tx, userID); err != nil {
		return domain.ContentAccessView{}, err
	}
	before, err := readContentAccess(ctx, tx, userID)
	if err != nil {
		return before, err
	}
	tags := make([]string, 0, len(in.BlockedTags))
	for _, tag := range in.BlockedTags {
		tags = append(tags, strings.TrimSpace(tag))
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_blocked_tags WHERE user_id=$1::uuid`, userID); err != nil {
		return domain.ContentAccessView{}, storageError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_blocked_tags(user_id,tag) SELECT DISTINCT $1::uuid,lower(btrim(t)) FROM unnest($2::text[]) t`, userID, tags); err != nil {
		return domain.ContentAccessView{}, storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET parental_rating_max=$2,block_unrated=$3,content_filtered=true WHERE id=$1::uuid`, userID, in.ParentalRatingMax, in.BlockUnrated); err != nil {
		return domain.ContentAccessView{}, storageError(err)
	}
	if err = refreshContentFiltered(ctx, tx, userID); err != nil {
		return domain.ContentAccessView{}, err
	}
	after, err := readContentAccess(ctx, tx, userID)
	if err != nil {
		return after, err
	}
	if sameContentAccess(before.ContentAccess, after.ContentAccess) {
		// Nothing changed: leave no audit row and no write.
		return before, nil
	}
	if err = auditAccount(ctx, tx, actor, "user.content_access_changed", userID, contentAccessAudit(before.ContentAccess), contentAccessAudit(after.ContentAccess)); err != nil {
		return domain.ContentAccessView{}, err
	}
	return after, storageError(tx.Commit(ctx))
}

// SetItemAccessRule creates or replaces the explicit rule of a user on an
// item, in any library, and audits user.item_access_rule_set. An unchanged
// rule is a no-op without an audit row; a user holds at most
// ItemAccessRulesMax rules.
func (s *Store) SetItemAccessRule(ctx context.Context, actor domain.Actor, userID, itemID string, effect domain.ItemAccessEffect) (domain.ItemAccessRule, error) {
	if !effect.Valid() {
		return domain.ItemAccessRule{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ItemAccessRule{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(userID) || !domain.ValidID(itemID) {
		return domain.ItemAccessRule{}, domain.ErrNotFound
	}
	if err = contentAccessTarget(ctx, tx, userID); err != nil {
		return domain.ItemAccessRule{}, err
	}
	var before *string
	var count int
	err = tx.QueryRow(ctx, `SELECT (SELECT effect FROM user_item_access_rules WHERE user_id=$1::uuid AND item_id=i.id),
 (SELECT count(*) FROM user_item_access_rules WHERE user_id=$1::uuid) FROM items i WHERE i.id=$2::uuid FOR KEY SHARE OF i`, userID, itemID).Scan(&before, &count)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ItemAccessRule{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ItemAccessRule{}, storageError(err)
	}
	if before == nil && count >= domain.ItemAccessRulesMax {
		return domain.ItemAccessRule{}, domain.ErrConflict
	}
	var r domain.ItemAccessRule
	var stored string
	if err = tx.QueryRow(ctx, `INSERT INTO user_item_access_rules AS r(user_id,item_id,effect) VALUES($1::uuid,$2::uuid,$3)
 ON CONFLICT (user_id,item_id) DO UPDATE SET effect=EXCLUDED.effect
 RETURNING r.item_id::text,(SELECT library_id::text FROM items WHERE id=r.item_id),(SELECT kind FROM items WHERE id=r.item_id),(SELECT title FROM items WHERE id=r.item_id),r.effect,r.created_at`,
		userID, itemID, string(effect)).Scan(&r.ItemID, &r.LibraryID, &r.Kind, &r.Title, &stored, &r.CreatedAt); err != nil {
		return domain.ItemAccessRule{}, storageError(err)
	}
	r.Effect, r.CreatedAt = domain.ItemAccessEffect(stored), r.CreatedAt.UTC()
	if before != nil && *before == stored {
		return r, nil
	}
	var old any
	if before != nil {
		old = map[string]any{"itemId": itemID, "effect": *before}
	}
	if err = auditAccount(ctx, tx, actor, "user.item_access_rule_set", userID, old, map[string]any{"itemId": itemID, "effect": stored}); err != nil {
		return domain.ItemAccessRule{}, err
	}
	return r, storageError(tx.Commit(ctx))
}

// DeleteItemAccessRule removes a user's rule on an item and audits
// user.item_access_rule_removed. A missing rule is ErrNotFound.
func (s *Store) DeleteItemAccessRule(ctx context.Context, actor domain.Actor, userID, itemID string) error {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(userID) || !domain.ValidID(itemID) {
		return domain.ErrNotFound
	}
	if err = contentAccessTarget(ctx, tx, userID); err != nil {
		return err
	}
	var effect string
	err = tx.QueryRow(ctx, `DELETE FROM user_item_access_rules WHERE user_id=$1::uuid AND item_id=$2::uuid RETURNING effect`, userID, itemID).Scan(&effect)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return storageError(err)
	}
	if err = refreshContentFiltered(ctx, tx, userID); err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, actor, "user.item_access_rule_removed", userID, map[string]any{"itemId": itemID, "effect": effect}, nil); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// GetAccessPolicy reads the server-wide content access policy.
// Administrators only.
func (s *Store) GetAccessPolicy(ctx context.Context, actor domain.Actor) (domain.AccessPolicy, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.AccessPolicy{}, err
	}
	defer tx.Rollback(ctx)
	p, err := readAccessPolicy(ctx, tx, false)
	if err != nil {
		return p, err
	}
	return p, storageError(tx.Commit(ctx))
}

// readAccessPolicy reads the policy row; a missing row reads as the
// defaults the filter also assumes.
func readAccessPolicy(ctx context.Context, tx pgx.Tx, lock bool) (domain.AccessPolicy, error) {
	query := `SELECT restrict_admins,block_unrated FROM access_policy`
	if lock {
		query += ` FOR UPDATE`
	}
	var p domain.AccessPolicy
	err := tx.QueryRow(ctx, query).Scan(&p.RestrictAdmins, &p.BlockUnrated)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AccessPolicy{}, nil
	}
	return p, storageError(err)
}

// SetAccessPolicy replaces the server-wide policy and audits
// access.policy_changed. An unchanged policy is a no-op without an audit
// row.
func (s *Store) SetAccessPolicy(ctx context.Context, actor domain.Actor, p domain.AccessPolicy) (domain.AccessPolicy, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.AccessPolicy{}, err
	}
	defer tx.Rollback(ctx)
	before, err := readAccessPolicy(ctx, tx, true)
	if err != nil {
		return before, err
	}
	if before == p {
		return before, storageError(tx.Commit(ctx))
	}
	if _, err = tx.Exec(ctx, `INSERT INTO access_policy(id,restrict_admins,block_unrated) VALUES(true,$1,$2)
 ON CONFLICT (id) DO UPDATE SET restrict_admins=EXCLUDED.restrict_admins,block_unrated=EXCLUDED.block_unrated,updated_at=now()`, p.RestrictAdmins, p.BlockUnrated); err != nil {
		return domain.AccessPolicy{}, storageError(err)
	}
	audit := func(v domain.AccessPolicy) map[string]any {
		return map[string]any{"restrictAdmins": v.RestrictAdmins, "blockUnrated": v.BlockUnrated}
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "access.policy_changed", Actor: actor, Before: audit(before), After: audit(p)}); err != nil {
		return domain.AccessPolicy{}, err
	}
	return p, storageError(tx.Commit(ctx))
}

// ListParentalRatings returns the recognized rating codes by level and
// code. Administrators only.
func (s *Store) ListParentalRatings(ctx context.Context, actor domain.Actor) ([]domain.ParentalRating, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT code,level FROM parental_ratings ORDER BY level,code LIMIT 1000`)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	result := make([]domain.ParentalRating, 0, 64)
	for rows.Next() {
		var r domain.ParentalRating
		if err = rows.Scan(&r.Code, &r.Level); err != nil {
			return nil, storageError(err)
		}
		result = append(result, r)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return result, storageError(tx.Commit(ctx))
}
