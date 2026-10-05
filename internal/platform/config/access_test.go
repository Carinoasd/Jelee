package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAccessHiddenContentStatusConfiguration(t *testing.T) {
	if got := (Config{}).Access.HiddenContentStatus(); got != 404 {
		t.Fatal("zero configuration must keep 404", got)
	}
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"access":{"hiddenStatus":403}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, env string
		file, set bool
		want      int
		fail      bool
	}{
		{name: "default", want: 404},
		{name: "env_403", env: "403", set: true, want: 403},
		{name: "env_404", env: "404", set: true, want: 404},
		{name: "file_403", file: true, want: 403},
		{name: "env_overrides_file", file: true, env: "404", set: true, want: 404},
		{name: "env_invalid_status", env: "401", set: true, fail: true},
		{name: "env_not_number", env: "forbidden", set: true, fail: true},
		{name: "env_empty", env: "", set: true, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
			if tc.file {
				values["JELEE_CONFIG"] = file
			}
			if tc.set {
				values["JELEE_HIDDEN_CONTENT_STATUS"] = tc.env
			}
			c, err := LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
			if tc.fail {
				if err == nil {
					t.Fatal("invalid hidden content status accepted")
				}
				return
			}
			if err != nil || c.Access.HiddenContentStatus() != tc.want {
				t.Fatal("hidden content status", err, c.Access.HiddenStatus)
			}
		})
	}
	for _, status := range []int{200, 401, 410, 500, -1} {
		c := Config{Access: AccessConfig{HiddenStatus: status}}
		if c.Access.Validate() == nil {
			t.Fatal("hidden status accepted", status)
		}
	}
	file = filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(file, []byte(`{"access":{"hiddenStatus":200}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWith(func(k string) (string, bool) {
		v, ok := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_CONFIG": file}[k]
		return v, ok
	}); err == nil {
		t.Fatal("configuration file hidden status not validated")
	}
}
