// Package secretbox seals small secrets (webhook signing secrets and header
// values) with the environment master key so storage never holds them in
// plain text.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

// version prefixes every sealed value so the format, or the key it was
// sealed with, can change later without guessing.
const version byte = 1

// KeyBytes is the master key size (AES-256).
const KeyBytes = 32

// MaxPlaintext bounds what may be sealed.
const MaxPlaintext = 64 << 10

var (
	ErrKey    = errors.New("secretbox: invalid master key")
	ErrSealed = errors.New("secretbox: sealed value cannot be opened")
)

// Box is an AES-256-GCM sealer. The context string is authenticated
// additional data: a value sealed for one endpoint and purpose does not
// open for another.
type Box struct {
	aead cipher.AEAD
	// fingerprint identifies the key in sealed values without revealing it.
	fingerprint [4]byte
}

func (*Box) String() string   { return "secretbox (redacted)" }
func (*Box) GoString() string { return "secretbox (redacted)" }

// New copies nothing from key after deriving the cipher; callers should
// clear their copy when done.
func New(key []byte) (*Box, error) {
	if len(key) != KeyBytes {
		return nil, ErrKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrKey
	}
	b := &Box{aead: aead}
	sum := sha256.Sum256(append([]byte("jelee-secretbox-fingerprint:"), key...))
	copy(b.fingerprint[:], sum[:4])
	return b, nil
}

// Seal returns version || key fingerprint || nonce || ciphertext+tag.
func (b *Box) Seal(context string, plaintext []byte) ([]byte, error) {
	if len(plaintext) > MaxPlaintext {
		return nil, ErrSealed
	}
	header := 1 + len(b.fingerprint)
	out := make([]byte, header+b.aead.NonceSize(), header+b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	out[0] = version
	copy(out[1:header], b.fingerprint[:])
	nonce := out[header:]
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrSealed
	}
	return b.aead.Seal(out, nonce, plaintext, b.additional(context, out[:header])), nil
}

// Open reverses Seal. Every failure, including a different master key, is
// ErrSealed.
func (b *Box) Open(context string, sealed []byte) ([]byte, error) {
	header := 1 + len(b.fingerprint)
	if len(sealed) < header+b.aead.NonceSize()+b.aead.Overhead() || sealed[0] != version {
		return nil, ErrSealed
	}
	nonce := sealed[header : header+b.aead.NonceSize()]
	plain, err := b.aead.Open(nil, nonce, sealed[header+b.aead.NonceSize():], b.additional(context, sealed[:header]))
	if err != nil {
		return nil, ErrSealed
	}
	return plain, nil
}

func (b *Box) additional(context string, header []byte) []byte {
	return append(append([]byte(nil), header...), context...)
}
