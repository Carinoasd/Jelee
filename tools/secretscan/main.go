// Command secretscan is the G01.7 secret gate, written with the standard
// library only. It looks for private keys, cloud and SaaS tokens, URLs with
// embedded passwords and high-entropy values assigned to secret-like names.
//
// Modes:
//
//	secretscan                  every tracked file (work tree copy); make lint
//	secretscan -staged          staged files (index copy); pre-commit hook
//	secretscan -range A..B      lines added by the commits in A..B; CI
//	secretscan -history         lines added by every commit reachable from any ref
//
// Findings print as path, line, rule and a fingerprint (a hash of the rule
// and the matched value); the matched value itself is never printed, so the
// gate's output can be shared and logged. Known test values are accepted
// through tools/secretscan/allowlist.txt, one reviewed entry per line.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// maxFileBytes bounds the size of one scanned file; larger tracked files are
// already rejected by gitignore-check unless allowlisted.
const maxFileBytes = 16 << 20

// detector is one secret format. A line is matched only when it contains
// one of the lower-case keywords (a cheap prefilter; Go regular expressions
// are linear but slow to start at every byte). multiline detectors run on
// the whole text instead. When group > 0 the fingerprint and check use that
// capture group; nameGroup, when set, is the variable name passed to check.
type detector struct {
	id        string
	keywords  []string
	multiline bool
	re        *regexp.Regexp
	group     int
	nameGroup int
	check     func(name, value string) bool
}

var detectors = []detector{
	// PEM private keys: a header followed by key material (a bare header in a
	// redaction test is not a key).
	{id: "private-key", keywords: []string{"private key"}, multiline: true, re: regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED |PGP )?PRIVATE KEY(?: BLOCK)?-----\r?\n(?:[A-Za-z0-9+/=:, -]*\r?\n)*?[A-Za-z0-9+/=]{40,}`)},
	// A PEM key escaped inside JSON, as in a Google Cloud service account file.
	{id: "json-private-key", keywords: []string{"private_key"}, re: regexp.MustCompile(`"private_key"\s*:\s*"-----BEGIN [A-Z ]*PRIVATE KEY-----\\n[A-Za-z0-9+/=]{40,}`)},
	{id: "aws-access-key-id", keywords: []string{"akia", "asia", "abia", "acca"}, re: regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
	{id: "aws-secret-access-key", keywords: []string{"aws"}, re: regexp.MustCompile(`(?i)aws.{0,20}secret.{0,20}?[:=]\s*["']?([A-Za-z0-9/+=]{40})(?:[^A-Za-z0-9/+=]|$)`), group: 1, check: func(_, v string) bool { return highEntropy(v) }},
	{id: "github-token", keywords: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}, re: regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,255}\b`)},
	{id: "github-fine-grained-token", keywords: []string{"github_pat_"}, re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{60,255}\b`)},
	{id: "gitlab-token", keywords: []string{"glpat-"}, re: regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{id: "slack-token", keywords: []string{"xox"}, re: regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`)},
	{id: "slack-webhook", keywords: []string{"hooks.slack.com"}, re: regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Za-z0-9_]+/B[A-Za-z0-9_]+/[A-Za-z0-9_]+`)},
	{id: "google-api-key", keywords: []string{"aiza"}, re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{id: "stripe-live-key", keywords: []string{"_live_"}, re: regexp.MustCompile(`\b[rs]k_live_[0-9A-Za-z]{20,}\b`)},
	{id: "npm-token", keywords: []string{"npm_"}, re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
	{id: "telegram-bot-token", keywords: []string{":aa"}, re: regexp.MustCompile(`\b[0-9]{8,10}:AA[0-9A-Za-z_-]{33}\b`)},
	{id: "anthropic-api-key", keywords: []string{"sk-ant-"}, re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{id: "openai-api-key", keywords: []string{"sk-"}, re: regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}\b`), check: func(_, v string) bool { return highEntropy(v) }},
	{id: "jwt", keywords: []string{"eyj"}, re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{16,}`)},
	// scheme://user:password@host with a real-looking password.
	{id: "url-credentials", keywords: []string{"://"}, re: regexp.MustCompile(`\b[a-z][a-z0-9+.-]{1,20}://[^\s:/@'"<>{}]+:([^\s@/'"<>{}]{3,})@[^\s/'"<>]+`), group: 1, check: func(_, v string) bool { return notPlaceholder(v) }},
	// Quoted high-entropy value assigned to a secret-like name.
	{id: "secret-assignment", keywords: secretWords, re: regexp.MustCompile(`(?i)((?:secret|passwd|password|token|api[_-]?key|access[_-]?key|private[_-]?key|signing[_-]?key|credential)[a-z0-9_.-]*)["']?\s*(?::=|=|:|=>)\s*["'` + "`" + `]([^"'` + "`" + `\s]{16,200})["'` + "`" + `]`), group: 2, nameGroup: 1, check: likelySecret},
	// Unquoted KEY=value lines in env files, shell and YAML.
	{id: "secret-env", keywords: secretWords, re: regexp.MustCompile(`^\s*(?:export\s+)?-?\s*([A-Z0-9_]*(?:SECRET|PASSWORD|TOKEN|API_KEY|ACCESS_KEY|PRIVATE_KEY)[A-Z0-9_]*)\s*[=:]\s*([^\s"'#$]{16,200})\s*$`), group: 2, nameGroup: 1, check: likelySecret},
}

