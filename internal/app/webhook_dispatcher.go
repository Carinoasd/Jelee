package app

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// WebhookDispatcherOptions configure the background deliverer (G12.3).
type WebhookDispatcherOptions struct {
	Clock Clock
	// Jitter draws the retry jitter factor; math/rand by default.
	Jitter domain.WebhookJitter
	// Batch bounds the events fanned out and deliveries claimed per round.
	Batch int
	// Concurrency bounds deliveries in flight.
	Concurrency int
	// Lease reserves a claimed delivery. It must outlive the longest attempt
	// plus its recording; an expired lease makes the delivery claimable
	// again, which re-sends it with the same eventId.
	Lease time.Duration
	// PollInterval is the idle wait between rounds.
	PollInterval time.Duration
	// Retention deletes settled events older than this.
	Retention time.Duration
	// StopGrace is how long in-flight attempts may finish after Run's
	// context is cancelled before they are cancelled too.
	StopGrace time.Duration
	Logger    *slog.Logger
	// Spans starts the span of one delivery attempt for an event and
	// returns its end function (G46.6). Nil starts none.
	Spans func(ctx context.Context, eventID string) (context.Context, func(outcome string))
}

// WebhookDispatcher fans outbox events out to subscribed endpoints and
// delivers them with retries, backoff and dead-lettering.
type WebhookDispatcher struct {
	endpoints WebhookRepository
	store     WebhookDeliveryStore
	box       WebhookSecretBox
	deliverer WebhookDeliverer
	opts      WebhookDispatcherOptions

	mu         sync.Mutex
	lastPurge  time.Time
	wake       chan struct{}
	sealedWarn time.Time
}

const (
	webhookPurgeEvery = time.Hour
	webhookPurgeBatch = 1000
	// webhookRecordTimeout bounds the RecordAttempt that follows a delivery;
	// it runs even when the dispatcher is stopping so finished work counts.
	webhookRecordTimeout = 5 * time.Second
)

