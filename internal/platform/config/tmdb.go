package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

func loadTMDBKey(lookup func(string) (string, bool)) (string, error) {
	key, present := lookup("TMDB_API_KEY")
	path, filePresent := lookup("TMDB_API_KEY_FILE")
	if present && filePresent {
		return "", errors.New("set only one TMDB credential source")
	}
	if filePresent {
		if path == "" {
			return "", errors.New("cannot read TMDB credential file")
		}
		f, err := os.Open(path) //nolint:gosec // G304: the operator names the secret file
		if err != nil {
			return "", errors.New("cannot read TMDB credential file")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 4097))
		if err != nil || len(data) > 4096 {
			return "", errors.New("invalid TMDB credential file")
		}
		key = strings.TrimSpace(string(data))
	}
	if (present || filePresent) && !validTMDBKey(key) {
		return "", errors.New("invalid TMDB_API_KEY")
	}
	return key, nil
}

func validTMDBKey(key string) bool {
	if len(key) != 32 {
		return false
	}
	for _, ch := range key {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
			return false
		}
	}
	return true
}

// TMDBConfig is the request budget of the TMDB adapter (G14.3). Zero fields
// keep the defaults, so configurations built without this section stay valid.
// The budget is per process: several instances each spend their own.
type TMDBConfig struct {
	// RequestIntervalMs is the minimum spacing between request starts
	// (default 250, 20–60000).
	RequestIntervalMs int `json:"requestIntervalMs"`
	// MaxConcurrent bounds requests in flight or waiting for a start
	// (default 4, 1–32).
	MaxConcurrent int `json:"maxConcurrent"`
	// DailyRequestLimit bounds request attempts, retries included, per UTC
	// day; 0 (default) is no daily limit, otherwise up to 10000000.
	DailyRequestLimit int `json:"dailyRequestLimit"`
}

// TMDB budget defaults used when the section leaves a field at zero.
const (
	DefaultTMDBRequestIntervalMs = 250
	DefaultTMDBMaxConcurrent     = 4
)

// Validate never includes configured values in its errors.
func (c TMDBConfig) Validate() error {
	if c.RequestIntervalMs != 0 && (c.RequestIntervalMs < 20 || c.RequestIntervalMs > 60000) {
		return errors.New("tmdb requestIntervalMs must be between 20 and 60000")
	}
	if c.MaxConcurrent < 0 || c.MaxConcurrent > 32 {
		return errors.New("tmdb maxConcurrent must be between 1 and 32")
	}
	if c.DailyRequestLimit < 0 || c.DailyRequestLimit > 10_000_000 {
		return errors.New("tmdb dailyRequestLimit must be between 0 and 10000000")
	}
	return nil
}

// RequestInterval is the effective spacing between request starts.
func (c TMDBConfig) RequestInterval() time.Duration {
	if c.RequestIntervalMs == 0 {
		return DefaultTMDBRequestIntervalMs * time.Millisecond
	}
	return time.Duration(c.RequestIntervalMs) * time.Millisecond
}

// Concurrency is the effective request capacity.
func (c TMDBConfig) Concurrency() int {
	if c.MaxConcurrent == 0 {
		return DefaultTMDBMaxConcurrent
	}
	return c.MaxConcurrent
}

func (c *TMDBConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	for _, item := range []struct {
		name     string
		target   *int
		zeroSafe bool
	}{
		{"JELEE_TMDB_REQUEST_INTERVAL_MS", &c.RequestIntervalMs, false},
		{"JELEE_TMDB_MAX_CONCURRENT", &c.MaxConcurrent, false},
		{"JELEE_TMDB_DAILY_REQUEST_LIMIT", &c.DailyRequestLimit, true},
	} {
		value, ok := lookup(item.name)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(value)
		// Zero means "default" only for configurations built without the
		// section; an explicit zero interval or capacity is a mistake.
		if err != nil || n < 0 || n == 0 && !item.zeroSafe {
			return fmt.Errorf("invalid %s", item.name)
		}
		*item.target = n
	}
	return nil
}
