package domain

import (
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Client control (G47): administrator rules on who is calling. The rule
// engine lives in internal/access; these are the stored and listed forms.
// docs/client-control.md describes the model, precedence and its limits.

var (
	// ErrClientBlocked refuses a request a client rule or the unknown-client
	// policy denies (403 client_blocked).
	ErrClientBlocked = errors.New("client blocked")
	// ErrClientPending refuses a client that awaits administrator approval
	// (403 client_pending_approval).
	ErrClientPending = errors.New("client pending approval")
	// ErrClientReadOnly refuses a write by a client restricted to reading
	// (403 client_read_only).
	ErrClientReadOnly = errors.New("client restricted to reading")
	// ErrClientRateLimited refuses a request over a client rate limit
	// (429 client_rate_limited).
	ErrClientRateLimited = errors.New("client rate limited")
)

const (
	// ClientRulesMax bounds the stored rules; the compiled set stays well
	// inside the engine limits.
	ClientRulesMax = 10000
	// ClientRulePatternMax bounds a pattern in bytes.
	ClientRulePatternMax = 1024
	// ClientRuleNoteMax bounds a note in bytes.
	ClientRuleNoteMax = 2048
	// ClientRuleScopeValuesMax bounds the values of a scoped rule.
	ClientRuleScopeValuesMax = 1000
	// ClientRuleLibrariesMax bounds the libraries of a restrict_libraries rule.
	ClientRuleLibrariesMax = 1000
	// ClientRulePriorityLimit bounds a priority in both directions.
	ClientRulePriorityLimit = 1000000
	// ClientAliasMax bounds an administrator alias of a known client.
	ClientAliasMax = 128
	// ClientHitRetention is how long hit records are kept.
	ClientHitRetention = 30 * 24 * time.Hour
	// ClientHitExportMax bounds one export.
	ClientHitExportMax = 10000
)

// Client rule vocabularies, mirroring internal/access.
var (
	clientRuleDimensions = []string{"user_agent", "app_name", "app_version", "device_id", "device_name", "device_type", "ip", "api_key_fingerprint", "header"}
	clientRuleMatches    = []string{"exact", "prefix", "glob", "regex", "cidr", "absent"}
	// restrict_libraries narrows the libraries of the request through the
	// unified storage filter (G48.5).
	clientRuleActions   = []string{"allow", "deny", "read_only", "rate_limit", "force_relogin", "restrict_libraries", "observe", "shadow"}
	clientRuleIntents   = []string{"allow", "deny", "read_only", "rate_limit", "force_relogin", "restrict_libraries"}
	clientRuleScopes    = []string{"global", "user", "client_kind"}
	clientUnknownPolicy = []string{"allow", "read_only", "deny", "pending_approval"}
)

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// ClientRuleWindow bounds when a rule is in force; see access.Window.
type ClientRuleWindow struct {
	From       *time.Time `json:"from,omitempty"`
	Until      *time.Time `json:"until,omitempty"`
	DailyStart string     `json:"dailyStart,omitempty"`
	DailyEnd   string     `json:"dailyEnd,omitempty"`
	// Weekdays are 0 (Sunday) to 6 (Saturday); empty means every day.
	Weekdays []int  `json:"weekdays,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

// Empty reports a window without any bound.
func (w ClientRuleWindow) Empty() bool {
	return w.From == nil && w.Until == nil && w.DailyStart == "" && w.DailyEnd == "" && len(w.Weekdays) == 0 && w.TimeZone == ""
}

// ClientRuleRate is the limit of a rate_limit rule.
type ClientRuleRate struct {
	Requests      int `json:"requests"`
	PeriodSeconds int `json:"periodSeconds"`
}

// ClientRuleInput is the administrator-editable part of a rule.
type ClientRuleInput struct {
	Dimension string `json:"dimension"`
	Header    string `json:"header,omitempty"`
	Match     string `json:"match"`
	Pattern   string `json:"pattern"`
	CaseFold  bool   `json:"caseFold"`
	Priority  int    `json:"priority"`
	Action    string `json:"action"`
	// Intent is what an observe or shadow rule would enforce once switched
	// to blocking; required for those actions, refused for the others.
	Intent    string          `json:"intent,omitempty"`
	RateLimit *ClientRuleRate `json:"rateLimit,omitempty"`
	// Libraries are the library IDs a restrict_libraries rule leaves the
	// request; required for that action, refused for the others.
	Libraries   []string          `json:"libraries,omitempty"`
	ScopeKind   string            `json:"scopeKind"`
	ScopeValues []string          `json:"scopeValues"`
	Window      *ClientRuleWindow `json:"window,omitempty"`
	Enabled     bool              `json:"enabled"`
	Note        string            `json:"note"`
}

// ClientRule is a stored rule with its counters.
type ClientRule struct {
	ID string `json:"id"`
	ClientRuleInput
	HitCount  int64      `json:"hitCount"`
	LastHitAt *time.Time `json:"lastHitAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

func validClientLabel(s string, max int) bool {
	return len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

// validClientNote allows line breaks and tabs but no other control characters.
func validClientNote(s string) bool {
	return len(s) <= ClientRuleNoteMax && utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) < 0
}

// Normalize fills defaults that have a single meaning: an empty scope is
// global and a window without bounds is none.
func (in ClientRuleInput) Normalize() ClientRuleInput {
	if in.ScopeKind == "" {
		in.ScopeKind = "global"
	}
	if in.ScopeValues == nil {
		in.ScopeValues = []string{}
	}
	if len(in.Libraries) > 0 {
		// One spelling per library, in a stable order.
		libraries := make([]string, 0, len(in.Libraries))
		for _, id := range in.Libraries {
			libraries = append(libraries, strings.ToLower(id))
		}
		slices.Sort(libraries)
		in.Libraries = slices.Compact(libraries)
	} else {
		in.Libraries = nil
	}
	if in.Window != nil {
		w := *in.Window
		if w.From != nil {
			t := w.From.UTC()
			w.From = &t
		}
		if w.Until != nil {
			t := w.Until.UTC()
			w.Until = &t
		}
		if len(w.Weekdays) == 0 {
			w.Weekdays = nil
		}
		in.Window = &w
		if w.Empty() {
			in.Window = nil
		}
	}
	return in
}

// Valid checks the vocabularies and bounds storage relies on. Pattern
// syntax, regex cost and window semantics are checked by compiling the rule
// (internal/access), which the adapter does before storing it.
func (in ClientRuleInput) Valid() bool {
	if !oneOf(in.Dimension, clientRuleDimensions) || !oneOf(in.Match, clientRuleMatches) || !oneOf(in.Action, clientRuleActions) || !oneOf(in.ScopeKind, clientRuleScopes) {
		return false
	}
	if (in.Dimension == "header") != (in.Header != "") || len(in.Header) > 256 {
		return false
	}
	if len(in.Pattern) > ClientRulePatternMax || !utf8.ValidString(in.Pattern) || (in.Match == "absent") != (in.Pattern == "") {
		return false
	}
	if in.Priority < -ClientRulePriorityLimit || in.Priority > ClientRulePriorityLimit || !validClientNote(in.Note) {
		return false
	}
	recordOnly := in.Action == "observe" || in.Action == "shadow"
	if recordOnly != (in.Intent != "") || in.Intent != "" && !oneOf(in.Intent, clientRuleIntents) {
		return false
	}
	effective := in.Action
	if recordOnly {
		effective = in.Intent
	}
	if (effective == "rate_limit") != (in.RateLimit != nil) {
		return false
	}
	if (effective == "restrict_libraries") != (len(in.Libraries) > 0) || len(in.Libraries) > ClientRuleLibrariesMax {
		return false
	}
	for _, id := range in.Libraries {
		if !ValidID(id) {
			return false
		}
	}
	if in.RateLimit != nil && (in.RateLimit.Requests < 1 || in.RateLimit.Requests > 1000000 || in.RateLimit.PeriodSeconds < 1 || in.RateLimit.PeriodSeconds > 86400) {
		return false
	}
	if (in.ScopeKind == "global") != (len(in.ScopeValues) == 0) || len(in.ScopeValues) > ClientRuleScopeValuesMax {
		return false
	}
	for _, v := range in.ScopeValues {
		if v == "" || !validClientLabel(v, 256) {
			return false
		}
		if in.ScopeKind == "user" && !ValidID(v) {
			return false
		}
		if in.ScopeKind == "client_kind" && v != "web" && v != "native" {
			return false
		}
	}
	if w := in.Window; w != nil {
		if len(w.Weekdays) > 7 || len(w.TimeZone) > 64 {
			return false
		}
		for _, d := range w.Weekdays {
			if d < 0 || d > 6 {
				return false
			}
		}
	}
	return true
}

// ClientPolicy is the server-wide client control policy (G47.6, G47.7).
type ClientPolicy struct {
	// UnknownClients applies to authenticated requests of clients that are
	// not trusted and that no allow rule covers: allow, read_only, deny or
	// pending_approval.
	UnknownClients string `json:"unknownClients"`
	// ExemptAdmins keeps administrator sessions out of enforcement.
	ExemptAdmins bool `json:"exemptAdmins"`
	// ExemptLoopback keeps loopback peers out of enforcement.
	ExemptLoopback bool `json:"exemptLoopback"`
	// Version increases with every change that alters an evaluation.
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Valid checks the unknown-client policy.
func (p ClientPolicy) Valid() bool { return oneOf(p.UnknownClients, clientUnknownPolicy) }

// DefaultClientPolicy is the policy jelee-cli access reset-policies restores.
func DefaultClientPolicy() ClientPolicy {
	return ClientPolicy{UnknownClients: "allow", ExemptAdmins: true, ExemptLoopback: true}
}

// ClientControlState is everything the request gate compiles.
type ClientControlState struct {
	Policy ClientPolicy
	Rules  []ClientRule
	// TrustedKeys are the client keys of trusted known clients.
	TrustedKeys []string
}

// KnownClient is a client seen on authenticated requests (G47.5).
type KnownClient struct {
	ID          string    `json:"id"`
	AppName     string    `json:"appName,omitempty"`
	AppVersion  string    `json:"appVersion,omitempty"`
	UserAgent   string    `json:"userAgent,omitempty"`
	DeviceID    string    `json:"deviceId,omitempty"`
	DeviceName  string    `json:"deviceName,omitempty"`
	ClientKind  string    `json:"clientKind,omitempty"`
	Alias       string    `json:"alias,omitempty"`
	Trusted     bool      `json:"trusted"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	LastIP      string    `json:"lastIp,omitempty"`
	LastUserID  string    `json:"lastUserId,omitempty"`
	// ActiveSessions counts the unrevoked, unexpired sessions it used.
	ActiveSessions int `json:"activeSessions"`
	// Blocked reports an enabled, global, always-on deny rule on the same
	// identity the block action matches (the device ID, or the user agent
	// when the client reports none); BlockRuleID names the highest-priority
	// such rule. Other deny rules that may also match are not reflected.
	Blocked     bool   `json:"blocked"`
	BlockRuleID string `json:"blockRuleId,omitempty"`
}

// KnownClientUpdate changes the administrator fields of a known client;
// nil leaves a field unchanged and an empty alias removes it.
type KnownClientUpdate struct {
	Alias   *string `json:"alias,omitempty"`
	Trusted *bool   `json:"trusted,omitempty"`
}

// Valid bounds the alias.
func (u KnownClientUpdate) Valid() bool {
	return u.Alias == nil || validClientLabel(*u.Alias, ClientAliasMax) && strings.TrimSpace(*u.Alias) == *u.Alias
}

// ClientActivity is one throttled observation of a client on an
// authenticated request, recorded into the known clients.
type ClientActivity struct {
	Key        string
	AppName    string
	AppVersion string
	UserAgent  string
	DeviceID   string
	DeviceName string
	ClientKind string
	UserID     string
	SessionID  string
	IP         string
	At         time.Time
}

// ClientHit is one aggregated hit bucket to append.
type ClientHit struct {
	Bucket    time.Time
	RuleID    string
	Mode      string
	Action    string
	Surface   string
	UserID    string
	IP        string
	UserAgent string
	AppName   string
	Hits      int64
}

// ClientRuleCount is a hit counter increment for one rule.
type ClientRuleCount struct {
	RuleID string
	Hits   int64
	Last   time.Time
}

// ClientHitRecord is a listed or exported hit bucket. IP is masked to its
// network (/24, /48) and the user agent truncated (G47.9).
type ClientHitRecord struct {
	ID        int64     `json:"id"`
	Bucket    time.Time `json:"bucket"`
	RuleID    string    `json:"ruleId,omitempty"`
	Mode      string    `json:"mode"`
	Action    string    `json:"action"`
	Surface   string    `json:"surface"`
	UserID    string    `json:"userId,omitempty"`
	Network   string    `json:"network,omitempty"`
	UserAgent string    `json:"userAgent,omitempty"`
	AppName   string    `json:"appName,omitempty"`
	Hits      int64     `json:"hits"`
}

// ClientHitFilter selects hit records. Zero values do not filter.
type ClientHitFilter struct {
	RuleID string
	Mode   string
	Since  time.Time
	Until  time.Time
}

// Valid checks the filter fields.
func (f ClientHitFilter) Valid() bool {
	if f.RuleID != "" && !ValidID(f.RuleID) {
		return false
	}
	if f.Mode != "" && !oneOf(f.Mode, []string{"enforced", "exempt", "observe", "shadow", "default"}) {
		return false
	}
	return f.Since.IsZero() || f.Until.IsZero() || f.Since.Before(f.Until)
}

// ClientHitCount is one entry of a top list.
type ClientHitCount struct {
	Value string `json:"value"`
	Hits  int64  `json:"hits"`
}

// ClientHitStats summarizes hits since a point in time (G47.8). Shadow hits
// are excluded; they feed no statistics.
type ClientHitStats struct {
	Since         time.Time        `json:"since"`
	Total         int64            `json:"total"`
	Blocked       int64            `json:"blocked"`
	Observed      int64            `json:"observed"`
	ByAction      []ClientHitCount `json:"byAction"`
	TopUserAgents []ClientHitCount `json:"topUserAgents"`
	TopIPs        []ClientHitCount `json:"topIps"`
	TopRules      []ClientHitCount `json:"topRules"`
}

// ClientPolicyReset is the result of the emergency reset.
type ClientPolicyReset struct {
	RulesDisabled int64        `json:"rulesDisabled"`
	Policy        ClientPolicy `json:"policy"`
}
