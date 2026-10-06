// Command commitlint is the G01.2 commit message gate.
//
// Headers follow Conventional Commits with the types allowed by G01.2:
//
//	<type>[(scope[,scope])][!]: <description>
//	type  = feat|fix|refactor|perf|chore|docs|test|build|ci
//	scope = a lower-case module name (letters, digits, . _ / -)
//	!     = breaking change (a "BREAKING CHANGE:" footer works too)
//
// Messages Git writes itself ("Merge ...", "Revert \"...\"", "Reapply
// \"...\"") are accepted. With -message-file (the commit-msg hook) the
// autosquash prefixes "fixup! ", "squash! " and "amend! " are accepted too;
// in -range (CI) they are not, because they must be squashed before merging.
//
// -range also measures each non-merge commit. A commit that changes more
// files or lines than the thresholds (generated files excluded) needs a
// "Large-Change: <why this is one goal>" trailer; without it the commit is
// reported as a warning, or fails with -size-mode=fail. History before the
// range is never checked.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var (
	header     = regexp.MustCompile(`^(feat|fix|refactor|perf|chore|docs|test|build|ci)(\([a-z0-9][a-z0-9._/-]*(?:,[a-z0-9][a-z0-9._/-]*)*\))?(!)?: \S`)
	gitMade    = regexp.MustCompile(`^(Merge .+|Revert ".+"|Reapply ".+")$`)
	autosquash = regexp.MustCompile(`^(fixup|squash|amend)! .+`)
	typeOnly   = regexp.MustCompile(`^([A-Za-z]+)(\(.*\))?!?:`)
	largeTrail = regexp.MustCompile(`(?m)^Large-Change: *\S`)
)

var allowedTypes = "feat|fix|refactor|perf|chore|docs|test|build|ci"

// generatedPaths do not count towards the size of a commit: they change
// with their generators, not with the goal of the commit.
var generatedPaths = []string{"api/openapi.json", "web/src/api/schema.d.ts", "package-lock.json", "web/e2e/__screenshots__/", "docs/evidence/", "tools/lint-baseline/"}

// checkMessage returns the problems of one commit message.
func checkMessage(message string, allowAutosquash bool) []string {
	lines := strings.Split(strings.TrimRight(message, "\n"), "\n")
	subject := strings.TrimRight(lines[0], " \t")
	if subject == "" {
		return []string{"empty subject line"}
	}
	if gitMade.MatchString(subject) || allowAutosquash && autosquash.MatchString(subject) {
		return nil
	}
	var problems []string
	if !header.MatchString(subject) {
		problems = append(problems, describe(subject))
	}
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" {
		problems = append(problems, "leave a blank line between the subject and the body")
	}
	return problems
}

// describe explains why a subject is not a Conventional Commits header.
func describe(subject string) string {
	m := typeOnly.FindStringSubmatch(subject)
	switch {
	case m == nil:
		return fmt.Sprintf("subject %q must start with <type>[(scope)][!]: where type is %s", subject, allowedTypes)
	case !regexp.MustCompile(`^(` + allowedTypes + `)$`).MatchString(m[1]):
		return fmt.Sprintf("type %q is not one of %s", m[1], allowedTypes)
	case m[2] != "" && !regexp.MustCompile(`^\([a-z0-9][a-z0-9._/-]*(?:,[a-z0-9][a-z0-9._/-]*)*\)$`).MatchString(m[2]):
		return fmt.Sprintf("scope %s must be lower-case module names (letters, digits, . _ / -), comma separated", m[2])
	default:
		return "the colon must be followed by one space and a description"
	}
}

// stripComments drops the lines Git removes from an edited message.
func stripComments(message string) string {
	var kept []string
	for _, line := range strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "# ------------------------ >8 ------------------------") {
			break
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimLeft(strings.Join(kept, "\n"), "\n")
}

type commit struct {
	sha     string
	parents int
	message string
}

type size struct {
	files, lines int
}

// measure counts changed files and lines of one commit, without generated paths.
func measure(numstat string) size {
	var s size
	for _, line := range strings.Split(numstat, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 || generated(fields[2]) {
			continue
		}
		s.files++
		added, errA := strconv.Atoi(fields[0])
		deleted, errD := strconv.Atoi(fields[1])
		if errA == nil && errD == nil { // binary files show "-"
			s.lines += added + deleted
		}
	}
	return s
}

