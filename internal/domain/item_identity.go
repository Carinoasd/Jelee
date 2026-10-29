package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// G21.5: an administrator corrects how an item was identified and locks the
// correction against later scans.

const (
	// ItemIdentityRevisionMax is the last revision a correction row holds.
	ItemIdentityRevisionMax int64 = 2147483647
	// ItemIdentityNumberMax bounds season and episode numbers like the scan
	// structure does.
	ItemIdentityNumberMax = 100000
)

// Sources of a season or episode number, from the highest priority down.
const (
	ItemIdentitySourceManual = "manual"
	ItemIdentitySourceNFO    = "nfo"
	ItemIdentitySourceScan   = "scan"
	ItemIdentitySourceNone   = "none"
)

// ItemIdentityScan carries the values the file name produced. Scans keep
// refreshing them under a lock; they become effective again on unlock.
type ItemIdentityScan struct {
	Kind       string `json:"kind"`
	Season     *int   `json:"season,omitempty"`
	Episode    *int   `json:"episode,omitempty"`
	EpisodeEnd *int   `json:"episodeEnd,omitempty"`
	// SeasonEnd is the last season of a cross-season file; EpisodeEnd then
	// numbers the episode in that season (G21.3).
	SeasonEnd *int `json:"seasonEnd,omitempty"`
	// Absolute is the absolute number of an episode an absolute-numbered
	// library filed under season 1; AirDate (YYYY-MM-DD) the date of a daily
	// episode; Special the Season 0 or theatrical classification.
	Absolute *int   `json:"absolute,omitempty"`
	AirDate  string `json:"airDate,omitempty"`
	Special  string `json:"special,omitempty"`
}

// ItemIdentity is the effective identification of an item and its locks.
type ItemIdentity struct {
	ItemID        string            `json:"itemId"`
	LibraryID     string            `json:"libraryId"`
	Revision      int64             `json:"revision"`
	Kind          string            `json:"kind"`
	KindLocked    bool              `json:"kindLocked"`
	ParentID      string            `json:"parentId,omitempty"`
	ParentKind    string            `json:"parentKind,omitempty"`
	ParentLocked  bool              `json:"parentLocked"`
	Season        *int              `json:"season,omitempty"`
	SeasonSource  string            `json:"seasonSource"`
	SeasonLocked  bool              `json:"seasonLocked"`
	Episode       *int              `json:"episode,omitempty"`
	EpisodeEnd    *int              `json:"episodeEnd,omitempty"`
	EpisodeSource string            `json:"episodeSource"`
	EpisodeLocked bool              `json:"episodeLocked"`
	Scan          *ItemIdentityScan `json:"scan,omitempty"`
	UpdatedAt     *time.Time        `json:"updatedAt"`
}

// ItemIdentityPatch changes one field. A value is always locked: Locked may
// only be omitted or true with it. Without a value, Locked true locks the
// current effective value and Locked false returns the field to the scanner.
type ItemIdentityPatch struct {
	Field    string
	HasValue bool
	// Kind is the value of "kind"; Parent the value of "parent", empty for
	// none; Number and NumberEnd the value of "season" and "episode".
	Kind      string
	Parent    string
	Number    int
	NumberEnd int
	Locked    *bool
}

// ItemIdentityFields lists the correctable fields in the order a request
// applies them: a new kind decides which other fields apply.
func ItemIdentityFields() []string { return []string{"kind", "parent", "season", "episode"} }

// ItemIdentityLeafKind reports the kinds that hold media and no children.
// Only these may change kind.
func ItemIdentityLeafKind(kind string) bool {
	return kind == "Movie" || kind == "Episode" || kind == "HomeVideo"
}

// ItemIdentityFieldApplies reports whether an item of kind carries field.
func ItemIdentityFieldApplies(kind, field string) bool {
	switch field {
	case "kind":
		return true
	case "parent", "season":
		return kind == "Season" || kind == "Episode"
	case "episode":
		return kind == "Episode"
	}
	return false
}

// ItemIdentityParentAllowed is the nesting rule of item_parent_links.
func ItemIdentityParentAllowed(kind, parentKind string) bool {
	return kind == "Season" && parentKind == "Series" || kind == "Episode" && (parentKind == "Season" || parentKind == "Series")
}

func validItemIdentityPatch(p ItemIdentityPatch) bool {
	if !p.HasValue {
		return p.Locked != nil && p.Kind == "" && p.Parent == "" && p.Number == 0 && p.NumberEnd == 0
	}
	if p.Locked != nil && !*p.Locked {
		return false
	}
	switch p.Field {
	case "kind":
		return ItemIdentityLeafKind(p.Kind) && p.Parent == "" && p.Number == 0 && p.NumberEnd == 0
	case "parent":
		return (p.Parent == "" || ValidID(p.Parent)) && p.Kind == "" && p.Number == 0 && p.NumberEnd == 0
	case "season":
		return p.Number >= 0 && p.Number <= ItemIdentityNumberMax && p.NumberEnd == 0 && p.Kind == "" && p.Parent == ""
	case "episode":
		return p.Number >= 0 && p.NumberEnd >= p.Number && p.NumberEnd <= ItemIdentityNumberMax && p.Kind == "" && p.Parent == ""
	}
	return false
}

