package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sidecar kinds reuse the MediaStream.Kind vocabulary.
const (
	SidecarKindSubtitle = "subtitle"
	SidecarKindAudio    = "audio"

	// SidecarNameMaxBytes bounds every untrusted name segment. It matches the
	// common filesystem limit for one path component.
	SidecarNameMaxBytes = 255
)

// SidecarTrack is what an external subtitle or audio file name claims about
// itself. It is a naming observation only: Format is the lower-case file
// extension, not a probed codec, and a ".sub" file may be either MicroDVD
// text or the data half of a VobSub ".sub"/".idx" pair. Pairing and probing
// belong to the caller.
type SidecarTrack struct {
	Kind   string `json:"kind"`
	Format string `json:"format"`
	// Language is the first (primary) BCP 47 tag; Languages keeps every tag
	// of a multi-language marker such as "chs&eng" in file-name order.
	Language   string   `json:"language,omitempty"`
	Languages  []string `json:"languages,omitempty"`
	Title      string   `json:"title,omitempty"`
	Forced     bool     `json:"forced"`
	SDH        bool     `json:"sdh"`
	Default    bool     `json:"default"`
	Commentary bool     `json:"commentary"`
	// Subdir is the canonical lower-case name of the sidecar subdirectory, or
	// empty when the file sits next to the video.
	Subdir string `json:"subdir,omitempty"`
}

var sidecarSubtitleFormats = map[string]bool{
	"srt": true, "ass": true, "ssa": true, "vtt": true, "webvtt": true,
	"ttml": true, "dfxp": true, "smi": true, "sami": true,
	"sub": true, "idx": true, "sup": true,
}

// WAV carries PCM; raw headerless PCM is not accepted because nothing in the
// name says how to read it.
var sidecarAudioFormats = map[string]bool{
	"mka": true, "aac": true, "m4a": true, "ac3": true, "eac3": true, "ec3": true,
	"dts": true, "dtshd": true, "thd": true, "truehd": true, "mlp": true,
	"flac": true, "alac": true, "opus": true, "ogg": true, "oga": true,
	"mp3": true, "wav": true,
}

var sidecarSubtitleDirs = map[string]string{"subs": "subs", "subtitles": "subtitles", "sub": "sub", "subtitle": "subtitle"}
var sidecarAudioDirs = map[string]string{"audio": "audio", "audios": "audios"}

// ParseSidecarName decides whether candidate is an external subtitle or audio
// track of the video whose file name without extension is videoBase.
// inSubdir is empty for a file next to the video, or the single name of the
// subdirectory (e.g. "Subs", "Audio") that holds candidate.
//
// Accepted names are videoBase itself or videoBase followed by "." tokens,
// compared case-insensitively, then a known extension. Tokens may be a
// language, the flags forced/sdh/cc/default/commentary, or anything else,
// which becomes the title. A bare prefix such as "Movie2.srt" for "Movie" is
// rejected. Because "Movie.Part2.srt" would also match "Movie", callers with
// several videos in one directory must use SelectSidecarVideo.
func ParseSidecarName(videoBase string, candidate string, inSubdir string) (SidecarTrack, bool) {
	if !validSidecarSegment(videoBase) || !validSidecarSegment(candidate) || candidate[0] == '.' {
		return SidecarTrack{}, false
	}
	if inSubdir != "" && !validSidecarSegment(inSubdir) {
		return SidecarTrack{}, false
	}
	dot := strings.LastIndexByte(candidate, '.')
	if dot <= 0 {
		return SidecarTrack{}, false
	}
	stem, ext := candidate[:dot], strings.ToLower(candidate[dot+1:])
	track := SidecarTrack{Format: ext}
	dirs := sidecarSubtitleDirs
	switch {
	case sidecarSubtitleFormats[ext]:
		track.Kind = SidecarKindSubtitle
	case sidecarAudioFormats[ext]:
		track.Kind, dirs = SidecarKindAudio, sidecarAudioDirs
	default:
		return SidecarTrack{}, false
	}
	if inSubdir != "" {
		name, ok := dirs[strings.ToLower(inSubdir)]
		if !ok {
			return SidecarTrack{}, false
		}
		track.Subdir = name
	}
	if len(stem) < len(videoBase) || !strings.EqualFold(stem[:len(videoBase)], videoBase) {
		return SidecarTrack{}, false
	}
	rest := stem[len(videoBase):]
	if rest == "" {
		return track, true
	}
	if rest[0] != '.' {
		return SidecarTrack{}, false
	}
	tokens := strings.Split(rest[1:], ".")
	langAt := -1
	var langs []string
	for i := len(tokens) - 1; i >= 0; i-- {
		if sidecarFlag(strings.ToLower(tokens[i]), &SidecarTrack{}) {
			continue
		}
		if found := sidecarLanguages(tokens[i]); found != nil {
			langAt, langs = i, found
			break
		}
	}
	var title []string
	for i, token := range tokens {
		if i == langAt {
			continue
		}
		if sidecarFlag(strings.ToLower(token), &track) {
			continue
		}
		if strings.TrimSpace(token) == "" {
			return SidecarTrack{}, false
		}
		title = append(title, token)
	}
	if langs != nil {
		track.Language, track.Languages = langs[0], langs
	}
	track.Title = strings.Join(title, ".")
	return track, true
}

