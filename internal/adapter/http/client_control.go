package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/maphash"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/compat"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
)

// Client control request gate (G47). Rules are compiled once per policy
// version into an immutable access.Snapshot and evaluated in memory; the
// version rides on the session lookup every authenticated request already
// makes, so a change takes effect on the next request on every instance
// without an extra query. Hits and client activity are aggregated in memory
// and written in the background. docs/client-control.md describes the model.

// ClientControlStore is the storage the gate reads and writes without an
// administrator actor.
type ClientControlStore interface {
	ClientControlState(context.Context) (domain.ClientControlState, error)
	ClientControlVersion(context.Context) (int64, error)
	RecordClientActivity(context.Context, []domain.ClientActivity) error
	RecordClientHits(context.Context, []domain.ClientHit, []domain.ClientRuleCount) error
	RevokeSessionByClientRule(ctx context.Context, sessionID, ip string, rules []string) error
}

// clientAuthenticator is a backend whose session lookup also returns the
// client labels recorded with the session and the client control version.
type clientAuthenticator interface {
	AuthenticateClient(ctx context.Context, token, ip string) (access.Principal, access.SessionClient, int64, error)
}

// ClientControlOptions tune the gate; zero values take the defaults.
type ClientControlOptions struct {
	// FlushInterval is how often buffered hits and activity are written.
	FlushInterval time.Duration
	// ActivityInterval throttles known-client updates per client and session.
	ActivityInterval time.Duration
	// BlockAlert is the number of blocked requests per minute that logs a
	// client_control_block_burst warning (G47.8).
	BlockAlert int64
	Now        func() time.Time
}

// ClientControl is the request gate. It is safe for concurrent use; Close
// stops its writer and flushes what is buffered.
type ClientControl struct {
	store   ClientControlStore
	service *app.ClientControl
	logger  *slog.Logger
	opts    ClientControlOptions

	cur      atomic.Pointer[clientGateState]
	reloadMu sync.Mutex
	failedAt time.Time

	limitMu  sync.Mutex
	limiters map[access.RateLimit]*LoginLimiter

	seed       maphash.Seed
	activityMu sync.Mutex
	recent     map[uint64]time.Time

	// dev answers developer mode relaxations (G45.4); nil in production.
	dev *devmode.Controller

	rec       clientRecorder
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// clientGateState is one compiled policy version.
type clientGateState struct {
	version int64
	snap    *access.Snapshot
	// unknown is the stored unknown-client policy, so the gate knows when
	// it must compute a client's key to look up its trust.
	unknown access.UnknownClientPolicy
	trusted map[string]struct{}
	// reloginSince maps a force_relogin rule to its last change: sessions
	// issued before it must log in again, later ones are not affected.
	reloginSince map[string]time.Time
	empty        bool
}

const (
	clientActivityMax  = 65536
	clientHitBucketMax = 4096
	clientStoreTimeout = 5 * time.Second
	// clientReloadBackoff spaces reload attempts after a failure; requests
	// meanwhile use the last compiled version.
	clientReloadBackoff = time.Second
)

// NewClientControl loads and compiles the stored rules and starts the
// background writer.
func NewClientControl(ctx context.Context, store ClientControlStore, service *app.ClientControl, logger *slog.Logger, opts ClientControlOptions) (*ClientControl, error) {
	if store == nil || service == nil || logger == nil {
		return nil, errors.New("client control store, service and logger must be provided")
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = 2 * time.Second
	}
	if opts.ActivityInterval <= 0 {
		opts.ActivityInterval = time.Minute
	}
	if opts.BlockAlert <= 0 {
		opts.BlockAlert = 100
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	c := &ClientControl{store: store, service: service, logger: logger, opts: opts, limiters: map[access.RateLimit]*LoginLimiter{},
		seed: maphash.MakeSeed(), recent: map[uint64]time.Time{}, stop: make(chan struct{}), done: make(chan struct{})}
	c.rec.init()
	state, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	c.cur.Store(state)
	go c.run() //nolint:gosec,contextcheck // G118: background refresher lives until Close, not for one request
	return c, nil
}

// WithClientControl enables the client control gate and its administrator API.
func WithClientControl(c *ClientControl) Option { return func(s *Server) { s.clients = c } }

// Close stops the background writer and writes what is still buffered.
// relaxed reports whether the developer mode toggle t suspends a check.
func (c *ClientControl) relaxed(t devmode.Toggle) bool { return c.dev != nil && c.dev.Effective(t) }

func (c *ClientControl) Close(ctx context.Context) error {
	c.closeOnce.Do(func() { close(c.stop) })
	select {
	case <-c.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return c.Flush(ctx)
}

func (c *ClientControl) run() {
	defer close(c.done)
	ticker := time.NewTicker(c.opts.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), clientStoreTimeout)
			if err := c.Flush(ctx); err != nil {
				c.logger.Warn("client control records not written", "component", "client_control", "code", "client_control_record_failed")
			}
			cancel()
		}
	}
}

