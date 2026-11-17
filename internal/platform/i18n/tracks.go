package i18n

import "strings"

// Track display words (G24.2). Third-party clients show the upstream
// MediaStream DisplayTitle in their track menus; the compatibility layer
// assembles it from these words in the locale Locale negotiates. Like the
// error catalog, the tables are package-private, complete for every locale
// and never modified after initialization.

// Track label keys.
const (
	TrackLabelDefault      = "default"
	TrackLabelForced       = "forced"
	TrackLabelExternal     = "external"
	TrackLabelSDH          = "sdh"
	TrackLabelCommentary   = "commentary"
	TrackLabelUndetermined = "undetermined"
)

// localeColumn is the column of a locale in the tables below.
var localeColumn = map[string]int{"en-US": 0, "zh-CN": 1, "zh-TW": 2, "ja-JP": 3}

var trackLabels = map[string][4]string{
	TrackLabelDefault:      {"Default", "默认", "預設", "デフォルト"},
	TrackLabelForced:       {"Forced", "强制", "強制", "強制"},
	TrackLabelExternal:     {"External", "外挂", "外掛", "外部"},
	TrackLabelSDH:          {"SDH", "听障", "聽障", "聴覚障害者用"},
	TrackLabelCommentary:   {"Commentary", "评论", "評論", "コメンタリー"},
	TrackLabelUndetermined: {"Undetermined", "未知语言", "未知語言", "言語不明"},
}

// TrackLabel returns the word for key in locale (a Locale result); an
// unknown locale reads as en-US and an unknown key as itself.
func TrackLabel(locale, key string) string {
	words, ok := trackLabels[key]
	if !ok {
		return key
	}
	return words[localeColumn[locale]]
}

