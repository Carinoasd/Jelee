package domain

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// Administrator CSS sanitizer (G33.4). This is a port of the web client's
// web/src/theme/customCss.ts and must stay equivalent to it: both read the
// shared cases in web/src/theme/customCss.cases.json in their tests. The
// server sanitizes again because it must not trust the browser that saved
// the text: a request that bypasses the web client still cannot make other
// users' browsers apply anything the checks did not see.
//
// Refused outright: markup ("<"), backslash escapes, control characters,
// unterminated comments and unbalanced blocks, more than 64 Ki UTF-16 code
// units. Dropped with an issue: @import and unknown at-rules, expression(),
// script URLs, behavior/-moz-binding, image-set()/src()/element()/paint(),
// attr() used as a URL, every url() that is not a same-origin path, and
// external fonts unless enabled for an allowlisted https host. The output is
// re-serialized from what passed, so it never contains unchecked text.
//
// String offsets follow JavaScript semantics where they matter: lengths are
// counted in UTF-16 code units, whitespace is the ECMAScript set and lower
// casing matches String.prototype.toLowerCase for the characters that could
// otherwise turn into ASCII.

// CSS issue codes, identical to the web client's CssIssueCode.
const (
	CSSIssueTooLong             = "too_long"
	CSSIssueMarkup              = "markup"
	CSSIssueEscape              = "escape"
	CSSIssueControlChar         = "control_char"
	CSSIssueUnterminatedComment = "unterminated_comment"
	CSSIssueUnbalanced          = "unbalanced"
	CSSIssueTooDeep             = "too_deep"
	CSSIssueImportBlocked       = "import_blocked"
	CSSIssueAtRuleBlocked       = "at_rule_blocked"
	CSSIssueInvalidSelector     = "invalid_selector"
	CSSIssueInvalidDeclaration  = "invalid_declaration"
	CSSIssueNestingUnsupported  = "nesting_unsupported"
	CSSIssueExpression          = "expression"
	CSSIssueScriptURL           = "script_url"
	CSSIssueBinding             = "binding"
	CSSIssueBlockedFunction     = "blocked_function"
	CSSIssueExternalURL         = "external_url"
	CSSIssueExternalFont        = "external_font"
)

const (
	// CustomCSSMaxLength bounds administrator CSS in UTF-16 code units, the
	// unit the web client measures.
	CustomCSSMaxLength = 64 * 1024
	// FontHostLimit bounds the external font host allowlist.
	FontHostLimit = 10
	cssMaxDepth   = 3
)

// ErrCustomCSSRejected refuses administrator CSS with a structural problem
// (markup, escapes, control characters, unbalanced blocks, too long).
var ErrCustomCSSRejected = errors.New("custom CSS rejected")

// CSSIssue is one removed or refused part of administrator CSS.
type CSSIssue struct {
	Code string `json:"code"`
	// Excerpt is a short piece of the offending text for the administrator.
	Excerpt string `json:"excerpt"`
}

// CSSPolicy decides which external font sources are allowed.
type CSSPolicy struct {
	AllowExternalFonts bool
	FontHosts          []string
}

// SanitizedCSS is the sanitizer's verdict.
type SanitizedCSS struct {
	// CSS is safe to apply; empty when the input was refused.
	CSS      string
	Issues   []CSSIssue
	Rejected bool
}

