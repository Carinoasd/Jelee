package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// G12.1 events no request or job transaction carries: system.alert, raised
// from the warnings and errors the process logs with a known code, and
// session.ended for sessions that expired, which changes no row.

// SystemAlert is one operational condition worth an alert. Code is a fixed
// machine code from the log record; it never carries a path, address or
// message text.
type SystemAlert struct {
	Code      string
	Component string
	// Severity is "warning" or "error".
	Severity string
}

// systemAlertCodes are the log codes that raise system.alert: an optional
// tool or isolated runtime that is unavailable, and background maintenance
// that keeps failing. systemAlertPrefixes cover code families whose suffix
// is a fixed reason.
var (
	systemAlertCodes = map[string]bool{
		"probe_runtime_unavailable":       true,
		"probe_maintenance_failed":        true,
		"nfo_maintenance_failed":          true,
		"schedule_dispatch_failed":        true,
		"subtitle_utf8_cache_unavailable": true,
		"two_factor_key_invalid":          true,
		"log_prune_failed":                true,
		"watch_unavailable":               true,
	}
	systemAlertPrefixes = []string{"matroska_", "subtitle_ocr_", "embedded_cover_", "chapter_image_"}
)

// SystemAlertCode reports whether a log code raises system.alert.
func SystemAlertCode(code string) bool {
	if len(code) == 0 || len(code) > 64 {
		return false
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	if systemAlertCodes[code] {
		return true
	}
	for _, prefix := range systemAlertPrefixes {
		if strings.HasPrefix(code, prefix) && len(code) > len(prefix) {
			return true
		}
	}
	return false
}

const (
	// DefaultSystemAlertInterval throttles system.alert per code.
	DefaultSystemAlertInterval = time.Hour
	// maxPendingAlerts bounds the alerts held until the outbox is reachable.
	maxPendingAlerts = 64
)

// SystemAlerts collects alerts from any goroutine without blocking: each
// code is accepted at most once per interval, and at most maxPendingAlerts
// wait for WebhookProducers to append them. Alerts raised while webhooks are
// off are held (bounded) and dropped with the process.
type SystemAlerts struct {
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	last    map[string]time.Time
	pending []pendingAlert
	wake    chan struct{}
}

type pendingAlert struct {
	alert SystemAlert
	at    time.Time
}

// NewSystemAlerts builds the collector; interval zero takes the default.
func NewSystemAlerts(interval time.Duration, now func() time.Time) *SystemAlerts {
	if interval <= 0 {
		interval = DefaultSystemAlertInterval
	}
	if now == nil {
		now = time.Now
	}
	return &SystemAlerts{interval: interval, now: now, last: map[string]time.Time{}, wake: make(chan struct{}, 1)}
}

// Raise accepts an alert unless its code is unknown, was accepted within the
// interval, or the queue is full. It never blocks.
func (a *SystemAlerts) Raise(alert SystemAlert) bool {
	if a == nil || !SystemAlertCode(alert.Code) || alert.Severity != "warning" && alert.Severity != "error" {
		return false
	}
	if !validAlertComponent(alert.Component) {
		alert.Component = ""
	}
	now := a.now()
	a.mu.Lock()
	if last, ok := a.last[alert.Code]; ok && now.Sub(last) < a.interval || len(a.pending) >= maxPendingAlerts {
		a.mu.Unlock()
		return false
	}
	a.last[alert.Code] = now
	a.pending = append(a.pending, pendingAlert{alert: alert, at: now})
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
	return true
}

// validAlertComponent accepts the fixed component labels of log records.
func validAlertComponent(component string) bool {
	if len(component) == 0 || len(component) > 32 {
		return false
	}
	for _, r := range component {
		if (r < 'a' || r > 'z') && r != '_' {
			return false
		}
	}
	return true
}

func (a *SystemAlerts) take() []pendingAlert {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.pending
	a.pending = nil
	return out
}

// putBack returns alerts an append failed for, ahead of newer ones, within
// the queue bound.
func (a *SystemAlerts) putBack(alerts []pendingAlert) {
	a.mu.Lock()
	defer a.mu.Unlock()
	merged := append(alerts, a.pending...)
	if len(merged) > maxPendingAlerts {
		merged = merged[:maxPendingAlerts]
	}
	a.pending = merged
}

// SessionExpiryRepository raises session.ended for expired sessions.
type SessionExpiryRepository interface {
	// RaiseExpiredSessions raises session.ended for at most limit sessions
	// that expired since its previous call and returns how many it raised.
	RaiseExpiredSessions(ctx context.Context, limit int) (int, error)
}

// WebhookProducerOptions configure WebhookProducers.
type WebhookProducerOptions struct {
	// ExpiryInterval spaces the expiry sweeps; zero takes one minute.
	ExpiryInterval time.Duration
	// ExpiryBatch bounds one sweep statement; zero takes 500.
	ExpiryBatch int
	Random      func([]byte) error
	Timeout     time.Duration
}

// WebhookProducers appends collected system alerts and sweeps session
// expiry while webhooks are enabled.
type WebhookProducers struct {
	outbox WebhookOutbox
	alerts *SystemAlerts
	expiry SessionExpiryRepository
	opts   WebhookProducerOptions
}

// NewWebhookProducers checks the options and fills in the defaults.
func NewWebhookProducers(outbox WebhookOutbox, alerts *SystemAlerts, expiry SessionExpiryRepository, opts WebhookProducerOptions) (*WebhookProducers, error) {
	if outbox == nil || alerts == nil || expiry == nil || opts.ExpiryInterval < 0 || opts.ExpiryBatch < 0 || opts.ExpiryBatch > 10000 || opts.Timeout < 0 {
		return nil, domain.ErrInvalid
	}
	if opts.ExpiryInterval == 0 {
		opts.ExpiryInterval = time.Minute
	}
	if opts.ExpiryBatch == 0 {
		opts.ExpiryBatch = 500
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.Random == nil {
		opts.Random = func(b []byte) error { _, err := rand.Read(b); return err }
	}
	return &WebhookProducers{outbox: outbox, alerts: alerts, expiry: expiry, opts: opts}, nil
}

// FlushAlerts appends every pending alert as system.alert. An alert whose
// append failed stays queued for the next call.
func (p *WebhookProducers) FlushAlerts(ctx context.Context) error {
	pending := p.alerts.take()
	for i, a := range pending {
		var raw [16]byte
		if err := p.opts.Random(raw[:]); err != nil {
			p.alerts.putBack(pending[i:])
			return domain.ErrDatabase
		}
		data := map[string]any{"code": a.alert.Code, "severity": a.alert.Severity}
		if a.alert.Component != "" {
			data["component"] = a.alert.Component
		}
		event, err := domain.NewWebhookEvent(hex.EncodeToString(raw[:]), domain.WebhookSystemAlert, a.at, domain.WebhookSubject{Kind: domain.WebhookSubjectSystem}, data)
		if err != nil {
			// Not reachable for an accepted alert; never retried.
			continue
		}
		c, cancel := context.WithTimeout(ctx, p.opts.Timeout)
		err = p.outbox.Append(c, event)
		cancel()
		if err != nil {
			p.alerts.putBack(pending[i:])
			return err
		}
	}
	return nil
}

// SweepExpiry raises session.ended for sessions that expired, one bounded
// statement at a time until a short page.
func (p *WebhookProducers) SweepExpiry(ctx context.Context) error {
	for {
		c, cancel := context.WithTimeout(ctx, p.opts.Timeout)
		n, err := p.expiry.RaiseExpiredSessions(c, p.opts.ExpiryBatch)
		cancel()
		if err != nil || n < p.opts.ExpiryBatch || ctx.Err() != nil {
			return err
		}
	}
}

// Run appends alerts as they arrive and sweeps expiry every ExpiryInterval
// until ctx ends. Failures are retried on the next round.
func (p *WebhookProducers) Run(ctx context.Context) {
	ticker := time.NewTicker(p.opts.ExpiryInterval)
	defer ticker.Stop()
	_ = p.FlushAlerts(ctx)
	_ = p.SweepExpiry(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.alerts.wake:
			_ = p.FlushAlerts(ctx)
		case <-ticker.C:
			_ = p.FlushAlerts(ctx)
			_ = p.SweepExpiry(ctx)
		}
	}
}
