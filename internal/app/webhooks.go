package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// G12 webhook ports. The PostgreSQL adapter implements the repositories
// (tables webhook_outbox/webhooks/webhook_deliveries), internal/adapter/events
// the HTTP deliverer behind the G11.4/G12.5 outbound client. This file fixes
// the contracts and the pure steps the background deliverer composes:
//
//	producer tx: WebhookOutbox.Append (same transaction as the change)
//	fan-out:     PlanWebhookFanout -> WebhookDeliveryStore.CreateDeliveries
//	deliver:     ClaimDue -> BuildWebhookRequest -> WebhookDeliverer.Deliver
//	             -> ResolveWebhookAttempt -> WebhookDeliveryStore.RecordAttempt
//
// Delivery is at least once: a crash between Deliver and RecordAttempt
// re-sends after the claim lease expires, with the same eventId.

// WebhookOutbox appends events inside the caller's transaction so an event
// exists if and only if the change that caused it committed.
type WebhookOutbox interface {
	Append(context.Context, domain.WebhookEvent) error
}

// WebhookEndpoint is the delivery view of one configured endpoint. URL is
// validated by the outbound network layer (G12.5), not here; the secret is
// loaded separately so listings never carry it.
type WebhookEndpoint struct {
	ID      string
	URL     string
	Enabled bool
	Filter  []domain.WebhookEventType
	Headers map[string]string
	Timeout time.Duration
	Retry   domain.WebhookRetryPolicy
}

const (
	MinWebhookTimeout     = time.Second
	MaxWebhookTimeout     = 30 * time.Second
	DefaultWebhookTimeout = 10 * time.Second
	// MaxWebhookBodyBytes bounds the serialized envelope.
	MaxWebhookBodyBytes = 64 << 10
)

// ValidateWebhookEndpoint checks the configuration fields owned by the
// application layer.
func ValidateWebhookEndpoint(e WebhookEndpoint) error {
	if e.ID == "" || e.URL == "" || e.Timeout < MinWebhookTimeout || e.Timeout > MaxWebhookTimeout {
		return domain.ErrInvalid
	}
	if _, err := domain.NormalizeWebhookFilter(e.Filter); err != nil {
		return err
	}
	if err := domain.ValidateWebhookHeaders(e.Headers); err != nil {
		return err
	}
	return e.Retry.Validate()
}

// WebhookSealedKeys are an endpoint's signing secrets as stored: sealed by
// the WebhookSecretBox under WebhookSecretContext(endpointID). Storage never
// holds or returns them in plain text.
type WebhookSealedKeys struct {
	Current       []byte
	Previous      []byte
	PreviousUntil time.Time
}

// WebhookSealedEndpoint is an endpoint as the deliverer loads it. Endpoint
// carries no headers; Headers is the sealed JSON object of custom header
// values (nil when there are none), sealed under
// WebhookHeadersContext(endpointID).
type WebhookSealedEndpoint struct {
	Endpoint WebhookEndpoint
	Keys     WebhookSealedKeys
	Headers  []byte
}

// WebhookRepository reads endpoint configuration.
type WebhookRepository interface {
	// ListDeliveryEndpoints returns enabled endpoints only, without headers.
	ListDeliveryEndpoints(context.Context) ([]WebhookEndpoint, error)
	// LoadWebhookEndpoint returns domain.ErrNotFound for a deleted endpoint.
	LoadWebhookEndpoint(ctx context.Context, endpointID string) (WebhookSealedEndpoint, error)
}

// WebhookDelivery is one claimed (event, endpoint) pair.
type WebhookDelivery struct {
	ID         string
	EndpointID string
	Event      domain.WebhookEvent
	// Attempts already recorded before this claim (since the last replay).
	Attempts int
	// LeaseToken fences RecordAttempt: a claim whose lease expired and was
	// taken over can no longer record.
	LeaseToken string
}

// WebhookAttemptRecord is appended to the queryable delivery log and moves
// the delivery to Decision.State.
type WebhookAttemptRecord struct {
	DeliveryID string
	LeaseToken string
	Attempt    int
	StartedAt  time.Time
	FinishedAt time.Time
	Outcome    domain.WebhookOutcome
	Decision   domain.WebhookRetryDecision
}

