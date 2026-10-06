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
// authorize content. Bulk changes and templates (access_bulk.go) write
// restrictions through writeContentAccess here.

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
	var err error
	if v.ContentAccess, err = readRestrictions(ctx, tx, userID); err != nil {
		return domain.ContentAccessView{}, err
	}
	if v.Windows, err = readAccessWindows(ctx, tx, userID); err != nil {
		return domain.ContentAccessView{}, err
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

// readRestrictions reads the replaceable restrictions of a user: ceiling,
// unrated override, blocked tags and keywords.
func readRestrictions(ctx context.Context, tx pgx.Tx, userID string) (domain.ContentAccess, error) {
	var c domain.ContentAccess
	var ceiling *int16
	if err := tx.QueryRow(ctx, `SELECT parental_rating_max,block_unrated,ARRAY(SELECT tag FROM user_blocked_tags WHERE user_id=$1::uuid ORDER BY tag),
 ARRAY(SELECT keyword FROM user_blocked_keywords WHERE user_id=$1::uuid ORDER BY keyword)
 FROM users WHERE id=$1::uuid`, userID).Scan(&ceiling, &c.BlockUnrated, &c.BlockedTags, &c.BlockedKeywords); err != nil {
		return domain.ContentAccess{}, storageError(err)
	}
	if ceiling != nil {
		level := int(*ceiling)
		c.ParentalRatingMax = &level
	}
	return c, nil
}

// readAccessWindows reads a user's restricted time windows in order.
func readAccessWindows(ctx context.Context, tx pgx.Tx, userID string) ([]domain.AccessWindow, error) {
	rows, err := tx.Query(ctx, `SELECT weekdays,start_minute,end_minute,time_zone,rating_max FROM user_access_windows WHERE user_id=$1::uuid ORDER BY position LIMIT $2`, userID, domain.AccessWindowsMax)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	windows := make([]domain.AccessWindow, 0)
	for rows.Next() {
		var w domain.AccessWindow
		var weekdays []int16
		var start, end int16
		var ceiling *int16
		if err = rows.Scan(&weekdays, &start, &end, &w.TimeZone, &ceiling); err != nil {
			return nil, storageError(err)
		}
		w.Weekdays = make([]int, 0, len(weekdays))
		for _, d := range weekdays {
			w.Weekdays = append(w.Weekdays, int(d))
		}
		w.Start, w.End = domain.AccessClock(int(start)), domain.AccessClock(int(end))
		if ceiling != nil {
			level := int(*ceiling)
			w.RatingMax = &level
		}
		windows = append(windows, w)
	}
	return windows, storageError(rows.Err())
}

// refreshContentFiltered recomputes the filtered flag after a restriction
// was removed, so an unrestricted user skips the rule lookups again.
func refreshContentFiltered(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `UPDATE users SET content_filtered=parental_rating_max IS NOT NULL
 OR EXISTS(SELECT 1 FROM user_item_access_rules WHERE user_id=$1::uuid) OR EXISTS(SELECT 1 FROM user_blocked_tags WHERE user_id=$1::uuid)
 OR EXISTS(SELECT 1 FROM user_blocked_keywords WHERE user_id=$1::uuid) OR EXISTS(SELECT 1 FROM user_access_windows WHERE user_id=$1::uuid)
 WHERE id=$1::uuid`, userID)
	return storageError(err)
}

// contentAccessAudit is the audited state of a user's restrictions; item
// rules are audited by their own events.
func contentAccessAudit(c domain.ContentAccess) map[string]any {
	return map[string]any{"parentalRatingMax": c.ParentalRatingMax, "blockUnrated": c.BlockUnrated, "blockedTags": c.BlockedTags, "blockedKeywords": c.BlockedKeywords}
}

func sameContentAccess(a, b domain.ContentAccess) bool {
	sameCeiling := a.ParentalRatingMax == nil && b.ParentalRatingMax == nil || a.ParentalRatingMax != nil && b.ParentalRatingMax != nil && *a.ParentalRatingMax == *b.ParentalRatingMax
	sameUnrated := a.BlockUnrated == nil && b.BlockUnrated == nil || a.BlockUnrated != nil && b.BlockUnrated != nil && *a.BlockUnrated == *b.BlockUnrated
	return sameCeiling && sameUnrated && slices.Equal(a.BlockedTags, b.BlockedTags) && slices.Equal(a.BlockedKeywords, b.BlockedKeywords)
}

