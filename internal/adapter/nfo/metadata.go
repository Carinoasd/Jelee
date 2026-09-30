package nfo

import (
	"math"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

func knownField(name string) bool {
	switch name {
	case "title", "name", "localtitle", "originaltitle", "sorttitle", "sortname", "plot", "outline", "tagline", "year", "season", "seasonnumber", "seasonname", "episode", "displayseason", "displayepisode", "runtime", "premiered", "releasedate", "aired", "dateadded", "mpaa", "certification", "status", "showtitle", "set", "collection", "genre", "tag", "style", "studio", "country", "language", "director", "writer", "credits", "producer", "trailer", "actor", "uniqueid", "imdbid", "tmdbid", "tvdbid", "id", "thumb", "fanart", "art", "poster", "banner", "clearart", "clearlogo", "landscape", "rating", "communityrating", "userrating", "ratings", "lockdata", "lockedfields":
		return true
	}
	return false
}

func mapField(metadata *Metadata, node *element, entry int) []Issue {
	var issues []Issue
	issue := func(code string) {
		issues = append(issues, Issue{Severity: "error", Code: code, Field: node.name, Entry: entry})
	}
	integer := func(value string, minimum, maximum int) *int {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < minimum || parsed > maximum {
			issue("nfo_invalid_integer")
			return nil
		}
		return &parsed
	}
	number := func(value string, maximum float64) *float64 {
		parsed, err := strconv.ParseFloat(strings.ReplaceAll(value, ",", "."), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed > maximum {
			issue("nfo_invalid_number")
			return nil
		}
		return &parsed
	}
	boolean := func(value string) *bool {
		parsed, err := strconv.ParseBool(strings.ToLower(value))
		if err != nil {
			issue("nfo_invalid_boolean")
			return nil
		}
		return &parsed
	}
	value := node.value()
	switch node.name {
	case "title", "name", "localtitle", "seasonname":
		metadata.Title = value
	case "originaltitle":
		metadata.OriginalTitle = value
	case "sorttitle", "sortname":
		metadata.SortTitle = value
	case "plot":
		metadata.Plot = value
	case "outline":
		metadata.Outline = value
	case "tagline":
		metadata.Tagline = value
	case "year":
		metadata.Year = integer(value, 1, 9999)
	case "season", "seasonnumber":
		metadata.Season = integer(value, 0, 1000000)
	case "episode":
		metadata.Episode = integer(value, 0, 1000000)
	case "displayseason":
		metadata.DisplaySeason = integer(value, 0, 1000000)
	case "displayepisode":
		metadata.DisplayEpisode = integer(value, 0, 1000000)
	case "runtime":
		parts := strings.Fields(value)
		if len(parts) == 2 && (parts[1] == "min" || parts[1] == "minutes") {
			value = parts[0]
		}
		metadata.RuntimeMinutes = integer(value, 0, 10000000)
	case "premiered", "releasedate":
		metadata.Premiered = value
	case "aired":
		metadata.Aired = value
	case "dateadded":
		metadata.DateAdded = value
	case "mpaa":
		metadata.MPAA = value
	case "certification":
		metadata.Certification = value
	case "status":
		metadata.Status = value
	case "showtitle":
		metadata.ShowTitle = value
	case "collection", "set":
		if name := node.child("name"); name != nil {
			metadata.Collection = name.value()
		} else {
			metadata.Collection = value
		}
		metadata.CollectionOverview = node.childValue("overview")
	case "genre":
		metadata.Genres = append(metadata.Genres, splitValues(value, "/")...)
	case "tag", "style":
		metadata.Tags = append(metadata.Tags, splitValues(value, "/")...)
	case "studio":
		metadata.Studios = append(metadata.Studios, splitValues(value, "/")...)
	case "country":
		metadata.Countries = append(metadata.Countries, splitValues(value, "/")...)
	case "language":
		metadata.Languages = append(metadata.Languages, splitValues(value, "/")...)
	case "director":
		metadata.Directors = append(metadata.Directors, splitValues(value, "/")...)
	case "writer", "credits":
		metadata.Writers = append(metadata.Writers, splitValues(value, "/")...)
	case "producer":
		metadata.Producers = append(metadata.Producers, splitValues(value, "/")...)
	case "trailer":
		if value != "" {
			metadata.Trailers = append(metadata.Trailers, value)
		}
	case "actor":
		person := Person{Name: node.childValue("name"), Role: node.childValue("role"), Thumb: node.childValue("thumb")}
		if order := node.child("order"); order != nil {
			person.Order = integer(order.value(), 0, 1000000)
		}
		if person.Name == "" {
			issue("nfo_person_name_missing")
		}
		metadata.Actors = append(metadata.Actors, person)
	case "uniqueid":
		id := UniqueID{Type: strings.ToLower(node.attribute("type")), Value: value}
		if raw := node.attribute("default"); raw != "" {
			if result := boolean(raw); result != nil {
				id.Default = *result
			}
		}
		if id.Type == "" || id.Value == "" {
			issue("nfo_id_incomplete")
		}
		metadata.UniqueIDs = append(metadata.UniqueIDs, id)
	case "imdbid", "tmdbid", "tvdbid", "id":
		provider := strings.TrimSuffix(node.name, "id")
		if node.name == "id" {
			provider = "imdb"
		}
		if value == "" {
			issue("nfo_id_incomplete")
		}
		metadata.UniqueIDs = append(metadata.UniqueIDs, UniqueID{Type: provider, Value: value})
	case "thumb", "poster", "banner", "clearart", "clearlogo", "landscape":
		kind := node.attribute("aspect")
		if kind == "" {
			kind = node.attribute("type")
		}
		if kind == "" {
			kind = node.name
		}
		if kind == "thumb" {
			kind = "poster"
		}
		art := Artwork{Kind: kind, Location: value, Preview: node.attribute("preview")}
		if season := node.attribute("season"); season != "" {
			art.Season = integer(season, 0, 1000000)
		}
		metadata.Art = append(metadata.Art, art)
	case "fanart":
		if len(node.children) == 0 {
			metadata.Art = append(metadata.Art, Artwork{Kind: "fanart", Location: value})
		} else {
			for _, thumb := range node.children {
				if thumb.name == "thumb" {
					metadata.Art = append(metadata.Art, Artwork{Kind: "fanart", Location: thumb.value(), Preview: thumb.attribute("preview")})
				}
			}
		}
	case "art":
		for _, art := range node.children {
			switch art.name {
			case "poster", "fanart", "banner", "clearart", "clearlogo", "thumb", "landscape":
				metadata.Art = append(metadata.Art, Artwork{Kind: art.name, Location: art.value(), Preview: art.attribute("preview")})
			}
		}
	case "rating", "communityrating":
		metadata.Rating = number(value, 10)
	case "userrating":
		metadata.UserRating = number(value, 10)
	case "ratings":
		for _, item := range node.children {
			if item.name != "rating" {
				continue
			}
			rating := Rating{Name: item.attribute("name")}
			maximum := 10.0
			if raw := item.attribute("max"); raw != "" {
				rating.Max = number(raw, 1000000)
				if rating.Max != nil && *rating.Max > 0 {
					maximum = *rating.Max
				} else {
					issue("nfo_invalid_rating_scale")
				}
			}
			if raw := item.attribute("default"); raw != "" {
				if parsed := boolean(raw); parsed != nil {
					rating.Default = *parsed
				}
			}
			if child := item.child("value"); child != nil {
				rating.Value = number(child.value(), maximum)
			} else {
				issue("nfo_rating_value_missing")
			}
			if votes := item.child("votes"); votes != nil {
				rating.Votes = integer(votes.value(), 0, 2147483647)
			}
			metadata.Ratings = append(metadata.Ratings, rating)
		}
	case "lockdata":
		metadata.LockData = boolean(value)
	case "lockedfields":
		metadata.LockedFields = append(metadata.LockedFields, splitValues(value, "|")...)
	}
	return issues
}

func splitValues(value, delimiter string) []string {
	var values []string
	for _, part := range strings.Split(value, delimiter) {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	return values
}

func validateMetadata(metadata Metadata, entry int) []Issue {
	var issues []Issue
	warning := func(code, field string) {
		issues = append(issues, Issue{Severity: "warning", Code: code, Field: field, Entry: entry})
	}
	if metadata.Title == "" {
		warning("nfo_title_missing", "title")
	}
	if metadata.Root == "episode" || metadata.Root == "episodedetails" {
		if metadata.Season == nil {
			warning("nfo_season_missing", "season")
		}
		if metadata.Episode == nil {
			warning("nfo_episode_missing", "episode")
		}
	}
	for _, date := range []struct{ field, value string }{{"premiered", metadata.Premiered}, {"aired", metadata.Aired}, {"dateadded", metadata.DateAdded}} {
		if date.value != "" && !validDate(date.value) {
			issues = append(issues, Issue{Severity: "error", Code: "nfo_invalid_date", Field: date.field, Entry: entry})
		}
	}
	ids := make(map[string]string)
	for _, id := range metadata.UniqueIDs {
		if previous, exists := ids[id.Type]; exists && previous != id.Value {
			warning("nfo_conflicting_id", "uniqueid")
		}
		ids[id.Type] = id.Value
	}
	checkReference := func(reference, field string) {
		if reference != "" && !safeReference(reference) {
			issues = append(issues, Issue{Severity: "error", Code: "nfo_unsafe_reference", Field: field, Entry: entry})
		}
	}
	for _, art := range metadata.Art {
		checkReference(art.Location, "art")
		checkReference(art.Preview, "art.preview")
	}
	for _, person := range metadata.Actors {
		checkReference(person.Thumb, "actor.thumb")
	}
	return issues
}

func validDate(value string) bool {
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

// This is a syntax warning for stored references, not authorization to open or
// fetch them. Future fetchers still need root boundaries and SSRF controls.
func safeReference(value string) bool {
	if strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.Scheme != "" {
		return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil
	}
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return false
	}
	decoded = strings.ReplaceAll(decoded, "\\", "/")
	if path.IsAbs(decoded) || strings.Contains(decoded, ":") {
		return false
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
