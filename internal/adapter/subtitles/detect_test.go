package subtitles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"
)

type detectCase struct {
	name   string
	want   Charset
	data   []byte
	method Method
	// thin marks samples with fewer than three non-ASCII characters, where
	// a statistical guess is allowed to say it is unsure.
	thin bool
}

// accuracyCorpus builds every (charset, language, format, rotation) sample.
func accuracyCorpus(t testing.TB) []detectCase {
	t.Helper()
	type plan struct {
		cs    Charset
		langs []string
		bom   bool
	}
	plans := []plan{
		{UTF8, []string{"zh-Hans", "zh-Hant", "ja", "ko", "western"}, false},
		{UTF8, []string{"zh-Hans", "ja", "western"}, true},
		{UTF16LE, []string{"zh-Hans", "ja", "ko", "western"}, true},
		{UTF16LE, []string{"zh-Hans", "ja", "ko", "western"}, false},
		{UTF16BE, []string{"zh-Hant", "ja", "ko", "western"}, true},
		{UTF16BE, []string{"zh-Hant", "ja", "ko", "western"}, false},
		{GB18030, []string{"zh-Hans"}, false},
		{Big5, []string{"zh-Hant"}, false},
		{ShiftJIS, []string{"ja"}, false},
		{EUCJP, []string{"ja"}, false},
		{EUCKR, []string{"ko"}, false},
		{Windows1252, []string{"western"}, false},
	}
	var cases []detectCase
	for _, p := range plans {
		for _, lang := range p.langs {
			for rot := range corpus[lang] {
				for _, format := range []string{"srt", "ass"} {
					for _, n := range []int{2, 6} {
						lines := pick(lang, rot, n)
						text := srtText(lines)
						if format == "ass" {
							text = assText(lines)
						}
						data := encodeWith(t, p.cs, text)
						method := Method("")
						if p.bom {
							data, method = withBOM(p.cs, data), MethodBOM
						}
						cases = append(cases, detectCase{
							name: fmt.Sprintf("%s/%s/bom=%v/%s/rot%d/n%d", p.cs, lang, p.bom, format, rot, n),
							want: p.cs, data: data, method: method,
							thin: nonASCIIRunes(text) < 3,
						})
					}
				}
			}
		}
	}
	// GB18030 beyond GBK: four-byte sequences for emoji and extension B.
	for rot := range corpus["zh-Hans"] {
		lines := pick("zh-Hans", rot, 4)
		lines[1] += " 😀"
		lines[2] = "𠮷野家见吧！" + lines[2]
		cases = append(cases, detectCase{
			name: fmt.Sprintf("GB18030/four-byte/rot%d", rot), want: GB18030,
			data: encodeWith(t, GB18030, srtText(lines)),
		})
	}
	return cases
}

