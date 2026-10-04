package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// AuditEntry is one audit row to append. TargetID must be a UUID; TargetRef
// carries any other target. At most one of them may be set.
type AuditEntry struct {
	Event     string
	Actor     domain.Actor
	TargetID  string
	TargetRef string
	RequestID string
	Before    any
	After     any
}

// auditEvents is the closed set of events that may be recorded, mapped to the
// category that decides their retention. Unknown events are rejected so a
// typo cannot create an unqueryable or wrongly retained stream.
var auditEvents = map[string]string{
	"audit.retention_changed":              domain.AuditCategoryAudit,
	"audit.retention_purged":               domain.AuditCategoryAudit,
	"catalog_import.finished":              domain.AuditCategoryAudit,
	"catalog_import.submitted":             domain.AuditCategoryAudit,
	"directory.registered":                 domain.AuditCategoryAudit,
	"inventory.imported":                   domain.AuditCategoryAudit,
	"image.added":                          domain.AuditCategoryAudit,
	"image.deleted":                        domain.AuditCategoryAudit,
	"image.locked":                         domain.AuditCategoryAudit,
	"image.replaced":                       domain.AuditCategoryAudit,
	"image.unlocked":                       domain.AuditCategoryAudit,
	"item.external_metadata_removed":       domain.AuditCategoryAudit,
	"job.recovered":                        domain.AuditCategoryAudit,
	"nfo.write_submitted":                  domain.AuditCategoryAudit,
	"job.missing_accepted":                 domain.AuditCategoryAudit,
	"inventory.baseline_accepted":          domain.AuditCategoryAudit,
	"catalog_sync.submitted":               domain.AuditCategoryAudit,
	"library.catalog_sync_changed":         domain.AuditCategoryAudit,
	"catalog_sync.batch":                   domain.AuditCategoryAudit,
	"catalog_sync.removed":                 domain.AuditCategoryAudit,
	"catalog_sync.finished":                domain.AuditCategoryAudit,
	"catalog_sync.deferred":                domain.AuditCategoryAudit,
	"item.metadata_changed":                domain.AuditCategoryAudit,
	"item.nfo_metadata_applied":            domain.AuditCategoryAudit,
	"item.tmdb_metadata_applied":           domain.AuditCategoryAudit,
	"job.cancel_requested":                 domain.AuditCategoryAudit,
	"job.finished":                         domain.AuditCategoryAudit,
	"job.submitted":                        domain.AuditCategoryAudit,
	"library.metadata_preferences_changed": domain.AuditCategoryAudit,
	"library.registered":                   domain.AuditCategoryAudit,
	"login.failed":                         domain.AuditCategorySecurity,
	"login.native_denied":                  domain.AuditCategorySecurity,
	"media.registered":                     domain.AuditCategoryAudit,
	"media.sidecars_changed":               domain.AuditCategoryAudit,
	"nfo.policy_changed":                   domain.AuditCategoryAudit,
	"nfo.write_prepared":                   domain.AuditCategoryAudit,
	"playback.history_cleared":             domain.AuditCategoryAudit,
	"probe.item_invalidated":               domain.AuditCategoryAudit,
	"probe.library_invalidated":            domain.AuditCategoryAudit,
	"schedule.updated":                     domain.AuditCategoryAudit,
	"session.created":                      domain.AuditCategoryAudit,
	"session.revoked":                      domain.AuditCategoryAudit,
	"session.rotated":                      domain.AuditCategoryAudit,
	"sessions.revoked":                     domain.AuditCategoryAudit,
	"user.bootstrapped":                    domain.AuditCategoryAudit,
	"user.created":                         domain.AuditCategoryAudit,
	"user.deleted":                         domain.AuditCategoryAudit,
	"user.library_access_replaced":         domain.AuditCategoryAudit,
	"user.native_access_changed":           domain.AuditCategoryAudit,
	"user.delivery_limits_changed":         domain.AuditCategoryAudit,
	"user.password_changed":                domain.AuditCategoryAudit,
	"user.password_reset_local":            domain.AuditCategoryAudit,
	"user.profile_changed":                 domain.AuditCategoryAudit,
	"user.provisioned":                     domain.AuditCategoryAudit,
	"user.restored":                        domain.AuditCategoryAudit,
	"user.unlocked":                        domain.AuditCategoryAudit,
	"user.updated":                         domain.AuditCategoryAudit,
}

