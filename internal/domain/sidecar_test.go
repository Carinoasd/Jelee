package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSidecarNameFormats(t *testing.T) {
	subtitles := []string{"srt", "ass", "ssa", "vtt", "webvtt", "ttml", "dfxp", "smi", "sami", "sub", "idx", "sup"}
	audio := []string{"mka", "aac", "m4a", "ac3", "eac3", "ec3", "dts", "dtshd", "thd", "truehd", "mlp", "flac", "alac", "opus", "ogg", "oga", "mp3", "wav"}
	for _, list := range []struct {
		kind string
		exts []string
	}{{SidecarKindSubtitle, subtitles}, {SidecarKindAudio, audio}} {
		for _, ext := range list.exts {
			for _, name := range []string{"Movie." + ext, "Movie.en." + strings.ToUpper(ext)} {
				got, ok := ParseSidecarName("Movie", name, "")
				if !ok || got.Kind != list.kind || got.Format != ext {
					t.Errorf("%s: got %+v %v", name, got, ok)
				}
			}
		}
	}
	for _, name := range []string{"Movie.mkv", "Movie.mp4", "Movie.nfo", "Movie.jpg", "Movie.txt", "Movie.xml", "Movie.pcm", "Movie", "Movie."} {
		if got, ok := ParseSidecarName("Movie", name, ""); ok {
			t.Errorf("%s accepted as %+v", name, got)
		}
	}
}

func TestParseSidecarNameLanguages(t *testing.T) {
	cases := []struct {
		token string
		want  []string
	}{
		{"en", []string{"en"}}, {"EN", []string{"en"}}, {"eng", []string{"en"}}, {"English", []string{"en"}},
		{"ja", []string{"ja"}}, {"jpn", []string{"ja"}}, {"jp", []string{"ja"}}, {"jap", []string{"ja"}},
		{"日文", []string{"ja"}}, {"日本語", []string{"ja"}},
		{"zh", []string{"zh"}}, {"zho", []string{"zh"}}, {"chi", []string{"zh"}}, {"中文", []string{"zh"}},
		{"chs", []string{"zh-Hans"}}, {"SC", []string{"zh-Hans"}}, {"zh-Hans", []string{"zh-Hans"}}, {"zh_hans", []string{"zh-Hans"}},
		{"简体", []string{"zh-Hans"}}, {"簡體", []string{"zh-Hans"}}, {"简中", []string{"zh-Hans"}},
		{"cht", []string{"zh-Hant"}}, {"TC", []string{"zh-Hant"}}, {"zh-hant", []string{"zh-Hant"}},
		{"繁体", []string{"zh-Hant"}}, {"繁體", []string{"zh-Hant"}}, {"繁中", []string{"zh-Hant"}},
		{"zh-CN", []string{"zh-CN"}}, {"zh-tw", []string{"zh-TW"}}, {"zh_HK", []string{"zh-HK"}},
		{"zh-Hant-TW", []string{"zh-Hant-TW"}}, {"pt-BR", []string{"pt-BR"}}, {"pob", []string{"pt-BR"}},
		{"es-419", []string{"es-419"}}, {"sr-Latn", []string{"sr-Latn"}},
		{"ger", []string{"de"}}, {"deu", []string{"de"}}, {"fre", []string{"fr"}}, {"kor", []string{"ko"}},
		{"韓語", []string{"ko"}}, {"yue", []string{"yue"}}, {"粵語", []string{"yue"}}, {"und", []string{"und"}},
		{"chs&eng", []string{"zh-Hans", "en"}}, {"CHT&JPN", []string{"zh-Hant", "ja"}},
		{"eng+jpn", []string{"en", "ja"}}, {"chs_eng", []string{"zh-Hans", "en"}}, {"sc&tc", []string{"zh-Hans", "zh-Hant"}},
		{"en&eng", []string{"en"}},
		{"简日", []string{"zh-Hans", "ja"}}, {"繁英双语", []string{"zh-Hant", "en"}}, {"簡日雙語", []string{"zh-Hans", "ja"}},
		{"简繁", []string{"zh-Hans", "zh-Hant"}}, {"中英", []string{"zh", "en"}},
	}
	for _, tc := range cases {
		got, ok := ParseSidecarName("Movie", "Movie."+tc.token+".srt", "")
		if !ok || !reflect.DeepEqual(got.Languages, tc.want) || got.Language != tc.want[0] || got.Title != "" {
			t.Errorf("%q: got %+v %v", tc.token, got, ok)
		}
	}
	// Tokens that only look like language markers become the title.
	for _, token := range []string{"xx", "qq", "abc", "zh-Hans-CN-x", "en--us", "en-u", "eng-1234", "chs&xx", "双语", "简x", "Signs & Songs", "hd", "zh-12"} {
		got, ok := ParseSidecarName("Movie", "Movie."+token+".ass", "")
		if !ok || got.Language != "" || got.Languages != nil || got.Title != token {
			t.Errorf("%q: got %+v %v", token, got, ok)
		}
	}
}

