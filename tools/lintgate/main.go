// lintgate compares a golangci-lint JSON report with the committed baseline
// of pre-existing findings (G30.1, G30.5). New code is held to every enabled
// linter: any finding that is not in the baseline fails the gate. The
// baseline may only shrink: an entry that no longer occurs also fails the
// gate until -prune removes it, and -prune never adds entries.
//
//	go run ./tools/lintgate -report .testdata/golangci-lint.json -baseline tools/lint-baseline/linux.json
//
// A finding is identified by linter, file, message and a hash of its trimmed
// source line, not by line number: unrelated edits that shift a file do not
// matter, but editing a baselined line re-reports it, so touched code must be
// fixed. Exit status: 0 pass, 1 new or fixed-but-baselined findings, 2 usage
// or input error.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const schemaVersion = 1

// Issue is the subset of a golangci-lint JSON issue the gate reads.
type Issue struct {
	FromLinter  string
	Text        string
	SourceLines []string
	Pos         struct {
		Filename string
		Line     int
		Column   int
	}
}

// Report is the golangci-lint JSON output (--output.json.path).
type Report struct {
	Issues []Issue
}

// Key identifies one baselined finding independent of its line number.
type Key struct {
	Linter string `json:"linter"`
	File   string `json:"file"`
	Text   string `json:"text"`
	Source string `json:"source"`
}

// Entry is a baselined finding and how many identical findings it covers.
type Entry struct {
	Key
	Count int `json:"count"`
}

// Baseline is the committed list of accepted pre-existing findings.
type Baseline struct {
	SchemaVersion int     `json:"schemaVersion"`
	GOOS          string  `json:"goos"`
	Total         int     `json:"total"`
	Entries       []Entry `json:"entries"`
}

// KeyOf normalises an issue: slash-separated paths and a short hash of the
// whitespace-trimmed source line (CRLF checkouts hash the same).
func KeyOf(issue Issue) Key {
	source := ""
	if len(issue.SourceLines) > 0 {
		source = strings.TrimSpace(issue.SourceLines[0])
	}
	sum := sha256.Sum256([]byte(source))
	return Key{
		Linter: issue.FromLinter,
		File:   filepath.ToSlash(issue.Pos.Filename),
		Text:   issue.Text,
		Source: hex.EncodeToString(sum[:6]),
	}
}

// Counts reduces a report to a multiset of keys.
func Counts(report Report) map[Key]int {
	counts := map[Key]int{}
	for _, issue := range report.Issues {
		counts[KeyOf(issue)]++
	}
	return counts
}

// Result lists the findings outside the baseline and the baseline entries
// that no longer occur.
type Result struct {
	New   []Issue
	Fixed []Entry
}

// Compare matches the report against the baseline multiset. When a key
// occurs more often than baselined, the surplus occurrences (latest lines)
// are reported as new.
func Compare(report Report, baseline Baseline) Result {
	allowed := map[Key]int{}
	for _, e := range baseline.Entries {
		allowed[e.Key] += e.Count
	}
	seen := map[Key]int{}
	var result Result
	issues := append([]Issue(nil), report.Issues...)
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Pos.Line < issues[j].Pos.Line })
	for _, issue := range issues {
		key := KeyOf(issue)
		seen[key]++
		if seen[key] > allowed[key] {
			result.New = append(result.New, issue)
		}
	}
	for key, count := range allowed {
		if missing := count - seen[key]; missing > 0 {
			result.Fixed = append(result.Fixed, Entry{Key: key, Count: missing})
		}
	}
	result.Fixed = normalise(Baseline{Entries: result.Fixed}).Entries
	sortIssues(result.New)
	return result
}

// Prune returns the baseline restricted to findings that still occur. It can
// only lower counts; nothing outside the old baseline is ever added.
func Prune(report Report, baseline Baseline) Baseline {
	current := Counts(report)
	pruned := Baseline{SchemaVersion: schemaVersion, GOOS: baseline.GOOS}
	for _, e := range baseline.Entries {
		keep := min(e.Count, current[e.Key])
		current[e.Key] -= keep
		if keep > 0 {
			pruned.Entries = append(pruned.Entries, Entry{Key: e.Key, Count: keep})
		}
	}
	return normalise(pruned)
}

// Initial builds a baseline from a report. It is only used to create a
// missing baseline file.
func Initial(report Report, goos string) Baseline {
	b := Baseline{SchemaVersion: schemaVersion, GOOS: goos}
	for key, count := range Counts(report) {
		b.Entries = append(b.Entries, Entry{Key: key, Count: count})
	}
	return normalise(b)
}

