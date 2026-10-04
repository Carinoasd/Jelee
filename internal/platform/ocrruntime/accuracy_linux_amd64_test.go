package ocrruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode"

	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/goregular"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub"
	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
	"github.com/MoYuanCN/Jelee/internal/adapter/subtitleocr"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// Accuracy and cost of subtitle OCR on synthetic pictures with known text
// (G15.6). Pictures are drawn like broadcast subtitles (fill with outline on
// a transparent canvas), encoded as real PGS or VobSub streams, decoded by
// bitmapsub and recognized by the pinned Tesseract in its sandbox. Latin
// cases use the Go fonts shipped with golang.org/x/image; CJK cases and more
// Latin faces use font files from JELEE_OCR_FONT_DIR (for example a
// Windows Fonts directory), which are read locally and never committed.

var englishLines = []string{
	"Where were you last night?",
	"I told you, I was at the office.",
	"The train leaves at 7:45 tomorrow.",
	"Don't touch anything until I get back.",
	"We have 3 hours before sunrise.",
	"She said the password was \"blue river\".",
	"Is that really what you want?",
	"Nobody leaves this room.",
	"Call me when you reach London.",
	"It's not about the money, Tom.",
	"Turn left after the old bridge.",
	"How many people know about this?",
}

var traditionalLines = []string{"我們今天晚上去看電影吧", "你昨天到底去了哪裡？", "這件事情沒有那麼簡單", "請把門關上，外面很冷", "他說明天早上七點出發", "我不知道該怎麼辦才好",
	"警察已經在路上了", "這是我們最後的機會", "你為什麼不早點告訴我？", "火車三點十五分到站", "別擔心，一切都會好的", "把錢放在桌子上就走吧"}
var simplifiedLines = []string{"我们今天晚上去看电影吧", "你昨天到底去了哪里？", "这件事情没有那么简单", "请把门关上，外面很冷", "他说明天早上七点出发", "我不知道该怎么办才好",
	"警察已经在路上了", "这是我们最后的机会", "你为什么不早点告诉我？", "火车三点十五分到站", "别担心，一切都会好的", "把钱放在桌子上就走吧"}
var japaneseLines = []string{"今夜は映画を見に行こう", "昨日はどこに行ったの？", "そんなに簡単じゃない", "ドアを閉めてください", "明日の朝七時に出発する", "どうすればいいのか分からない",
	"警察はもうすぐ来るよ", "これが最後のチャンスだ", "なぜ早く言わなかったの？", "電車は三時十五分に着く", "心配しないで、大丈夫だから", "お金を机に置いて行って"}

// twoLineEnglish puts two subtitle lines in one picture, as most subtitles are.
var twoLineEnglish = []string{
	"Where were you last night?\nI told you, I was at the office.",
	"The train leaves at 7:45 tomorrow.\nDon't touch anything until I get back.",
	"We have 3 hours before sunrise.\nShe said the password was \"blue river\".",
	"Is that really what you want?\nNobody leaves this room.",
	"Call me when you reach London.\nIt's not about the money, Tom.",
	"Turn left after the old bridge.\nHow many people know about this?",
}

type accuracyCase struct {
	name      string
	font      []byte
	fontIndex int
	size      float64
	format    string // "pgs" or "vobsub"
	languages []string
	lines     []string
	cjk       bool
	style     bitmapsubtest.Style
	box       bool // opaque dark box behind the text
}

type accuracyResult struct {
	Case            string  `json:"case"`
	Format          string  `json:"format"`
	Languages       string  `json:"languages"`
	Pictures        int     `json:"pictures"`
	Characters      int     `json:"characters"`
	Errors          int     `json:"errors"`
	CharAccuracy    float64 `json:"charAccuracy"`
	ExactLines      int     `json:"exactLines"`
	MeanMillis      float64 `json:"meanMillisPerPicture"`
	P95Millis       float64 `json:"p95MillisPerPicture"`
	DecodeMillis    float64 `json:"decodeMillisPerPicture"`
	PictureKiB      float64 `json:"meanPictureKiB"`
	ChildMaxRSSMiB  float64 `json:"childMaxRssMiB"`
	WorstLine       string  `json:"worstLine,omitempty"`
	WorstRecognized string  `json:"worstRecognized,omitempty"`
}

