package secretbox

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func key(fill byte) []byte { return bytes.Repeat([]byte{fill}, KeyBytes) }

func TestSealOpenRoundTripAndBinding(t *testing.T) {
	box, err := New(key(1))
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("whsec_plain-secret-material")
	sealed, err := box.Seal("jelee-webhook-secret:a", secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, secret) || sealed[0] != version || len(sealed) != 1+4+12+len(secret)+16 {
		t.Fatalf("sealed form %x", sealed)
	}
	again, _ := box.Seal("jelee-webhook-secret:a", secret)
	if bytes.Equal(again, sealed) {
		t.Fatal("nonce reused")
	}
	plain, err := box.Open("jelee-webhook-secret:a", sealed)
	if err != nil || !bytes.Equal(plain, secret) {
		t.Fatalf("open %q %v", plain, err)
	}
	other, _ := New(key(2))
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	truncated := sealed[:20]
	versioned := append([]byte{9}, sealed[1:]...)
	for name, open := range map[string]func() ([]byte, error){
		"other context": func() ([]byte, error) { return box.Open("jelee-webhook-secret:b", sealed) },
		"other key":     func() ([]byte, error) { return other.Open("jelee-webhook-secret:a", sealed) },
		"tampered":      func() ([]byte, error) { return box.Open("jelee-webhook-secret:a", tampered) },
		"truncated":     func() ([]byte, error) { return box.Open("jelee-webhook-secret:a", truncated) },
		"version":       func() ([]byte, error) { return box.Open("jelee-webhook-secret:a", versioned) },
	} {
		if plain, err := open(); !errors.Is(err, ErrSealed) || plain != nil {
			t.Errorf("%s opened: %v", name, err)
		}
	}
	if text := fmt.Sprintf("%v %#v %+v", box, box, box); bytes.Contains([]byte(text), key(1)[:4]) || text != "secretbox (redacted) secretbox (redacted) secretbox (redacted)" {
		t.Fatalf("box prints %q", text)
	}
}

func TestNewRejectsBadKeys(t *testing.T) {
	for _, k := range [][]byte{nil, key(1)[:16], append(key(1), 0)} {
		if _, err := New(k); !errors.Is(err, ErrKey) {
			t.Errorf("key of %d bytes accepted", len(k))
		}
	}
	box, _ := New(key(1))
	if _, err := box.Seal("c", make([]byte, MaxPlaintext+1)); err == nil {
		t.Fatal("oversized plaintext sealed")
	}
}
