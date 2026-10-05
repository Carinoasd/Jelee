package subtitles

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/text/encoding"
)

// Dialogue lines written for these tests; none are taken from a real work.
var corpus = map[string][]string{
	"zh-Hans": {
		"你到底在说什么？我完全听不懂。",
		"我们必须在天黑之前离开这个地方。",
		"别担心，一切都会好起来的。",
		"这是我这辈子见过最美的风景。",
		"你还记得我们第一次见面的时候吗？",
		"对不起，我不是故意要骗你的。",
		"快走！他们马上就要追上来了！",
		"今天晚上我请你吃饭，怎么样？",
		"我已经决定了，明天就出发去北京。",
		"你为什么从来都不告诉我真相？",
		"这个问题比我们想象的要复杂得多。",
		"谢谢你一直陪在我身边。",
	},
	"zh-Hant": {
		"你到底在說什麼？我完全聽不懂。",
		"我們必須在天黑之前離開這個地方。",
		"別擔心，一切都會好起來的。",
		"這是我這輩子見過最美的風景。",
		"你還記得我們第一次見面的時候嗎？",
		"對不起，我不是故意要騙你的。",
		"快走！他們馬上就要追上來了！",
		"今天晚上我請你吃飯，怎麼樣？",
		"我已經決定了，明天就出發去台北。",
		"你為什麼從來都不告訴我真相？",
		"這個問題比我們想像的要複雜得多。",
		"謝謝你一直陪在我身邊。",
	},
	"ja": {
		"一体何を言っているんだ？全然わからないよ。",
		"暗くなる前にここを離れなければならない。",
		"心配しないで、きっと大丈夫だから。",
		"こんなに美しい景色は初めて見た。",
		"私たちが初めて会った日のこと、覚えてる？",
		"ごめんなさい、騙すつもりはなかったの。",
		"早く逃げろ！奴らがすぐに追いついてくるぞ！",
		"今夜は僕がご飯をおごるよ。どうかな？",
		"もう決めたんだ。明日東京へ出発する。",
		"どうして本当のことを教えてくれなかったの？",
		"この問題は思っていたよりずっと複雑だ。",
		"ずっとそばにいてくれて、ありがとう。",
	},
	"ko": {
		"도대체 무슨 소리를 하는 거야? 전혀 모르겠어.",
		"어두워지기 전에 여기를 떠나야 해.",
		"걱정하지 마, 다 잘 될 거야.",
		"이렇게 아름다운 풍경은 처음 봐.",
		"우리가 처음 만났던 날 기억나?",
		"미안해, 속이려고 했던 건 아니었어.",
		"빨리 도망쳐! 놈들이 곧 따라올 거야!",
		"오늘 저녁은 내가 살게. 어때?",
		"이미 결정했어. 내일 서울로 떠날 거야.",
		"왜 한 번도 진실을 말해 주지 않았어?",
		"이 문제는 우리가 생각했던 것보다 훨씬 복잡해.",
		"항상 내 곁에 있어 줘서 고마워.",
	},
	"western": {
		"Qu’est-ce que tu racontes ? Je ne comprends rien.",
		"Nous devons quitter cet endroit avant la tombée de la nuit.",
		"Ne t’inquiète pas, tout va s’arranger.",
		"Das ist die schönste Landschaft, die ich je gesehen habe.",
		"Erinnerst du dich an unser erstes Treffen?",
		"Lo siento, no quería engañarte.",
		"¡Corre! ¡Ya casi nos alcanzan!",
		"Esta noche invito yo a cenar, ¿qué te parece?",
		"Já decidi: amanhã parto para São Paulo.",
		"Pourquoi ne m’as-tu jamais dit la vérité ?",
		"Dieses Problem ist viel komplizierter, als wir dachten.",
		"Merci d’être toujours resté à mes côtés… « vraiment ».",
	},
}

// pick returns n lines starting at offset, wrapping around.
func pick(lang string, offset, n int) []string {
	lines := corpus[lang]
	out := make([]string, n)
	for i := range out {
		out[i] = lines[(offset+i)%len(lines)]
	}
	return out
}

func srtText(lines []string) string {
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%d\r\n00:%02d:%02d,%03d --> 00:%02d:%02d,%03d\r\n%s\r\n\r\n",
			i+1, i/20, (i*3)%60, i*37%1000, i/20, (i*3+2)%60, i*53%1000, line)
	}
	return b.String()
}

func assText(lines []string) string {
	var b strings.Builder
	b.WriteString("[Script Info]\nTitle: Sample\nScriptType: v4.00+\nPlayResX: 1920\nPlayResY: 1080\n\n")
	b.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	b.WriteString("Style: Default,Arial,64,&H00FFFFFF,&H000000FF,&H00000000,&H64000000,0,0,0,0,100,100,0,0,1,3,1,2,40,40,40,1\n\n")
	b.WriteString("[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for i, line := range lines {
		fmt.Fprintf(&b, "Dialogue: 0,0:%02d:%02d.%02d,0:%02d:%02d.%02d,Default,,0,0,0,,{\\fad(200,200)}%s\n",
			i/20, (i*3)%60, i*7%100, i/20, (i*3+2)%60, i*11%100, line)
	}
	return b.String()
}

func encodeWith(t testing.TB, cs Charset, text string) []byte {
	t.Helper()
	enc, ok := cs.Encoding()
	if !ok {
		t.Fatalf("no encoding for %s", cs)
	}
	if cs == UTF8 {
		return []byte(text)
	}
	out, err := encoding.ReplaceUnsupported(enc.NewEncoder()).Bytes([]byte(text))
	if err != nil {
		t.Fatalf("encode %s: %v", cs, err)
	}
	// Strict check: every rune must round trip, or the sample is not real.
	if _, err := enc.NewEncoder().Bytes([]byte(text)); err != nil {
		t.Fatalf("sample not representable in %s: %v", cs, err)
	}
	return out
}

func withBOM(cs Charset, data []byte) []byte {
	var bom []byte
	switch cs {
	case UTF8:
		bom = []byte{0xef, 0xbb, 0xbf}
	case UTF16LE:
		bom = []byte{0xff, 0xfe}
	case UTF16BE:
		bom = []byte{0xfe, 0xff}
	case GB18030:
		bom = []byte{0x84, 0x31, 0x95, 0x33}
	}
	return append(bom, data...)
}
