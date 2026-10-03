package domain

import "testing"

// BenchmarkVersionLabels resolves labels for a fixed set of synthetic probe
// shapes paired with filenames that agree, conflict or carry the only evidence.
func BenchmarkVersionLabels(b *testing.B) {
	cases := []struct {
		meta MediaMetadata
		file string
	}{
		{probeOf(labelVideo("hevc", 3840, 1600, transfer("smpte2084"), mastering()), labelAudio(1, "truehd", "Dolby TrueHD + Dolby Atmos", 8, "7.1")), "Movie.2019.2160p.UHD.BluRay.HDR.TrueHD.Atmos.7.1.x265.mkv"},
		{probeOf(labelVideo("h264", 1920, 1080), labelAudio(1, "aac", "LC", 2, "stereo")), "Show.S01E01.1080p.WEB-DL.AAC2.0.H.264.mkv"},
		{probeOf(labelVideo("hevc", 3840, 2160, transfer("smpte2084"), dovi(8, 1)), labelAudio(1, "eac3", "Dolby Digital Plus + Dolby Atmos", 6, "5.1(side)")), "Movie.DV.HDR10.2160p.mkv"},
		{probeOf(labelVideo("hevc", 1920, 1080)), "Movie.2019.2160p.WEB-DL.mkv"},
		{MediaMetadata{}, "[字幕组][电影][BD1080P].mp4"},
		{MediaMetadata{}, "Movie [1920x804].mkv"},
	}
	b.ReportAllocs()
	for b.Loop() {
		for _, c := range cases {
			if got := VersionLabelsFromProbe(c.meta, c.file); got.Resolution == "" {
				b.Fatalf("no resolution for %q", c.file)
			}
		}
	}
}