// accessRule maps a stored rule onto the engine's rule.
func accessRule(id string, in domain.ClientRuleInput) access.Rule {
	r := access.Rule{ID: id, Dimension: access.Dimension(in.Dimension), Header: in.Header, Match: access.MatchKind(in.Match), Pattern: in.Pattern, CaseFold: in.CaseFold,
		Priority: in.Priority, Action: access.Action(in.Action), Intent: access.Action(in.Intent), Libraries: in.Libraries, Scope: access.Scope{Kind: access.ScopeKind(in.ScopeKind), Values: in.ScopeValues}, Enabled: in.Enabled, Note: in.Note}
	if in.RateLimit != nil {
		r.RateLimit = &access.RateLimit{Requests: in.RateLimit.Requests, Per: time.Duration(in.RateLimit.PeriodSeconds) * time.Second}
	}
	if w := in.Window; w != nil {
		aw := &access.Window{DailyStart: w.DailyStart, DailyEnd: w.DailyEnd, TimeZone: w.TimeZone}
		if w.From != nil {
			aw.From = *w.From
		}
		if w.Until != nil {
			aw.Until = *w.Until
		}
		for _, d := range w.Weekdays {
			aw.Weekdays = append(aw.Weekdays, time.Weekday(d))
		}
		r.Window = aw
	}
	return r
}

// ValidateClientRule compiles one rule with the request engine; it is the
// application's rule validator.
func ValidateClientRule(in domain.ClientRuleInput) error {
	_, err := access.Compile([]access.Rule{accessRule("validate", in)}, access.DefaultOptions())
	return err
}

func (c *ClientControl) load(ctx context.Context) (*clientGateState, error) {
	ctx, cancel := context.WithTimeout(ctx, clientStoreTimeout)
	defer cancel()
	st, err := c.store.ClientControlState(ctx)
	if err != nil {
		return nil, err
	}
	opts := access.DefaultOptions()
	opts.ExemptAdmins, opts.ExemptLoopback = st.Policy.ExemptAdmins, st.Policy.ExemptLoopback
	opts.UnknownClients = access.UnknownClientPolicy(st.Policy.UnknownClients)
	rules := make([]access.Rule, 0, len(st.Rules))
	relogin := map[string]time.Time{}
	for _, r := range st.Rules {
		rules = append(rules, accessRule(r.ID, r.ClientRuleInput))
		if r.Action == string(access.ActionForceRelogin) {
			relogin[r.ID] = r.UpdatedAt
		}
	}
	snap, err := access.Compile(rules, opts)
	if err != nil {
		// Storage only holds rules that compiled one by one; a set that
		// fails anyway (a changed limit) keeps every rule that compiles on
		// its own, and the dropped ones are logged.
		kept := rules[:0]
		for _, r := range rules {
			if _, one := access.Compile([]access.Rule{r}, opts); one == nil {
				kept = append(kept, r)
			} else {
				c.logger.Error("client rule cannot be compiled and is ignored", "component", "client_control", "rule", r.ID)
			}
		}
		if snap, err = access.Compile(kept, opts); err != nil {
			return nil, err
		}
	}
	trusted := make(map[string]struct{}, len(st.TrustedKeys))
	for _, k := range st.TrustedKeys {
		trusted[k] = struct{}{}
	}
	return &clientGateState{version: st.Policy.Version, snap: snap, unknown: opts.UnknownClients, trusted: trusted, reloginSince: relogin,
		empty: snap.Len() == 0 && opts.UnknownClients == access.UnknownAllow}, nil
}

