package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Client control storage (G47): rules, the server-wide policy, known
// clients and aggregated hit records. Every administrator change runs in an
// authorized transaction, takes the policy row lock first (so rule limits
// and the version are serialized), bumps the version when it changes an
// evaluation, and is audited.

const clientRuleColumns = `id::text,dimension,COALESCE(header_name,''),match_kind,pattern,case_fold,priority,action,COALESCE(intent,''),rate_requests,rate_period_seconds,
 scope_kind,scope_values,window_from,window_until,COALESCE(daily_start,''),COALESCE(daily_end,''),weekdays,time_zone,enabled,note,hit_count,last_hit_at,created_at,updated_at,libraries::text[]`

const clientPolicyColumns = `unknown_clients,exempt_admins,exempt_loopback,version,updated_at`

// clientRegexRulesMax mirrors the engine's default regex rule limit.
const clientRegexRulesMax = 1000

// knownClientsMax bounds the known client table; past it only existing
// clients are updated.
const knownClientsMax = 100000

// clientBlockPriority is the priority of a rule added by blocking a known
// client: above ordinary allow-list entries so the block takes effect.
const clientBlockPriority = 100000

func scanClientRule(row pgx.Row) (domain.ClientRule, error) {
	var r domain.ClientRule
	var rateRequests, ratePeriod *int32
	var w domain.ClientRuleWindow
	var weekdays []int16
	err := row.Scan(&r.ID, &r.Dimension, &r.Header, &r.Match, &r.Pattern, &r.CaseFold, &r.Priority, &r.Action, &r.Intent, &rateRequests, &ratePeriod,
		&r.ScopeKind, &r.ScopeValues, &w.From, &w.Until, &w.DailyStart, &w.DailyEnd, &weekdays, &w.TimeZone, &r.Enabled, &r.Note, &r.HitCount, &r.LastHitAt, &r.CreatedAt, &r.UpdatedAt, &r.Libraries)
	if err != nil {
		return r, storageError(err)
	}
	if rateRequests != nil && ratePeriod != nil {
		r.RateLimit = &domain.ClientRuleRate{Requests: int(*rateRequests), PeriodSeconds: int(*ratePeriod)}
	}
	for _, d := range weekdays {
		w.Weekdays = append(w.Weekdays, int(d))
	}
	if w.From != nil {
		t := w.From.UTC()
		w.From = &t
	}
	if w.Until != nil {
		t := w.Until.UTC()
		w.Until = &t
	}
	if !w.Empty() {
		r.Window = &w
	}
	if r.ScopeValues == nil {
		r.ScopeValues = []string{}
	}
	if len(r.Libraries) == 0 {
		r.Libraries = nil
	}
	r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	if r.LastHitAt != nil {
		t := r.LastHitAt.UTC()
		r.LastHitAt = &t
	}
	return r, nil
}

func collectClientRules(rows pgx.Rows) ([]domain.ClientRule, error) {
	defer rows.Close()
	rules := make([]domain.ClientRule, 0)
	for rows.Next() {
		r, err := scanClientRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, storageError(rows.Err())
}

// clientRuleArgs are the stored values of an input, in clientRuleWrite order.
func clientRuleArgs(in domain.ClientRuleInput) []any {
	var header, intent, dailyStart, dailyEnd *string
	if in.Header != "" {
		header = &in.Header
	}
	if in.Intent != "" {
		intent = &in.Intent
	}
	var rateRequests, ratePeriod *int
	if in.RateLimit != nil {
		rateRequests, ratePeriod = &in.RateLimit.Requests, &in.RateLimit.PeriodSeconds
	}
	var from, until *time.Time
	weekdays := []int16{}
	timeZone := ""
	if w := in.Window; w != nil {
		from, until, timeZone = w.From, w.Until, w.TimeZone
		if w.DailyStart != "" {
			dailyStart, dailyEnd = &w.DailyStart, &w.DailyEnd
		}
		for _, d := range w.Weekdays {
			weekdays = append(weekdays, int16(d))
		}
	}
	scope := in.ScopeValues
	if scope == nil {
		scope = []string{}
	}
	libraries := in.Libraries
	if libraries == nil {
		libraries = []string{}
	}
	return []any{in.Dimension, header, in.Match, in.Pattern, in.CaseFold, in.Priority, in.Action, intent, rateRequests, ratePeriod,
		in.ScopeKind, scope, from, until, dailyStart, dailyEnd, weekdays, timeZone, in.Enabled, in.Note, libraries}
}

const clientRuleWrite = `dimension,header_name,match_kind,pattern,case_fold,priority,action,intent,rate_requests,rate_period_seconds,
 scope_kind,scope_values,window_from,window_until,daily_start,daily_end,weekdays,time_zone,enabled,note,libraries`

const clientRuleValues = `$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::text[],$13,$14,$15,$16,$17::smallint[],$18,$19,$20,$21::uuid[]`

// lockClientPolicy takes the policy row lock every change serializes on.
func lockClientPolicy(ctx context.Context, tx pgx.Tx) (domain.ClientPolicy, error) {
	var p domain.ClientPolicy
	err := tx.QueryRow(ctx, `SELECT `+clientPolicyColumns+` FROM client_control_policy WHERE id FOR UPDATE`).Scan(&p.UnknownClients, &p.ExemptAdmins, &p.ExemptLoopback, &p.Version, &p.UpdatedAt)
	p.UpdatedAt = p.UpdatedAt.UTC()
	return p, storageError(err)
}

// bumpClientVersion makes every instance recompile on its next request.
func bumpClientVersion(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE client_control_policy SET version=version+1,updated_at=now() WHERE id`)
	return storageError(err)
}

// checkClientRuleLimits enforces the rule count and the enabled regex limit
// after a write, inside the same transaction.
func checkClientRuleLimits(ctx context.Context, tx pgx.Tx) error {
	var total, regex int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE enabled AND match_kind='regex') FROM client_rules`).Scan(&total, &regex); err != nil {
		return storageError(err)
	}
	if total > domain.ClientRulesMax || regex > clientRegexRulesMax {
		return domain.ErrConflict
	}
	return nil
}

