package domain

import (
	"reflect"
	"sort"
	"testing"
)

// Synthetic probe fixtures: they model normalized MediaMetadata shapes, not
// real media files.
type videoOpt func(*MediaStream)

func labelVideo(codec string, w, h int64, opts ...videoOpt) MediaStream {
	s := MediaStream{Index: 0, Kind: "video", Video: &MediaVideo{}}
	if codec != "" {
		s.Codec = metadataPtr(codec)
	}
	if w > 0 {
		s.Video.Width = metadataPtr(w)
	}
	if h > 0 {
		s.Video.Height = metadataPtr(h)
	}
	for _, o := range opts {
		o(&s)
	}
	return s
}

func transfer(v string) videoOpt {
	return func(s *MediaStream) { s.Video.ColorTransfer = metadataPtr(v) }
}
func dovi(profile, compat int64) videoOpt {
	return func(s *MediaStream) {
		s.Video.DolbyVision = &MediaDolbyVision{Profile: metadataPtr(profile), CompatibilityID: metadataPtr(compat), RPU: metadataPtr(true)}
	}
}
func hdr10plus() videoOpt { return func(s *MediaStream) { s.Video.HDR10Plus = metadataPtr(true) } }
func mastering() videoOpt {
	return func(s *MediaStream) {
		s.Video.MasteringDisplay = &MediaMasteringDisplay{MaxLuminance: &MediaRational{1000, 1}}
	}
}
func isDefault() videoOpt  { return func(s *MediaStream) { s.Default = metadataPtr(true) } }
func index(i int) videoOpt { return func(s *MediaStream) { s.Index = i } }

func labelAudio(i int, codec, profile string, channels int64, layout string) MediaStream {
	s := MediaStream{Index: i, Kind: "audio", Codec: metadataPtr(codec), Audio: &MediaAudio{}}
	if profile != "" {
		s.Profile = metadataPtr(profile)
		if profile == "Dolby TrueHD + Dolby Atmos" || profile == "Dolby Digital Plus + Dolby Atmos" {
			s.Audio.Atmos = metadataPtr(true)
		}
	}
	if channels > 0 {
		s.Audio.Channels = metadataPtr(channels)
	}
	if layout != "" {
		s.Audio.ChannelLayout = metadataPtr(layout)
	}
	return s
}

func probeOf(streams ...MediaStream) MediaMetadata { return MediaMetadata{Streams: streams} }

func TestVersionResolutionTier(t *testing.T) {
	cases := []struct {
		w, h int64
		want VersionResolution
	}{
		{3840, 2160, VersionResolution2160p}, {4096, 2160, VersionResolution2160p}, {3840, 1600, VersionResolution2160p}, {3840, 1608, VersionResolution2160p},
		{4096, 1716, VersionResolution2160p}, {3996, 2160, VersionResolution2160p}, {2880, 2160, VersionResolution2160p}, // 4:3 UHD
		{1920, 1080, VersionResolution1080p}, {1920, 800, VersionResolution1080p}, {1920, 804, VersionResolution1080p}, {1916, 1036, VersionResolution1080p},
		{1440, 1080, VersionResolution1080p}, {1080, 1920, VersionResolution1080p}, {2048, 858, VersionResolution1080p}, {1920, 400, VersionResolution1080p}, // 4.8:1
		{1600, 900, VersionResolution720p}, {1280, 720, VersionResolution720p}, {1280, 536, VersionResolution720p}, {960, 720, VersionResolution720p},
		{720, 576, VersionResolution576p}, {1024, 576, VersionResolution576p}, {720, 480, VersionResolution480p}, {640, 480, VersionResolution480p},
		{854, 480, VersionResolution480p}, {640, 360, VersionResolution360p}, {426, 240, VersionResolution240p}, {176, 144, VersionResolutionSD},
		{2560, 1440, VersionResolution1440p}, {2560, 1072, VersionResolution1440p}, {3200, 1800, VersionResolution1440p}, {7680, 4320, VersionResolution4320p},
		{7680, 3200, VersionResolution4320p}, {0, 1080, ""}, {1920, 0, ""},
	}
	for _, c := range cases {
		if got := resolutionTier(c.w, c.h); got != c.want {
			t.Errorf("resolutionTier(%d,%d) = %q, want %q", c.w, c.h, got, c.want)
		}
	}
}

