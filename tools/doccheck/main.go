// doccheck is the offline documentation gate (G49.8). Run it from the
// repository root. It checks README.md and docs/**/*.md for:
//
//   - relative links whose target file or directory does not exist, or whose
//     #fragment names no heading or explicit anchor in the target Markdown file;
//   - external links that are malformed (it never touches the network);
//   - HTTP error codes the documentation names together with a status code
//     (or as a JSON "code" value) that are missing from the public error code
//     table in internal/adapter/http/openapi_contract.go, or documented with a
//     status the table does not allow.
//
// Findings recorded in the baseline file are tolerated so that the gate
// rejects new breakage while the existing backlog is cleaned up; baseline
// entries that no longer occur are reported so the file only shrinks.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	defaultBaseline   = "tools/doccheck/baseline.txt"
	defaultErrorTable = "internal/adapter/http/openapi_contract.go"
	errorTableVar     = "errorCodeStatuses"
)

// requirementQuotes hold verbatim requirement text and traceability rows that
// describe planned behaviour, not the implementation, so their error code
// mentions are not implementation claims.
var requirementQuotes = map[string]bool{
	"docs/requirements-source.md":       true,
	"docs/requirements-traceability.md": true,
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doccheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	baselinePath := flags.String("baseline", defaultBaseline, "tolerated findings, relative to -root; empty disables")
	update := flags.Bool("update-baseline", false, "rewrite the baseline with the current findings")
	undocumented := flags.Bool("undocumented", false, "also list error codes no document mentions (informational)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	codes, err := loadErrorCodes(filepath.Join(*root, filepath.FromSlash(defaultErrorTable)))
	if err != nil {
		fmt.Fprintln(stderr, "doccheck:", err)
		return 2
	}
	report, err := check(*root, codes)
	if err != nil {
		fmt.Fprintln(stderr, "doccheck:", err)
		return 2
	}
	if *undocumented {
		for _, code := range report.undocumented {
			fmt.Fprintf(stdout, "undocumented error code: %s\n", code)
		}
	}
	if *baselinePath == "" {
		return emit(report.findings, nil, stdout, stderr)
	}
	baseFile := filepath.Join(*root, filepath.FromSlash(*baselinePath))
	if *update {
		if err := writeBaseline(baseFile, report.findings); err != nil {
			fmt.Fprintln(stderr, "doccheck:", err)
			return 2
		}
		fmt.Fprintf(stdout, "wrote %d baseline entries to %s\n", len(report.findings), *baselinePath)
		return 0
	}
	baseline, err := readBaseline(baseFile)
	if err != nil {
		fmt.Fprintln(stderr, "doccheck:", err)
		return 2
	}
	return emit(report.findings, baseline, stdout, stderr)
}

// emit prints findings not covered by the baseline and stale baseline
// entries; either makes the gate fail.
func emit(findings []finding, baseline map[string]int, stdout, stderr io.Writer) int {
	remaining := map[string]int{}
	for key, count := range baseline {
		remaining[key] = count
	}
	fresh, tolerated := 0, 0
	for _, f := range findings {
		if remaining[f.key()] > 0 {
			remaining[f.key()]--
			tolerated++
			continue
		}
		fmt.Fprintln(stderr, f)
		fresh++
	}
	stale := make([]string, 0)
	for key, count := range remaining {
		for ; count > 0; count-- {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		fmt.Fprintf(stderr, "stale baseline entry (fixed? remove it): %s\n", key)
	}
	if fresh > 0 || len(stale) > 0 {
		fmt.Fprintf(stderr, "doccheck: %d new finding(s), %d stale baseline entr(ies), %d tolerated by baseline\n", fresh, len(stale), tolerated)
		return 1
	}
	fmt.Fprintf(stdout, "doccheck: ok (%d tolerated by baseline)\n", tolerated)
	return 0
}

type finding struct {
	file   string // slash-separated, relative to the repository root
	line   int
	kind   string
	target string
	detail string
}

// key identifies a finding for the baseline independently of line numbers,
// so unrelated edits above a known problem do not invalidate the baseline.
func (f finding) key() string { return f.file + "\t" + f.kind + "\t" + f.target }

func (f finding) String() string {
	return fmt.Sprintf("%s:%d: %s %q: %s", f.file, f.line, f.kind, f.target, f.detail)
}

type report struct {
	findings     []finding
	undocumented []string
}

// loadErrorCodes parses the error code table literal without importing the
// HTTP adapter, so the gate stays fast and dependency free.
func loadErrorCodes(file string) (map[string][]int, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse error code table: %w", err)
	}
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			for i, name := range value.Names {
				if name.Name != errorTableVar || i >= len(value.Values) {
					continue
				}
				return errorTable(value.Values[i])
			}
		}
	}
	return nil, fmt.Errorf("%s not found in %s", errorTableVar, file)
}

