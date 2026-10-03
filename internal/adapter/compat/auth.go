package compat

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Limits applied before and after parsing legacy client credentials.
const (
	// MaxAuthHeaderBytes bounds the raw parameterised authorization header.
	MaxAuthHeaderBytes = 4096
	// MaxAuthFieldBytes bounds every decoded client metadata value.
	MaxAuthFieldBytes = 512
	// MaxAuthParams bounds the number of key/value pairs in one header.
	MaxAuthParams = 64
	// tokenLength is the only accepted access token length: 32 random bytes
	// encoded as unpadded base64url, matching the session store.
	tokenLength = 43
	tokenBytes  = 32
)

var (
	// ErrMalformedAuth reports credentials that cannot be parsed safely:
	// oversized, control characters, invalid UTF-8, too many parameters or
	// ambiguous repeated header lines.
	ErrMalformedAuth = fmt.Errorf("compat: malformed client authorization: %w", domain.ErrInvalid)
	// ErrInvalidToken reports that the selected token source is present but
	// is not a well-formed access token of this system, or that a token
	// source carries more than one value.
	ErrInvalidToken = fmt.Errorf("compat: invalid access token: %w", domain.ErrUnauthenticated)
)

// TokenSource names where ParseClientAuth found the token.
type TokenSource uint8

const (
	TokenSourceNone TokenSource = iota
	// TokenSourceAuthorization is the Token parameter of the parameterised
	// Authorization (or legacy authorization) header.
	TokenSourceAuthorization
	// TokenSourceLegacyHeader is one of the two bare legacy token headers.
	TokenSourceLegacyHeader
	// TokenSourceQueryAPIKey is the ApiKey query parameter.
	TokenSourceQueryAPIKey
	// TokenSourceQueryLegacyAPIKey is the api_key query parameter.
	TokenSourceQueryLegacyAPIKey
)

// ClientAuth is the parsed, unauthenticated client identity of a legacy
// protocol request. Token is empty when the request carries none; a non-empty
// Token is only syntactically valid and still has to be looked up.
type ClientAuth struct {
	Token    string
	Client   string
	Device   string
	DeviceID string
	Version  string
	Source   TokenSource
}

// String never prints the token.
func (a ClientAuth) String() string {
	return fmt.Sprintf("client=%q device=%q deviceId=%q version=%q token=%t", a.Client, a.Device, a.DeviceID, a.Version, a.Token != "")
}

// GoString never prints the token.
func (a ClientAuth) GoString() string { return "compat.ClientAuth{" + a.String() + "}" }

// AuthOptions selects upstream-compatible behaviour.
type AuthOptions struct {
	// Legacy mirrors the upstream legacy authorization switch: it enables the
	// legacy scheme name, the legacy authorization header, both bare token
	// headers and the api_key query parameter.
	Legacy bool
}

// ParseClientAuth parses legacy client credentials with every legacy source
// enabled, because third-party clients still send them. See
// ParseClientAuthOptions for the exact order.
func ParseClientAuth(header http.Header, query url.Values) (ClientAuth, error) {
	return ParseClientAuthOptions(header, query, AuthOptions{Legacy: true})
}

// ParseClientAuthOptions is a pure parser; it performs no lookup. Order,
// taken from the upstream authorization context:
//
//  1. The Authorization header is used if non-empty; only when it is empty
//     (and Legacy is on) is the legacy authorization header consulted. A
//     non-matching Authorization value (for example a Bearer token) therefore
//     still shadows the legacy authorization header, as upstream does.
//  2. The chosen header must be "<scheme> k=v, ..." with the primary scheme,
//     or the legacy scheme when Legacy is on, compared case-insensitively.
//     Otherwise it yields no parameters (not an error). Parameter keys are
//     case-sensitive (Client, Device, DeviceId, Version, Token); a repeated
//     key keeps its last value; values are unquoted and URL-decoded.
//  3. The token is the first non-empty of: the Token parameter, the two bare
//     legacy token headers (Legacy only), the ApiKey query parameter, the
//     api_key query parameter (Legacy only). Query keys match
//     case-insensitively, as the upstream query collection does.
//
// Deliberate hardening over upstream: the selected token must be exactly the
// 43-character base64url session token form (otherwise ErrInvalidToken, with
// no fall-through to later sources); a token source or authorization header
// with more than one value is rejected instead of joined or truncated; raw
// and decoded values are length bounded and may not contain control
// characters or invalid UTF-8 (ErrMalformedAuth).
func ParseClientAuthOptions(header http.Header, query url.Values, opts AuthOptions) (ClientAuth, error) {
	raw, ok, err := authorizationValue(header, opts.Legacy)
	if err != nil {
		return ClientAuth{}, err
	}
	var auth ClientAuth
	if ok {
		params, err := authorizationParams(raw, opts.Legacy)
		if err != nil {
			return ClientAuth{}, err
		}
		for key, value := range params {
			if len(value) > MaxAuthFieldBytes || !utf8.ValidString(value) || hasControl(value) {
				return ClientAuth{}, ErrMalformedAuth
			}
			switch key {
			case "Client":
				auth.Client = value
			case "Device":
				auth.Device = value
			case "DeviceId":
				auth.DeviceID = value
			case "Version":
				auth.Version = value
			case "Token":
				if value != "" {
					auth.Token, auth.Source = value, TokenSourceAuthorization
				}
			}
		}
	}
	if auth.Token == "" && opts.Legacy {
		for _, name := range []string{headerLegacyToken, headerLegacyTokenAlt} {
			value, err := singleValue(header.Values(name))
			if err != nil {
				return withoutToken(auth), err
			}
			if value != "" {
				auth.Token, auth.Source = value, TokenSourceLegacyHeader
				break
			}
		}
	}
	if auth.Token == "" {
		value, err := singleValue(queryValues(query, "ApiKey"))
		if err != nil {
			return withoutToken(auth), err
		}
		if value != "" {
			auth.Token, auth.Source = value, TokenSourceQueryAPIKey
		}
	}
	if auth.Token == "" && opts.Legacy {
		value, err := singleValue(queryValues(query, "api_key"))
		if err != nil {
			return withoutToken(auth), err
		}
		if value != "" {
			auth.Token, auth.Source = value, TokenSourceQueryLegacyAPIKey
		}
	}
	if auth.Token != "" && !ValidToken(auth.Token) {
		return withoutToken(auth), ErrInvalidToken
	}
	return auth, nil
}