func TestVersionLabelsResolution(t *testing.T) {
	cases := []struct {
		name     string
		meta     MediaMetadata
		file     string
		want     VersionResolution
		from     VersionLabelSource
		conflict bool
	}{
		{"probe scope crop", probeOf(labelVideo("hevc", 3840, 1600)), "Movie.2019.mkv", VersionResolution2160p, VersionLabelFromProbe, false},
		{"probe matches name", probeOf(labelVideo("h264", 1920, 800)), "Movie.2019.1080p.BluRay.x264.mkv", VersionResolution1080p, VersionLabelFromProbe, false},
		{"probe beats upscale claim", probeOf(labelVideo("hevc", 1920, 1080)), "Movie.2019.2160p.WEB-DL.mkv", VersionResolution1080p, VersionLabelFromProbe, true},
		{"probe beats 4K claim", probeOf(labelVideo("hevc", 1280, 720)), "Movie 4K.mkv", VersionResolution720p, VersionLabelFromProbe, true},
		{"no dims uses name", probeOf(labelVideo("hevc", 0, 0)), "Movie.2019.2160p.mkv", VersionResolution2160p, VersionLabelFromFilename, false},
		{"empty probe 4k", MediaMetadata{}, "电影 4K HDR.mkv", VersionResolution2160p, VersionLabelFromFilename, false},
		{"empty probe uhd", MediaMetadata{}, "Movie.UHD.BluRay.mkv", VersionResolution2160p, VersionLabelFromFilename, false},
		{"empty probe 1080i", MediaMetadata{}, "Show.S01E01.1080i.HDTV.ts", VersionResolution1080p, VersionLabelFromFilename, false},
		{"glued bd prefix", MediaMetadata{}, "[字幕组][电影][BD1080P].mp4", VersionResolution1080p, VersionLabelFromFilename, false},
		{"size token", MediaMetadata{}, "Movie [1920x804].mkv", VersionResolution1080p, VersionLabelFromFilename, false},
		{"8k", MediaMetadata{}, "Demo.8K.mkv", VersionResolution4320p, VersionLabelFromFilename, false},
		{"nothing", MediaMetadata{}, "Movie.mkv", "", "", false},
		{"cover art ignored", probeOf(MediaStream{Index: 1, Kind: "video", Codec: metadataPtr("mjpeg"), Video: &MediaVideo{Width: metadataPtr(int64(3000)), Height: metadataPtr(int64(3000))}}, labelVideo("h264", 1280, 720)), "", VersionResolution720p, VersionLabelFromProbe, false},
		{"only cover art", probeOf(MediaStream{Index: 0, Kind: "video", Codec: metadataPtr("png"), Video: &MediaVideo{Width: metadataPtr(int64(1000)), Height: metadataPtr(int64(1000))}}), "Album.1080p.mka", VersionResolution1080p, VersionLabelFromFilename, false},
		{"default stream wins", probeOf(labelVideo("h264", 3840, 2160, index(0)), labelVideo("h264", 1280, 720, index(1), isDefault())), "", VersionResolution720p, VersionLabelFromProbe, false},
		{"largest wins without default", probeOf(labelVideo("h264", 1280, 720, index(0)), labelVideo("h264", 1920, 1080, index(1))), "", VersionResolution1080p, VersionLabelFromProbe, false},
		{"directory ignored", MediaMetadata{}, "/media/2160p/Movie.mkv", "", "", false},
		{"windows path", MediaMetadata{}, `D:\Films\Movie.720p.mkv`, VersionResolution720p, VersionLabelFromFilename, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := VersionLabelsFromProbe(c.meta, c.file)
			if got.Resolution != c.want || got.ResolutionFrom != c.from || hasConflict(got, "resolution") != c.conflict {
				t.Fatalf("got %q from %q conflicts %+v", got.Resolution, got.ResolutionFrom, got.Conflicts)
			}
		})
	}
}

