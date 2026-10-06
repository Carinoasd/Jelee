// Command gitignore-check is the G01.4/G01.4b/G01.4c repository gate.
//
// It checks four things and fails on any violation:
//
//  1. .gitignore structure: eight numbered groups, a comment line directly
//     above every pattern, and every pattern listed in docs/toolchain.md.
//  2. Ignore semantics: every probe in tools/gitignore-check/probes.txt is
//     ignored or kept as declared (git check-ignore in an isolated scratch
//     repository), and every pattern decides at least one probe.
//  3. Tracked files: no file is tracked below a generated directory or
//     against the ignore rules, and every binary, large (> 1 MiB), archive,
//     media, database, key or executable file is covered by an entry of
//     docs/binary-allowlist.md (kind and size limit). Entries that match no
//     tracked file fail too, so the list cannot go stale.
//  4. Untracked files that are not ignored (what `git add -A` would pick up)
//     get the same content checks.
//
// With -list it also enumerates ignored and untracked-not-ignored files.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// largeFileLimit is the size above which a tracked file needs an allowlist
// entry of kind "large" (G01.4b).
const largeFileLimit = 1 << 20

// Kinds of findings. Allowlistable kinds can be accepted by an entry of
// docs/binary-allowlist.md; the others never can.
const (
	kindBinary     = "binary"
	kindLarge      = "large"
	kindArchive    = "archive"
	kindMedia      = "media"
	kindDatabase   = "database"
	kindKey        = "key"
	kindExecutable = "executable"
	kindGenerated  = "generated"
	kindIgnored    = "ignored"
	kindPrivateKey = "private-key"
)

var allowlistable = map[string]bool{
	kindBinary: true, kindLarge: true, kindArchive: true, kindMedia: true,
	kindDatabase: true, kindKey: true, kindExecutable: true,
}

// generatedPrefixes are directories whose content is always produced locally.
var generatedPrefixes = []string{".tools/", ".bin/", ".testdata/", ".testfixtures/", ".cache/", ".gocache/", "node_modules/", "data/", "bin/", "dist/", "tools/vendor-downloads/"}

var extensionKinds = map[string]string{
	".zip": kindArchive, ".gz": kindArchive, ".tgz": kindArchive, ".xz": kindArchive, ".bz2": kindArchive,
	".zst": kindArchive, ".7z": kindArchive, ".rar": kindArchive, ".tar": kindArchive, ".deb": kindArchive,
	".rpm": kindArchive, ".msi": kindArchive, ".jar": kindArchive,
	".mp4": kindMedia, ".mkv": kindMedia, ".webm": kindMedia, ".avi": kindMedia, ".mov": kindMedia,
	".m4v": kindMedia, ".mp3": kindMedia, ".flac": kindMedia, ".wav": kindMedia, ".m4a": kindMedia,
	".ogg": kindMedia, ".opus": kindMedia, ".m2ts": kindMedia, ".iso": kindMedia,
	".db": kindDatabase, ".sqlite": kindDatabase, ".sqlite3": kindDatabase, ".dump": kindDatabase,
	".pem": kindKey, ".key": kindKey, ".p12": kindKey, ".pfx": kindKey, ".jks": kindKey, ".keystore": kindKey,
	".exe": kindExecutable, ".dll": kindExecutable, ".so": kindExecutable, ".dylib": kindExecutable,
}

