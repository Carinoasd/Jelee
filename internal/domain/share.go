package domain

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Share links and their guest sessions (G48.6) and the network rules of
// libraries (G48.5). docs/access-control.md describes both and their place
// in the precedence of the unified filter.

var (
	// ErrShareUnavailable refuses a share link that is unknown, revoked or
	// expired, alike (404 share_unavailable).
	ErrShareUnavailable = errors.New("share unavailable")
	// ErrShareForbidden refuses a guest session a route outside what a share
	// allows (403 share_forbidden).
	ErrShareForbidden = errors.New("share guest forbidden")
	// ErrShareReadOnly refuses a write of a guest whose share is read-only
	// (403 share_read_only).
	ErrShareReadOnly = errors.New("share read only")
	// ErrSharePlaybackDisabled refuses a native guest session for a share
	// that does not allow playback (403 share_playback_disabled).
	ErrSharePlaybackDisabled = errors.New("share playback disabled")
)

const (
	// ShareLifetimeMax bounds how far ahead a share may expire.
	ShareLifetimeMax = 90 * 24 * time.Hour
	// ShareLifetimeMin is the shortest share lifetime.
	ShareLifetimeMin = 5 * time.Minute
	// ShareStreamsMax bounds the concurrent playbacks of one share.
	ShareStreamsMax = 16
	// ShareNoteMax bounds a share note in bytes.
	ShareNoteMax = 512
	// SharesLiveMax bounds the live (unrevoked, unexpired) shares.
	SharesLiveMax = 1000
	// SharesListMax bounds one share listing.
	SharesListMax = 200
	// ShareAccessPageMax bounds one page of a share's access records.
	ShareAccessPageMax = 100
	// NetworkRulesMax bounds the stored network rules.
	NetworkRulesMax = 1000
	// NetworkRuleCIDRsMax bounds the address ranges of one network rule.
	NetworkRuleCIDRsMax = 64
	// NetworkRuleNoteMax bounds a network rule note in bytes.
	NetworkRuleNoteMax = 2048
)