func errorTable(expr ast.Expr) (map[string][]int, error) {
	literal, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, errors.New("error code table is not a composite literal")
	}
	codes := map[string][]int{}
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return nil, errors.New("error code table entry is not key: value")
		}
		key, ok := pair.Key.(*ast.BasicLit)
		if !ok || key.Kind != token.STRING {
			return nil, errors.New("error code table key is not a string literal")
		}
		code, err := strconv.Unquote(key.Value)
		if err != nil {
			return nil, err
		}
		statuses, ok := pair.Value.(*ast.CompositeLit)
		if !ok {
			return nil, fmt.Errorf("statuses of %s are not a literal", code)
		}
		for _, raw := range statuses.Elts {
			lit, ok := raw.(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				return nil, fmt.Errorf("status of %s is not an integer literal", code)
			}
			status, err := strconv.Atoi(lit.Value)
			if err != nil {
				return nil, err
			}
			codes[code] = append(codes[code], status)
		}
	}
	if len(codes) == 0 {
		return nil, errors.New("error code table is empty")
	}
	return codes, nil
}

// documents lists README.md and docs/**/*.md as slash-separated paths.
func documents(root string) ([]string, error) {
	var files []string
	if _, err := os.Stat(filepath.Join(root, "README.md")); err == nil {
		files = append(files, "README.md")
	}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(file), ".md") {
			rel, err := filepath.Rel(root, file)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func check(root string, codes map[string][]int) (report, error) {
	files, err := documents(root)
	if err != nil {
		return report{}, err
	}
	parsed := map[string]*document{}
	load := func(rel string) (*document, error) {
		if doc, ok := parsed[rel]; ok {
			return doc, nil
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		doc := parseDocument(string(content))
		parsed[rel] = doc
		return doc, nil
	}
	var out report
	mentioned := map[string]bool{}
	for _, rel := range files {
		doc, err := load(rel)
		if err != nil {
			return report{}, err
		}
		for _, link := range doc.links {
			if f, bad := checkLink(root, rel, link, load); bad {
				out.findings = append(out.findings, f)
			}
		}
		for code := range codes {
			if doc.codeSpans[code] {
				mentioned[code] = true
			}
		}
		if !requirementQuotes[rel] {
			out.findings = append(out.findings, checkErrorCodes(rel, doc, codes)...)
		}
	}
	for code := range codes {
		if !mentioned[code] {
			out.undocumented = append(out.undocumented, code)
		}
	}
	sort.Strings(out.undocumented)
	return out, nil
}

func checkLink(root, from string, l link, load func(string) (*document, error)) (finding, bool) {
	bad := func(kind, detail string) (finding, bool) {
		return finding{file: from, line: l.line, kind: kind, target: l.target, detail: detail}, true
	}
	target := l.target
	if target == "" {
		return bad("broken-link", "empty link target")
	}
	if scheme, ok := linkScheme(target); ok {
		switch scheme {
		case "http", "https":
			parsed, err := url.Parse(target)
			if err != nil || parsed.Host == "" || strings.ContainsAny(target, " \t") {
				return bad("bad-external-link", "malformed URL")
			}
		case "mailto":
			if !strings.Contains(target, "@") {
				return bad("bad-external-link", "mailto without address")
			}
		default:
			return bad("bad-external-link", "unsupported scheme "+scheme)
		}
		return finding{}, false
	}
	if strings.HasPrefix(target, "//") {
		return bad("bad-external-link", "protocol-relative URL")
	}
	rawPath, fragment, _ := strings.Cut(target, "#")
	rawPath, _, _ = strings.Cut(rawPath, "?")
	decoded, err := url.PathUnescape(rawPath)
	if err != nil {
		return bad("broken-link", "invalid percent encoding")
	}
	resolved := from
	if decoded != "" {
		if strings.HasPrefix(decoded, "/") {
			resolved = path.Clean(strings.TrimPrefix(decoded, "/"))
		} else {
			resolved = path.Join(path.Dir(from), decoded)
		}
		if resolved == ".." || strings.HasPrefix(resolved, "../") {
			return bad("broken-link", "target is outside the repository")
		}
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(resolved)))
		if err != nil {
			return bad("broken-link", "target does not exist")
		}
		if info.IsDir() || !strings.EqualFold(path.Ext(resolved), ".md") {
			// Anchors into directories and non-Markdown files (for example
			// source line anchors) are not validated.
			return finding{}, false
		}
	}
	if fragment == "" {
		return finding{}, false
	}
	anchor, err := url.PathUnescape(fragment)
	if err != nil {
		return bad("broken-anchor", "invalid percent encoding")
	}
	doc, err := load(resolved)
	if err != nil {
		return bad("broken-link", "target cannot be read")
	}
	if !doc.anchors[anchor] && !doc.anchors[strings.ToLower(anchor)] {
		return bad("broken-anchor", "no heading or anchor "+strconv.Quote(anchor)+" in "+resolved)
	}
	return finding{}, false
}