var secretWords = []string{"secret", "passwd", "password", "token", "key", "credential"}

// sourceExtensions end names that are file paths (JSON maps of file hashes),
// not variables.
var sourceExtensions = []string{".go", ".ts", ".js", ".py", ".json", ".md", ".sql", ".vue", ".ps1", ".sh", ".txt", ".yml", ".yaml"}

var placeholderWords = []string{"example", "changeme", "change-me", "placeholder", "dummy", "redacted", "your", "xxxx", "****", "...", "secret", "password", "passwd", "token"}

// identifierLike matches words with at most a short numeric suffix.
var identifierLike = regexp.MustCompile(`^[A-Za-z][A-Za-z_-]*[0-9]{0,3}$`)

// placeholderValues are whole values that only stand in for a credential.
var placeholderValues = map[string]bool{"pass": true, "pw": true, "pwd": true, "private": true, "user": true, "test": true, "x": true, "xxx": true}

// notPlaceholder rejects values that are documentation stand-ins.
func notPlaceholder(v string) bool {
	lower := strings.ToLower(v)
	if placeholderValues[lower] {
		return false
	}
	if strings.ContainsAny(v, "$%{}<>[]()") {
		return false
	}
	for _, w := range placeholderWords {
		if strings.Contains(lower, w) {
			return false
		}
	}
	return true
}

// likelySecret accepts random-looking values: high entropy, at least two
// character classes, not a placeholder, an identifier path or a file name,
// assigned to a name that is not itself a file name.
func likelySecret(name, v string) bool {
	lowerName := strings.ToLower(name)
	for _, ext := range sourceExtensions {
		if strings.HasSuffix(lowerName, ext) {
			return false
		}
	}
	if !notPlaceholder(v) || !highEntropy(v) {
		return false
	}
	// Credentials are printable ASCII without escapes; translated text
	// (raw or \uXXXX-escaped) is not.
	for _, r := range v {
		if r < 0x21 || r > 0x7e || r == '\\' {
			return false
		}
	}
	classes := 0
	for _, set := range []string{"abcdefghijklmnopqrstuvwxyz", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "0123456789"} {
		if strings.ContainsAny(v, set) {
			classes++
		}
	}
	// Random values of this length almost always contain a digit; names and
	// CamelCase identifiers (Token="ButtonTermsOfService", "ServiceIntro2")
	// do not, or only at the end.
	if classes < 2 || !strings.ContainsAny(v, "0123456789") || identifierLike.MatchString(v) {
		return false
	}
	// Dotted or slashed identifiers (pkg.Func, a/b/c.go, https URLs) are code.
	if strings.Count(v, ".") >= 2 || strings.Count(v, "/") >= 2 || strings.HasPrefix(v, "http") {
		return false
	}
	return true
}

// highEntropy reports a Shannon entropy of at least 3.5 bits per character.
func highEntropy(v string) bool { return entropy(v) >= 3.5 }

