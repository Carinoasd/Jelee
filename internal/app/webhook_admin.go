package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// WebhookSecretBox seals endpoint secrets and header values with the
// environment master key (AES-GCM in production). context binds a sealed
// value to its endpoint and purpose, so a value copied into another row or
// column cannot be opened there. Errors never contain key material.
type WebhookSecretBox interface {
	Seal(context string, plaintext []byte) ([]byte, error)
	Open(context string, sealed []byte) ([]byte, error)
}

// WebhookSecretContext and WebhookHeadersContext name the sealing contexts
// of an endpoint's signing secrets and custom header values.
func WebhookSecretContext(endpointID string) string  { return "jelee-webhook-secret:" + endpointID }
func WebhookHeadersContext(endpointID string) string { return "jelee-webhook-headers:" + endpointID }

// WebhookTargetPolicy checks an endpoint URL against the G12.5 target policy
// before it is stored. Refusals are domain.ErrWebhookTargetDenied. The
// delivery itself is checked again, including DNS answers, by the outbound
// client.
type WebhookTargetPolicy interface {
	CheckWebhookTarget(rawURL string) error
}

// WebhookEndpointView is an endpoint as administrators read it. Neither the
// signing secret nor header values are ever returned; HeaderNames lists the
// configured custom headers.
type WebhookEndpointView struct {
	ID                  string                    `json:"id"`
	Name                string                    `json:"name"`
	URL                 string                    `json:"url"`
	Enabled             bool                      `json:"enabled"`
	Events              []domain.WebhookEventType `json:"events"`
	HeaderNames         []string                  `json:"headerNames"`
	TimeoutSeconds      int                       `json:"timeoutSeconds"`
	Retry               WebhookRetryView          `json:"retry"`
	PreviousSecretUntil *time.Time                `json:"previousSecretUntil,omitempty"`
	Pending             int64                     `json:"pending"`
	Dead                int64                     `json:"dead"`
	CreatedAt           time.Time                 `json:"createdAt"`
	UpdatedAt           time.Time                 `json:"updatedAt"`
}

// WebhookRetryView is the retry policy in API units.
type WebhookRetryView struct {
	MaxAttempts      int     `json:"maxAttempts"`
	BaseDelaySeconds int     `json:"baseDelaySeconds"`
	MaxDelaySeconds  int     `json:"maxDelaySeconds"`
	Jitter           float64 `json:"jitter"`
}

func (r WebhookRetryView) Policy() domain.WebhookRetryPolicy {
	return domain.WebhookRetryPolicy{MaxAttempts: r.MaxAttempts, BaseDelay: time.Duration(r.BaseDelaySeconds) * time.Second,
		MaxDelay: time.Duration(r.MaxDelaySeconds) * time.Second, Jitter: r.Jitter}
}

func RetryView(p domain.WebhookRetryPolicy) WebhookRetryView {
	return WebhookRetryView{MaxAttempts: p.MaxAttempts, BaseDelaySeconds: int(p.BaseDelay / time.Second), MaxDelaySeconds: int(p.MaxDelay / time.Second), Jitter: p.Jitter}
}

// WebhookEndpointInput is a create or replace request. Headers nil keeps the
// stored headers on replace; an empty map removes them.
type WebhookEndpointInput struct {
	Name           string
	URL            string
	Enabled        bool
	Events         []domain.WebhookEventType
	Headers        map[string]string
	TimeoutSeconds int
	Retry          *WebhookRetryView
}

// WebhookEndpointRecord is what the service hands to storage: validated
// configuration with header values and the secret already sealed.
type WebhookEndpointRecord struct {
	ID          string
	Name        string
	URL         string
	Enabled     bool
	Events      []domain.WebhookEventType
	HeaderNames []string
	// SealedHeaders is nil when the endpoint has no custom headers. On
	// replace it is applied only when ReplaceHeaders is set.
	SealedHeaders  []byte
	ReplaceHeaders bool
	// SealedSecret is set on create only.
	SealedSecret []byte
	Timeout      time.Duration
	Retry        domain.WebhookRetryPolicy
}