// state returns the compiled state for at least version. A newer version
// is compiled once; concurrent requests wait for it, so no request after a
// committed change is evaluated against an older version. When loading
// fails the last version stays in force and the failure is logged.
func (c *ClientControl) state(ctx context.Context, version int64) *clientGateState {
	st := c.cur.Load()
	if version <= st.version {
		return st
	}
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()
	if st = c.cur.Load(); version <= st.version {
		return st
	}
	now := c.opts.Now()
	if !c.failedAt.IsZero() && now.Sub(c.failedAt) < clientReloadBackoff {
		return st
	}
	next, err := c.load(ctx)
	if err != nil {
		c.failedAt = now
		c.logger.Error("client rules not reloaded; previous version stays in force", "component", "client_control", "code", "client_control_reload_failed", "version", st.version)
		return st
	}
	c.failedAt = time.Time{}
	c.cur.Store(next)
	return next
}

type clientRequestKey struct{}

// clientRequest is what the gate reads from a request; the boundary stores
// it in the context so the compatibility layer's session lookup, which sees
// only a context, can be gated too.
type clientRequest struct {
	method  string
	path    string
	header  http.Header
	ip      netip.Addr
	proxied bool
	compat  bool
}

func newClientRequest(r *http.Request) *clientRequest {
	req := &clientRequest{method: r.Method, path: r.URL.Path, header: r.Header, compat: compat.HasPrefix(r.URL.Path)}
	if addr, err := netip.ParseAddr(requestClientIP(r)); err == nil {
		req.ip = addr.Unmap().WithZone("")
	}
	// Forwarding headers matter only for the loopback exemption.
	req.proxied = req.ip.IsLoopback() && forwardingHeadersPresent(r)
	return req
}

func clientRequestFrom(ctx context.Context) *clientRequest {
	req, _ := ctx.Value(clientRequestKey{}).(*clientRequest)
	return req
}

func (req *clientRequest) surface() string {
	if req.compat {
		return "compat"
	}
	return "native"
}

// clientLabels are the client-supplied identity of one request. Labels a
// session recorded at login win over the request's own headers: they are
// fixed for the session's lifetime, so a client cannot shed a rule by
// changing a header on the next request.
type clientLabels struct {
	app, version, deviceID, deviceName string
}

func (req *clientRequest) labels(session access.SessionClient) clientLabels {
	var l clientLabels
	if req.compat {
		// Unparseable headers carry no labels; the layer itself rejects them.
		if auth, err := compat.ParseClientAuth(req.header, nil); err == nil {
			l = clientLabels{app: auth.Client, version: auth.Version, deviceID: auth.DeviceID, deviceName: auth.Device}
		}
	}
	pick := func(recorded, reported string) string {
		if recorded != "" {
			return recorded
		}
		return reported
	}
	return clientLabels{app: pick(session.Name, l.app), version: pick(session.Version, l.version), deviceID: pick(session.DeviceID, l.deviceID), deviceName: pick(session.DeviceName, l.deviceName)}
}

