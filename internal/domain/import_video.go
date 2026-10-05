package domain

// ValidVideoItemKind excludes directory-backed series and seasons.
func ValidVideoItemKind(kind string) bool {
	return kind == "HomeVideo" || kind == "Movie" || kind == "Episode"
}