func TestParseSidecarNameFlagsAndTitle(t *testing.T) {
	cases := []struct {
		name string
		want SidecarTrack
	}{
		{"Movie.srt", SidecarTrack{Kind: "subtitle", Format: "srt"}},
		{"Movie.en.forced.srt", SidecarTrack{Kind: "subtitle", Format: "srt", Language: "en", Languages: []string{"en"}, Forced: true}},
		{"Movie.en.sdh.srt", SidecarTrack{Kind: "subtitle", Format: "srt", Language: "en", Languages: []string{"en"}, SDH: true}},
		{"Movie.en.CC.srt", SidecarTrack{Kind: "subtitle", Format: "srt", Language: "en", Languages: []string{"en"}, SDH: true}},
		{"Movie.chs.forced.sdh.default.ass", SidecarTrack{Kind: "subtitle", Format: "ass", Language: "zh-Hans", Languages: []string{"zh-Hans"}, Forced: true, SDH: true, Default: true}},
		{"Movie.default.forced.zh-TW.vtt", SidecarTrack{Kind: "subtitle", Format: "vtt", Language: "zh-TW", Languages: []string{"zh-TW"}, Forced: true, Default: true}},
		{"Movie.Forced.srt", SidecarTrack{Kind: "subtitle", Format: "srt", Forced: true}},
		{"Movie.en.Signs & Songs.ass", SidecarTrack{Kind: "subtitle", Format: "ass", Language: "en", Languages: []string{"en"}, Title: "Signs & Songs"}},
		{"Movie.Director Commentary.eng.commentary.mka", SidecarTrack{Kind: "audio", Format: "mka", Language: "en", Languages: []string{"en"}, Title: "Director Commentary", Commentary: true}},
		{"Movie.jpn.commentary.default.forced.ac3", SidecarTrack{Kind: "audio", Format: "ac3", Language: "ja", Languages: []string{"ja"}, Commentary: true, Default: true, Forced: true}},
		{"Movie.国语.flac", SidecarTrack{Kind: "audio", Format: "flac", Language: "zh", Languages: []string{"zh"}}},
		// Only the rightmost language token is the language.
		{"Movie.en.ja.srt", SidecarTrack{Kind: "subtitle", Format: "srt", Language: "ja", Languages: []string{"ja"}, Title: "en"}},
		{"Movie.Extended Cut.srt", SidecarTrack{Kind: "subtitle", Format: "srt", Title: "Extended Cut"}},
		{"Movie.2.chs&eng.ass", SidecarTrack{Kind: "subtitle", Format: "ass", Language: "zh-Hans", Languages: []string{"zh-Hans", "en"}, Title: "2"}},
		// Case-insensitive base match, including non-ASCII letters.
		{"MOVIE.EN.SRT", SidecarTrack{Kind: "subtitle", Format: "srt", Language: "en", Languages: []string{"en"}}},
	}
	for _, tc := range cases {
		got, ok := ParseSidecarName("Movie", tc.name, "")
		if !ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v %v\nwant %+v", tc.name, got, ok, tc.want)
		}
	}
	if got, ok := ParseSidecarName("影片 Ωmega", "影片 ωMEGA.繁中.ass", ""); !ok || got.Language != "zh-Hant" {
		t.Errorf("unicode base: %+v %v", got, ok)
	}
}