// linkScheme reports a URL scheme per RFC 3986 §3.1. A single letter followed
// by ':' is treated as a scheme too, so Windows paths are rejected.
func linkScheme(target string) (string, bool) {
	for i, r := range target {
		switch {
		case r == ':':
			if i == 0 {
				return "", false
			}
			return strings.ToLower(target[:i]), true
		case r < 0x80 && (unicode.IsLetter(r) || (i > 0 && (unicode.IsDigit(r) || r == '+' || r == '-' || r == '.'))):
		default:
			return "", false
		}
	}
	return "", false
}

type link struct {
	line   int
	target string
}

type codeMention struct {
	line   int
	code   string
	status int // 0 when the mention has no adjacent status
}

type document struct {
	links     []link
	anchors   map[string]bool
	codeSpans map[string]bool
	mentions  []codeMention
}

var (
	fenceOpen     = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	atxHeading    = regexp.MustCompile(`^ {0,3}#{1,6}(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$`)
	setextLine    = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)
	referenceDef  = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:[ \t]*(\S+)`)
	autoLink      = regexp.MustCompile(`<([A-Za-z][A-Za-z0-9+.-]*:[^<>\s]*)>`)
	htmlAnchor    = regexp.MustCompile(`<[A-Za-z][^>]*\s(?:id|name)\s*=\s*["']([^"']+)["']`)
	codeSpan      = regexp.MustCompile("`([^`\n]+)`")
	snakeCode     = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)+$`)
	jsonErrorCode = regexp.MustCompile(`"code"\s*:\s*"([a-z][a-z0-9_]*)"`)
	statusAfter   = regexp.MustCompile(`^[^` + "`" + `\n]{0,12}?(?:HTTP\s*|/|（|\()([1-5][0-9]{2})\b`)
	statusBefore  = regexp.MustCompile(`(?:HTTP\s*)([1-5][0-9]{2})[^` + "`" + `\n]{0,12}$`)
)

// parseDocument extracts links, heading anchors and error code mentions.
// Links inside fenced code blocks and code spans are ignored; JSON error
// examples inside fenced blocks are still audited unless the fence's info
// string contains "not-http".
func parseDocument(content string) *document {
	doc := &document{anchors: map[string]bool{}, codeSpans: map[string]bool{}}
	slugs := map[string]int{}
	addHeading := func(text string) {
		slug := slugify(text)
		if n := slugs[slug]; n > 0 {
			doc.anchors[slug+"-"+strconv.Itoa(n)] = true
		} else {
			doc.anchors[slug] = true
		}
		slugs[slug]++
	}
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var fence, previous string
	// A fence whose info string contains "not-http" holds non-HTTP JSON (for
	// example doctor output); its "code" fields are not HTTP error claims.
	auditFence := false
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if fence != "" {
			if trimmed := strings.TrimLeft(line, " "); strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]+" \t") == "" {
				fence = ""
			} else if auditFence {
				doc.addJSONCodes(lineNo, line)
			}
			previous = ""
			continue
		}
		if m := fenceOpen.FindStringSubmatch(line); m != nil {
			fence = m[1]
			auditFence = !strings.Contains(strings.TrimSpace(line[len(m[0]):]), "not-http")
			previous = ""
			continue
		}
		if m := atxHeading.FindStringSubmatch(line); m != nil {
			addHeading(m[1])
		} else if setextLine.MatchString(line) && isSetextText(previous) {
			addHeading(previous)
		}
		for _, m := range htmlAnchor.FindAllStringSubmatch(line, -1) {
			doc.anchors[m[1]] = true
		}
		doc.addCodeMentions(lineNo, line)
		doc.addJSONCodes(lineNo, line)
		plain := codeSpan.ReplaceAllStringFunc(line, func(span string) string { return strings.Repeat(" ", len(span)) })
		if m := referenceDef.FindStringSubmatch(plain); m != nil {
			doc.links = append(doc.links, link{line: lineNo, target: strings.Trim(m[1], "<>")})
		}
		for _, target := range inlineLinks(plain) {
			doc.links = append(doc.links, link{line: lineNo, target: target})
		}
		for _, m := range autoLink.FindAllStringSubmatch(plain, -1) {
			doc.links = append(doc.links, link{line: lineNo, target: m[1]})
		}
		previous = line
	}
	return doc
}

