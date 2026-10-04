package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// G12 webhooks. This file holds the transport-free rules: the event
// catalogue and envelope (G12.1), payload redaction and HMAC signing
// (G12.4), the eventId idempotency contract (G12.6) and the retry schedule
// used by the outbox deliverer (G12.3). Delivery itself lives behind ports in
// internal/app and adapters in internal/adapter/events.

// WebhookEventType is the stable wire name of an event. Names are
// "<area>.<action>" and never change once published; incompatible payload
// changes bump WebhookEvent.Version instead.
type WebhookEventType string

// G12.1 event catalogue, in the order the requirement lists them.
const (
	WebhookMediaAdded       WebhookEventType = "media.added"
	WebhookMediaUpdated     WebhookEventType = "media.updated"
	WebhookMediaDeleted     WebhookEventType = "media.deleted"
	WebhookScanStarted      WebhookEventType = "scan.started"
	WebhookScanCompleted    WebhookEventType = "scan.completed"
	WebhookScanFailed       WebhookEventType = "scan.failed"
	WebhookPlaybackStarted  WebhookEventType = "playback.started"
	WebhookPlaybackPaused   WebhookEventType = "playback.paused"
	WebhookPlaybackProgress WebhookEventType = "playback.progress"
	WebhookPlaybackStopped  WebhookEventType = "playback.stopped"
	WebhookUserLogin        WebhookEventType = "user.login"
	WebhookUserLoginFailed  WebhookEventType = "user.login_failed"
	WebhookUserLocked       WebhookEventType = "user.locked"
	WebhookSessionCreated   WebhookEventType = "session.created"
	WebhookSessionEnded     WebhookEventType = "session.ended"
	WebhookNFOWritten       WebhookEventType = "nfo.written"
	WebhookImagesFetched    WebhookEventType = "images.fetched"
	WebhookSystemAlert      WebhookEventType = "system.alert"
)

var webhookEventTypes = [...]WebhookEventType{
	WebhookMediaAdded, WebhookMediaUpdated, WebhookMediaDeleted,
	WebhookScanStarted, WebhookScanCompleted, WebhookScanFailed,
	WebhookPlaybackStarted, WebhookPlaybackPaused, WebhookPlaybackProgress, WebhookPlaybackStopped,
	WebhookUserLogin, WebhookUserLoginFailed, WebhookUserLocked,
	WebhookSessionCreated, WebhookSessionEnded,
	WebhookNFOWritten, WebhookImagesFetched, WebhookSystemAlert,
}

// WebhookEventTypes returns every published event type in catalogue order.
func WebhookEventTypes() []WebhookEventType {
	out := make([]WebhookEventType, len(webhookEventTypes))
	copy(out, webhookEventTypes[:])
	return out
}

func (t WebhookEventType) Valid() bool {
	for _, known := range webhookEventTypes {
		if t == known {
			return true
		}
	}
	return false
}

func ParseWebhookEventType(value string) (WebhookEventType, bool) {
	t := WebhookEventType(value)
	return t, t.Valid()
}

// WebhookEventVersion is the current envelope/payload schema version.
const WebhookEventVersion = 1

// WebhookSubjectKind names what an event is about.
type WebhookSubjectKind string

const (
	WebhookSubjectItem    WebhookSubjectKind = "item"
	WebhookSubjectLibrary WebhookSubjectKind = "library"
	WebhookSubjectUser    WebhookSubjectKind = "user"
	WebhookSubjectSession WebhookSubjectKind = "session"
	WebhookSubjectJob     WebhookSubjectKind = "job"
	WebhookSubjectSystem  WebhookSubjectKind = "system"
)

func (k WebhookSubjectKind) Valid() bool {
	switch k {
	case WebhookSubjectItem, WebhookSubjectLibrary, WebhookSubjectUser, WebhookSubjectSession, WebhookSubjectJob, WebhookSubjectSystem:
		return true
	}
	return false
}

