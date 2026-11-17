package app

import (
	"fmt"
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
	// SeasonEnd is the last season of a cross-season file (S01E10-S02E01);
	// the item belongs to Season and EpisodeEnd numbers the episode in
	// SeasonEnd. Zero inside one season.
	SeasonEnd int
	// Absolute is the absolute episode number an absolute-numbered library
	// filed under season 1; AirDate (YYYY-MM-DD) the date of a daily episode.
	Absolute int
	AirDate  string
	// Special classifies Season 0 episodes and theatrical movies (G21.3):
	// season, sp, ova, oad, extra or theatrical; empty for regular items.
	Special string
}

// catalogParserOptions maps a library's naming options onto the parser.
func catalogParserOptions(naming domain.CatalogNamingOptions) medianame.Options {
	return medianame.Options{MaxPathBytes: domain.ScanPathMaxBytes, DateEpisodes: naming.DateEpisodes, AbsoluteEpisodes: naming.AbsoluteEpisodes}
}

// PlanCatalogScan applies the confidence gate: only High results with a
// registrable container create structure automatically (G14.4, G21.2). The
// library's naming options (G21.3) decide whether dates and absolute numbers
// count as episodes.
func PlanCatalogScan(relative string, naming domain.CatalogNamingOptions) CatalogScanPlan {
	parsed := medianame.ParsePath(relative, catalogParserOptions(naming))
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
	if parsed.Special != medianame.SpecialNone {
		plan.Special = parsed.Special.String()
	}
	switch parsed.Kind {
	case medianame.KindMovie:
		plan.Kind, plan.Title = "Movie", domain.ClampCatalogTitle(parsed.Title)
	case medianame.KindEpisode:
		if !planEpisodeNumbers(&plan, naming) {
			plan.Reason = "confidence"
			return plan
		}
		plan.Kind = "Episode"
		stem := strings.TrimSuffix(path.Base(relative), path.Ext(relative))
		plan.Title = domain.ClampCatalogTitle(stem)
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

// planEpisodeNumbers fills season and episode numbers. Besides explicit
// numbers it maps a daily episode to season=year, episode=month*100+day and,
// in an absolute-numbered library, an absolute number to season 1. A date
// inside a season folder of another year is ambiguous and stays pending.
func planEpisodeNumbers(plan *CatalogScanPlan, naming domain.CatalogNamingOptions) bool {
	p := plan.Parsed
	switch {
	case !p.Date.IsZero() && !p.HasEpisode:
		if p.HasSeason && p.Season != p.Date.Year {
			return false
		}
		plan.Season = p.Date.Year
		plan.Episode = p.Date.Month*100 + p.Date.Day
		plan.EpisodeEnd = plan.Episode
		plan.AirDate = fmt.Sprintf("%04d-%02d-%02d", p.Date.Year, p.Date.Month, p.Date.Day)
		return true
	case !p.HasSeason && p.HasEpisode && p.Absolute > 0 && naming.AbsoluteEpisodes:
		plan.Season, plan.Episode, plan.EpisodeEnd, plan.Absolute = 1, p.Episode, p.EpisodeEnd, p.Absolute
		return p.EpisodeEnd >= p.Episode
	case !p.HasSeason || !p.HasEpisode:
		return false
	case p.SeasonEnd > p.Season:
		plan.Season, plan.Episode, plan.EpisodeEnd, plan.SeasonEnd = p.Season, p.Episode, p.EpisodeEnd, p.SeasonEnd
		return true
	case p.EpisodeEnd < p.Episode:
		return false
	}
	plan.Season, plan.Episode, plan.EpisodeEnd = p.Season, p.Episode, p.EpisodeEnd
	return true
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

// PlanCatalogPendingAccept turns a pending file into a plan once an
// administrator accepted it. The decision completes or corrects the parsed
// values; the layout rules stay those of automatic plans. A container the
// catalog cannot register is a conflict, an incomplete identity is invalid.
func PlanCatalogPendingAccept(relative string, pending domain.CatalogPendingEntry, d domain.CatalogPendingDecision) (CatalogScanPlan, error) {
	if !domain.ValidCatalogPendingDecision(d) {
		return CatalogScanPlan{}, domain.ErrInvalid
	}
	base := PlanCatalogScan(relative, domain.CatalogNamingOptions{})
	if base.ContentType == "" {
		return CatalogScanPlan{}, domain.ErrConflict
	}
	plan := CatalogScanPlan{Parsed: base.Parsed, Auto: true, Directory: base.Directory, ContentType: base.ContentType, Year: pending.Year}
	if d.Year != nil {
		plan.Year = *d.Year
	}
	kind := d.Kind
	if kind == "" {
		kind = pending.Kind
	}
	pick := func(override *string, parsed string) string {
		if override != nil {
			return domain.ClampCatalogTitle(*override)
		}
		return domain.ClampCatalogTitle(parsed)
	}
	switch kind {
	case "movie":
		plan.Kind, plan.Title = "Movie", pick(d.Title, pending.Title)
		if plan.Title == "" || d.SeriesTitle != nil || d.Season != nil || d.Episode != nil || d.EpisodeEnd != nil {
			return CatalogScanPlan{}, domain.ErrInvalid
		}
		if pending.Special == medianame.SpecialTheatrical.String() {
			plan.Special = pending.Special
		}
		return plan, nil
	case "episode":
	default:
		return CatalogScanPlan{}, domain.ErrInvalid
	}
	season, episode, end := pending.Season, pending.Episode, pending.EpisodeEnd
	if d.Season != nil {
		season = d.Season
	}
	if d.Episode != nil {
		episode, end = d.Episode, d.Episode
	}
	if d.EpisodeEnd != nil {
		end = d.EpisodeEnd
	}
	if season == nil || episode == nil {
		return CatalogScanPlan{}, domain.ErrInvalid
	}
	if end == nil || *end < *episode {
		end = episode
	}
	plan.Kind = "Episode"
	plan.Season, plan.Episode, plan.EpisodeEnd = *season, *episode, *end
	// The parsed special kind stays only for the Season 0 it described.
	if *season == 0 && pending.Special != "" && pending.Special != medianame.SpecialNone.String() && pending.Special != medianame.SpecialTheatrical.String() {
		plan.Special = pending.Special
	}
	plan.SeriesTitle, plan.SeriesYear = pick(d.SeriesTitle, pending.Title), plan.Year
	plan.Title = pick(d.Title, strings.TrimSuffix(path.Base(relative), path.Ext(relative)))
	if plan.Title == "" || plan.SeriesTitle == "" {
		return CatalogScanPlan{}, domain.ErrInvalid
	}
	episodeLayout(&plan)
	return plan, nil
}