func TestDetectAccuracyTable(t *testing.T) {
	type tally struct {
		total, hits, low int
		scoreSum         float64
	}
	table := map[string]*tally{}
	var order []string
	for _, c := range accuracyCorpus(t) {
		key := string(c.want)
		if c.method == MethodBOM {
			key += "+BOM"
		}
		if table[key] == nil {
			table[key] = &tally{}
			order = append(order, key)
		}
		got, conf, err := DetectCharset(context.Background(), bytes.NewReader(c.data), 0)
		tl := table[key]
		tl.total++
		if err == nil && got == c.want {
			tl.hits++
		} else {
			t.Errorf("%s: got %q (%+v, err %v)", c.name, got, conf, err)
		}
		if conf.Low {
			tl.low++
			if !c.thin || conf.Method != MethodStatistical {
				t.Errorf("%s: low confidence %+v", c.name, conf)
			}
		}
		if c.method != "" && conf.Method != c.method {
			t.Errorf("%s: method %s, want %s", c.name, conf.Method, c.method)
		}
		tl.scoreSum += conf.Score
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-18s %7s %5s %5s %9s\n", "charset", "samples", "hits", "low*", "avg score")
	total, hits := 0, 0
	for _, key := range order {
		tl := table[key]
		total += tl.total
		hits += tl.hits
		fmt.Fprintf(&b, "%-18s %7d %5d %5d %9.3f\n", key, tl.total, tl.hits, tl.low, tl.scoreSum/float64(tl.total))
	}
	fmt.Fprintf(&b, "%-18s %7d %5d\n", "total", total, hits)
	b.WriteString("low*: correct but marked low confidence; only allowed with fewer than three non-ASCII characters\n")
	t.Log(b.String())
}

func nonASCIIRunes(s string) int {
	n := 0
	for _, r := range s {
		if r >= utf8.RuneSelf {
			n++
		}
	}
	return n
}

func TestDetectStatisticalRunnerUp(t *testing.T) {
	data := encodeWith(t, Big5, srtText(pick("zh-Hant", 0, 6)))
	got, conf, err := DetectCharset(context.Background(), bytes.NewReader(data), 0)
	if err != nil || got != Big5 || conf.Method != MethodStatistical {
		t.Fatalf("got %s %+v %v", got, conf, err)
	}
	if conf.RunnerUp == "" || conf.RunnerUp == Big5 || conf.RunnerUpScore >= conf.Score {
		t.Fatalf("runner-up not reported: %+v", conf)
	}
}

func TestDetectTruncatedInput(t *testing.T) {
	for _, tc := range []struct {
		cs   Charset
		lang string
	}{{UTF8, "ja"}, {GB18030, "zh-Hans"}, {Big5, "zh-Hant"}, {ShiftJIS, "ja"}, {EUCJP, "ja"}, {EUCKR, "ko"}, {Windows1252, "western"}} {
		data := encodeWith(t, tc.cs, srtText(pick(tc.lang, 3, 6)))
		// Cut at every byte of the last 12 to hit both character boundaries
		// and the middle of multi-byte characters.
		for cut := len(data) - 12; cut < len(data); cut++ {
			got, conf, err := DetectCharset(context.Background(), bytes.NewReader(data[:cut]), 0)
			if err != nil || got != tc.cs || conf.Low {
				t.Errorf("%s cut %d/%d: got %s %+v %v", tc.cs, cut, len(data), got, conf, err)
			}
		}
		// The same cut made by the scan limit instead of end of file.
		limit := int64(len(data) * 2 / 3)
		got, conf, err := DetectCharset(context.Background(), bytes.NewReader(data), limit)
		if err != nil || got != tc.cs || !conf.Partial || conf.Scanned != limit {
			t.Errorf("%s limit %d: got %s %+v %v", tc.cs, limit, got, conf, err)
		}
	}
}

func TestDetectSmallReads(t *testing.T) {
	for _, cs := range []Charset{UTF8, GB18030, ShiftJIS} {
		lang := map[Charset]string{UTF8: "ko", GB18030: "zh-Hans", ShiftJIS: "ja"}[cs]
		data := encodeWith(t, cs, assText(pick(lang, 1, 6)))
		got, conf, err := DetectCharset(context.Background(), iotest.OneByteReader(bytes.NewReader(data)), 0)
		if err != nil || got != cs || conf.Scanned != int64(len(data)) || conf.Partial {
			t.Errorf("%s: got %s %+v %v", cs, got, conf, err)
		}
	}
}

func TestDetectLongASCIIHeader(t *testing.T) {
	// ASS files may embed fonts as long ASCII blocks before any dialogue.
	var b bytes.Buffer
	b.WriteString("[Script Info]\nScriptType: v4.00+\n\n[Fonts]\nfontname: sample_0.ttf\n")
	for b.Len() < 300<<10 {
		b.WriteString(strings.Repeat("M", 80) + "\n")
	}
	b.WriteString("\n")
	b.Write(encodeWith(t, GB18030, assText(pick("zh-Hans", 0, 6))))
	data := b.Bytes()

	got, conf, err := DetectCharset(context.Background(), bytes.NewReader(data), 0)
	if err != nil || got != GB18030 || conf.Low {
		t.Fatalf("default limit: got %s %+v %v", got, conf, err)
	}
	got, conf, err = DetectCharset(context.Background(), bytes.NewReader(data), 100<<10)
	if err != nil || got != UTF8 || conf.Method != MethodASCII || !conf.Low || !conf.Partial {
		t.Fatalf("ASCII prefix must be low confidence: got %s %+v %v", got, conf, err)
	}
	got, conf, err = DetectCharset(context.Background(), strings.NewReader("1\n00:00:01,000 --> 00:00:02,000\nHello\n"), 0)
	if err != nil || got != UTF8 || conf.Method != MethodASCII || conf.Low || conf.Partial || conf.Score != 1 {
		t.Fatalf("complete ASCII: got %s %+v %v", got, conf, err)
	}
}

func TestDetectMixedInput(t *testing.T) {
	// A UTF-8 file with a few lines pasted from a GBK source.
	var b bytes.Buffer
	b.WriteString(srtText(pick("zh-Hans", 0, 10)))
	gbk := encodeWith(t, GB18030, "12\r\n00:01:00,000 --> 00:01:02,000\r\n我们走吧，别回头。\r\n\r\n")
	if utf8.Valid(gbk) {
		t.Fatal("GBK line must not be valid UTF-8")
	}
	b.Write(gbk)
	got, conf, err := DetectCharset(context.Background(), bytes.NewReader(b.Bytes()), 0)
	if err != nil || got != UTF8 || conf.Method != MethodUTF8Mixed || !conf.Low || conf.InvalidUTF8 == 0 {
		t.Fatalf("mixed UTF-8: got %s %+v %v", got, conf, err)
	}
	var out bytes.Buffer
	res, err := ToUTF8(context.Background(), bytes.NewReader(b.Bytes()), &out, got, 0)
	if err != nil || res.Replaced == 0 || !strings.Contains(out.String(), corpus["zh-Hans"][0]) {
		t.Fatalf("mixed conversion: %+v %v", res, err)
	}

	// Two legacy charsets in one file: whatever wins, it must not be sure.
	b.Reset()
	b.Write(encodeWith(t, GB18030, srtText(pick("zh-Hans", 0, 3))))
	b.Write(encodeWith(t, EUCKR, srtText(pick("ko", 0, 3))))
	got, conf, err = DetectCharset(context.Background(), bytes.NewReader(b.Bytes()), 0)
	if err != nil || !conf.Low {
		t.Fatalf("GB18030+EUC-KR mix: got %s %+v %v", got, conf, err)
	}
}

func TestDetectRejectsBinaryAndUTF32(t *testing.T) {
	for name, data := range map[string][]byte{
		"vobsub": {0x00, 0x00, 0x01, 0xba, 0x44, 0x00, 0x04, 0x00, 0x04, 0x01},
		"pgs":    append([]byte("PG"), 0x00, 0x00, 0x01, 0x23, 0x00, 0x00, 0x00, 0x00, 0x16, 0x00, 0x13),
		"late":   append(bytes.Repeat([]byte("plain text line\n"), 8000), 0, 1, 2),
	} {
		if _, _, err := DetectCharset(context.Background(), bytes.NewReader(data), 0); !errors.Is(err, ErrNotText) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, bom := range [][]byte{{0xff, 0xfe, 0, 0}, {0, 0, 0xfe, 0xff}} {
		data := append(bom, 'a', 0, 0, 0)
		if _, _, err := DetectCharset(context.Background(), bytes.NewReader(data), 0); !errors.Is(err, ErrUnsupportedCharset) {
			t.Errorf("UTF-32 % x: %v", bom, err)
		}
	}
	got, conf, err := DetectCharset(context.Background(), bytes.NewReader(nil), 0)
	if err != nil || got != UTF8 || conf.Method != MethodASCII || conf.Scanned != 0 {
		t.Errorf("empty: %s %+v %v", got, conf, err)
	}
}

// endless repeats one line forever and counts bytes handed out.
type endless struct {
	line []byte
	off  int
	read int64
}

func (e *endless) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		c := copy(p[n:], e.line[e.off:])
		n += c
		e.off = (e.off + c) % len(e.line)
	}
	e.read += int64(n)
	return n, nil
}

