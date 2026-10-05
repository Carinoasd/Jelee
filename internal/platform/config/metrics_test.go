package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetricsExplicitRolloutRequiresAccounts(t *testing.T) {
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	c, err := LoadWith(lookup)
	if err != nil || c.EnableMetrics {
		t.Fatal("metrics must be disabled by default", err)
	}
	values["JELEE_ENABLE_METRICS"] = "private-invalid-value"
	if _, err = LoadWith(lookup); err == nil || strings.Contains(err.Error(), "private-invalid-value") {
		t.Fatal("invalid metrics flag was accepted or leaked")
	}
	values["JELEE_ENABLE_METRICS"] = "true"
	if _, err = LoadWith(lookup); err == nil {
		t.Fatal("metrics enabled without account rollout")
	}
	values["JELEE_ENABLE_ACCOUNTS"] = "true"
	c, err = LoadWith(lookup)
	if err != nil || !c.EnableMetrics || !c.EnableAccounts {
		t.Fatal("valid metrics rollout was rejected", err)
	}
	c.EnableAccounts = false
	if c.Validate() == nil {
		t.Fatal("direct configuration bypassed the account dependency")
	}
}

func TestMetricsFileConfigurationAndEnvironmentOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"enableAccounts":true,"enableMetrics":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_CONFIG": path}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	c, err := LoadWith(lookup)
	if err != nil || !c.EnableMetrics {
		t.Fatal("file metrics rollout was rejected", err)
	}
	values["JELEE_ENABLE_METRICS"] = "false"
	values["JELEE_ENABLE_ACCOUNTS"] = "false"
	c, err = LoadWith(lookup)
	if err != nil || c.EnableMetrics || c.EnableAccounts {
		t.Fatal("environment could not disable metrics and accounts", err)
	}
}
