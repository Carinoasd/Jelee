package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func issue(linter, file string, line int, text, source string) Issue {
	i := Issue{FromLinter: linter, Text: text, SourceLines: []string{source}}
	i.Pos.Filename, i.Pos.Line = file, line
	return i
}

func report(issues ...Issue) Report { return Report{Issues: issues} }

func TestCompareNewFixedAndShifted(t *testing.T) {
	old := report(
		issue("errcheck", "a.go", 10, "unchecked", "\tf.Close()"),
		issue("revive", "b.go", 3, "exported: comment", "func X() {}"),
	)
	base := Initial(old, "linux")
	if base.Total != 2 || Validate(base) != nil {
		t.Fatalf("initial baseline %+v", base)
	}
	// Shifted lines and CRLF/indentation changes keep matching.
	shifted := report(
		issue("errcheck", "a.go", 42, "unchecked", "  f.Close()\r"),
		issue("revive", "b.go", 7, "exported: comment", "func X() {}"),
	)
	if r := Compare(shifted, base); len(r.New) != 0 || len(r.Fixed) != 0 {
		t.Fatalf("shifted report: %+v", r)
	}
	// An edited baselined line is new and its old entry counts as fixed.
	edited := report(
		issue("errcheck", "a.go", 10, "unchecked", "f.Close() // touched"),
		issue("revive", "b.go", 3, "exported: comment", "func X() {}"),
	)
	r := Compare(edited, base)
	if len(r.New) != 1 || r.New[0].Pos.Line != 10 || len(r.Fixed) != 1 || r.Fixed[0].File != "a.go" {
		t.Fatalf("edited report: %+v", r)
	}
	// A second identical finding exceeds the baselined count.
	doubled := report(old.Issues[0], old.Issues[1], issue("errcheck", "a.go", 11, "unchecked", "f.Close()"))
	if r := Compare(doubled, base); len(r.New) != 1 || r.New[0].Pos.Line != 11 {
		t.Fatalf("surplus occurrence: %+v", r)
	}
}

func TestWindowsPathsMatchSlashBaseline(t *testing.T) {
	base := Initial(report(issue("gosec", "internal/x/y.go", 1, "G115: overflow", "v := uint8(n)")), "windows")
	got := report(issue("gosec", filepath.FromSlash("internal/x/y.go"), 9, "G115: overflow", "v := uint8(n)"))
	if base.Entries[0].File != "internal/x/y.go" {
		t.Fatalf("baseline path %q", base.Entries[0].File)
	}
	if r := Compare(got, base); len(r.New) != 0 || len(r.Fixed) != 0 {
		t.Fatalf("path normalisation: %+v", r)
	}
}

func TestPruneOnlyShrinks(t *testing.T) {
	a := issue("errcheck", "a.go", 1, "unchecked", "x()")
	b := issue("errcheck", "a.go", 2, "unchecked", "y()")
	base := Initial(report(a, a, b), "linux")
	fresh := issue("revive", "c.go", 1, "unused-parameter", "func f(x int) {}")
	pruned := Prune(report(a, fresh), base)
	if pruned.Total != 1 || len(pruned.Entries) != 1 || pruned.Entries[0].Source != KeyOf(a).Source || Validate(pruned) != nil {
		t.Fatalf("pruned baseline %+v", pruned)
	}
	if again := Prune(report(a, a, b, fresh), pruned); again.Total != 1 {
		t.Fatalf("prune grew the baseline: %+v", again)
	}
}

func TestValidateRejectsHandEditedTotals(t *testing.T) {
	base := Initial(report(issue("errcheck", "a.go", 1, "unchecked", "x()")), "linux")
	base.Entries = append(base.Entries, Entry{Key: Key{Linter: "gosec", File: "b.go"}, Count: 3})
	if Validate(base) == nil {
		t.Fatal("total mismatch accepted")
	}
	base.Total = 4
	base.Entries[1].Count = 0
	if Validate(base) == nil {
		t.Fatal("zero count accepted")
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunExitCodes(t *testing.T) {
	dir := t.TempDir()
	reportPath, baselinePath := filepath.Join(dir, "report.json"), filepath.Join(dir, "baseline.json")
	old := report(issue("errcheck", "a.go", 1, "unchecked", "x()"), issue("errcheck", "a.go", 2, "unchecked", "y()"))
	writeJSON(t, reportPath, old)
	var out, errs bytes.Buffer
	if code := run([]string{"-report", reportPath, "-baseline", baselinePath, "-init", "-goos", "linux"}, &out, &errs); code != 0 {
		t.Fatalf("init exit %d: %s", code, errs.String())
	}
	if code := run([]string{"-report", reportPath, "-baseline", baselinePath, "-init"}, &out, &errs); code != 2 {
		t.Fatalf("init over an existing baseline exit %d", code)
	}
	if code := run([]string{"-report", reportPath, "-baseline", baselinePath}, &out, &errs); code != 0 {
		t.Fatalf("unchanged report exit %d: %s", code, out.String())
	}
	// A fixed finding fails until the baseline is pruned.
	writeJSON(t, reportPath, report(old.Issues[0]))
	out.Reset()
	if code := run([]string{"-report", reportPath, "-baseline", baselinePath}, &out, &errs); code != 1 || !strings.Contains(out.String(), "lint-baseline-prune") {
		t.Fatalf("fixed finding exit %d: %s", code, out.String())
	}
	if code := run([]string{"-report", reportPath, "-baseline", baselinePath, "-prune"}, &out, &errs); code != 0 {
		t.Fatalf("prune exit %d: %s", code, out.String())
	}
	if code := run([]string{"-report", reportPath, "-baseline", baselinePath}, &out, &errs); code != 0 {
		t.Fatalf("after prune exit %d: %s", code, out.String())
	}
	// A new finding fails and is printed with its position.
	writeJSON(t, reportPath, report(old.Issues[0], issue("bodyclose", "b.go", 7, "response body must be closed", "resp, err := c.Do(r)")))
	out.Reset()
	if code := run([]string{"-report", reportPath, "-baseline", baselinePath}, &out, &errs); code != 1 || !strings.Contains(out.String(), "b.go:7:0: bodyclose") {
		t.Fatalf("new finding exit %d: %s", code, out.String())
	}
	// Prune never absorbs the new finding.
	if code := run([]string{"-report", reportPath, "-baseline", baselinePath, "-prune"}, &out, &errs); code != 1 {
		t.Fatalf("prune with a new finding exit %d", code)
	}
	var b Baseline
	if err := readJSON(baselinePath, &b); err != nil || b.Total != 1 {
		t.Fatalf("baseline after prune %+v %v", b, err)
	}
	if code := run([]string{"-report", filepath.Join(dir, "missing.json"), "-baseline", baselinePath}, &out, &errs); code != 2 {
		t.Fatalf("missing report exit %d", code)
	}
}