// clientKey identifies a known client: its application and device ID, or
// its user agent when it reports no device ID. It is a digest, so storage
// and logs never need the raw values as a key.
func clientKey(l clientLabels, userAgent string) string {
	h := sha256.New()
	if l.deviceID != "" {
		h.Write([]byte("jelee-client-v1\x00d\x00" + l.app + "\x00" + l.deviceID))
	} else {
		h.Write([]byte("jelee-client-v1\x00u\x00" + userAgent))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// APIKeyFingerprint is the value the api_key_fingerprint dimension matches:
// "sha256:" and the first 16 bytes of the credential's SHA-256 in hex.
func APIKeyFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:16])
}

// clientRetryError carries the wait of a rate limit refusal.
type clientRetryError struct{ retry time.Duration }

func (e clientRetryError) Error() string             { return domain.ErrClientRateLimited.Error() }
func (e clientRetryError) Unwrap() error             { return domain.ErrClientRateLimited }
func (e clientRetryError) RetryAfter() time.Duration { return e.retry }

// gateInput is one gate decision's input beyond the request.
type gateInput struct {
	principal     access.Principal
	session       access.SessionClient
	version       int64
	token         string
	authenticated bool
	// login carries the labels a login body reports; logins skip the
	// unknown-client policy so a new client can appear for approval.
	login *clientLabels
}

// check evaluates one request and returns nil or the refusal: a domain
// client control error, domain.ErrUnauthenticated after a forced relogin, or
// domain.ErrDatabase when a forced revocation could not be stored.
func (c *ClientControl) check(ctx context.Context, req *clientRequest, in gateInput) error {
	_, err := c.decide(ctx, req, in)
	return err
}

// decide is check that also returns the library set a restrict_libraries
// decision left the request: nil when unrestricted (G47, G48.5). The caller
// passes it to the unified storage filter with the request's principal.
func (c *ClientControl) decide(ctx context.Context, req *clientRequest, in gateInput) ([]string, error) {
	st := c.state(ctx, in.version)
	labels := req.labels(in.session)
	if in.login != nil {
		labels = *in.login
	}
	userAgent := req.header.Get("User-Agent")
	now := c.opts.Now()
	key := ""
	if in.authenticated && c.activityDue(in.principal.SessionID, labels, userAgent, now) {
		key = clientKey(labels, userAgent)
		c.rec.activity(domain.ClientActivity{Key: key, AppName: labels.app, AppVersion: labels.version, UserAgent: userAgent, DeviceID: labels.deviceID, DeviceName: labels.deviceName,
			ClientKind: string(in.principal.Kind), UserID: in.principal.UserID, SessionID: in.principal.SessionID, IP: ipString(req.ip), At: now})
	}
	if st.empty {
		return nil, nil
	}
	areq := access.Request{UserAgent: userAgent, AppName: labels.app, AppVersion: labels.version, DeviceID: labels.deviceID, DeviceName: labels.deviceName,
		IP: req.ip, Headers: req.header, Principal: in.principal, Proxied: req.proxied, Time: now, Known: true}
	if st.snap.Uses(access.DimAPIKey) && in.token != "" {
		areq.APIKeyFingerprint = APIKeyFingerprint(in.token)
	}
	if in.login == nil && st.unknown != access.UnknownAllow {
		if key == "" {
			key = clientKey(labels, userAgent)
		}
		_, areq.Known = st.trusted[key]
	}
	d := st.snap.Evaluate(areq)
	c.recordHits(st, req, in.principal, labels, userAgent, d, now)
	if d.Exempt != access.ExemptNone {
		return nil, nil
	}
	switch {
	case (d.Verdict == access.VerdictDeny || d.Verdict == access.VerdictPending) && c.relaxed(devmode.RelaxClientUABlock):
		// Developer mode (G45.4): blocking and approval are suspended; hits
		// are still recorded above, every other action still applies.
	case d.Verdict == access.VerdictDeny:
		// A blocked client is a security event: its trace is never
		// dropped by sampling (G46.6).
		domain.ForceTraceSampling(ctx)
		return nil, domain.ErrClientBlocked
	case d.Verdict == access.VerdictPending:
		return nil, domain.ErrClientPending
	}
	if d.ForceRelogin && in.authenticated {
		var rules []string
		for _, id := range d.Rules {
			if since, ok := st.reloginSince[id]; ok && in.session.IssuedAt.Before(since) {
				rules = append(rules, id)
			}
		}
		if len(rules) > 0 {
			if err := c.store.RevokeSessionByClientRule(ctx, in.principal.SessionID, ipString(req.ip), rules); err != nil {
				return nil, domain.ErrDatabase
			}
			return nil, domain.ErrUnauthenticated
		}
	}
	if d.RateLimit != nil && !c.relaxed(devmode.RelaxAPIRateLimit) {
		if allowed, retry := c.allowRate(*d.RateLimit, req, labels, userAgent, in.principal.UserID); !allowed {
			return nil, clientRetryError{retry: retry}
		}
	}
	if d.ReadOnly && !safeMethod(req.method) && !readOnlyAllowed(req) {
		return nil, domain.ErrClientReadOnly
	}
	// A restrict_libraries decision (possibly an empty set) narrows the
	// request in storage; developer mode does not relax it.
	return d.Libraries, nil
}