// webhookSubjectKinds pins which subject an event type must carry, so a
// consumer can rely on subject.kind without inspecting data.
var webhookSubjectKinds = map[WebhookEventType]WebhookSubjectKind{
	WebhookMediaAdded: WebhookSubjectItem, WebhookMediaUpdated: WebhookSubjectItem, WebhookMediaDeleted: WebhookSubjectItem,
	WebhookScanStarted: WebhookSubjectLibrary, WebhookScanCompleted: WebhookSubjectLibrary, WebhookScanFailed: WebhookSubjectLibrary,
	WebhookPlaybackStarted: WebhookSubjectSession, WebhookPlaybackPaused: WebhookSubjectSession,
	WebhookPlaybackProgress: WebhookSubjectSession, WebhookPlaybackStopped: WebhookSubjectSession,
	WebhookUserLogin: WebhookSubjectUser, WebhookUserLoginFailed: WebhookSubjectUser, WebhookUserLocked: WebhookSubjectUser,
	WebhookSessionCreated: WebhookSubjectSession, WebhookSessionEnded: WebhookSubjectSession,
	WebhookNFOWritten: WebhookSubjectItem, WebhookImagesFetched: WebhookSubjectItem,
	WebhookSystemAlert: WebhookSubjectSystem,
}

// WebhookSubjectKindFor returns the subject kind required for t.
func WebhookSubjectKindFor(t WebhookEventType) (WebhookSubjectKind, bool) {
	k, ok := webhookSubjectKinds[t]
	return k, ok
}

// WebhookSubject identifies the entity an event is about. ID is an opaque
// Jelee identifier (empty only for system subjects), never a path or name.
type WebhookSubject struct {
	Kind WebhookSubjectKind `json:"kind"`
	ID   string             `json:"id,omitempty"`
}

// WebhookEvent is the envelope every delivery carries (G12.1). EventID is
// stable across retries and manual replays of the same event, so consumers
// deduplicate on it (G12.6); no global ordering is promised, consumers that
// care order by OccurredAt and tolerate ties and reordering.
type WebhookEvent struct {
	EventID    string           `json:"eventId"`
	Type       WebhookEventType `json:"type"`
	Version    int              `json:"version"`
	OccurredAt time.Time        `json:"occurredAt"`
	Subject    WebhookSubject   `json:"subject"`
	Data       map[string]any   `json:"data,omitempty"`
}

const (
	MaxWebhookEventIDLength = 64
	MaxWebhookSubjectID     = 128
	maxWebhookDataDepth     = 4
	maxWebhookDataKeys      = 64
	maxWebhookDataString    = 2048
	maxWebhookDataList      = 256
)