func TestParseSidecarNameSubdirs(t *testing.T) {
	cases := []struct {
		name, dir, want string
		ok              bool
	}{
		{"Movie.en.srt", "Subs", "subs", true},
		{"Movie.en.srt", "subtitles", "subtitles", true},
		{"Movie.en.srt", "SUB", "sub", true},
		{"Movie.en.srt", "Subtitle", "subtitle", true},
		{"Movie.en.mka", "Audio", "audio", true},
		{"Movie.en.mka", "audios", "audios", true},
		{"Movie.en.srt", "Audio", "", false},
		{"Movie.en.mka", "Subs", "", false},
		{"Movie.en.srt", "Extras", "", false},
		{"Movie.en.srt", "..", "", false},
		{"Movie.en.srt", ".", "", false},
		{"Movie.en.srt", "Subs/..", "", false},
		{"Movie.en.srt", "a\\Subs", "", false},
		{"Movie.en.srt", "Subs\x00", "", false},
	}
	for _, tc := range cases {
		got, ok := ParseSidecarName("Movie", tc.name, tc.dir)
		if ok != tc.ok || got.Subdir != tc.want {
			t.Errorf("%s in %q: got %+v %v", tc.name, tc.dir, got, ok)
		}
	}
}

func TestParseSidecarNameRejectsOtherVideosAndUnsafeInput(t *testing.T) {
	long := strings.Repeat("a", SidecarNameMaxBytes)
	cases := []struct{ base, name string }{
		{"Movie", "Movie2.srt"},
		{"Movie", "Movie 2.en.srt"},
		{"Movie", "Movi.srt"},
		{"Movie", "Other.en.srt"},
		{"Movie", "TheMovie.en.srt"},
		{"Movie", "Movie_en.srt"},
		{"Movie", "Movie-en.srt"},
		{"Movie (2020)", "Movie.en.srt"},
		{"Movie", "Movie..srt"},
		{"Movie", "Movie.en..srt"},
		{"Movie", "Movie. .srt"},
		{"Movie", ".Movie.en.srt"},
		{"Movie", "../Movie.en.srt"},
		{"Movie", "x/Movie.en.srt"},
		{"Movie", "x\\Movie.en.srt"},
		{"Movie", "Movie.en.srt:stream"},
		{"Movie", "Movie.en\x00.srt"},
		{"Movie", "Movie.en\n.srt"},
		{"Movie", "Movie.en\x7f.srt"},
		{"Movie", "Movie.\u202ets.srt"},
		{"Movie", "Movie.en\xff.srt"},
		{"Movie", "Movie." + long + ".srt"},
		{"", "Movie.en.srt"},
		{".", ".en.srt"},
		{"..", "...srt"},
		{"a/b", "a/b.srt"},
		{"Mo\x01vie", "Mo\x01vie.srt"},
		{long + "a", long + "a.srt"},
	}
	for _, tc := range cases {
		if got, ok := ParseSidecarName(tc.base, tc.name, ""); ok {
			t.Errorf("%q for %q accepted as %+v", tc.name, tc.base, got)
		}
	}
	// The bound is inclusive.
	base := strings.Repeat("a", SidecarNameMaxBytes-4)
	if _, ok := ParseSidecarName(base, base+".srt", ""); !ok {
		t.Error("name at the length bound rejected")
	}
}

func TestSelectSidecarVideo(t *testing.T) {
	bases := []string{"Movie", "Movie.Part2", "Movie 2", "Other"}
	cases := []struct {
		name  string
		index int
		lang  string
		title string
	}{
		{"Movie.en.srt", 0, "en", ""},
		{"Movie.Part2.en.srt", 1, "en", ""},
		{"movie.part2.srt", 1, "", ""},
		{"Movie.Part3.en.srt", 0, "en", "Part3"},
		{"Movie 2.chs.ass", 2, "zh-Hans", ""},
		{"Other.cht.ass", 3, "zh-Hant", ""},
	}
	for _, tc := range cases {
		index, got, ok := SelectSidecarVideo(bases, tc.name, "")
		if !ok || index != tc.index || got.Language != tc.lang || got.Title != tc.title {
			t.Errorf("%s: got %d %+v %v", tc.name, index, got, ok)
		}
	}
	for _, name := range []string{"Unknown.en.srt", "Movie2.srt"} {
		if index, _, ok := SelectSidecarVideo(bases, name, ""); ok {
			t.Errorf("%s matched %d", name, index)
		}
	}
	if index, _, ok := SelectSidecarVideo([]string{"Movie", "MOVIE"}, "movie.en.srt", ""); ok {
		t.Errorf("ambiguous bases matched %d", index)
	}
	if _, _, ok := SelectSidecarVideo(nil, "Movie.en.srt", ""); ok {
		t.Error("empty base list matched")
	}
}