func NewWebhookDispatcher(endpoints WebhookRepository, store WebhookDeliveryStore, box WebhookSecretBox, deliverer WebhookDeliverer, opts WebhookDispatcherOptions) (*WebhookDispatcher, error) {
	if endpoints == nil || store == nil || box == nil || deliverer == nil {
		return nil, errors.New("webhook dispatcher dependencies must be provided")
	}
	if opts.Clock == nil {
		opts.Clock = systemClock{}
	}
	if opts.Jitter == nil {
		opts.Jitter = rand.Float64
	}
	if opts.Batch == 0 {
		opts.Batch = 50
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = 4
	}
	if opts.Lease == 0 {
		opts.Lease = 2 * time.Minute
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Second
	}
	if opts.Retention == 0 {
		opts.Retention = 14 * 24 * time.Hour
	}
	if opts.StopGrace == 0 {
		opts.StopGrace = 5 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Batch < 1 || opts.Batch > 500 || opts.Concurrency < 1 || opts.Concurrency > 32 ||
		opts.Lease < MaxWebhookTimeout+2*webhookRecordTimeout || opts.PollInterval < 10*time.Millisecond || opts.Retention < time.Hour || opts.StopGrace < 0 {
		return nil, domain.ErrInvalid
	}
	return &WebhookDispatcher{endpoints: endpoints, store: store, box: box, deliverer: deliverer, opts: opts, wake: make(chan struct{}, 1)}, nil
}

// WebhookRound counts what one round did.
type WebhookRound struct {
	Planned   int
	Claimed   int
	Delivered int
	Retrying  int
	Dead      int
	// Skipped attempts were not recorded (stopping, stale lease, sealed
	// secret unreadable); their lease expires and they are sent again.
	Skipped int
	Purged  int
}

// Wake asks a running dispatcher to start a round now.
func (d *WebhookDispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// RunOnce plans new events, delivers the due deliveries and, at most once an
// hour, purges settled history. Attempts run with ctx: cancelling it
// cancels in-flight requests, whose deliveries are then re-sent after their
// lease expires.
func (d *WebhookDispatcher) RunOnce(ctx context.Context) (WebhookRound, error) {
	return d.round(ctx, ctx)
}

func (d *WebhookDispatcher) round(ctx, attemptCtx context.Context) (WebhookRound, error) {
	var round WebhookRound
	planned, err := d.plan(ctx)
	round.Planned = planned
	if err != nil {
		return round, err
	}
	now := d.opts.Clock.Now()
	claimed, err := d.store.ClaimDue(ctx, now, now.Add(d.opts.Lease), d.opts.Batch)
	if err != nil {
		return round, err
	}
	round.Claimed = len(claimed)
	if len(claimed) > 0 {
		d.deliverAll(attemptCtx, claimed, &round)
	}
	d.mu.Lock()
	due := d.lastPurge.IsZero() || now.Sub(d.lastPurge) >= webhookPurgeEvery
	if due {
		d.lastPurge = now
	}
	d.mu.Unlock()
	if due {
		n, err := d.store.PurgeWebhookHistory(ctx, now.Add(-d.opts.Retention), webhookPurgeBatch)
		round.Purged = n
		if err != nil {
			return round, err
		}
		if n == webhookPurgeBatch {
			// More to purge: try again next round instead of in an hour.
			d.mu.Lock()
			d.lastPurge = time.Time{}
			d.mu.Unlock()
		}
	}
	return round, nil
}

func (d *WebhookDispatcher) plan(ctx context.Context) (int, error) {
	events, err := d.store.FetchUnplanned(ctx, d.opts.Batch)
	if err != nil || len(events) == 0 {
		return 0, err
	}
	endpoints, err := d.endpoints.ListDeliveryEndpoints(ctx)
	if err != nil {
		return 0, err
	}
	due := d.opts.Clock.Now()
	for i, event := range events {
		if err := d.store.CreateDeliveries(ctx, event, PlanWebhookFanout(event, endpoints), due); err != nil {
			return i, err
		}
	}
	return len(events), nil
}

type webhookEndpointCache struct {
	mu      sync.Mutex
	entries map[string]*webhookEndpointEntry
}

type webhookEndpointEntry struct {
	once     sync.Once
	endpoint WebhookEndpoint
	keys     domain.WebhookSigningKeys
	err      error
}

func (d *WebhookDispatcher) loadEndpoint(ctx context.Context, cache *webhookEndpointCache, id string) (WebhookEndpoint, domain.WebhookSigningKeys, error) {
	cache.mu.Lock()
	entry := cache.entries[id]
	if entry == nil {
		entry = &webhookEndpointEntry{}
		cache.entries[id] = entry
	}
	cache.mu.Unlock()
	entry.once.Do(func() {
		var sealed WebhookSealedEndpoint
		sealed, entry.err = d.endpoints.LoadWebhookEndpoint(ctx, id)
		if entry.err == nil {
			entry.endpoint, entry.keys, entry.err = OpenWebhookEndpoint(d.box, sealed)
		}
	})
	return entry.endpoint, entry.keys, entry.err
}

func (d *WebhookDispatcher) deliverAll(ctx context.Context, claimed []WebhookDelivery, round *WebhookRound) {
	cache := &webhookEndpointCache{entries: map[string]*webhookEndpointEntry{}}
	slots := make(chan struct{}, d.opts.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, delivery := range claimed {
		slots <- struct{}{}
		wg.Add(1)
		go func(delivery WebhookDelivery) {
			defer func() { <-slots; wg.Done() }()
			state, ok := d.deliverOne(ctx, cache, delivery)
			mu.Lock()
			defer mu.Unlock()
			if !ok {
				round.Skipped++
				return
			}
			switch state {
			case domain.WebhookDeliveryDelivered:
				round.Delivered++
			case domain.WebhookDeliveryPending:
				round.Retrying++
			default:
				round.Dead++
			}
		}(delivery)
	}
	wg.Wait()
}

// deliverOne sends one claimed delivery and records the attempt. It reports
// false when nothing was recorded.
func (d *WebhookDispatcher) deliverOne(ctx context.Context, cache *webhookEndpointCache, delivery WebhookDelivery) (domain.WebhookDeliveryState, bool) {
	outcome := "skipped"
	if d.opts.Spans != nil {
		var end func(string)
		ctx, end = d.opts.Spans(ctx, delivery.Event.EventID)
		defer func() { end(outcome) }()
	}
	state, recorded := d.attempt(ctx, cache, delivery)
	if recorded {
		outcome = string(state)
	}
	return state, recorded
}

func (d *WebhookDispatcher) attempt(ctx context.Context, cache *webhookEndpointCache, delivery WebhookDelivery) (domain.WebhookDeliveryState, bool) {
	endpoint, keys, err := d.loadEndpoint(ctx, cache, delivery.EndpointID)
	if errors.Is(err, domain.ErrNotFound) {
		// Deleted while claimed: its deliveries went with it.
		return "", false
	}
	if err != nil {
		if ErrWebhookSealed(err) {
			d.warnSealed()
		} else {
			d.opts.Logger.WarnContext(ctx, "webhook endpoint unavailable", "component", "webhook", "webhookId", delivery.EndpointID)
		}
		return "", false
	}
	policy := endpoint.Retry
	if policy.Validate() != nil {
		policy = domain.DefaultWebhookRetryPolicy()
	}
	started := d.opts.Clock.Now()
	var outcome domain.WebhookOutcome
	request, err := BuildWebhookRequest(endpoint, keys, delivery.Event, started)
	if err != nil {
		// The stored event or endpoint no longer passes validation (for
		// example an event written by an older build that is no longer
		// redacted): it cannot heal by retrying.
		outcome = domain.WebhookOutcome{Kind: domain.WebhookOutcomeInvalid}
	} else {
		outcome, err = d.deliverer.Deliver(ctx, request)
		if err != nil {
			// Cancelled: the lease expires and the delivery is sent again.
			return "", false
		}
		if outcome.Kind == domain.WebhookOutcomeBlocked {
			// G46.3: the outbound guard refused the resolved target or a
			// redirect (SSRF protection); the URL is never logged.
			domain.ForceTraceSampling(ctx)
			d.opts.Logger.WarnContext(ctx, "outbound request blocked", "component", "security", "event", "ssrf_blocked", "webhookId", delivery.EndpointID, "deliveryId", delivery.ID)
		}
	}
	finished := d.opts.Clock.Now()
	record := ResolveWebhookAttempt(delivery, policy, outcome, started, finished, d.opts.Jitter)
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), webhookRecordTimeout)
	defer cancel()
	if err := d.store.RecordAttempt(recordCtx, record); err != nil {
		if !errors.Is(err, domain.ErrConflict) {
			d.opts.Logger.WarnContext(ctx, "webhook attempt not recorded", "component", "webhook", "deliveryId", delivery.ID)
		}
		return "", false
	}
	if record.Decision.State == domain.WebhookDeliveryDead {
		d.opts.Logger.InfoContext(ctx, "webhook delivery dead-lettered", "component", "webhook", "webhookId", delivery.EndpointID, "deliveryId", delivery.ID, "eventId", delivery.Event.EventID, "outcome", string(outcome.Kind), "status", outcome.StatusCode)
	}
	return record.Decision.State, true
}

func (d *WebhookDispatcher) warnSealed() {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.opts.Clock.Now()
	if !d.sealedWarn.IsZero() && now.Sub(d.sealedWarn) < time.Minute {
		return
	}
	d.sealedWarn = now
	d.opts.Logger.Error("webhook secrets cannot be opened; check JELEE_WEBHOOK_MASTER_KEY", "component", "webhooks")
}

// Run delivers until ctx is cancelled. After cancellation no new round
// starts; attempts in flight get StopGrace to finish and record before they
// are cancelled. Run returns when they have.
func (d *WebhookDispatcher) Run(ctx context.Context) {
	attemptCtx, cancelAttempts := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelAttempts()
	stop := context.AfterFunc(ctx, func() {
		timer := time.NewTimer(d.opts.StopGrace)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancelAttempts()
		case <-attemptCtx.Done():
		}
	})
	defer stop()
	for ctx.Err() == nil {
		round, err := d.round(ctx, attemptCtx)
		if err != nil && ctx.Err() == nil {
			d.opts.Logger.Warn("webhook delivery round failed", "component", "webhooks")
		}
		// A full batch suggests more work: continue at once.
		if err == nil && (round.Planned == d.opts.Batch || round.Claimed == d.opts.Batch) {
			continue
		}
		timer := time.NewTimer(d.opts.PollInterval)
		select {
		case <-ctx.Done():
		case <-d.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}