func TestDetectHugeInputIsBounded(t *testing.T) {
	ascii := &endless{line: []byte("Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,text\n")}
	got, conf, err := DetectCharset(context.Background(), ascii, 512<<10)
	if err != nil || got != UTF8 || !conf.Partial || !conf.Low || conf.Scanned != 512<<10 || ascii.read > 512<<10 {
		t.Fatalf("ascii: %s %+v %v read=%d", got, conf, err, ascii.read)
	}
	// Non-ASCII input stops once the evidence buffer is full, long before
	// the limit.
	gbk := &endless{line: encodeWith(t, GB18030, srtText(pick("zh-Hans", 0, 12)))}
	got, conf, err = DetectCharset(context.Background(), gbk, MaxDetectLimit)
	if err != nil || got != GB18030 || conf.Low || !conf.Partial || gbk.read > 4*evidenceBytes {
		t.Fatalf("gbk: %s %+v %v read=%d", got, conf, err, gbk.read)
	}
	// Requests above the cap are clamped.
	for _, tc := range []struct{ in, def, max, want int64 }{
		{0, DefaultDetectLimit, MaxDetectLimit, DefaultDetectLimit},
		{-5, DefaultConvertLimit, MaxConvertLimit, DefaultConvertLimit},
		{1 << 40, DefaultDetectLimit, MaxDetectLimit, MaxDetectLimit},
		{1 << 40, DefaultConvertLimit, MaxConvertLimit, MaxConvertLimit},
		{123, DefaultDetectLimit, MaxDetectLimit, 123},
	} {
		if got := effectiveLimit(tc.in, tc.def, tc.max); got != tc.want {
			t.Errorf("effectiveLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// cancelAfter cancels its context after n reads.
type cancelAfter struct {
	r      io.Reader
	n      int
	cancel context.CancelFunc
}

func (c *cancelAfter) Read(p []byte) (int, error) {
	if c.n--; c.n < 0 {
		c.cancel()
	}
	return c.r.Read(p)
}

func TestDetectCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := DetectCharset(ctx, strings.NewReader("abc"), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	src := &cancelAfter{r: &endless{line: []byte("ascii only\n")}, n: 3, cancel: cancel}
	if _, conf, err := DetectCharset(ctx, src, MaxDetectLimit); !errors.Is(err, context.Canceled) || conf.Scanned > 5*readChunk {
		t.Fatalf("mid-stream: %v scanned=%d", err, conf.Scanned)
	}
}

func TestDetectReadError(t *testing.T) {
	boom := errors.New("boom")
	r := io.MultiReader(strings.NewReader(strings.Repeat("a", 10<<10)), iotest.ErrReader(boom))
	if _, _, err := DetectCharset(context.Background(), r, 0); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestParseCharset(t *testing.T) {
	for label, want := range map[string]Charset{
		"utf8": UTF8, "US-ASCII": UTF8, "UTF-16LE": UTF16LE, "utf_16be": UTF16BE,
		"GBK": GB18030, "cp936": GB18030, "gb2312": GB18030, "BIG5": Big5, "cp950": Big5,
		"Shift_JIS": ShiftJIS, "sjis": ShiftJIS, "CP932": ShiftJIS, "euc-jp": EUCJP,
		"EUC-KR": EUCKR, "cp949": EUCKR, "latin1": Windows1252, "Windows-1252": Windows1252,
	} {
		if got, ok := ParseCharset(label); !ok || got != want {
			t.Errorf("%q: %s %v", label, got, ok)
		}
	}
	for _, label := range []string{"", "utf-32", "koi8-r", "auto"} {
		if _, ok := ParseCharset(label); ok {
			t.Errorf("%q accepted", label)
		}
	}
	for _, cs := range Charsets() {
		if got, ok := ParseCharset(string(cs)); !ok || got != cs {
			t.Errorf("own name %s: %s", cs, got)
		}
	}
}

// heldOut lines were written after the frequency tables were tuned, and each
// is scored alone, which is harder than the six-line samples above.
var heldOut = map[Charset][]string{
	GB18030: {
		"船长，前方发现一座无人岛。", "把门锁好，今晚谁也不许出去。", "这份报告明天早上必须交给经理。",
		"你要是敢动她一根头发，我饶不了你。", "外面下雨了，记得带伞。", "他们说的话你千万别信。",
		"火车晚点了两个小时。", "妈妈做的红烧肉最好吃了。",
	},
	Big5: {
		"船長，前方發現一座無人島。", "把門鎖好，今晚誰也不許出去。", "這份報告明天早上必須交給經理。",
		"你要是敢動她一根頭髮，我饒不了你。", "外面下雨了，記得帶傘。", "他們說的話你千萬別信。",
		"火車誤點了兩個小時。", "媽媽做的紅燒肉最好吃了。",
	},
	ShiftJIS: {
		"船長、前方に無人島が見えます。", "ドアに鍵をかけて、今夜は誰も外に出るな。", "この報告書は明日の朝までに部長へ提出してくれ。",
		"彼女に指一本でも触れたら許さないぞ。", "外は雨だよ、傘を忘れないで。", "あいつらの言うことは信じるな。",
		"電車が二時間も遅れている。", "お母さんの肉じゃがが一番おいしい。",
	},
	EUCKR: {
		"선장님, 앞에 무인도가 보입니다.", "문 잠가, 오늘 밤엔 아무도 나가지 마.", "이 보고서는 내일 아침까지 부장님께 내야 해.",
		"그녀에게 손끝 하나라도 대면 가만두지 않겠어.", "밖에 비가 와, 우산 챙겨.", "그 사람들 말은 절대 믿지 마.",
		"기차가 두 시간이나 늦었어.", "엄마가 해 준 김치찌개가 제일 맛있어.",
	},
	Windows1252: {
		"Capitaine, une île déserte droit devant !", "Schließ die Tür ab, heute Nacht geht keiner raus.", "¿Dónde está la estación de tren?",
		"Não acredite no que eles dizem, está bem?", "Il treno è in ritardo di due ore… è assurdo.", "Ça ne me regarde pas, c’est ton problème.",
		"Für mich ist das völlig in Ordnung.", "Él nunca había visto algo así – jamás.",
	},
}

func TestDetectHeldOutSingleLines(t *testing.T) {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-14s %5s %5s %5s\n", "charset", "lines", "hits", "low")
	for _, cs := range []Charset{GB18030, Big5, ShiftJIS, EUCJP, EUCKR, Windows1252} {
		lines := heldOut[cs]
		if cs == EUCJP {
			lines = heldOut[ShiftJIS]
		}
		hits, low := 0, 0
		for i, line := range lines {
			got, conf, err := DetectCharset(context.Background(), bytes.NewReader(encodeWith(t, cs, srtText([]string{line}))), 0)
			if err == nil && got == cs {
				hits++
			} else {
				t.Logf("%s line %d: got %s %+v %v", cs, i, got, conf, err)
			}
			if conf.Low {
				low++
			}
		}
		fmt.Fprintf(&b, "%-14s %5d %5d %5d\n", cs, len(lines), hits, low)
		if hits*10 < len(lines)*9 {
			t.Errorf("%s held-out accuracy %d/%d below 90%%", cs, hits, len(lines))
		}
	}
	t.Log(b.String())
}

// Double-byte text read through windows-1252 looks like long runs of
// accented letters; the run-length rule must keep that from scoring as
// Western text, independently of how the other candidates fare.
func TestWindows1252RejectsDoubleByteRuns(t *testing.T) {
	for _, tc := range []struct {
		cs   Charset
		lang string
	}{{GB18030, "zh-Hans"}, {Big5, "zh-Hant"}, {ShiftJIS, "ja"}, {EUCJP, "ja"}, {EUCKR, "ko"}} {
		data := encodeWith(t, tc.cs, srtText(pick(tc.lang, 0, 12)))
		if got := scoreWindows1252(data); got.score > 0.2 {
			t.Errorf("%s text scores %.3f as windows-1252", tc.cs, got.score)
		}
	}
	western := scoreWindows1252(encodeWith(t, Windows1252, srtText(pick("western", 0, 12))))
	if western.score < 0.8 {
		t.Errorf("Western text scores only %.3f", western.score)
	}
}
