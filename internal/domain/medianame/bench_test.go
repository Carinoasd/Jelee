package medianame

import "testing"

// benchPaths is a fixed mix of the naming shapes a library scan sees: movies,
// season/episode, anime absolute numbering, daily shows, extras and CJK names.
var benchPaths = []string{
	"Movies/The Example (2019)/The Example (2019) [1080p].mkv",
	"Movies/Another.Film.2021.2160p.UHD.BluRay.x265.HDR.mkv",
	"Shows/Show (2010)/Season 02/Show - S02E07 - Episode Title.mkv",
	"Shows/Show/Season 1/Show.S01E01E02.720p.WEB-DL.mkv",
	"Anime/[Group] Title - 07 [1080p][ABCDEF12].mkv",
	"Anime/作品名/第2季/作品名 第05话 [1080P].mp4",
	"Daily/Talk Show/2024-01-31 Guest Name.mkv",
	"Movies/Feature (2018)/extras/Behind the Scenes.mkv",
	"Shows/Show/Specials/Show S00E03.mkv",
	"Movies/Part.Film.Part.2.2005.DVDRip.avi",
}

func BenchmarkParsePath(b *testing.B) {
	opts := Options{DateEpisodes: true}
	b.ReportAllocs()
	for b.Loop() {
		for _, p := range benchPaths {
			if got := ParsePath(p, opts); got.Rejected != RejectNone {
				b.Fatalf("rejected %q: %v", p, got.Rejected)
			}
		}
	}
}
