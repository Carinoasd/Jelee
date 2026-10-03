package medianame

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParsePathRejectsUntrustedInput(t *testing.T) {
	cases := []struct {
		path string
		opts Options
		want Rejection
	}{
		{"", Options{}, RejectEmpty},
		{strings.Repeat("a", MaxPathBytes+1), Options{}, RejectTooLong},
		{strings.Repeat("a", 65), Options{MaxPathBytes: 64}, RejectTooLong},
		{strings.Repeat("a", MaxPathBytes+1), Options{MaxPathBytes: MaxPathBytes * 2}, RejectTooLong},
		{"Show\xff.mkv", Options{}, RejectInvalidUTF8},
		{"Show\x00.mkv", Options{}, RejectControl},
		{"Show\n.mkv", Options{}, RejectControl},
		{"Show\t.mkv", Options{}, RejectControl},
		{"Show\x7f.mkv", Options{}, RejectControl},
		{"Show\u0085.mkv", Options{}, RejectControl},
		{"Show\u202e.mkv", Options{}, RejectControl},
		{"Show\u2066.mkv", Options{}, RejectControl},
		{"\ufeffShow.mkv", Options{}, RejectControl},
		{"/Show/S01E01.mkv", Options{}, RejectNotRelative},
		{"Show//S01E01.mkv", Options{}, RejectBadSegment},
		{"Show/", Options{}, RejectBadSegment},
		{"./Show.mkv", Options{}, RejectBadSegment},
		{"Show/../S01E01.mkv", Options{}, RejectBadSegment},
		{"Show/ /S01E01.mkv", Options{}, RejectBadSegment},
		{strings.Repeat("a/", MaxSegments) + "x.mkv", Options{}, RejectTooDeep},
	}
	for _, tc := range cases {
		got := ParsePath(tc.path, tc.opts)
		if got.Rejected != tc.want {
			t.Errorf("ParsePath(%.40q) rejected=%v want %v", tc.path, got.Rejected, tc.want)
		}
		if tc.want != RejectNone && !reflect.DeepEqual(got, Parsed{Rejected: tc.want}) {
			t.Errorf("rejected result for %.40q carries fields: %+v", tc.path, got)
		}
	}
	edge := strings.Repeat("a", MaxPathBytes-len(" S01E01.mkv")) + " S01E01.mkv"
	if got := ParsePath(edge, Options{}); got.Rejected != RejectNone || got.Episode != 1 {
		t.Fatalf("path at the byte limit refused: %v", got.Rejected)
	}
	if got := ParsePath(strings.Repeat("a/", MaxSegments-1)+"x S01E01.mkv", Options{}); got.Rejected != RejectNone {
		t.Fatalf("maximum depth refused: %v", got.Rejected)
	}
}

func evidenceFor(p Parsed, f Field) []Evidence {
	var out []Evidence
	for _, e := range p.Evidence {
		if e.Field == f {
			out = append(out, e)
		}
	}
	return out
}

// G21.2: explicit naming beats directory structure; the losing directory
// evidence is kept and marked so reviewers can see the conflict.
func TestExplicitFilenameSeasonOverridesDirectory(t *testing.T) {
	p := ParsePath("Show (2010)/Season 2/Show S03E04.mkv", Options{})
	if p.Season != 3 || !p.Conflict || p.Confidence != ConfidenceMedium {
		t.Fatalf("filename season must win with reduced confidence: %s", describe(p))
	}
	ev := evidenceFor(p, FieldSeason)
	if len(ev) != 2 || ev[0].Source != SourceFilename || ev[0].Overridden || ev[0].Text != "S03E04" ||
		ev[1].Source != SourceDirectory || !ev[1].Overridden || ev[1].Text != "Season 2" {
		t.Fatalf("season evidence: %+v", ev)
	}
	if y := evidenceFor(p, FieldYear); len(y) != 1 || y[0].Source != SourceDirectory || p.Year != 2010 {
		t.Fatalf("year evidence: %+v", y)
	}

	// Directory structure beats heuristics: the folder season is used, and a
	// bare number becomes an in-season episode instead of an absolute one.
	p = ParsePath("Show/Season 2/Show - 07.mkv", Options{})
	if !p.HasSeason || p.Season != 2 || p.Episode != 7 || p.Absolute != 0 || p.Confidence != ConfidenceMedium {
		t.Fatalf("directory season with heuristic episode: %s", describe(p))
	}
	if ev := evidenceFor(p, FieldSeason); len(ev) != 1 || ev[0].Source != SourceDirectory || ev[0].Overridden {
		t.Fatalf("directory season evidence: %+v", ev)
	}
	if ev := evidenceFor(p, FieldEpisode); len(ev) != 1 || ev[0].Rule != "bare-dash" {
		t.Fatalf("heuristic episode evidence: %+v", ev)
	}

	// The same number without season context is an absolute episode.
	p = ParsePath("[Group] Title - 07 [1080p].mkv", Options{})
	if p.HasSeason || p.Absolute != 7 || p.Confidence != ConfidenceLow || len(evidenceFor(p, FieldAbsolute)) != 1 {
		t.Fatalf("absolute numbering: %s", describe(p))
	}
}