// ValidToken reports whether token is exactly the canonical unpadded
// base64url encoding of 32 bytes, the only token form this system issues.
func ValidToken(token string) bool {
	if len(token) != tokenLength {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(decoded) == tokenBytes
}

func withoutToken(auth ClientAuth) ClientAuth {
	auth.Token, auth.Source = "", TokenSourceNone
	return auth
}

// authorizationValue picks the raw parameterised header value.
func authorizationValue(header http.Header, legacy bool) (string, bool, error) {
	values := header.Values("Authorization")
	if strings.Join(values, ",") == "" && legacy {
		values = header.Values(headerLegacyAuthorization)
	}
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) > 1 {
		return "", false, ErrMalformedAuth
	}
	raw := values[0]
	if len(raw) > MaxAuthHeaderBytes || !utf8.ValidString(raw) {
		return "", false, ErrMalformedAuth
	}
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return "", false, ErrMalformedAuth
		}
	}
	return raw, true, nil
}

// authorizationParams splits off the scheme name at the first space. A
// missing space or an unknown scheme yields no parameters.
func authorizationParams(raw string, legacy bool) (map[string]string, error) {
	name, rest, found := strings.Cut(raw, " ")
	if !found {
		return nil, nil
	}
	if !strings.EqualFold(name, schemePrimary) && !(legacy && strings.EqualFold(name, schemeLegacy)) {
		return nil, nil
	}
	return splitParams(rest)
}

// splitParams follows the upstream part splitter exactly: a double quote
// toggles quoting, a comma outside quotes ends a value, an equals sign outside
// quotes ends a key (so the last unquoted equals sign wins), keys are trimmed
// of white space, values only of surrounding double quotes, and a value is
// only stored when it has at least one byte.
func splitParams(s string) (map[string]string, error) {
	result := map[string]string{}
	quoted := false
	start := 0
	key := ""
	store := func(end int) error {
		if start >= end {
			return nil
		}
		if _, exists := result[key]; !exists && len(result) == MaxAuthParams {
			return ErrMalformedAuth
		}
		result[key] = urlDecode(strings.Trim(s[start:end], `"`))
		return nil
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			quoted = !quoted
		case c == ',' && !quoted:
			if start < i {
				if err := store(i); err != nil {
					return nil, err
				}
				key = ""
			}
			start = i + 1
		case c == '=' && !quoted:
			key = strings.TrimSpace(s[start:i])
			start = i + 1
		}
	}
	if err := store(len(s)); err != nil {
		return nil, err
	}
	return result, nil
}

// urlDecode matches the lenient upstream decoder: '+' becomes a space, %XX
// with two hex digits becomes that byte, and any other '%' is kept literally.
// The caller rejects a result that is not valid UTF-8.
func urlDecode(s string) string {
	if !strings.ContainsAny(s, "%+") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '+':
			b.WriteByte(' ')
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	default:
		return c - 'a' + 10
	}
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

// singleValue rejects a token source carrying more than one value; upstream
// would join them with commas, which can never form a valid token.
func singleValue(values []string) (string, error) {
	switch len(values) {
	case 0:
		return "", nil
	case 1:
		return values[0], nil
	default:
		return "", ErrInvalidToken
	}
}

// queryValues gathers every value whose key matches name case-insensitively.
func queryValues(query url.Values, name string) []string {
	var out []string
	for key, values := range query {
		if strings.EqualFold(key, name) {
			out = append(out, values...)
		}
	}
	return out
}

// sensitiveQueryKeys are dropped by RedactQuery, compared case-insensitively.
var sensitiveQueryKeys = []string{"api_key", "apikey", "access_token", "token", "password", "pw"}

// RedactQuery returns a copy of query without credential-bearing parameters,
// for logging. The input is never modified; a nil input returns nil.
func RedactQuery(query url.Values) url.Values {
	if query == nil {
		return nil
	}
	out := make(url.Values, len(query))
	for key, values := range query {
		if sensitiveQueryKey(key) {
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	return out
}

func sensitiveQueryKey(key string) bool {
	key = strings.TrimSpace(key)
	for _, name := range sensitiveQueryKeys {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
