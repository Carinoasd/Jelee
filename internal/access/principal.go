// Package access carries server-authenticated identity across application boundaries.
package access

import (
	"context"
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
