package subtitleocr

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

type fakeRunner struct {
	err       error
	output    string
	input     []byte
	languages []string
	path      string
	writable  bool
}

func (f *fakeRunner) Run(_ context.Context, request process.ToolRequest) (process.Result, error) {
	f.path = request.Stdin.Name()
	f.languages = request.Extraction.Languages
	f.input, _ = io.ReadAll(request.Stdin)
	_, err := request.Stdin.Write([]byte("x"))
	f.writable = err == nil
	if f.err != nil {
		return process.Result{}, f.err
	}
	return process.Result{Stdout: []byte(f.output)}, nil
}

func TestRecognizerPassesAPrivateReadOnlyPGM(t *testing.T) {
	directory := privateDir(t)
	runner := &fakeRunner{output: "Hello\n"}
	recognizer, err := NewRecognizer(runner, directory)
	if err != nil {
		t.Fatal(err)
	}
	picture := image.NewGray(image.Rect(0, 0, 3, 2))
	copy(picture.Pix, []byte{0, 50, 100, 150, 200, 250})
	text, err := recognizer.Recognize(context.Background(), picture, []string{"eng", "jpn"})
	if err != nil || text != "Hello\n" {
		t.Fatalf("%q %v", text, err)
	}
	if !bytes.Equal(runner.input, []byte("P5\n3 2\n255\n\x00\x32\x64\x96\xc8\xfa")) || runner.writable || !slices.Equal(runner.languages, []string{"eng", "jpn"}) {
		t.Fatalf("input %q writable %v languages %q", runner.input, runner.writable, runner.languages)
	}
	if filepath.Dir(runner.path) != directory || !strings.HasSuffix(runner.path, ".pgm") {
		t.Fatalf("picture path %s", runner.path)
	}
	if entries, _ := os.ReadDir(directory); len(entries) != 0 {
		t.Fatal("picture file left behind")
	}
	for err, want := range map[error]error{process.ErrBusy: ErrRecognizerBusy, process.ErrExit: ErrPictureRejected, process.ErrOutputLimit: ErrPictureRejected,
		process.ErrTimeout: ErrPictureRejected, process.ErrStart: ErrUnavailable, process.ErrSandboxUnavailable: ErrUnavailable} {
		runner.err = err
		if _, got := recognizer.Recognize(context.Background(), picture, []string{"eng"}); !errors.Is(got, want) {
			t.Fatalf("%v mapped to %v", err, got)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	runner.err = process.ErrCancelled
	if _, err := recognizer.Recognize(cancelled, picture, []string{"eng"}); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := recognizer.Recognize(context.Background(), nil, []string{"eng"}); err != ErrPictureRejected {
		t.Fatal("nil picture")
	}
	if _, err := recognizer.Recognize(context.Background(), picture, nil); err != ErrPictureRejected {
		t.Fatal("no languages")
	}
	if _, err := recognizer.Recognize(context.Background(), image.NewGray(image.Rect(0, 0, 0, 0)), []string{"eng"}); err != ErrUnavailable {
		t.Fatal("empty picture")
	}
	if entries, _ := os.ReadDir(directory); len(entries) != 0 {
		t.Fatal("picture file left behind after failures")
	}
	public := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(public, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{public, filepath.Join(directory, "missing")} {
		if _, err := NewRecognizer(runner, bad); err != ErrUnavailable {
			t.Fatalf("%s accepted", bad)
		}
	}
	if _, err := NewRecognizer(nil, directory); err != ErrUnavailable {
		t.Fatal("nil runner")
	}
	gone := privateDir(t)
	broken, _ := NewRecognizer(runner, gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := broken.Recognize(context.Background(), picture, []string{"eng"}); err != ErrUnavailable {
		t.Fatal("missing picture directory")
	}
}

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