// jsSpace is the ECMAScript WhiteSpace and LineTerminator set (\s).
const jsSpace = `\t\n\x0B\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', 0x0B, '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// jsLower matches String.prototype.toLowerCase where Go differs in a way
// that matters here: U+0130 becomes "i̇", never a bare ASCII "i".
func jsLower(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == 0x130 {
			b.WriteString("i̇")
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// utf16Length counts UTF-16 code units like JavaScript's String length.
func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

var (
	cssSpaceRun        = regexp.MustCompile(`[` + jsSpace + `]+`)
	cssHostPattern     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
	cssNumericLabel    = regexp.MustCompile(`\.[0-9]+$`)
	cssSelector        = regexp.MustCompile(`^[A-Za-z0-9_` + jsSpace + `.#:,>+~*\[\]="'^$|()&%-]+$`)
	cssProperty        = regexp.MustCompile(`^(?:--[A-Za-z0-9_-]{1,64}|-?[a-z][a-z0-9-]{0,63})$`)
	cssKeyframe        = regexp.MustCompile(`(?i)^(?:from|to|\d{1,3}(?:\.\d+)?%)(?:[` + jsSpace + `]*,[` + jsSpace + `]*(?:from|to|\d{1,3}(?:\.\d+)?%))*$`)
	cssIdent           = regexp.MustCompile(`^-?[A-Za-z_][A-Za-z0-9_-]*$`)
	cssCondition       = regexp.MustCompile(`^[A-Za-z0-9_` + jsSpace + `.,:()<>=/-]*$`)
	cssLayerList       = regexp.MustCompile(`^[A-Za-z0-9_` + jsSpace + `,.-]+$`)
	cssSameOrigin      = regexp.MustCompile(`^/[A-Za-z0-9._~%/-]*(?:\?[A-Za-z0-9._~%=&-]*)?$`)
	cssFragment        = regexp.MustCompile(`^#[A-Za-z][A-Za-z0-9_-]*$`)
	cssBlockedFunction = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-])(?:-webkit-image-set|image-set|src|element|-moz-element|paint)[` + jsSpace + `]*\(`)
	cssExpression      = regexp.MustCompile(`expression[` + jsSpace + `]*\(`)
	cssScriptURL       = regexp.MustCompile(`(?:javascript|vbscript|livescript)[` + jsSpace + `]*:`)
	cssAttrURL         = regexp.MustCompile(`attr[` + jsSpace + `]*\([^)]*\burl\b`)
	cssURLCall         = regexp.MustCompile(`(?i)url[` + jsSpace + `]*\(`)
	cssURL             = regexp.MustCompile(`(?i)url[` + jsSpace + `]*\([` + jsSpace + `]*(?:"([^"]*)"|'([^']*)'|([^"'()` + jsSpace + `]*))[` + jsSpace + `]*\)`)
	cssAtName          = regexp.MustCompile(`^@([A-Za-z-]+)`)
	cssControl         = regexp.MustCompile(`[\x00-\x08\x0B\x0E-\x1F\x7F\x{2028}\x{2029}]`)
	cssTokenControl    = regexp.MustCompile(`[\x00-\x1F\x7F\x{2028}\x{2029}]`)
	cssComment         = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssTokenForbidden  = regexp.MustCompile(`[<>{};\\@]`)
)

// NormalizeFontHost returns an allowlist entry in canonical form, or false
// when it is not a plain DNS host name. IP addresses (a numeric last label)
// and single labels are refused, so the entry can be placed in a CSP
// font-src source list verbatim.
func NormalizeFontHost(value string) (string, bool) {
	host := jsLower(jsTrim(value))
	if len(host) > 253 || !cssHostPattern.MatchString(host) || cssNumericLabel.MatchString(host) {
		return "", false
	}
	return host, true
}

func cssExcerpt(text string) string {
	flat := jsTrim(cssSpaceRun.ReplaceAllString(text, " "))
	if utf16Length(flat) <= 80 {
		return flat
	}
	units := 0
	for i, r := range flat {
		width := 1
		if r >= 0x10000 {
			width = 2
		}
		if units+width > 77 {
			return flat[:i] + "..."
		}
		units += width
	}
	return flat
}

// cssPrefix returns at most n bytes of s, cut at a rune boundary.
func cssPrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

type cssEntry struct {
	prelude string
	// body is the text between the braces, nil for a statement ending in ";".
	body *string
}

var errCSSUnbalanced = errors.New("unbalanced CSS")

// cssSplit splits text into top-level statements and blocks, respecting
// strings and parentheses.
func cssSplit(text string) ([]cssEntry, error) {
	var entries []cssEntry
	var buffer strings.Builder
	parens := 0
	for i := 0; i < len(text); {
		c := text[i]
		if c == '"' || c == '\'' {
			end := strings.IndexByte(text[i+1:], c)
			if end < 0 {
				return nil, errCSSUnbalanced
			}
			end += i + 1
			if strings.ContainsAny(text[i+1:end], "\n\r\f") {
				return nil, errCSSUnbalanced
			}
			buffer.WriteString(text[i : end+1])
			i = end + 1
			continue
		}
		switch {
		case c == '(':
			parens++
		case c == ')':
			parens--
			if parens < 0 {
				return nil, errCSSUnbalanced
			}
		case parens == 0 && c == ';':
			entries = append(entries, cssEntry{prelude: buffer.String()})
			buffer.Reset()
			i++
			continue
		case parens == 0 && c == '{':
			end, err := cssMatchingBrace(text, i)
			if err != nil {
				return nil, err
			}
			body := text[i+1 : end]
			entries = append(entries, cssEntry{prelude: buffer.String(), body: &body})
			buffer.Reset()
			i = end + 1
			continue
		case c == '}' || c == '{':
			return nil, errCSSUnbalanced
		}
		buffer.WriteByte(c)
		i++
	}
	if parens != 0 {
		return nil, errCSSUnbalanced
	}
	if jsTrim(buffer.String()) != "" {
		entries = append(entries, cssEntry{prelude: buffer.String()})
	}
	return entries, nil
}

func cssMatchingBrace(text string, open int) (int, error) {
	depth := 0
	for i := open; i < len(text); i++ {
		switch c := text[i]; c {
		case '"', '\'':
			end := strings.IndexByte(text[i+1:], c)
			if end < 0 {
				return 0, errCSSUnbalanced
			}
			i += end + 1
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, errCSSUnbalanced
}

type cssContext struct {
	policy CSSPolicy
	issues []CSSIssue
}

func (c *cssContext) report(code, text string) {
	c.issues = append(c.issues, CSSIssue{Code: code, Excerpt: cssExcerpt(text)})
}

// cssValueIssue returns the issue code that blocks a declaration value, or "".
func cssValueIssue(value string, fontFace bool, policy CSSPolicy) string {
	lower := jsLower(value)
	if cssExpression.MatchString(lower) {
		return CSSIssueExpression
	}
	if cssScriptURL.MatchString(lower) {
		return CSSIssueScriptURL
	}
	if cssBlockedFunction.MatchString(lower) || cssAttrURL.MatchString(lower) {
		return CSSIssueBlockedFunction
	}
	urls := cssURL.FindAllStringSubmatch(value, -1)
	if len(urls) != len(cssURLCall.FindAllStringIndex(lower, -1)) {
		return CSSIssueExternalURL
	}
	for _, match := range urls {
		target := jsTrim(match[1] + match[2] + match[3])
		if cssSameOrigin.MatchString(target) && !strings.HasPrefix(target, "//") || cssFragment.MatchString(target) {
			continue
		}
		if fontFace {
			if policy.AllowExternalFonts && cssAllowedFontURL(target, policy.FontHosts) {
				continue
			}
			return CSSIssueExternalFont
		}
		return CSSIssueExternalURL
	}
	return ""
}

// cssAllowedFontURL accepts https://<allowlisted host>/... without
// credentials or an explicit port. It is stricter than the browser's URL
// parser: anything it would have to normalize is refused.
func cssAllowedFontURL(target string, hosts []string) bool {
	if len(target) < len("https://") || !strings.EqualFold(target[:len("https://")], "https://") {
		return false
	}
	rest := target[len("https://"):]
	authority := rest
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		authority = rest[:end]
	}
	if authority == "" || strings.ContainsAny(authority, "@:") {
		return false
	}
	host := jsLower(authority)
	for _, allowed := range hosts {
		if host == allowed {
			return true
		}
	}
	return false
}

func cssDeclarations(body string, c *cssContext, fontFace bool) ([]string, error) {
	entries, err := cssSplit(body)
	if err != nil {
		return nil, err
	}
	var kept []string
	for _, entry := range entries {
		if entry.body != nil {
			c.report(CSSIssueNestingUnsupported, entry.prelude)
			continue
		}
		text := jsTrim(entry.prelude)
		if text == "" {
			continue
		}
		property, value := "", ""
		if colon := strings.IndexByte(text, ':'); colon >= 0 {
			property = jsTrim(jsLower(text[:colon]))
			value = cssSpaceRun.ReplaceAllString(jsTrim(text[colon+1:]), " ")
		}
		if !cssProperty.MatchString(property) || value == "" || utf16Length(value) > 2048 {
			c.report(CSSIssueInvalidDeclaration, text)
			continue
		}
		if property == "behavior" || strings.HasSuffix(property, "-binding") {
			c.report(CSSIssueBinding, text)
			continue
		}
		if issue := cssValueIssue(value, fontFace, c.policy); issue != "" {
			c.report(issue, text)
			continue
		}
		kept = append(kept, property+":"+value)
	}
	return kept, nil
}

func cssRule(prelude, body string, c *cssContext, fontFace bool) (string, error) {
	kept, err := cssDeclarations(body, c, fontFace)
	if err != nil || len(kept) == 0 {
		return "", err
	}
	return prelude + "{" + strings.Join(kept, ";") + "}", nil
}

func cssStylesheet(text string, c *cssContext, depth int) ([]string, error) {
	if depth > cssMaxDepth {
		c.report(CSSIssueTooDeep, text)
		return nil, nil
	}
	entries, err := cssSplit(text)
	if err != nil {
		return nil, err
	}
	var output []string
	for _, entry := range entries {
		prelude := cssSpaceRun.ReplaceAllString(jsTrim(entry.prelude), " ")
		if prelude == "" && entry.body == nil {
			continue
		}
		if strings.HasPrefix(prelude, "@") {
			name := ""
			if m := cssAtName.FindStringSubmatch(prelude); m != nil {
				name = strings.ToLower(m[1])
			}
			condition := jsTrim(prelude[len(name)+1:])
			switch {
			case name == "import":
				c.report(CSSIssueImportBlocked, prelude)
			case (name == "media" || name == "supports" || name == "container" || name == "layer") && cssCondition.MatchString(condition) && !cssURLCall.MatchString(condition):
				if entry.body == nil {
					if name == "layer" && cssLayerList.MatchString(condition) {
						output = append(output, "@layer "+condition+";")
					} else {
						c.report(CSSIssueAtRuleBlocked, prelude)
					}
					continue
				}
				inner, err := cssStylesheet(*entry.body, c, depth+1)
				if err != nil {
					return nil, err
				}
				if len(inner) > 0 {
					head := "@" + name
					if condition != "" {
						head += " " + condition
					}
					output = append(output, head+"{"+strings.Join(inner, "")+"}")
				}
			case name == "font-face" && condition == "" && entry.body != nil:
				face, err := cssRule("@font-face", *entry.body, c, true)
				if err != nil {
					return nil, err
				}
				if face != "" {
					output = append(output, face)
				}
			case (name == "keyframes" || name == "-webkit-keyframes") && cssIdent.MatchString(condition) && entry.body != nil:
				frames, err := cssSplit(*entry.body)
				if err != nil {
					return nil, err
				}
				var kept []string
				for _, frame := range frames {
					selector := jsTrim(frame.prelude)
					if frame.body == nil || !cssKeyframe.MatchString(selector) {
						c.report(CSSIssueInvalidSelector, selector)
						continue
					}
					rule, err := cssRule(selector, *frame.body, c, false)
					if err != nil {
						return nil, err
					}
					if rule != "" {
						kept = append(kept, rule)
					}
				}
				output = append(output, "@"+name+" "+condition+"{"+strings.Join(kept, "")+"}")
			default:
				c.report(CSSIssueAtRuleBlocked, prelude)
			}
			continue
		}
		if entry.body == nil || prelude == "" || utf16Length(prelude) > 1024 || !cssSelector.MatchString(prelude) {
			excerpt := prelude
			if prelude == "" && entry.body != nil {
				excerpt = *entry.body
			}
			c.report(CSSIssueInvalidSelector, excerpt)
			continue
		}
		rule, err := cssRule(prelude, *entry.body, c, false)
		if err != nil {
			return nil, err
		}
		if rule != "" {
			output = append(output, rule)
		}
	}
	return output, nil
}

// SanitizeCustomCSS sanitizes administrator CSS. The policy's hosts must
// already be normalized (NormalizeFontHost).
func SanitizeCustomCSS(input string, policy CSSPolicy) SanitizedCSS {
	refuse := func(code, text string) SanitizedCSS {
		return SanitizedCSS{Issues: []CSSIssue{{Code: code, Excerpt: cssExcerpt(text)}}, Rejected: true}
	}
	if utf16Length(input) > CustomCSSMaxLength {
		return refuse(CSSIssueTooLong, cssPrefix(input, 80))
	}
	if i := strings.IndexByte(input, '<'); i >= 0 {
		return refuse(CSSIssueMarkup, cssPrefix(input[i:], 80))
	}
	if i := strings.IndexByte(input, '\\'); i >= 0 {
		return refuse(CSSIssueEscape, cssPrefix(input[i:], 80))
	}
	// Tabs, line breaks and form feeds are whitespace; every other control
	// character (including NUL) and the Unicode line separators are refused.
	if loc := cssControl.FindStringIndex(input); loc != nil {
		start := max(0, loc[0]-20)
		for start > 0 && !isRuneStart(input[start]) {
			start--
		}
		return refuse(CSSIssueControlChar, cssPrefix(input[start:], 40))
	}
	// Comments go first: "expr/**/ession(" must not survive as two halves.
	withoutComments := cssComment.ReplaceAllString(input, " ")
	if i := strings.Index(withoutComments, "/*"); i >= 0 {
		return refuse(CSSIssueUnterminatedComment, cssPrefix(withoutComments[i:], 80))
	}
	c := &cssContext{policy: policy}
	rules, err := cssStylesheet(withoutComments, c, 0)
	if err != nil {
		return refuse(CSSIssueUnbalanced, cssPrefix(input, 80))
	}
	return SanitizedCSS{CSS: strings.Join(rules, "\n"), Issues: c.issues}
}

// SanitizeTokenValue checks one design token value. It returns the trimmed
// value, or false: no url(), no blocks or statements, no quotes except in
// font lists. Port of sanitizeTokenValue in web/src/theme/customCss.ts.
func SanitizeTokenValue(value string, allowQuotes bool) (string, bool) {
	text := jsTrim(value)
	if text == "" || utf16Length(text) > 256 || cssTokenForbidden.MatchString(text) || !allowQuotes && strings.ContainsAny(text, `"'`) {
		return "", false
	}
	if cssURLCall.MatchString(text) || cssTokenControl.MatchString(text) {
		return "", false
	}
	if cssValueIssue(text, false, CSSPolicy{}) != "" {
		return "", false
	}
	return text, true
}