func TestVersionLabelsHDR(t *testing.T) {
	cases := []struct {
		name     string
		video    MediaStream
		file     string
		want     []VersionHDR
		from     VersionLabelSource
		profile  int64
		conflict bool
	}{
		{"pq hdr10", labelVideo("hevc", 3840, 2160, transfer("smpte2084"), mastering()), "", []VersionHDR{VersionHDR10}, VersionLabelFromProbe, 0, false},
		{"pq without static metadata", labelVideo("hevc", 3840, 2160, transfer("smpte2084")), "", []VersionHDR{VersionHDR10}, VersionLabelFromProbe, 0, false},
		{"hdr10plus", labelVideo("hevc", 3840, 2160, transfer("smpte2084"), hdr10plus(), mastering()), "Movie.2160p.HDR10+.mkv", []VersionHDR{VersionHDR10Plus, VersionHDR10}, VersionLabelFromProbe, 0, false},
		{"hlg", labelVideo("hevc", 3840, 2160, transfer("arib-std-b67")), "Broadcast.HLG.ts", []VersionHDR{VersionHDRHLG}, VersionLabelFromProbe, 0, false},
		{"dv p8.1", labelVideo("hevc", 3840, 2160, transfer("smpte2084"), dovi(8, 1)), "Movie.DV.HDR10.mkv", []VersionHDR{VersionHDRDolbyVision, VersionHDR10}, VersionLabelFromProbe, 8, false},
		{"dv p8.4 hlg base", labelVideo("hevc", 3840, 2160, dovi(8, 4)), "", []VersionHDR{VersionHDRDolbyVision, VersionHDRHLG}, VersionLabelFromProbe, 8, false},
		{"dv p5 no fallback even with pq", labelVideo("hevc", 3840, 2160, transfer("smpte2084"), dovi(5, 0)), "Movie.DoVi.mkv", []VersionHDR{VersionHDRDolbyVision}, VersionLabelFromProbe, 5, false},
		{"dv p5 vs hdr10 claim", labelVideo("hevc", 3840, 2160, dovi(5, 0)), "Movie.DV.HDR10.mkv", []VersionHDR{VersionHDRDolbyVision}, VersionLabelFromProbe, 5, true},
		{"dv p8.2 sdr base", labelVideo("hevc", 3840, 2160, transfer("bt709"), dovi(8, 2)), "", []VersionHDR{VersionHDRDolbyVision}, VersionLabelFromProbe, 8, false},
		{"dv p7 dual layer", labelVideo("hevc", 3840, 2160, transfer("smpte2084"), dovi(7, 6)), "Movie.Dolby.Vision.mkv", []VersionHDR{VersionHDRDolbyVision, VersionHDR10}, VersionLabelFromProbe, 7, false},
		{"dv plus hdr10plus", labelVideo("hevc", 3840, 2160, transfer("smpte2084"), dovi(8, 1), hdr10plus()), "", []VersionHDR{VersionHDRDolbyVision, VersionHDR10Plus, VersionHDR10}, VersionLabelFromProbe, 8, false},
		{"mastering without transfer implies pq", labelVideo("hevc", 3840, 2160, mastering()), "", []VersionHDR{VersionHDR10}, VersionLabelFromProbe, 0, false},
		{"explicit sdr transfer beats mastering", labelVideo("hevc", 1920, 1080, transfer("bt709"), mastering()), "", nil, VersionLabelFromProbe, 0, false},
		{"sdr probe beats hdr name", labelVideo("h264", 1920, 1080, transfer("bt709")), "Movie.1080p.HDR.mkv", nil, VersionLabelFromProbe, 0, true},
		{"hdr10 probe beats dv name", labelVideo("hevc", 3840, 2160, transfer("smpte2084")), "Movie.2160p.DV.mkv", []VersionHDR{VersionHDR10}, VersionLabelFromProbe, 0, true},
		{"generic hdr agrees with hdr10", labelVideo("hevc", 3840, 2160, transfer("smpte2084")), "Movie.2160p.HDR.mkv", []VersionHDR{VersionHDR10}, VersionLabelFromProbe, 0, false},
		{"no color info uses name", labelVideo("hevc", 3840, 2160), "Movie.2160p.DV.HDR10.mkv", []VersionHDR{VersionHDRDolbyVision, VersionHDR10}, VersionLabelFromFilename, 0, false},
		{"name hdr10+ implies hdr10", labelVideo("hevc", 3840, 2160), "Movie.HDR10Plus.mkv", []VersionHDR{VersionHDR10Plus, VersionHDR10}, VersionLabelFromFilename, 0, false},
		{"chinese dv", labelVideo("", 0, 0), "沙丘 杜比视界 4K.mkv", []VersionHDR{VersionHDRDolbyVision}, VersionLabelFromFilename, 0, false},
		{"japanese dv", labelVideo("", 0, 0), "映画【ドルビービジョン】.mkv", []VersionHDR{VersionHDRDolbyVision}, VersionLabelFromFilename, 0, false},
		{"generic only", labelVideo("", 0, 0), "Movie.HDR.mkv", []VersionHDR{VersionHDRGeneric}, VersionLabelFromFilename, 0, false},
		{"unknown", labelVideo("hevc", 1920, 1080), "Movie.mkv", nil, "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := VersionLabelsFromProbe(probeOf(c.video), c.file)
			if !reflect.DeepEqual(got.HDR, c.want) || got.HDRFrom != c.from || hasConflict(got, "hdr") != c.conflict {
				t.Fatalf("got %v from %q conflicts %+v", got.HDR, got.HDRFrom, got.Conflicts)
			}
			if c.profile == 0 && got.DolbyVisionProfile != nil || c.profile != 0 && (got.DolbyVisionProfile == nil || *got.DolbyVisionProfile != c.profile) {
				t.Fatalf("dv profile %v, want %d", got.DolbyVisionProfile, c.profile)
			}
		})
	}
}