func clientRuleAudit(r domain.ClientRule) map[string]any {
	return map[string]any{"dimension": r.Dimension, "header": r.Header, "match": r.Match, "pattern": r.Pattern, "caseFold": r.CaseFold, "priority": r.Priority,
		"action": r.Action, "intent": r.Intent, "rateLimit": r.RateLimit, "scopeKind": r.ScopeKind, "scopeValues": r.ScopeValues, "window": r.Window, "enabled": r.Enabled, "note": r.Note, "libraries": r.Libraries}
}

func auditClientControl(ctx context.Context, tx pgx.Tx, actor domain.Actor, event, target string, before, after any) error {
	e := AuditEntry{Event: event, Actor: actor, Before: before, After: after}
	if domain.ValidID(target) {
		e.TargetID = target
	} else {
		e.TargetRef = target
	}
	return appendAudit(ctx, tx, e)
}

// ClientControlState reads everything the request gate compiles. It is a
// system read without an actor; the gate never exposes it.
func (s *Store) ClientControlState(ctx context.Context) (domain.ClientControlState, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.ClientControlState{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	var st domain.ClientControlState
	p := &st.Policy
	if err = tx.QueryRow(ctx, `SELECT `+clientPolicyColumns+` FROM client_control_policy WHERE id`).Scan(&p.UnknownClients, &p.ExemptAdmins, &p.ExemptLoopback, &p.Version, &p.UpdatedAt); err != nil {
		return st, storageError(err)
	}
	rows, err := tx.Query(ctx, `SELECT `+clientRuleColumns+` FROM client_rules WHERE enabled ORDER BY priority DESC,id`)
	if err != nil {
		return st, storageError(err)
	}
	if st.Rules, err = collectClientRules(rows); err != nil {
		return st, err
	}
	if err = tx.QueryRow(ctx, `SELECT ARRAY(SELECT client_key FROM known_clients WHERE trusted ORDER BY client_key)`).Scan(&st.TrustedKeys); err != nil {
		return st, storageError(err)
	}
	return st, storageError(tx.Commit(ctx))
}

// ClientControlVersion reads the current version for requests that carry
// no session (logins), whose lookup cannot piggyback on authentication.
func (s *Store) ClientControlVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.Pool.QueryRow(ctx, `SELECT version FROM client_control_policy WHERE id`).Scan(&v)
	return v, storageError(err)
}

func clipClientText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// RecordClientActivity upserts the known clients of a batch of throttled
// observations and links the sessions they used. Best effort: the caller
// drops the batch on error.
func (s *Store) RecordClientActivity(ctx context.Context, batch []domain.ClientActivity) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback(ctx)
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM known_clients`).Scan(&count); err != nil {
		return storageError(err)
	}
	b := &pgx.Batch{}
	for _, a := range batch {
		if len(a.Key) != 64 {
			continue
		}
		var ip *string
		if a.IP != "" {
			ip = &a.IP
		}
		var user, session *string
		if domain.ValidID(a.UserID) {
			user = &a.UserID
		}
		if domain.ValidID(a.SessionID) {
			session = &a.SessionID
		}
		kind := a.ClientKind
		if kind != "web" && kind != "native" {
			kind = ""
		}
		// The CTE inserts only below the table bound; an existing client is
		// always updated.
		b.Queue(`WITH upsert AS (
 INSERT INTO known_clients AS k(client_key,app_name,app_version,user_agent,device_id,device_name,client_kind,first_seen_at,last_seen_at,last_ip,last_user_id)
 SELECT $1,NULLIF($2,''),NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),$8,$8,$9::inet,$10::uuid
 WHERE $12 OR EXISTS(SELECT 1 FROM known_clients WHERE client_key=$1)
 ON CONFLICT(client_key) DO UPDATE SET app_name=COALESCE(EXCLUDED.app_name,k.app_name),app_version=COALESCE(EXCLUDED.app_version,k.app_version),
  user_agent=COALESCE(EXCLUDED.user_agent,k.user_agent),device_name=COALESCE(EXCLUDED.device_name,k.device_name),client_kind=COALESCE(EXCLUDED.client_kind,k.client_kind),
  last_seen_at=GREATEST(k.last_seen_at,EXCLUDED.last_seen_at),last_ip=COALESCE(EXCLUDED.last_ip,k.last_ip),
  last_user_id=COALESCE((SELECT id FROM users WHERE id=EXCLUDED.last_user_id),k.last_user_id)
 RETURNING id)
