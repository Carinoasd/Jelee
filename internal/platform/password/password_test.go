package password

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func minimumConfig() Config {
	return Config{MemoryKiB: MinMemoryKiB, Iterations: MinIterations, Parallelism: 1, MaxConcurrent: 1}
}

func newTestHasher(t *testing.T, config Config) *Hasher {
	t.Helper()
	hasher, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return hasher
}

func TestCreationPolicyPreservesUserInput(t *testing.T) {
	for _, input := range []string{strings.Repeat("a", 12), strings.Repeat("a", 1024), strings.Repeat(" ", 12), "密碼短語測試", "hello\x00world!", "  long password  "} {
		if err := ValidatePassword(input); err != nil {
			t.Errorf("valid password rejected: length=%d err=%v", len(input), err)
		}
	}
	for _, input := range []string{"", strings.Repeat("a", 11), strings.Repeat("a", 1025), string([]byte{0xff}) + strings.Repeat("a", 15), "abc\xc0\x80defghijkl"} {
		if err := ValidatePassword(input); !errors.Is(err, ErrInvalidPassword) {
			t.Errorf("invalid password accepted: length=%d", len(input))
		}
	}
}

func TestConfigurationBoundsAndDefault(t *testing.T) {
	want := Config{MemoryKiB: 65536, Iterations: 3, Parallelism: 2, MaxConcurrent: 2}
	if got := DefaultConfig(); got != want {
		t.Fatalf("default=%#v", got)
	}
	for _, config := range []Config{minimumConfig(), DefaultConfig(), {MemoryKiB: MaxMemoryKiB, Iterations: MaxIterations, Parallelism: MaxParallelism, MaxConcurrent: MaxConcurrentLimit}} {
		if _, err := New(config); err != nil {
			t.Errorf("valid configuration rejected: %#v: %v", config, err)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"memory_low", func(c *Config) { c.MemoryKiB = MinMemoryKiB - 1 }},
		{"memory_high", func(c *Config) { c.MemoryKiB = MaxMemoryKiB + 1 }},
		{"iterations_low", func(c *Config) { c.Iterations = 1 }},
		{"iterations_high", func(c *Config) { c.Iterations = MaxIterations + 1 }},
		{"parallelism_zero", func(c *Config) { c.Parallelism = 0 }},
		{"parallelism_high", func(c *Config) { c.Parallelism = 255 }},
		{"concurrency_zero", func(c *Config) { c.MaxConcurrent = 0 }},
		{"concurrency_high", func(c *Config) { c.MaxConcurrent = MaxConcurrentLimit + 1 }},
		{"concurrency_negative", func(c *Config) { c.MaxConcurrent = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			tc.change(&config)
			if _, err := New(config); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("config err=%v", err)
			}
		})
	}
}

func TestHashUsesRandomSaltAndInteroperablePHC(t *testing.T) {
	hasher := newTestHasher(t, minimumConfig())
	password := "correct horse battery staple"
	first, err := hasher.Hash(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hasher.Hash(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("reused hash/salt for identical passwords")
	}
	for _, encoded := range []string{first, second} {
		parts := strings.Split(encoded, "$")
		if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=19456,t=2,p=1" {
			t.Fatalf("noncanonical PHC: %q", encoded)
		}
		salt, err := base64.RawStdEncoding.DecodeString(parts[4])
		if err != nil || len(salt) != 16 {
			t.Fatal("salt format")
		}
		key, err := base64.RawStdEncoding.DecodeString(parts[5])
		if err != nil || len(key) != 32 {
			t.Fatal("key format")
		}
		// Verify interoperability directly against the official library, rather
		// than allowing matching encoder/decoder bugs to mask each other.
		want := argon2.IDKey([]byte(password), salt, 2, 19456, 1, 32)
		if !bytes.Equal(key, want) {
			t.Fatal("PHC does not encode the actual Argon2id output")
		}
		matched, err := hasher.Verify(context.Background(), password, encoded)
		if err != nil || !matched {
			t.Fatalf("correct password rejected: %v", err)
		}
	}
	for _, wrong := range []string{"", "wrong", "incorrect password"} {
		matched, err := hasher.Verify(context.Background(), wrong, first)
		if err != nil || matched {
			t.Fatalf("wrong password result: match=%v err=%v", matched, err)
		}
	}
}