// auditStateLimit bounds each encoded before/after state. Larger states are
// replaced by a size and digest marker instead of failing the audited change.
// The database enforces a looser backstop on its own text rendering.
const auditStateLimit = 32 << 10

const auditRedacted = "[redacted]"

// Normalized (lower case, separators removed) keys whose values are never stored.
var auditSecretKeys = map[string]bool{
	"authorization": true, "cookie": true, "setcookie": true, "dsn": true,
	"databaseurl": true, "connectionstring": true, "privatekey": true,
	"passwd": true, "credential": true, "credentials": true,
}

// Normalized key suffixes whose values are never stored, e.g. newPassword,
// refresh_token, webhookSecret, apiKey, tokenHash.
var auditSecretSuffixes = []string{"password", "passwordhash", "token", "tokenhash", "secret", "apikey"}

func auditCategory(event string) (string, bool) {
	category, ok := auditEvents[event]
	return category, ok
}

func auditSecretKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		switch r {
		case '_', '-', '.', ' ':
			return -1
		}
		return r
	}, strings.ToLower(key))
	if auditSecretKeys[normalized] {
		return true
	}
	for _, suffix := range auditSecretSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func auditSecretValue(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "$argon2")
}

func redactAuditValue(v any) any {
	switch value := v.(type) {
	case map[string]any:
		for key, child := range value {
			if auditSecretKey(key) {
				value[key] = auditRedacted
			} else {
				value[key] = redactAuditValue(child)
			}
		}
		return value
	case []any:
		for i, child := range value {
			value[i] = redactAuditValue(child)
		}
		return value
	case string:
		if auditSecretValue(value) {
			return auditRedacted
		}
	}
	return v
}

// auditState encodes one before/after value with secrets removed and the size
// bounded. A nil value is stored as an empty object, as before.
func auditState(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, domain.ErrDatabase
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err = decoder.Decode(&decoded); err != nil {
		return nil, domain.ErrDatabase
	}
	clean, err := json.Marshal(redactAuditValue(decoded))
	if err != nil {
		return nil, domain.ErrDatabase
	}
	if len(clean) > auditStateLimit {
		digest := sha256.Sum256(clean)
		return json.Marshal(map[string]any{"truncated": true, "bytes": len(clean), "sha256": hex.EncodeToString(digest[:])})
	}
	return clean, nil
}

func validAuditRequestID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

func validAuditTargetRef(ref string) bool {
	return validText(ref, 256, false)
}

