package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustedProxyConfigurationSources(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"trustedProxies":["10.1.2.3/8"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		file bool
		env  *string
		want string
	}{
		{"default", false, nil, ""}, {"file", true, nil, "10.0.0.0/8"},
		{"env_override", true, func() *string { v := "2001:db8::1/32"; return &v }(), "2001:db8::/32"},
		{"env_clear", true, func() *string { v := "  "; return &v }(), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
			if tc.file {
				values["JELEE_CONFIG"] = file
			}
			if tc.env != nil {
				values["JELEE_TRUSTED_PROXIES"] = *tc.env
			}
			c, err := LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
			if err != nil {
				t.Fatal(err)
			}
			prefixes, err := c.TrustedProxyPrefixes()
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(prefixes) != 0 {
					t.Fatal("unexpected trust")
				}
				return
			}
			if len(prefixes) != 1 || prefixes[0].String() != tc.want {
				t.Fatalf("prefixes=%v", prefixes)
			}
		})
	}
}

func TestTrustedProxyConfigurationValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		fail   bool
		want   string
	}{
		{"mapped", []string{"::ffff:192.0.2.5/120"}, false, "192.0.2.0/24"},
		{"trim", []string{" 10.1.1.1/8 "}, false, "10.0.0.0/8"},
		{"max", strings.Split(strings.TrimSuffix(strings.Repeat("10.0.0.0/8,", 64), ","), ","), false, "10.0.0.0/8"},
		{"too_many", strings.Split(strings.TrimSuffix(strings.Repeat("10.0.0.0/8,", 65), ","), ","), true, ""},
		{"missing_prefix", []string{"192.0.2.5"}, true, ""}, {"empty", []string{""}, true, ""},
		{"hostname", []string{"private-secret.invalid/24"}, true, ""}, {"zone", []string{"fe80::1%private-secret/64"}, true, ""},
		{"mapped_broad", []string{"::ffff:192.0.2.5/95"}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{TrustedProxies: tc.values}
			prefixes, err := c.TrustedProxyPrefixes()
			if tc.fail {
				if err == nil {
					t.Fatal("invalid trust accepted")
				}
				if strings.Contains(err.Error(), "private-secret") {
					t.Fatal("value leaked")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(prefixes) == 0 || prefixes[0].String() != tc.want {
				t.Fatalf("prefixes=%v", prefixes)
			}
			c.TrustedProxies[0] = "0.0.0.0/0"
			if prefixes[0].String() != tc.want {
				t.Fatal("parsed prefix aliased configuration")
			}
		})
	}
	_, err := LoadWith(func(k string) (string, bool) {
		values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_TRUSTED_PROXIES": "private-secret.invalid"}
		v, ok := values[k]
		return v, ok
	})
	if err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("startup error=%v", err)
	}
}