func ipString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// activityDue throttles known-client bookkeeping to once per interval per
// session and identity. The map is bounded; when full it starts over, which
// only costs a few early writes.
func (c *ClientControl) activityDue(session string, l clientLabels, userAgent string, now time.Time) bool {
	var h maphash.Hash
	h.SetSeed(c.seed)
	for _, s := range [...]string{session, l.app, l.deviceID, l.version, l.deviceName, userAgent} {
		h.WriteString(s)
		h.WriteByte(0)
	}
	k := h.Sum64()
	c.activityMu.Lock()
	defer c.activityMu.Unlock()
	if last, ok := c.recent[k]; ok && now.Sub(last) < c.opts.ActivityInterval {
		return false
	}
	if len(c.recent) >= clientActivityMax {
		clear(c.recent)
	}
	c.recent[k] = now
	return true
}

// allowRate applies a rate limit per client identity and user through the
// shared fixed-window limiter, one limiter per distinct limit.
func (c *ClientControl) allowRate(rl access.RateLimit, req *clientRequest, l clientLabels, userAgent, userID string) (bool, time.Duration) {
	c.limitMu.Lock()
	limiter := c.limiters[rl]
	if limiter == nil {
		var err error
		limiter, err = NewLoginLimiter(LoginLimiterOptions{Window: rl.Per, IPLimit: math.MaxInt32, UserLimit: rl.Requests, MaxEntries: 100000, Now: c.opts.Now})
		if err != nil {
			c.limitMu.Unlock()
			return false, time.Second
		}
		c.limiters[rl] = limiter
	}
	c.limitMu.Unlock()
	ip := "0.0.0.0"
	if req.ip.IsValid() {
		ip = req.ip.String()
	}
	return limiter.Allow(ip, userID+"\x00"+clientKey(l, userAgent))
}

// readOnlyAllowed lists the unsafe-method routes a read-only client still
// needs: ending or renewing its own session and read-shaped POSTs.
func readOnlyAllowed(req *clientRequest) bool {
	path := strings.TrimSuffix(req.path, "/")
	if req.compat {
		rest := path[len(compat.Prefix):]
		segments := strings.Split(strings.TrimPrefix(rest, "/"), "/")
		is := func(parts ...string) bool {
			if len(parts) != len(segments) {
				return false
			}
			for i, p := range parts {
				if p != "*" && !strings.EqualFold(p, segments[i]) || p == "*" && segments[i] == "" {
					return false
				}
			}
			return true
		}
		return req.method == http.MethodPost && (is("Sessions", "Logout") || is("System", "Ping") || is("Users", "AuthenticateByName") || is("Items", "*", "PlaybackInfo"))
	}
	if req.method != http.MethodPost {
		return false
	}
	switch path {
	case "/api/v1/auth/logout", "/api/v1/auth/rotate", "/api/v1/auth/login", "/api/v1/auth/login/native":
		return true
	}
	return strings.HasPrefix(path, "/api/v1/items/") && strings.HasSuffix(path, "/playback/check") && strings.Count(path, "/") == 6
}