func TestPasswordsAreNotTrimmedNormalizedOrTruncated(t *testing.T) {
	hasher := newTestHasher(t, minimumConfig())
	for _, tc := range []struct{ name, original, changed string }{
		{"spaces", "  a long password  ", "a long password"},
		{"unicode", "caf\u00e9 password", "cafe\u0301 password"},
		{"nul", "long secret\x00tail", "long secret"},
		{"maximum", strings.Repeat("a", 1024), strings.Repeat("a", 1023) + "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := hasher.Hash(context.Background(), tc.original)
			if err != nil {
				t.Fatal(err)
			}
			if matched, err := hasher.Verify(context.Background(), tc.original, encoded); err != nil || !matched {
				t.Fatalf("unchanged password rejected: %v", err)
			}
			if matched, err := hasher.Verify(context.Background(), tc.changed, encoded); err != nil || matched {
				t.Fatalf("changed password matched: %v", err)
			}
		})
	}
}

func canonicalFixture(config Config) string {
	return encode(config, make([]byte, saltBytes), make([]byte, keyBytes))
}

func TestMalformedPHCNeverStartsKDF(t *testing.T) {
	hasher := newTestHasher(t, minimumConfig())
	var calls atomic.Int32
	hasher.derive = func([]byte, []byte, uint32, uint32, uint8, uint32) []byte {
		calls.Add(1)
		return make([]byte, keyBytes)
	}
	valid := canonicalFixture(minimumConfig())
	parts := strings.Split(valid, "$")
	cases := map[string]string{
		"empty": "", "huge": strings.Repeat("$", 1<<20), "algorithm": strings.Replace(valid, "argon2id", "argon2i", 1),
		"algorithm_case": strings.Replace(valid, "argon2id", "Argon2id", 1), "version": strings.Replace(valid, "v=19", "v=16", 1),
		"version_leading_zero": strings.Replace(valid, "v=19", "v=019", 1), "trailing": valid + "$", "prefix": " " + valid,
		"missing_salt":           strings.Replace(valid, "$"+parts[4]+"$", "$$", 1),
		"salt_padding":           strings.Replace(valid, parts[4], parts[4]+"==", 1),
		"salt_noncanonical_bits": strings.Replace(valid, "$"+parts[4]+"$", "$"+parts[4][:21]+"B$", 1),
		"hash_noncanonical_bits": valid[:len(valid)-1] + "B",
		"hash_newline":           valid[:len(valid)-1] + "\n",
		"hash_short":             valid[:len(valid)-1],
		"hash_bad_alphabet":      valid[:len(valid)-1] + "-",
		"nul":                    valid[:len(valid)-1] + "\x00",
	}
	for _, parameter := range []string{
		"m=0,t=2,p=1", "m=19455,t=2,p=1", "m=131073,t=2,p=1", "m=4294967296,t=2,p=1", "m=999999999999999,t=2,p=1",
		"m=019456,t=2,p=1", "m=+19456,t=2,p=1", "m=-19456,t=2,p=1", "m=19456,t=0,p=1", "m=19456,t=1,p=1", "m=19456,t=7,p=1",
		"m=19456,t=2,p=0", "m=19456,t=2,p=5", "m=19456,t=2,p=256", "m=19456,t=2,p=4294967296",
		"m=19456,t=2,p=1,p=2", "t=2,m=19456,p=1", "m=19456,t=2", "m=19456, t=2,p=1", "m=１９４５６,t=2,p=1",
	} {
		cases[parameter] = strings.Replace(valid, "m=19456,t=2,p=1", parameter, 1)
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			matched, err := hasher.Verify(context.Background(), "a valid password", encoded)
			if matched || !errors.Is(err, ErrInvalidHash) || calls.Load() != 0 {
				t.Fatalf("invalid PHC reached KDF: matched=%v err=%v calls=%d", matched, err, calls.Load())
			}
			if err != nil && err.Error() != ErrInvalidHash.Error() {
				t.Fatal("error leaked stored hash text")
			}
		})
	}
}

func TestVerificationAcceptsBoundedStoredParameters(t *testing.T) {
	hasher := newTestHasher(t, minimumConfig())
	for _, config := range []Config{minimumConfig(), DefaultConfig(), {MemoryKiB: MaxMemoryKiB, Iterations: MaxIterations, Parallelism: MaxParallelism, MaxConcurrent: 1}} {
		hasher.derive = func(_ []byte, salt []byte, iterations, memory uint32, parallelism uint8, length uint32) []byte {
			if memory != config.MemoryKiB || iterations != config.Iterations || parallelism != config.Parallelism || len(salt) != saltBytes || length != keyBytes {
				t.Fatal("stored costs were truncated or replaced with current configuration")
			}
			return make([]byte, keyBytes)
		}
		matched, err := hasher.Verify(context.Background(), "password", canonicalFixture(config))
		if err != nil || !matched {
			t.Fatalf("bounded stored costs rejected: %v", err)
		}
	}
}