func normalise(b Baseline) Baseline {
	merged := map[Key]int{}
	for _, e := range b.Entries {
		merged[e.Key] += e.Count
	}
	b.Entries, b.Total = b.Entries[:0], 0
	for key, count := range merged {
		b.Entries = append(b.Entries, Entry{Key: key, Count: count})
		b.Total += count
	}
	sort.Slice(b.Entries, func(i, j int) bool {
		x, y := b.Entries[i].Key, b.Entries[j].Key
		if x.File != y.File {
			return x.File < y.File
		}
		if x.Linter != y.Linter {
			return x.Linter < y.Linter
		}
		if x.Text != y.Text {
			return x.Text < y.Text
		}
		return x.Source < y.Source
	})
	return b
}

func sortIssues(issues []Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.Pos.Filename != b.Pos.Filename {
			return a.Pos.Filename < b.Pos.Filename
		}
		return a.Pos.Line < b.Pos.Line
	})
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeBaseline(path string, b Baseline) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644) //nolint:gosec // G306: committed repository file
}

// Validate rejects a baseline whose recorded total disagrees with its
// entries, so a hand edit cannot hide growth behind a stale total.
func Validate(b Baseline) error {
	if b.SchemaVersion != schemaVersion {
		return fmt.Errorf("unsupported baseline schema %d", b.SchemaVersion)
	}
	total := 0
	for _, e := range b.Entries {
		if e.Count < 1 {
			return fmt.Errorf("baseline entry with count %d", e.Count)
		}
		total += e.Count
	}
	if total != b.Total {
		return fmt.Errorf("baseline total %d does not match its entries (%d)", b.Total, total)
	}
	return nil
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("lintgate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	reportPath := flags.String("report", "", "golangci-lint JSON report")
	baselinePath := flags.String("baseline", "", "baseline JSON file")
	goos := flags.String("goos", "", "GOOS recorded in a new baseline (-init)")
	prune := flags.Bool("prune", false, "remove baseline entries that no longer occur")
	initial := flags.Bool("init", false, "create a missing baseline from the report")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *reportPath == "" || *baselinePath == "" || *prune && *initial {
		say(stderr, "lintgate: -report and -baseline are required; -prune and -init are exclusive")
		return 2
	}
	var report Report
	if err := readJSON(*reportPath, &report); err != nil {
		say(stderr, "lintgate: read report:", err)
		return 2
	}
	if *initial {
		if _, err := os.Stat(*baselinePath); !errors.Is(err, os.ErrNotExist) {
			say(stderr, "lintgate: -init refuses to replace an existing baseline; use -prune")
			return 2
		}
		b := Initial(report, *goos)
		if err := writeBaseline(*baselinePath, b); err != nil {
			say(stderr, "lintgate: write baseline:", err)
			return 2
		}
		sayf(stdout, "lintgate: created %s with %d findings\n", *baselinePath, b.Total)
		return 0
	}
	var baseline Baseline
	if err := readJSON(*baselinePath, &baseline); err != nil {
		say(stderr, "lintgate: read baseline:", err)
		return 2
	}
	if err := Validate(baseline); err != nil {
		say(stderr, "lintgate:", err)
		return 2
	}
	result := Compare(report, baseline)
	for _, issue := range result.New {
		sayf(stdout, "%s:%d:%d: %s: %s\n", filepath.ToSlash(issue.Pos.Filename), issue.Pos.Line, issue.Pos.Column, issue.FromLinter, issue.Text)
	}
	if *prune {
		pruned := Prune(report, baseline)
		if err := writeBaseline(*baselinePath, pruned); err != nil {
			say(stderr, "lintgate: write baseline:", err)
			return 2
		}
		sayf(stdout, "lintgate: baseline %d -> %d findings\n", baseline.Total, pruned.Total)
		if len(result.New) > 0 {
			sayf(stdout, "lintgate: FAIL %d new findings; fix them or add a reviewed //nolint:<linter> // reason\n", len(result.New))
			return 1
		}
		return 0
	}
	fixed := 0
	for _, e := range result.Fixed {
		fixed += e.Count
		sayf(stdout, "fixed but still baselined: %s: %s: %s\n", e.File, e.Linter, e.Text)
	}
	sayf(stdout, "lintgate: %d findings, %d baselined, %d new, %d fixed\n", len(report.Issues), baseline.Total, len(result.New), fixed)
	if len(result.New) > 0 || fixed > 0 {
		if len(result.New) > 0 {
			say(stdout, "lintgate: FAIL new findings; fix them or add a reviewed //nolint:<linter> // reason")
		}
		if fixed > 0 {
			say(stdout, "lintgate: FAIL the baseline must shrink with the fixes; run `make lint-baseline-prune`")
		}
		return 1
	}
	return 0
}

// say and sayf write diagnostics; a failed write to the terminal has no
// better place to be reported.
func say(w io.Writer, args ...any)                 { _, _ = fmt.Fprintln(w, args...) }
func sayf(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
