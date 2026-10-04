package domain

import (
	"crypto/sha1" //nolint:gosec // G505: RFC 6238 test vectors
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"hash"
	"net/url"
	"strings"
	"testing"
	"time"
)

// RFC 4226 appendix D: HOTP values of the ASCII key "12345678901234567890".
func TestHOTPRFC4226Vectors(t *testing.T) {
	key := []byte("12345678901234567890")
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for counter, code := range want {
		if got := HOTP(sha1.New, key, uint64(counter), 6); got != code {
			t.Errorf("HOTP(%d) = %s, want %s", counter, got, code)
		}
	}
}

// RFC 6238 appendix B: eight digit TOTP values for SHA1, SHA256 and SHA512
// with the 20, 32 and 64 byte ASCII seeds, period 30 s, T0 = 0.
func TestTOTPRFC6238Vectors(t *testing.T) {
	seeds := map[string]struct {
		key  string
		hash func() hash.Hash
	}{
		"SHA1":   {"12345678901234567890", sha1.New},
		"SHA256": {"12345678901234567890123456789012", sha256.New},
		"SHA512": {"1234567890123456789012345678901234567890123456789012345678901234", sha512.New},
	}
	vectors := []struct {
		unix int64
		want map[string]string
	}{
		{59, map[string]string{"SHA1": "94287082", "SHA256": "46119246", "SHA512": "90693936"}},
		{1111111109, map[string]string{"SHA1": "07081804", "SHA256": "68084774", "SHA512": "25091201"}},
		{1111111111, map[string]string{"SHA1": "14050471", "SHA256": "67062674", "SHA512": "99943326"}},
		{1234567890, map[string]string{"SHA1": "89005924", "SHA256": "91819424", "SHA512": "93441116"}},
		{2000000000, map[string]string{"SHA1": "69279037", "SHA256": "90698825", "SHA512": "38618901"}},
		{20000000000, map[string]string{"SHA1": "65353130", "SHA256": "77737706", "SHA512": "47863826"}},
	}
	for _, v := range vectors {
		step := TOTPStep(time.Unix(v.unix, 0))
		for name, seed := range seeds {
			if got := HOTP(seed.hash, []byte(seed.key), uint64(step), 8); got != v.want[name] {
				t.Errorf("%s at %d = %s, want %s", name, v.unix, got, v.want[name])
			}
		}
		// The six digit SHA1 code the server uses is the same value truncated.
		if got := TOTPCode([]byte(seeds["SHA1"].key), step); got != v.want["SHA1"][2:] {
			t.Errorf("six digit code at %d = %s", v.unix, got)
		}
	}
	if TOTPCode([]byte("k"), -1) != "" {
		t.Fatal("negative step produced a code")
	}
}

func TestMatchTOTPSkewAndReplay(t *testing.T) {
	key := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	step := TOTPStep(now)
	for _, offset := range []int64{-1, 0, 1} {
		got, ok := MatchTOTP(key, TOTPCode(key, step+offset), now, 0)
		if !ok || got != step+offset {
			t.Errorf("offset %d: %d %t", offset, got, ok)
		}
	}
	for _, offset := range []int64{-2, 2} {
		if _, ok := MatchTOTP(key, TOTPCode(key, step+offset), now, 0); ok {
			t.Errorf("offset %d accepted", offset)
		}
	}
	// A code is never accepted for a step at or before the last one used,
	// even inside the window.
	if _, ok := MatchTOTP(key, TOTPCode(key, step), now, step); ok {
		t.Fatal("replayed step accepted")
	}
	if _, ok := MatchTOTP(key, TOTPCode(key, step-1), now, step); ok {
		t.Fatal("older step accepted after a newer one")
	}
	if got, ok := MatchTOTP(key, TOTPCode(key, step+1), now, step); !ok || got != step+1 {
		t.Fatal("next step refused")
	}
	// Spaces are ignored; anything else that is not six digits is refused.
	code := TOTPCode(key, step)
	if _, ok := MatchTOTP(key, code[:3]+" "+code[3:], now, 0); !ok {
		t.Fatal("spaced code refused")
	}
	for _, bad := range []string{"", "12345", "1234567", "12345a", "１２３４５６", strings.Repeat(" ", 20) + code} {
		if _, ok := MatchTOTP(key, bad, now, 0); ok {
			t.Errorf("malformed %q accepted", bad)
		}
	}
	if _, ok := MatchTOTP([]byte("another key 12345678"), code, now, 0); ok {
		t.Fatal("code of another key accepted")
	}
}

func TestTOTPURI(t *testing.T) {
	key := []byte("12345678901234567890")
	uri := TOTPURI("ann&x=1/é", key)
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "otpauth" || u.Host != "totp" {
		t.Fatalf("uri %q: %v", uri, err)
	}
	if u.Path != "/Jelee:ann&x=1/é" || strings.Count(u.EscapedPath(), "/") != 1 {
		t.Fatalf("label %q / %q", u.Path, u.EscapedPath())
	}
	q := u.Query()
	if len(q) != 5 || q.Get("issuer") != "Jelee" || q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Fatalf("query %v", q)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(q.Get("secret"))
	if err != nil || string(secret) != string(key) || EncodeTOTPSecret(key) != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Fatalf("secret %q", q.Get("secret"))
	}
}

func TestSecretCodes(t *testing.T) {
	random := make([]byte, RecoveryCodeBytes)
	for i := range random {
		random[i] = byte(i*37 + 5)
	}
	code := FormatSecretCode(random)
	if len(code) != 19 || strings.Count(code, "-") != 3 {
		t.Fatalf("recovery code form %q", code)
	}
	user, other := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	digest := RecoveryCodeDigest(user, code)
	if len(digest) != 32 || string(RecoveryCodeDigest(user, strings.ToUpper(strings.ReplaceAll(code, "-", " ")))) != string(digest) {
		t.Fatal("recovery code normalization")
	}
	if string(RecoveryCodeDigest(other, code)) == string(digest) || RecoveryCodeDigest("bad", code) != nil {
		t.Fatal("recovery digest not bound to the user")
	}
	for _, bad := range []string{"", code[:18], code + "a", "0000-1111-0000-1111", "abcd_efgh_ijkl_mnop", strings.Repeat("a", 60)} {
		if RecoveryCodeDigest(user, bad) != nil {
			t.Errorf("malformed recovery code %q accepted", bad)
		}
	}
	app := FormatSecretCode(make([]byte, AppPasswordBytes))
	if !LooksLikeAppPassword(app) || LooksLikeAppPassword("correct horse battery staple") || AppPasswordDigest(user, "correct horse battery") != nil {
		t.Fatal("application password form")
	}
	if string(AppPasswordDigest(user, app)) == string(AppPasswordDigest(other, app)) || len(AppPasswordDigest(user, strings.ToUpper(app))) != 32 {
		t.Fatal("application password digest")
	}
	token := strings.Repeat("A", 43)
	if len(ChallengeDigest(token)) != 32 || ChallengeDigest(token[:42]) != nil || ChallengeDigest(strings.Repeat("+", 43)) != nil {
		t.Fatal("challenge digest")
	}
	for name, ok := range map[string]bool{"TV": true, "Living room 電視": true, "": false, " TV": false, "TV\n": false, strings.Repeat("a", 129): false, "\xff": false} {
		if ValidAppPasswordName(name) != ok {
			t.Errorf("name %q", name)
		}
	}
	if TOTPSealContext("ABC") != "jelee-totp-secret:v1:abc" {
		t.Fatal("seal context")
	}
}
