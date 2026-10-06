package logging

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
)

// Developer mode logging (G45.5). debug_sql_logging and debug_body_logging
// write statement text and request or response bodies, which the production
// whitelist always refuses. Those values pass only when three things hold:
// the value was built by DeveloperSQL or DeveloperBody (an ordinary string
// under the same key is still refused), it sits under the key its kind owns,
// and the router's switch for that kind, normally the developer mode toggle,
// reports true at the moment the record is formatted. Even then secret values
// are masked again here, whatever the call site already did.

// DevKind names the developer mode log a value belongs to.
type DevKind uint8

// The developer mode logs: debug_sql_logging and debug_body_logging.
const (
	DevSQL DevKind = iota + 1
	DevBody
)

// devValue is the marker type of developer mode text. Rendered outside the
// router (a plain handler, fmt) it stays redacted.
type devValue struct {
	kind DevKind
	text string
}

func (devValue) String() string { return redacted }

func (devValue) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// DeveloperSQL marks a statement written by the developer mode SQL log.
func DeveloperSQL(statement string) slog.Value {
	return slog.AnyValue(devValue{kind: DevSQL, text: statement})
}

// DeveloperBody marks a body summary written by the developer mode body log.
func DeveloperBody(summary string) slog.Value {
	return slog.AnyValue(devValue{kind: DevBody, text: summary})
}

// devKeys maps each developer mode attribute to the log that owns it. The
// codes of both logs are listed under "code" separately.
var devKeys = map[string]DevKind{
	"statement": DevSQL, "durationMicros": DevSQL, "rows": DevSQL, "failed": DevSQL,
	"route": DevBody, "requestBody": DevBody, "responseBody": DevBody,
}

var devCodes = map[string]DevKind{"devmode_sql_log": DevSQL, "devmode_body_log": DevBody}

type devSwitches struct{ sql, body func() bool }

func (s *devSwitches) on(kind DevKind) bool {
	if s == nil {
		return false
	}
	switch kind {
	case DevSQL:
		return s.sql != nil && s.sql()
	case DevBody:
		return s.body != nil && s.body()
	}
	return false
}

// developer applies the developer mode rules to a, reporting false when the
// attribute is not a developer mode field that may be written now.
func (r *redactor) developer(a slog.Attr) (slog.Attr, bool) {
	kind, ok := devKeys[a.Key]
	if !ok || !r.dev.Load().on(kind) {
		return a, false
	}
	switch a.Key {
	case "durationMicros", "rows":
		switch a.Value.Kind() {
		case slog.KindInt64, slog.KindUint64:
			return a, true
		}
	case "failed":
		if a.Value.Kind() == slog.KindBool {
			return a, true
		}
	case "route":
		if a.Value.Kind() == slog.KindString && (a.Value.String() == "" || safeToken(a.Value.String(), 256, "/{}_-.*:")) {
			return a, true
		}
	default:
		if v, ok := a.Value.Any().(devValue); ok && v.kind == kind {
			if kind == DevBody {
				return slog.String(a.Key, maskBody(v.text)), true
			}
			return slog.String(a.Key, MaskSecretText(v.text)), true
		}
	}
	return a, false
}

func (r *redactor) developerCode(value string) bool {
	kind, ok := devCodes[value]
	return ok && r.dev.Load().on(kind)
}

// Secret detection shared by developer mode logs. The key rules are those of
// the audit state redaction (internal/adapter/postgres/audit.go) plus the
// TOTP fields: keys are compared lowered with separators removed.
var secretKeys = map[string]bool{
	"authorization": true, "cookie": true, "setcookie": true, "dsn": true,
	"databaseurl": true, "connectionstring": true, "privatekey": true,
	"passwd": true, "pwd": true, "credential": true, "credentials": true, "csrf": true,
	"otp": true, "totp": true, "totpcode": true, "otpcode": true, "mfacode": true, "twofactorcode": true, "recoverycode": true, "recoverycodes": true, "uri": true,
}

var secretSuffixes = []string{"password", "passwordhash", "token", "tokenhash", "secret", "apikey", "key", "totp", "otp", "recoverycode", "recoverycodes"}

