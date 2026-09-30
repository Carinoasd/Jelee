package httpapi

import (
	"os"
	"regexp"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/i18n"
)

// The adapter owns this cross-layer contract, so the translation package can
// build independently of the HTTP implementation.
func TestPublicHTTPErrorCodesHaveTranslations(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`status, code, message\s*(?::=|=)\s*\d+,\s*"([a-z_]+)"`),
		regexp.MustCompile(`writeProblem\(w, r,\s*\d+,\s*"([a-z_]+)"`),
	}
	seen := map[string]bool{}
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
			code := match[1]
			seen[code] = true
			for _, locale := range []string{"zh-CN", "zh-TW", "ja-JP", "en-US"} {
				if i18n.Message(code, locale, "MISSING") == "MISSING" {
					t.Errorf("untranslated public HTTP code %s/%s", locale, code)
				}
			}
		}
	}
	if len(seen) < 15 {
		t.Fatalf("error-code scan found only %d codes; review extraction after mapper change", len(seen))
	}
}
