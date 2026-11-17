package compat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestMediaSourceCarriesChaptersAndVideoRange(t *testing.T) {
	i := func(n int64) *int64 { return &n }
	source := domain.PlaybackSource{
		ID:        "0197a1f0-0000-7000-8000-000000000001",
		Container: "mkv",
		Chapters: []domain.PlaybackChapter{
			{Index: 0, StartMicros: 0, Title: "Opening"},
			{Index: 1, StartMicros: 90_500_000},
		},
		Video: []domain.PlaybackVideoTrack{{Index: 0, Codec: "hevc", HDR: []string{"dolby_vision", "hdr10"}, DolbyVisionProfile: i(8), DolbyVisionLevel: i(6)}},
		Audio: []domain.PlaybackAudioTrack{{Index: 1, Codec: "truehd", Features: []string{"dolby_atmos"}}},
	}
	rt := &router{opts: Options{Library: &LibraryOptions{}}}
	info, err := rt.mediaSource("0197a1f0-0000-7000-8000-000000000002", source, "Movie")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(info)
	body := string(raw)
	for _, want := range []string{
		`"Chapters":[{"StartPositionTicks":0,"Name":"Opening"},{"StartPositionTicks":905000000,"Name":"Chapter 2"}]`,
		`"VideoRange":"HDR"`, `"VideoRangeType":"DOVI"`, `"DvProfile":8`, `"DvLevel":6`, `"AudioSpatialFormat":"DolbyAtmos"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "ImageTag") {
		t.Error("chapter image tags are not served")
	}
	if got := chapterInfos(domain.PlaybackSource{}); got != nil {
		t.Errorf("a source without chapters must omit the member: %v", got)
	}
}