// WebhookDeliveryView is one row of the delivery log (G12.3).
type WebhookDeliveryView struct {
	ID            string                      `json:"id"`
	WebhookID     string                      `json:"webhookId"`
	EventID       string                      `json:"eventId"`
	EventType     domain.WebhookEventType     `json:"eventType"`
	OccurredAt    time.Time                   `json:"occurredAt"`
	State         domain.WebhookDeliveryState `json:"state"`
	Attempts      int                         `json:"attempts"`
	Replays       int                         `json:"replays"`
	NextAttemptAt *time.Time                  `json:"nextAttemptAt,omitempty"`
	LastOutcome   domain.WebhookOutcomeKind   `json:"lastOutcome,omitempty"`
	LastStatus    int                         `json:"lastStatus,omitempty"`
	LastAttemptAt *time.Time                  `json:"lastAttemptAt,omitempty"`
	CreatedAt     time.Time                   `json:"createdAt"`
	// Cursor orders the log; pass the last one as the next page cursor.
	Cursor string `json:"-"`
}

// WebhookAttemptView is one recorded attempt. Round counts manual replays:
// attempt numbers restart at 1 in every round.
type WebhookAttemptView struct {
	Round       int                       `json:"round"`
	Attempt     int                       `json:"attempt"`
	StartedAt   time.Time                 `json:"startedAt"`
	FinishedAt  time.Time                 `json:"finishedAt"`
	Outcome     domain.WebhookOutcomeKind `json:"outcome"`
	StatusCode  int                       `json:"statusCode,omitempty"`
	NextAttempt *time.Time                `json:"nextAttemptAt,omitempty"`
}

// WebhookDeliveryDetail is a delivery with its most recent attempts.
type WebhookDeliveryDetail struct {
	WebhookDeliveryView
	History []WebhookAttemptView `json:"history"`
}

// WebhookAdminRepository stores endpoint configuration. Every method checks
// in the same transaction that actor is a live administrator, and every
// change (create, replace, delete, secret rotation, replay) records its
// audit entry in that transaction.
type WebhookAdminRepository interface {
	ListWebhooks(ctx context.Context, actor domain.Actor) ([]WebhookEndpointView, error)
	GetWebhook(ctx context.Context, actor domain.Actor, id string) (WebhookEndpointView, error)
	CreateWebhook(ctx context.Context, actor domain.Actor, record WebhookEndpointRecord) (WebhookEndpointView, error)
	UpdateWebhook(ctx context.Context, actor domain.Actor, record WebhookEndpointRecord) (WebhookEndpointView, error)
	DeleteWebhook(ctx context.Context, actor domain.Actor, id string) error
	// RotateWebhookSecret makes sealed the current secret and keeps the
	// previous one signing until previousUntil.
	RotateWebhookSecret(ctx context.Context, actor domain.Actor, id string, sealed []byte, previousUntil time.Time) (WebhookEndpointView, error)
	// WebhookForTest loads any endpoint, enabled or not, for a test send.
	WebhookForTest(ctx context.Context, actor domain.Actor, id string) (WebhookSealedEndpoint, error)
	// ListWebhookDeliveries pages the log newest first; state may be empty.
	ListWebhookDeliveries(ctx context.Context, actor domain.Actor, webhookID string, state domain.WebhookDeliveryState, cursor string, limit int) ([]WebhookDeliveryView, error)
	GetWebhookDelivery(ctx context.Context, actor domain.Actor, webhookID, deliveryID string) (WebhookDeliveryDetail, error)
	// ReplayWebhookDelivery moves a dead or delivered delivery back to
	// pending, due at now, with a fresh attempt budget and the same eventId.
	// A pending delivery is domain.ErrConflict.
	ReplayWebhookDelivery(ctx context.Context, actor domain.Actor, webhookID, deliveryID string, now time.Time) (WebhookDeliveryView, error)
}

// WebhookOptions configure the administration service.
type WebhookOptions struct {
	Clock Clock
	// Random fills b with cryptographically secure bytes; crypto/rand by
	// default.
	Random func(b []byte) error
}

// Webhooks is the G12.2 administration service.
type Webhooks struct {
	repo      WebhookAdminRepository
	box       WebhookSecretBox
	target    WebhookTargetPolicy
	deliverer WebhookDeliverer
	clock     Clock
	random    func([]byte) error
}

