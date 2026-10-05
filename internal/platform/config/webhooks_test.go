package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func webhookEnv(extra map[string]string) func(string) (string, bool) {
	env := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_WEBHOOKS": "true"}
	for k, v := range extra {
		env[k] = v
	}
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func TestWebhooksRequireMasterKey(t *testing.T) {
	_, err := LoadWith(webhookEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "JELEE_WEBHOOK_MASTER_KEY") {
		t.Fatalf("webhooks enabled without a master key: %v", err)
	}
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	for _, text := range []string{
		base64.StdEncoding.EncodeToString(raw), base64.RawURLEncoding.EncodeToString(raw),
		strings.Repeat("ab", 32), " " + base64.StdEncoding.EncodeToString(raw) + "\n",
	} {
		c, err := LoadWith(webhookEnv(map[string]string{"JELEE_WEBHOOK_MASTER_KEY": text}))
		if err != nil {
			t.Fatalf("valid key refused: %v", err)
		}
		key, err := c.Webhooks.Key()
		if err != nil || len(key) != 32 {
			t.Fatalf("key %d %v", len(key), err)
		}
		if printed := fmt.Sprintf("%v %+v %#v", c, c.Webhooks, c.Webhooks); strings.Contains(printed, strings.TrimSpace(text)) {
			t.Fatal("configuration prints the master key")
		}
	}
	for _, bad := range []string{"short", base64.StdEncoding.EncodeToString(raw[:16]), strings.Repeat("zz", 32)} {
		_, err := LoadWith(webhookEnv(map[string]string{"JELEE_WEBHOOK_MASTER_KEY": bad}))
		if err == nil || strings.Contains(err.Error(), bad) {
			t.Fatalf("bad key %q: %v", bad, err)
		}
	}
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte(strings.Repeat("cd", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWith(webhookEnv(map[string]string{"JELEE_WEBHOOK_MASTER_KEY_FILE": file})); err != nil {
		t.Fatalf("key file: %v", err)
	}
	if _, err := LoadWith(webhookEnv(map[string]string{"JELEE_WEBHOOK_MASTER_KEY_FILE": file, "JELEE_WEBHOOK_MASTER_KEY": strings.Repeat("cd", 32)})); err == nil {
		t.Fatal("two key sources accepted")
	}
	// Webhooks need accounts; without webhooks the key is not required.
	if _, err := LoadWith(webhookEnv(map[string]string{"JELEE_ENABLE_ACCOUNTS": "false", "JELEE_WEBHOOK_MASTER_KEY": strings.Repeat("cd", 32)})); err == nil {
		t.Fatal("webhooks without accounts accepted")
	}
	if _, err := LoadWith(webhookEnv(map[string]string{"JELEE_ENABLE_WEBHOOKS": "false"})); err != nil {
		t.Fatalf("disabled webhooks need no key: %v", err)
	}
}

func TestWebhooksSettings(t *testing.T) {
	key := strings.Repeat("cd", 32)
	c, err := LoadWith(webhookEnv(map[string]string{"JELEE_WEBHOOK_MASTER_KEY": key, "JELEE_WEBHOOK_ALLOWED_HOSTS": "hooks.example, ops.example", "JELEE_WEBHOOK_BATCH": "20", "JELEE_WEBHOOK_RETENTION_DAYS": "3"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Webhooks.AllowedHosts, ",") != "hooks.example,ops.example" || c.Webhooks.BatchSize() != 20 || c.Webhooks.Retention().Hours() != 72 || c.Webhooks.Lease().Seconds() != 120 {
		t.Fatalf("settings %+v", c.Webhooks)
	}
	for name, value := range map[string]string{
		"JELEE_WEBHOOK_ALLOWED_HOSTS":     "Hooks.Example",
		"JELEE_WEBHOOK_LEASE_SECONDS":     "30",
		"JELEE_WEBHOOK_CONCURRENCY":       "0",
		"JELEE_WEBHOOK_RETENTION_DAYS":    "x",
		"JELEE_WEBHOOK_POLL_MILLISECONDS": "10",
	} {
		if _, err := LoadWith(webhookEnv(map[string]string{"JELEE_WEBHOOK_MASTER_KEY": key, name: value})); err == nil {
			t.Errorf("%s=%s accepted", name, value)
		}
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte("-----BEGIN CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Webhooks.CAFile = ca
	if data, err := c.Webhooks.RootCAs(); err != nil || len(data) == 0 {
		t.Fatalf("ca file %v", err)
	}
}