func entropy(v string) float64 {
	if v == "" {
		return 0
	}
	counts := map[rune]int{}
	for _, r := range v {
		counts[r]++
	}
	n := float64(len([]rune(v)))
	var h float64
	for _, c := range counts {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// finding is one detected value; secret is used for the fingerprint only.
type finding struct {
	commit string
	path   string
	line   int
	rule   string
	secret string
}

func (f finding) fingerprint() string {
	sum := sha256.Sum256([]byte(f.rule + "\x00" + f.secret))
	return hex.EncodeToString(sum[:8])
}

func (f finding) String() string {
	prefix := ""
	if f.commit != "" {
		prefix = "commit " + f.commit[:min(12, len(f.commit))] + " "
	}
	return fmt.Sprintf("%s%s:%d: %s [%s]", prefix, f.path, f.line, f.rule, f.fingerprint())
}

// scanText runs every detector over data. lineOf maps a 0-based line index
// to the reported line number.
func scanText(path string, data []byte, lineOf func(int) int) []finding {
	var out []finding
	lowerAll := bytes.ToLower(data)
	for _, d := range detectors {
		if !d.multiline || !containsAny(lowerAll, d.keywords) {
			continue
		}
		for _, m := range d.re.FindAllSubmatchIndex(data, -1) {
			if f, ok := d.accept(path, data, m); ok {
				f.line = lineOf(bytes.Count(data[:m[0]], []byte{'\n'}))
				out = append(out, f)
			}
		}
	}
	for i, line := range bytes.Split(data, []byte{'\n'}) {
		lower := bytes.ToLower(line)
		for _, d := range detectors {
			if d.multiline || !containsAny(lower, d.keywords) {
				continue
			}
			for _, m := range d.re.FindAllSubmatchIndex(line, -1) {
				if f, ok := d.accept(path, line, m); ok {
					f.line = lineOf(i)
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// accept turns one regular expression match into a finding unless the
// detector's check rejects it.
func (d detector) accept(path string, data []byte, m []int) (finding, bool) {
	start, end := m[0], m[1]
	if d.group > 0 {
		start, end = m[2*d.group], m[2*d.group+1]
	}
	value := string(data[start:end])
	name := ""
	if d.nameGroup > 0 && m[2*d.nameGroup] >= 0 {
		name = string(data[m[2*d.nameGroup]:m[2*d.nameGroup+1]])
	}
	if d.check != nil && !d.check(name, value) {
		return finding{}, false
	}
	return finding{path: path, rule: d.id, secret: value}, true
}

func containsAny(lower []byte, keywords []string) bool {
	for _, k := range keywords {
		if bytes.Contains(lower, []byte(k)) {
			return true
		}
	}
	return false
}

func scanFile(path string, data []byte) []finding {
	if len(data) > maxFileBytes || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil // binary or oversized: gitignore-check owns those
	}
	return scanText(path, data, func(index int) int { return index + 1 })
}

// allowEntry accepts findings by fingerprint (or * for any) of one rule (or
// *) in paths matching a glob (** crosses directories).
type allowEntry struct {
	line        int
	fingerprint string
	rule        string
	glob        *regexp.Regexp
	pattern     string
}

func parseAllowlist(text string) ([]allowEntry, error) {
	var entries []allowEntry
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return nil, fmt.Errorf("allowlist line %d: want \"<fingerprint|*> <rule|*> <path glob> <reason>\"", i+1)
		}
		fp, rule, pattern := fields[0], fields[1], fields[2]
		if fp != "*" && !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(fp) {
			return nil, fmt.Errorf("allowlist line %d: fingerprint %q is not 16 hex digits or *", i+1, fp)
		}
		if rule != "*" && !knownRule(rule) {
			return nil, fmt.Errorf("allowlist line %d: unknown rule %q", i+1, rule)
		}
		if fp == "*" && rule == "*" {
			return nil, fmt.Errorf("allowlist line %d: name a rule or a fingerprint; * * would hide everything", i+1)
		}
		entries = append(entries, allowEntry{line: i + 1, fingerprint: fp, rule: rule, glob: globRegexp(pattern), pattern: pattern})
	}
	return entries, nil
}

func knownRule(id string) bool {
	for _, d := range detectors {
		if d.id == id {
			return true
		}
	}
	return false
}

// globRegexp converts a slash glob: ** matches across directories, * and ?
// stay within one path segment.
func globRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; {
		case strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// filter splits findings into reported and allowed, marking used entries.
func filter(findings []finding, entries []allowEntry, used []bool) (reported []finding, allowed int) {
	for _, f := range findings {
		ok := false
		for i, e := range entries {
			if (e.fingerprint == "*" || e.fingerprint == f.fingerprint()) && (e.rule == "*" || e.rule == f.rule) && e.glob.MatchString(f.path) {
				used[i] = true
				ok = true
				break
			}
		}
		if ok {
			allowed++
		} else {
			reported = append(reported, f)
		}
	}
	return reported, allowed
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("secretscan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	allowPath := flags.String("allowlist", "tools/secretscan/allowlist.txt", "reviewed allowlist, relative to -root")
	historyAllowPath := flags.String("history-allowlist", "tools/secretscan/history-allowlist.txt", "reviewed findings in old history, used with -history only")
	staged := flags.Bool("staged", false, "scan the index copy of staged files")
	rangeSpec := flags.String("range", "", "scan lines added by the commits in this revision range (A..B)")
	history := flags.Bool("history", false, "scan lines added by every commit reachable from any ref")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	modes := 0
	for _, on := range []bool{*staged, *rangeSpec != "", *history} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		_, _ = fmt.Fprintln(stderr, "secretscan: -staged, -range and -history are exclusive")
		return 2
	}
	allowText, err := os.ReadFile(filepath.Join(*root, filepath.FromSlash(*allowPath)))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "secretscan: %v\n", err)
		return 2
	}
	entries, err := parseAllowlist(string(allowText))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "secretscan: %v\n", err)
		return 2
	}
	// The history allowlist covers commits that no longer touch the tree
	// (upstream history before the fork); only -history reads it, and only
	// -history can tell that one of its entries went stale.
	historyStart := len(entries)
	if *history {
		text, err := os.ReadFile(filepath.Join(*root, filepath.FromSlash(*historyAllowPath)))
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "secretscan: %v\n", err)
			return 2
		}
		more, err := parseAllowlist(string(text))
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "secretscan: %s: %v\n", *historyAllowPath, err)
			return 2
		}
		entries = append(entries, more...)
	}
	var findings []finding
	var scanned int
	mode := "tracked"
	switch {
	case *staged:
		mode = "staged"
		findings, scanned, err = scanStaged(*root)
	case *rangeSpec != "":
		mode = "range " + *rangeSpec
		findings, scanned, err = scanLog(*root, *rangeSpec)
	case *history:
		mode = "history"
		findings, scanned, err = scanLog(*root, "--all")
	default:
		findings, scanned, err = scanTracked(*root)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "secretscan: %v\n", err)
		return 2
	}
	used := make([]bool, len(entries))
	reported, allowed := filter(findings, entries, used)
	sort.SliceStable(reported, func(i, j int) bool { return reported[i].String() < reported[j].String() })
	for _, f := range reported {
		_, _ = fmt.Fprintln(stdout, f)
	}
	stale := 0
	// Only the full tracked scan sees every value of the main allowlist, and
	// only -history every value of the history allowlist.
	from, to, file := 0, historyStart, *allowPath
	if *history {
		from, to, file = historyStart, len(entries), *historyAllowPath
	}
	if mode == "tracked" || *history {
		for i := from; i < to; i++ {
			if e := entries[i]; !used[i] {
				_, _ = fmt.Fprintf(stdout, "%s line %d (%s %s %s) matches nothing; remove it\n", file, e.line, e.fingerprint, e.rule, e.pattern)
				stale++
			}
		}
	}
	unit := "files"
	if strings.HasPrefix(mode, "range") || mode == "history" {
		unit = "commits"
	}
	_, _ = fmt.Fprintf(stdout, "secret scan (%s): %s=%d findings=%d allowlisted=%d stale-allowlist=%d\n", mode, unit, scanned, len(reported), allowed, stale)
	if len(reported) > 0 {
		_, _ = fmt.Fprintln(stdout, "values are not printed; see docs/secret-leak-response.md before rotating or allowlisting")
	}
	if len(reported) > 0 || stale > 0 {
		return 1
	}
	return 0
}