func TestDateEpisodesAreConfigurable(t *testing.T) {
	const path = "The Daily Show/The Daily Show 2024-01-31.mkv"
	on := ParsePath(path, Options{DateEpisodes: true})
	off := ParsePath(path, Options{})
	if on.Kind != KindEpisode || on.Date != (Date{2024, 1, 31}) || on.Year != 0 {
		t.Fatalf("enabled: %s", describe(on))
	}
	if off.Kind != KindUnknown || !off.Date.IsZero() || off.Year != 0 || off.Title != "The Daily Show" {
		t.Fatalf("disabled dates must neither parse nor leak a year: %s", describe(off))
	}
	// Directories never produce date episodes.
	if p := ParsePath("2024-01-31/Show.mkv", Options{DateEpisodes: true}); !p.Date.IsZero() {
		t.Fatalf("directory date leaked: %s", describe(p))
	}
}

func TestEvidenceTextIsBounded(t *testing.T) {
	long := strings.Repeat("長", 500)
	p := ParsePath(long+"/"+long+" S01E01.mkv", Options{})
	if p.Episode != 1 || len(p.Evidence) == 0 {
		t.Fatalf("long title: %s", describe(p))
	}
	for _, e := range p.Evidence {
		if utf8.RuneCountInString(e.Text) > maxEvidenceRunes {
			t.Fatalf("evidence text unbounded: %d runes", utf8.RuneCountInString(e.Text))
		}
	}
}

func TestStringers(t *testing.T) {
	if KindMovie.String() != "movie" || ConfidenceHigh.String() != "high" || SpecialOVA.String() != "ova" ||
		SourceDirectory.String() != "directory" || FieldAbsolute.String() != "absolute" || RejectTooDeep.String() != "too-deep" {
		t.Fatal("stringer drift")
	}
}

// Adversarial inputs at the size limit must stay linear: no matcher may rescan
// unbounded windows.
func TestAdversarialInputsStayLinear(t *testing.T) {
	inputs := []string{
		strings.Repeat("S01E01", 680),
		strings.Repeat("e1-", 1360),
		strings.Repeat("1x1", 1360),
		strings.Repeat("第", 1360),
		strings.Repeat("第一", 680),
		strings.Repeat("[", 4096),
		strings.Repeat("[a", 2048),
		strings.Repeat(" - 1", 1024),
		strings.Repeat("1", 4096),
		strings.Repeat("2024-", 819),
		strings.Repeat("シーズン", 341),
		strings.Repeat("part1 ", 682),
		strings.Repeat("s", 4096),
		strings.Repeat("part1 x ", 512),
		strings.Repeat("-", 3000) + strings.Repeat(" 2010", 200),
		strings.Repeat("_", 3000) + strings.Repeat(" 01", 300),
		strings.Repeat("[a] ", 1024),
		strings.Repeat("劇場版", 1365),
		strings.Repeat("S01E01-E", 512),
		strings.Repeat("EP1-", 1024),
		strings.Repeat("a/", MaxSegments-1) + strings.Repeat("S1E", 1300),
	}
	for _, in := range inputs {
		begin := time.Now()
		for i := 0; i < 20; i++ {
			ParsePath(in, Options{DateEpisodes: true})
		}
		if d := time.Since(begin); d > 2*time.Second {
			t.Fatalf("input %.20q took %v for 20 parses", in, d)
		}
	}
}

func FuzzParsePath(f *testing.F) {
	for _, tc := range namingCases {
		f.Add(tc.path, tc.dates)
	}
	f.Add("[[[[ - 1 - 2 - 3", false)
	f.Add("第第第一百百百集", true)
	f.Fuzz(func(t *testing.T, path string, dates bool) {
		opts := Options{DateEpisodes: dates}
		p := ParsePath(path, opts)
		if !reflect.DeepEqual(p, ParsePath(path, opts)) {
			t.Fatal("parser is not deterministic")
		}
		if p.Rejected != RejectNone {
			if !reflect.DeepEqual(p, Parsed{Rejected: p.Rejected}) {
				t.Fatal("rejected result carries fields")
			}
			return
		}
		if len(path) > MaxPathBytes || !utf8.ValidString(path) {
			t.Fatal("unsafe input accepted")
		}
		if !utf8.ValidString(p.Title) || len(p.Title) > len(path)*2 {
			t.Fatal("title escaped its input")
		}
		if len(p.Evidence) > 16 {
			t.Fatalf("evidence unbounded: %d", len(p.Evidence))
		}
		for _, e := range p.Evidence {
			if utf8.RuneCountInString(e.Text) > maxEvidenceRunes {
				t.Fatal("evidence text unbounded")
			}
		}
		if p.HasEpisode && (p.EpisodeEnd < p.Episode || p.Episode < 0 || p.EpisodeEnd > 9999) {
			t.Fatalf("bad episode range %d-%d", p.Episode, p.EpisodeEnd)
		}
		if !p.HasEpisode && (p.Episode != 0 || p.EpisodeEnd != 0 || p.Absolute != 0) {
			t.Fatal("episode fields without episode")
		}
		if p.HasSeason && (p.Season < 0 || p.Season > 9999) || p.Part < 0 || p.Part > 99 {
			t.Fatal("number out of range")
		}
		if p.Year != 0 && (p.Year < 1900 || p.Year > 2099) {
			t.Fatalf("bad year %d", p.Year)
		}
		if !p.Date.IsZero() && (!dates || !validDate(p.Date.Year, p.Date.Month, p.Date.Day)) {
			t.Fatal("bad date")
		}
		if p.Kind == KindUnknown && p.Confidence > ConfidenceLow || p.Conflict && p.Confidence > ConfidenceMedium {
			t.Fatal("confidence exceeds evidence")
		}
	})
}