func NewWebhooks(repo WebhookAdminRepository, box WebhookSecretBox, target WebhookTargetPolicy, deliverer WebhookDeliverer, opts WebhookOptions) (*Webhooks, error) {
	if repo == nil || box == nil || target == nil || deliverer == nil {
		return nil, errors.New("webhook dependencies must be provided")
	}
	w := &Webhooks{repo: repo, box: box, target: target, deliverer: deliverer, clock: opts.Clock, random: opts.Random}
	if w.clock == nil {
		w.clock = systemClock{}
	}
	if w.random == nil {
		w.random = func(b []byte) error { _, err := rand.Read(b); return err }
	}
	return w, nil
}

const (
	maxWebhookNameBytes = 128
	maxWebhookURLBytes  = 2048
	// webhookSecretPrefix marks Jelee signing secrets. The whole string,
	// prefix included, is the HMAC key.
	webhookSecretPrefix = "whsec_"
	// DefaultWebhookRotationGrace keeps the previous secret signing after a
	// rotation so consumers can switch at their own pace.
	DefaultWebhookRotationGrace = 24 * time.Hour
	MaxWebhookRotationGrace     = 7 * 24 * time.Hour
	// MaxWebhookDeliveryPage bounds one page of the delivery log.
	MaxWebhookDeliveryPage = 100
)