// SelectSidecarVideo matches candidate against every video base of one
// directory and keeps the longest matching base, so "Movie.Part2.en.srt"
// belongs to "Movie.Part2" and not to "Movie". Bases equal under case
// folding make the match ambiguous and nothing is selected.
func SelectSidecarVideo(videoBases []string, candidate string, inSubdir string) (int, SidecarTrack, bool) {
	best, ambiguous := -1, false
	var track SidecarTrack
	for i, base := range videoBases {
		parsed, ok := ParseSidecarName(base, candidate, inSubdir)
		if !ok {
			continue
		}
		switch {
		case best < 0 || len(base) > len(videoBases[best]):
			best, track, ambiguous = i, parsed, false
		case len(base) == len(videoBases[best]):
			ambiguous = true
		}
	}
	if best < 0 || ambiguous {
		return -1, SidecarTrack{}, false
	}
	return best, track, true
}

func validSidecarSegment(s string) bool {
	if s == "" || len(s) > SidecarNameMaxBytes || s == "." || s == ".." || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':':
			return false
		case unicode.IsControl(r):
			return false
		case r == '‎' || r == '‏' || (r >= '‪' && r <= '‮') || (r >= '⁦' && r <= '⁩'):
			// Bidi controls can disguise the real extension.
			return false
		}
	}
	return true
}

func sidecarFlag(lower string, track *SidecarTrack) bool {
	switch lower {
	case "forced":
		track.Forced = true
	case "sdh", "cc":
		track.SDH = true
	case "default":
		track.Default = true
	case "commentary":
		track.Commentary = true
	default:
		return false
	}
	return true
}

// sidecarLanguages returns the BCP 47 tags named by one token, or nil.
// A multi-language marker ("chs&eng", "eng+jpn", "简日双语") yields every
// tag in order; all parts must be languages or the token is not one.
func sidecarLanguages(token string) []string {
	if tag, ok := sidecarLanguage(token); ok {
		return []string{tag}
	}
	parts := strings.FieldsFunc(token, func(r rune) bool { return r == '&' || r == '+' || r == '_' || r == '-' })
	if len(parts) >= 2 {
		if tags := sidecarLanguageList(parts, sidecarLanguage); tags != nil {
			return tags
		}
	}
	compact := strings.TrimSuffix(strings.TrimSuffix(token, "双语"), "雙語")
	if utf8.RuneCountInString(compact) >= 2 {
		var runes []string
		for _, r := range compact {
			runes = append(runes, string(r))
		}
		return sidecarLanguageList(runes, func(s string) (string, bool) {
			tag, ok := sidecarHanLanguages[s]
			return tag, ok
		})
	}
	return nil
}

