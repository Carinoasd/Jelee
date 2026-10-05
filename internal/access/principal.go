// Package access carries server-authenticated identity across application boundaries.
package access

import (
	"context"
	"net/netip"
	"time"
)

// ClientKind is persisted with a session when the server issues its credential.
// Never construct it from a request's User-Agent or a client-supplied kind field.
type ClientKind string

const (
	ClientWeb    ClientKind = "web"
	ClientNative ClientKind = "native"
)

// Principal is populated only after authentication and session revocation checks.
type Principal struct {
	UserID    string
	SessionID string
	Kind      ClientKind
	Admin     bool
	Locale    string
	// ShareID is set for the guest session of a share link (G48.6): the
	// guest account sees only the share's scope, through the unified filter.
	ShareID string
	// ShareReadOnly reports a guest whose share refuses every write.
	ShareReadOnly bool
	// Request is what the HTTP layer observed about the request this
	// principal authenticated (G48.5, G47); storage passes it to the unified
	// filter. Nil outside a request.
	Request *RequestScope
}

// RequestScope is the per-request input of the unified authorization
// filter: the network attributes network rules match (G48.5) and the
// library set a client control restrict_libraries decision left (G47).
type RequestScope struct {
	// IP is the client address as resolved through the trusted proxy
	// settings, never read from a forwarding header directly.
	IP netip.Addr
	// Kind is the server-issued session kind.
	Kind ClientKind
	// Libraries is nil when the request may see every library it is
	// granted; otherwise only these library IDs (possibly none).
	Libraries []string
}

// LAN reports a private (RFC 1918), unique local (fc00::/7) or loopback
// client address. An unknown address is not on the LAN.
func (s RequestScope) LAN() bool {
	a := s.IP.Unmap()
	return a.IsValid() && (a.IsPrivate() || a.IsLoopback())
}

type principalKey struct{}

// WithPrincipal attaches a verified identity. It belongs in authentication code,
// never in a mapper that copies unsigned fields from the incoming request.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok && principal.UserID != "" && principal.SessionID != ""
}

// SessionClient is what the server recorded about a session's client when it
// issued the credential: the labels a native login reported (empty for web
// sessions) and the device name. They are client-supplied at login and only
// describe the client; client control rules match them (G47.1).
type SessionClient struct {
	DeviceID   string
	Name       string
	Version    string
	DeviceName string
	// IssuedAt is when the session was created.
	IssuedAt time.Time
}
