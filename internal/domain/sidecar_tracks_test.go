package domain

import (
	"strings"
	"testing"
)

func TestValidSidecarLanguageTag(t *testing.T) {
	for _, tag := range []string{"en", "yue", "zh-Hans", "zh-Hant-TW", "pt-BR", "es-419", "und"} {
		if !ValidSidecarLanguageTag(tag) {
			t.Error("rejected", tag)
		}
	}
	for _, tag := range []string{"", "e", "engl", "EN", "zh-hans", "zh-HANS", "pt-br", "es-41", "en-US-x", "en-", "-en", "en_US", "zh-Hans-TW-x", "en,ja"} {
		if ValidSidecarLanguageTag(tag) {
			t.Error("accepted", tag)
		}
	}
	// Every tag the name parser emits is storable.
	for _, name := range []string{"Movie.chs&eng.srt", "Movie.简日双语.ass", "Movie.zh-tw.srt", "Movie.pob.srt", "Movie.es-419.srt",
		"Movie.zh-Hant-TW.srt", "Movie.cantonese.mka", "Movie.ger.srt", "Movie.fil.srt"} {
		track, ok := ParseSidecarName("Movie", name, "")
		if !ok || track.Language == "" {
			t.Fatal("parse", name)
		}
		for _, tag := range track.Languages {
			if !ValidSidecarLanguageTag(tag) {
				t.Error(name, "emits unstorable tag", tag)
			}
		}
	}
}

func TestValidSidecarTrackInput(t *testing.T) {
	root := "10000000-0000-4000-8000-000000000001"
	base := func(name string) SidecarTrackInput {
		track, ok := ParseSidecarName("Movie", name, "")
		if !ok {
			t.Fatal("parse", name)
		}
		return SidecarTrackInput{Track: track, RootID: root, RelativePath: "Movie/" + name, Size: 1}
	}
	valid := []SidecarTrackInput{base("Movie.srt"), base("Movie.en.forced.SRT"), base("Movie.chs&eng.ass"), base("Movie.ja.commentary.mka")}
	withCharset := base("Movie.en.srt")
	withCharset.Charset = "GB18030"
	withFingerprint := base("Movie.en.srt")
	withFingerprint.Fingerprint = make([]byte, 32)
	valid = append(valid, withCharset, withFingerprint)
	for _, in := range valid {
		if !ValidSidecarTrackInput(in) {
			t.Error("rejected", in.RelativePath)
		}
	}
	mutate := func(name string, change func(*SidecarTrackInput)) SidecarTrackInput {
		in := base(name)
		change(&in)
		return in
	}
	for label, in := range map[string]SidecarTrackInput{
		"kind":             mutate("Movie.srt", func(in *SidecarTrackInput) { in.Track.Kind = "video" }),
		"kind format":      mutate("Movie.srt", func(in *SidecarTrackInput) { in.Track.Kind = SidecarKindAudio }),
		"unknown format":   mutate("Movie.srt", func(in *SidecarTrackInput) { in.Track.Format, in.RelativePath = "txt", "Movie/Movie.txt" }),
		"extension":        mutate("Movie.srt", func(in *SidecarTrackInput) { in.RelativePath = "Movie/Movie.ass" }),
		"root":             mutate("Movie.srt", func(in *SidecarTrackInput) { in.RootID = "root" }),
		"parent path":      mutate("Movie.srt", func(in *SidecarTrackInput) { in.RelativePath = "../Movie.srt" }),
		"absolute path":    mutate("Movie.srt", func(in *SidecarTrackInput) { in.RelativePath = "/Movie.srt" }),
		"negative size":    mutate("Movie.srt", func(in *SidecarTrackInput) { in.Size = -1 }),
		"fingerprint":      mutate("Movie.srt", func(in *SidecarTrackInput) { in.Fingerprint = []byte{1} }),
		"empty fp":         mutate("Movie.srt", func(in *SidecarTrackInput) { in.Fingerprint = []byte{} }),
		"charset":          mutate("Movie.srt", func(in *SidecarTrackInput) { in.Charset = "latin1" }),
		"audio charset":    mutate("Movie.mka", func(in *SidecarTrackInput) { in.Charset = "UTF-8" }),
		"title control":    mutate("Movie.srt", func(in *SidecarTrackInput) { in.Track.Title = "a\tb" }),
		"title long":       mutate("Movie.srt", func(in *SidecarTrackInput) { in.Track.Title = strings.Repeat("t", 256) }),
		"list no language": mutate("Movie.srt", func(in *SidecarTrackInput) { in.Track.Languages = []string{"en"} }),
		"language no list": mutate("Movie.srt", func(in *SidecarTrackInput) { in.Track.Language = "en" }),
		"first differs":    mutate("Movie.en.srt", func(in *SidecarTrackInput) { in.Track.Languages = []string{"ja", "en"} }),
		"bad tag":          mutate("Movie.en.srt", func(in *SidecarTrackInput) { in.Track.Languages = []string{"en", "EN"} }),
		"too many": mutate("Movie.en.srt", func(in *SidecarTrackInput) {
			in.Track.Languages = []string{"en", "ja", "ko", "fr", "de", "es", "it", "pt", "ru"}
		}),
	} {
		if ValidSidecarTrackInput(in) {
			t.Error("accepted", label)
		}
	}
}

// The media_sidecar_tracks format check and the charset check copy these
// lists; the PostgreSQL tests insert every entry.
func TestSidecarStoredListsAreClosed(t *testing.T) {
	if len(sidecarSubtitleFormats) != 12 || len(sidecarAudioFormats) != 18 || len(SidecarCharsets) != 9 {
		t.Fatal("sidecar format or charset list changed; update migration media_sidecar_tracks and its tests")
	}
}
