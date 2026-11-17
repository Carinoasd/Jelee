// Command namingguard is the G21.4 parser test gate.
//
// A range that changes the media name parser (non-test Go files under
// internal/domain/medianame/) must also change its parameterised case table,
// internal/domain/medianame/naming_table_test.go. A change that cannot alter
// any parse result (a comment, a rename) may instead carry a
// "Naming-Table: unchanged <why>" trailer in one of the range's commits.
// History before the range is never checked.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const (
	parserDir = "internal/domain/medianame/"
	caseTable = parserDir + "naming_table_test.go"
)

var unchangedTrailer = regexp.MustCompile(`(?m)^Naming-Table: *unchanged +\S`)

// parserChanges returns the parser files among paths that need new cases,
// and whether the case table itself is among them.
func parserChanges(paths []string) (parser []string, table bool) {
	for _, path := range paths {
		switch {
		case path == caseTable:
			table = true
		case strings.HasPrefix(path, parserDir) && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go"):
			parser = append(parser, path)
		}
	}
	return parser, table
}

// check returns the problem of one range, or "" when it passes.
func check(paths []string, messages []string) string {
	parser, table := parserChanges(paths)
	if len(parser) == 0 || table {
		return ""
	}
	for _, message := range messages {
		if unchangedTrailer.MatchString(message) {
			return ""
		}
	}
	return fmt.Sprintf("the media name parser changed (%s) without new cases in %s; add cases, or add a \"Naming-Table: unchanged <why>\" trailer when no parse result can change", strings.Join(parser, ", "), caseTable)
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("namingguard", flag.ContinueOnError)
	flags.SetOutput(stderr)
	spec := flags.String("range", "", "revision range to check, for example origin/master..HEAD")
	root := flags.String("C", ".", "repository root")
	if err := flags.Parse(args); err != nil || *spec == "" || flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: namingguard -range <revision range> [-C <repository>]")
		return 2
	}
	diffSpec := *spec
	if !strings.Contains(diffSpec, "..") {
		// A single commit: compare it with its first parent, or the empty tree.
		if _, err := git(*root, "rev-parse", "--verify", "--quiet", diffSpec+"^"); err == nil {
			diffSpec = diffSpec + "^.." + diffSpec
		} else {
			diffSpec = "4b825dc642cb6eb9a060e54bf8d69288fbee4904.." + diffSpec
		}
	}
	names, err := git(*root, "diff", "--name-only", "--no-renames", diffSpec)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	bodies, err := git(*root, "log", "--format=%B%x1e", *spec)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if problem := check(strings.Fields(names), strings.Split(bodies, "\x1e")); problem != "" {
		_, _ = fmt.Fprintln(stderr, "namingguard:", problem)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "namingguard: ok")
	return 0
}

func git(root string, args ...string) (string, error) {
	var errBuf bytes.Buffer
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return string(out), nil
}
