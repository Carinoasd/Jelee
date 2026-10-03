package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTMDBKeySourcesAndPrivacy(t *testing.T) {
	key := strings.Repeat("a", 32)
	file := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(file, []byte(key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
		fail bool
	}{
		{"absent", nil, "", false}, {"env", map[string]string{"TMDB_API_KEY": key}, key, false},
		{"file", map[string]string{"TMDB_API_KEY_FILE": file}, key, false},
		{"both", map[string]string{"TMDB_API_KEY": key, "TMDB_API_KEY_FILE": file}, "", true},
		{"empty", map[string]string{"TMDB_API_KEY": ""}, "", true},
		{"empty_file", map[string]string{"TMDB_API_KEY_FILE": ""}, "", true},
		{"bad", map[string]string{"TMDB_API_KEY": "secret"}, "", true},
		{"whitespace", map[string]string{"TMDB_API_KEY": " " + key}, "", true},
		{"missing_file", map[string]string{"TMDB_API_KEY_FILE": "/secret/missing"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(k string) (string, bool) {
				if k == "JELEE_DATABASE_URL" {
					return "postgres://localhost/jelee", true
				}
				v, ok := tc.env[k]
				return v, ok
			}
			cfg, err := LoadWith(lookup)
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v", err)
			}
			if err != nil {
				if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), file) {
					t.Fatal("credential or path leaked")
				}
				return
			}
			if cfg.TMDBAPIKey != tc.want {
				t.Fatal("credential source differs")
			}
			raw, _ := json.Marshal(cfg)
			if strings.Contains(string(raw), key) || strings.Contains(string(raw), "TMDBAPIKey") {
				t.Fatal("credential serialized")
			}
		})
	}
	if err := os.WriteFile(file, []byte(strings.Repeat("a", 4097)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTMDBKey(func(k string) (string, bool) { return file, k == "TMDB_API_KEY_FILE" }); err == nil {
		t.Fatal("oversized file accepted")
	}
	if err := os.WriteFile(file, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTMDBKey(func(k string) (string, bool) { return file, k == "TMDB_API_KEY_FILE" }); err == nil {
		t.Fatal("empty file accepted")
	}
}