func sidecarLanguageList(parts []string, parse func(string) (string, bool)) []string {
	var tags []string
	seen := map[string]bool{}
	for _, part := range parts {
		tag, ok := parse(part)
		if !ok {
			return nil
		}
		if !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	return tags
}

// sidecarLanguage normalizes one language marker. Region subtags are kept
// ("zh-TW" stays "zh-TW"); the community markers chs/sc and cht/tc map to the
// script tags zh-Hans and zh-Hant. "sc" therefore never means Sardinian.
func sidecarLanguage(token string) (string, bool) {
	lower := strings.ToLower(token)
	if tag, ok := sidecarLanguageAliases[lower]; ok {
		return tag, true
	}
	if tag, ok := sidecarHanLanguages[lower]; ok {
		return tag, true
	}
	parts := strings.FieldsFunc(lower, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) == 0 || len(parts) > 3 || strings.Count(lower, "-")+strings.Count(lower, "_") != len(parts)-1 {
		return "", false
	}
	primary, ok := sidecarPrimaryLanguage(parts[0])
	if !ok {
		return "", false
	}
	out := []string{primary}
	rest := parts[1:]
	if len(rest) > 0 && len(rest[0]) == 4 && sidecarAlpha(rest[0]) {
		out = append(out, strings.ToUpper(rest[0][:1])+rest[0][1:])
		rest = rest[1:]
	}
	if len(rest) > 0 {
		region := rest[0]
		switch {
		case len(region) == 2 && sidecarAlpha(region):
			out = append(out, strings.ToUpper(region))
		case len(region) == 3 && strings.Trim(region, "0123456789") == "":
			out = append(out, region)
		default:
			return "", false
		}
		rest = rest[1:]
	}
	if len(rest) > 0 {
		return "", false
	}
	return strings.Join(out, "-"), true
}

func sidecarPrimaryLanguage(code string) (string, bool) {
	if !sidecarAlpha(code) {
		return "", false
	}
	switch len(code) {
	case 2:
		if strings.Contains(sidecarISO6391, " "+code+":") {
			return code, true
		}
	case 3:
		if tag, ok := sidecarISO6392B[code]; ok {
			return tag, true
		}
		if i := strings.Index(sidecarISO6391, ":"+code+" "); i >= 2 {
			return sidecarISO6391[i-2 : i], true
		}
		if sidecarISO6392Extra[code] {
			return code, true
		}
	}
	return "", false
}

func sidecarAlpha(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return s != ""
}

// sidecarISO6391 pairs every current ISO 639-1 code with its ISO 639-2/T code.
const sidecarISO6391 = " aa:aar ab:abk ae:ave af:afr ak:aka am:amh an:arg ar:ara as:asm av:ava ay:aym az:aze" +
	" ba:bak be:bel bg:bul bi:bis bm:bam bn:ben bo:bod br:bre bs:bos ca:cat ce:che ch:cha co:cos cr:cre" +
	" cs:ces cu:chu cv:chv cy:cym da:dan de:deu dv:div dz:dzo ee:ewe el:ell en:eng eo:epo es:spa et:est" +
	" eu:eus fa:fas ff:ful fi:fin fj:fij fo:fao fr:fra fy:fry ga:gle gd:gla gl:glg gn:grn gu:guj gv:glv" +
	" ha:hau he:heb hi:hin ho:hmo hr:hrv ht:hat hu:hun hy:hye hz:her ia:ina id:ind ie:ile ig:ibo ii:iii" +
	" ik:ipk io:ido is:isl it:ita iu:iku ja:jpn jv:jav ka:kat kg:kon ki:kik kj:kua kk:kaz kl:kal km:khm" +
	" kn:kan ko:kor kr:kau ks:kas ku:kur kv:kom kw:cor ky:kir la:lat lb:ltz lg:lug li:lim ln:lin lo:lao" +
	" lt:lit lu:lub lv:lav mg:mlg mh:mah mi:mri mk:mkd ml:mal mn:mon mr:mar ms:msa mt:mlt my:mya na:nau" +
	" nb:nob nd:nde ne:nep ng:ndo nl:nld nn:nno no:nor nr:nbl nv:nav ny:nya oc:oci oj:oji om:orm or:ori" +
	" os:oss pa:pan pi:pli pl:pol ps:pus pt:por qu:que rm:roh rn:run ro:ron ru:rus rw:kin sa:san sc:srd" +
	" sd:snd se:sme sg:sag si:sin sk:slk sl:slv sm:smo sn:sna so:som sq:sqi sr:srp ss:ssw st:sot su:sun" +
	" sv:swe sw:swa ta:tam te:tel tg:tgk th:tha ti:tir tk:tuk tl:tgl tn:tsn to:ton tr:tur ts:tso tt:tat" +
	" tw:twi ty:tah ug:uig uk:ukr ur:urd uz:uzb ve:ven vi:vie vo:vol wa:wln wo:wol xh:xho yi:yid yo:yor" +
	" za:zha zh:zho zu:zul "

// sidecarISO6392B maps ISO 639-2/B codes that differ from their /T codes.
var sidecarISO6392B = map[string]string{
	"alb": "sq", "arm": "hy", "baq": "eu", "bur": "my", "chi": "zh", "cze": "cs", "dut": "nl",
	"fre": "fr", "geo": "ka", "ger": "de", "gre": "el", "ice": "is", "mac": "mk", "mao": "mi",
	"may": "ms", "per": "fa", "rum": "ro", "slo": "sk", "tib": "bo", "wel": "cy",
}

// Three-letter codes without a two-letter form that are kept as they are.
var sidecarISO6392Extra = map[string]bool{"yue": true, "fil": true, "und": true, "mul": true}

var sidecarLanguageAliases = map[string]string{
	"chs": "zh-Hans", "sc": "zh-Hans", "hans": "zh-Hans", "zh-chs": "zh-Hans",
	"cht": "zh-Hant", "tc": "zh-Hant", "hant": "zh-Hant", "zh-cht": "zh-Hant",
	"jap": "ja", "jp": "ja", "cmn": "zh", "pob": "pt-BR",
	"chinese": "zh", "english": "en", "japanese": "ja", "korean": "ko", "cantonese": "yue",
	"french": "fr", "german": "de", "spanish": "es", "italian": "it", "portuguese": "pt",
	"russian": "ru", "thai": "th", "vietnamese": "vi", "indonesian": "id", "arabic": "ar",
	"dutch": "nl", "polish": "pl", "turkish": "tr", "swedish": "sv", "hindi": "hi",
}

// sidecarHanLanguages holds Chinese-script markers. Single characters double
// as the parts of compact bilingual markers such as "简日" or "繁英双语".
var sidecarHanLanguages = map[string]string{
	"简": "zh-Hans", "簡": "zh-Hans", "简体": "zh-Hans", "簡體": "zh-Hans", "简中": "zh-Hans", "簡中": "zh-Hans",
	"简体中文": "zh-Hans", "簡體中文": "zh-Hans",
	"繁": "zh-Hant", "繁体": "zh-Hant", "繁體": "zh-Hant", "繁中": "zh-Hant", "繁体中文": "zh-Hant", "繁體中文": "zh-Hant",
	"中": "zh", "中文": "zh", "国语": "zh", "國語": "zh",
	"粤": "yue", "粵": "yue", "粤语": "yue", "粵語": "yue",
	"日": "ja", "日文": "ja", "日语": "ja", "日語": "ja", "日本語": "ja",
	"英": "en", "英文": "en", "英语": "en", "英語": "en",
	"韩": "ko", "韓": "ko", "韩文": "ko", "韓文": "ko", "韩语": "ko", "韓語": "ko",
}