func normalizeKey(key string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '_', '-', '.', ' ':
			return -1
		}
		return r
	}, strings.ToLower(key))
}

// SecretKey reports whether values under key must never be logged.
func SecretKey(key string) bool {
	normalized := normalizeKey(key)
	if secretKeys[normalized] {
		return true
	}
	for _, suffix := range secretSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

// secretValue reports values that are secret whatever their key: password
// hashes, otpauth URIs (they carry the TOTP secret), authorization headers,
// connection strings with credentials, and the opaque token formats the
// server issues.
func secretValue(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	for _, prefix := range []string{"$argon2", "otpauth:", "bearer ", "basic ", "jdm_", "whsec_"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return userinfoPattern.MatchString(s)
}

// MaskSecrets replaces, in a decoded JSON value, every value under a secret
// key and every secret-looking string. A numeric "code" is a TOTP code. It
// changes v in place and returns it.
func MaskSecrets(v any) any {
	switch value := v.(type) {
	case map[string]any:
		for key, child := range value {
			if SecretKey(key) || (normalizeKey(key) == "code" && numericCode(child)) {
				value[key] = redacted
			} else {
				value[key] = MaskSecrets(child)
			}
		}
		return value
	case []any:
		for i, child := range value {
			value[i] = MaskSecrets(child)
		}
		return value
	case string:
		if secretValue(value) {
			return redacted
		}
		return MaskSecretText(value)
	}
	return v
}

// numericCode matches one-time codes: a JSON number or a string of 4 to 10
// digits. Error codes are identifiers and stay readable.
func numericCode(v any) bool {
	switch c := v.(type) {
	case float64, json.Number:
		return true
	case string:
		if len(c) < 4 || len(c) > 10 {
			return false
		}
		for _, d := range c {
			if d < '0' || d > '9' {
				return false
			}
		}
		return true
	}
	return false
}

var (
	// userinfoPattern finds URL credentials such as postgres://user:pw@host.
	userinfoPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s@'"]+@`)
	// assignmentPattern finds key=value and key: value pairs, quoted or not.
	assignmentPattern = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.-]*)(\s*[=:]\s*)('[^']*'|"[^"]*"|[^\s&,;)]+)`)
	// schemePattern finds Authorization header values.
	schemePattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	// passwordLiteralPattern finds SQL such as PASSWORD 'x'.
	passwordLiteralPattern = regexp.MustCompile(`(?i)\b(password|secret|token)\s+'[^']*'`)
	// placeholderPattern matches SQL parameter placeholders and the "?" of
	// a statement template, which carry no value and keep statements
	// readable.
	placeholderPattern = regexp.MustCompile(`^(\$[0-9]+|\?)$`)
	// opaquePattern finds the token formats the server issues and otpauth URIs.
	opaquePattern = regexp.MustCompile(`(?i)\b(jdm_[0-9a-f]+|whsec_[A-Za-z0-9_-]+|otpauth://\S+|\$argon2[^\s'"]*)`)
)

// MaskSecretText masks secrets inside free text such as a SQL statement:
// URL credentials, secret key=value pairs, Authorization schemes, password
// literals and issued token formats.
func MaskSecretText(s string) string {
	s = userinfoPattern.ReplaceAllString(s, "${1}"+redacted+"@")
	s = opaquePattern.ReplaceAllString(s, redacted)
	s = schemePattern.ReplaceAllString(s, "${1} "+redacted)
	s = passwordLiteralPattern.ReplaceAllString(s, "${1} '"+redacted+"'")
	return assignmentPattern.ReplaceAllStringFunc(s, func(m string) string {
		parts := assignmentPattern.FindStringSubmatch(m)
		if !SecretKey(parts[1]) || parts[3] == redacted || placeholderPattern.MatchString(parts[3]) {
			return m
		}
		return parts[1] + parts[2] + redacted
	})
}

// maskBody re-masks a body summary: JSON is decoded and masked again, any
// other text (the size markers the body log writes) is masked as text.
func maskBody(s string) string {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil || dec.More() {
		return MaskSecretText(s)
	}
	out, err := json.Marshal(MaskSecrets(v))
	if err != nil {
		return redacted
	}
	return string(out)
}
