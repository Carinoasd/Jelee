package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTMDBBudgetEnvironmentDefaultsAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      map[string]string
		interval time.Duration
		capacity int
		daily    int
		fail     string
	}{
		{"defaults", nil, 250 * time.Millisecond, 4, 0, ""},
		{"configured", map[string]string{"JELEE_TMDB_REQUEST_INTERVAL_MS": "1000", "JELEE_TMDB_MAX_CONCURRENT": "1", "JELEE_TMDB_DAILY_REQUEST_LIMIT": "5000"}, time.Second, 1, 5000, ""},
		{"lower_bounds", map[string]string{"JELEE_TMDB_REQUEST_INTERVAL_MS": "20", "JELEE_TMDB_MAX_CONCURRENT": "32", "JELEE_TMDB_DAILY_REQUEST_LIMIT": "0"}, 20 * time.Millisecond, 32, 0, ""},
		{"upper_bounds", map[string]string{"JELEE_TMDB_REQUEST_INTERVAL_MS": "60000", "JELEE_TMDB_DAILY_REQUEST_LIMIT": "10000000"}, time.Minute, 4, 10_000_000, ""},
		{"interval_zero", map[string]string{"JELEE_TMDB_REQUEST_INTERVAL_MS": "0"}, 0, 0, 0, "invalid JELEE_TMDB_REQUEST_INTERVAL_MS"},
		{"interval_text", map[string]string{"JELEE_TMDB_REQUEST_INTERVAL_MS": "250ms"}, 0, 0, 0, "invalid JELEE_TMDB_REQUEST_INTERVAL_MS"},
		{"interval_low", map[string]string{"JELEE_TMDB_REQUEST_INTERVAL_MS": "19"}, 0, 0, 0, "tmdb requestIntervalMs must be between 20 and 60000"},
		{"interval_high", map[string]string{"JELEE_TMDB_REQUEST_INTERVAL_MS": "60001"}, 0, 0, 0, "tmdb requestIntervalMs must be between 20 and 60000"},
		{"capacity_zero", map[string]string{"JELEE_TMDB_MAX_CONCURRENT": "0"}, 0, 0, 0, "invalid JELEE_TMDB_MAX_CONCURRENT"},
		{"capacity_negative", map[string]string{"JELEE_TMDB_MAX_CONCURRENT": "-1"}, 0, 0, 0, "invalid JELEE_TMDB_MAX_CONCURRENT"},
		{"capacity_high", map[string]string{"JELEE_TMDB_MAX_CONCURRENT": "33"}, 0, 0, 0, "tmdb maxConcurrent must be between 1 and 32"},
		{"daily_negative", map[string]string{"JELEE_TMDB_DAILY_REQUEST_LIMIT": "-1"}, 0, 0, 0, "invalid JELEE_TMDB_DAILY_REQUEST_LIMIT"},
		{"daily_high", map[string]string{"JELEE_TMDB_DAILY_REQUEST_LIMIT": "10000001"}, 0, 0, 0, "tmdb dailyRequestLimit must be between 0 and 10000000"},
		{"daily_empty", map[string]string{"JELEE_TMDB_DAILY_REQUEST_LIMIT": ""}, 0, 0, 0, "invalid JELEE_TMDB_DAILY_REQUEST_LIMIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadWith(func(k string) (string, bool) {
				if k == "JELEE_DATABASE_URL" {
					return "postgres://localhost/jelee", true
				}
				v, ok := tc.env[k]
				return v, ok
			})
			if tc.fail != "" {
				if err == nil || err.Error() != tc.fail {
					t.Fatalf("error=%v want %q", err, tc.fail)
				}
				for _, value := range tc.env {
					if value != "" && strings.Contains(err.Error(), value) {
						t.Fatal("configured value echoed in the startup error")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.TMDB.RequestInterval() != tc.interval || cfg.TMDB.Concurrency() != tc.capacity || cfg.TMDB.DailyRequestLimit != tc.daily {
				t.Fatalf("budget=%+v", cfg.TMDB)
			}
		})
	}
}

func TestTMDBBudgetConfigurationFileAndEnvironmentPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"tmdb":{"requestIntervalMs":500,"maxConcurrent":2,"dailyRequestLimit":100}}`), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_CONFIG": path}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	cfg, err := LoadWith(lookup)
	if err != nil || cfg.TMDB.RequestInterval() != 500*time.Millisecond || cfg.TMDB.Concurrency() != 2 || cfg.TMDB.DailyRequestLimit != 100 {
		t.Fatalf("file budget=%+v error=%v", cfg.TMDB, err)
	}
	env["JELEE_TMDB_MAX_CONCURRENT"] = "8"
	if cfg, err = LoadWith(lookup); err != nil || cfg.TMDB.Concurrency() != 8 || cfg.TMDB.RequestInterval() != 500*time.Millisecond {
		t.Fatalf("environment did not override the file: %+v %v", cfg.TMDB, err)
	}
	if err := os.WriteFile(path, []byte(`{"tmdb":{"maxConcurrent":-1}}`), 0600); err != nil {
		t.Fatal(err)
	}
	delete(env, "JELEE_TMDB_MAX_CONCURRENT")
	if _, err := LoadWith(lookup); err == nil || err.Error() != "tmdb maxConcurrent must be between 1 and 32" {
		t.Fatalf("invalid file budget accepted: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"tmdb":{"requestsPerSecond":4}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("unknown budget field accepted")
	}
	// A configuration built in code without the section keeps the defaults.
	if (TMDBConfig{}).Validate() != nil || (TMDBConfig{}).RequestInterval() != 250*time.Millisecond || (TMDBConfig{}).Concurrency() != 4 {
		t.Fatal("zero section is not the default budget")
	}
}