INSERT INTO known_client_sessions(client_id,session_id) SELECT u.id,$11::uuid FROM upsert u WHERE $11::uuid IS NOT NULL AND EXISTS(SELECT 1 FROM sessions WHERE id=$11::uuid) ON CONFLICT DO NOTHING`,
			a.Key, clipClientText(a.AppName, 256), clipClientText(a.AppVersion, 128), clipClientText(a.UserAgent, 512), clipClientText(a.DeviceID, 512), clipClientText(a.DeviceName, 256), kind, a.At, ip, user, session, count < knownClientsMax)
	}
	results := tx.SendBatch(ctx, b)
	for i := 0; i < b.Len(); i++ {
		if _, err = results.Exec(); err != nil {
			_ = results.Close()
			return storageError(err)
		}
	}
	if err = results.Close(); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}

// RecordClientHits appends aggregated hit buckets, adds the rule counters
// and purges buckets past the retention, in one transaction. A hit of a
// rule deleted meanwhile keeps its bucket without the rule.
func (s *Store) RecordClientHits(ctx context.Context, hits []domain.ClientHit, counts []domain.ClientRuleCount) error {
	if len(hits) == 0 && len(counts) == 0 {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback(ctx)
	if len(hits) > 0 {
		n := len(hits)
		buckets, rules, modes, actions, surfaces, users, ips, agents, apps, totals := make([]time.Time, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]int64, n)
		for i, h := range hits {
			buckets[i], modes[i], actions[i], surfaces[i], totals[i] = h.Bucket, h.Mode, h.Action, h.Surface, h.Hits
			if domain.ValidID(h.RuleID) {
				rules[i] = h.RuleID
			}
			if domain.ValidID(h.UserID) {
				users[i] = h.UserID
			}
			ips[i], agents[i], apps[i] = h.IP, clipClientText(h.UserAgent, 256), clipClientText(h.AppName, 256)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO client_control_hits(bucket,rule_id,mode,action,surface,user_id,ip,user_agent,app_name,hits)
 SELECT t.b,(SELECT r.id FROM client_rules r WHERE r.id=NULLIF(t.r,'')::uuid),t.m,t.a,t.s,NULLIF(t.u,'')::uuid,NULLIF(t.i,'')::inet,t.ua,t.an,t.h
 FROM unnest($1::timestamptz[],$2::text[],$3::text[],$4::text[],$5::text[],$6::text[],$7::text[],$8::text[],$9::text[],$10::bigint[]) AS t(b,r,m,a,s,u,i,ua,an,h)`,
			buckets, rules, modes, actions, surfaces, users, ips, agents, apps, totals); err != nil {
			return storageError(err)
		}
	}
	if len(counts) > 0 {
		ids, totals, lasts := make([]string, 0, len(counts)), make([]int64, 0, len(counts)), make([]time.Time, 0, len(counts))
		for _, c := range counts {
			if domain.ValidID(c.RuleID) && c.Hits > 0 {
				ids, totals, lasts = append(ids, c.RuleID), append(totals, c.Hits), append(lasts, c.Last)
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE client_rules r SET hit_count=r.hit_count+t.h,last_hit_at=GREATEST(r.last_hit_at,t.l)
 FROM unnest($1::uuid[],$2::bigint[],$3::timestamptz[]) AS t(id,h,l) WHERE r.id=t.id`, ids, totals, lasts); err != nil {
			return storageError(err)
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM client_control_hits WHERE id IN (SELECT id FROM client_control_hits WHERE bucket<now()-$1*interval '1 second' ORDER BY bucket LIMIT 10000)`, int64(domain.ClientHitRetention/time.Second)); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}

// RevokeSessionByClientRule revokes a session a force_relogin rule matched
// and records it in the security log.
func (s *Store) RevokeSessionByClientRule(ctx context.Context, sessionID, ip string, rules []string) error {
	if !domain.ValidID(sessionID) {
		return domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback(ctx)
	var userID string
	err = tx.QueryRow(ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1::uuid AND revoked_at IS NULL RETURNING user_id::text`, sessionID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return storageError(err)
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "client_control.session_revoked", Actor: domain.Actor{IP: ip}, TargetID: userID, After: map[string]any{"sessionId": sessionID, "rules": rules}}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// ResetClientPolicies is the emergency recovery behind jelee-cli access
// reset-policies (G47.7): it disables every rule, restores the default
// policy and bumps the version, audited without an actor. Known clients,
// their trust and the hit records are kept.
func (s *Store) ResetClientPolicies(ctx context.Context) (domain.ClientPolicyReset, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.ClientPolicyReset{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	before, err := lockClientPolicy(ctx, tx)
	if err != nil {
		return domain.ClientPolicyReset{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE client_rules SET enabled=false,updated_at=now() WHERE enabled`)
	if err != nil {
		return domain.ClientPolicyReset{}, storageError(err)
	}
	def := domain.DefaultClientPolicy()
	var after domain.ClientPolicy
	if err = tx.QueryRow(ctx, `UPDATE client_control_policy SET unknown_clients=$1,exempt_admins=$2,exempt_loopback=$3,version=version+1,updated_at=now() WHERE id RETURNING `+clientPolicyColumns,
		def.UnknownClients, def.ExemptAdmins, def.ExemptLoopback).Scan(&after.UnknownClients, &after.ExemptAdmins, &after.ExemptLoopback, &after.Version, &after.UpdatedAt); err != nil {
		return domain.ClientPolicyReset{}, storageError(err)
	}
	after.UpdatedAt = after.UpdatedAt.UTC()
	result := domain.ClientPolicyReset{RulesDisabled: tag.RowsAffected(), Policy: after}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "client_control.policies_reset", TargetRef: "client_control_policy",
		Before: map[string]any{"unknownClients": before.UnknownClients, "exemptAdmins": before.ExemptAdmins, "exemptLoopback": before.ExemptLoopback},
		After:  map[string]any{"unknownClients": after.UnknownClients, "exemptAdmins": after.ExemptAdmins, "exemptLoopback": after.ExemptLoopback, "rulesDisabled": result.RulesDisabled}}); err != nil {
		return domain.ClientPolicyReset{}, err
	}
	return result, storageError(tx.Commit(ctx))
}

