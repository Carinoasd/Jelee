package access

import (
	"net/http"
	"net/netip"
	"time"
)

// Dimension names the request attribute a client-control rule inspects (G47.1).
// Everything except the server-observed IP and the API key fingerprint is
// client-supplied and therefore forgeable; rules that must hold against a
// hostile client should combine them with device IDs, API keys or the
// server-issued session kind (Scope ScopeClientKind).
type Dimension string

const (
	DimUserAgent  Dimension = "user_agent"
	DimAppName    Dimension = "app_name"
	DimAppVersion Dimension = "app_version"
	DimDeviceID   Dimension = "device_id"
	DimDeviceName Dimension = "device_name"
	DimDeviceType Dimension = "device_type"
	// DimIP matches the server-observed peer address against an IP or CIDR.
	DimIP Dimension = "ip"
	// DimAPIKey matches a fingerprint of the presented API key, never the key itself.
	DimAPIKey Dimension = "api_key_fingerprint"
	// DimHeader matches the values of the request header named by Rule.Header.
	DimHeader Dimension = "header"
)

// MatchKind selects how Rule.Pattern is compared with the request value.
type MatchKind string

const (
	MatchExact  MatchKind = "exact"
	MatchPrefix MatchKind = "prefix"
	// MatchGlob supports '*' (any run, including empty) and '?' (one rune);
	// a backslash escapes the next rune. It matches the whole value.
	MatchGlob MatchKind = "glob"
	// MatchRegex uses Go RE2 syntax (linear time, no backreferences) and is an
	// unanchored search; use ^ and $ for a full match.
	MatchRegex MatchKind = "regex"
	// MatchCIDR is valid only for DimIP; Pattern is an address or a prefix.
	MatchCIDR MatchKind = "cidr"
	// MatchAbsent matches when the dimension carries no value; Pattern must be empty.
	MatchAbsent MatchKind = "absent"
)

// Action is what a matching rule does (G47.3). ActionAllow is the allow-list
// entry; every other enforcing action is a deny-list entry of some strength.
type Action string

const (
	ActionAllow             Action = "allow"
	ActionDeny              Action = "deny"
	ActionReadOnly          Action = "read_only"
	ActionRestrictLibraries Action = "restrict_libraries"
	ActionRateLimit         Action = "rate_limit"
	ActionForceRelogin      Action = "force_relogin"
	// ActionObserve records a hit (security log and statistics) without enforcing it.
	ActionObserve Action = "observe"
	// ActionShadow records a hit only in the shadow log; it feeds no statistics or alerts.
	ActionShadow Action = "shadow"
)

func (a Action) enforcing() bool {
	switch a {
	case ActionAllow, ActionDeny, ActionReadOnly, ActionRestrictLibraries, ActionRateLimit, ActionForceRelogin:
		return true
	}
	return false
}

func (a Action) recordOnly() bool { return a == ActionObserve || a == ActionShadow }

// ScopeKind limits where a rule applies (G47.4).
type ScopeKind string

const (
	ScopeGlobal     ScopeKind = "global"
	ScopeUser       ScopeKind = "user"
	ScopeGroup      ScopeKind = "group"
	ScopeLibrary    ScopeKind = "library"
	ScopeClientKind ScopeKind = "client_kind"
)

// Scope restricts a rule to requests whose user, any group, target library or
// server-issued client kind is one of Values. The zero Scope is global.
type Scope struct {
	Kind   ScopeKind
	Values []string
}

// Window bounds when a rule is in force. From is inclusive and Until exclusive;
// zero values leave that side open. DailyStart/DailyEnd ("HH:MM", end
// exclusive) add a recurring wall-clock window in TimeZone (IANA name, empty
// means UTC); a start later than the end wraps past midnight, and Weekdays
// (empty means every day) refer to the day on which that daily window opened.
type Window struct {
	From       time.Time
	Until      time.Time
	DailyStart string
	DailyEnd   string
	Weekdays   []time.Weekday
	TimeZone   string
}

// RateLimit caps a client to Requests per Per.
type RateLimit struct {
	Requests int
	Per      time.Duration
}

// Rule is one administrator-defined client-control rule (G47.2).
type Rule struct {
	ID        string
	Dimension Dimension
	// Header names the inspected header when Dimension is DimHeader.
	Header   string
	Match    MatchKind
	Pattern  string
	CaseFold bool
	// Priority orders rules; larger values win. See Evaluate for conflict rules.
	Priority int
	Window   *Window
	Action   Action
	// Intent is the action an observe/shadow rule would enforce once switched
	// to blocking; it drives Decision.Simulated. Empty means ActionDeny.
	Intent Action
	// Libraries lists the library IDs still reachable under restrict_libraries.
	Libraries []string
	// RateLimit is required for rate_limit.
	RateLimit *RateLimit
	Scope     Scope
	Enabled   bool
	Note      string
}