func TestInvalidInputIsRejectedBeforeKDF(t *testing.T) {
	hasher := newTestHasher(t, minimumConfig())
	hasher.derive = func([]byte, []byte, uint32, uint32, uint8, uint32) []byte {
		t.Fatal("invalid input started KDF")
		return nil
	}
	for _, input := range []string{strings.Repeat("x", 1<<20), "\xff" + strings.Repeat("a", 12)} {
		if _, err := hasher.Hash(context.Background(), input); !errors.Is(err, ErrInvalidPassword) {
			t.Fatal(err)
		}
		if _, err := hasher.Verify(context.Background(), input, canonicalFixture(minimumConfig())); !errors.Is(err, ErrInvalidPassword) {
			t.Fatal(err)
		}
		if err := hasher.DummyVerify(context.Background(), input); !errors.Is(err, ErrInvalidPassword) {
			t.Fatal(err)
		}
	}
	if _, err := hasher.Hash(context.Background(), "short"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatal(err)
	}
	if _, err := hasher.Hash(nil, "long valid password"); !errors.Is(err, ErrInvalidContext) {
		t.Fatal(err)
	}
	var zero Hasher
	if _, err := zero.Hash(context.Background(), "long valid password"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	var absent *Hasher
	if err := absent.DummyVerify(context.Background(), "guess"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
}

func TestDummyUsesCurrentCostsAndNeverAuthenticates(t *testing.T) {
	hasher := newTestHasher(t, DefaultConfig())
	called := 0
	hasher.derive = func(_ []byte, salt []byte, iterations, memory uint32, parallelism uint8, length uint32) []byte {
		called++
		if iterations != 3 || memory != 65536 || parallelism != 2 || len(salt) != saltBytes || length != keyBytes {
			t.Fatal("dummy used different configured work")
		}
		return make([]byte, keyBytes)
	}
	for _, guess := range []string{"", "short", "a valid password"} {
		if err := hasher.DummyVerify(context.Background(), guess); err != nil {
			t.Fatal(err)
		}
	}
	if called != 3 || len(hasher.slots) != 0 {
		t.Fatal("dummy skipped work or leaked quota")
	}
	other := newTestHasher(t, DefaultConfig())
	if hasher.dummyEncoded == other.dummyEncoded {
		t.Fatal("dummy material reused across instances")
	}
}

func TestWaitingOperationsCancelAndReleaseQuota(t *testing.T) {
	hasher := newTestHasher(t, minimumConfig())
	hasher.derive = func([]byte, []byte, uint32, uint32, uint8, uint32) []byte { return make([]byte, keyBytes) }
	for _, name := range []string{"hash", "verify", "dummy"} {
		t.Run(name, func(t *testing.T) {
			hasher.slots <- struct{}{}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() {
				switch name {
				case "hash":
					_, err := hasher.Hash(ctx, "a valid password")
					result <- err
				case "verify":
					_, err := hasher.Verify(ctx, "guess", canonicalFixture(minimumConfig()))
					result <- err
				case "dummy":
					result <- hasher.DummyVerify(ctx, "guess")
				}
			}()
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("waiting request did not cancel")
			}
			if len(hasher.slots) != 1 {
				t.Fatal("cancelled waiter consumed or released another operation's quota")
			}
			<-hasher.slots
			if err := hasher.DummyVerify(context.Background(), "guess"); err != nil || len(hasher.slots) != 0 {
				t.Fatal("quota not reusable after cancellation")
			}
		})
	}
}

func TestActiveKDFRetainsQuotaUntilSynchronousCompletion(t *testing.T) {
	hasher := newTestHasher(t, minimumConfig())
	entered, finish := make(chan struct{}), make(chan struct{})
	var inputCopy, derived []byte
	hasher.derive = func(input []byte, _ []byte, _, _ uint32, _ uint8, _ uint32) []byte {
		inputCopy = input
		close(entered)
		<-finish
		derived = bytes.Repeat([]byte{7}, keyBytes)
		return derived
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		encoded, err := hasher.Hash(ctx, "a valid password")
		if encoded != "" {
			result <- errors.New("cancelled operation returned a hash")
			return
		}
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("KDF did not start")
	}
	cancel()
	if len(hasher.slots) != 1 {
		t.Fatal("active KDF quota released before completion")
	}
	select {
	case err := <-result:
		t.Fatalf("KDF detached after cancellation: %v", err)
	default:
	}
	close(finish)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("completed cancelled KDF did not exit")
	}
	if len(hasher.slots) != 0 || !bytes.Equal(inputCopy, make([]byte, len(inputCopy))) || !bytes.Equal(derived, make([]byte, keyBytes)) {
		t.Fatal("quota or temporary key material remained after cancellation")
	}
}

