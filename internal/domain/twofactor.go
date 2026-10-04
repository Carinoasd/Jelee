package domain

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // G505: RFC 6238 authenticator apps use HMAC-SHA1; HMAC keeps it sound
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"hash"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Optional time-based second factor (G07.8). Web logins of an enrolled
// account need a second step; clients that cannot ask for a code (native
// devices, the compatibility layer) sign in with an application password
// created from a web session that completed the second step.
var (
	// ErrSecondFactorRequired refuses a native or compatibility login that
	// presented the account password of an enrolled account: such logins
	// must use an application password. Returned only after the password
	// verified, like ErrNativeLoginDisabled.
	ErrSecondFactorRequired = errors.New("application password required")
	// ErrSecondFactorMismatch is a wrong, reused or expired code.
	ErrSecondFactorMismatch = errors.New("second factor code does not match")
	// ErrChallengeInvalid is an unknown, used, expired or exhausted login
	// challenge; the login must start again with the password.
	ErrChallengeInvalid = errors.New("login challenge invalid")
	// ErrSecondFactorUnavailable means no master key is configured, so
	// authenticator secrets can be neither sealed nor opened.
	ErrSecondFactorUnavailable = errors.New("second factor unavailable")
)

const (
	// TOTPSecretBytes is the size of a generated authenticator secret (160 bits,
	// the HMAC-SHA1 block recommendation of RFC 4226).
	TOTPSecretBytes = 20
	// TOTPPeriod and TOTPDigits are the parameters every common
	// authenticator app uses by default.
	TOTPPeriod = 30 * time.Second
	// TOTPDigits is the code length.
	TOTPDigits = 6
	// TOTPSkew is the accepted drift in time steps on either side.
	TOTPSkew = 1
	// RecoveryCodeCount codes are issued at once; each works once.
	RecoveryCodeCount = 10
	// ChallengeTTL bounds the second login step.
	ChallengeTTL = 5 * time.Minute
	// ChallengeAttempts bounds the codes tried against one challenge.
	ChallengeAttempts = 5
	// AppPasswordLimit bounds a user's active application passwords.
	AppPasswordLimit = 20
	// AppPasswordNameMax bounds an application password label in UTF-8 bytes.
	AppPasswordNameMax = 128
	// TOTPIssuer names the service in authenticator apps.
	TOTPIssuer = "Jelee"

	recoveryCodeChars = 16
	appPasswordChars  = 32
)

// TwoFactorStatus is a user's second factor state. It never carries the
// secret.
type TwoFactorStatus struct {
	// Available is false when the server has no master key: enrollment
	// is refused and authenticator codes cannot be checked.
	Available bool       `json:"available"`
	Enabled   bool       `json:"enabled"`
	EnabledAt *time.Time `json:"enabledAt,omitempty"`
	// Pending reports an enrollment waiting for its confirming code.
	Pending                bool `json:"pending"`
	RecoveryCodesRemaining int  `json:"recoveryCodesRemaining"`
}

// TwoFactorEnrollment is returned once, when enrollment starts.
type TwoFactorEnrollment struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

// RecoveryCodes are shown once; storage keeps only digests.
type RecoveryCodes struct {
	Codes []string `json:"recoveryCodes"`
}

// AppPassword is an application password record. The password itself is
// returned only by creation.
type AppPassword struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// NewAppPassword is the creation result.
type NewAppPassword struct {
	AppPassword AppPassword `json:"appPassword"`
	Password    string      `json:"password"`
}

