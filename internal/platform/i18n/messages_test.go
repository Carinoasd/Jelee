package i18n

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestLocaleNegotiation(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{"", "zh-CN"}, {"  ", "zh-CN"}, {"fr-FR", "en-US"}, {"zh-CN", "zh-CN"}, {"zh-TW", "zh-TW"}, {"ja-JP", "ja-JP"}, {"en-US", "en-US"},
		{"zh-Hant", "zh-TW"}, {"zh-HK", "zh-TW"}, {"zh-Hans-SG", "zh-CN"}, {"zh", "zh-CN"}, {"ja", "ja-JP"}, {"en-GB", "en-US"}, {"ZH-tW", "zh-TW"},
		{"en-US;q=0.4,ja-JP;q=0.9", "ja-JP"}, {"fr-FR;q=1,zh-TW;q=0.8", "zh-TW"}, {"ja;q=0.8,en;q=0.8", "ja-JP"}, {"en;q=0.8,ja;q=0.8", "en-US"},
		{"ja;q=0,en;q=0.5", "en-US"}, {"*", "zh-CN"}, {"zh-CN;q=0,*;q=0.5", "zh-TW"}, {"*;q=0,ja-JP;q=0.5", "ja-JP"}, {"en-US;q=0,en;q=1,ja;q=0.4", "ja-JP"},
		{"en;q=NaN,ja;q=0.5", "ja-JP"}, {"en;q=1.1,ja;q=0.5", "ja-JP"}, {"en;q=-1,ja;q=0.5", "ja-JP"}, {"en;q=0.0001,ja;q=0.5", "ja-JP"}, {"en;q=.9,ja;q=0.5", "ja-JP"},
		{"invalid_@;q=1", "en-US"}, {"ja;q=0", "en-US"}, {"en;q=0.3;q=1,ja;q=0.5", "ja-JP"}, {"ja ; Q = 0.500, en;q=0.4", "ja-JP"}, {strings.Repeat("ja,", 4097), "en-US"},
	} {
		name := tc.header
		if len(name) > 80 {
			name = "oversized_header"
		}
		t.Run(name, func(t *testing.T) {
			if got := Locale(tc.header); got != tc.want {
				t.Fatalf("Locale(%q)=%s want=%s", tc.header, got, tc.want)
			}
		})
	}
}

func TestTranslationAndSafeFallback(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{"", "请先登录。"}, {"zh-TW", "請先登入。"}, {"ja-JP", "ログインしてください。"}, {"fr-FR", "Authentication is required."},
	} {
		if got := Message("authentication_required", tc.header, "safe fallback"); got != tc.want {
			t.Fatalf("translation=%q", got)
		}
	}
	if got := Message("unknown", "ja-JP", "Safe public message."); got != "Safe public message." {
		t.Fatalf("fallback=%q", got)
	}
	if got := Message("internal_error", "en-US", "/private/db-secret"); strings.Contains(got, "secret") {
		t.Fatal("known code exposed fallback")
	}
}

func TestCatalogKeyAndPlaceholderParity(t *testing.T) {
	if len(messages) != 4 {
		t.Fatalf("unsupported locale count=%d", len(messages))
	}
	pattern := regexp.MustCompile(`\{[A-Za-z][A-Za-z0-9_]*\}|%[sdv]`)
	for locale, catalog := range messages {
		if len(catalog) != len(messages["en-US"]) {
			t.Fatalf("locale %s key count differs", locale)
		}
		for code, english := range messages["en-US"] {
			translated, ok := catalog[code]
			if !ok || strings.TrimSpace(translated) == "" {
				t.Fatalf("locale %s missing %s", locale, code)
			}
			want, got := pattern.FindAllString(english, -1), pattern.FindAllString(translated, -1)
			slices.Sort(want)
			slices.Sort(got)
			if !slices.Equal(want, got) {
				t.Fatalf("placeholder mismatch: %s/%s", locale, code)
			}
		}
	}
}

func FuzzLocale(f *testing.F) {
	for _, seed := range []string{"", "zh-TW, en;q=0.5", "*;q=0", "en;q=NaN"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, header string) {
		if _, ok := messages[Locale(header)]; !ok {
			t.Fatal("negotiation produced unsupported locale")
		}
	})
}