func scanTracked(root string) ([]finding, int, error) {
	files, err := gitZ(root, "ls-files", "-z")
	if err != nil {
		return nil, 0, err
	}
	var findings []finding
	for _, f := range files {
		full := filepath.Join(root, filepath.FromSlash(f))
		info, err := os.Lstat(full)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, 0, err
		}
		findings = append(findings, scanFile(f, data)...)
	}
	return findings, len(files), nil
}

func scanStaged(root string) ([]finding, int, error) {
	files, err := gitZ(root, "diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR")
	if err != nil {
		return nil, 0, err
	}
	var findings []finding
	for _, f := range files {
		data, err := exec.Command("git", "-C", root, "show", ":"+f).Output()
		if err != nil {
			return nil, 0, fmt.Errorf("git show :%s: %v", f, err)
		}
		findings = append(findings, scanFile(f, data)...)
	}
	return findings, len(files), nil
}

// scanLog scans the lines added by each commit of a git log selection; merge
// commits add nothing of their own and are skipped.
func scanLog(root, selection string) ([]finding, int, error) {
	cmd := exec.Command("git", "-C", root, "log", "-p", "-U0", "--no-color", "--no-ext-diff", "--no-renames", "--no-merges", "--format=commit %H", selection)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, 0, err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	findings, commits, parseErr := parsePatch(out)
	waitErr := cmd.Wait()
	if parseErr != nil {
		return nil, 0, parseErr
	}
	if waitErr != nil {
		return nil, 0, fmt.Errorf("git log %s: %v: %s", selection, waitErr, strings.TrimSpace(errBuf.String()))
	}
	return findings, commits, nil
}