func TestVersionLabelsVideoCodec(t *testing.T) {
	cases := []struct {
		name, codec, file, want string
		from                    VersionLabelSource
		conflict                bool
	}{
		{"probe hevc", "hevc", "", "hevc", VersionLabelFromProbe, false},
		{"probe agrees x265", "hevc", "Movie.2019.1080p.x265.mkv", "hevc", VersionLabelFromProbe, false},
		{"probe beats name", "h264", "Movie.2019.1080p.HEVC.mkv", "h264", VersionLabelFromProbe, true},
		{"name h.264", "", "Movie.2019.1080p.H.264.mkv", "h264", VersionLabelFromFilename, false},
		{"name h265", "", "Movie.2019.H265.mkv", "hevc", VersionLabelFromFilename, false},
		{"name avc", "", "Movie.2019.BluRay.AVC.REMUX.mkv", "h264", VersionLabelFromFilename, false},
		{"name av1", "", "Movie.2019.AV1.mkv", "av1", VersionLabelFromFilename, false},
		{"name vc-1", "", "Movie.2007.VC-1.mkv", "vc1", VersionLabelFromFilename, false},
		{"name xvid", "", "Movie.2003.DVDRip.XviD.avi", "mpeg4", VersionLabelFromFilename, false},
		{"none", "", "Movie.mkv", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := VersionLabelsFromProbe(probeOf(labelVideo(c.codec, 1920, 1080)), c.file)
			if got.VideoCodec != c.want || got.VideoCodecFrom != c.from || hasConflict(got, "videoCodec") != c.conflict {
				t.Fatalf("got %q from %q conflicts %+v", got.VideoCodec, got.VideoCodecFrom, got.Conflicts)
			}
		})
	}
}

