package formatsamples

import (
	"fmt"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

// Cue lines of the story in each language. They are original sentences, long
// enough for character set detection to be decisive.
var stories = map[string][]string{
	"en":      {"Hello, and welcome to our little town.", "The weather is lovely today, let us go for a walk.", "Please pass me that book, thank you.", "They leave for the coast tomorrow at eight.", "I have never seen such a beautiful view.", "Do not worry, everything will be fine."},
	"zh-Hans": {"你好，欢迎来到这个小镇。", "今天的天气真不错，我们去散步吧。", "请把那本书递给我，谢谢。", "他们明天早上八点出发去海边。", "我从来没有见过这么漂亮的风景。", "不要担心，一切都会好起来的。"},
	"zh-Hant": {"你好，歡迎來到這個小鎮。", "今天的天氣真不錯，我們去散步吧。", "請把那本書遞給我，謝謝。", "他們明天早上八點出發去海邊。", "我從來沒有見過這麼漂亮的風景。", "不要擔心，一切都會好起來的。"},
	"ja":      {"こんにちは、この町へようこそ。", "今日はとても良い天気ですね。散歩に行きましょう。", "その本を取ってください、ありがとう。", "彼らは明日の朝八時に海へ出発します。", "こんなに美しい景色は初めて見ました。", "心配しないでください、きっと大丈夫です。"},
	"ko":      {"안녕하세요, 이 마을에 오신 것을 환영합니다.", "오늘 날씨가 정말 좋네요. 산책하러 갑시다.", "그 책 좀 건네주세요, 고맙습니다.", "그들은 내일 아침 여덟 시에 바다로 떠납니다.", "이렇게 아름다운 풍경은 처음 봅니다.", "걱정하지 마세요, 모든 게 잘될 거예요."},
	"fr":      {"Bonjour, et bienvenue dans notre petit village.", "Il fait très beau aujourd'hui, allons nous promener près de l'église.", "Passez-moi ce livre, s'il vous plaît, merci beaucoup.", "Ils partent demain matin à huit heures pour la côte.", "Je n'ai jamais vu un paysage aussi magnifique.", "Ne vous inquiétez pas, tout ira très bien. À bientôt !"},
}

func clock(i int, sep string) (string, string) {
	return fmt.Sprintf("00:00:%02d%s000", i*2, sep), fmt.Sprintf("00:00:%02d%s500", i*2+1, sep)
}

func srt(lines []string) string {
	var b strings.Builder
	for i, l := range lines {
		start, end := clock(i, ",")
		fmt.Fprintf(&b, "%d\r\n%s --> %s\r\n%s\r\n\r\n", i+1, start, end, l)
	}
	return b.String()
}

func vtt(lines []string) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i, l := range lines {
		start, end := clock(i, ".")
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", start, end, l)
	}
	return b.String()
}

func ass(lines []string, v4plus bool) string {
	var b strings.Builder
	if v4plus {
		b.WriteString("[Script Info]\r\nTitle: Jelee sample\r\nScriptType: v4.00+\r\n\r\n[V4+ Styles]\r\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\r\nStyle: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1\r\n\r\n[Events]\r\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\r\n")
	} else {
		b.WriteString("[Script Info]\r\nTitle: Jelee sample\r\nScriptType: v4.00\r\n\r\n[V4 Styles]\r\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, TertiaryColour, BackColour, Bold, Italic, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, AlphaLevel, Encoding\r\nStyle: Default,Arial,20,16777215,255,0,0,0,0,1,1,0,2,10,10,10,0,1\r\n\r\n[Events]\r\nFormat: Marked, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\r\n")
	}
	for i, l := range lines {
		start, end := fmt.Sprintf("0:00:%02d.00", i*2), fmt.Sprintf("0:00:%02d.50", i*2+1)
		if v4plus {
			fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\r\n", start, end, l)
		} else {
			fmt.Fprintf(&b, "Dialogue: Marked=0,%s,%s,Default,,0,0,0,,%s\r\n", start, end, l)
		}
	}
	return b.String()
}