// addedBlock collects the added lines of one file in one commit.
type addedBlock struct {
	commit, path string
	text         bytes.Buffer
	lines        []int // new-file line number of each added line, in order
}

func (b *addedBlock) scan() []finding {
	if b.path == "" || len(b.lines) == 0 {
		return nil
	}
	data := b.text.Bytes()
	found := scanText(b.path, data, func(index int) int { return b.lines[min(index, len(b.lines)-1)] })
	for i := range found {
		found[i].commit = b.commit
	}
	return found
}

var hunkHeader = regexp.MustCompile(`^@@ -[0-9,]+ \+([0-9]+)(?:,[0-9]+)? @@`)

// parsePatch reads `git log -p -U0 --format="commit %H"` output.
func parsePatch(r io.Reader) ([]finding, int, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	var findings []finding
	commits := 0
	commit := ""
	block := &addedBlock{}
	next := 0
	flush := func() {
		findings = append(findings, block.scan()...)
		block = &addedBlock{commit: commit}
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "commit ") && len(line) >= 47:
			flush()
			commit = strings.TrimPrefix(line, "commit ")
			block.commit = commit
			commits++
		case strings.HasPrefix(line, "diff --git "):
			flush()
		case strings.HasPrefix(line, "+++ "):
			name := strings.TrimPrefix(line, "+++ ")
			if name == "/dev/null" {
				block.path = ""
			} else {
				block.path = unquote(strings.TrimPrefix(name, "b/"))
			}
		case strings.HasPrefix(line, "@@ "):
			if m := hunkHeader.FindStringSubmatch(line); m != nil {
				next, _ = strconv.Atoi(m[1])
			}
		case strings.HasPrefix(line, "+") && block.path != "":
			block.text.WriteString(line[1:])
			block.text.WriteByte('\n')
			block.lines = append(block.lines, next)
			next++
		}
	}
	flush()
	return findings, commits, scanner.Err()
}

// unquote decodes the C-style quoting git uses for unusual file names.
func unquote(name string) string {
	if strings.HasPrefix(name, `"b/`) {
		if s, err := strconv.Unquote(name); err == nil {
			return strings.TrimPrefix(s, "b/")
		}
	}
	return name
}

// gitZ runs git in root and splits its NUL-terminated output.
func gitZ(root string, args ...string) ([]string, error) {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v", strings.Join(args, " "), err)
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\x00") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}
