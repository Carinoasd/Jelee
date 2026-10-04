package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// G12 webhooks: endpoint configuration, the outbox and the delivery log.

var (
	_ app.WebhookOutbox          = (*Store)(nil)
	_ app.WebhookRepository      = (*Store)(nil)
	_ app.WebhookDeliveryStore   = (*Store)(nil)
	_ app.WebhookAdminRepository = (*Store)(nil)
)

// maxWebhooks bounds configured endpoints; each event fans out to all of
// them.
const maxWebhooks = 64

// EnableWebhookEvents turns event production on or off for this store. The
// runtime enables it when the webhook deliverer runs.
func (s *Store) EnableWebhookEvents(enabled bool) { s.webhookEvents.Store(enabled) }

func (s *Store) webhooksOn() bool { return s.webhookEvents.Load() }

// webhookSubscribedSQL is true while an enabled endpoint receives events of
// the type typeExpr evaluates to. Producers insert nothing otherwise, so an
// unused webhook feature costs one index-free probe of a tiny table.
func webhookSubscribedSQL(typeExpr string) string {
	return `EXISTS(SELECT 1 FROM webhooks hook WHERE hook.enabled AND (cardinality(hook.events)=0 OR ` + typeExpr + `=ANY(hook.events)))`
}

func newWebhookEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", domain.ErrDatabase
	}
	return hex.EncodeToString(b[:]), nil
}

const insertWebhookEventSQL = `INSERT INTO webhook_outbox(event_id,event_type,version,occurred_at,subject_kind,subject_id,data)
 SELECT $1,$2::text,$3,$4,$5,NULLIF($6,''),$7::jsonb WHERE ` + "EXISTS(SELECT 1 FROM webhooks hook WHERE hook.enabled AND (cardinality(hook.events)=0 OR $2::text=ANY(hook.events)))"

func insertWebhookEvent(ctx context.Context, tx pgx.Tx, e domain.WebhookEvent) error {
	if err := e.Validate(); err != nil {
		return err
	}
	var data []byte
	if len(e.Data) > 0 {
		raw, err := json.Marshal(e.Data)
		if err != nil {
			return domain.ErrInvalid
		}
		data = raw
	}
	_, err := tx.Exec(ctx, insertWebhookEventSQL, e.EventID, string(e.Type), e.Version, e.OccurredAt, string(e.Subject.Kind), e.Subject.ID, data)
	return storageError(err)
}

// appendWebhook records one event in the caller's transaction when events
// are enabled. An event the domain rejects is a programming error in a
// producer; it is dropped rather than failing the change that raised it.
func appendWebhook(ctx context.Context, tx pgx.Tx, enabled bool, t domain.WebhookEventType, at time.Time, subject domain.WebhookSubject, data map[string]any) error {
	if !enabled {
		return nil
	}
	id, err := newWebhookEventID()
	if err != nil {
		return err
	}
	event, err := domain.NewWebhookEvent(id, t, at, subject, data)
	if err != nil {
		return nil
	}
	return insertWebhookEvent(ctx, tx, event)
}