// appendAudit is the only writer of audit_logs. It runs inside the caller's
// transaction so the audit row commits or rolls back with the change itself.
func appendAudit(ctx context.Context, tx pgx.Tx, e AuditEntry) error {
	category, ok := auditCategory(e.Event)
	if !ok || e.TargetID != "" && e.TargetRef != "" || e.TargetRef != "" && !validAuditTargetRef(e.TargetRef) {
		return domain.ErrDatabase
	}
	before, err := auditState(e.Before)
	if err != nil {
		return err
	}
	after, err := auditState(e.After)
	if err != nil {
		return err
	}
	ip := ""
	if addr, parseErr := netip.ParseAddr(e.Actor.IP); parseErr == nil {
		ip = addr.Unmap().String()
	}
	// A malformed correlation id is dropped rather than failing the change.
	request := ""
	if validAuditRequestID(e.RequestID) {
		request = e.RequestID
	}
	// Columns added with the append-only schema are named only when they carry
	// a non-default value, so an ordinary entry is the same statement the
	// account schema has always accepted.
	columns := `event,target_id,actor_id,actor_ip,before_state,after_state`
	values := `$1,NULLIF($2,'')::uuid,NULLIF($3,'')::uuid,NULLIF($4,'')::inet,$5::jsonb,$6::jsonb`
	args := []any{e.Event, e.TargetID, e.Actor.UserID, ip, before, after}
	for _, extra := range []struct{ column, value string }{{"category", category}, {"target_ref", e.TargetRef}, {"request_id", request}} {
		if extra.value == "" || extra.column == "category" && extra.value == domain.AuditCategoryAudit {
			continue
		}
		args = append(args, extra.value)
		columns += "," + extra.column
		values += ",$" + strconv.Itoa(len(args))
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_logs(`+columns+`) VALUES(`+values+`)`, args...)
	return storageError(err)
}

const auditColumns = `id,category,event,COALESCE(target_id::text,''),COALESCE(target_ref,''),COALESCE(actor_id::text,''),COALESCE(host(actor_ip),''),COALESCE(request_id,''),before_state::text,after_state::text,occurred_at`

func validAuditFilter(f domain.AuditFilter) bool {
	if f.Category != "" && f.Category != domain.AuditCategoryAudit && f.Category != domain.AuditCategorySecurity {
		return false
	}
	if f.Event != "" {
		if _, ok := auditCategory(f.Event); !ok {
			return false
		}
	}
	if f.ActorID != "" && !domain.ValidID(f.ActorID) {
		return false
	}
	if f.Target != "" && !domain.ValidID(f.Target) && !validAuditTargetRef(f.Target) {
		return false
	}
	return f.Since.IsZero() || f.Until.IsZero() || f.Since.Before(f.Until)
}

func parseAuditCursor(cursor string) (int64, bool) {
	if cursor == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(cursor, 10, 64)
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == cursor
}

func auditTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// ListAudit returns audit rows newest first for an administrator. The cursor
// is the opaque next value returned by the previous page; an empty next value
// means the listing is complete.
func (s *Store) ListAudit(ctx context.Context, actor domain.Actor, filter domain.AuditFilter, cursor string, limit int) ([]domain.AuditRecord, string, error) {
	before, ok := parseAuditCursor(cursor)
	if !ok || limit < 1 || limit > 100 || !validAuditFilter(filter) {
		return nil, "", domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)
	targetID, targetRef := "", ""
	if domain.ValidID(filter.Target) {
		targetID = filter.Target
	} else {
		targetRef = filter.Target
	}
	rows, err := tx.Query(ctx, `SELECT `+auditColumns+` FROM audit_logs
	 WHERE ($1=0 OR id<$1) AND ($2='' OR category=$2) AND ($3='' OR event=$3)
	 AND (NULLIF($4,'')::uuid IS NULL OR actor_id=NULLIF($4,'')::uuid) AND (NULLIF($5,'')::uuid IS NULL OR target_id=NULLIF($5,'')::uuid) AND ($6='' OR target_ref=$6)
	 AND ($7::timestamptz IS NULL OR occurred_at>=$7) AND ($8::timestamptz IS NULL OR occurred_at<$8)
	 ORDER BY id DESC LIMIT $9`,
		before, filter.Category, filter.Event, filter.ActorID, targetID, targetRef, auditTime(filter.Since), auditTime(filter.Until), limit+1)
	if err != nil {
		return nil, "", storageError(err)
	}
	records := make([]domain.AuditRecord, 0, limit)
	for rows.Next() {
		var r domain.AuditRecord
		var b, a string
		if err = rows.Scan(&r.ID, &r.Category, &r.Event, &r.TargetID, &r.TargetRef, &r.ActorID, &r.ActorIP, &r.RequestID, &b, &a, &r.OccurredAt); err != nil {
			rows.Close()
			return nil, "", storageError(err)
		}
		r.Before, r.After = json.RawMessage(b), json.RawMessage(a)
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", storageError(err)
	}
	next := ""
	if len(records) > limit {
		records = records[:limit]
		next = strconv.FormatInt(records[limit-1].ID, 10)
	}
	return records, next, storageError(tx.Commit(ctx))
}

func scanAuditRetention(row pgx.Row) (domain.AuditRetention, error) {
	var r domain.AuditRetention
	err := row.Scan(&r.AuditDays, &r.SecurityDays, &r.Revision, &r.UpdatedAt)
	return r, storageError(err)
}

// AuditRetention returns the audit and security retention for an administrator.
func (s *Store) AuditRetention(ctx context.Context, actor domain.Actor) (domain.AuditRetention, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.AuditRetention{}, err
	}
	defer tx.Rollback(ctx)
	r, err := scanAuditRetention(tx.QueryRow(ctx, `SELECT audit_days,security_days,revision,updated_at FROM audit_retention WHERE singleton`))
	if err != nil {
		return domain.AuditRetention{}, err
	}
	return r, storageError(tx.Commit(ctx))
}

func validAuditRetentionDays(days int) bool {
	return days >= domain.AuditRetentionMinDays && days <= domain.AuditRetentionMaxDays
}

// SetAuditRetention changes both retention periods and audits the change.
func (s *Store) SetAuditRetention(ctx context.Context, actor domain.Actor, auditDays, securityDays int) (domain.AuditRetention, error) {
	if !validAuditRetentionDays(auditDays) || !validAuditRetentionDays(securityDays) {
		return domain.AuditRetention{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.AuditRetention{}, err
	}
	defer tx.Rollback(ctx)
	old, err := scanAuditRetention(tx.QueryRow(ctx, `SELECT audit_days,security_days,revision,updated_at FROM audit_retention WHERE singleton FOR UPDATE`))
	if err != nil {
		return domain.AuditRetention{}, err
	}
	if old.AuditDays == auditDays && old.SecurityDays == securityDays {
		return old, storageError(tx.Commit(ctx))
	}
	r, err := scanAuditRetention(tx.QueryRow(ctx, `UPDATE audit_retention SET audit_days=$1,security_days=$2,revision=revision+1,updated_at=now() WHERE singleton RETURNING audit_days,security_days,revision,updated_at`, auditDays, securityDays))
	if err != nil {
		return domain.AuditRetention{}, err
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "audit.retention_changed", Actor: actor, TargetRef: "audit_retention", Before: old, After: r}); err != nil {
		return domain.AuditRetention{}, err
	}
	return r, storageError(tx.Commit(ctx))
}

// PurgeAudit removes at most batch rows older than their category's retention
// through purge_audit_logs, the only deletion path the table accepts, and
// records the purge itself. Callers repeat it until both counts are zero.
func (s *Store) PurgeAudit(ctx context.Context, batch int) (domain.AuditPurgeResult, error) {
	if batch < 1 || batch > 10000 {
		return domain.AuditPurgeResult{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.AuditPurgeResult{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT purged_category,purged FROM purge_audit_logs(current_schema(),$1)`, batch)
	if err != nil {
		return domain.AuditPurgeResult{}, storageError(err)
	}
	var result domain.AuditPurgeResult
	for rows.Next() {
		var category string
		var count int64
		if err = rows.Scan(&category, &count); err != nil {
			rows.Close()
			return domain.AuditPurgeResult{}, storageError(err)
		}
		switch category {
		case domain.AuditCategoryAudit:
			result.Audit = count
		case domain.AuditCategorySecurity:
			result.Security = count
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return domain.AuditPurgeResult{}, storageError(err)
	}
	if result.Audit+result.Security > 0 {
		if err = appendAudit(ctx, tx, AuditEntry{Event: "audit.retention_purged", TargetRef: "audit_logs", After: result}); err != nil {
			return domain.AuditPurgeResult{}, err
		}
	}
	return result, storageError(tx.Commit(ctx))
}