// languageNames are the display names of the primary language subtags a
// media file commonly declares, after normalization to ISO 639-1 where one
// exists. Languages outside the table keep the code the file declares.
var languageNames = map[string][4]string{
	"ar":  {"Arabic", "阿拉伯语", "阿拉伯語", "アラビア語"},
	"bg":  {"Bulgarian", "保加利亚语", "保加利亞語", "ブルガリア語"},
	"bn":  {"Bengali", "孟加拉语", "孟加拉語", "ベンガル語"},
	"ca":  {"Catalan", "加泰罗尼亚语", "加泰隆尼亞語", "カタロニア語"},
	"cs":  {"Czech", "捷克语", "捷克語", "チェコ語"},
	"da":  {"Danish", "丹麦语", "丹麥語", "デンマーク語"},
	"de":  {"German", "德语", "德語", "ドイツ語"},
	"el":  {"Greek", "希腊语", "希臘語", "ギリシャ語"},
	"en":  {"English", "英语", "英語", "英語"},
	"es":  {"Spanish", "西班牙语", "西班牙語", "スペイン語"},
	"et":  {"Estonian", "爱沙尼亚语", "愛沙尼亞語", "エストニア語"},
	"eu":  {"Basque", "巴斯克语", "巴斯克語", "バスク語"},
	"fa":  {"Persian", "波斯语", "波斯語", "ペルシア語"},
	"fi":  {"Finnish", "芬兰语", "芬蘭語", "フィンランド語"},
	"fil": {"Filipino", "菲律宾语", "菲律賓語", "フィリピノ語"},
	"fr":  {"French", "法语", "法語", "フランス語"},
	"gl":  {"Galician", "加利西亚语", "加利西亞語", "ガリシア語"},
	"he":  {"Hebrew", "希伯来语", "希伯來語", "ヘブライ語"},
	"hi":  {"Hindi", "印地语", "印地語", "ヒンディー語"},
	"hr":  {"Croatian", "克罗地亚语", "克羅埃西亞語", "クロアチア語"},
	"hu":  {"Hungarian", "匈牙利语", "匈牙利語", "ハンガリー語"},
	"id":  {"Indonesian", "印度尼西亚语", "印尼語", "インドネシア語"},
	"is":  {"Icelandic", "冰岛语", "冰島語", "アイスランド語"},
	"it":  {"Italian", "意大利语", "義大利語", "イタリア語"},
	"ja":  {"Japanese", "日语", "日語", "日本語"},
	"ko":  {"Korean", "韩语", "韓語", "韓国語"},
	"lt":  {"Lithuanian", "立陶宛语", "立陶宛語", "リトアニア語"},
	"lv":  {"Latvian", "拉脱维亚语", "拉脫維亞語", "ラトビア語"},
	"ms":  {"Malay", "马来语", "馬來語", "マレー語"},
	"nb":  {"Norwegian Bokmål", "书面挪威语", "書面挪威語", "ノルウェー語（ブークモール）"},
	"nl":  {"Dutch", "荷兰语", "荷蘭語", "オランダ語"},
	"no":  {"Norwegian", "挪威语", "挪威語", "ノルウェー語"},
	"pl":  {"Polish", "波兰语", "波蘭語", "ポーランド語"},
	"pt":  {"Portuguese", "葡萄牙语", "葡萄牙語", "ポルトガル語"},
	"ro":  {"Romanian", "罗马尼亚语", "羅馬尼亞語", "ルーマニア語"},
	"ru":  {"Russian", "俄语", "俄語", "ロシア語"},
	"sk":  {"Slovak", "斯洛伐克语", "斯洛伐克語", "スロバキア語"},
	"sl":  {"Slovenian", "斯洛文尼亚语", "斯洛維尼亞語", "スロベニア語"},
	"sr":  {"Serbian", "塞尔维亚语", "塞爾維亞語", "セルビア語"},
	"sv":  {"Swedish", "瑞典语", "瑞典語", "スウェーデン語"},
	"ta":  {"Tamil", "泰米尔语", "坦米爾語", "タミル語"},
	"te":  {"Telugu", "泰卢固语", "泰盧固語", "テルグ語"},
	"th":  {"Thai", "泰语", "泰語", "タイ語"},
	"tl":  {"Tagalog", "他加禄语", "他加祿語", "タガログ語"},
	"tr":  {"Turkish", "土耳其语", "土耳其語", "トルコ語"},
	"uk":  {"Ukrainian", "乌克兰语", "烏克蘭語", "ウクライナ語"},
	"ur":  {"Urdu", "乌尔都语", "烏都語", "ウルドゥー語"},
	"vi":  {"Vietnamese", "越南语", "越南語", "ベトナム語"},
	"yue": {"Cantonese", "粤语", "粵語", "広東語"},
	"zh":  {"Chinese", "中文", "中文", "中国語"},
}

// chineseScripts name the two written forms of Chinese.
var chineseScripts = map[string][4]string{
	"Hans": {"Chinese (Simplified)", "简体中文", "簡體中文", "中国語（簡体字）"},
	"Hant": {"Chinese (Traditional)", "繁体中文", "繁體中文", "中国語（繁体字）"},
}

// LanguageName returns the display name in locale of a canonical language
// tag (primary subtag, optional script and region, as the track language
// normalization emits it). Chinese is named by its script, also when only
// a region implies one (TW, HK, MO: traditional; CN, SG, MY: simplified).
// ok is false for a language outside the table.
func LanguageName(locale, tag string) (string, bool) {
	parts := strings.Split(tag, "-")
	column := localeColumn[locale]
	primary := strings.ToLower(parts[0])
	if primary == "zh" {
		script := ""
		for _, part := range parts[1:] {
			switch {
			case strings.EqualFold(part, "Hans"), strings.EqualFold(part, "CN"), strings.EqualFold(part, "SG"), strings.EqualFold(part, "MY"):
				script = "Hans"
			case strings.EqualFold(part, "Hant"), strings.EqualFold(part, "TW"), strings.EqualFold(part, "HK"), strings.EqualFold(part, "MO"):
				script = "Hant"
			}
			if script != "" {
				break
			}
		}
		if names, ok := chineseScripts[script]; ok {
			return names[column], true
		}
	}
	names, ok := languageNames[primary]
	if !ok {
		return "", false
	}
	return names[column], true
}