// Request carries the attributes of one request to be evaluated. The HTTP
// layer fills it; IP must be the transport peer (or a trusted-proxy resolved
// address), never a raw forwarding header.
type Request struct {
	UserAgent         string
	AppName           string
	AppVersion        string
	DeviceID          string
	DeviceName        string
	DeviceType        string
	IP                netip.Addr
	APIKeyFingerprint string
	Headers           http.Header

	Principal Principal
	Groups    []string
	LibraryID string
	// Known reports whether the client is already registered or trusted (G47.5).
	// Unknown clients that no allow rule covers get Options.UnknownClients.
	Known bool
	// Time is the evaluation instant; zero means time.Now().
	Time time.Time
}

// UnknownClientPolicy is the default for clients that are neither known nor
// covered by an allow rule (G47.6).
type UnknownClientPolicy string

const (
	UnknownAllow    UnknownClientPolicy = "allow"
	UnknownReadOnly UnknownClientPolicy = "read_only"
	UnknownDeny     UnknownClientPolicy = "deny"
	UnknownPending  UnknownClientPolicy = "pending_approval"
)

// Limits bound what a rule set may cost to compile and evaluate.
type Limits struct {
	MaxRules        int
	MaxPatternLen   int
	MaxRegexRules   int
	MaxRegexInsts   int
	MaxLibraries    int
	MaxNoteLen      int
	MaxScopeValues  int
	MaxValueLen     int
	MaxHeaderValues int
}

// DefaultLimits returns the limits used when Options.Limits is zero.
func DefaultLimits() Limits {
	return Limits{
		MaxRules:        50000,
		MaxPatternLen:   1024,
		MaxRegexRules:   1000,
		MaxRegexInsts:   4000,
		MaxLibraries:    10000,
		MaxNoteLen:      2048,
		MaxScopeValues:  10000,
		MaxValueLen:     8192,
		MaxHeaderValues: 32,
	}
}

// Options configure a compiled snapshot.
type Options struct {
	// ExemptAdmins keeps administrator sessions out of enforcement (G47.7).
	ExemptAdmins bool
	// ExemptLoopback keeps loopback peers out of enforcement for local diagnostics.
	ExemptLoopback bool
	UnknownClients UnknownClientPolicy
	Limits         Limits
}

// DefaultOptions matches G47.7: admins and loopback are exempt by default and
// unknown clients are allowed.
func DefaultOptions() Options {
	return Options{ExemptAdmins: true, ExemptLoopback: true, UnknownClients: UnknownAllow, Limits: DefaultLimits()}
}

// Verdict is the final gate outcome.
type Verdict string

const (
	VerdictAllow   Verdict = "allow"
	VerdictDeny    Verdict = "deny"
	VerdictPending Verdict = "pending_approval"
)

// Error codes the HTTP layer returns with 403 responses.
const (
	CodeClientBlocked = "client_blocked"
	CodeClientPending = "client_pending_approval"
)

// Outcome is the merged effect of the enforcing rules.
type Outcome struct {
	Verdict Verdict
	// Code is set when Verdict is not allow.
	Code         string
	ReadOnly     bool
	ForceRelogin bool
	// Libraries is nil when library access is unrestricted; otherwise it is the
	// (possibly empty) intersection of all restrict_libraries rules.
	Libraries []string
	RateLimit *RateLimit
	// Rules lists the IDs that shaped this outcome, in precedence order.
	Rules []string
	// DefaultApplied reports that the unknown-client policy contributed.
	DefaultApplied bool
}

// Hit is one matching rule. For observe and shadow hits Action is the rule's
// Intent, the action it would enforce.
type Hit struct {
	RuleID string
	Action Action
}

// ExemptReason explains why enforcement was skipped.
type ExemptReason string

const (
	ExemptNone     ExemptReason = ""
	ExemptAdmin    ExemptReason = "admin"
	ExemptLoopback ExemptReason = "loopback"
)

// Decision is the result of Evaluate.
type Decision struct {
	Outcome
	// Matched lists every enforcing rule that matched, in precedence order,
	// including ones overridden by a higher allow or suppressed by an exemption.
	Matched  []Hit
	Observed []Hit
	Shadowed []Hit
	Exempt   ExemptReason
	// Oversized reports that a dimension value exceeded Limits.MaxValueLen.
	Oversized bool
	// Simulated is the outcome if every matching observe/shadow rule enforced
	// its Intent; nil when none matched. It lets the UI preview a switch to
	// blocking (G47.3).
	Simulated *Outcome
}