func TestVersionLabelsAudio(t *testing.T) {
	cases := []struct {
		name     string
		streams  []MediaStream
		file     string
		want     VersionAudio
		conflict string
	}{
		{"truehd atmos", []MediaStream{labelAudio(1, "truehd", "Dolby TrueHD + Dolby Atmos", 8, "7.1")}, "", VersionAudio{Codec: "truehd", Format: "TrueHD", Channels: "7.1", Atmos: true, From: VersionLabelFromProbe}, ""},
		{"eac3 atmos", []MediaStream{labelAudio(1, "eac3", "Dolby Digital Plus + Dolby Atmos", 6, "5.1(side)")}, "", VersionAudio{Codec: "eac3", Format: "DD+", Channels: "5.1", Atmos: true, From: VersionLabelFromProbe}, ""},
		{"dts-hd ma", []MediaStream{labelAudio(1, "dts", "DTS-HD MA", 8, "")}, "", VersionAudio{Codec: "dts", Format: "DTS-HD MA", Channels: "7.1", From: VersionLabelFromProbe}, ""},
		{"future dts:x profile", []MediaStream{labelAudio(1, "dts", "DTS-HD MA + DTS:X", 8, "7.1")}, "", VersionAudio{Codec: "dts", Format: "DTS-HD MA", Channels: "7.1", DTSX: true, From: VersionLabelFromProbe}, ""},
		{"dts:x from name on dts-hd ma", []MediaStream{labelAudio(1, "dts", "DTS-HD MA", 8, "7.1")}, "Movie.2019.DTS-X.7.1.mkv", VersionAudio{Codec: "dts", Format: "DTS-HD MA", Channels: "7.1", DTSX: true, From: VersionLabelFromProbe, ImmersiveFrom: VersionLabelFromFilename}, ""},
		{"dts:x claim on core dts conflicts", []MediaStream{labelAudio(1, "dts", "DTS", 6, "5.1")}, "Movie.2019.DTS:X.mkv", VersionAudio{Codec: "dts", Format: "DTS", Channels: "5.1", From: VersionLabelFromProbe}, "dtsX"},
		{"atmos from name when profile missing", []MediaStream{labelAudio(1, "truehd", "", 8, "7.1")}, "Movie.2019.TrueHD.7.1.Atmos.mkv", VersionAudio{Codec: "truehd", Channels: "7.1", Atmos: true, From: VersionLabelFromProbe, ImmersiveFrom: VersionLabelFromFilename}, ""},
		{"atmos claim vs plain truehd profile", []MediaStream{labelAudio(1, "truehd", "Dolby TrueHD", 8, "7.1")}, "Movie.2019.TrueHD.Atmos.mkv", VersionAudio{Codec: "truehd", Format: "TrueHD", Channels: "7.1", From: VersionLabelFromProbe}, "atmos"},
		{"atmos claim vs aac", []MediaStream{labelAudio(1, "aac", "LC", 2, "stereo")}, "Movie.2019.Atmos.mkv", VersionAudio{Codec: "aac", Channels: "2.0", From: VersionLabelFromProbe}, "atmos"},
		{"best of several tracks", []MediaStream{labelAudio(1, "ac3", "", 6, "5.1"), labelAudio(2, "truehd", "Dolby TrueHD + Dolby Atmos", 8, "7.1"), labelAudio(3, "aac", "LC", 2, "stereo")}, "", VersionAudio{Codec: "truehd", Format: "TrueHD", Channels: "7.1", Atmos: true, From: VersionLabelFromProbe}, ""},
		{"name codec present on another track", []MediaStream{labelAudio(1, "truehd", "Dolby TrueHD", 8, "7.1"), labelAudio(2, "ac3", "", 6, "5.1")}, "Movie.2019.AC3.mkv", VersionAudio{Codec: "truehd", Format: "TrueHD", Channels: "7.1", From: VersionLabelFromProbe}, ""},
		{"name codec absent conflicts", []MediaStream{labelAudio(1, "aac", "LC", 2, "stereo")}, "Movie.2019.FLAC.mkv", VersionAudio{Codec: "aac", Channels: "2.0", From: VersionLabelFromProbe}, "audioCodec"},
		{"default breaks tie", []MediaStream{labelAudio(1, "aac", "LC", 2, "stereo"), func() MediaStream {
			s := labelAudio(2, "aac", "LC", 6, "5.1")
			s.Default = metadataPtr(true)
			return s
		}()}, "", VersionAudio{Codec: "aac", Channels: "5.1", From: VersionLabelFromProbe}, ""},
		{"channels count only", []MediaStream{labelAudio(1, "flac", "", 3, "")}, "", VersionAudio{Codec: "flac", Channels: "3ch", From: VersionLabelFromProbe}, ""},
		{"mono layout", []MediaStream{labelAudio(1, "mp3", "", 0, "mono")}, "", VersionAudio{Codec: "mp3", Channels: "1.0", From: VersionLabelFromProbe}, ""},
		{"channels from name when probe lacks", []MediaStream{labelAudio(1, "eac3", "", 0, "")}, "Show.S01E01.1080p.WEB-DL.DDP5.1.H.264.mkv", VersionAudio{Codec: "eac3", Channels: "5.1", From: VersionLabelFromProbe}, ""},
		{"name only ddp atmos", nil, "Show.S01E01.2160p.WEB-DL.DDP5.1.Atmos.mkv", VersionAudio{Codec: "eac3", Channels: "5.1", Atmos: true, From: VersionLabelFromFilename}, ""},
		{"name only dd+", nil, "Show.S01E01.DD+2.0.mkv", VersionAudio{Codec: "eac3", Channels: "2.0", From: VersionLabelFromFilename}, ""},
		{"name only dts-hd ma", nil, "Movie.2010.BluRay.DTS-HD.MA.5.1.mkv", VersionAudio{Codec: "dts", Format: "DTS-HD MA", Channels: "5.1", From: VersionLabelFromFilename}, ""},
		{"name only chinese atmos", nil, "电影 杜比全景声.mkv", VersionAudio{Atmos: true, From: VersionLabelFromFilename}, ""},
		{"none", nil, "Movie.mkv", VersionAudio{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := VersionLabelsFromProbe(probeOf(append([]MediaStream{labelVideo("h264", 1920, 1080)}, c.streams...)...), c.file)
			if !reflect.DeepEqual(got.Audio, c.want) {
				t.Fatalf("audio %+v, want %+v", got.Audio, c.want)
			}
			for _, f := range []string{"atmos", "dtsX", "audioCodec"} {
				if hasConflict(got, f) != (f == c.conflict) {
					t.Fatalf("conflicts %+v, want %q", got.Conflicts, c.conflict)
				}
			}
		})
	}
}