// writeContentAccess replaces the restrictions of a locked, live user and
// audits user.content_access_changed. It reports whether anything changed;
// an unchanged value writes no audit row (the caller still commits or
// rolls back as a whole). Blocked tags are stored trimmed and in the
// database's lower case, keywords NFKC-normalized as well: the forms the
// filter compares.
func writeContentAccess(ctx context.Context, tx pgx.Tx, actor domain.Actor, userID string, in domain.ContentAccess) (bool, error) {
	before, err := readRestrictions(ctx, tx, userID)
	if err != nil {
		return false, err
	}
	tags := make([]string, 0, len(in.BlockedTags))
	for _, tag := range in.BlockedTags {
		tags = append(tags, strings.TrimSpace(tag))
	}
	keywords := make([]string, 0, len(in.BlockedKeywords))
	for _, keyword := range in.BlockedKeywords {
		keywords = append(keywords, strings.TrimSpace(keyword))
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_blocked_tags WHERE user_id=$1::uuid`, userID); err != nil {
		return false, storageError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_blocked_tags(user_id,tag) SELECT DISTINCT $1::uuid,lower(btrim(t)) FROM unnest($2::text[]) t`, userID, tags); err != nil {
		return false, storageError(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_blocked_keywords WHERE user_id=$1::uuid`, userID); err != nil {
		return false, storageError(err)
	}
	// A keyword that normalizes to nothing (only spaces after NFKC) is
	// invalid rather than silently dropped.
	var dropped bool
	if err = tx.QueryRow(ctx, `WITH k AS (SELECT DISTINCT lower(btrim(normalize(t,NFKC))) AS keyword FROM unnest($2::text[]) t),
 ins AS (INSERT INTO user_blocked_keywords(user_id,keyword) SELECT $1::uuid,keyword FROM k WHERE keyword<>'' RETURNING 1)
 SELECT EXISTS(SELECT 1 FROM k WHERE keyword='')`, userID, keywords).Scan(&dropped); err != nil {
		return false, storageError(err)
	}
	if dropped {
		return false, domain.ErrInvalid
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET parental_rating_max=$2,block_unrated=$3,content_filtered=true WHERE id=$1::uuid`, userID, in.ParentalRatingMax, in.BlockUnrated); err != nil {
		return false, storageError(err)
	}
	if err = refreshContentFiltered(ctx, tx, userID); err != nil {
		return false, err
	}
	after, err := readRestrictions(ctx, tx, userID)
	if err != nil {
		return false, err
	}
	if sameContentAccess(before, after) {
		return false, nil
	}
	return true, auditAccount(ctx, tx, actor, "user.content_access_changed", userID, contentAccessAudit(before), contentAccessAudit(after))
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

// SetContentAccess replaces a user's rating ceiling, unrated override,
// blocked tags and keywords, and audits user.content_access_changed. An
// unchanged value is a no-op without an audit row.
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
	changed, err := writeContentAccess(ctx, tx, actor, userID, in)
	if err != nil {
		return domain.ContentAccessView{}, err
	}
	view, err := readContentAccess(ctx, tx, userID)
	if err != nil || !changed {
		// Nothing changed: leave no write behind.
		return view, err
	}
	return view, storageError(tx.Commit(ctx))
}

// SetAccessWindows replaces a user's restricted time windows (G48.4) and
// audits user.access_windows_changed. An unchanged list is a no-op without
// an audit row. Time zones PostgreSQL does not know are invalid: the
// filter reads every window in its zone and must never fail on one.
func (s *Store) SetAccessWindows(ctx context.Context, actor domain.Actor, userID string, windows []domain.AccessWindow) (domain.ContentAccessView, error) {
	if !domain.ValidAccessWindows(windows) {
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
	zones := make([]string, 0, len(windows))
	for _, w := range windows {
		zones = append(zones, w.TimeZone)
	}
	var unknown bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM unnest($1::text[]) z WHERE NOT EXISTS(SELECT 1 FROM pg_timezone_names n WHERE n.name=z))`, zones).Scan(&unknown); err != nil {
		return domain.ContentAccessView{}, storageError(err)
	}
	if unknown {
		return domain.ContentAccessView{}, domain.ErrInvalid
	}
	before, err := readAccessWindows(ctx, tx, userID)
	if err != nil {
		return domain.ContentAccessView{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_access_windows WHERE user_id=$1::uuid`, userID); err != nil {
		return domain.ContentAccessView{}, storageError(err)
	}
	for i, w := range windows {
		start, _ := domain.ParseAccessClock(w.Start, false)
		end, _ := domain.ParseAccessClock(w.End, true)
		weekdays := slices.Clone(w.Weekdays)
		if weekdays == nil {
			weekdays = []int{}
		}
		slices.Sort(weekdays)
		if _, err = tx.Exec(ctx, `INSERT INTO user_access_windows(user_id,position,weekdays,start_minute,end_minute,time_zone,rating_max) VALUES($1::uuid,$2,$3::int[]::smallint[],$4,$5,$6,$7)`,
			userID, i, weekdays, start, end, w.TimeZone, w.RatingMax); err != nil {
			return domain.ContentAccessView{}, storageError(err)
		}
	}
	if err = refreshContentFiltered(ctx, tx, userID); err != nil {
		return domain.ContentAccessView{}, err
	}
	view, err := readContentAccess(ctx, tx, userID)
	if err != nil {
		return view, err
	}
	if slices.EqualFunc(before, view.Windows, sameAccessWindow) {
		return view, nil
	}
	if err = auditAccount(ctx, tx, actor, "user.access_windows_changed", userID, map[string]any{"windows": before}, map[string]any{"windows": view.Windows}); err != nil {
		return domain.ContentAccessView{}, err
	}
	return view, storageError(tx.Commit(ctx))
}