// WebhookDeliveryStore owns webhook_deliveries and its attempt log.
type WebhookDeliveryStore interface {
	// FetchUnplanned returns outbox events not yet fanned out, oldest first.
	FetchUnplanned(ctx context.Context, limit int) ([]domain.WebhookEvent, error)
	// CreateDeliveries inserts the pending deliveries for event, due at
	// due, and marks it planned in one transaction; repeating it for the
	// same event is a no-op (unique on eventId, endpointId).
	CreateDeliveries(ctx context.Context, event domain.WebhookEvent, endpointIDs []string, due time.Time) error
	// ClaimDue leases up to limit pending deliveries with NextAt <= now
	// until leaseUntil. An expired lease makes a delivery claimable again.
	ClaimDue(ctx context.Context, now, leaseUntil time.Time, limit int) ([]WebhookDelivery, error)
	// RecordAttempt stores the attempt and the decision atomically and
	// releases the lease. A lease that is no longer held is
	// domain.ErrConflict and records nothing.
	RecordAttempt(context.Context, WebhookAttemptRecord) error
	// PurgeWebhookHistory deletes at most limit events created before cutoff
	// that have no pending delivery, with their deliveries and attempt log,
	// and reports how many it deleted.
	PurgeWebhookHistory(ctx context.Context, cutoff time.Time, limit int) (int, error)
}

// Manual replay (G12.3) is an administrator operation with an audit entry,
// so it lives on WebhookAdminRepository.ReplayWebhookDelivery.

// WebhookRequest is a fully signed request ready for the network.
type WebhookRequest struct {
	EndpointID string
	URL        string
	Timeout    time.Duration
	Headers    map[string]string
	Body       []byte
}

// WebhookDeliverer performs one HTTP POST through the SSRF-guarded client.
// It must not follow redirects and must classify every failure into a
// domain.WebhookOutcome instead of returning transport errors; the error
// result is reserved for ctx cancellation.
type WebhookDeliverer interface {
	Deliver(context.Context, WebhookRequest) (domain.WebhookOutcome, error)
}

// PlanWebhookFanout returns the IDs of enabled endpoints subscribed to the
// event, in input order.
func PlanWebhookFanout(event domain.WebhookEvent, endpoints []WebhookEndpoint) []string {
	var ids []string
	for _, e := range endpoints {
		if e.Enabled && domain.WebhookFilterMatches(e.Filter, event.Type) {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

var errWebhookBodyTooLarge = errors.New("webhook body too large")

// EncodeWebhookEvent re-validates (and so re-checks redaction of) the event
// and serializes it without HTML escaping.
func EncodeWebhookEvent(event domain.WebhookEvent) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(event); err != nil {
		return nil, err
	}
	body := bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
	if len(body) > MaxWebhookBodyBytes {
		return nil, errWebhookBodyTooLarge
	}
	return body, nil
}

// BuildWebhookRequest signs event for endpoint at now. Custom headers are
// applied first so the reserved Content-Type and X-Jelee-* values always win
// (ValidateWebhookHeaders also rejects them at configuration time).
func BuildWebhookRequest(endpoint WebhookEndpoint, keys domain.WebhookSigningKeys, event domain.WebhookEvent, now time.Time) (WebhookRequest, error) {
	if err := ValidateWebhookEndpoint(endpoint); err != nil {
		return WebhookRequest{}, err
	}
	body, err := EncodeWebhookEvent(event)
	if err != nil {
		return WebhookRequest{}, err
	}
	signed, err := domain.SignWebhook(keys, body, now)
	if err != nil {
		return WebhookRequest{}, err
	}
	headers := make(map[string]string, len(endpoint.Headers)+4)
	for k, v := range endpoint.Headers {
		headers[k] = v
	}
	headers["Content-Type"] = "application/json"
	headers[domain.WebhookTimestampHeader] = signed.Timestamp
	headers[domain.WebhookSignatureHeader] = signed.Signature
	headers[domain.WebhookEventIDHeader] = event.EventID
	return WebhookRequest{EndpointID: endpoint.ID, URL: endpoint.URL, Timeout: endpoint.Timeout, Headers: headers, Body: body}, nil
}

// ResolveWebhookAttempt turns the outcome of the attempt that started after
// delivery.Attempts previous ones into the record to store.
func ResolveWebhookAttempt(delivery WebhookDelivery, policy domain.WebhookRetryPolicy, outcome domain.WebhookOutcome, startedAt, finishedAt time.Time, jitter domain.WebhookJitter) WebhookAttemptRecord {
	attempt := delivery.Attempts + 1
	return WebhookAttemptRecord{
		DeliveryID: delivery.ID,
		LeaseToken: delivery.LeaseToken,
		Attempt:    attempt,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Outcome:    outcome,
		Decision:   domain.DecideWebhookRetry(policy, attempt, outcome, finishedAt, jitter),
	}
}
