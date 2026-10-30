package i18n

import (
	"strings"
	"testing"
)

func TestTrackWordTablesAreComplete(t *testing.T) {
	if len(localeColumn) != len(messages) {
		t.Fatalf("track tables cover %d locales, messages %d", len(localeColumn), len(messages))
	}
	for locale := range messages {
		if _, ok := localeColumn[locale]; !ok {
			t.Fatalf("locale %s has no track column", locale)
		}
	}
	for _, table := range []map[string][4]string{trackLabels, languageNames, chineseScripts} {
		for key, words := range table {
			for i, word := range words {
				if strings.TrimSpace(word) == "" || strings.Contains(word, " - ") {
					t.Fatalf("%s column %d: %q", key, i, word)
				}
			}
		}
	}
}

func TestTrackLabelsAndLanguageNames(t *testing.T) {
	if got := TrackLabel("zh-TW", TrackLabelExternal); got != "外掛" {
		t.Fatalf("zh-TW external = %q", got)
	}
	if got := TrackLabel("fr-FR", TrackLabelDefault); got != "Default" {
		t.Fatalf("unknown locale = %q", got)
	}
	if got := TrackLabel("en-US", "nope"); got != "nope" {
		t.Fatalf("unknown key = %q", got)
	}
	for _, c := range []struct{ locale, tag, want string }{
		{"en-US", "en", "English"}, {"ja-JP", "ja", "日本語"}, {"zh-CN", "de-AT", "德语"},
		{"zh-TW", "zh", "中文"}, {"zh-TW", "zh-Hant", "繁體中文"}, {"zh-TW", "zh-HK", "繁體中文"}, {"en-US", "zh-Hans-SG", "Chinese (Simplified)"},
		{"zh-CN", "zh-CN", "简体中文"}, {"ja-JP", "zh-TW", "中国語（繁体字）"}, {"en-US", "zh-US", "Chinese"},
	} {
		if got, ok := LanguageName(c.locale, c.tag); !ok || got != c.want {
			t.Fatalf("LanguageName(%s, %s) = %q %v, want %q", c.locale, c.tag, got, ok, c.want)
		}
	}
	if _, ok := LanguageName("en-US", "tlh"); ok {
		t.Fatal("a language outside the table was named")
	}
}