// privateKeyBlock is a PEM private key header followed by key material; a
// bare header (redaction tests) is not a key. tools/secretscan covers other
// credential formats.
var privateKeyBlock = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY` + `-----\r?\n(?:[A-Za-z0-9+/=:, -]*\r?\n)*?[A-Za-z0-9+/=]{48,}`)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type config struct {
	root      string
	gitignore string
	toolchain string
	allowlist string
	probes    string
	list      bool
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gitignore-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var cfg config
	flags.StringVar(&cfg.root, "root", ".", "repository root")
	flags.StringVar(&cfg.gitignore, "gitignore", ".gitignore", "ignore file, relative to -root")
	flags.StringVar(&cfg.toolchain, "toolchain-doc", "docs/toolchain.md", "document that must list every pattern")
	flags.StringVar(&cfg.allowlist, "allowlist", "docs/binary-allowlist.md", "binary and large file allowlist")
	flags.StringVar(&cfg.probes, "probes", "tools/gitignore-check/probes.txt", "ignored/kept probe paths")
	flags.BoolVar(&cfg.list, "list", false, "also enumerate ignored and untracked-not-ignored files")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	report, err := check(cfg, stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gitignore-check: %v\n", err)
		return 2
	}
	for _, v := range report.violations {
		_, _ = fmt.Fprintln(stdout, v)
	}
	_, _ = fmt.Fprintf(stdout, "gitignore check: rules=%d probes=%d tracked=%d allowlisted=%d untracked=%d violations=%d\n",
		report.rules, report.probes, report.tracked, report.allowlisted, report.untracked, len(report.violations))
	if len(report.violations) > 0 {
		return 1
	}
	return 0
}

type report struct {
	rules, probes, tracked, allowlisted, untracked int
	violations                                     []string
}

func (r *report) fail(format string, args ...any) {
	r.violations = append(r.violations, fmt.Sprintf(format, args...))
}

func check(cfg config, stdout io.Writer) (*report, error) {
	r := &report{}
	ignoreText, err := os.ReadFile(filepath.Join(cfg.root, cfg.gitignore))
	if err != nil {
		return nil, err
	}
	rules, problems := parseGitignore(string(ignoreText))
	r.rules = len(rules)
	for _, p := range problems {
		r.fail(".gitignore: %s", p)
	}
	doc, err := os.ReadFile(filepath.Join(cfg.root, cfg.toolchain))
	if err != nil {
		return nil, err
	}
	for _, missing := range undocumented(rules, string(doc)) {
		r.fail(".gitignore pattern %q is not listed in %s", missing, cfg.toolchain)
	}
	probeText, err := os.ReadFile(filepath.Join(cfg.root, cfg.probes))
	if err != nil {
		return nil, err
	}
	probes, err := parseProbes(string(probeText))
	if err != nil {
		return nil, err
	}
	r.probes = len(probes)
	results, err := evaluateProbes(string(ignoreText), probes)
	if err != nil {
		return nil, err
	}
	for _, p := range probeProblems(rules, probes, results) {
		r.fail("%s", p)
	}

	allowText, err := os.ReadFile(filepath.Join(cfg.root, cfg.allowlist))
	if err != nil {
		return nil, err
	}
	entries, problems := parseAllowlist(string(allowText))
	for _, p := range problems {
		r.fail("%s: %s", cfg.allowlist, p)
	}
	tracked, err := trackedFiles(cfg.root)
	if err != nil {
		return nil, err
	}
	r.tracked = len(tracked)
	ignoredTracked, err := gitLines(cfg.root, "ls-files", "-z", "-c", "-i", "--exclude-per-directory=.gitignore")
	if err != nil {
		return nil, err
	}
	isIgnored := map[string]bool{}
	for _, p := range ignoredTracked {
		isIgnored[p] = true
	}
	used := make([]bool, len(entries))
	for _, f := range tracked {
		if f.mode == "120000" || f.mode == "160000" {
			continue // symlinks and submodules carry no file content here
		}
		data, err := os.ReadFile(filepath.Join(cfg.root, filepath.FromSlash(f.path)))
		if errors.Is(err, os.ErrNotExist) {
			continue // deleted in the work tree; the deletion is what gets committed
		}
		if err != nil {
			return nil, err
		}
		kinds := classify(f.path, f.mode == "100755", data)
		if isIgnored[f.path] {
			kinds = append(kinds, kindIgnored)
		}
		if judge(r, f.path, int64(len(data)), kinds, entries, used, "tracked") {
			r.allowlisted++
		}
	}
	for i, e := range entries {
		if !used[i] {
			r.fail("%s: entry `%s` matches no tracked file; remove it", cfg.allowlist, e.pattern)
		}
	}

	untracked, err := gitLines(cfg.root, "ls-files", "-z", "-o", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	r.untracked = len(untracked)
	for _, p := range untracked {
		full := filepath.Join(cfg.root, filepath.FromSlash(p))
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		judge(r, p, info.Size(), classify(p, info.Mode()&0o111 != 0, data), entries, nil, "untracked")
	}

	if cfg.list {
		ignored, err := gitLines(cfg.root, "ls-files", "-z", "-o", "-i", "--exclude-standard", "--directory")
		if err != nil {
			return nil, err
		}
		for _, p := range ignored {
			_, _ = fmt.Fprintf(stdout, "ignored\t%s\n", p)
		}
		for _, p := range untracked {
			_, _ = fmt.Fprintf(stdout, "not-ignored\t%s\n", p)
		}
		_, _ = fmt.Fprintf(stdout, "listed: ignored=%d not-ignored-untracked=%d tracked=%d\n", len(ignored), len(untracked), len(tracked))
	}
	return r, nil
}

// judge records violations for kinds that no allowlist entry accepts. It
// returns true when the file had findings and all of them were allowlisted.
func judge(r *report, file string, size int64, kinds []string, entries []allowEntry, used []bool, state string) bool {
	if len(kinds) == 0 {
		return false
	}
	accepted := true
	for _, kind := range kinds {
		idx := -1
		if allowlistable[kind] {
			idx = matchAllowlist(entries, file, kind, size)
		}
		if idx < 0 {
			accepted = false
			r.fail("%s file %s: %s (%d bytes)%s", state, file, kind, size, hint(kind))
			continue
		}
		if used != nil {
			used[idx] = true
		}
	}
	return accepted
}

func hint(kind string) string {
	switch kind {
	case kindGenerated:
		return "; generated directories must stay untracked"
	case kindIgnored:
		return "; tracked against .gitignore, add a negation rule or untrack it"
	case kindPrivateKey:
		return "; never commit private keys (docs/secret-leak-response.md)"
	default:
		return "; needs an entry in docs/binary-allowlist.md"
	}
}

// classify returns the finding kinds of one file, deduplicated and sorted.
func classify(file string, executableBit bool, data []byte) []string {
	set := map[string]bool{}
	for _, prefix := range generatedPrefixes {
		if strings.HasPrefix(file, prefix) {
			set[kindGenerated] = true
		}
	}
	if kind, ok := extensionKinds[strings.ToLower(path.Ext(file))]; ok {
		set[kind] = true
	}
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		set[kindBinary] = true
	}
	if len(data) > largeFileLimit {
		set[kindLarge] = true
	}
	if nativeExecutable(data) || executableBit && !bytes.HasPrefix(data, []byte("#!")) {
		set[kindExecutable] = true
	}
	if privateKeyBlock.Match(data) {
		set[kindPrivateKey] = true
	}
	kinds := make([]string, 0, len(set))
	for k := range set {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// nativeExecutable recognises ELF, PE and Mach-O headers.
func nativeExecutable(data []byte) bool {
	magics := [][]byte{
		{0x7f, 'E', 'L', 'F'},
		{'M', 'Z'},
		{0xfe, 0xed, 0xfa, 0xce}, {0xfe, 0xed, 0xfa, 0xcf},
		{0xce, 0xfa, 0xed, 0xfe}, {0xcf, 0xfa, 0xed, 0xfe},
		{0xca, 0xfe, 0xba, 0xbe},
	}
	for _, m := range magics {
		if bytes.HasPrefix(data, m) {
			// "MZ" alone is too weak for text files; require a binary byte too.
			if m[0] == 'M' && bytes.IndexByte(data[:min(len(data), 512)], 0) < 0 {
				continue
			}
			return true
		}
	}
	return false
}

// rule is one pattern line of .gitignore.
type rule struct {
	line    int
	pattern string
}

// parseGitignore returns the pattern lines and structural problems: eight
// "## N." group headings in order, every pattern inside a group and directly
// preceded by a "#" comment line, and no duplicate patterns.
func parseGitignore(text string) ([]rule, []string) {
	var rules []rule
	var problems []string
	group := 0
	previousComment := false
	seen := map[string]int{}
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " \t")
		n := i + 1
		switch {
		case strings.HasPrefix(line, "## "):
			heading := strings.TrimPrefix(line, "## ")
			if dot := strings.Index(heading, ". "); dot > 0 {
				if number, err := strconv.Atoi(heading[:dot]); err == nil {
					if number != group+1 {
						problems = append(problems, fmt.Sprintf("line %d: group %d follows group %d", n, number, group))
					}
					group = number
				}
			}
			previousComment = false
		case strings.HasPrefix(line, "#"):
			previousComment = strings.TrimSpace(strings.TrimLeft(line, "#")) != ""
		case line == "":
			previousComment = false
		default:
			rules = append(rules, rule{line: n, pattern: line})
			if group == 0 {
				problems = append(problems, fmt.Sprintf("line %d: pattern %q is outside the numbered groups", n, line))
			}
			if !previousComment {
				problems = append(problems, fmt.Sprintf("line %d: pattern %q needs its own comment line directly above", n, line))
			}
			if first, dup := seen[line]; dup {
				problems = append(problems, fmt.Sprintf("line %d: pattern %q repeats line %d", n, line, first))
			}
			seen[line] = n
			previousComment = false
		}
	}
	if group != 8 {
		problems = append(problems, fmt.Sprintf("expected the eight G01.4 groups, found %d", group))
	}
	return rules, problems
}

// undocumented lists patterns that the toolchain document does not mention
// as `pattern`.
func undocumented(rules []rule, doc string) []string {
	var missing []string
	for _, r := range rules {
		if !strings.Contains(doc, "`"+r.pattern+"`") {
			missing = append(missing, r.pattern)
		}
	}
	return missing
}

type probe struct {
	line    int
	ignored bool
	path    string
}

// parseProbes reads "ignored <path>" and "kept <path>" lines.
func parseProbes(text string) ([]probe, error) {
	var probes []probe
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		verdict, p, ok := strings.Cut(line, " ")
		p = strings.TrimSpace(p)
		if !ok || p == "" || verdict != "ignored" && verdict != "kept" {
			return nil, fmt.Errorf("probes line %d: want \"ignored <path>\" or \"kept <path>\"", i+1)
		}
		probes = append(probes, probe{line: i + 1, ignored: verdict == "ignored", path: p})
	}
	return probes, nil
}

// probeResult is git's verdict for one probe: the deciding pattern line (0
// when nothing matched) and whether the path is ignored.
type probeResult struct {
	ruleLine int
	pattern  string
	ignored  bool
}

// evaluateProbes runs git check-ignore against the given .gitignore content in
// a scratch repository, isolated from the user's global and system excludes
// and from links in the real work tree (worktrees link .tools).
func evaluateProbes(gitignore string, probes []probe) ([]probeResult, error) {
	dir, err := os.MkdirTemp("", "jelee-gitignore-check-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	env := append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "XDG_CONFIG_HOME="+dir, "HOME="+dir)
	repo := filepath.Join(dir, "repo")
	initRepo := exec.Command("git", "init", "-q", repo)
	initRepo.Env = env
	if out, err := initRepo.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(gitignore), 0o600); err != nil { //nolint:gosec // G703: fixed name inside our own MkdirTemp directory

		return nil, err
	}
	var input bytes.Buffer
	for _, p := range probes {
		input.WriteString(p.path)
		input.WriteByte(0)
	}
	cmd := exec.Command("git", "-C", repo, "check-ignore", "--no-index", "--stdin", "-z", "-v", "-n")
	cmd.Env = env
	cmd.Stdin = &input
	out, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, fmt.Errorf("git check-ignore: %v", err)
	}
	fields := strings.Split(string(out), "\x00")
	byPath := map[string]probeResult{}
	for i := 0; i+3 < len(fields); i += 4 {
		lineNo, _ := strconv.Atoi(fields[i+1])
		pattern := fields[i+2]
		byPath[fields[i+3]] = probeResult{ruleLine: lineNo, pattern: pattern, ignored: pattern != "" && !strings.HasPrefix(pattern, "!")}
	}
	results := make([]probeResult, len(probes))
	for i, p := range probes {
		res, ok := byPath[p.path]
		if !ok {
			return nil, fmt.Errorf("git check-ignore gave no verdict for %q", p.path)
		}
		results[i] = res
	}
	return results, nil
}

// probeProblems compares verdicts with expectations and requires every rule
// to decide at least one probe.
func probeProblems(rules []rule, probes []probe, results []probeResult) []string {
	var problems []string
	decided := map[int]bool{}
	for i, p := range probes {
		res := results[i]
		if res.ruleLine > 0 {
			decided[res.ruleLine] = true
		}
		if res.ignored != p.ignored {
			want := "kept"
			if p.ignored {
				want = "ignored"
			}
			problems = append(problems, fmt.Sprintf("probe line %d: %s should be %s (deciding rule: %q)", p.line, p.path, want, res.pattern))
		}
	}
	for _, r := range rules {
		if !decided[r.line] {
			problems = append(problems, fmt.Sprintf(".gitignore line %d: pattern %q decides no probe; add one to the probe list", r.line, r.pattern))
		}
	}
	return problems
}

// allowEntry is one row of docs/binary-allowlist.md.
type allowEntry struct {
	pattern  string
	kinds    map[string]bool
	maxBytes int64
}

// allowlistHeading starts the section whose table holds the entries.
const allowlistHeading = "## 当前例外"

// parseAllowlist reads the rows of the table below allowlistHeading whose
// first cell is a backticked path pattern:
// | `pattern` | kind, kind | max bytes | count and size | purpose | source |
func parseAllowlist(text string) ([]allowEntry, []string) {
	var entries []allowEntry
	var problems []string
	scanner := bufio.NewScanner(strings.NewReader(text))
	n := 0
	inSection, sawSection := false, false
	for scanner.Scan() {
		n++
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "## ") {
			inSection = line == allowlistHeading
			sawSection = sawSection || inSection
			continue
		}
		if !inSection || !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if len(cells) < 6 {
			problems = append(problems, fmt.Sprintf("line %d: want 6 columns (pattern, kinds, max bytes, count and size, purpose, source)", n))
			continue
		}
		pattern := strings.Trim(cells[0], "`")
		if _, err := path.Match(pattern, ""); err != nil || pattern == "" {
			problems = append(problems, fmt.Sprintf("line %d: bad pattern %q", n, pattern))
			continue
		}
		entry := allowEntry{pattern: pattern, kinds: map[string]bool{}}
		for _, k := range strings.Split(cells[1], ",") {
			k = strings.Trim(strings.TrimSpace(k), "`")
			if !allowlistable[k] {
				problems = append(problems, fmt.Sprintf("line %d: kind %q cannot be allowlisted", n, k))
				continue
			}
			entry.kinds[k] = true
		}
		limit, err := strconv.ParseInt(strings.ReplaceAll(strings.Trim(cells[2], "`"), ",", ""), 10, 64)
		if err != nil || limit <= 0 {
			problems = append(problems, fmt.Sprintf("line %d: max bytes %q is not a positive integer", n, cells[2]))
			continue
		}
		entry.maxBytes = limit
		if cells[3] == "" || cells[4] == "" || cells[5] == "" {
			problems = append(problems, fmt.Sprintf("line %d: count and size, purpose and source must not be empty", n))
			continue
		}
		entries = append(entries, entry)
	}
	if !sawSection {
		problems = append(problems, "missing section "+allowlistHeading)
	}
	return entries, problems
}

// matchAllowlist returns the first entry accepting file for kind, or -1.
func matchAllowlist(entries []allowEntry, file, kind string, size int64) int {
	for i, e := range entries {
		if ok, _ := path.Match(e.pattern, file); ok && e.kinds[kind] && size <= e.maxBytes {
			return i
		}
	}
	return -1
}

type trackedFile struct {
	mode string
	path string
}

func trackedFiles(root string) ([]trackedFile, error) {
	lines, err := gitLines(root, "ls-files", "-z", "-s")
	if err != nil {
		return nil, err
	}
	var files []trackedFile
	for _, l := range lines {
		meta, p, ok := strings.Cut(l, "\t")
		if !ok {
			return nil, fmt.Errorf("unexpected ls-files line %q", l)
		}
		mode, _, _ := strings.Cut(meta, " ")
		files = append(files, trackedFile{mode: mode, path: p})
	}
	return files, nil
}

// gitLines runs git in root and splits its NUL-terminated output.
func gitLines(root string, args ...string) ([]string, error) {
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
