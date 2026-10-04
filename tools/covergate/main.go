// covergate enforces per-package statement coverage (G30.1). Each gated
// package has a tier target (core 70%, critical 85%) and a ratchet minimum:
// the gate fails when coverage drops below the minimum, and -update may only
// raise minimums toward the measured value, never lower them. Packages still
// under their tier target are listed with the remaining gap.
//
//	go test -coverprofile=.testdata/coverage.out <packages from the config>
//	go run ./tools/covergate -profile .testdata/coverage.out -config tools/coverage-thresholds.json
//
// Exit status: 0 pass, 1 coverage below a minimum or a package missing from
// the profile, 2 usage or input error.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

const schemaVersion = 1

// Package is one gated Go package, relative to the module path.
type Package struct {
	Path    string  `json:"path"`
	Tier    string  `json:"tier"`
	Minimum float64 `json:"minimum"`
	Reason  string  `json:"reason,omitempty"`
}

// Config is tools/coverage-thresholds.json.
type Config struct {
	SchemaVersion int                `json:"schemaVersion"`
	Module        string             `json:"module"`
	Targets       map[string]float64 `json:"targets"`
	Packages      []Package          `json:"packages"`
}

// Validate checks tiers, bounds and duplicates.
func (c Config) Validate() error {
	if c.SchemaVersion != schemaVersion || c.Module == "" {
		return errors.New("unsupported coverage config schema or missing module")
	}
	seen := map[string]bool{}
	for _, p := range c.Packages {
		target, ok := c.Targets[p.Tier]
		switch {
		case !ok:
			return fmt.Errorf("%s: unknown tier %q", p.Path, p.Tier)
		case p.Path == "" || seen[p.Path] || path.Clean(p.Path) != p.Path || strings.HasPrefix(p.Path, "."):
			return fmt.Errorf("invalid or duplicate package path %q", p.Path)
		case p.Minimum < 0 || p.Minimum > 100 || target <= 0 || target > 100:
			return fmt.Errorf("%s: minimum %.1f or target %.1f out of range", p.Path, p.Minimum, target)
		case p.Minimum < target && p.Reason == "":
			return fmt.Errorf("%s: a minimum below the %s target needs a reason", p.Path, p.Tier)
		}
		seen[p.Path] = true
	}
	return nil
}

type block struct {
	statements int
	covered    bool
}