var (
	whiteOnBlack = bitmapsubtest.Style{Fill: color.NRGBA{255, 255, 255, 255}, Outline: color.NRGBA{16, 16, 16, 255}, OutlineWidth: 3, Padding: 2}
	yellow       = bitmapsubtest.Style{Fill: color.NRGBA{250, 230, 60, 255}, Outline: color.NRGBA{0, 0, 0, 255}, OutlineWidth: 2, Padding: 2}
	blackOnWhite = bitmapsubtest.Style{Fill: color.NRGBA{10, 10, 10, 255}, Outline: color.NRGBA{245, 245, 245, 255}, OutlineWidth: 3, Padding: 2}
	thinOutline  = bitmapsubtest.Style{Fill: color.NRGBA{235, 235, 235, 255}, Outline: color.NRGBA{20, 20, 20, 255}, OutlineWidth: 1, Padding: 2}
)

func fontFile(t *testing.T, name string) []byte {
	t.Helper()
	directory := os.Getenv("JELEE_OCR_FONT_DIR")
	if directory == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		return nil
	}
	return data
}

func accuracyCases(t *testing.T) []accuracyCase {
	t.Helper()
	cases := []accuracyCase{
		{name: "go-regular-48px-white", font: goregular.TTF, size: 48, format: "pgs", languages: []string{"eng"}, lines: englishLines, style: whiteOnBlack},
		{name: "go-bold-48px-yellow", font: gobold.TTF, size: 48, format: "pgs", languages: []string{"eng"}, lines: englishLines, style: yellow},
		{name: "go-italic-48px-white", font: goitalic.TTF, size: 48, format: "pgs", languages: []string{"eng"}, lines: englishLines, style: whiteOnBlack},
		{name: "go-regular-48px-black-on-white", font: goregular.TTF, size: 48, format: "pgs", languages: []string{"eng"}, lines: englishLines, style: blackOnWhite},
		{name: "go-regular-48px-box", font: goregular.TTF, size: 48, format: "pgs", languages: []string{"eng"}, lines: englishLines, style: whiteOnBlack, box: true},
		{name: "go-regular-48px-two-lines", font: goregular.TTF, size: 48, format: "pgs", languages: []string{"eng"}, lines: twoLineEnglish, style: whiteOnBlack},
		{name: "go-regular-64px-white", font: goregular.TTF, size: 64, format: "pgs", languages: []string{"eng"}, lines: englishLines, style: whiteOnBlack},
		{name: "go-regular-24px-thin-dvd", font: goregular.TTF, size: 24, format: "vobsub", languages: []string{"eng"}, lines: englishLines, style: thinOutline},
		{name: "go-bold-30px-dvd", font: gobold.TTF, size: 30, format: "vobsub", languages: []string{"eng"}, lines: englishLines, style: whiteOnBlack},
		{name: "go-regular-16px-dvd-tiny", font: goregular.TTF, size: 16, format: "vobsub", languages: []string{"eng"}, lines: englishLines, style: thinOutline},
	}
	if arial := fontFile(t, "arial.ttf"); arial != nil {
		cases = append(cases, accuracyCase{name: "arial-48px-white", font: arial, size: 48, format: "pgs", languages: []string{"eng"}, lines: englishLines, style: whiteOnBlack})
	}
	if times := fontFile(t, "timesi.ttf"); times != nil {
		cases = append(cases, accuracyCase{name: "times-italic-44px-white", font: times, size: 44, format: "pgs", languages: []string{"eng"}, lines: englishLines, style: whiteOnBlack})
	}
	if msjh := fontFile(t, "msjh.ttc"); msjh != nil {
		cases = append(cases,
			accuracyCase{name: "msjh-52px-chi_tra", font: msjh, size: 52, format: "pgs", languages: []string{"chi_tra", "eng"}, lines: traditionalLines, cjk: true, style: whiteOnBlack},
			accuracyCase{name: "msjh-68px-chi_tra", font: msjh, size: 68, format: "pgs", languages: []string{"chi_tra", "eng"}, lines: traditionalLines, cjk: true, style: whiteOnBlack},
			accuracyCase{name: "msjh-32px-chi_tra-dvd", font: msjh, size: 32, format: "vobsub", languages: []string{"chi_tra", "eng"}, lines: traditionalLines, cjk: true, style: whiteOnBlack})
	}
	if mingliu := fontFile(t, "mingliu.ttc"); mingliu != nil {
		cases = append(cases, accuracyCase{name: "mingliu-52px-chi_tra", font: mingliu, size: 52, format: "pgs", languages: []string{"chi_tra", "eng"}, lines: traditionalLines, cjk: true, style: whiteOnBlack})
	}
	if msyh := fontFile(t, "msyh.ttc"); msyh != nil {
		cases = append(cases, accuracyCase{name: "msyh-52px-chi_sim", font: msyh, size: 52, format: "pgs", languages: []string{"chi_sim", "eng"}, lines: simplifiedLines, cjk: true, style: whiteOnBlack})
	}
	if yugoth := fontFile(t, "YuGothM.ttc"); yugoth != nil {
		cases = append(cases, accuracyCase{name: "yugothic-52px-jpn", font: yugoth, size: 52, format: "pgs", languages: []string{"jpn", "eng"}, lines: japaneseLines, cjk: true, style: whiteOnBlack})
	}
	return cases
}