// ClientPolicy reads the server-wide policy. Administrators only.
func (s *Store) ClientPolicy(ctx context.Context, actor domain.Actor) (domain.ClientPolicy, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ClientPolicy{}, err
	}
	defer tx.Rollback(ctx)
	var p domain.ClientPolicy
	if err = tx.QueryRow(ctx, `SELECT `+clientPolicyColumns+` FROM client_control_policy WHERE id`).Scan(&p.UnknownClients, &p.ExemptAdmins, &p.ExemptLoopback, &p.Version, &p.UpdatedAt); err != nil {
		return p, storageError(err)
	}
	p.UpdatedAt = p.UpdatedAt.UTC()
	return p, storageError(tx.Commit(ctx))
}

// SetClientPolicy replaces the policy; unchanged is a no-op without audit.
func (s *Store) SetClientPolicy(ctx context.Context, actor domain.Actor, in domain.ClientPolicy) (domain.ClientPolicy, error) {
	if !in.Valid() {
		return domain.ClientPolicy{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ClientPolicy{}, err
	}
	defer tx.Rollback(ctx)
	old, err := lockClientPolicy(ctx, tx)
	if err != nil {
		return old, err
	}
	if old.UnknownClients == in.UnknownClients && old.ExemptAdmins == in.ExemptAdmins && old.ExemptLoopback == in.ExemptLoopback {
		return old, storageError(tx.Commit(ctx))
	}
	var p domain.ClientPolicy
	if err = tx.QueryRow(ctx, `UPDATE client_control_policy SET unknown_clients=$1,exempt_admins=$2,exempt_loopback=$3,version=version+1,updated_at=now() WHERE id RETURNING `+clientPolicyColumns,
		in.UnknownClients, in.ExemptAdmins, in.ExemptLoopback).Scan(&p.UnknownClients, &p.ExemptAdmins, &p.ExemptLoopback, &p.Version, &p.UpdatedAt); err != nil {
		return p, storageError(err)
	}
	p.UpdatedAt = p.UpdatedAt.UTC()
	view := func(p domain.ClientPolicy) map[string]any {
		return map[string]any{"unknownClients": p.UnknownClients, "exemptAdmins": p.ExemptAdmins, "exemptLoopback": p.ExemptLoopback}
	}
	if err = auditClientControl(ctx, tx, actor, "client_control.policy_changed", "client_control_policy", view(old), view(p)); err != nil {
		return domain.ClientPolicy{}, err
	}
	return p, storageError(tx.Commit(ctx))
}

// ListClientRules lists every rule, enabled or not, in precedence order.
func (s *Store) ListClientRules(ctx context.Context, actor domain.Actor) ([]domain.ClientRule, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+clientRuleColumns+` FROM client_rules ORDER BY priority DESC,id LIMIT $1`, domain.ClientRulesMax)
	if err != nil {
		return nil, storageError(err)
	}
	rules, err := collectClientRules(rows)
	if err != nil {
		return nil, err
	}
	return rules, storageError(tx.Commit(ctx))
}

func clientRuleInTransaction(ctx context.Context, tx pgx.Tx, id string, lock bool) (domain.ClientRule, error) {
	if !domain.ValidID(id) {
		return domain.ClientRule{}, domain.ErrNotFound
	}
	query := `SELECT ` + clientRuleColumns + ` FROM client_rules WHERE id=$1::uuid`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanClientRule(tx.QueryRow(ctx, query, id))
}

func (s *Store) GetClientRule(ctx context.Context, actor domain.Actor, id string) (domain.ClientRule, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ClientRule{}, err
	}
	defer tx.Rollback(ctx)
	r, err := clientRuleInTransaction(ctx, tx, id, false)
	if err != nil {
		return r, err
	}
	return r, storageError(tx.Commit(ctx))
}