func TestConcurrentOperationsShareOneBudget(t *testing.T) {
	config := minimumConfig()
	config.MaxConcurrent = 2
	hasher := newTestHasher(t, config)
	var active, maximum atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	hasher.derive = func([]byte, []byte, uint32, uint32, uint8, uint32) []byte {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := maximum.Load(); current > previous; previous = maximum.Load() {
			if maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		return make([]byte, keyBytes)
	}
	var workers sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); results <- hasher.DummyVerify(context.Background(), "guess") }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("parallel work did not start")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := hasher.Hash(ctx, "a valid password"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third operation bypassed budget: %v", err)
	}
	close(release)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if maximum.Load() != 2 || active.Load() != 0 || len(hasher.slots) != 0 {
		t.Fatalf("budget maximum=%d active=%d quota=%d", maximum.Load(), active.Load(), len(hasher.slots))
	}
}

type failedEntropy struct{}

func (failedEntropy) Read([]byte) (int, error) {
	return 0, errors.New("sensitive entropy provider details")
}

func TestEntropyFailureIsSafeAndDoesNotConsumeQuota(t *testing.T) {
	hasher := newTestHasher(t, minimumConfig())
	hasher.entropy = failedEntropy{}
	if encoded, err := hasher.Hash(context.Background(), "a valid password"); encoded != "" || !errors.Is(err, ErrEntropy) || strings.Contains(err.Error(), "sensitive") || len(hasher.slots) != 0 {
		t.Fatalf("unsafe entropy failure: %v", err)
	}
	// Truncated entropy also fails, rather than generating a partially random salt.
	hasher.entropy = io.LimitReader(bytes.NewReader(make([]byte, 15)), 15)
	if _, err := hasher.Hash(context.Background(), "a valid password"); !errors.Is(err, ErrEntropy) {
		t.Fatal(err)
	}
}

func TestRehashNeverRecommendsLoweringEitherCost(t *testing.T) {
	hasher := newTestHasher(t, DefaultConfig())
	for _, tc := range []struct {
		config Config
		want   bool
	}{
		{minimumConfig(), true}, {DefaultConfig(), false},
		{Config{MemoryKiB: MaxMemoryKiB, Iterations: 6, Parallelism: 4}, false},
		{Config{MemoryKiB: MinMemoryKiB, Iterations: 6, Parallelism: 1}, false},
		{Config{MemoryKiB: MaxMemoryKiB, Iterations: 2, Parallelism: 1}, false},
		{Config{MemoryKiB: 65536, Iterations: 3, Parallelism: 1}, false},
	} {
		if got := hasher.NeedsRehash(canonicalFixture(tc.config)); got != tc.want {
			t.Fatalf("rehash %#v=%v", tc.config, got)
		}
	}
	if hasher.NeedsRehash("corrupt") {
		t.Fatal("corrupt hash recommended an automatic replacement")
	}
}

func FuzzPHCParserBounds(f *testing.F) {
	for _, seed := range []string{canonicalFixture(minimumConfig()), canonicalFixture(DefaultConfig()), "$argon2id$v=19$m=4294967295,t=0,p=255$$", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, encoded string) {
		stored, err := parse(encoded)
		if err != nil {
			return
		}
		// Accepted storage values must be canonical and safe before callers can
		// allocate KDF memory. Fuzzing deliberately never invokes expensive Argon2.
		if len(stored.salt) != 16 || len(stored.key) != 32 || !validCosts(stored.config.MemoryKiB, stored.config.Iterations, uint32(stored.config.Parallelism)) || encode(stored.config, stored.salt, stored.key) != encoded {
			t.Fatal("accepted unsafe or noncanonical PHC")
		}
	})
}

func BenchmarkHashDefault(b *testing.B) {
	hasher, err := New(DefaultConfig())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := hasher.Hash(context.Background(), "benchmark password phrase"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyDefaultConcurrent(b *testing.B) {
	hasher, err := New(DefaultConfig())
	if err != nil {
		b.Fatal(err)
	}
	encoded, err := hasher.Hash(context.Background(), "benchmark password phrase")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if matched, err := hasher.Verify(context.Background(), "benchmark password phrase", encoded); err != nil || !matched {
				b.Error("verification failed")
			}
		}
	})
}