func generated(path string) bool {
	for _, g := range generatedPaths {
		if path == g || strings.HasSuffix(g, "/") && strings.HasPrefix(path, g) {
			return true
		}
	}
	return false
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("commitlint", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	messageFile := flags.String("message-file", "", "check one message file (commit-msg hook)")
	message := flags.String("message", "", "check one message given on the command line")
	rangeSpec := flags.String("range", "", "check every commit in this revision range (A..B); history before A is not checked")
	maxFiles := flags.Int("max-files", 80, "files changed above which a commit needs a Large-Change trailer")
	maxLines := flags.Int("max-lines", 8000, "lines changed above which a commit needs a Large-Change trailer")
	sizeMode := flags.String("size-mode", "warn", "warn or fail when a large commit has no Large-Change trailer")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *sizeMode != "warn" && *sizeMode != "fail" {
		_, _ = fmt.Fprintln(stderr, "commitlint: -size-mode must be warn or fail")
		return 2
	}
	set := 0
	for _, v := range []string{*messageFile, *message, *rangeSpec} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		_, _ = fmt.Fprintln(stderr, "commitlint: give exactly one of -message-file, -message or -range")
		return 2
	}
	switch {
	case *messageFile != "":
		data, err := os.ReadFile(*messageFile)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "commitlint: %v\n", err)
			return 2
		}
		text := stripComments(string(data))
		if strings.TrimSpace(text) == "" {
			return 0 // git aborts an empty message itself
		}
		return report(stdout, "commit message", checkMessage(text, true))
	case *message != "":
		return report(stdout, "commit message", checkMessage(*message, true))
	}
	commits, err := listCommits(*root, *rangeSpec)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "commitlint: %v\n", err)
		return 2
	}
	failed, warned := 0, 0
	annotate := os.Getenv("GITHUB_ACTIONS") == "true"
	for _, c := range commits {
		short := c.sha[:min(12, len(c.sha))]
		for _, p := range checkMessage(c.message, false) {
			_, _ = fmt.Fprintf(stdout, "%s: %s\n", short, p)
			if annotate {
				_, _ = fmt.Fprintf(stdout, "::error title=Commit message %s::%s\n", short, p)
			}
			failed++
		}
		if c.parents > 1 {
			continue
		}
		numstat, err := git(*root, "diff-tree", "--no-commit-id", "--numstat", "-r", "--root", "--no-renames", c.sha)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "commitlint: %v\n", err)
			return 2
		}
		s := measure(numstat)
		if (s.files > *maxFiles || s.lines > *maxLines) && !largeTrail.MatchString(c.message) {
			msg := fmt.Sprintf("%d files / %d lines exceeds %d files or %d lines: split unrelated goals into separate commits, or add a \"Large-Change: <reason>\" trailer", s.files, s.lines, *maxFiles, *maxLines)
			_, _ = fmt.Fprintf(stdout, "%s: large commit: %s\n", short, msg)
			if annotate {
				level := "warning"
				if *sizeMode == "fail" {
					level = "error"
				}
				_, _ = fmt.Fprintf(stdout, "::%s title=Large commit %s::%s\n", level, short, msg)
			}
			if *sizeMode == "fail" {
				failed++
			} else {
				warned++
			}
		}
	}
	_, _ = fmt.Fprintf(stdout, "commit lint (%s): commits=%d problems=%d size-warnings=%d\n", *rangeSpec, len(commits), failed, warned)
	if failed > 0 {
		_, _ = fmt.Fprintln(stdout, "see docs/git-workflow.md#提交规范; reword with `git rebase -i` before the branch is pushed or merged")
		return 1
	}
	return 0
}

func report(stdout io.Writer, what string, problems []string) int {
	for _, p := range problems {
		_, _ = fmt.Fprintf(stdout, "%s: %s\n", what, p)
	}
	if len(problems) > 0 {
		_, _ = fmt.Fprintf(stdout, "expected <type>[(scope)][!]: <description> with type %s; see docs/git-workflow.md\n", allowedTypes)
		return 1
	}
	return 0
}

// listCommits returns the commits of a range, oldest first.
func listCommits(root, spec string) ([]commit, error) {
	out, err := git(root, "log", "--reverse", "--format=%H%x1f%P%x1f%B%x1e", spec)
	if err != nil {
		return nil, err
	}
	var commits []commit
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimLeft(record, "\n")
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\x1f", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected git log record %q", record)
		}
		commits = append(commits, commit{sha: fields[0], parents: len(strings.Fields(fields[1])), message: fields[2]})
	}
	return commits, nil
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
