package config

import (
	"strings"
	"testing"
)

func TestJobsRolloutAndPoolBudget(t *testing.T) {
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ENABLE_JOBS": "true"}
	if _, err := LoadWith(accountConfigLookup(values)); err == nil {
		t.Fatal("jobs enabled without accounts")
	}
	values["JELEE_ENABLE_ACCOUNTS"] = "true"
	c, err := LoadWith(accountConfigLookup(values))
	if err != nil || !c.EnableJobs || c.Jobs != DefaultJobsConfig() {
		t.Fatalf("job defaults: %v", err)
	}
	values["JELEE_MAX_CONNECTIONS"] = "3"
	if _, err = LoadWith(accountConfigLookup(values)); err == nil {
		t.Fatal("missing connection reserve")
	}
	values["JELEE_MAX_CONNECTIONS"] = "4"
	if _, err = LoadWith(accountConfigLookup(values)); err != nil {
		t.Fatal(err)
	}
	values["JELEE_CONFIG"] = accountConfigFile(t, `{"jobs":{"maxEntries":1000,"workers":1}}`)
	values["JELEE_JOB_WORKERS"] = "2"
	c, err = LoadWith(accountConfigLookup(values))
	if err != nil || c.Jobs.Workers != 2 || c.Jobs.MaxEntries != 1000 || c.Jobs.LeaseSeconds != 30 {
		t.Fatal("partial config/default override")
	}
}

func TestJobsConfigurationRejectsUnboundedSettings(t *testing.T) {
	for _, n := range []int{18446744075, 9223372036854775807} {
		c := DefaultJobsConfig()
		c.DatabaseTimeoutSeconds = n
		if c.Validate() == nil {
			t.Fatal("database timeout duration overflow accepted")
		}
	}
	for key, value := range map[string]string{"JELEE_ENABLE_JOBS": "secret", "JELEE_JOB_WORKERS": "0", "JELEE_JOB_POLL_MILLISECONDS": "99", "JELEE_JOB_LEASE_SECONDS": "9", "JELEE_JOB_DATABASE_TIMEOUT_SECONDS": "10", "JELEE_JOB_MAX_RUNTIME_SECONDS": "86401", "JELEE_JOB_QUEUE_LIMIT": "1001", "JELEE_JOB_HISTORY_LIMIT": "101", "JELEE_SCAN_MAX_ENTRIES": "500001", "JELEE_SCAN_MAX_DIRECTORIES": "100001", "JELEE_JOB_MAX_ATTEMPTS": "11", "JELEE_SCAN_MISSING_COUNT_LIMIT": "0", "JELEE_SCAN_MISSING_PERCENT_LIMIT": "101"} {
		t.Run(key, func(t *testing.T) {
			values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_JOBS": "true", key: value}
			_, err := LoadWith(accountConfigLookup(values))
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal("invalid settings accepted or value leaked")
			}
		})
	}
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_JOB_WORKERS": "secret"}
	if _, err := LoadWith(accountConfigLookup(values)); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("bad disabled environment accepted")
	}
}

func TestJobsWorkWindowConfiguration(t *testing.T) {
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ENABLE_JOBS": "true", "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_CONFIG": accountConfigFile(t, `{"jobs":{"windowStart":"22:00","windowEnd":"06:00","windowTimezone":"Asia/Taipei"}}`)}
	c, err := LoadWith(accountConfigLookup(values))
	if err != nil || c.Jobs.WindowStart != "22:00" || c.Jobs.WindowEnd != "06:00" || c.Jobs.WindowTimezone != "Asia/Taipei" {
		t.Fatalf("window load: %v", err)
	}
	values["JELEE_JOB_WINDOW_START"] = "23:00"
	c, err = LoadWith(accountConfigLookup(values))
	if err != nil || c.Jobs.WindowStart != "23:00" {
		t.Fatalf("environment override: %v", err)
	}
	for _, bad := range []string{"secret", "06:00", "24:00", ""} {
		values["JELEE_JOB_WINDOW_START"] = bad
		if _, err = LoadWith(accountConfigLookup(values)); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid window accepted or echoed")
		}
	}
}
