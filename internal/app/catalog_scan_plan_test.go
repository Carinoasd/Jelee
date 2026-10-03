package app

import "testing"

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
		p := PlanCatalogScan(c.path)
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