// insertClientRule writes a validated rule and enforces the limits.
func insertClientRule(ctx context.Context, tx pgx.Tx, actor domain.Actor, in domain.ClientRuleInput) (domain.ClientRule, error) {
	args := append(clientRuleArgs(in), nullableID(actor.UserID))
	r, err := scanClientRule(tx.QueryRow(ctx, `INSERT INTO client_rules(`+clientRuleWrite+`,created_by) VALUES(`+clientRuleValues+`,$22::uuid) RETURNING `+clientRuleColumns, args...))
	if err != nil {
		return r, err
	}
	return r, checkClientRuleLimits(ctx, tx)
}

func nullableID(id string) *string {
	if domain.ValidID(id) {
		return &id
	}
	return nil
}

func (s *Store) CreateClientRule(ctx context.Context, actor domain.Actor, in domain.ClientRuleInput) (domain.ClientRule, error) {
	if !in.Valid() {
		return domain.ClientRule{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ClientRule{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockClientPolicy(ctx, tx); err != nil {
		return domain.ClientRule{}, err
	}
	r, err := insertClientRule(ctx, tx, actor, in)
	if err != nil {
		return domain.ClientRule{}, err
	}
	if err = bumpClientVersion(ctx, tx); err != nil {
		return domain.ClientRule{}, err
	}
	if err = auditClientControl(ctx, tx, actor, "client_control.rule_created", r.ID, nil, clientRuleAudit(r)); err != nil {
		return domain.ClientRule{}, err
	}
	return r, storageError(tx.Commit(ctx))
}

// UpdateClientRule replaces a rule's settings; counters are kept. An
// unchanged rule is a no-op without audit.
func (s *Store) UpdateClientRule(ctx context.Context, actor domain.Actor, id string, in domain.ClientRuleInput) (domain.ClientRule, error) {
	if !in.Valid() {
		return domain.ClientRule{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ClientRule{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockClientPolicy(ctx, tx); err != nil {
		return domain.ClientRule{}, err
	}
	old, err := clientRuleInTransaction(ctx, tx, id, true)
	if err != nil {
		return old, err
	}
	if sameClientRule(old, in) {
		return old, storageError(tx.Commit(ctx))
	}
	args := append(clientRuleArgs(in), id)
	r, err := scanClientRule(tx.QueryRow(ctx, `UPDATE client_rules SET (`+clientRuleWrite+`,updated_at)=(`+clientRuleValues+`,now()) WHERE id=$22::uuid RETURNING `+clientRuleColumns, args...))
	if err != nil {
		return r, err
	}
	if err = checkClientRuleLimits(ctx, tx); err != nil {
		return domain.ClientRule{}, err
	}
	if err = bumpClientVersion(ctx, tx); err != nil {
		return domain.ClientRule{}, err
	}
	if err = auditClientControl(ctx, tx, actor, "client_control.rule_updated", r.ID, clientRuleAudit(old), clientRuleAudit(r)); err != nil {
		return domain.ClientRule{}, err
	}
	return r, storageError(tx.Commit(ctx))
}

// sameClientRule compares the stored settings with an input through their
// audited form, which covers every setting.
func sameClientRule(old domain.ClientRule, in domain.ClientRuleInput) bool {
	a, errA := json.Marshal(clientRuleAudit(old))
	b, errB := json.Marshal(clientRuleAudit(domain.ClientRule{ClientRuleInput: in}))
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func (s *Store) DeleteClientRule(ctx context.Context, actor domain.Actor, id string) error {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = lockClientPolicy(ctx, tx); err != nil {
		return err
	}
	old, err := clientRuleInTransaction(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM client_rules WHERE id=$1::uuid`, id); err != nil {
		return storageError(err)
	}
	if err = bumpClientVersion(ctx, tx); err != nil {
		return err
	}
	if err = auditClientControl(ctx, tx, actor, "client_control.rule_deleted", old.ID, clientRuleAudit(old), nil); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// SetClientRuleEnforcing switches an observe or shadow rule to its intent
// (enforce) or an enforcing rule to observing that action. Asking for the
// mode a rule already has is a no-op.
func (s *Store) SetClientRuleEnforcing(ctx context.Context, actor domain.Actor, id string, enforce bool) (domain.ClientRule, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ClientRule{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockClientPolicy(ctx, tx); err != nil {
		return domain.ClientRule{}, err
	}
	old, err := clientRuleInTransaction(ctx, tx, id, true)
	if err != nil {
		return old, err
	}
	recordOnly := old.Action == "observe" || old.Action == "shadow"
	if enforce != recordOnly {
		return old, storageError(tx.Commit(ctx))
	}
	query := `UPDATE client_rules SET action=intent,intent=NULL,updated_at=now() WHERE id=$1::uuid RETURNING ` + clientRuleColumns
	if !enforce {
		query = `UPDATE client_rules SET intent=action,action='observe',updated_at=now() WHERE id=$1::uuid RETURNING ` + clientRuleColumns
	}
	r, err := scanClientRule(tx.QueryRow(ctx, query, id))
	if err != nil {
		return r, err
	}
	if err = bumpClientVersion(ctx, tx); err != nil {
		return domain.ClientRule{}, err
	}
	if err = auditClientControl(ctx, tx, actor, "client_control.rule_mode_changed", r.ID, map[string]string{"action": old.Action, "intent": old.Intent}, map[string]string{"action": r.Action, "intent": r.Intent}); err != nil {
		return domain.ClientRule{}, err
	}
	return r, storageError(tx.Commit(ctx))
}

// clientHitColumns mask the address to its /24 or /48 network (G47.9).
const clientHitColumns = `id,bucket,COALESCE(rule_id::text,''),mode,action,surface,COALESCE(user_id::text,''),
 COALESCE(network(set_masklen(ip,CASE WHEN family(ip)=4 THEN 24 ELSE 48 END))::text,''),user_agent,app_name,hits`

const clientHitWhere = `(NULLIF($1,'')::uuid IS NULL OR rule_id=NULLIF($1,'')::uuid) AND ($2='' OR mode=$2)
 AND ($3::timestamptz IS NULL OR bucket>=$3) AND ($4::timestamptz IS NULL OR bucket<$4)`

func collectClientHits(rows pgx.Rows, capacity int) ([]domain.ClientHitRecord, error) {
	defer rows.Close()
	out := make([]domain.ClientHitRecord, 0, capacity)
	for rows.Next() {
		var h domain.ClientHitRecord
		if err := rows.Scan(&h.ID, &h.Bucket, &h.RuleID, &h.Mode, &h.Action, &h.Surface, &h.UserID, &h.Network, &h.UserAgent, &h.AppName, &h.Hits); err != nil {
			return nil, storageError(err)
		}
		h.Bucket = h.Bucket.UTC()
		out = append(out, h)
	}
	return out, storageError(rows.Err())
}

// ListClientHits pages hit buckets newest first; the cursor is the opaque
// next value of the previous page.
func (s *Store) ListClientHits(ctx context.Context, actor domain.Actor, f domain.ClientHitFilter, cursor string, limit int) ([]domain.ClientHitRecord, string, error) {
	before, ok := parseAuditCursor(cursor)
	if !ok || limit < 1 || limit > 100 || !f.Valid() {
		return nil, "", domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+clientHitColumns+` FROM client_control_hits WHERE `+clientHitWhere+` AND ($5=0 OR id<$5) ORDER BY id DESC LIMIT $6`,
		f.RuleID, f.Mode, auditTime(f.Since), auditTime(f.Until), before, limit+1)
	if err != nil {
		return nil, "", storageError(err)
	}
	hits, err := collectClientHits(rows, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(hits) > limit {
		hits = hits[:limit]
		next = strconv.FormatInt(hits[limit-1].ID, 10)
	}
	return hits, next, storageError(tx.Commit(ctx))
}

// ExportClientHits returns at most max masked buckets newest first and
// audits the export. More than max fails with the export limit error
// (409 stats_export_limit) so a partial export is never taken for a full one.
func (s *Store) ExportClientHits(ctx context.Context, actor domain.Actor, f domain.ClientHitFilter, max int) ([]domain.ClientHitRecord, error) {
	if max < 1 || max > domain.ClientHitExportMax || !f.Valid() {
		return nil, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+clientHitColumns+` FROM client_control_hits WHERE `+clientHitWhere+` ORDER BY id DESC LIMIT $5`,
		f.RuleID, f.Mode, auditTime(f.Since), auditTime(f.Until), max+1)
	if err != nil {
		return nil, storageError(err)
	}
	hits, err := collectClientHits(rows, max+1)
	if err != nil {
		return nil, err
	}
	if len(hits) > max {
		return nil, domain.ErrWatchStatsExportLimit
	}
	filter := map[string]any{"ruleId": f.RuleID, "mode": f.Mode, "since": auditTime(f.Since), "until": auditTime(f.Until)}
	if err = auditClientControl(ctx, tx, actor, "client_control.hits_exported", "client_control_hits", nil, map[string]any{"filter": filter, "rows": len(hits)}); err != nil {
		return nil, err
	}
	return hits, storageError(tx.Commit(ctx))
}

// ClientHitStats summarizes the hits since a point in time. Shadow hits are
// excluded (G47.3: they feed no statistics).
func (s *Store) ClientHitStats(ctx context.Context, actor domain.Actor, since time.Time, top int) (domain.ClientHitStats, error) {
	if top < 1 || top > 100 {
		return domain.ClientHitStats{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ClientHitStats{}, err
	}
	defer tx.Rollback(ctx)
	st := domain.ClientHitStats{Since: since.UTC()}
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(hits),0),COALESCE(sum(hits) FILTER (WHERE mode IN ('enforced','default') AND action IN ('deny','pending_approval')),0),
 COALESCE(sum(hits) FILTER (WHERE mode='observe'),0) FROM client_control_hits WHERE bucket>=$1 AND mode<>'shadow'`, since).Scan(&st.Total, &st.Blocked, &st.Observed); err != nil {
		return st, storageError(err)
	}
	top5 := func(expr string, extra string) ([]domain.ClientHitCount, error) {
		rows, err := tx.Query(ctx, `SELECT `+expr+` AS v,sum(hits) AS n FROM client_control_hits WHERE bucket>=$1 AND mode<>'shadow' `+extra+` GROUP BY v ORDER BY n DESC,v LIMIT $2`, since, top)
		if err != nil {
			return nil, storageError(err)
		}
		defer rows.Close()
		out := make([]domain.ClientHitCount, 0, top)
		for rows.Next() {
			var c domain.ClientHitCount
			if err = rows.Scan(&c.Value, &c.Hits); err != nil {
				return nil, storageError(err)
			}
			out = append(out, c)
		}
		return out, storageError(rows.Err())
	}
	if st.ByAction, err = top5(`mode||':'||action`, ``); err != nil {
		return st, err
	}
	if st.TopUserAgents, err = top5(`user_agent`, `AND user_agent<>''`); err != nil {
		return st, err
	}
	if st.TopIPs, err = top5(`host(ip)`, `AND ip IS NOT NULL`); err != nil {
		return st, err
	}
	if st.TopRules, err = top5(`rule_id::text`, `AND rule_id IS NOT NULL`); err != nil {
		return st, err
	}
	return st, storageError(tx.Commit(ctx))
}

const knownClientColumns = `k.id::text,COALESCE(k.app_name,''),COALESCE(k.app_version,''),COALESCE(k.user_agent,''),COALESCE(k.device_id,''),COALESCE(k.device_name,''),COALESCE(k.client_kind,''),
 COALESCE(k.alias,''),k.trusted,k.first_seen_at,k.last_seen_at,COALESCE(host(k.last_ip),''),COALESCE(k.last_user_id::text,''),
 (SELECT count(*) FROM known_client_sessions l JOIN sessions s ON s.id=l.session_id WHERE l.client_id=k.id AND s.revoked_at IS NULL AND s.expires_at>now()),
 COALESCE(` + knownClientBlockRuleSQL + `,'')`

// knownClientBlockRuleSQL finds an enabled, global deny rule without a time
// window on the identity BlockKnownClient matches: the exact device ID, or
// the user agent (prefix when the stored value was clipped at 512 bytes)
// when the client reports no device ID. client_rules_block_lookup_idx
// serves it; keep it in step with BlockKnownClient.
const knownClientBlockRuleSQL = `(SELECT r.id::text FROM client_rules r WHERE r.enabled AND r.action='deny' AND r.scope_kind='global'
 AND r.window_from IS NULL AND r.window_until IS NULL AND r.daily_start IS NULL AND cardinality(r.weekdays)=0
 AND ((r.dimension='device_id' AND r.match_kind='exact' AND r.pattern=k.device_id)
  OR (k.device_id IS NULL AND r.dimension='user_agent' AND r.pattern=k.user_agent AND r.match_kind=CASE WHEN octet_length(k.user_agent)>=512 THEN 'prefix' ELSE 'exact' END))
 ORDER BY r.priority DESC,r.id LIMIT 1)`

func scanKnownClient(row pgx.Row) (domain.KnownClient, error) {
	var c domain.KnownClient
	err := row.Scan(&c.ID, &c.AppName, &c.AppVersion, &c.UserAgent, &c.DeviceID, &c.DeviceName, &c.ClientKind, &c.Alias, &c.Trusted, &c.FirstSeenAt, &c.LastSeenAt, &c.LastIP, &c.LastUserID, &c.ActiveSessions, &c.BlockRuleID)
	c.Blocked = c.BlockRuleID != ""
	c.FirstSeenAt, c.LastSeenAt = c.FirstSeenAt.UTC(), c.LastSeenAt.UTC()
	return c, storageError(err)
}

// parseKnownClientCursor reads "<last seen unix microseconds>_<id>".
func parseKnownClientCursor(cursor string) (time.Time, string, bool) {
	if cursor == "" {
		return time.Time{}, "", true
	}
	micros, id, found := strings.Cut(cursor, "_")
	n, err := strconv.ParseInt(micros, 10, 64)
	if !found || err != nil || n < 0 || strconv.FormatInt(n, 10) != micros || !domain.ValidID(id) {
		return time.Time{}, "", false
	}
	return time.UnixMicro(n).UTC(), id, true
}

// ListKnownClients pages known clients, most recently seen first.
func (s *Store) ListKnownClients(ctx context.Context, actor domain.Actor, cursor string, limit int) ([]domain.KnownClient, string, error) {
	at, after, ok := parseKnownClientCursor(cursor)
	if !ok || limit < 1 || limit > 100 {
		return nil, "", domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+knownClientColumns+` FROM known_clients k
 WHERE $1::uuid IS NULL OR (k.last_seen_at,k.id)<($2::timestamptz,$1::uuid) ORDER BY k.last_seen_at DESC,k.id DESC LIMIT $3`, nullableID(after), at, limit+1)
	if err != nil {
		return nil, "", storageError(err)
	}
	clients := make([]domain.KnownClient, 0, limit+1)
	for rows.Next() {
		c, err := scanKnownClient(rows)
		if err != nil {
			rows.Close()
			return nil, "", err
		}
		clients = append(clients, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", storageError(err)
	}
	next := ""
	if len(clients) > limit {
		clients = clients[:limit]
		last := clients[limit-1]
		next = strconv.FormatInt(last.LastSeenAt.UnixMicro(), 10) + "_" + last.ID
	}
	return clients, next, storageError(tx.Commit(ctx))
}

func knownClientInTransaction(ctx context.Context, tx pgx.Tx, id string) (domain.KnownClient, error) {
	if !domain.ValidID(id) {
		return domain.KnownClient{}, domain.ErrNotFound
	}
	return scanKnownClient(tx.QueryRow(ctx, `SELECT `+knownClientColumns+` FROM known_clients k WHERE k.id=$1::uuid FOR UPDATE OF k`, id))
}

func knownClientAudit(c domain.KnownClient) map[string]any {
	return map[string]any{"alias": c.Alias, "trusted": c.Trusted, "appName": c.AppName, "deviceName": c.DeviceName}
}

// UpdateKnownClient renames a client or changes its trust. Trust changes
// the evaluation and bumps the version; a rename does not.
func (s *Store) UpdateKnownClient(ctx context.Context, actor domain.Actor, id string, u domain.KnownClientUpdate) (domain.KnownClient, error) {
	if !u.Valid() {
		return domain.KnownClient{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.KnownClient{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockClientPolicy(ctx, tx); err != nil {
		return domain.KnownClient{}, err
	}
	old, err := knownClientInTransaction(ctx, tx, id)
	if err != nil {
		return old, err
	}
	alias, trusted := old.Alias, old.Trusted
	if u.Alias != nil {
		alias = *u.Alias
	}
	if u.Trusted != nil {
		trusted = *u.Trusted
	}
	if alias == old.Alias && trusted == old.Trusted {
		return old, storageError(tx.Commit(ctx))
	}
	if _, err = tx.Exec(ctx, `UPDATE known_clients SET alias=NULLIF($2,''),trusted=$3 WHERE id=$1::uuid`, id, alias, trusted); err != nil {
		return domain.KnownClient{}, storageError(err)
	}
	if trusted != old.Trusted {
		if err = bumpClientVersion(ctx, tx); err != nil {
			return domain.KnownClient{}, err
		}
	}
	c, err := knownClientInTransaction(ctx, tx, id)
	if err != nil {
		return c, err
	}
	if err = auditClientControl(ctx, tx, actor, "client_control.client_updated", id, knownClientAudit(old), knownClientAudit(c)); err != nil {
		return domain.KnownClient{}, err
	}
	return c, storageError(tx.Commit(ctx))
}

// BlockKnownClient adds an exact deny rule on the client's device ID, or
// on its user agent when it reports none, above ordinary allow rules.
func (s *Store) BlockKnownClient(ctx context.Context, actor domain.Actor, id string) (domain.ClientRule, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ClientRule{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockClientPolicy(ctx, tx); err != nil {
		return domain.ClientRule{}, err
	}
	c, err := knownClientInTransaction(ctx, tx, id)
	if err != nil {
		return domain.ClientRule{}, err
	}
	in := domain.ClientRuleInput{Dimension: "device_id", Match: "exact", Pattern: c.DeviceID, Priority: clientBlockPriority, Action: "deny", ScopeKind: "global", ScopeValues: []string{}, Enabled: true}
	if c.DeviceID == "" {
		in.Dimension, in.Pattern = "user_agent", c.UserAgent
		if len(c.UserAgent) >= 512 {
			// The stored user agent was clipped; match what was kept.
			in.Match = "prefix"
		}
	}
	label := c.Alias
	if label == "" {
		label = strings.TrimSpace(c.AppName + " " + c.DeviceName)
	}
	in.Note = clipClientText("Blocked known client "+label, domain.ClientRuleNoteMax)
	if in.Pattern == "" || len(in.Pattern) > domain.ClientRulePatternMax {
		// Nothing identifies the client in a way a rule can match.
		return domain.ClientRule{}, domain.ErrConflict
	}
	if !in.Valid() {
		return domain.ClientRule{}, domain.ErrInvalid
	}
	r, err := insertClientRule(ctx, tx, actor, in)
	if err != nil {
		return domain.ClientRule{}, err
	}
	if err = bumpClientVersion(ctx, tx); err != nil {
		return domain.ClientRule{}, err
	}
	if err = auditClientControl(ctx, tx, actor, "client_control.client_blocked", c.ID, nil, map[string]any{"ruleId": r.ID, "rule": clientRuleAudit(r)}); err != nil {
		return domain.ClientRule{}, err
	}
	return r, storageError(tx.Commit(ctx))
}

// KickKnownClient revokes every active session the client used, and for a
// client with a device ID every active session recorded with that
// application and device ID. It does not block the client from logging in.
func (s *Store) KickKnownClient(ctx context.Context, actor domain.Actor, id string) (int64, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	c, err := knownClientInTransaction(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE revoked_at IS NULL AND expires_at>now() AND (
 id IN (SELECT session_id FROM known_client_sessions WHERE client_id=$1::uuid)
 OR $2<>'' AND device_id=$2 AND COALESCE(client_name,'')=$3)`, id, c.DeviceID, c.AppName)
	if err != nil {
		return 0, storageError(err)
	}
	if err = auditClientControl(ctx, tx, actor, "client_control.client_kicked", c.ID, nil, map[string]any{"sessionsRevoked": tag.RowsAffected()}); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), storageError(tx.Commit(ctx))
}
