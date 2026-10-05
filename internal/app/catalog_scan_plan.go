package app

import (
	"path"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/domain/medianame"
)

// CatalogScanPlan is the pure decision for one observed video path.
type CatalogScanPlan struct {
	Parsed      medianame.Parsed
	Auto        bool
	Reason      string // pending reason when Auto is false
	Kind        string // Movie or Episode when Auto
	Title       string // item title for the file's own item
	Year        int
	Directory   string
	ContentType string
	// Episode structure. Directories are empty when the layout does not
	// identify a dedicated folder; grouping then falls back to title and year.
	SeriesTitle     string
	SeriesYear      int
	SeriesDirectory string
	SeasonDirectory string
	Season          int
	Episode         int
	EpisodeEnd      int
}

// PlanCatalogScan applies the confidence gate: only High results with a
// registrable container create structure automatically (G14.4, G21.2).
func PlanCatalogScan(relative string) CatalogScanPlan {
	parsed := medianame.ParsePath(relative, medianame.Options{MaxPathBytes: domain.ScanPathMaxBytes})
	plan := CatalogScanPlan{Parsed: parsed, Directory: path.Dir(relative), ContentType: domain.ImportVideoContentType(relative), Year: parsed.Year}
	switch {
	case parsed.Rejected != medianame.RejectNone:
		plan.Reason = "rejected"
		return plan
	case plan.ContentType == "":
		plan.Reason = "unsupported"
		return plan
	case parsed.Confidence != medianame.ConfidenceHigh || parsed.Conflict:
		plan.Reason = "confidence"
		return plan
	}
	switch parsed.Kind {
	case medianame.KindMovie:
		plan.Kind, plan.Title = "Movie", domain.ClampCatalogTitle(parsed.Title)
	case medianame.KindEpisode:
		if !parsed.HasSeason || !parsed.HasEpisode || parsed.EpisodeEnd < parsed.Episode {
			plan.Reason = "confidence"
			return plan
		}
		plan.Kind = "Episode"
		stem := strings.TrimSuffix(path.Base(relative), path.Ext(relative))
		plan.Title = domain.ClampCatalogTitle(stem)
		plan.Season, plan.Episode, plan.EpisodeEnd = parsed.Season, parsed.Episode, parsed.EpisodeEnd
		plan.SeriesTitle, plan.SeriesYear = domain.ClampCatalogTitle(parsed.Title), parsed.Year
		episodeLayout(&plan)
	default:
		plan.Reason = "confidence"
		return plan
	}
	if plan.Title == "" || plan.Kind == "Episode" && plan.SeriesTitle == "" {
		plan.Reason = "confidence"
		return plan
	}
	plan.Auto = true
	return plan
}

// episodeLayout recognises Show/Season NN/file and Show/file. A folder is used
// only when its own name agrees with the parsed series title, so a shared
// folder holding several shows never becomes one series.
func episodeLayout(plan *CatalogScanPlan) {
	dir := plan.Directory
	series := dir
	if dir != "." && catalogSeasonDirectory(path.Base(dir)) {
		plan.SeasonDirectory, series = dir, path.Dir(dir)
	}
	if series == "." {
		plan.SeasonDirectory = ""
		return
	}
	title, year := catalogDirectoryTitle(path.Base(series))
	if title == "" || domain.NormalizeCatalogTitle(title) != domain.NormalizeCatalogTitle(plan.SeriesTitle) {
		plan.SeasonDirectory = ""
		return
	}
	plan.SeriesDirectory, plan.SeriesTitle = series, domain.ClampCatalogTitle(title)
	if year != 0 {
		plan.SeriesYear = year
	}
}

func catalogSeasonDirectory(name string) bool {
	parsed := medianame.ParsePath(name+"/x.mkv", medianame.Options{})
	for _, e := range parsed.Evidence {
		if e.Source == medianame.SourceDirectory && !e.Overridden && (e.Field == medianame.FieldSeason || e.Field == medianame.FieldSpecial && parsed.Special != medianame.SpecialTheatrical) {
			return true
		}
	}
	return false
}

func catalogDirectoryTitle(name string) (string, int) {
	parsed := medianame.ParsePath(name+"/S01E01.mkv", medianame.Options{})
	title, year := "", 0
	for _, e := range parsed.Evidence {
		if e.Source != medianame.SourceDirectory {
			continue
		}
		if e.Field == medianame.FieldTitle {
			title = parsed.Title
		}
		if e.Field == medianame.FieldYear {
			year = parsed.Year
		}
	}
	return title, year
}