func sameAccessWindow(a, b domain.AccessWindow) bool {
	sameCeiling := a.RatingMax == nil && b.RatingMax == nil || a.RatingMax != nil && b.RatingMax != nil && *a.RatingMax == *b.RatingMax
	return sameCeiling && slices.Equal(a.Weekdays, b.Weekdays) && a.Start == b.Start && a.End == b.End && a.TimeZone == b.TimeZone
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
	result, err := listParentalRatings(ctx, tx)
	if err != nil {
		return nil, err
	}
	return result, storageError(tx.Commit(ctx))
}

// ReplaceParentalRatings replaces the rating code table (G48.4) and audits
// access.rating_codes_changed with the codes added, removed and
// relevelled. Codes are stored the way the filter normalizes item values;
// a code it could never match (a leading "Rated " or two-letter country
// prefix) and two codes that normalize alike are invalid. An unchanged
// table is a no-op without an audit row. Administrators only.
func (s *Store) ReplaceParentalRatings(ctx context.Context, actor domain.Actor, ratings []domain.ParentalRating) ([]domain.ParentalRating, error) {
	if !domain.ValidParentalRatings(ratings) {
		return nil, domain.ErrInvalid
	}
	codes := make([]string, 0, len(ratings))
	levels := make([]int, 0, len(ratings))
	for _, r := range ratings {
		codes, levels = append(codes, r.Code), append(levels, r.Level)
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `LOCK TABLE parental_ratings IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return nil, storageError(err)
	}
	var valid bool
	if err = tx.QueryRow(ctx, `WITH n AS (SELECT upper(btrim(c)) AS code,`+parentalRatingCodeSQL("c")+` AS normal FROM unnest($1::text[]) c)
 SELECT NOT EXISTS(SELECT 1 FROM n WHERE code<>normal OR code='') AND (SELECT count(DISTINCT code) FROM n)=(SELECT count(*) FROM n)`, codes).Scan(&valid); err != nil {
		return nil, storageError(err)
	}
	if !valid {
		return nil, domain.ErrInvalid
	}
	type change struct {
		Code   string `json:"code"`
		Before *int16 `json:"before"`
		After  *int16 `json:"after"`
	}
	rows, err := tx.Query(ctx, `WITH n AS (SELECT upper(btrim(c)) AS code,l::smallint AS level FROM unnest($1::text[],$2::int[]) t(c,l))
 SELECT COALESCE(n.code,p.code),p.level,n.level FROM n FULL JOIN parental_ratings p ON p.code=n.code
 WHERE p.level IS DISTINCT FROM n.level ORDER BY 1`, codes, levels)
	if err != nil {
		return nil, storageError(err)
	}
	changes := make([]change, 0)
	for rows.Next() {
		var c change
		if err = rows.Scan(&c.Code, &c.Before, &c.After); err != nil {
			rows.Close()
			return nil, storageError(err)
		}
		changes = append(changes, c)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	if len(changes) > 0 {
		if _, err = tx.Exec(ctx, `DELETE FROM parental_ratings WHERE code<>ALL(SELECT upper(btrim(c)) FROM unnest($1::text[]) c)`, codes); err != nil {
			return nil, storageError(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO parental_ratings(code,level) SELECT upper(btrim(c)),l::smallint FROM unnest($1::text[],$2::int[]) t(c,l)
 ON CONFLICT(code) DO UPDATE SET level=EXCLUDED.level WHERE parental_ratings.level<>EXCLUDED.level`, codes, levels); err != nil {
			return nil, storageError(err)
		}
		if err = appendAudit(ctx, tx, AuditEntry{Event: "access.rating_codes_changed", Actor: actor, After: map[string]any{"changes": changes}}); err != nil {
			return nil, err
		}
	}
	result, err := listParentalRatings(ctx, tx)
	if err != nil {
		return nil, err
	}
	return result, storageError(tx.Commit(ctx))
}

func listParentalRatings(ctx context.Context, tx pgx.Tx) ([]domain.ParentalRating, error) {
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
	return result, storageError(rows.Err())
}
