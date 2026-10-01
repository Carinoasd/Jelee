// Package password hashes and verifies passwords with bounded Argon2id work.
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"io"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	MinPasswordBytes          = 12
	MaxPasswordBytes          = 1024
	MinMemoryKiB       uint32 = 19 * 1024
	MaxMemoryKiB       uint32 = 128 * 1024
	MinIterations      uint32 = 2
	MaxIterations      uint32 = 6
	MaxParallelism     uint8  = 4
	MaxConcurrentLimit        = 8
	saltBytes                 = 16
	keyBytes                  = 32
)

var (
	ErrInvalidConfig   = errors.New("password_invalid_config")
	ErrInvalidPassword = errors.New("password_invalid_input")
	ErrInvalidHash     = errors.New("password_invalid_hash")
	ErrInvalidContext  = errors.New("password_invalid_context")
	ErrEntropy         = errors.New("password_randomness_unavailable")
)

type Config struct {
	MemoryKiB     uint32 `json:"memoryKiB"`
	Iterations    uint32 `json:"iterations"`
	Parallelism   uint8  `json:"parallelism"`
	MaxConcurrent int    `json:"maxConcurrent"`
}

func DefaultConfig() Config {
	return Config{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, MaxConcurrent: 2}
}

func (config Config) Validate() error {
	if !validCosts(config.MemoryKiB, config.Iterations, uint32(config.Parallelism)) || config.MaxConcurrent < 1 || config.MaxConcurrent > MaxConcurrentLimit {
		return ErrInvalidConfig
	}
	return nil
}

type deriveKey func(password, salt []byte, iterations, memoryKiB uint32, parallelism uint8, keyLength uint32) []byte

// Hasher must be shared by all password operations in a process so its work
// budget applies across user creation, login, password changes and dummy work.
// Construct with New; the zero value intentionally rejects operations.
type Hasher struct {
	config       Config
	slots        chan struct{}
	dummyEncoded string
	entropy      io.Reader
	derive       deriveKey
}

func New(config Config) (*Hasher, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	hasher := &Hasher{config: config, slots: make(chan struct{}, config.MaxConcurrent), entropy: rand.Reader, derive: argon2.IDKey}
	// A random expected result makes the dummy PHC independent of a real user or
	// a hard-coded password. It is never used to authenticate anyone.
	var material [saltBytes + keyBytes]byte
	if _, err := io.ReadFull(hasher.entropy, material[:]); err != nil {
		return nil, ErrEntropy
	}
	hasher.dummyEncoded = encode(config, material[:saltBytes], material[saltBytes:])
	return hasher, nil
}

// ValidatePassword is the creation/change policy: 12..1024 UTF-8 bytes.
// Spaces, NUL, Unicode and all character compositions are accepted unchanged.
// It does not trim, normalize, impose character classes or count code points.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordBytes || len(password) > MaxPasswordBytes || !utf8.ValidString(password) {
		return ErrInvalidPassword
	}
	return nil
}

func validVerificationInput(password string) error {
	if len(password) > MaxPasswordBytes || !utf8.ValidString(password) {
		return ErrInvalidPassword
	}
	return nil
}

func (h *Hasher) usable(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if h == nil || h.slots == nil || h.entropy == nil || h.derive == nil {
		return ErrInvalidConfig
	}
	return nil
}

func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if err := h.usable(ctx); err != nil {
		return "", err
	}
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	var salt [saltBytes]byte
	if _, err := io.ReadFull(h.entropy, salt[:]); err != nil {
		return "", ErrEntropy
	}
	key, err := h.deriveBounded(ctx, password, salt[:], h.config)
	if err != nil {
		return "", err
	}
	defer clear(key)
	return encode(h.config, salt[:], key), nil
}

// Verify treats a wrong password as (false,nil). It accepts short/empty guesses
// so they still consume normal verification work; only creation enforces 12
// bytes. Invalid UTF-8, oversized input or a malformed/unsafe PHC return errors.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	if err := h.usable(ctx); err != nil {
		return false, err
	}
	if err := validVerificationInput(password); err != nil {
		return false, err
	}
	stored, err := parse(encoded)
	if err != nil {
		return false, err
	}
	key, err := h.deriveBounded(ctx, password, stored.salt, stored.config)
	if err != nil {
		return false, err
	}
	defer clear(key)
	return subtle.ConstantTimeCompare(key, stored.key) == 1, nil
}

// DummyVerify performs the same algorithm, configured work and comparison as a
// current-policy verification, but discards the match result. Call it for unknown
// users before returning the same public authentication failure as a wrong
// password. It does not promise identical timing across database/cache paths or
// hashes with older, different cost parameters.
func (h *Hasher) DummyVerify(ctx context.Context, password string) error {
	if err := h.usable(ctx); err != nil {
		return err
	}
	_, err := h.Verify(ctx, password, h.dummyEncoded)
	return err
}

func (h *Hasher) deriveBounded(ctx context.Context, password string, salt []byte, config Config) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Both select cases may be ready together. Never start work for an already
	// cancelled waiter just because a slot also became available.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input := []byte(password)
	defer clear(input)
	// IDKey has no context API. Run synchronously and retain the quota until it
	// finishes; returning early with a detached goroutine would break the budget.
	key := h.derive(input, salt, config.Iterations, config.MemoryKiB, config.Parallelism, keyBytes)
	if err := ctx.Err(); err != nil {
		clear(key)
		return nil, err
	}
	return key, nil
}

// NeedsRehash only recommends an upgrade if neither memory nor iteration cost
// would decrease. Changed parallelism alone, mixed cost tradeoffs and invalid
// hashes require an explicit policy decision, so they return false.
func (h *Hasher) NeedsRehash(encoded string) bool {
	if h == nil || h.slots == nil {
		return false
	}
	stored, err := parse(encoded)
	if err != nil {
		return false
	}
	return h.config.MemoryKiB >= stored.config.MemoryKiB && h.config.Iterations >= stored.config.Iterations &&
		(h.config.MemoryKiB > stored.config.MemoryKiB || h.config.Iterations > stored.config.Iterations)
}