func ttml(lines []string) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<tt xmlns=\"http://www.w3.org/ns/ttml\" xml:lang=\"en\">\n<body><div>\n")
	for i, l := range lines {
		start, end := clock(i, ".")
		fmt.Fprintf(&b, "<p begin=\"%s\" end=\"%s\">%s</p>\n", start, end, l)
	}
	b.WriteString("</div></body>\n</tt>\n")
	return b.String()
}

func smi(lines []string, class string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<SAMI>\r\n<HEAD>\r\n<TITLE>Jelee sample</TITLE>\r\n<STYLE TYPE=\"text/css\">\r\n<!--\r\nP { font-size: 20pt; }\r\n.%s { Name: sample; lang: en-US; SAMIType: CC; }\r\n-->\r\n</STYLE>\r\n</HEAD>\r\n<BODY>\r\n", class)
	for i, l := range lines {
		fmt.Fprintf(&b, "<SYNC Start=%d><P Class=%s>%s\r\n<SYNC Start=%d><P Class=%s>&nbsp;\r\n", i*2000, class, l, i*2000+1500, class)
	}
	b.WriteString("</BODY>\r\n</SAMI>\r\n")
	return b.String()
}

func microdvd(lines []string) string {
	var b strings.Builder
	b.WriteString("{1}{1}25.000\r\n")
	for i, l := range lines {
		fmt.Fprintf(&b, "{%d}{%d}%s\r\n", i*50+1, i*50+38, l)
	}
	return b.String()
}

func withBOM(enc string, text string) []byte {
	switch enc {
	case "utf8":
		return append([]byte{0xEF, 0xBB, 0xBF}, text...)
	}
	panic("unsupported BOM encoding")
}

func encode(enc encoding.Encoding, text string) []byte {
	out, err := enc.NewEncoder().Bytes([]byte(text))
	if err != nil {
		panic("formatsamples: " + err.Error())
	}
	return out
}

func utf16(endian unicode.Endianness, text string) []byte {
	return encode(unicode.UTF16(endian, unicode.UseBOM), text)
}

// textFiles builds every text sample from the stories: the plain files in
// UTF-8, then the legacy encodings. The bytes are deterministic.
func textFiles() map[string][]byte {
	en := stories["en"]
	zh := stories["zh-Hans"]
	return map[string][]byte{
		"plain.srt":    []byte(srt(en)),
		"plain.ass":    []byte(ass(en, true)),
		"plain.ssa":    []byte(ass(en, false)),
		"plain.vtt":    []byte(vtt(en)),
		"plain.ttml":   []byte(ttml(en)),
		"plain.smi":    []byte(smi(en, "ENCC")),
		"microdvd.sub": []byte(microdvd(en)),

		"utf8-bom.srt":     withBOM("utf8", srt(zh)),
		"utf16le.srt":      utf16(unicode.LittleEndian, srt(zh)),
		"utf16be.srt":      utf16(unicode.BigEndian, srt(zh)),
		"gb18030.srt":      encode(simplifiedchinese.GB18030, srt(zh)),
		"big5.srt":         encode(traditionalchinese.Big5, srt(stories["zh-Hant"])),
		"shift_jis.srt":    encode(japanese.ShiftJIS, srt(stories["ja"])),
		"euc-jp.srt":       encode(japanese.EUCJP, srt(stories["ja"])),
		"euc-kr.srt":       encode(korean.EUCKR, srt(stories["ko"])),
		"windows-1252.srt": encode(charmap.Windows1252, srt(stories["fr"])),

		"gb18030.ass":            encode(simplifiedchinese.GB18030, ass(zh, true)),
		"big5.ssa":               encode(traditionalchinese.Big5, ass(stories["zh-Hant"], false)),
		"euc-kr.smi":             encode(korean.EUCKR, smi(stories["ko"], "KRCC")),
		"shift_jis-microdvd.sub": encode(japanese.ShiftJIS, microdvd(stories["ja"])),
	}
}