// withBox puts the text on an opaque dark box, as some DVD subtitles do.
func withBox(img *image.NRGBA) *image.NRGBA {
	boxed := image.NewNRGBA(img.Bounds())
	for i := 0; i < len(boxed.Pix); i += 4 {
		boxed.Pix[i], boxed.Pix[i+1], boxed.Pix[i+2], boxed.Pix[i+3] = 30, 30, 30, 255
	}
	for i := 0; i < len(img.Pix); i += 4 {
		a := int(img.Pix[i+3])
		for c := range 3 {
			boxed.Pix[i+c] = uint8((int(img.Pix[i+c])*a + int(boxed.Pix[i+c])*(255-a)) / 255) //nolint:gosec // G115: a weighted mean of bytes
		}
	}
	return boxed
}

// encodeCase renders every line as one cue and encodes the track.
func encodeCase(t *testing.T, c accuracyCase) (index, data []byte) {
	t.Helper()
	width, height := 1920, 1080
	if c.format == "vobsub" {
		width, height = 720, 480
	}
	var cues []bitmapsubtest.Cue
	for i, line := range c.lines {
		style := c.style
		style.FontIndex = c.fontIndex
		img, err := bitmapsubtest.RenderText(c.font, line, c.size, style)
		if err != nil {
			t.Fatal(err)
		}
		if c.box {
			img = withBox(img)
		}
		x := max((width-img.Bounds().Dx())/2, 0)
		cues = append(cues, bitmapsubtest.Cue{Start: time.Duration(i*4+1) * time.Second, End: time.Duration(i*4+3) * time.Second, Image: img, X: x, Y: height - img.Bounds().Dy() - 40})
	}
	var idx, sub bytes.Buffer
	var err error
	if c.format == "vobsub" {
		err = bitmapsubtest.EncodeVobSub(&idx, &sub, width, height, cues)
	} else {
		err = bitmapsubtest.EncodePGS(&sub, width, height, cues)
	}
	if err != nil {
		t.Fatal(err)
	}
	return idx.Bytes(), sub.Bytes()
}

// normalize compares text as a viewer reads it: whitespace runs collapse to
// one space (and vanish between CJK characters), typographic quotes count
// as plain ones and full-width ASCII forms (for example the full-width
// question mark) as their ASCII characters.
func normalize(text string, cjk bool) []rune {
	text = strings.NewReplacer("\u201c", "\"", "\u201d", "\"", "\u2018", "'", "\u2019", "'", "\n", " ").Replace(text)
	text = strings.Map(func(r rune) rune {
		if r >= 0xFF01 && r <= 0xFF5E {
			return r - 0xFF01 + 0x21
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if cjk {
		text = strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, text)
	}
	return []rune(text)
}

func editDistance(a, b []rune) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}

func childMaxRSS() float64 {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_CHILDREN, &usage) != nil {
		return 0
	}
	return float64(usage.Maxrss) / 1024 // Linux reports KiB.
}

