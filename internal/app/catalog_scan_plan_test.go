package app

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestPlanCatalogScanConfidenceGateAndLayout(t *testing.T) {
	cases := []struct {
		path, kind, reason, series, seriesDir, seasonDir string
		season, episode, end                             int
	}{
		{path: "Inception (2010)/Inception (2010).mkv", kind: "Movie"},
		{path: "Heat (1995).mp4", kind: "Movie"},
		{path: "Breaking Bad (2008)/Season 01/Breaking Bad S01E02.mkv", kind: "Episode", series: "Breaking Bad", seriesDir: "Breaking Bad (2008)", seasonDir: "Breaking Bad (2008)/Season 01", season: 1, episode: 2, end: 2},
		{path: "Breaking Bad (2008)/Season 01/Breaking Bad S01E03-E04.mkv", kind: "Episode", series: "Breaking Bad", seriesDir: "Breaking Bad (2008)", seasonDir: "Breaking Bad (2008)/Season 01", season: 1, episode: 3, end: 4},
		{path: "Breaking Bad (2008)/Specials/Breaking Bad S00E01.mkv", kind: "Episode", series: "Breaking Bad", seriesDir: "Breaking Bad (2008)", seasonDir: "Breaking Bad (2008)/Specials", season: 0, episode: 1, end: 1},
		{path: "Show/Show 1x05.mkv", kind: "Episode", series: "Show", seriesDir: "Show", season: 1, episode: 5, end: 5},
		{path: "TV/Other Show S01E01.mkv", kind: "Episode", series: "Other Show", season: 1, episode: 1, end: 1},
		{path: "Naruto/Naruto - 012.mkv", reason: "confidence"},
		{path: "Show/Show EP05.mkv", reason: "confidence"},
		{path: "Movie.2019.1080p.mkv", reason: "confidence"},
		{path: "random clip.mkv", reason: "confidence"},
		{path: "Old (1999).wmv", reason: "unsupported"},
	}
	for _, c := range cases {
		p := PlanCatalogScan(c.path, domain.CatalogNamingOptions{})
		if c.reason != "" {
			if p.Auto || p.Reason != c.reason {
				t.Errorf("%s: auto=%t reason=%q want %q", c.path, p.Auto, p.Reason, c.reason)
			}
			continue
		}
		if !p.Auto || p.Kind != c.kind || p.ContentType == "" {
			t.Errorf("%s: %+v", c.path, p)
			continue
		}
		if c.kind == "Episode" && (p.SeriesTitle != c.series || p.SeriesDirectory != c.seriesDir || p.SeasonDirectory != c.seasonDir || p.Season != c.season || p.Episode != c.episode || p.EpisodeEnd != c.end) {
			t.Errorf("%s: series=%q dir=%q season=%q %d %d-%d", c.path, p.SeriesTitle, p.SeriesDirectory, p.SeasonDirectory, p.Season, p.Episode, p.EpisodeEnd)
		}
	}
}

