package domain

import (
	"fmt"
	"slices"
	"testing"
)

func TestSidecarOwnerDirectory(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"Movie.mkv", ""},
		{"Movie.en.srt", ""},
		{"Subs/Movie.en.srt", ""},
		{"Audio/Movie.ja.flac", ""},
		{"Films/Movie (2010)/Movie (2010).mkv", "Films/Movie (2010)"},
		{"Films/Movie (2010)/Subs/Movie (2010).en.srt", "Films/Movie (2010)"},
		{"Films/Movie (2010)/SUBTITLES/x.srt", "Films/Movie (2010)"},
		{"Films/Movie (2010)/sub/x.srt", "Films/Movie (2010)"},
		{"Films/Movie (2010)/Subtitle/x.srt", "Films/Movie (2010)"},
		{"Films/Movie (2010)/audios/x.flac", "Films/Movie (2010)"},
		{"Films/Movie (2010)/Subs/Extra/x.srt", "Films/Movie (2010)/Subs/Extra"},
		{"Films/Movie (2010)/MySubs/x.srt", "Films/Movie (2010)/MySubs"},
		{"Films/Movie (2010)/Subsx/x.srt", "Films/Movie (2010)/Subsx"},
		// Only ASCII letters fold, exactly as in the database function.
		{"Films/ſubs/x.srt", "Films/ſubs"},
		{"Subs/Subs/x.srt", "Subs"},
	} {
		if got := SidecarOwnerDirectory(tc.path); got != tc.want {
			t.Errorf("SidecarOwnerDirectory(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestSidecarLookupDirectories(t *testing.T) {
	for _, tc := range []struct {
		dir  string
		want []string
	}{
		{".", []string{""}},
		{"", []string{""}},
		{"Films/Movie", []string{"Films/Movie"}},
		// A video kept inside a folder named like a sidecar folder still
		// sees its siblings, which are owned by the folder above.
		{"Films/Audio", []string{"Films/Audio", "Films"}},
		{"Subs", []string{"Subs", ""}},
	} {
		if got := SidecarLookupDirectories(tc.dir); !slices.Equal(got, tc.want) {
			t.Errorf("SidecarLookupDirectories(%q) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

func sidecarScanFiles(paths ...string) []SidecarScanFile {
	files := make([]SidecarScanFile, 0, len(paths))
	for i, p := range paths {
		kind := "other"
		switch {
		case len(p) > 4 && (p[len(p)-4:] == ".mkv" || p[len(p)-4:] == ".mp4"):
			kind = "video"
		case len(p) > 4 && p[len(p)-4:] == ".nfo":
			kind = "nfo"
		}
		files = append(files, SidecarScanFile{Path: p, Kind: kind, Size: int64(i + 1), ModifiedUnixNano: int64(1000 + i)})
	}
	return files
}

func pairedPaths(pairs map[string][]SidecarScanMatch) map[string][]string {
	out := map[string][]string{}
	for video, matches := range pairs {
		for _, m := range matches {
			out[video] = append(out[video], m.File.Path)
		}
	}
	return out
}

func TestPairSidecarFilesRules(t *testing.T) {
	files := sidecarScanFiles(
		"M/Movie.mkv",
		"M/Movie.Part2.mkv",
		"M/Movie.en.srt",
		"M/movie.EN.forced.SRT",
		"M/Movie.Part2.ja.ass",
		"M/Movie.zh-Hans.Director.default.sdh.ass",
		"M/Subs/Movie.chs&eng.ass",
		"M/subtitles/Movie.Part2.fr.vtt",
		"M/Audio/Movie.ja.commentary.flac",
		"M/Movie.de.ac3",
		// Rejected: wrong folder kind, unrelated names, bare prefix, nested
		// deeper, not a sidecar extension, a video and an NFO.
		"M/Audio/Movie.en.srt",
		"M/Subs/Movie.en.flac",
		"M/Other.en.srt",
		"M/Movie2.en.srt",
		"M/Subs/Deep/Movie.en.srt",
		"M/Movie.en.txt",
		"M/Movie.nfo",
		"M/Movie.Sample.mkv",
		// Another directory never pairs.
		"N/Movie.en.srt",
		"Movie.en.srt",
	)
	got := pairedPaths(PairSidecarFiles("M", files))
	want := map[string][]string{
		"M/Movie.mkv":       {"M/Audio/Movie.ja.commentary.flac", "M/Movie.de.ac3", "M/Movie.en.srt", "M/Movie.zh-Hans.Director.default.sdh.ass", "M/Subs/Movie.chs&eng.ass", "M/movie.EN.forced.SRT"},
		"M/Movie.Part2.mkv": {"M/Movie.Part2.ja.ass", "M/subtitles/Movie.Part2.fr.vtt"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pairs\n got %v\nwant %v", got, want)
	}
	byPath := map[string]SidecarScanMatch{}
	for _, matches := range PairSidecarFiles("M", files) {
		for _, m := range matches {
			byPath[m.File.Path] = m
		}
	}
	forced := byPath["M/movie.EN.forced.SRT"]
	if forced.Track.Kind != SidecarKindSubtitle || forced.Track.Format != "srt" || forced.Track.Language != "en" || !forced.Track.Forced || forced.File.Size == 0 {
		t.Fatalf("forced subtitle %+v", forced)
	}
	full := byPath["M/Movie.zh-Hans.Director.default.sdh.ass"].Track
	if full.Language != "zh-Hans" || full.Title != "Director" || !full.Default || !full.SDH || full.Forced {
		t.Fatalf("flags and title %+v", full)
	}
	bilingual := byPath["M/Subs/Movie.chs&eng.ass"].Track
	if !slices.Equal(bilingual.Languages, []string{"zh-Hans", "en"}) || bilingual.Subdir != "subs" {
		t.Fatalf("subdirectory track %+v", bilingual)
	}
	commentary := byPath["M/Audio/Movie.ja.commentary.flac"].Track
	if commentary.Kind != SidecarKindAudio || commentary.Language != "ja" || !commentary.Commentary || commentary.Subdir != "audio" {
		t.Fatalf("audio track %+v", commentary)
	}
}

func TestPairSidecarFilesRootAmbiguityAndCap(t *testing.T) {
	// Root directory, given as "" or ".".
	files := sidecarScanFiles("Film.mkv", "Film.en.srt", "Subs/Film.ja.srt", "Dir/Film.en.srt")
	for _, dir := range []string{"", "."} {
		got := pairedPaths(PairSidecarFiles(dir, files))
		if !slices.Equal(got["Film.mkv"], []string{"Film.en.srt", "Subs/Film.ja.srt"}) || len(got) != 1 {
			t.Fatalf("root %q: %v", dir, got)
		}
	}
	// Equal bases under case folding: the match is ambiguous and nothing is
	// paired with either video.
	if got := PairSidecarFiles("A", sidecarScanFiles("A/Show.mkv", "A/show.mp4", "A/Show.en.srt")); len(got) != 0 {
		t.Fatalf("ambiguous bases paired: %v", pairedPaths(got))
	}
	// No video: nothing to pair with.
	if got := PairSidecarFiles("B", sidecarScanFiles("B/Show.en.srt")); len(got) != 0 {
		t.Fatal("paired without a video")
	}
	// A directory full of look-alike names is capped per source, by path.
	paths := []string{"C/Big.mkv"}
	for i := 0; i < SidecarTracksPerSource+5; i++ {
		paths = append(paths, fmt.Sprintf("C/Big.t%03d.srt", i))
	}
	got := PairSidecarFiles("C", sidecarScanFiles(paths...))["C/Big.mkv"]
	if len(got) != SidecarTracksPerSource || got[0].File.Path != "C/Big.t000.srt" || got[len(got)-1].File.Path != fmt.Sprintf("C/Big.t%03d.srt", SidecarTracksPerSource-1) {
		t.Fatalf("cap kept %d tracks", len(got))
	}
}

func TestValidSidecarInspection(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	ok := SidecarInspection{ID: id, Size: 1, Fingerprint: make([]byte, 32), Charset: "Shift_JIS"}
	if !ValidSidecarInspection(ok) {
		t.Fatal("valid inspection rejected")
	}
	for name, bad := range map[string]SidecarInspection{
		"id":          {ID: "x", Fingerprint: make([]byte, 32)},
		"size":        {ID: id, Size: -1, Fingerprint: make([]byte, 32)},
		"fingerprint": {ID: id, Fingerprint: make([]byte, 31)},
		"missing":     {ID: id},
		"charset":     {ID: id, Fingerprint: make([]byte, 32), Charset: "utf-8; x=1"},
	} {
		if ValidSidecarInspection(bad) {
			t.Error("accepted bad inspection", name)
		}
	}
	if fmt.Sprint(SidecarInspectionTarget{RootPath: "/secret"}) != "sidecar inspection (redacted)" {
		t.Fatal("target not redacted")
	}
}
