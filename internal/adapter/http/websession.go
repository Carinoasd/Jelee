package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Browser sessions (G35.1). The __Host- prefix makes the browser refuse the
// cookie unless it is Secure, host-only (no Domain) and scoped to Path=/, so a
// sibling subdomain or plain-HTTP origin cannot plant or overwrite it. There is
// deliberately no insecure fallback: browsers treat http://localhost and
// loopback addresses as secure contexts, which covers local development.
const (
	sessionCookieName = "__Host-jelee_session"
	csrfHeaderName    = "X-Jelee-CSRF"
	sessionTokenSize  = 43
)

// authMethod records how the current request was authenticated. token stays
// in memory only; it is never logged or written to a response.
type authMethod struct {
	cookie bool
	token  string
}

type authMethodKey struct{}

func withAuthMethod(ctx context.Context, method authMethod) context.Context {
	return context.WithValue(ctx, authMethodKey{}, method)
}

func authMethodFrom(ctx context.Context) (authMethod, bool) {
	method, ok := ctx.Value(authMethodKey{}).(authMethod)
	return method, ok
}

// csrfToken derives the synchronizer token from the stored session digest:
// HMAC-SHA256 keyed by SHA256(token) over a fixed label. The server recomputes
// it from the presented credential, so it needs no storage, changes with every
// rotation and dies with the session. The digest is only a key here; the raw
// token cannot be recovered from the result.
func csrfToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, digest[:])
	_, _ = mac.Write([]byte("csrf"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func validCSRF(r *http.Request, token string) bool {
	values := r.Header.Values(csrfHeaderName)
	if len(values) != 1 || len(values[0]) != sessionTokenSize {
		return false
	}
	return hmac.Equal([]byte(values[0]), []byte(csrfToken(token)))
}

func safeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

func validSessionToken(token string) bool {
	if len(token) != sessionTokenSize {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// sessionCookieToken returns the single session cookie of a request. Several
// cookies with that name are ambiguous and rejected rather than guessed.
func sessionCookieToken(r *http.Request) (string, bool) {
	cookies := r.CookiesNamed(sessionCookieName)
	if len(cookies) == 0 {
		return "", false
	}
	if len(cookies) != 1 {
		return "", true
	}
	return cookies[0].Value, true
}

func setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	seconds := int(time.Until(expires) / time.Second)
	if seconds < 1 || !validSessionToken(token) {
		clearSessionCookie(w)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", MaxAge: seconds, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// clearRevokedSessionCookie expires the browser cookie when it carries the
// credential this request has just revoked. A cookie for some other session
// is left alone.
func clearRevokedSessionCookie(w http.ResponseWriter, r *http.Request) {
	method, ok := authMethodFrom(r.Context())
	if !ok {
		return
	}
	if cookie, present := sessionCookieToken(r); present && cookie == method.token {
		clearSessionCookie(w)
	}
}

// webGrant adds the browser-only fields to a session grant.
type webGrant struct {
	domain.SessionGrant
	CSRF string `json:"csrf,omitempty"`
}

// issueWebGrant sets the session cookie for web sessions only. Native
// credentials are never placed in a cookie and receive no CSRF token.
func issueWebGrant(w http.ResponseWriter, grant domain.SessionGrant) webGrant {
	if grant.Session.ClientKind != string(access.ClientWeb) {
		return webGrant{SessionGrant: grant}
	}
	setSessionCookie(w, grant.Token, grant.Session.ExpiresAt)
	return webGrant{SessionGrant: grant, CSRF: csrfToken(grant.Token)}
}