func TestRealOCRAccuracyAndCost(t *testing.T) {
	requireHostRuntime(t, "JELEE_OCR_HOST_RUNTIME")
	root := hostToolsRoot(t)
	runners := hostRunners(t, 1, hostOCRRegistration(t, root))
	pictures := filepath.Join(t.TempDir(), "pictures")
	if err := os.Mkdir(pictures, 0o700); err != nil {
		t.Fatal(err)
	}
	recognizer, err := subtitleocr.NewRecognizer(runners[sandbox.ToolOCR], pictures)
	if err != nil {
		t.Fatal(err)
	}
	var results []accuracyResult
	for _, c := range accuracyCases(t) {
		t.Run(c.name, func(t *testing.T) {
			index, data := encodeCase(t, c)
			var events []bitmapsub.Event
			started := time.Now()
			visit := func(event bitmapsub.Event) error { events = append(events, event); return nil }
			if c.format == "vobsub" {
				_, err = bitmapsub.DecodeVobSub(bytes.NewReader(index), bytes.NewReader(data), int64(len(data)), visit)
			} else {
				_, err = bitmapsub.DecodePGS(bytes.NewReader(data), visit)
			}
			if err != nil || len(events) != len(c.lines) {
				t.Fatalf("decode: %v, %d events", err, len(events))
			}
			decode := time.Since(started)
			result := accuracyResult{Case: c.name, Format: c.format, Languages: strings.Join(c.languages, "+"), Pictures: len(events), DecodeMillis: float64(decode.Microseconds()) / 1000 / float64(len(events))}
			var durations []float64
			worst := -1.0
			var pictureBytes int
			for i, event := range events {
				picture := bitmapsub.OCRImage(event.Bitmap)
				if picture == nil {
					t.Fatal("empty picture")
				}
				pictureBytes += len(picture.Pix)
				begin := time.Now()
				text, err := recognizer.Recognize(context.Background(), picture, c.languages)
				if err != nil {
					t.Fatalf("recognize: %v", err)
				}
				durations = append(durations, float64(time.Since(begin).Microseconds())/1000)
				want, got := normalize(c.lines[i], c.cjk), normalize(text, c.cjk)
				distance := editDistance(want, got)
				result.Characters += len(want)
				result.Errors += distance
				if distance == 0 {
					result.ExactLines++
				}
				if rate := float64(distance) / float64(len(want)); rate > worst {
					worst, result.WorstLine, result.WorstRecognized = rate, c.lines[i], strings.TrimSpace(text)
				}
			}
			if result.ExactLines == len(events) {
				result.WorstLine, result.WorstRecognized = "", ""
			}
			result.CharAccuracy = math.Round((1-float64(result.Errors)/float64(result.Characters))*10000) / 100
			sort.Float64s(durations)
			var total float64
			for _, d := range durations {
				total += d
			}
			result.MeanMillis = math.Round(total/float64(len(durations))*10) / 10
			result.P95Millis = durations[int(math.Ceil(0.95*float64(len(durations))))-1]
			result.PictureKiB = math.Round(float64(pictureBytes)/float64(len(events))/1024*10) / 10
			result.ChildMaxRSSMiB = math.Round(childMaxRSS()*10) / 10
			t.Logf("%-34s %-6s %-12s accuracy %6.2f%% (%d/%d chars wrong, %d/%d lines exact) mean %.1f ms p95 %.1f ms decode %.2f ms picture %.1f KiB child RSS %.1f MiB",
				c.name, c.format, result.Languages, result.CharAccuracy, result.Errors, result.Characters, result.ExactLines, result.Pictures, result.MeanMillis, result.P95Millis, result.DecodeMillis, result.PictureKiB, result.ChildMaxRSSMiB)
			if result.WorstLine != "" {
				t.Logf("  worst: %q -> %q", result.WorstLine, result.WorstRecognized)
			}
			results = append(results, result)
			// Floors that any regression of decoding, polarity or picture
			// preparation breaks; the measured figures are in the docs.
			if floor, ok := accuracyFloors[c.name]; ok && result.CharAccuracy < floor {
				t.Errorf("accuracy %.2f%% below the %.2f%% floor", result.CharAccuracy, floor)
			}
		})
	}
	if report := os.Getenv("JELEE_OCR_ACCURACY_REPORT"); report != "" {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.ContainsFunc(results, func(r accuracyResult) bool { return r.Case == "go-regular-48px-white" }) {
		t.Fatal("baseline case did not run")
	}
}

// accuracyFloors are lower bounds, a few points under the figures measured
// on 2026-10-05 (docs/subtitle-ocr.md), for the cases that need no local
// font file.
var accuracyFloors = map[string]float64{
	"go-regular-48px-white": 97, "go-bold-48px-yellow": 97, "go-italic-48px-white": 97, "go-regular-48px-black-on-white": 97,
	"go-regular-48px-box": 97, "go-regular-24px-thin-dvd": 97, "go-bold-30px-dvd": 97, "go-regular-16px-dvd-tiny": 95,
	"go-regular-48px-two-lines": 97, "go-regular-64px-white": 97,
}
