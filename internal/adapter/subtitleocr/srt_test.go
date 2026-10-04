package subtitleocr

import (
	"strings"
	"testing"
	"time"
)

func TestCleanTextAndSRT(t *testing.T) {
	for raw, want := range map[string]string{
		"  Hello\tworld \n\n":              "Hello world",
		"line one\r\nline\u200b two\x00\n": "line one\nline two",
		"a\nb\nc\nd\ne\n":                  "a\nb\nc\nd",
		"\xff\xfeok":                       "ok",
		"\u3000全形\u3000空白\u3000":           "全形 空白",
		"\n \n":                            "",
	} {
		if got := cleanText(raw); got != want {
			t.Errorf("%q -> %q, want %q", raw, got, want)
		}
	}
	if got := cleanText(strings.Repeat("字", 300)); len([]rune(got)) != maxCueLineRunes {
		t.Fatalf("long line kept %d runes", len([]rune(got)))
	}
	for d, want := range map[time.Duration]string{0: "00:00:00,000", -time.Second: "00:00:00,000", 3723456 * time.Millisecond: "01:02:03,456", 100 * time.Hour: "100:00:00,000"} {
		if got := srtTime(d); got != want {
			t.Errorf("%v -> %s", d, got)
		}
	}
	data, truncated := FormatSRT([]Cue{{Start: time.Second, End: 2 * time.Second, Text: "One"}, {Start: 3 * time.Second, End: 3 * time.Second, Text: "empty span"}, {Start: 4 * time.Second, End: 5 * time.Second}, {Start: 6 * time.Second, End: 7 * time.Second, Text: "Two\nlines"}})
	if truncated || string(data) != "1\n00:00:01,000 --> 00:00:02,000\nOne\n\n2\n00:00:06,000 --> 00:00:07,000\nTwo\nlines\n\n" {
		t.Fatalf("%q %v", data, truncated)
	}
	var many []Cue
	for i := range 40000 {
		many = append(many, Cue{Start: time.Duration(i) * time.Second, End: time.Duration(i)*time.Second + 500*time.Millisecond, Text: strings.Repeat("x", 200)})
	}
	data, truncated = FormatSRT(many)
	if !truncated || len(data) > MaxTrackBytes || len(data) < MaxTrackBytes-300 {
		t.Fatalf("truncation %v %d", truncated, len(data))
	}
}
