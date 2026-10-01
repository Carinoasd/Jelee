package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurationFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"missing", nil, "PostgreSQL"},
		{"sqlite", map[string]string{"JELEE_DATABASE_URL": "sqlite:///secret/location"}, "PostgreSQL"},
		{"malformed", map[string]string{"JELEE_DATABASE_URL": "postgres://user:secret@%zz/db"}, "PostgreSQL"},
		{"dev", map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_DEV_MODE": "true"}, "developer mode"},
		{"wild host", map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ALLOWED_HOSTS": ""}, "allowedHosts"},
		{"direct rollout", map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ENABLE_DIRECT": "true"}, "catalog"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadWith(func(k string) (string, bool) { v, ok := tt.values[k]; return v, ok })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestEnvironmentOverridesFileAndSecretsStayOutOfJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"listen":"127.0.0.1:9000","maxConnections":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"JELEE_CONFIG": path, "JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_LISTEN": "127.0.0.1:8097"}
	c, err := LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8097" || c.MaxConnections != 3 {
		t.Fatalf("unexpected configuration")
	}
	if err := os.WriteFile(path, []byte(`{"databaseURL":"postgres://user:secret@localhost/db"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok }); err == nil {
		t.Fatal("file secret accepted")
	}
}

func TestConfigurationSizeAndCredentialBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	values := map[string]string{"JELEE_CONFIG": path, "JELEE_DATABASE_URL": "postgres://localhost/jelee"}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	for _, content := range []string{
		`{}` + strings.Repeat(" ", 65536),
		`{} {"enableDirect":true}`,
		`{"unknownSetting":true}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWith(lookup); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	delete(values, "JELEE_CONFIG")
	values["JELEE_DATABASE_URL_FILE"] = path
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("ambiguous credential sources accepted")
	}
	delete(values, "JELEE_DATABASE_URL")
	if err := os.WriteFile(path, []byte("postgres://localhost/jelee\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWith(lookup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 16385)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("oversize credential file accepted")
	}
}

func TestNumericAndRolloutEnvironmentValidation(t *testing.T) {
	for _, entry := range []struct{ key, value string }{
		{"JELEE_MAX_CONNECTIONS", "0"}, {"JELEE_MAX_CONNECTIONS", "129"}, {"JELEE_MAX_CONNECTIONS", "invalid"},
		{"JELEE_MAX_STREAMS", "0"}, {"JELEE_MAX_STREAMS", "129"}, {"JELEE_MAX_STREAMS", "invalid"},
		{"JELEE_ENABLE_CATALOG", "invalid"}, {"JELEE_LISTEN", "localhost:8097"},
		{"JELEE_LISTEN", "127.0.0.1:0"}, {"JELEE_LISTEN", "127.0.0.1:65536"},
		{"JELEE_ALLOWED_HOSTS", "localhost,evil/host"},
	} {
		t.Run(entry.key+entry.value, func(t *testing.T) {
			values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", entry.key: entry.value}
			if _, err := LoadWith(func(key string) (string, bool) { v, ok := values[key]; return v, ok }); err == nil {
				t.Fatal("invalid environment accepted")
			}
		})
	}
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_MAX_CONNECTIONS": "4", "JELEE_MAX_STREAMS": "2", "JELEE_ENABLE_CATALOG": "true", "JELEE_ENABLE_DIRECT": "true"}
	c, err := LoadWith(func(key string) (string, bool) { v, ok := values[key]; return v, ok })
	if err != nil || c.MaxConnections != 4 || c.MaxStreams != 2 || !c.EnableDirect || c.RequestTimeout() <= 0 {
		t.Fatalf("valid configuration rejected: %v", err)
	}
}