// recordHits buffers the decision's hits. Allow rules only count; every
// other applied, exempted, observed or shadowed rule and the unknown-client
// policy also leave an aggregated record.
func (c *ClientControl) recordHits(st *clientGateState, req *clientRequest, p access.Principal, l clientLabels, userAgent string, d access.Decision, now time.Time) {
	if len(d.Matched)+len(d.Observed)+len(d.Shadowed) == 0 && !d.DefaultApplied {
		return
	}
	applied := map[string]bool{}
	for _, id := range d.Rules {
		applied[id] = true
	}
	base := domain.ClientHit{Bucket: now.UTC().Truncate(time.Minute), Surface: req.surface(), UserID: p.UserID, IP: ipString(req.ip), UserAgent: userAgent, AppName: l.app, Hits: 1}
	add := func(id, mode string, action access.Action, record bool) {
		c.rec.count(id, now)
		if !record {
			return
		}
		h := base
		h.RuleID, h.Mode, h.Action = id, mode, string(action)
		c.rec.hit(h)
	}
	for _, h := range d.Matched {
		switch {
		case h.Action == access.ActionAllow:
			add(h.RuleID, "", h.Action, false)
		case d.Exempt != access.ExemptNone:
			add(h.RuleID, "exempt", h.Action, true)
		default:
			// A rule overridden by a higher allow did not apply.
			add(h.RuleID, "enforced", h.Action, applied[h.RuleID])
		}
	}
	for _, h := range d.Observed {
		add(h.RuleID, "observe", h.Action, true)
	}
	for _, h := range d.Shadowed {
		add(h.RuleID, "shadow", h.Action, true)
	}
	if d.DefaultApplied {
		h := base
		h.Mode = "default"
		switch st.unknown {
		case access.UnknownReadOnly:
			h.Action = string(access.ActionReadOnly)
		case access.UnknownPending:
			h.Action = string(access.VerdictPending)
		default:
			h.Action = string(access.ActionDeny)
		}
		c.rec.hit(h)
	}
}

// admitLogin gates a login before the password is checked. It has no
// session, so rules scoped to users and the unknown-client policy do not
// apply; the version is read with one small query.
func (c *ClientControl) admitLogin(r *http.Request, labels clientLabels) error {
	req := clientRequestFrom(r.Context())
	if req == nil {
		req = newClientRequest(r)
	}
	ctx, cancel := context.WithTimeout(r.Context(), clientStoreTimeout)
	defer cancel()
	version, err := c.store.ClientControlVersion(ctx)
	if err != nil {
		version = 0 // keep the compiled version; the store error surfaces on the login itself
	}
	return c.check(ctx, req, gateInput{version: version, login: &labels})
}

// Flush writes the buffered hits and activity now.
func (c *ClientControl) Flush(ctx context.Context) error {
	hits, counts, activity, blocked := c.rec.drain()
	if dropped := c.rec.takeDropped(); dropped > 0 {
		c.logger.Warn("client control hit records dropped; buffer full", "component", "client_control", "code", "client_control_hits_dropped", "count", dropped)
	}
	var errs []error
	if len(activity) > 0 {
		if err := c.store.RecordClientActivity(ctx, activity); err != nil {
			errs = append(errs, err)
		}
	}
	if len(hits) > 0 || len(counts) > 0 {
		if err := c.store.RecordClientHits(ctx, hits, counts); err != nil {
			errs = append(errs, err)
		}
	}
	c.alert(blocked)
	return errors.Join(errs...)
}

// BlockedTotal is the number of requests enforced rules denied or held for
// approval since the process started (G47.8, G50.6). It reads memory only.
func (c *ClientControl) BlockedTotal() int64 { return c.rec.total.Load() }

