package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	// 75% ratchets to 73 (floor minus the 2-point margin); 50% to 48 stays
	// under the existing 50.
	raised := Ratchet(config(), totals)
	if raised.Packages[0].Minimum != 73 || raised.Packages[1].Minimum != 50 {
		t.Fatalf("ratchet %+v", raised.Packages)
	}
	c = config()
	c.Packages[0].Minimum = 80
	if Ratchet(c, totals).Packages[0].Minimum != 80 {
		t.Fatal("ratchet lowered a minimum")
	}
}

func TestRatchetFloorKeepsTheMargin(t *testing.T) {
	for _, tc := range []struct{ measured, minimum float64 }{
		{87.99, 85}, {87, 85}, {86.999999999999, 85}, {100, 98}, {1.5, 0}, {0, 0},
	} {
		if got := ratchetFloor(tc.measured); got != tc.minimum {
			t.Fatalf("ratchetFloor(%v) = %v, want %v", tc.measured, got, tc.minimum)
		}
	}
}

func TestRebaseRecomputesMinimumsAndReasons(t *testing.T) {
	totals, _ := Parse(strings.NewReader(profile))
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	c := config()
	c.Packages[0].Minimum, c.Packages[0].Reason = 74, "measured 74.2% on 2026-10-04: success paths need the image files"
	c.Packages[1].Minimum, c.Packages[1].Reason = 50, "measured 50.0% on 2026-10-04 without PostgreSQL; ratchet until tests close the 35.0-point gap to the critical target"
	c.Packages = append(c.Packages, Package{Path: "gone", Tier: "core", Minimum: 90})
	got, missing := Rebase(c, totals, day)
	if len(missing) != 1 || missing[0] != "gone" || got.Packages[2] != c.Packages[2] {
		t.Fatalf("missing %v %+v", missing, got.Packages[2])
	}
	// 75% lowers 74 to 73 and needs no reason against the 70 target.
	if p := got.Packages[0]; p.Minimum != 73 || p.Reason != "" {
		t.Fatalf("core package %+v", p)
	}
	if p := got.Packages[1]; p.Minimum != 48 || p.Reason != "measured 50.0% on 2026-11-01 without PostgreSQL; ratchet until tests close the 35.0-point gap to the critical target" {
		t.Fatalf("critical package %+v", p)
	}
	// A hand-written explanation keeps its text; only the measurement moves.
	c = config()
	c.Targets["core"] = 90
	c.Packages[0].Reason = "measured 74.2% on 2026-10-04: success paths need the image files"
	got, _ = Rebase(c, totals, day)
	if p := got.Packages[0]; p.Minimum != 73 || p.Reason != "measured 75.0% on 2026-11-01: success paths need the image files" {
		t.Fatalf("hand-written reason %+v", p)
	}
	c.Packages[0].Reason = ""
	if got, _ = Rebase(c, totals, day); !strings.HasPrefix(got.Packages[0].Reason, "measured 75.0% on 2026-11-01 without PostgreSQL") || got.Validate() != nil {
		t.Fatalf("generated reason %+v", got.Packages[0])
	}
	if c.Packages[0].Minimum != 70 {
		t.Fatal("rebase changed its input")
	}
}

func TestValidate(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"unknown tier":    func(c *Config) { c.Packages[0].Tier = "other" },
		"duplicate":       func(c *Config) { c.Packages[1].Path = "a" },
		"dot path":        func(c *Config) { c.Packages[0].Path = "./a" },
		"missing reason":  func(c *Config) { c.Packages[1].Reason = "" },
		"margin exceeded": func(c *Config) { c.Packages[0].Minimum = 67.9 },
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
	// Within the 2-point margin of the target no reason is needed.
	c := config()
	c.Packages[1].Minimum, c.Packages[1].Reason = 83, ""
	if err := c.Validate(); err != nil {
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
	if json.Unmarshal(data, &updated) != nil || updated.Packages[0].Minimum != 73 {
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
	// -rebase lowers the 76 minimum to the measured 75 minus the margin.
	out.Reset()
	if code := run([]string{"-profile", profilePath, "-config", configPath, "-rebase"}, &out, &errs); code != 0 || !strings.Contains(out.String(), "76.0% ->  73.0%") {
		t.Fatalf("rebase exit %d: %s", code, out.String())
	}
	data, _ = os.ReadFile(configPath)
	if json.Unmarshal(data, &updated) != nil || updated.Packages[0].Minimum != 73 || updated.Packages[1].Minimum != 48 {
		t.Fatalf("rebased config %s", data)
	}
	if code := run([]string{"-profile", profilePath, "-config", configPath, "-rebase", "-update"}, &out, &errs); code != 2 {
		t.Fatalf("rebase with update exit %d", code)
	}
	c = config()
	c.Packages = append(c.Packages, Package{Path: "gone", Tier: "core", Minimum: 90})
	write(c)
	out.Reset()
	if code := run([]string{"-profile", profilePath, "-config", configPath, "-rebase"}, &out, &errs); code != 1 || !strings.Contains(out.String(), "gone has no coverage data") {
		t.Fatalf("rebase with a missing package exit %d: %s", code, out.String())
	}
}