// Parse reads a Go cover profile and returns covered and total statements
// per package import path. Repeated blocks (several runs) are merged.
func Parse(r io.Reader) (map[string][2]int, error) {
	blocks := map[string]block{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if first {
			first = false
			if !strings.HasPrefix(line, "mode:") {
				return nil, errors.New("not a cover profile")
			}
			continue
		}
		if line == "" {
			continue
		}
		// file.go:startLine.startCol,endLine.endCol numStatements count
		fields := strings.Fields(line)
		if len(fields) != 3 || !strings.Contains(fields[0], ":") {
			return nil, fmt.Errorf("malformed profile line %q", line)
		}
		statements, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || statements < 0 || count < 0 {
			return nil, fmt.Errorf("malformed profile counts %q", line)
		}
		b := blocks[fields[0]]
		b.statements = statements
		b.covered = b.covered || count > 0
		blocks[fields[0]] = b
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if first {
		return nil, errors.New("empty cover profile")
	}
	totals := map[string][2]int{}
	for key, b := range blocks {
		file := key[:strings.LastIndex(key, ":")]
		pkg := path.Dir(file)
		t := totals[pkg]
		if b.covered {
			t[0] += b.statements
		}
		t[1] += b.statements
		totals[pkg] = t
	}
	return totals, nil
}

// Percent is go test's statement coverage, 0 for a package without
// statements.
func Percent(t [2]int) float64 {
	if t[1] == 0 {
		return 0
	}
	return 100 * float64(t[0]) / float64(t[1])
}

// ratchetFloor is the minimum recorded for a measured value: whole percent
// points, rounded down. It absorbs run-to-run and host-to-host noise below
// one point (timing-dependent branches, tests that only run where an
// optional tool exists) while any real drop still fails.
func ratchetFloor(v float64) float64 { return math.Floor(v + 1e-9) }

// Row is one line of the coverage report.
type Row struct {
	Package
	Target  float64
	Current float64
	Found   bool
}

// Evaluate compares measured coverage with the config.
func Evaluate(c Config, totals map[string][2]int) (rows []Row, failed bool) {
	for _, p := range c.Packages {
		t, found := totals[c.Module+"/"+p.Path]
		row := Row{Package: p, Target: c.Targets[p.Tier], Current: Percent(t), Found: found}
		if !found || row.Current < p.Minimum {
			failed = true
		}
		rows = append(rows, row)
	}
	return rows, failed
}

// Ratchet raises each minimum to the floored measured value when higher.
func Ratchet(c Config, totals map[string][2]int) Config {
	out := c
	out.Packages = append([]Package(nil), c.Packages...)
	for i, p := range out.Packages {
		if t, ok := totals[c.Module+"/"+p.Path]; ok {
			if v := ratchetFloor(Percent(t)); v > p.Minimum {
				out.Packages[i].Minimum = v
			}
		}
	}
	return out
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("covergate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	profilePath := flags.String("profile", "", "go test -coverprofile output")
	configPath := flags.String("config", "", "coverage thresholds JSON")
	update := flags.Bool("update", false, "raise minimums to the measured coverage (never lowers)")
	list := flags.Bool("list", false, "print the gated packages as ./path arguments and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || !*list && *profilePath == "" {
		say(stderr, "covergate: -config and -profile are required")
		return 2
	}
	var config Config
	data, err := os.ReadFile(*configPath)
	if err == nil {
		err = json.Unmarshal(data, &config)
	}
	if err == nil {
		err = config.Validate()
	}
	if err != nil {
		say(stderr, "covergate: config:", err)
		return 2
	}
	if *list {
		for _, p := range config.Packages {
			say(stdout, "./"+p.Path)
		}
		return 0
	}
	profile, err := os.Open(*profilePath)
	if err != nil {
		say(stderr, "covergate: profile:", err)
		return 2
	}
	totals, err := Parse(profile)
	_ = profile.Close()
	if err != nil {
		say(stderr, "covergate: profile:", err)
		return 2
	}
	rows, failed := Evaluate(config, totals)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Tier < rows[j].Tier })
	sayf(stdout, "%-36s %-8s %8s %8s %7s %s\n", "package", "tier", "current", "minimum", "target", "gap")
	for _, r := range rows {
		status := ""
		switch {
		case !r.Found:
			status = "  FAIL: no coverage data (package not tested?)"
		case r.Current < r.Minimum:
			status = "  FAIL: below ratchet minimum"
		case r.Current < r.Target:
			status = fmt.Sprintf("  %.1f points below target", r.Target-r.Current)
		}
		sayf(stdout, "%-36s %-8s %7.1f%% %7.1f%% %6.0f%%%s\n", r.Path, r.Tier, r.Current, r.Minimum, r.Target, status)
	}
	if *update {
		if failed {
			say(stdout, "covergate: FAIL; -update never lowers a minimum")
			return 1
		}
		data, err := json.MarshalIndent(Ratchet(config, totals), "", "  ")
		if err == nil {
			err = os.WriteFile(*configPath, append(data, '\n'), 0o644) //nolint:gosec // G306: committed repository file
		}
		if err != nil {
			say(stderr, "covergate: write config:", err)
			return 2
		}
		say(stdout, "covergate: minimums raised to measured coverage where higher")
		return 0
	}
	if failed {
		say(stdout, "covergate: FAIL coverage below the ratchet minimum; add tests (minimums never go down)")
		return 1
	}
	say(stdout, "covergate: PASS")
	return 0
}

// say and sayf write diagnostics; a failed write to the terminal has no
// better place to be reported.
func say(w io.Writer, args ...any)                 { _, _ = fmt.Fprintln(w, args...) }
func sayf(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