func TestVersionLabelsReleaseTokens(t *testing.T) {
	cases := []struct {
		file     string
		source   VersionSource
		editions []VersionEdition
		custom   string
	}{
		{"Movie.2019.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos-GRP.mkv", VersionSourceRemux, nil, ""},
		{"Movie.2019.BDRemux.mkv", VersionSourceRemux, nil, ""},
		{"Movie.2019.1080p.Blu-ray.x264.mkv", VersionSourceBluRay, nil, ""},
		{"Movie.2019.1080p.WEB-DL.mkv", VersionSourceWebDL, nil, ""},
		{"Movie.2019.1080p.WEBRip.mkv", VersionSourceWebRip, nil, ""},
		{"Show.2019.S01E01.HDTV.ts", VersionSourceHDTV, nil, ""},
		{"Movie.2001.DVDRip.avi", VersionSourceDVD, nil, ""},
		{"Movie.2001.Directors.Cut.1080p.mkv", "", []VersionEdition{VersionEditionDirectorsCut}, ""},
		{"Movie (2001) Director's Cut.mkv", "", []VersionEdition{VersionEditionDirectorsCut}, ""},
		{"Movie.2001.DIRECTORS_CUT.mkv", "", []VersionEdition{VersionEditionDirectorsCut}, ""},
		{"Movie.2001.Extended.Cut.REMUX.mkv", VersionSourceRemux, []VersionEdition{VersionEditionExtended}, ""},
		{"Movie.2001.EXTENDED.Remastered.mkv", "", []VersionEdition{VersionEditionExtended, VersionEditionRemastered}, ""},
		{"Movie.2001.UNRATED.mkv", "", []VersionEdition{VersionEditionUnrated}, ""},
		{"Movie.2001.Uncut.mkv", "", []VersionEdition{VersionEditionUncut}, ""},
		{"Movie.2001.Theatrical.Cut.mkv", "", []VersionEdition{VersionEditionTheatrical}, ""},
		{"Movie.2001.IMAX.2160p.mkv", "", []VersionEdition{VersionEditionIMAX}, ""},
		{"Movie.2001.Open.Matte.mkv", "", []VersionEdition{VersionEditionOpenMatte}, ""},
		{"Blade Runner 2049 (2017) Final Cut.mkv", "", []VersionEdition{VersionEditionFinalCut}, ""},
		{"Movie.2001.Ultimate.Edition.mkv", "", []VersionEdition{VersionEditionUltimate}, ""},
		{"Movie.2001.Special.Edition.mkv", "", []VersionEdition{VersionEditionSpecial}, ""},
		{"Movie.2001.Criterion.mkv", "", []VersionEdition{VersionEditionCriterion}, ""},
		{"Movie (2001) {edition-Director's Cut}.mkv", "", []VersionEdition{VersionEditionDirectorsCut}, ""},
		{"Movie (2001) {Edition-Fan  Restoration}.mkv", "", nil, "Fan Restoration"},
		{"天堂电影院 导演剪辑版 1080p.mkv", "", []VersionEdition{VersionEditionDirectorsCut}, ""},
		{"天堂電影院 導演剪輯版.mkv", "", []VersionEdition{VersionEditionDirectorsCut}, ""},
		{"指环王 加长版 蓝光原盘REMUX.mkv", VersionSourceRemux, []VersionEdition{VersionEditionExtended}, ""},
		{"魔戒 加長版 藍光.mkv", VersionSourceBluRay, []VersionEdition{VersionEditionExtended}, ""},
		{"映画 ディレクターズ・カット版.mkv", "", []VersionEdition{VersionEditionDirectorsCut}, ""},
		{"映画 エクステンデッド・エディション.mkv", "", []VersionEdition{VersionEditionExtended}, ""},
		{"電影 未刪減版.mkv", "", []VersionEdition{VersionEditionUncut}, ""},
		{"アニメ 完全版 リマスター.mkv", "", []VersionEdition{VersionEditionExtended, VersionEditionRemastered}, ""},
		// Title words before the year are not release tokens.
		{"The Final Cut (2004) 1080p.mkv", "", nil, ""},
		{"Extended Family (2019) WEB-DL.mkv", VersionSourceWebDL, nil, ""},
		{"/movies/Directors Cut Collection/Movie.mkv", "", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			got := VersionLabelsFromProbe(MediaMetadata{}, c.file)
			if got.Source != c.source || !reflect.DeepEqual(got.Editions, c.editions) || got.CustomEdition != c.custom {
				t.Fatalf("source %q editions %v custom %q", got.Source, got.Editions, got.CustomEdition)
			}
		})
	}
}

