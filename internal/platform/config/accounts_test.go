package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func accountConfigLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

func accountConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAccountsConfigurationEnvironmentOverridesJSONAndPreservesDefaults(t *testing.T) {
	path := accountConfigFile(t, `{"enableAccounts":true,"accounts":{"passwordMemoryKiB":32768,"passwordIterations":2,"passwordParallelism":1,"passwordConcurrency":1,"loginIPLimit":11,"loginUserLimit":4,"loginWindowSeconds":45,"loginMaxEntries":500,"lockAfter":6,"lockSeconds":120,"maxSessions":3,"sessionHours":2}}`)
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_CONFIG": path}
	c, err := LoadWith(accountConfigLookup(values))
	fileWant := AccountsConfig{PasswordMemoryKiB: 32768, PasswordIterations: 2, PasswordParallelism: 1, PasswordConcurrency: 1, LoginIPLimit: 11, LoginUserLimit: 4, LoginWindowSeconds: 45, LoginMaxEntries: 500, LockAfter: 6, LockSeconds: 120, MaxSessions: 3, SessionHours: 2}
	if err != nil || !c.EnableAccounts || c.Accounts != fileWant {
		t.Fatalf("JSON account configuration was not applied: %v", err)
	}
	for key, value := range map[string]int{
		"JELEE_PASSWORD_MEMORY_KIB": 98304, "JELEE_PASSWORD_ITERATIONS": 4,
		"JELEE_PASSWORD_PARALLELISM": 3, "JELEE_PASSWORD_CONCURRENCY": 4,
		"JELEE_LOGIN_IP_LIMIT": 19, "JELEE_LOGIN_USER_LIMIT": 7,
		"JELEE_LOGIN_WINDOW_SECONDS": 90, "JELEE_LOGIN_MAX_ENTRIES": 800,
		"JELEE_LOGIN_LOCK_AFTER": 9, "JELEE_LOGIN_LOCK_SECONDS": 180,
		"JELEE_MAX_SESSIONS": 5, "JELEE_SESSION_HOURS": 48,
	} {
		values[key] = strconv.Itoa(value)
	}
	c, err = LoadWith(accountConfigLookup(values))
	envWant := AccountsConfig{PasswordMemoryKiB: 98304, PasswordIterations: 4, PasswordParallelism: 3, PasswordConcurrency: 4, LoginIPLimit: 19, LoginUserLimit: 7, LoginWindowSeconds: 90, LoginMaxEntries: 800, LockAfter: 9, LockSeconds: 180, MaxSessions: 5, SessionHours: 48}
	if err != nil || c.Accounts != envWant {
		t.Fatalf("environment did not override each JSON account setting: %v", err)
	}
	values = map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_CONFIG": accountConfigFile(t, `{"accounts":{"sessionHours":72}}`)}
	c, err = LoadWith(accountConfigLookup(values))
	defaultWant := DefaultAccountsConfig()
	defaultWant.SessionHours = 72
	if err != nil || !c.EnableAccounts || c.Accounts != defaultWant {
		t.Fatalf("partial JSON erased defaults or rollout environment: %v", err)
	}
	// Validation must run after an explicit environment correction to file data.
	values["JELEE_CONFIG"] = accountConfigFile(t, `{"enableAccounts":true,"accounts":{"passwordMemoryKiB":1}}`)
	values["JELEE_PASSWORD_MEMORY_KIB"] = "65536"
	if _, err := LoadWith(accountConfigLookup(values)); err != nil {
		t.Fatalf("valid override was checked after invalid file value: %v", err)
	}
}

type accountConfigBound struct {
	key  string
	min  int
	max  int
	edit func(*AccountsConfig, int)
}

func accountConfigBounds() []accountConfigBound {
	return []accountConfigBound{
		{"JELEE_PASSWORD_MEMORY_KIB", 19456, 131072, func(c *AccountsConfig, n int) { c.PasswordMemoryKiB = n }},
		{"JELEE_PASSWORD_ITERATIONS", 2, 6, func(c *AccountsConfig, n int) { c.PasswordIterations = n }},
		{"JELEE_PASSWORD_PARALLELISM", 1, 4, func(c *AccountsConfig, n int) { c.PasswordParallelism = n }},
		{"JELEE_PASSWORD_CONCURRENCY", 1, 8, func(c *AccountsConfig, n int) { c.PasswordConcurrency = n }},
		{"JELEE_LOGIN_IP_LIMIT", 1, 10000, func(c *AccountsConfig, n int) { c.LoginIPLimit = n }},
		{"JELEE_LOGIN_USER_LIMIT", 1, 1000, func(c *AccountsConfig, n int) { c.LoginUserLimit = n }},
		{"JELEE_LOGIN_WINDOW_SECONDS", 1, 3600, func(c *AccountsConfig, n int) { c.LoginWindowSeconds = n }},
		{"JELEE_LOGIN_MAX_ENTRIES", 2, 100000, func(c *AccountsConfig, n int) { c.LoginMaxEntries = n }},
		{"JELEE_LOGIN_LOCK_AFTER", 3, 100, func(c *AccountsConfig, n int) { c.LockAfter = n }},
		{"JELEE_LOGIN_LOCK_SECONDS", 60, 86400, func(c *AccountsConfig, n int) { c.LockSeconds = n }},
		{"JELEE_MAX_SESSIONS", 1, 100, func(c *AccountsConfig, n int) { c.MaxSessions = n }},
		{"JELEE_SESSION_HOURS", 1, 720, func(c *AccountsConfig, n int) { c.SessionHours = n }},
	}
}

