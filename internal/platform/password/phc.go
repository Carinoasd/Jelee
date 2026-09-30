package password

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const maxEncodedBytes = 128

type storedHash struct {
	config Config
	salt   []byte
	key    []byte
}

func validCosts(memory, iterations, parallelism uint32) bool {
	return memory >= MinMemoryKiB && memory <= MaxMemoryKiB && iterations >= MinIterations && iterations <= MaxIterations && parallelism >= 1 && parallelism <= uint32(MaxParallelism)
}

func encode(config Config, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, config.MemoryKiB, config.Iterations, config.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// parse accepts a single canonical PHC form. All lengths and cost bounds are
// checked before any KDF work. Database values are untrusted inputs too.
func parse(encoded string) (storedHash, error) {
	invalid := func() (storedHash, error) { return storedHash{}, ErrInvalidHash }
	if len(encoded) > maxEncodedBytes {
		return invalid()
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return invalid()
	}
	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 {
		return invalid()
	}
	values := [3]uint32{}
	for index, prefix := range []string{"m=", "t=", "p="} {
		if !strings.HasPrefix(parameters[index], prefix) {
			return invalid()
		}
		value := strings.TrimPrefix(parameters[index], prefix)
		if value == "" || len(value) > 10 || (len(value) > 1 && value[0] == '0') {
			return invalid()
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return invalid()
			}
		}
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return invalid()
		}
		values[index] = uint32(parsed)
	}
	if !validCosts(values[0], values[1], values[2]) {
		return invalid()
	}
	if len(parts[4]) != base64.RawStdEncoding.EncodedLen(saltBytes) || len(parts[5]) != base64.RawStdEncoding.EncodedLen(keyBytes) {
		return invalid()
	}
	salt, err := decodeCanonical(parts[4], saltBytes)
	if err != nil {
		return invalid()
	}
	key, err := decodeCanonical(parts[5], keyBytes)
	if err != nil {
		return invalid()
	}
	return storedHash{config: Config{MemoryKiB: values[0], Iterations: values[1], Parallelism: uint8(values[2])}, salt: salt, key: key}, nil
}

func decodeCanonical(value string, length int) ([]byte, error) {
	decoded, err := base64.RawStdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != length || base64.RawStdEncoding.EncodeToString(decoded) != value {
		return nil, ErrInvalidHash
	}
	return decoded, nil
}