// LoginChallenge is the second login step handed to the web client. Token is
// a bearer secret for ChallengeTTL; storage keeps its digest.
type LoginChallenge struct {
	Token     string    `json:"challenge"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// TOTPVerifier checks a code against a user's sealed secret and the last
// accepted time step. It returns the matched step, which must exceed
// lastStep, or ErrSecondFactorMismatch. Stores call it inside their
// transaction so the step is recorded atomically with the check.
type TOTPVerifier func(userID string, sealed []byte, lastStep int64) (int64, error)

// SecondFactorInput completes a web login challenge. Exactly one of Verify
// (an authenticator code) and RecoveryDigest (the digest of a recovery code
// for the challenge's user, nil when it cannot be one) is set: the user is
// known only from the challenge, inside the store's transaction.
type SecondFactorInput struct {
	ChallengeDigest []byte
	Verify          TOTPVerifier
	RecoveryDigest  func(userID string) []byte
	IP              string
	MaxSessions     int
	SessionTTL      time.Duration
	LockAfter       int
	LockFor         time.Duration
}

// HOTP is RFC 4226 with a selectable HMAC hash, exposed for the RFC test
// vectors. digits is 6 to 8.
func HOTP(h func() hash.Hash, key []byte, counter uint64, digits int) string {
	mac := hmac.New(h, key)
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	mac.Write(message[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	modulus := uint32(1)
	for range digits {
		modulus *= 10
	}
	text := strconv.FormatUint(uint64(value%modulus), 10)
	return strings.Repeat("0", digits-len(text)) + text
}

// TOTPStep is the RFC 6238 time step of t for the default period.
func TOTPStep(t time.Time) int64 {
	return t.Unix() / int64(TOTPPeriod/time.Second)
}

// TOTPCode is the six digit HMAC-SHA1 code of a time step.
func TOTPCode(key []byte, step int64) string {
	if step < 0 {
		return ""
	}
	return HOTP(sha1.New, key, uint64(step), TOTPDigits)
}

// MatchTOTP accepts code for the steps around now (TOTPSkew either side) that
// are later than lastStep, so a code once accepted never works again. It
// compares every candidate in constant time and returns the earliest
// matching step.
func MatchTOTP(key []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	code, ok := NormalizeTOTPCode(code)
	if !ok {
		return 0, false
	}
	current := TOTPStep(now)
	matched, found := int64(0), false
	for step := current - TOTPSkew; step <= current+TOTPSkew; step++ {
		candidate := TOTPCode(key, step)
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 && step > lastStep && !found {
			matched, found = step, true
		}
	}
	return matched, found
}

// NormalizeTOTPCode drops spaces and requires exactly six ASCII digits.
func NormalizeTOTPCode(code string) (string, bool) {
	if len(code) > 16 {
		return "", false
	}
	code = strings.ReplaceAll(code, " ", "")
	if len(code) != TOTPDigits {
		return "", false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return code, true
}

var base32Text = base32.StdEncoding.WithPadding(base32.NoPadding)

// EncodeTOTPSecret is the unpadded upper-case base32 form authenticator apps
// expect.
func EncodeTOTPSecret(key []byte) string { return base32Text.EncodeToString(key) }

// TOTPURI builds the otpauth URI of an authenticator secret for account.
func TOTPURI(account string, key []byte) string {
	label := escapeURIComponent(TOTPIssuer) + ":" + escapeURIComponent(account)
	return "otpauth://totp/" + label + "?secret=" + EncodeTOTPSecret(key) + "&issuer=" + escapeURIComponent(TOTPIssuer) +
		"&algorithm=SHA1&digits=" + strconv.Itoa(TOTPDigits) + "&period=" + strconv.Itoa(int(TOTPPeriod/time.Second))
}

// escapeURIComponent percent-encodes everything but RFC 3986 unreserved
// characters, so a user name cannot add a parameter or a path segment.
func escapeURIComponent(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// lowerBase32 is the alphabet of recovery codes and application passwords:
// lower-case RFC 4648 base32, which has no 0/O or 1/I/L confusion.
const lowerBase32 = "abcdefghijklmnopqrstuvwxyz234567"

// FormatSecretCode turns random bytes into groups of four lower-case base32
// characters; it uses five bits of each byte, so chars bytes give chars
// characters of 5 bits each.
func FormatSecretCode(random []byte) string {
	var b strings.Builder
	for i, c := range random {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(lowerBase32[c&31])
	}
	return b.String()
}

// RecoveryCodeBytes and AppPasswordBytes are the random bytes FormatSecretCode
// needs: 80 and 160 bits.
const (
	RecoveryCodeBytes = recoveryCodeChars
	AppPasswordBytes  = appPasswordChars
)

// normalizeSecretCode accepts the displayed form, upper or lower case, with
// or without hyphens and spaces.
func normalizeSecretCode(value string, chars int) (string, bool) {
	if len(value) > 2*chars+8 {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c == '-' || c == ' ':
			continue
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
		case c >= 'a' && c <= 'z' || c >= '2' && c <= '7':
		default:
			return "", false
		}
		b.WriteByte(c)
	}
	return b.String(), b.Len() == chars
}

// RecoveryCodeDigest binds a recovery code to its user. It is nil when the
// value cannot be a recovery code.
func RecoveryCodeDigest(userID, code string) []byte {
	normalized, ok := normalizeSecretCode(code, recoveryCodeChars)
	if !ok || !ValidID(userID) {
		return nil
	}
	sum := sha256.Sum256([]byte("jelee-recovery-code:v1:" + strings.ToLower(userID) + ":" + normalized))
	return sum[:]
}

// AppPasswordDigest binds an application password to its user. It is nil
// when the value cannot be an application password, so an ordinary account
// password is never looked up as one.
func AppPasswordDigest(userID, password string) []byte {
	normalized, ok := normalizeSecretCode(password, appPasswordChars)
	if !ok || !ValidID(userID) {
		return nil
	}
	sum := sha256.Sum256([]byte("jelee-app-password:v1:" + strings.ToLower(userID) + ":" + normalized))
	return sum[:]
}

// LooksLikeAppPassword reports the application password form.
func LooksLikeAppPassword(value string) bool {
	_, ok := normalizeSecretCode(value, appPasswordChars)
	return ok
}

// ChallengeDigest is the stored form of a login challenge token.
func ChallengeDigest(token string) []byte {
	if len(token) != 43 {
		return nil
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return nil
		}
	}
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// TOTPSealContext binds a sealed authenticator secret to its user and
// purpose: a sealed value copied to another user does not open.
func TOTPSealContext(userID string) string { return "jelee-totp-secret:v1:" + strings.ToLower(userID) }

// ValidAppPasswordName bounds a label: 1–128 UTF-8 bytes, no control
// characters, no surrounding spaces.
func ValidAppPasswordName(name string) bool {
	if name == "" || len(name) > AppPasswordNameMax || strings.TrimSpace(name) != name || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