// alert writes the security log line for blocked requests and the burst
// warning (G47.8). Only rule IDs and counts are logged, never a user agent,
// address or path (G47.9).
func (c *ClientControl) alert(blocked map[string]int64) {
	if len(blocked) == 0 {
		return
	}
	var total int64
	parts := make([]string, 0, len(blocked))
	for rule, n := range blocked {
		total += n
		if rule == "" {
			rule = "default"
		}
		parts = append(parts, fmt.Sprintf("%s=%d", rule, n))
	}
	c.logger.Warn("client requests blocked", "component", "client_control", "code", "client_blocked", "count", total, "rules", strings.Join(parts, ","))
	if c.rec.burst(total, c.opts.Now(), c.opts.BlockAlert) {
		c.logger.Warn("client block burst", "component", "client_control", "code", "client_control_block_burst", "perMinute", c.opts.BlockAlert)
	}
}

// clientRecorder aggregates hits per minute bucket and buffers activity.
type clientRecorder struct {
	mu       sync.Mutex
	hits     map[domain.ClientHit]int64
	counts   map[string]domain.ClientRuleCount
	acts     []domain.ClientActivity
	blocked  map[string]int64
	dropped  int64
	minute   time.Time
	inMinute int64
	alerted  time.Time
	// total counts blocked requests since start for the metrics endpoint;
	// unlike blocked it is never drained.
	total atomic.Int64
}

func (r *clientRecorder) init() {
	r.hits, r.counts, r.blocked = map[domain.ClientHit]int64{}, map[string]domain.ClientRuleCount{}, map[string]int64{}
}

func (r *clientRecorder) hit(h domain.ClientHit) {
	h.UserAgent = clipUserAgent(h.UserAgent)
	n := h.Hits
	h.Hits = 0
	r.mu.Lock()
	defer r.mu.Unlock()
	if (h.Mode == "enforced" || h.Mode == "default") && (h.Action == string(access.ActionDeny) || h.Action == string(access.VerdictPending)) {
		r.blocked[h.RuleID] += n
		r.total.Add(n)
	}
	if _, ok := r.hits[h]; !ok && len(r.hits) >= clientHitBucketMax {
		r.dropped += n
		return
	}
	r.hits[h] += n
}

func (r *clientRecorder) count(id string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.counts[id]
	c.RuleID, c.Hits = id, c.Hits+1
	if at.After(c.Last) {
		c.Last = at
	}
	r.counts[id] = c
}

func (r *clientRecorder) activity(a domain.ClientActivity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.acts) < clientActivityMax {
		r.acts = append(r.acts, a)
	}
}

func (r *clientRecorder) drain() ([]domain.ClientHit, []domain.ClientRuleCount, []domain.ClientActivity, map[string]int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	hits := make([]domain.ClientHit, 0, len(r.hits))
	for h, n := range r.hits {
		h.Hits = n
		hits = append(hits, h)
	}
	counts := make([]domain.ClientRuleCount, 0, len(r.counts))
	for _, c := range r.counts {
		counts = append(counts, c)
	}
	acts, blocked := r.acts, r.blocked
	r.hits, r.counts, r.acts, r.blocked = map[domain.ClientHit]int64{}, map[string]domain.ClientRuleCount{}, nil, map[string]int64{}
	return hits, counts, acts, blocked
}

func (r *clientRecorder) takeDropped() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.dropped
	r.dropped = 0
	return n
}

// burst reports once per minute that the blocked requests of the current
// minute reached the threshold.
func (r *clientRecorder) burst(n int64, now time.Time, threshold int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	minute := now.Truncate(time.Minute)
	if !minute.Equal(r.minute) {
		r.minute, r.inMinute = minute, 0
	}
	r.inMinute += n
	if r.inMinute >= threshold && !r.alerted.Equal(minute) {
		r.alerted = minute
		return true
	}
	return false
}

// clipUserAgent keeps at most 256 bytes on a rune boundary, the stored form.
func clipUserAgent(s string) string {
	if len(s) <= 256 {
		return s
	}
	cut := 256
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}