// ValidWebhookEventID accepts 1..64 characters of [A-Za-z0-9_-]. Generators
// typically emit UUIDs or ULIDs.
func ValidWebhookEventID(id string) bool {
	if id == "" || len(id) > MaxWebhookEventIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// NewWebhookEvent builds a validated envelope. occurredAt is normalized to
// UTC with millisecond precision and data is redacted with
// RedactWebhookData; the caller's map is not modified.
func NewWebhookEvent(eventID string, eventType WebhookEventType, occurredAt time.Time, subject WebhookSubject, data map[string]any) (WebhookEvent, error) {
	redacted, err := RedactWebhookData(data)
	if err != nil {
		return WebhookEvent{}, err
	}
	event := WebhookEvent{
		EventID:    eventID,
		Type:       eventType,
		Version:    WebhookEventVersion,
		OccurredAt: occurredAt.UTC().Truncate(time.Millisecond),
		Subject:    subject,
		Data:       redacted,
	}
	if err := event.Validate(); err != nil {
		return WebhookEvent{}, err
	}
	return event, nil
}

// Validate checks the envelope and that Data is already redacted. It is
// re-run before every delivery so rows written by an older build cannot
// leak data the current rules forbid.
func (e WebhookEvent) Validate() error {
	if !ValidWebhookEventID(e.EventID) || e.Version < 1 || e.Version > WebhookEventVersion || e.OccurredAt.IsZero() {
		return ErrInvalid
	}
	want, ok := WebhookSubjectKindFor(e.Type)
	if !ok || e.Subject.Kind != want {
		return ErrInvalid
	}
	if len(e.Subject.ID) > MaxWebhookSubjectID || !utf8.ValidString(e.Subject.ID) ||
		(e.Subject.Kind != WebhookSubjectSystem && e.Subject.ID == "") || webhookSensitiveString(e.Subject.ID) {
		return ErrInvalid
	}
	redacted, err := RedactWebhookData(e.Data)
	if err != nil {
		return err
	}
	if !webhookDataEqual(redacted, e.Data) {
		return ErrInvalid
	}
	return nil
}

// WebhookRedacted replaces values that must never leave the server.
const WebhookRedacted = "[redacted]"

// webhookSensitiveKeyParts: a data key whose normalized form (lower case,
// separators removed) contains any of these has its value replaced.
var webhookSensitiveKeyParts = []string{
	"password", "passwd", "passphrase", "token", "secret", "apikey", "accesskey",
	"privatekey", "authorization", "cookie", "credential", "signature", "hash",
	"salt", "totp", "sessionkey", "deviceid", "ipaddress", "remoteaddr",
}

// webhookPathKeySuffixes: values under keys ending in these (normalized)
// are reduced to their final path element even when they do not look
// absolute, e.g. "filePath", "nfoFile", "mediaDirectory".
var webhookPathKeySuffixes = []string{"path", "filename", "directory", "folder"}

func webhookPathKey(key string) bool {
	k := normalizeWebhookKey(key)
	if k == "file" || strings.HasSuffix(k, "nfofile") || strings.HasSuffix(k, "mediafile") {
		return true
	}
	for _, s := range webhookPathKeySuffixes {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

func normalizeWebhookKey(key string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		if r == '_' || r == '-' || r == '.' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func webhookKeyHas(key string, parts []string) bool {
	k := normalizeWebhookKey(key)
	for _, p := range parts {
		if strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// webhookCredentialMarkers catch credentials embedded in free text such as
// URLs with query tokens or copied header lines.
var webhookCredentialMarkers = []string{
	"token=", "api_key=", "apikey=", "access_token", "password=", "secret=", "signature=",
	"authorization:", "bearer ", "-token:", "-----begin",
}

func webhookSensitiveString(s string) bool {
	l := strings.ToLower(s)
	for _, m := range webhookCredentialMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	// userinfo in a URL: scheme://user:pass@host
	if i := strings.Index(l, "://"); i >= 0 {
		rest := l[i+3:]
		if end := strings.IndexAny(rest, "/?#"); end >= 0 {
			rest = rest[:end]
		}
		if strings.Contains(rest, "@") {
			return true
		}
	}
	return false
}

// WebhookLooksLikeLocalPath reports POSIX absolute paths, Windows drive or
// UNC paths, home-relative paths and file URLs.
func WebhookLooksLikeLocalPath(s string) bool {
	switch {
	case strings.HasPrefix(s, "/"), strings.HasPrefix(s, `\\`), strings.HasPrefix(s, "~/"):
		return true
	case strings.HasPrefix(strings.ToLower(s), "file:"):
		return true
	case len(s) >= 3 && (s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z') && s[1] == ':' && (s[2] == '\\' || s[2] == '/'):
		return true
	}
	return false
}

// webhookBaseName returns the last path element using either separator.
func webhookBaseName(s string) string {
	s = strings.TrimRight(s, `/\`)
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	if strings.HasSuffix(s, ":") || s == "~" {
		return ""
	}
	return s
}

// RedactWebhookData returns a deep copy of data that satisfies G12.4:
//   - values under credential-like keys (password, token, secret, api key,
//     cookie, authorization, hash, device id, IP address...) become
//     WebhookRedacted, whatever their type;
//   - strings containing embedded credentials (query tokens, bearer
//     headers, URL userinfo, PEM blocks) become WebhookRedacted;
//   - local paths (absolute, UNC, drive, file: URLs) and any value under a
//     path-like key are reduced to their final element, so a file name may
//     be shared but never the directory layout;
//   - strings are capped at 2048 bytes, lists at 256 entries, objects at 64
//     keys and nesting at 4 levels.
//
// Only JSON-compatible values are accepted (string, bool, integers, finite
// floats, nil, []any, []string, map[string]any); anything else, and invalid
// UTF-8, is ErrInvalid. An empty input yields nil.
func RedactWebhookData(data map[string]any) (map[string]any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	v, err := redactWebhookValue("", data, 1)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}

func redactWebhookValue(key string, value any, depth int) (any, error) {
	if key != "" && webhookKeyHas(key, webhookSensitiveKeyParts) {
		return WebhookRedacted, nil
	}
	switch v := value.(type) {
	case nil, bool, int, int32, int64, uint32:
		return v, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, ErrInvalid
		}
		return v, nil
	case string:
		return redactWebhookString(key, v)
	case []string:
		list := make([]any, len(v))
		for i, s := range v {
			list[i] = s
		}
		return redactWebhookValue(key, list, depth)
	case []any:
		if depth > maxWebhookDataDepth || len(v) > maxWebhookDataList {
			return nil, ErrInvalid
		}
		out := make([]any, len(v))
		for i, item := range v {
			r, err := redactWebhookValue(key, item, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		if depth > maxWebhookDataDepth || len(v) > maxWebhookDataKeys {
			return nil, ErrInvalid
		}
		out := make(map[string]any, len(v))
		for k, item := range v {
			if k == "" || len(k) > 64 || !utf8.ValidString(k) {
				return nil, ErrInvalid
			}
			r, err := redactWebhookValue(k, item, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	}
	return nil, ErrInvalid
}

func redactWebhookString(key, s string) (any, error) {
	if !utf8.ValidString(s) {
		return nil, ErrInvalid
	}
	if webhookSensitiveString(s) {
		return WebhookRedacted, nil
	}
	if WebhookLooksLikeLocalPath(s) || (key != "" && webhookPathKey(key) && strings.ContainsAny(s, `/\`)) {
		s = webhookBaseName(s)
	}
	if len(s) > maxWebhookDataString {
		cut := maxWebhookDataString
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return s, nil
}

func webhookDataEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, x := range av {
			y, ok := bv[k]
			if !ok || !webhookDataEqual(x, y) {
				return false
			}
		}
		return true
	case []any:
		var bv []any
		switch t := b.(type) {
		case []any:
			bv = t
		case []string:
			bv = make([]any, len(t))
			for i, s := range t {
				bv[i] = s
			}
		default:
			return false
		}
		if len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !webhookDataEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case nil:
		switch t := b.(type) {
		case nil:
			return true
		case map[string]any:
			return t == nil
		}
		return false
	default:
		return a == b
	}
}

// --- G12.4 signing ---

const (
	WebhookSignatureHeader = "X-Jelee-Signature"
	WebhookTimestampHeader = "X-Jelee-Timestamp"
	// WebhookEventIDHeader repeats the envelope eventId so consumers can
	// deduplicate before parsing the body.
	WebhookEventIDHeader = "X-Jelee-Event-Id"
	// webhookSignatureScheme prefixes each signature so the algorithm can be
	// rotated without ambiguity.
	webhookSignatureScheme = "v1="
	// MinWebhookSecretBytes is the minimum secret length (256 bits).
	MinWebhookSecretBytes = 32
	maxWebhookSecretBytes = 256
	// DefaultWebhookReplayWindow is how far a timestamp may lie from the
	// verifier's clock in either direction.
	DefaultWebhookReplayWindow = 5 * time.Minute
	maxWebhookSignatures       = 4
)

var (
	ErrWebhookSignatureMissing   = errors.New("webhook signature missing")
	ErrWebhookSignatureMalformed = errors.New("webhook signature malformed")
	ErrWebhookSignatureMismatch  = errors.New("webhook signature mismatch")
	ErrWebhookTimestampExpired   = errors.New("webhook timestamp outside replay window")
	// ErrWebhookTargetDenied refuses an endpoint URL that the G12.5 target
	// policy forbids: not HTTPS, credentials in the URL, a host outside the
	// configured allow list or a literal non-public address.
	ErrWebhookTargetDenied = errors.New("webhook target denied")
)

// WebhookSecret is an endpoint signing secret. It never prints.
type WebhookSecret []byte

func (WebhookSecret) String() string   { return "webhook secret (redacted)" }
func (WebhookSecret) GoString() string { return "webhook secret (redacted)" }

func (s WebhookSecret) Valid() bool {
	return len(s) >= MinWebhookSecretBytes && len(s) <= maxWebhookSecretBytes
}

// WebhookSigningKeys supports rotation (G12.4): while Previous is set and
// now is before PreviousUntil, every delivery carries a signature from both
// secrets so consumers can switch at their own pace.
type WebhookSigningKeys struct {
	Current       WebhookSecret
	Previous      WebhookSecret
	PreviousUntil time.Time
}

func (WebhookSigningKeys) String() string   { return "webhook signing keys (redacted)" }
func (WebhookSigningKeys) GoString() string { return "webhook signing keys (redacted)" }

func (k WebhookSigningKeys) Validate() error {
	if !k.Current.Valid() {
		return ErrInvalid
	}
	if len(k.Previous) > 0 && (!k.Previous.Valid() || k.PreviousUntil.IsZero() || hmac.Equal(k.Previous, k.Current)) {
		return ErrInvalid
	}
	return nil
}

// Active returns the secrets that sign at now, current first.
func (k WebhookSigningKeys) Active(now time.Time) []WebhookSecret {
	out := []WebhookSecret{k.Current}
	if len(k.Previous) > 0 && now.Before(k.PreviousUntil) {
		out = append(out, k.Previous)
	}
	return out
}

// WebhookSignedHeaders are the two G12.4 header values for one attempt.
type WebhookSignedHeaders struct {
	Timestamp string // X-Jelee-Timestamp: Unix seconds, decimal
	Signature string // X-Jelee-Signature: "v1=<hex>" entries joined by ", "
}

func webhookMAC(secret []byte, timestamp string, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return mac.Sum(nil)
}

// SignWebhook signs "<timestamp>.<body>" with HMAC-SHA256 under every active
// key. Each attempt is signed afresh with the attempt time, so retries and
// manual replays of an old event still fall inside the consumer's window,
// while the unchanged eventId lets the consumer deduplicate them.
func SignWebhook(keys WebhookSigningKeys, body []byte, now time.Time) (WebhookSignedHeaders, error) {
	if err := keys.Validate(); err != nil {
		return WebhookSignedHeaders{}, err
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	active := keys.Active(now)
	parts := make([]string, len(active))
	for i, secret := range active {
		parts[i] = webhookSignatureScheme + hex.EncodeToString(webhookMAC(secret, ts, body))
	}
	return WebhookSignedHeaders{Timestamp: ts, Signature: strings.Join(parts, ", ")}, nil
}

// VerifyWebhookSignature is the consumer-side check (also used by Jelee's
// own tests and endpoint self-test). It accepts the request when any v1
// entry matches any of secrets and the timestamp lies within window of now.
// window <= 0 means DefaultWebhookReplayWindow. Comparison is constant
// time. Requests inside the window can still be replayed verbatim; consumers
// close that gap by deduplicating on eventId (see WebhookDedup).
func VerifyWebhookSignature(secrets []WebhookSecret, header WebhookSignedHeaders, body []byte, now time.Time, window time.Duration) error {
	if header.Signature == "" || header.Timestamp == "" {
		return ErrWebhookSignatureMissing
	}
	if window <= 0 {
		window = DefaultWebhookReplayWindow
	}
	if len(header.Timestamp) > 12 {
		return ErrWebhookSignatureMalformed
	}
	unix, err := strconv.ParseInt(header.Timestamp, 10, 64)
	if err != nil || unix <= 0 || strconv.FormatInt(unix, 10) != header.Timestamp {
		return ErrWebhookSignatureMalformed
	}
	var candidates [][]byte
	for _, part := range strings.Split(header.Signature, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, webhookSignatureScheme) {
			continue // unknown future scheme
		}
		raw, err := hex.DecodeString(part[len(webhookSignatureScheme):])
		if err != nil || len(raw) != sha256.Size {
			return ErrWebhookSignatureMalformed
		}
		candidates = append(candidates, raw)
		if len(candidates) > maxWebhookSignatures {
			return ErrWebhookSignatureMalformed
		}
	}
	if len(candidates) == 0 {
		return ErrWebhookSignatureMalformed
	}
	matched := false
	for _, secret := range secrets {
		if len(secret) == 0 {
			continue
		}
		want := webhookMAC(secret, header.Timestamp, body)
		for _, got := range candidates {
			if hmac.Equal(want, got) {
				matched = true
			}
		}
	}
	if !matched {
		return ErrWebhookSignatureMismatch
	}
	skew := now.Sub(time.Unix(unix, 0))
	if skew > window || skew < -window {
		return ErrWebhookTimestampExpired
	}
	return nil
}

// WebhookDedup is a consumer-side replay guard: it remembers eventIds for
// the replay window. A signed request older than the window is rejected by
// VerifyWebhookSignature, so after that the memory is no longer needed.
// Consumers answer a duplicate with 2xx without reprocessing it; failing it
// would make Jelee retry an event that was already handled. Not safe for
// concurrent use; callers serialize access.
type WebhookDedup struct {
	window time.Duration
	seen   map[string]time.Time
}

func NewWebhookDedup(window time.Duration) *WebhookDedup {
	if window <= 0 {
		window = DefaultWebhookReplayWindow
	}
	return &WebhookDedup{window: window, seen: map[string]time.Time{}}
}

// Seen records eventID at now and reports whether it was already recorded
// within the last two windows (a timestamp may be up to one window in the
// future and one in the past).
func (d *WebhookDedup) Seen(eventID string, now time.Time) bool {
	for id, at := range d.seen {
		if now.Sub(at) > 2*d.window || at.After(now.Add(2*d.window)) {
			delete(d.seen, id)
		}
	}
	if _, ok := d.seen[eventID]; ok {
		return true
	}
	d.seen[eventID] = now
	return false
}

// Len reports how many eventIds are remembered.
func (d *WebhookDedup) Len() int { return len(d.seen) }

// --- G12.2 headers and subscription ---

// webhookReservedHeaders cannot be set by endpoint configuration.
var webhookReservedHeaders = []string{
	"host", "content-length", "content-type", "content-encoding", "transfer-encoding",
	"connection", "keep-alive", "upgrade", "te", "trailer", "proxy-authorization",
	"proxy-connection", "expect", "cookie", "user-agent",
	strings.ToLower(WebhookSignatureHeader), strings.ToLower(WebhookTimestampHeader), strings.ToLower(WebhookEventIDHeader),
}

const (
	maxWebhookCustomHeaders   = 16
	maxWebhookHeaderValueSize = 1024
)

// ValidateWebhookHeaders checks endpoint custom headers: RFC 7230 token
// names, no control characters in values, no reserved or X-Jelee-* names,
// case-insensitively unique.
func ValidateWebhookHeaders(headers map[string]string) error {
	if len(headers) > maxWebhookCustomHeaders {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for name, value := range headers {
		lower := strings.ToLower(name)
		if name == "" || len(name) > 64 || seen[lower] || strings.HasPrefix(lower, "x-jelee-") {
			return ErrInvalid
		}
		seen[lower] = true
		for _, r := range webhookReservedHeaders {
			if lower == r {
				return ErrInvalid
			}
		}
		for i := 0; i < len(name); i++ {
			if !webhookTokenChar(name[i]) {
				return ErrInvalid
			}
		}
		if len(value) > maxWebhookHeaderValueSize || !utf8.ValidString(value) {
			return ErrInvalid
		}
		for _, r := range value {
			if r < 0x20 && r != '\t' || r == 0x7f {
				return ErrInvalid
			}
		}
	}
	return nil
}

func webhookTokenChar(c byte) bool {
	if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// NormalizeWebhookFilter validates an event subscription filter and returns
// it sorted and deduplicated. An empty filter subscribes to every event.
func NormalizeWebhookFilter(filter []WebhookEventType) ([]WebhookEventType, error) {
	if len(filter) == 0 {
		return nil, nil
	}
	set := map[WebhookEventType]bool{}
	for _, t := range filter {
		if !t.Valid() {
			return nil, ErrInvalid
		}
		set[t] = true
	}
	out := make([]WebhookEventType, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// WebhookFilterMatches reports whether filter subscribes to t.
func WebhookFilterMatches(filter []WebhookEventType, t WebhookEventType) bool {
	if len(filter) == 0 {
		return t.Valid()
	}
	for _, f := range filter {
		if f == t {
			return true
		}
	}
	return false
}

// --- G12.3 retry schedule ---

// WebhookRetryPolicy is the per-endpoint retry configuration (G12.2/G12.3).
// Attempt n (1-based) that fails is retried after
// min(MaxDelay, BaseDelay*2^(n-1)) scaled by a jitter factor uniformly drawn
// from [1-Jitter, 1+Jitter] and capped at MaxDelay again. After
// MaxAttempts attempts the delivery is dead-lettered.
type WebhookRetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Jitter      float64
}

func DefaultWebhookRetryPolicy() WebhookRetryPolicy {
	return WebhookRetryPolicy{MaxAttempts: 8, BaseDelay: 10 * time.Second, MaxDelay: time.Hour, Jitter: 0.2}
}

const (
	MaxWebhookAttempts     = 20
	MinWebhookRetryDelay   = time.Second
	MaxWebhookRetryCeiling = 24 * time.Hour
)

func (p WebhookRetryPolicy) Validate() error {
	if p.MaxAttempts < 1 || p.MaxAttempts > MaxWebhookAttempts ||
		p.BaseDelay < MinWebhookRetryDelay || p.MaxDelay < p.BaseDelay || p.MaxDelay > MaxWebhookRetryCeiling ||
		math.IsNaN(p.Jitter) || p.Jitter < 0 || p.Jitter > 1 {
		return ErrInvalid
	}
	return nil
}

// WebhookJitter returns a value in [0, 1). Production wiring passes a
// random source; tests pass a constant.
type WebhookJitter func() float64

// Delay is the wait after failed attempt number attempt (>= 1).
func (p WebhookRetryPolicy) Delay(attempt int, jitter WebhookJitter) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := p.MaxDelay
	if shift := attempt - 1; shift < 62 {
		if d := p.BaseDelay << shift; d > 0 && d>>shift == p.BaseDelay && d < p.MaxDelay {
			delay = d
		}
	}
	if p.Jitter > 0 && jitter != nil {
		r := jitter()
		if math.IsNaN(r) || r < 0 {
			r = 0
		}
		if r >= 1 {
			r = math.Nextafter(1, 0)
		}
		factor := 1 - p.Jitter + 2*p.Jitter*r
		delay = time.Duration(float64(delay) * factor)
	}
	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	if delay < MinWebhookRetryDelay {
		delay = MinWebhookRetryDelay
	}
	return delay
}

// WebhookOutcomeKind classifies one delivery attempt.
type WebhookOutcomeKind string

const (
	WebhookOutcomeDelivered WebhookOutcomeKind = "delivered" // 2xx
	WebhookOutcomeHTTP      WebhookOutcomeKind = "http"      // non-2xx response
	WebhookOutcomeTimeout   WebhookOutcomeKind = "timeout"
	WebhookOutcomeNetwork   WebhookOutcomeKind = "network" // DNS, connect, TLS handshake, reset
	WebhookOutcomeBlocked   WebhookOutcomeKind = "blocked" // SSRF/allow-list refusal (G12.5)
	WebhookOutcomeTLS       WebhookOutcomeKind = "tls"     // certificate verification failed
	WebhookOutcomeInvalid   WebhookOutcomeKind = "invalid" // event or endpoint no longer valid
)

// WebhookOutcome is what the deliverer observed. RetryAfter carries a
// parsed Retry-After header for 429/503, zero otherwise.
type WebhookOutcome struct {
	Kind       WebhookOutcomeKind
	StatusCode int
	RetryAfter time.Duration
}

// Retryable: timeouts, network errors, 408, 425, 429 and 5xx are transient.
// Other 4xx, 3xx (redirects are not followed), SSRF refusals, certificate
// failures and invalid events will not heal by waiting and dead-letter at
// once; an operator can replay them manually after fixing the cause.
func (o WebhookOutcome) Retryable() bool {
	switch o.Kind {
	case WebhookOutcomeTimeout, WebhookOutcomeNetwork:
		return true
	case WebhookOutcomeHTTP:
		c := o.StatusCode
		return c == 408 || c == 425 || c == 429 || c >= 500 && c <= 599
	}
	return false
}

// WebhookDeliveryState is the lifecycle of one (event, endpoint) delivery.
type WebhookDeliveryState string

const (
	WebhookDeliveryPending   WebhookDeliveryState = "pending"
	WebhookDeliveryDelivered WebhookDeliveryState = "delivered"
	WebhookDeliveryDead      WebhookDeliveryState = "dead"
)

// WebhookRetryDecision is the next state after an attempt.
type WebhookRetryDecision struct {
	State  WebhookDeliveryState
	NextAt time.Time // set only when State is pending
	Delay  time.Duration
}

// DecideWebhookRetry applies the policy to failed or successful attempt
// number attempt (1-based) that finished at finishedAt. A Retry-After hint
// lengthens, never shortens, the computed delay and is capped at MaxDelay.
func DecideWebhookRetry(p WebhookRetryPolicy, attempt int, outcome WebhookOutcome, finishedAt time.Time, jitter WebhookJitter) WebhookRetryDecision {
	if outcome.Kind == WebhookOutcomeDelivered {
		return WebhookRetryDecision{State: WebhookDeliveryDelivered}
	}
	if !outcome.Retryable() || attempt >= p.MaxAttempts {
		return WebhookRetryDecision{State: WebhookDeliveryDead}
	}
	delay := p.Delay(attempt, jitter)
	if outcome.RetryAfter > delay {
		delay = min(outcome.RetryAfter, p.MaxDelay)
	}
	return WebhookRetryDecision{State: WebhookDeliveryPending, NextAt: finishedAt.Add(delay), Delay: delay}
}
