package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDevConfigThresholds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"dev":{"enabled":true,"ttlMinutes":90}}`), 0600); err != nil {
		t.Fatal(err)
	}
	base := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
	load := func(extra map[string]string) Config {
		t.Helper()
		values := map[string]string{}
		for k, v := range base {
			values[k] = v
		}
		for k, v := range extra {
			values[k] = v
		}
		cfg, err := LoadWith(func(name string) (string, bool) { v, ok := values[name]; return v, ok })
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	for _, tc := range []struct {
		name    string
		env     map[string]string
		capable bool
	}{
		{"defaults", nil, false},
		{"environment flag only", map[string]string{"JELEE_DEV_MODE": "true"}, false},
		{"configuration only", map[string]string{"JELEE_CONFIG": path}, false},
		{"both", map[string]string{"JELEE_CONFIG": path, "JELEE_DEV_MODE": "TRUE"}, true},
		{"explicit false", map[string]string{"JELEE_CONFIG": path, "JELEE_DEV_MODE": "false"}, false},
		{"production ignores both", map[string]string{"JELEE_CONFIG": path, "JELEE_DEV_MODE": "true", "JELEE_ENV": "production"}, false},
	} {
		cfg := load(tc.env)
		if cfg.Dev.Capable() != tc.capable {
			t.Errorf("%s: capable=%t", tc.name, cfg.Dev.Capable())
		}
	}
	if cfg := load(map[string]string{"JELEE_CONFIG": path}); cfg.Dev.TTL() != 90*time.Minute || (DevConfig{}).TTL() != 12*time.Hour {
		t.Fatal("ttl", cfg.Dev.TTL())
	}
	for _, ttl := range []int{-1, 1441} {
		if (DevConfig{TTLMinutes: ttl}).Validate() == nil {
			t.Fatal("ttl accepted", ttl)
		}
	}
}
