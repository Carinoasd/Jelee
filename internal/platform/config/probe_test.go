package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeExplicitOptInAndHeartbeatBounds(t *testing.T) {
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
	lookup := func(k string) (string, bool) { v, ok := values[k]; return v, ok }
	cfg, err := LoadWith(lookup)
	if err != nil || cfg.EnableProbe {
		t.Fatal("probe default must be disabled")
	}
	values["JELEE_ENABLE_PROBE"] = "invalid"
	if _, err = LoadWith(lookup); err == nil {
		t.Fatal("invalid probe flag")
	}
	values["JELEE_ENABLE_PROBE"] = "true"
	if _, err = LoadWith(lookup); err == nil {
		t.Fatal("probe without jobs")
	}
	values["JELEE_ENABLE_JOBS"] = "true"
	if _, err = LoadWith(lookup); err == nil {
		t.Fatal("probe without accounts")
	}
	values["JELEE_ENABLE_ACCOUNTS"] = "true"
	if cfg, err = LoadWith(lookup); err != nil || !cfg.EnableProbe {
		t.Fatal("valid probe configuration", err)
	}
	values["JELEE_JOB_LEASE_SECONDS"] = "90"
	values["JELEE_JOB_DATABASE_TIMEOUT_SECONDS"] = "10"
	if _, err = LoadWith(lookup); err == nil {
		t.Fatal("probe child lease heartbeat can be starved")
	}
	values["JELEE_JOB_DATABASE_TIMEOUT_SECONDS"] = "9"
	if _, err = LoadWith(lookup); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "probe.json")
	if err = os.WriteFile(path, []byte(`{"enableProbe":true,"enableJobs":true,"enableAccounts":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	values = map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_CONFIG": path}
	if cfg, err = LoadWith(lookup); err != nil || !cfg.EnableProbe {
		t.Fatal("file probe flag", err)
	}
	values["JELEE_ENABLE_PROBE"] = "false"
	if cfg, err = LoadWith(lookup); err != nil || cfg.EnableProbe {
		t.Fatal("environment did not override file")
	}
}
