package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const profile = `mode: set
example.com/m/a/x.go:1.1,2.2 3 1
example.com/m/a/x.go:3.1,4.2 1 0
example.com/m/a/b/y.go:1.1,2.2 2 0
example.com/m/a/b/y.go:1.1,2.2 2 1
example.com/m/a/b/y.go:3.1,4.2 2 0
`

func TestParseGroupsByPackageAndMergesRepeatedBlocks(t *testing.T) {
	totals, err := Parse(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	if got := totals["example.com/m/a"]; got != [2]int{3, 4} {
		t.Fatalf("package a: %v", got)
	}
	if got := totals["example.com/m/a/b"]; got != [2]int{2, 4} {
		t.Fatalf("package a/b: %v", got)
	}
	for _, bad := range []string{"", "x.go:1.1,2.2 1 1\n", "mode: set\nx.go:1.1,2.2 one 1\n"} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func config() Config {
	return Config{SchemaVersion: 1, Module: "example.com/m", Targets: map[string]float64{"core": 70, "critical": 85},
		Packages: []Package{{Path: "a", Tier: "core", Minimum: 70}, {Path: "a/b", Tier: "critical", Minimum: 50, Reason: "measured"}}}
}

func TestEvaluateAndRatchet(t *testing.T) {
	totals, _ := Parse(strings.NewReader(profile))
	if _, failed := Evaluate(config(), totals); failed {
		t.Fatal("75% and 50% failed minimums 70 and 50")
	}
	c := config()
	c.Packages[1].Minimum = 50.1
	if _, failed := Evaluate(c, totals); !failed {
		t.Fatal("50% passed a 50.1 minimum")
	}
	delete(totals, "example.com/m/a/b")
	if _, failed := Evaluate(config(), totals); !failed {
		t.Fatal("missing package passed")
	}
	totals, _ = Parse(strings.NewReader(profile))
	raised := Ratchet(config(), totals)
	if raised.Packages[0].Minimum != 75 || raised.Packages[1].Minimum != 50 {
		t.Fatalf("ratchet %+v", raised.Packages)
	}
	c = config()
	c.Packages[0].Minimum = 80
	if Ratchet(c, totals).Packages[0].Minimum != 80 {
		t.Fatal("ratchet lowered a minimum")
	}
}

func TestValidate(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"unknown tier":    func(c *Config) { c.Packages[0].Tier = "other" },
		"duplicate":       func(c *Config) { c.Packages[1].Path = "a" },
		"dot path":        func(c *Config) { c.Packages[0].Path = "./a" },
		"missing reason":  func(c *Config) { c.Packages[1].Reason = "" },
		"minimum too big": func(c *Config) { c.Packages[0].Minimum = 101 },
	} {
		c := config()
		c.Packages = append([]Package(nil), c.Packages...)
		mutate(&c)
		if c.Validate() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := config().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRunExitCodes(t *testing.T) {
	dir := t.TempDir()
	profilePath, configPath := filepath.Join(dir, "cover.out"), filepath.Join(dir, "thresholds.json")
	if err := os.WriteFile(profilePath, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	write := func(c Config) {
		data, _ := json.Marshal(c)
		if err := os.WriteFile(configPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errs bytes.Buffer
	write(config())
	if code := run([]string{"-profile", profilePath, "-config", configPath}, &out, &errs); code != 0 || !strings.Contains(out.String(), "35.0 points below target") {
		t.Fatalf("pass exit %d: %s%s", code, out.String(), errs.String())
	}
	if code := run([]string{"-config", configPath, "-list"}, &out, &errs); code != 0 || !strings.Contains(out.String(), "./a/b\n") {
		t.Fatalf("list exit %d: %s", code, out.String())
	}
	if code := run([]string{"-profile", profilePath, "-config", configPath, "-update"}, &out, &errs); code != 0 {
		t.Fatalf("update exit %d", code)
	}
	var updated Config
	data, _ := os.ReadFile(configPath)
	if json.Unmarshal(data, &updated) != nil || updated.Packages[0].Minimum != 75 {
		t.Fatalf("updated config %s", data)
	}
	c := config()
	c.Packages[0].Minimum = 76
	write(c)
	out.Reset()
	if code := run([]string{"-profile", profilePath, "-config", configPath}, &out, &errs); code != 1 || !strings.Contains(out.String(), "below ratchet minimum") {
		t.Fatalf("below minimum exit %d: %s", code, out.String())
	}
	if code := run([]string{"-profile", profilePath, "-config", configPath, "-update"}, &out, &errs); code != 1 {
		t.Fatalf("update below minimum exit %d", code)
	}
	if code := run([]string{"-profile", filepath.Join(dir, "missing"), "-config", configPath}, &out, &errs); code != 2 {
		t.Fatalf("missing profile exit %d", code)
	}
}