func TestVersionLabelsDisplayAndScore(t *testing.T) {
	uhd := probeOf(labelVideo("hevc", 3840, 1600, transfer("smpte2084"), dovi(8, 1)), labelAudio(1, "truehd", "Dolby TrueHD + Dolby Atmos", 8, "7.1"))
	got := VersionLabelsFromProbe(uhd, "Movie.2019.2160p.BluRay.REMUX.Directors.Cut.mkv")
	if want := "2160p · DV HDR10 · HEVC · TrueHD Atmos 7.1 · REMUX · Director's Cut"; got.DisplayName != want {
		t.Fatalf("display %q", got.DisplayName)
	}
	plus := VersionLabelsFromProbe(probeOf(labelVideo("hevc", 3840, 2160, transfer("smpte2084"), hdr10plus())), "")
	if plus.DisplayName != "2160p · HDR10+ · HEVC" {
		t.Fatalf("display %q", plus.DisplayName)
	}
	dtsx := VersionLabelsFromProbe(probeOf(labelVideo("h264", 720, 576), labelAudio(1, "dts", "DTS-HD MA", 8, "7.1")), "Movie.2001.DTS-X.{edition-Fan Cut}.mkv")
	if dtsx.DisplayName != "576p · H.264 · DTS:X 7.1 · Fan Cut" {
		t.Fatalf("display %q", dtsx.DisplayName)
	}
	if got := VersionLabelsFromProbe(MediaMetadata{}, "Movie.mkv"); got.DisplayName != "" || got.QualityScore != 0 || got.Conflicts != nil {
		t.Fatalf("empty labels %+v", got)
	}

	// Each line is strictly better than the next; a weaker high-priority field
	// must never be outweighed by stronger low-priority fields.
	ordered := []VersionLabels{
		VersionLabelsFromProbe(uhd, "Movie.2019.REMUX.mkv"),
		VersionLabelsFromProbe(uhd, "Movie.2019.WEB-DL.mkv"),
		VersionLabelsFromProbe(probeOf(labelVideo("hevc", 3840, 2160, transfer("smpte2084")), labelAudio(1, "truehd", "Dolby TrueHD + Dolby Atmos", 8, "7.1")), "Movie.2019.REMUX.mkv"),
		VersionLabelsFromProbe(probeOf(labelVideo("h264", 3840, 2160, transfer("bt709")), labelAudio(1, "truehd", "Dolby TrueHD + Dolby Atmos", 8, "7.1")), "Movie.2019.REMUX.mkv"),
		VersionLabelsFromProbe(probeOf(labelVideo("av1", 3840, 2160, transfer("bt709")), labelAudio(1, "aac", "LC", 2, "stereo")), "Movie.2019.WEB-DL.mkv"),
		VersionLabelsFromProbe(probeOf(labelVideo("h264", 1920, 800, transfer("smpte2084"), dovi(8, 1)), labelAudio(1, "truehd", "Dolby TrueHD + Dolby Atmos", 8, "7.1")), "Movie.2019.REMUX.mkv"),
		VersionLabelsFromProbe(probeOf(labelVideo("h264", 1920, 1080), labelAudio(1, "dts", "DTS-HD MA", 6, "5.1")), "Movie.2019.BluRay.mkv"),
		VersionLabelsFromProbe(probeOf(labelVideo("h264", 1920, 1080), labelAudio(1, "ac3", "", 6, "5.1")), "Movie.2019.BluRay.mkv"),
		VersionLabelsFromProbe(probeOf(labelVideo("hevc", 1920, 1080), labelAudio(1, "ac3", "", 6, "5.1")), "Movie.2019.WEBRip.mkv"),
		VersionLabelsFromProbe(probeOf(labelVideo("h264", 1920, 1080), labelAudio(1, "ac3", "", 6, "5.1")), "Movie.2019.WEBRip.mkv"),
		VersionLabelsFromProbe(probeOf(labelVideo("h264", 1280, 720)), ""),
		VersionLabelsFromProbe(probeOf(labelVideo("mpeg2video", 720, 480)), "Movie.2001.DVD.mkv"),
		VersionLabelsFromProbe(MediaMetadata{}, "Movie.mkv"),
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1].QualityScore <= ordered[i].QualityScore {
			t.Errorf("score[%d]=%d (%s) not above score[%d]=%d (%s)", i-1, ordered[i-1].QualityScore, ordered[i-1].DisplayName, i, ordered[i].QualityScore, ordered[i].DisplayName)
		}
	}
	shuffled := append([]VersionLabels(nil), ordered...)
	sort.SliceStable(shuffled, func(i, j int) bool { return shuffled[i].QualityScore < shuffled[j].QualityScore })
	sort.SliceStable(shuffled, func(i, j int) bool { return shuffled[i].QualityScore > shuffled[j].QualityScore })
	if !reflect.DeepEqual(shuffled, ordered) {
		t.Fatal("score sort is not stable/total for distinct versions")
	}
	again := VersionLabelsFromProbe(uhd, "Movie.2019.2160p.BluRay.REMUX.Directors.Cut.mkv")
	if !reflect.DeepEqual(again, got) {
		t.Fatal("derivation is not deterministic")
	}
}