// ShareInput is what an administrator sets when creating a share: exactly
// one of LibraryID (the whole library) and ItemID (the item and its
// descendants).
type ShareInput struct {
	LibraryID string    `json:"libraryId,omitempty"`
	ItemID    string    `json:"itemId,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
	// ReadOnly refuses every write of the guest, including playback
	// reports and played marks.
	ReadOnly bool `json:"readOnly"`
	// AllowPlayback lets the link issue native guest sessions, which may
	// direct play under the existing delivery rules (G07.4). Web guest
	// sessions never play.
	AllowPlayback bool `json:"allowPlayback"`
	// MaxStreams caps the concurrent playbacks of all the share's guests.
	MaxStreams int    `json:"maxStreams"`
	Note       string `json:"note"`
}

// Valid checks the input against now.
func (in ShareInput) Valid(now time.Time) bool {
	if (in.LibraryID == "") == (in.ItemID == "") || in.LibraryID != "" && !ValidID(in.LibraryID) || in.ItemID != "" && !ValidID(in.ItemID) {
		return false
	}
	if in.ExpiresAt.IsZero() || in.ExpiresAt.Before(now.Add(ShareLifetimeMin)) || in.ExpiresAt.After(now.Add(ShareLifetimeMax)) {
		return false
	}
	return in.MaxStreams >= 1 && in.MaxStreams <= ShareStreamsMax && validShareNote(in.Note, ShareNoteMax)
}

func validShareNote(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

// Share states.
const (
	ShareActive  = "active"
	ShareExpired = "expired"
	ShareRevoked = "revoked"
)

// Share is a stored share link; the token is never stored or listed.
type Share struct {
	ID            string     `json:"id"`
	LibraryID     string     `json:"libraryId"`
	LibraryName   string     `json:"libraryName"`
	ItemID        string     `json:"itemId,omitempty"`
	ItemTitle     string     `json:"itemTitle,omitempty"`
	ItemKind      string     `json:"itemKind,omitempty"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	ReadOnly      bool       `json:"readOnly"`
	AllowPlayback bool       `json:"allowPlayback"`
	MaxStreams    int        `json:"maxStreams"`
	Note          string     `json:"note"`
	CreatedBy     string     `json:"createdBy,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	// State is active, expired or revoked.
	State string `json:"state"`
	// ActiveSessions counts the guest sessions still live.
	ActiveSessions int `json:"activeSessions"`
	// LastUsedAt is the latest recorded use of any guest session.
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// ShareGrant is a created share with its token, returned once.
type ShareGrant struct {
	Share Share `json:"share"`
	// Token is the link secret; only its digest is stored.
	Token string `json:"token"`
}

// ShareRedemption exchanges a share token for a guest session.
type ShareRedemption struct {
	Token string
	// Native asks for a native session (playback shares only).
	Native      bool
	Client      NativeClient
	DeviceName  string
	IP          string
	MaxSessions int
	SessionTTL  time.Duration
}

// GuestShare is what a guest session may know about its own share.
type GuestShare struct {
	ID            string    `json:"id"`
	LibraryID     string    `json:"libraryId"`
	LibraryName   string    `json:"libraryName"`
	ItemID        string    `json:"itemId,omitempty"`
	ItemTitle     string    `json:"itemTitle,omitempty"`
	ItemKind      string    `json:"itemKind,omitempty"`
	ExpiresAt     time.Time `json:"expiresAt"`
	ReadOnly      bool      `json:"readOnly"`
	AllowPlayback bool      `json:"allowPlayback"`
}

// ShareAccess is one recorded use of a guest session (G48.6): the route
// pattern, never the path, query or credential.
type ShareAccess struct {
	ShareID    string
	Actor      Actor
	ClientKind string
	Route      string
	// Refused reports a route the share does not allow.
	Refused bool
}

// NetworkRuleInput is the administrator-editable part of a network rule
// (G48.5). A library with enabled rules is visible only to requests at
// least one of them matches; a rule matches when every condition it sets
// holds.
type NetworkRuleInput struct {
	LibraryID string `json:"libraryId"`
	// Network is any, lan (private, unique local or loopback client
	// address) or wan (any other address).
	Network string `json:"network"`
	// CIDRs are addresses or prefixes; empty means any address.
	CIDRs []string `json:"cidrs"`
	// ClientKinds are web and/or native; empty means any session kind.
	ClientKinds []string `json:"clientKinds"`
	// IncludeAdmins subjects administrators to the rule as well.
	IncludeAdmins bool   `json:"includeAdmins"`
	Enabled       bool   `json:"enabled"`
	Note          string `json:"note"`
}

// Valid checks the vocabularies and bounds; address syntax is checked
// where addresses are parsed (the adapter).
func (in NetworkRuleInput) Valid() bool {
	if !ValidID(in.LibraryID) || !oneOf(in.Network, []string{"any", "lan", "wan"}) || len(in.CIDRs) > NetworkRuleCIDRsMax || len(in.ClientKinds) > 2 {
		return false
	}
	for _, c := range in.CIDRs {
		if c == "" || len(c) > 64 {
			return false
		}
	}
	seen := map[string]bool{}
	for _, k := range in.ClientKinds {
		if k != "web" && k != "native" || seen[k] {
			return false
		}
		seen[k] = true
	}
	return validClientNote(in.Note) && len(in.Note) <= NetworkRuleNoteMax
}

// NetworkRule is a stored network rule.
type NetworkRule struct {
	ID string `json:"id"`
	NetworkRuleInput
	LibraryName string    `json:"libraryName"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ShareAccessRecord is one audited event of a share as listed to
// administrators: created, revoked, redeemed, refused or accessed.
type ShareAccessRecord struct {
	ID         int64     `json:"id"`
	Event      string    `json:"event"`
	OccurredAt time.Time `json:"occurredAt"`
	ActorID    string    `json:"actorId,omitempty"`
	IP         string    `json:"ip,omitempty"`
	SessionID  string    `json:"sessionId,omitempty"`
	ClientKind string    `json:"clientKind,omitempty"`
	DeviceName string    `json:"deviceName,omitempty"`
	// Route is the method and route pattern of an accessed or refused
	// request; never a path, query or credential.
	Route string `json:"route,omitempty"`
	// Reason explains a refused redemption: revoked, expired, playback or
	// session_limit.
	Reason string `json:"reason,omitempty"`
}