func validWebhookName(name string) bool {
	if name == "" || len(name) > maxWebhookNameBytes || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (w *Webhooks) uuid() (string, error) {
	var b [16]byte
	if err := w.random(b[:]); err != nil {
		return "", domain.ErrDatabase
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

func (w *Webhooks) newSecret() (string, error) {
	var b [32]byte
	if err := w.random(b[:]); err != nil {
		return "", domain.ErrDatabase
	}
	return webhookSecretPrefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// record validates input for endpoint id and seals its headers.
func (w *Webhooks) record(id string, in WebhookEndpointInput, replace bool) (WebhookEndpointRecord, error) {
	if !validWebhookName(in.Name) || in.URL == "" || len(in.URL) > maxWebhookURLBytes || !utf8.ValidString(in.URL) {
		return WebhookEndpointRecord{}, domain.ErrInvalid
	}
	if err := w.target.CheckWebhookTarget(in.URL); err != nil {
		return WebhookEndpointRecord{}, err
	}
	events, err := domain.NormalizeWebhookFilter(in.Events)
	if err != nil {
		return WebhookEndpointRecord{}, err
	}
	timeout := DefaultWebhookTimeout
	if in.TimeoutSeconds != 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	retry := domain.DefaultWebhookRetryPolicy()
	if in.Retry != nil {
		retry = in.Retry.Policy()
	}
	endpoint := WebhookEndpoint{ID: id, URL: in.URL, Enabled: in.Enabled, Filter: events, Headers: in.Headers, Timeout: timeout, Retry: retry}
	if err := ValidateWebhookEndpoint(endpoint); err != nil {
		return WebhookEndpointRecord{}, err
	}
	r := WebhookEndpointRecord{ID: id, Name: in.Name, URL: in.URL, Enabled: in.Enabled, Events: events, Timeout: timeout, Retry: retry}
	if !replace || in.Headers != nil {
		r.ReplaceHeaders = true
		r.HeaderNames = []string{}
		if len(in.Headers) > 0 {
			for name := range in.Headers {
				r.HeaderNames = append(r.HeaderNames, name)
			}
			sort.Strings(r.HeaderNames)
			raw, err := json.Marshal(in.Headers)
			if err != nil {
				return WebhookEndpointRecord{}, domain.ErrInvalid
			}
			if r.SealedHeaders, err = w.box.Seal(WebhookHeadersContext(id), raw); err != nil {
				return WebhookEndpointRecord{}, err
			}
		}
	}
	return r, nil
}

func (w *Webhooks) List(ctx context.Context, actor domain.Actor) ([]WebhookEndpointView, error) {
	return w.repo.ListWebhooks(ctx, actor)
}

func (w *Webhooks) Get(ctx context.Context, actor domain.Actor, id string) (WebhookEndpointView, error) {
	if !domain.ValidID(id) {
		return WebhookEndpointView{}, domain.ErrNotFound
	}
	return w.repo.GetWebhook(ctx, actor, id)
}

// Create stores a new endpoint and returns its signing secret. The secret is
// returned only here and by Rotate; storage keeps it sealed.
func (w *Webhooks) Create(ctx context.Context, actor domain.Actor, in WebhookEndpointInput) (WebhookEndpointView, string, error) {
	id, err := w.uuid()
	if err != nil {
		return WebhookEndpointView{}, "", err
	}
	r, err := w.record(id, in, false)
	if err != nil {
		return WebhookEndpointView{}, "", err
	}
	secret, err := w.newSecret()
	if err != nil {
		return WebhookEndpointView{}, "", err
	}
	if r.SealedSecret, err = w.box.Seal(WebhookSecretContext(id), []byte(secret)); err != nil {
		return WebhookEndpointView{}, "", err
	}
	view, err := w.repo.CreateWebhook(ctx, actor, r)
	if err != nil {
		return WebhookEndpointView{}, "", err
	}
	return view, secret, nil
}

// Update replaces an endpoint's configuration, including enabling and
// disabling it. Pending deliveries of a disabled endpoint wait until it is
// enabled again; events raised while it is disabled are not delivered to it.
func (w *Webhooks) Update(ctx context.Context, actor domain.Actor, id string, in WebhookEndpointInput) (WebhookEndpointView, error) {
	if !domain.ValidID(id) {
		return WebhookEndpointView{}, domain.ErrNotFound
	}
	r, err := w.record(id, in, true)
	if err != nil {
		return WebhookEndpointView{}, err
	}
	return w.repo.UpdateWebhook(ctx, actor, r)
}

func (w *Webhooks) Delete(ctx context.Context, actor domain.Actor, id string) error {
	if !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	return w.repo.DeleteWebhook(ctx, actor, id)
}

// Rotate issues a new signing secret. During grace both the new and the
// previous secret sign every delivery (G12.4); grace 0 drops the previous
// secret at once.
func (w *Webhooks) Rotate(ctx context.Context, actor domain.Actor, id string, grace time.Duration) (WebhookEndpointView, string, error) {
	if !domain.ValidID(id) {
		return WebhookEndpointView{}, "", domain.ErrNotFound
	}
	if grace < 0 || grace > MaxWebhookRotationGrace {
		return WebhookEndpointView{}, "", domain.ErrInvalid
	}
	secret, err := w.newSecret()
	if err != nil {
		return WebhookEndpointView{}, "", err
	}
	sealed, err := w.box.Seal(WebhookSecretContext(id), []byte(secret))
	if err != nil {
		return WebhookEndpointView{}, "", err
	}
	var until time.Time
	if grace > 0 {
		until = w.clock.Now().Add(grace)
	}
	view, err := w.repo.RotateWebhookSecret(ctx, actor, id, sealed, until)
	if err != nil {
		return WebhookEndpointView{}, "", err
	}
	return view, secret, nil
}

// WebhookTestResult reports a test send.
type WebhookTestResult struct {
	EventID    string                    `json:"eventId"`
	Outcome    domain.WebhookOutcomeKind `json:"outcome"`
	StatusCode int                       `json:"statusCode,omitempty"`
	DurationMS int64                     `json:"durationMs"`
}

// Test sends one signed system.alert event with data {"test": true}
// directly, outside the outbox: it is neither retried nor logged as a
// delivery. Disabled endpoints can be tested.
func (w *Webhooks) Test(ctx context.Context, actor domain.Actor, id string) (WebhookTestResult, error) {
	if !domain.ValidID(id) {
		return WebhookTestResult{}, domain.ErrNotFound
	}
	sealed, err := w.repo.WebhookForTest(ctx, actor, id)
	if err != nil {
		return WebhookTestResult{}, err
	}
	endpoint, keys, err := OpenWebhookEndpoint(w.box, sealed)
	if err != nil {
		return WebhookTestResult{}, err
	}
	var raw [16]byte
	if err = w.random(raw[:]); err != nil {
		return WebhookTestResult{}, domain.ErrDatabase
	}
	started := w.clock.Now()
	event, err := domain.NewWebhookEvent("test-"+hex.EncodeToString(raw[:]), domain.WebhookSystemAlert, started,
		domain.WebhookSubject{Kind: domain.WebhookSubjectSystem}, map[string]any{"test": true, "message": "Jelee webhook test"})
	if err != nil {
		return WebhookTestResult{}, err
	}
	request, err := BuildWebhookRequest(endpoint, keys, event, started)
	if err != nil {
		return WebhookTestResult{}, err
	}
	outcome, err := w.deliverer.Deliver(ctx, request)
	if err != nil {
		return WebhookTestResult{}, err
	}
	return WebhookTestResult{EventID: event.EventID, Outcome: outcome.Kind, StatusCode: outcome.StatusCode, DurationMS: w.clock.Now().Sub(started).Milliseconds()}, nil
}

func (w *Webhooks) Deliveries(ctx context.Context, actor domain.Actor, webhookID string, state domain.WebhookDeliveryState, cursor string, limit int) ([]WebhookDeliveryView, error) {
	if !domain.ValidID(webhookID) {
		return nil, domain.ErrNotFound
	}
	switch state {
	case "", domain.WebhookDeliveryPending, domain.WebhookDeliveryDelivered, domain.WebhookDeliveryDead:
	default:
		return nil, domain.ErrInvalid
	}
	if limit < 1 || limit > MaxWebhookDeliveryPage {
		return nil, domain.ErrInvalid
	}
	return w.repo.ListWebhookDeliveries(ctx, actor, webhookID, state, cursor, limit)
}

func (w *Webhooks) Delivery(ctx context.Context, actor domain.Actor, webhookID, deliveryID string) (WebhookDeliveryDetail, error) {
	if !domain.ValidID(webhookID) || !domain.ValidID(deliveryID) {
		return WebhookDeliveryDetail{}, domain.ErrNotFound
	}
	return w.repo.GetWebhookDelivery(ctx, actor, webhookID, deliveryID)
}

// Replay re-queues a dead or delivered delivery (G12.3). The consumer
// receives the same eventId again and deduplicates on it (G12.6).
func (w *Webhooks) Replay(ctx context.Context, actor domain.Actor, webhookID, deliveryID string) (WebhookDeliveryView, error) {
	if !domain.ValidID(webhookID) || !domain.ValidID(deliveryID) {
		return WebhookDeliveryView{}, domain.ErrNotFound
	}
	return w.repo.ReplayWebhookDelivery(ctx, actor, webhookID, deliveryID, w.clock.Now())
}

// OpenWebhookEndpoint unseals an endpoint's signing keys and header values.
func OpenWebhookEndpoint(box WebhookSecretBox, sealed WebhookSealedEndpoint) (WebhookEndpoint, domain.WebhookSigningKeys, error) {
	endpoint := sealed.Endpoint
	id := endpoint.ID
	current, err := box.Open(WebhookSecretContext(id), sealed.Keys.Current)
	if err != nil {
		return WebhookEndpoint{}, domain.WebhookSigningKeys{}, errWebhookSealed
	}
	keys := domain.WebhookSigningKeys{Current: domain.WebhookSecret(current)}
	if len(sealed.Keys.Previous) > 0 {
		previous, err := box.Open(WebhookSecretContext(id), sealed.Keys.Previous)
		if err != nil {
			return WebhookEndpoint{}, domain.WebhookSigningKeys{}, errWebhookSealed
		}
		keys.Previous, keys.PreviousUntil = domain.WebhookSecret(previous), sealed.Keys.PreviousUntil
	}
	endpoint.Headers = nil
	if len(sealed.Headers) > 0 {
		raw, err := box.Open(WebhookHeadersContext(id), sealed.Headers)
		if err != nil {
			return WebhookEndpoint{}, domain.WebhookSigningKeys{}, errWebhookSealed
		}
		if err = json.Unmarshal(raw, &endpoint.Headers); err != nil {
			return WebhookEndpoint{}, domain.WebhookSigningKeys{}, errWebhookSealed
		}
	}
	return endpoint, keys, nil
}

// errWebhookSealed: a stored secret cannot be opened with the configured
// master key (changed key or damaged row). It is a server configuration
// problem, never a delivery outcome.
var errWebhookSealed = errors.New("webhook secret cannot be opened with the configured master key")

// ErrWebhookSealed reports errWebhookSealed to adapters.
func ErrWebhookSealed(err error) bool { return errors.Is(err, errWebhookSealed) }