func TestVersionLabelsConflictReportsBothSides(t *testing.T) {
	got := VersionLabelsFromProbe(probeOf(labelVideo("h264", 1920, 1080, transfer("bt709")), labelAudio(1, "aac", "LC", 2, "stereo")), "Movie.2019.2160p.DV.HEVC.TrueHD.Atmos.mkv")
	want := []VersionLabelConflict{
		{Field: "resolution", Probe: "1080p", Filename: "2160p"},
		{Field: "hdr", Probe: "sdr", Filename: "dolby_vision"},
		{Field: "videoCodec", Probe: "h264", Filename: "hevc"},
		{Field: "audioCodec", Probe: "aac", Filename: "truehd"},
		{Field: "atmos", Probe: "aac", Filename: "atmos"},
	}
	if !reflect.DeepEqual(got.Conflicts, want) {
		t.Fatalf("conflicts %+v", got.Conflicts)
	}
	if got.DisplayName != "1080p · H.264 · AAC 2.0" {
		t.Fatalf("probe must win the display: %q", got.DisplayName)
	}
}

func TestVersionLabelsOddInputs(t *testing.T) {
	for _, file := range []string{"", ".mkv", "/", `\`, "\xff\xfe.mkv", "{edition-}.mkv", "..........", "movie.part1.rar"} {
		got := VersionLabelsFromProbe(MediaMetadata{}, file)
		if got.Resolution != "" || got.Source != "" || got.Editions != nil || got.DisplayName != "" {
			t.Errorf("%q => %+v", file, got)
		}
	}
	// Streams with nil sub-structures or unknown kinds must not panic.
	got := VersionLabelsFromProbe(MediaMetadata{Streams: []MediaStream{{Kind: "video"}, {Kind: "audio"}, {Kind: "subtitle"}, {Kind: "audio", Audio: &MediaAudio{}}}}, "")
	if got.DisplayName != "" {
		t.Fatalf("labels %+v", got)
	}
}

func hasConflict(l VersionLabels, field string) bool {
	for _, c := range l.Conflicts {
		if c.Field == field {
			return true
		}
	}
	return false
}