// Append implements app.WebhookOutbox for events that have no change of
// their own to share a transaction with (for example system alerts).
func (s *Store) Append(ctx context.Context, event domain.WebhookEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if !s.webhooksOn() {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback(ctx)
	if err = insertWebhookEvent(ctx, tx, event); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// --- endpoint configuration ---

const webhookViewColumns = `w.id::text,w.name,w.url,w.enabled,w.events,w.header_names,w.timeout_ms,w.max_attempts,w.base_delay_ms,w.max_delay_ms,w.jitter,
 CASE WHEN w.previous_until>clock_timestamp() THEN w.previous_until END,w.created_at,w.updated_at,
 (SELECT count(*) FROM webhook_deliveries d WHERE d.webhook_id=w.id AND d.state='pending'),
 (SELECT count(*) FROM webhook_deliveries d WHERE d.webhook_id=w.id AND d.state='dead')`

func eventTypes(values []string) []domain.WebhookEventType {
	out := make([]domain.WebhookEventType, len(values))
	for i, v := range values {
		out[i] = domain.WebhookEventType(v)
	}
	return out
}

func eventStrings(values []domain.WebhookEventType) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

func scanWebhookView(row pgx.Row) (app.WebhookEndpointView, error) {
	var v app.WebhookEndpointView
	var events []string
	var timeout int
	var base, maxDelay int64
	err := row.Scan(&v.ID, &v.Name, &v.URL, &v.Enabled, &events, &v.HeaderNames, &timeout, &v.Retry.MaxAttempts, &base, &maxDelay, &v.Retry.Jitter,
		&v.PreviousSecretUntil, &v.CreatedAt, &v.UpdatedAt, &v.Pending, &v.Dead)
	if err != nil {
		return v, storageError(err)
	}
	v.Events = eventTypes(events)
	v.TimeoutSeconds = timeout / 1000
	v.Retry.BaseDelaySeconds, v.Retry.MaxDelaySeconds = int(base/1000), int(maxDelay/1000)
	if v.HeaderNames == nil {
		v.HeaderNames = []string{}
	}
	return v, nil
}

// webhookAudit is the audited state of an endpoint: never secrets or header
// values, and only the URL's host since a query may carry a credential.
func webhookAudit(v app.WebhookEndpointView) map[string]any {
	host := ""
	if u, err := url.Parse(v.URL); err == nil {
		host = u.Host
	}
	return map[string]any{"name": v.Name, "host": host, "enabled": v.Enabled, "events": v.Events, "headerNames": v.HeaderNames,
		"timeoutSeconds": v.TimeoutSeconds, "retry": v.Retry}
}

// validWebhookRecord mirrors the service checks so storage never writes an
// unbounded or inconsistent row even when called directly. The URL target
// policy is the service's; the table only insists on HTTPS.
func validWebhookRecord(r app.WebhookEndpointRecord, create bool) bool {
	if !domain.ValidID(r.ID) || !validText(r.Name, 128, false) || r.URL == "" || len(r.URL) > 2048 || r.Timeout < app.MinWebhookTimeout || r.Timeout > app.MaxWebhookTimeout || r.Retry.Validate() != nil {
		return false
	}
	if create != (len(r.SealedSecret) > 0) || create && !r.ReplaceHeaders || len(r.HeaderNames) > 16 {
		return false
	}
	if r.ReplaceHeaders && (len(r.HeaderNames) == 0) != (len(r.SealedHeaders) == 0) || !r.ReplaceHeaders && (len(r.HeaderNames) > 0 || len(r.SealedHeaders) > 0) {
		return false
	}
	_, err := domain.NormalizeWebhookFilter(r.Events)
	return err == nil
}

func (s *Store) ListWebhooks(ctx context.Context, actor domain.Actor) ([]app.WebhookEndpointView, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+webhookViewColumns+` FROM webhooks w ORDER BY w.created_at,w.id LIMIT $1`, maxWebhooks)
	if err != nil {
		return nil, storageError(err)
	}
	views := []app.WebhookEndpointView{}
	for rows.Next() {
		v, err := scanWebhookView(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		views = append(views, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return views, storageError(tx.Commit(ctx))
}

func (s *Store) GetWebhook(ctx context.Context, actor domain.Actor, id string) (app.WebhookEndpointView, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return app.WebhookEndpointView{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(id) {
		return app.WebhookEndpointView{}, domain.ErrNotFound
	}
	v, err := scanWebhookView(tx.QueryRow(ctx, `SELECT `+webhookViewColumns+` FROM webhooks w WHERE w.id=$1::uuid`, id))
	if err != nil {
		return v, err
	}
	return v, storageError(tx.Commit(ctx))
}

func (s *Store) CreateWebhook(ctx context.Context, actor domain.Actor, r app.WebhookEndpointRecord) (app.WebhookEndpointView, error) {
	if !validWebhookRecord(r, true) {
		return app.WebhookEndpointView{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return app.WebhookEndpointView{}, err
	}
	defer tx.Rollback(ctx)
	// Serialize creation so the endpoint cap holds under concurrency.
	if _, err = tx.Exec(ctx, `LOCK TABLE webhooks IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return app.WebhookEndpointView{}, storageError(err)
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM webhooks`).Scan(&count); err != nil {
		return app.WebhookEndpointView{}, storageError(err)
	}
	if count >= maxWebhooks {
		return app.WebhookEndpointView{}, domain.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO webhooks(id,name,url,enabled,events,header_names,headers_sealed,timeout_ms,max_attempts,base_delay_ms,max_delay_ms,jitter,secret_sealed)
 VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, r.ID, r.Name, r.URL, r.Enabled, eventStrings(r.Events), r.HeaderNames, r.SealedHeaders,
		r.Timeout.Milliseconds(), r.Retry.MaxAttempts, r.Retry.BaseDelay.Milliseconds(), r.Retry.MaxDelay.Milliseconds(), r.Retry.Jitter, r.SealedSecret)
	if err != nil {
		return app.WebhookEndpointView{}, storageError(err)
	}
	v, err := scanWebhookView(tx.QueryRow(ctx, `SELECT `+webhookViewColumns+` FROM webhooks w WHERE w.id=$1::uuid`, r.ID))
	if err != nil {
		return v, err
	}
	if err = auditAccount(ctx, tx, actor, "webhook.created", r.ID, nil, webhookAudit(v)); err != nil {
		return app.WebhookEndpointView{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

func (s *Store) UpdateWebhook(ctx context.Context, actor domain.Actor, r app.WebhookEndpointRecord) (app.WebhookEndpointView, error) {
	if !validWebhookRecord(r, false) {
		return app.WebhookEndpointView{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return app.WebhookEndpointView{}, err
	}
	defer tx.Rollback(ctx)
	before, err := scanWebhookView(tx.QueryRow(ctx, `SELECT `+webhookViewColumns+` FROM webhooks w WHERE w.id=$1::uuid FOR UPDATE`, r.ID))
	if err != nil {
		return before, err
	}
	_, err = tx.Exec(ctx, `UPDATE webhooks SET name=$2,url=$3,enabled=$4,events=$5,timeout_ms=$6,max_attempts=$7,base_delay_ms=$8,max_delay_ms=$9,jitter=$10,
 header_names=CASE WHEN $11 THEN $12 ELSE header_names END,headers_sealed=CASE WHEN $11 THEN $13 ELSE headers_sealed END,updated_at=clock_timestamp() WHERE id=$1::uuid`,
		r.ID, r.Name, r.URL, r.Enabled, eventStrings(r.Events), r.Timeout.Milliseconds(), r.Retry.MaxAttempts, r.Retry.BaseDelay.Milliseconds(), r.Retry.MaxDelay.Milliseconds(), r.Retry.Jitter,
		r.ReplaceHeaders, r.HeaderNames, r.SealedHeaders)
	if err != nil {
		return app.WebhookEndpointView{}, storageError(err)
	}
	after, err := scanWebhookView(tx.QueryRow(ctx, `SELECT `+webhookViewColumns+` FROM webhooks w WHERE w.id=$1::uuid`, r.ID))
	if err != nil {
		return after, err
	}
	if err = auditAccount(ctx, tx, actor, "webhook.updated", r.ID, webhookAudit(before), webhookAudit(after)); err != nil {
		return app.WebhookEndpointView{}, err
	}
	return after, storageError(tx.Commit(ctx))
}

func (s *Store) DeleteWebhook(ctx context.Context, actor domain.Actor, id string) error {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	before, err := scanWebhookView(tx.QueryRow(ctx, `SELECT `+webhookViewColumns+` FROM webhooks w WHERE w.id=$1::uuid FOR UPDATE`, id))
	if err != nil {
		return err
	}
	// Its deliveries and their attempt log go with it.
	if _, err = tx.Exec(ctx, `DELETE FROM webhooks WHERE id=$1::uuid`, id); err != nil {
		return storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "webhook.deleted", id, webhookAudit(before), nil); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

func (s *Store) RotateWebhookSecret(ctx context.Context, actor domain.Actor, id string, sealed []byte, previousUntil time.Time) (app.WebhookEndpointView, error) {
	if len(sealed) < 33 || len(sealed) > 1024 {
		return app.WebhookEndpointView{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return app.WebhookEndpointView{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(id) {
		return app.WebhookEndpointView{}, domain.ErrNotFound
	}
	var until *time.Time
	if !previousUntil.IsZero() {
		until = &previousUntil
	}
	tag, err := tx.Exec(ctx, `UPDATE webhooks SET previous_secret_sealed=CASE WHEN $3::timestamptz IS NULL THEN NULL ELSE secret_sealed END,previous_until=$3,secret_sealed=$2,updated_at=clock_timestamp() WHERE id=$1::uuid`, id, sealed, until)
	if err != nil {
		return app.WebhookEndpointView{}, storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return app.WebhookEndpointView{}, domain.ErrNotFound
	}
	v, err := scanWebhookView(tx.QueryRow(ctx, `SELECT `+webhookViewColumns+` FROM webhooks w WHERE w.id=$1::uuid`, id))
	if err != nil {
		return v, err
	}
	if err = auditAccount(ctx, tx, actor, "webhook.secret_rotated", id, nil, map[string]any{"previousSecretUntil": until}); err != nil {
		return app.WebhookEndpointView{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

const webhookEndpointColumns = `w.id::text,w.url,w.enabled,w.events,w.timeout_ms,w.max_attempts,w.base_delay_ms,w.max_delay_ms,w.jitter`

func scanWebhookEndpoint(targets ...any) ([]any, func() app.WebhookEndpoint) {
	var e app.WebhookEndpoint
	var events []string
	var timeout int
	var base, maxDelay int64
	all := append([]any{&e.ID, &e.URL, &e.Enabled, &events, &timeout, &e.Retry.MaxAttempts, &base, &maxDelay, &e.Retry.Jitter}, targets...)
	return all, func() app.WebhookEndpoint {
		e.Filter = eventTypes(events)
		if len(e.Filter) == 0 {
			e.Filter = nil
		}
		e.Timeout = time.Duration(timeout) * time.Millisecond
		e.Retry.BaseDelay, e.Retry.MaxDelay = time.Duration(base)*time.Millisecond, time.Duration(maxDelay)*time.Millisecond
		return e
	}
}

// ListDeliveryEndpoints returns the enabled endpoints for fan-out.
func (s *Store) ListDeliveryEndpoints(ctx context.Context) ([]app.WebhookEndpoint, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+webhookEndpointColumns+` FROM webhooks w WHERE w.enabled ORDER BY w.created_at,w.id LIMIT $1`, maxWebhooks)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var out []app.WebhookEndpoint
	for rows.Next() {
		targets, done := scanWebhookEndpoint()
		if err = rows.Scan(targets...); err != nil {
			return nil, storageError(err)
		}
		out = append(out, done())
	}
	return out, storageError(rows.Err())
}

func loadSealedWebhook(q pgx.Row) (app.WebhookSealedEndpoint, error) {
	var sealed app.WebhookSealedEndpoint
	var until *time.Time
	targets, done := scanWebhookEndpoint(&sealed.Keys.Current, &sealed.Keys.Previous, &until, &sealed.Headers)
	if err := q.Scan(targets...); err != nil {
		return sealed, storageError(err)
	}
	sealed.Endpoint = done()
	if until != nil {
		sealed.Keys.PreviousUntil = *until
	}
	return sealed, nil
}

const webhookSealedSQL = `SELECT ` + webhookEndpointColumns + `,w.secret_sealed,w.previous_secret_sealed,w.previous_until,w.headers_sealed FROM webhooks w WHERE w.id=$1::uuid`

// LoadWebhookEndpoint returns an endpoint with its sealed secrets and
// header values for the deliverer.
func (s *Store) LoadWebhookEndpoint(ctx context.Context, id string) (app.WebhookSealedEndpoint, error) {
	if !domain.ValidID(id) {
		return app.WebhookSealedEndpoint{}, domain.ErrNotFound
	}
	return loadSealedWebhook(s.Pool.QueryRow(ctx, webhookSealedSQL, id))
}

func (s *Store) WebhookForTest(ctx context.Context, actor domain.Actor, id string) (app.WebhookSealedEndpoint, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return app.WebhookSealedEndpoint{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(id) {
		return app.WebhookSealedEndpoint{}, domain.ErrNotFound
	}
	sealed, err := loadSealedWebhook(tx.QueryRow(ctx, webhookSealedSQL, id))
	if err != nil {
		return sealed, err
	}
	return sealed, storageError(tx.Commit(ctx))
}

// --- delivery log ---

const webhookDeliveryColumns = `d.id::text,d.webhook_id::text,o.event_id,o.event_type,o.occurred_at,d.state,d.attempts,d.round-1,d.next_attempt_at,COALESCE(d.last_outcome,''),COALESCE(d.last_status,0),d.last_attempt_at,d.created_at,d.seq`

func scanWebhookDelivery(row pgx.Row) (app.WebhookDeliveryView, error) {
	var v app.WebhookDeliveryView
	var seq int64
	err := row.Scan(&v.ID, &v.WebhookID, &v.EventID, &v.EventType, &v.OccurredAt, &v.State, &v.Attempts, &v.Replays, &v.NextAttemptAt,
		&v.LastOutcome, &v.LastStatus, &v.LastAttemptAt, &v.CreatedAt, &seq)
	v.Cursor = strconv.FormatInt(seq, 10)
	return v, storageError(err)
}

func webhookExists(ctx context.Context, tx pgx.Tx, id string) error {
	var found bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM webhooks WHERE id=$1::uuid)`, id).Scan(&found); err != nil {
		return storageError(err)
	}
	if !found {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) ListWebhookDeliveries(ctx context.Context, actor domain.Actor, webhookID string, state domain.WebhookDeliveryState, cursor string, limit int) ([]app.WebhookDeliveryView, error) {
	before := int64(0)
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n < 1 || strconv.FormatInt(n, 10) != cursor {
			return nil, domain.ErrInvalid
		}
		before = n
	}
	if limit < 1 || limit > app.MaxWebhookDeliveryPage {
		return nil, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(webhookID) {
		return nil, domain.ErrNotFound
	}
	if err = webhookExists(ctx, tx, webhookID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+webhookDeliveryColumns+` FROM webhook_deliveries d JOIN webhook_outbox o ON o.id=d.outbox_id
 WHERE d.webhook_id=$1::uuid AND ($2='' OR d.state=$2) AND ($3=0 OR d.seq<$3) ORDER BY d.seq DESC LIMIT $4`, webhookID, string(state), before, limit)
	if err != nil {
		return nil, storageError(err)
	}
	out := []app.WebhookDeliveryView{}
	for rows.Next() {
		v, err := scanWebhookDelivery(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return out, storageError(tx.Commit(ctx))
}

// maxWebhookHistory bounds the attempts returned with one delivery.
const maxWebhookHistory = 100

func (s *Store) GetWebhookDelivery(ctx context.Context, actor domain.Actor, webhookID, deliveryID string) (app.WebhookDeliveryDetail, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return app.WebhookDeliveryDetail{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(webhookID) || !domain.ValidID(deliveryID) {
		return app.WebhookDeliveryDetail{}, domain.ErrNotFound
	}
	v, err := scanWebhookDelivery(tx.QueryRow(ctx, `SELECT `+webhookDeliveryColumns+` FROM webhook_deliveries d JOIN webhook_outbox o ON o.id=d.outbox_id WHERE d.id=$1::uuid AND d.webhook_id=$2::uuid`, deliveryID, webhookID))
	if err != nil {
		return app.WebhookDeliveryDetail{}, err
	}
	detail := app.WebhookDeliveryDetail{WebhookDeliveryView: v, History: []app.WebhookAttemptView{}}
	rows, err := tx.Query(ctx, `SELECT round,attempt,started_at,finished_at,outcome,COALESCE(status_code,0),next_attempt_at FROM webhook_delivery_attempts
 WHERE delivery_id=$1::uuid ORDER BY round DESC,attempt DESC LIMIT $2`, deliveryID, maxWebhookHistory)
	if err != nil {
		return app.WebhookDeliveryDetail{}, storageError(err)
	}
	for rows.Next() {
		var a app.WebhookAttemptView
		if err = rows.Scan(&a.Round, &a.Attempt, &a.StartedAt, &a.FinishedAt, &a.Outcome, &a.StatusCode, &a.NextAttempt); err != nil {
			rows.Close()
			return app.WebhookDeliveryDetail{}, storageError(err)
		}
		detail.History = append(detail.History, a)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return app.WebhookDeliveryDetail{}, storageError(err)
	}
	return detail, storageError(tx.Commit(ctx))
}

func (s *Store) ReplayWebhookDelivery(ctx context.Context, actor domain.Actor, webhookID, deliveryID string, now time.Time) (app.WebhookDeliveryView, error) {
	if now.IsZero() {
		return app.WebhookDeliveryView{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return app.WebhookDeliveryView{}, err
	}
	defer tx.Rollback(ctx)
	if !domain.ValidID(webhookID) || !domain.ValidID(deliveryID) {
		return app.WebhookDeliveryView{}, domain.ErrNotFound
	}
	var previous string
	err = tx.QueryRow(ctx, `SELECT state FROM webhook_deliveries WHERE id=$1::uuid AND webhook_id=$2::uuid FOR UPDATE`, deliveryID, webhookID).Scan(&previous)
	if err != nil {
		return app.WebhookDeliveryView{}, storageError(err)
	}
	if previous == string(domain.WebhookDeliveryPending) {
		return app.WebhookDeliveryView{}, domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE webhook_deliveries SET state='pending',attempts=0,round=round+1,next_attempt_at=$2,lease_until=NULL,lease_token=NULL WHERE id=$1::uuid`, deliveryID, now); err != nil {
		return app.WebhookDeliveryView{}, storageError(err)
	}
	v, err := scanWebhookDelivery(tx.QueryRow(ctx, `SELECT `+webhookDeliveryColumns+` FROM webhook_deliveries d JOIN webhook_outbox o ON o.id=d.outbox_id WHERE d.id=$1::uuid`, deliveryID))
	if err != nil {
		return v, err
	}
	if err = auditAccount(ctx, tx, actor, "webhook.delivery_replayed", deliveryID, map[string]any{"state": previous},
		map[string]any{"webhookId": webhookID, "eventId": v.EventID, "eventType": v.EventType, "round": v.Replays + 1}); err != nil {
		return app.WebhookDeliveryView{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

// --- delivery store ---

const webhookEventColumns = `o.event_id,o.event_type,o.version,o.occurred_at,o.subject_kind,COALESCE(o.subject_id,''),o.data`

// scanWebhookEvent rebuilds the envelope. A row that cannot be decoded
// yields an event that fails validation, so the deliverer dead-letters it
// as invalid instead of sending it.
func scanWebhookEvent(targets ...any) ([]any, func() domain.WebhookEvent) {
	var e domain.WebhookEvent
	var kind string
	var data []byte
	all := append(targets, &e.EventID, &e.Type, &e.Version, &e.OccurredAt, &kind, &e.Subject.ID, &data)
	return all, func() domain.WebhookEvent {
		e.Subject.Kind = domain.WebhookSubjectKind(kind)
		e.OccurredAt = e.OccurredAt.UTC().Truncate(time.Millisecond)
		if len(data) > 0 {
			if err := json.Unmarshal(data, &e.Data); err != nil {
				e.Version = 0
			}
		}
		return e
	}
}

func (s *Store) FetchUnplanned(ctx context.Context, limit int) ([]domain.WebhookEvent, error) {
	if limit < 1 || limit > 1000 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+webhookEventColumns+` FROM webhook_outbox o WHERE o.planned_at IS NULL ORDER BY o.id LIMIT $1`, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var out []domain.WebhookEvent
	for rows.Next() {
		targets, done := scanWebhookEvent()
		if err = rows.Scan(targets...); err != nil {
			return nil, storageError(err)
		}
		out = append(out, done())
	}
	return out, storageError(rows.Err())
}

// CreateDeliveries fans one event out. Concurrent deliverers skip an event
// another one is planning; repeating it is a no-op. An event no enabled
// endpoint takes is deleted at once.
func (s *Store) CreateDeliveries(ctx context.Context, event domain.WebhookEvent, endpointIDs []string, due time.Time) error {
	if !domain.ValidWebhookEventID(event.EventID) || len(endpointIDs) > maxWebhooks || due.IsZero() {
		return domain.ErrInvalid
	}
	for _, id := range endpointIDs {
		if !domain.ValidID(id) {
			return domain.ErrInvalid
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback(ctx)
	var outbox int64
	var planned bool
	err = tx.QueryRow(ctx, `SELECT id,planned_at IS NOT NULL FROM webhook_outbox WHERE event_id=$1 FOR UPDATE SKIP LOCKED`, event.EventID).Scan(&outbox, &planned)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && planned {
		return nil
	}
	if err != nil {
		return storageError(err)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO webhook_deliveries(outbox_id,webhook_id,next_attempt_at)
 SELECT o.id,w.id,$3 FROM webhook_outbox o JOIN webhooks w ON w.id=ANY($2::uuid[]) AND w.enabled WHERE o.id=$1
 ON CONFLICT (outbox_id,webhook_id) DO NOTHING`, outbox, endpointIDs, due)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() == 0 {
		_, err = tx.Exec(ctx, `DELETE FROM webhook_outbox WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM webhook_deliveries WHERE outbox_id=$1)`, outbox)
	} else {
		_, err = tx.Exec(ctx, `UPDATE webhook_outbox SET planned_at=clock_timestamp() WHERE id=$1`, outbox)
	}
	if err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}

// ClaimDue leases due deliveries of enabled endpoints. Times come from the
// caller's clock so leases and schedules share one time base.
func (s *Store) ClaimDue(ctx context.Context, now, leaseUntil time.Time, limit int) ([]app.WebhookDelivery, error) {
	if now.IsZero() || !leaseUntil.After(now) || limit < 1 || limit > 1000 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `WITH due AS (
 SELECT d.id FROM webhook_deliveries d JOIN webhooks w ON w.id=d.webhook_id AND w.enabled
 WHERE d.state='pending' AND d.next_attempt_at<=$1 AND (d.lease_until IS NULL OR d.lease_until<=$1)
 ORDER BY d.next_attempt_at,d.seq LIMIT $3 FOR UPDATE OF d SKIP LOCKED
)
UPDATE webhook_deliveries d SET lease_until=$2,lease_token=gen_random_uuid() FROM due,webhook_outbox o
 WHERE d.id=due.id AND o.id=d.outbox_id
RETURNING d.id::text,d.webhook_id::text,d.attempts,d.lease_token::text,`+webhookEventColumns, now, leaseUntil, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var out []app.WebhookDelivery
	for rows.Next() {
		var d app.WebhookDelivery
		targets, done := scanWebhookEvent(&d.ID, &d.EndpointID, &d.Attempts, &d.LeaseToken)
		if err = rows.Scan(targets...); err != nil {
			return nil, storageError(err)
		}
		d.Event = done()
		out = append(out, d)
	}
	return out, storageError(rows.Err())
}

func validWebhookOutcome(k domain.WebhookOutcomeKind) bool {
	switch k {
	case domain.WebhookOutcomeDelivered, domain.WebhookOutcomeHTTP, domain.WebhookOutcomeTimeout, domain.WebhookOutcomeNetwork,
		domain.WebhookOutcomeBlocked, domain.WebhookOutcomeTLS, domain.WebhookOutcomeInvalid:
		return true
	}
	return false
}

func (s *Store) RecordAttempt(ctx context.Context, r app.WebhookAttemptRecord) error {
	status := r.Outcome.StatusCode
	if !domain.ValidID(r.DeliveryID) || !domain.ValidID(r.LeaseToken) || r.Attempt < 1 || r.Attempt > domain.MaxWebhookAttempts || r.StartedAt.IsZero() || r.FinishedAt.IsZero() ||
		!validWebhookOutcome(r.Outcome.Kind) || status != 0 && (status < 100 || status > 599) {
		return domain.ErrInvalid
	}
	var next *time.Time
	switch r.Decision.State {
	case domain.WebhookDeliveryPending:
		if r.Decision.NextAt.IsZero() {
			return domain.ErrInvalid
		}
		next = &r.Decision.NextAt
	case domain.WebhookDeliveryDelivered, domain.WebhookDeliveryDead:
	default:
		return domain.ErrInvalid
	}
	var statusValue *int
	if status != 0 {
		statusValue = &status
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback(ctx)
	var round int
	err = tx.QueryRow(ctx, `UPDATE webhook_deliveries SET attempts=$3,state=$4,next_attempt_at=$5,lease_until=NULL,lease_token=NULL,last_outcome=$6,last_status=$7,last_attempt_at=$8
 WHERE id=$1::uuid AND lease_token=$2::uuid AND state='pending' AND attempts=$3-1 RETURNING round`,
		r.DeliveryID, r.LeaseToken, r.Attempt, string(r.Decision.State), next, string(r.Outcome.Kind), statusValue, r.FinishedAt).Scan(&round)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrConflict
	}
	if err != nil {
		return storageError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO webhook_delivery_attempts(delivery_id,round,attempt,started_at,finished_at,outcome,status_code,next_attempt_at) VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8)`,
		r.DeliveryID, round, r.Attempt, r.StartedAt, r.FinishedAt, string(r.Outcome.Kind), statusValue, next); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}

// PurgeWebhookHistory deletes settled events created before cutoff together
// with their deliveries and attempt log. Events with a pending delivery are
// kept until it settles. Rows another instance is purging or planning are
// skipped rather than waited for.
func (s *Store) PurgeWebhookHistory(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if cutoff.IsZero() || limit < 1 || limit > 10000 {
		return 0, domain.ErrInvalid
	}
	tag, err := s.Pool.Exec(ctx, `DELETE FROM webhook_outbox WHERE id IN (
 SELECT o.id FROM webhook_outbox o WHERE o.created_at<$1 AND o.planned_at IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM webhook_deliveries d WHERE d.outbox_id=o.id AND d.state='pending')
 ORDER BY o.created_at LIMIT $2 FOR UPDATE SKIP LOCKED)`, cutoff, limit)
	if err != nil {
		return 0, storageError(err)
	}
	return int(tag.RowsAffected()), nil
}