// G21.3: library naming options, cross-season files and special
// classification as the catalog files them.
func TestPlanCatalogScanNamingOptions(t *testing.T) {
	dates := domain.CatalogNamingOptions{DateEpisodes: true}
	absolute := domain.CatalogNamingOptions{AbsoluteEpisodes: true}
	cases := []struct {
		path                          string
		naming                        domain.CatalogNamingOptions
		reason, kind                  string
		season, episode, end, seasonE int
		absolute                      int
		airDate, special, seasonDir   string
	}{
		{path: "新闻联播/新闻联播 2024-03-15.mp4", naming: dates, kind: "Episode", season: 2024, episode: 315, end: 315, airDate: "2024-03-15"},
		{path: "The Daily Show/Season 2024/The Daily Show 2024.01.31.mkv", naming: dates, kind: "Episode", season: 2024, episode: 131, end: 131, airDate: "2024-01-31", seasonDir: "The Daily Show/Season 2024"},
		{path: "徹子の部屋/徹子の部屋 2023年12月31日.mkv", naming: dates, kind: "Episode", season: 2023, episode: 1231, end: 1231, airDate: "2023-12-31"},
		{path: "新闻联播/新闻联播 2024-03-15.mp4", reason: "confidence"},
		{path: "Show/Season 1/Show 2024-03-15.mkv", naming: dates, reason: "confidence"},
		{path: "[SubsPlease] Frieren - 05 (1080p).mkv", naming: absolute, kind: "Episode", season: 1, episode: 5, end: 5, absolute: 5},
		{path: "海贼王/海贼王 第1071集.mp4", naming: absolute, kind: "Episode", season: 1, episode: 1071, end: 1071, absolute: 1071},
		{path: "鬼滅の刃/鬼滅の刃 第1話.mkv", naming: absolute, kind: "Episode", season: 1, episode: 1, end: 1, absolute: 1},
		{path: "[SubsPlease] Frieren - 05 (1080p).mkv", reason: "confidence"},
		{path: "[Group] Title 05 [1080p].mkv", naming: absolute, reason: "confidence"},
		{path: "Show/Season 2/Show - 05.mkv", naming: absolute, reason: "confidence"},
		{path: "Show/Show.S01E10-S02E01.mkv", kind: "Episode", season: 1, episode: 10, end: 1, seasonE: 2},
		{path: "进击的巨人/进击的巨人 S03E22-S04E01.mkv", naming: absolute, kind: "Episode", season: 3, episode: 22, end: 1, seasonE: 4},
		{path: "Show/Season 2/Show S01E10-S02E01.mkv", reason: "confidence"},
		{path: "Title/Title OVA 2.mkv", kind: "Episode", season: 0, episode: 2, end: 2, special: "ova"},
		{path: "[Group] Title S01 OVA 02 [1080p].mkv", kind: "Episode", season: 0, episode: 2, end: 2, special: "ova"},
		{path: "Title/Specials/Title S00E03.mkv", kind: "Episode", season: 0, episode: 3, end: 3, special: "season", seasonDir: "Title/Specials"},
		{path: "进击的巨人/进击的巨人 番外篇1.mp4", kind: "Episode", season: 0, episode: 1, end: 1, special: "extra"},
		{path: "Title/Title OVA.mkv", reason: "confidence"},
		{path: "劇場版 名探偵コナン 黒鉄の魚影 (2023).mkv", reason: "confidence"},
	}
	for _, c := range cases {
		p := PlanCatalogScan(c.path, c.naming)
		if c.reason != "" {
			if p.Auto || p.Reason != c.reason {
				t.Errorf("%s %+v: auto=%t reason=%q want %q", c.path, c.naming, p.Auto, p.Reason, c.reason)
			}
			continue
		}
		if !p.Auto || p.Kind != c.kind || p.Season != c.season || p.Episode != c.episode || p.EpisodeEnd != c.end || p.SeasonEnd != c.seasonE ||
			p.Absolute != c.absolute || p.AirDate != c.airDate || p.Special != c.special || p.SeasonDirectory != c.seasonDir {
			t.Errorf("%s %+v: %+v", c.path, c.naming, p)
		}
	}
}

// A confident theatrical release is a standalone Movie marked theatrical,
// even inside a series folder; it never becomes an episode.
func TestPlanCatalogScanTheatricalMovie(t *testing.T) {
	p := PlanCatalogScan("名探偵コナン/劇場版/名探偵コナン (2023).mkv", domain.CatalogNamingOptions{})
	if p.Auto || p.Parsed.Special.String() != "theatrical" {
		t.Fatalf("medium theatrical confidence must stay pending: %+v", p)
	}
	pending := domain.CatalogPendingEntry{Kind: "movie", Title: "名探偵コナン", Year: 2023, Special: "theatrical"}
	plan, err := PlanCatalogPendingAccept("名探偵コナン/劇場版/名探偵コナン (2023).mkv", pending, domain.CatalogPendingDecision{})
	if err != nil || plan.Kind != "Movie" || plan.Special != "theatrical" || plan.Year != 2023 {
		t.Fatalf("accepted theatrical: %+v %v", plan, err)
	}
	ova := domain.CatalogPendingEntry{Kind: "episode", Title: "Title", Special: "ova"}
	zero, one := 0, 1
	plan, err = PlanCatalogPendingAccept("Title/Title OVA.mkv", ova, domain.CatalogPendingDecision{Season: &zero, Episode: &one})
	if err != nil || plan.Special != "ova" || plan.Season != 0 {
		t.Fatalf("accepted OVA: %+v %v", plan, err)
	}
	// Moved out of Season 0 the special classification no longer applies.
	plan, err = PlanCatalogPendingAccept("Title/Title OVA.mkv", ova, domain.CatalogPendingDecision{Season: &one, Episode: &one})
	if err != nil || plan.Special != "" {
		t.Fatalf("OVA accepted into season 1: %+v %v", plan, err)
	}
}