// ValidItemIdentityPatches checks one request: 1 to 4 distinct known fields
// against a revision the item can still advance from.
func ValidItemIdentityPatches(item string, revision int64, patches []ItemIdentityPatch) bool {
	if !ValidID(item) || revision < 1 || revision >= ItemIdentityRevisionMax || len(patches) < 1 || len(patches) > len(ItemIdentityFields()) {
		return false
	}
	seen := map[string]bool{}
	for _, p := range patches {
		if seen[p.Field] || !validItemIdentityPatch(p) {
			return false
		}
		seen[p.Field] = true
	}
	return true
}

// OrderItemIdentityPatches returns the patches in application order.
func OrderItemIdentityPatches(patches []ItemIdentityPatch) []ItemIdentityPatch {
	ordered := make([]ItemIdentityPatch, 0, len(patches))
	for _, field := range ItemIdentityFields() {
		for _, p := range patches {
			if p.Field == field {
				ordered = append(ordered, p)
			}
		}
	}
	return ordered
}

func cloneInt(v *int) *int {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// CloneItemIdentity copies every pointer so callers cannot share state.
func CloneItemIdentity(v ItemIdentity) ItemIdentity {
	v.Season, v.Episode, v.EpisodeEnd = cloneInt(v.Season), cloneInt(v.Episode), cloneInt(v.EpisodeEnd)
	if v.Scan != nil {
		s := *v.Scan
		s.Season, s.Episode, s.EpisodeEnd = cloneInt(s.Season), cloneInt(s.Episode), cloneInt(s.EpisodeEnd)
		s.SeasonEnd, s.Absolute = cloneInt(s.SeasonEnd), cloneInt(s.Absolute)
		v.Scan = &s
	}
	if v.UpdatedAt != nil {
		t := *v.UpdatedAt
		v.UpdatedAt = &t
	}
	return v
}

// Pending decisions. An entry is "pending" until an administrator rejects
// it; an accepted entry becomes an item and leaves the list.
const (
	CatalogPendingOpen     = "pending"
	CatalogPendingRejected = "rejected"
)

// CatalogPendingDecision completes or corrects the parse of a pending file
// when an administrator accepts it. Omitted values keep the parsed ones.
type CatalogPendingDecision struct {
	Kind        string  `json:"kind,omitempty"`
	Title       *string `json:"title,omitempty"`
	SeriesTitle *string `json:"seriesTitle,omitempty"`
	Year        *int    `json:"year,omitempty"`
	Season      *int    `json:"season,omitempty"`
	Episode     *int    `json:"episode,omitempty"`
	EpisodeEnd  *int    `json:"episodeEnd,omitempty"`
}

// CatalogPendingAcceptance names the item and source an accepted file became.
type CatalogPendingAcceptance struct {
	EntryID  string `json:"entryId"`
	ItemID   string `json:"itemId"`
	SourceID string `json:"sourceId"`
	Kind     string `json:"kind"`
	Created  bool   `json:"created"`
}

func validDecisionTitle(v *string) bool {
	return v == nil || utf8.ValidString(*v) && !strings.ContainsRune(*v, 0) && len(*v) <= 1024 && strings.TrimSpace(*v) != ""
}

func validDecisionNumber(v *int) bool {
	return v == nil || *v >= 0 && *v <= ItemIdentityNumberMax
}

// ValidCatalogPendingDecision checks the shape of a decision; whether it is
// complete depends on the parsed values it completes.
func ValidCatalogPendingDecision(d CatalogPendingDecision) bool {
	if d.Kind != "" && d.Kind != "movie" && d.Kind != "episode" {
		return false
	}
	if !validDecisionTitle(d.Title) || !validDecisionTitle(d.SeriesTitle) || !validDecisionNumber(d.Season) || !validDecisionNumber(d.Episode) || !validDecisionNumber(d.EpisodeEnd) {
		return false
	}
	if d.Year != nil && (*d.Year < 1 || *d.Year > 9999) {
		return false
	}
	if d.Episode != nil && d.EpisodeEnd != nil && *d.EpisodeEnd < *d.Episode {
		return false
	}
	if d.Kind == "movie" && (d.SeriesTitle != nil || d.Season != nil || d.Episode != nil || d.EpisodeEnd != nil) {
		return false
	}
	return true
}