func isSetextText(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, ">") {
		return false
	}
	if trimmed[0] == '-' || trimmed[0] == '*' || trimmed[0] == '+' || atxHeading.MatchString(line) {
		return false
	}
	return true
}

func (d *document) addJSONCodes(lineNo int, line string) {
	for _, m := range jsonErrorCode.FindAllStringSubmatch(line, -1) {
		d.mentions = append(d.mentions, codeMention{line: lineNo, code: m[1]})
	}
}

// addCodeMentions records code spans and treats a snake_case code span as an
// HTTP error code claim when an HTTP status is written right next to it, as
// in "`job_busy`/409", "`conflict`（409）" or "HTTP 501，`feature_removed`".
func (d *document) addCodeMentions(lineNo int, line string) {
	for _, loc := range codeSpan.FindAllStringSubmatchIndex(line, -1) {
		code := line[loc[2]:loc[3]]
		d.codeSpans[code] = true
		if !snakeCode.MatchString(code) {
			continue
		}
		status := 0
		if m := statusAfter.FindStringSubmatch(line[loc[1]:]); m != nil {
			status, _ = strconv.Atoi(m[1])
		} else if m := statusBefore.FindStringSubmatch(line[:loc[0]]); m != nil {
			status, _ = strconv.Atoi(m[1])
		}
		if status >= 400 {
			d.mentions = append(d.mentions, codeMention{line: lineNo, code: code, status: status})
		}
	}
}

func checkErrorCodes(rel string, doc *document, codes map[string][]int) []finding {
	var out []finding
	for _, m := range doc.mentions {
		statuses, ok := codes[m.code]
		if !ok {
			out = append(out, finding{file: rel, line: m.line, kind: "unknown-error-code", target: m.code, detail: "not in the error code table (" + defaultErrorTable + ")"})
			continue
		}
		if m.status == 0 {
			continue
		}
		allowed := false
		for _, s := range statuses {
			allowed = allowed || s == m.status
		}
		if !allowed {
			out = append(out, finding{file: rel, line: m.line, kind: "error-code-status", target: m.code + "/" + strconv.Itoa(m.status), detail: fmt.Sprintf("error code table allows %v", statuses)})
		}
	}
	return out
}

// inlineLinks returns destinations of [text](destination "title") links and
// images on one line, honouring <...> destinations and balanced parentheses.
func inlineLinks(line string) []string {
	var out []string
	for i := 0; i+1 < len(line); i++ {
		if line[i] != ']' || line[i+1] != '(' {
			continue
		}
		rest := line[i+2:]
		rest = strings.TrimLeft(rest, " \t")
		if strings.HasPrefix(rest, "<") {
			if end := strings.IndexByte(rest, '>'); end > 0 {
				out = append(out, rest[1:end])
			}
			continue
		}
		depth, end := 0, -1
		for j, r := range rest {
			if r == '(' {
				depth++
			} else if r == ')' {
				if depth == 0 {
					end = j
					break
				}
				depth--
			} else if r == ' ' || r == '\t' {
				end = j
				break
			}
		}
		if end < 0 {
			continue
		}
		out = append(out, rest[:end])
	}
	return out
}

// slugify follows GitHub's heading anchor rule: inline Markdown is reduced
// to its text, the result is lowercased, punctuation is dropped and each
// space becomes a hyphen.
func slugify(heading string) string {
	text := inlineLinkText.ReplaceAllString(heading, "$1")
	text = strings.NewReplacer("`", "", "*", "").Replace(text)
	text = htmlTag.ReplaceAllString(text, "")
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

var (
	inlineLinkText = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	htmlTag        = regexp.MustCompile(`<[^>]+>`)
)

func readBaseline(file string) (map[string]int, error) {
	content, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]int{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, line := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line]++
	}
	return out, nil
}

func writeBaseline(file string, findings []finding) error {
	keys := make([]string, 0, len(findings))
	for _, f := range findings {
		keys = append(keys, f.key())
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# doccheck baseline: existing findings tolerated until cleaned up.\n")
	b.WriteString("# Format: file<TAB>kind<TAB>target. Only remove lines; regenerate with\n")
	b.WriteString("# `go run ./tools/doccheck -update-baseline` after fixing documents.\n")
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(b.String()), 0o644)
}