func TestAccountsConfigurationSecurityBoundaries(t *testing.T) {
	if err := DefaultAccountsConfig().Validate(); err != nil {
		t.Fatalf("default account policy is invalid: %v", err)
	}
	for _, bound := range accountConfigBounds() {
		t.Run(bound.key, func(t *testing.T) {
			for _, value := range []int{bound.min - 1, bound.min, bound.max, bound.max + 1} {
				c := DefaultAccountsConfig()
				bound.edit(&c, value)
				wantValid := value >= bound.min && value <= bound.max
				if err := c.Validate(); (err == nil) != wantValid {
					t.Fatalf("value=%d valid=%v error=%v", value, wantValid, err)
				}
				values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ENABLE_ACCOUNTS": "true", bound.key: strconv.Itoa(value)}
				if _, err := LoadWith(accountConfigLookup(values)); (err == nil) != wantValid {
					t.Fatalf("environment value=%d valid=%v error=%v", value, wantValid, err)
				}
			}
		})
	}
}

func TestAccountsConfigurationMalformedEnvironmentDoesNotLeakValues(t *testing.T) {
	for _, bound := range accountConfigBounds() {
		for _, bad := range []string{"private-secret-value", "", "2.5", "999999999999999999999999999999"} {
			values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ENABLE_ACCOUNTS": "true", bound.key: bad}
			_, err := LoadWith(accountConfigLookup(values))
			if err == nil || err.Error() != "invalid "+bound.key {
				t.Fatalf("malformed %s error must contain only its key: %v", bound.key, err)
			}
		}
	}
	_, err := LoadWith(accountConfigLookup(map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ENABLE_ACCOUNTS": "private-boolean-value"}))
	if err == nil || err.Error() != "invalid JELEE_ENABLE_ACCOUNTS" {
		t.Fatalf("rollout flag value leaked or was accepted: %v", err)
	}
}

func TestAccountsConfigurationDisabledRolloutCompatibility(t *testing.T) {
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
	c, err := LoadWith(accountConfigLookup(values))
	if err != nil || c.EnableAccounts || c.Accounts != DefaultAccountsConfig() {
		t.Fatalf("existing configuration changed rollout behavior: %v", err)
	}
	// Existing callers constructing Config without Accounts keep working while
	// account routes are disabled. Enabling routes validates every account bound.
	c.Accounts = AccountsConfig{}
	if err := c.Validate(); err != nil {
		t.Fatalf("disabled account policy broke legacy Config construction: %v", err)
	}
	c.EnableAccounts = true
	if err := c.Validate(); err == nil {
		t.Fatal("enabled accounts accepted an uninitialized policy")
	}
	values["JELEE_CONFIG"] = accountConfigFile(t, `{"enableAccounts":true,"accounts":{"passwordMemoryKiB":0}}`)
	values["JELEE_ENABLE_ACCOUNTS"] = "false"
	c, err = LoadWith(accountConfigLookup(values))
	if err != nil || c.EnableAccounts {
		t.Fatalf("environment could not disable account rollout: %v", err)
	}
	values["JELEE_ENABLE_ACCOUNTS"] = "true"
	if _, err := LoadWith(accountConfigLookup(values)); err == nil {
		t.Fatal("enabled rollout ignored unsafe file policy")
	}
}

func TestAccountsConfigurationRejectsUnknownNestedJSON(t *testing.T) {
	for _, body := range []string{`{"enableAccounts":true,"accounts":{"passwordIteratons":3}}`, `{"enableAccounts":true,"accounts":{"passwordMemoryKiB":"private-value"}}`} {
		values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_CONFIG": accountConfigFile(t, body)}
		if _, err := LoadWith(accountConfigLookup(values)); err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("unknown/mistyped account setting accepted or exposed: %v", err)
		}
	}
	// Account policy serializes only configuration, never the database credential.
	c, err := LoadWith(accountConfigLookup(map[string]string{"JELEE_DATABASE_URL": "postgres://user:private-credential@localhost/jelee", "JELEE_ENABLE_ACCOUNTS": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(c)
	if err != nil || strings.Contains(string(data), "private-credential") || !strings.Contains(string(data), `"enableAccounts":true`) {
		t.Fatal("configuration JSON exposed credentials or lost rollout flag")
	}
}
