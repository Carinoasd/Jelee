package domain

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Metadata refresh (G13.6, G14.1, G14.4, G14.6): one metadata_refresh job
// walks the movies, home videos and series of one library in ascending item
// order, matches each against TMDB through the local external identifiers
// first and the title and year second, and either applies a match through the
// ordinary confirmed TMDB apply (field locks, manual values and trusted NFO
// keep priority there) or records it for administrator review. Every item is
// checkpointed on its own.

// JobMetadataRefresh is the job kind of a metadata refresh.
const JobMetadataRefresh = "metadata_refresh"

// MetadataRefreshBatchSize is how many items one read covers. Every item is
// checkpointed on its own, so this only bounds a read.
const MetadataRefreshBatchSize = 16

// MetadataRefreshMaxCandidates bounds the candidates stored with a review.
const MetadataRefreshMaxCandidates = 5

// MetadataRefreshMaxConsecutiveFailures ends a run after this many items in a
// row failed because the provider was unavailable, so an outage or an
// exhausted daily budget does not walk the rest of the library for nothing.
const MetadataRefreshMaxConsecutiveFailures = 10

// MetadataRefreshTitleSimilarity is the minimum title similarity, in percent,
// for a title match.
const MetadataRefreshTitleSimilarity = 90

// Match confidence. High comes from an exact identifier; medium from a unique
// title and year match; low from anything that needs a person to choose; none
// means the provider had no candidate.
const (
	MetadataConfidenceHigh   = "high"
	MetadataConfidenceMedium = "medium"
	MetadataConfidenceLow    = "low"
	MetadataConfidenceNone   = "none"
)

// Automatic apply thresholds: high applies only identifier matches (the
// default), medium also unique title and year matches, never sends every
// match to review.
const (
	MetadataAutoApplyHigh   = "high"
	MetadataAutoApplyMedium = "medium"
	MetadataAutoApplyNever  = "never"
)

// Match reasons stored with a review and reported by the job.
const (
	MetadataMatchNFOTMDB            = "nfo_tmdb_id"
	MetadataMatchNFOIMDb            = "nfo_imdb_id"
	MetadataMatchNFOTVDB            = "nfo_tvdb_id"
	MetadataMatchExistingProvider   = "existing_provider"
	MetadataMatchTitleYear          = "title_year_unique"
	MetadataMatchTitleYearAmbiguous = "title_year_ambiguous"
	MetadataMatchTitleOnly          = "title_without_year"
	MetadataMatchYearMismatch       = "year_mismatch"
	MetadataMatchLowSimilarity      = "low_similarity"
	MetadataMatchExternalAmbiguous  = "external_ambiguous"
	MetadataMatchExternalConflict   = "external_conflict"
	MetadataMatchNoCandidates       = "no_candidates"
)

var metadataMatchReasons = map[string]bool{MetadataMatchNFOTMDB: true, MetadataMatchNFOIMDb: true, MetadataMatchNFOTVDB: true,
	MetadataMatchExistingProvider: true, MetadataMatchTitleYear: true, MetadataMatchTitleYearAmbiguous: true, MetadataMatchTitleOnly: true,
	MetadataMatchYearMismatch: true, MetadataMatchLowSimilarity: true, MetadataMatchExternalAmbiguous: true, MetadataMatchExternalConflict: true,
	MetadataMatchNoCandidates: true}

// ValidMetadataMatchReason reports whether reason is a known match reason.
func ValidMetadataMatchReason(reason string) bool { return metadataMatchReasons[reason] }

// Metadata sources of a refresh. nfo is the local external identifiers
// (uniqueid entries read from NFO or entered by an administrator); every
// other name is a remote provider. TMDB is the only provider; the list is
// the framework a fallback provider registers in (G14.1), and validation
// refuses a name nothing implements.
const (
	MetadataSourceNFO  = "nfo"
	MetadataSourceTMDB = "tmdb"
)

// metadataRefreshProviders are the remote providers a refresh can query, in
// default priority order.
var metadataRefreshProviders = []string{MetadataSourceTMDB}

// MetadataRefreshProviders lists the remote providers a refresh can query.
func MetadataRefreshProviders() []string { return slices.Clone(metadataRefreshProviders) }

// DefaultMetadataRefreshSources is the source order of a library without
// settings: local identifiers, then TMDB.
func DefaultMetadataRefreshSources() []string {
	return []string{MetadataSourceNFO, MetadataSourceTMDB}
}

// ValidMetadataRefreshSources accepts a non-empty ordered list of distinct
// known sources naming at least one remote provider.
func ValidMetadataRefreshSources(sources []string) bool {
	if len(sources) < 1 || len(sources) > 1+len(metadataRefreshProviders) {
		return false
	}
	provider := false
	for i, source := range sources {
		if slices.Contains(sources[:i], source) {
			return false
		}
		switch {
		case source == MetadataSourceNFO:
		case slices.Contains(metadataRefreshProviders, source):
			provider = true
		default:
			return false
		}
	}
	return provider
}

// ValidMetadataAutoApply reports whether threshold is a known threshold.
func ValidMetadataAutoApply(threshold string) bool {
	return threshold == MetadataAutoApplyHigh || threshold == MetadataAutoApplyMedium || threshold == MetadataAutoApplyNever
}

// MetadataRefreshSettings is the per-library configuration. Auto queues a
// refresh after every successful catalog synchronisation; it is off by
// default. AutoApply is the lowest confidence applied without review.
type MetadataRefreshSettings struct {
	LibraryID string   `json:"libraryId"`
	Auto      bool     `json:"auto"`
	AutoApply string   `json:"autoApply"`
	Sources   []string `json:"sources"`
}

// DefaultMetadataRefreshSettings is the configuration of a library nobody
// configured: no automatic refresh, identifier matches only, NFO then TMDB.
func DefaultMetadataRefreshSettings(library string) MetadataRefreshSettings {
	return MetadataRefreshSettings{LibraryID: library, AutoApply: MetadataAutoApplyHigh, Sources: DefaultMetadataRefreshSources()}
}

// ValidMetadataRefreshSettings checks settings before they are stored.
func ValidMetadataRefreshSettings(s MetadataRefreshSettings) bool {
	return ValidID(s.LibraryID) && ValidMetadataAutoApply(s.AutoApply) && ValidMetadataRefreshSources(s.Sources)
}

// UsesSource reports whether the settings enable source.
func (s MetadataRefreshSettings) UsesSource(source string) bool {
	return slices.Contains(s.Sources, source)
}

// MetadataAutoApplies reports whether a match of confidence is applied
// without review under threshold.
func MetadataAutoApplies(threshold, confidence string) bool {
	switch threshold {
	case MetadataAutoApplyHigh:
		return confidence == MetadataConfidenceHigh
	case MetadataAutoApplyMedium:
		return confidence == MetadataConfidenceHigh || confidence == MetadataConfidenceMedium
	}
	return false
}

// Refresh triggers recorded with a job.
const (
	MetadataRefreshTriggerManual      = "manual"
	MetadataRefreshTriggerCatalogSync = "catalog_sync"
)

// MetadataRefreshCounts classifies the items a run finished with. Applied
// items changed at least one field; Unchanged were applied without a change
// (every field protected or equal); Review were recorded for review;
// Unmatched had no provider candidate; Skipped were not looked up (protected,
// rejected, locked kind, gone); Failed hit a provider or item-local error.
type MetadataRefreshCounts struct {
	Applied   int
	Unchanged int
	Review    int
	Unmatched int
	Skipped   int
	Failed    int
}

// MetadataRefreshJobState is the public progress of a metadata_refresh job.
// CursorItemID is null until the first checkpoint.
type MetadataRefreshJobState struct {
	JobID        string    `json:"jobId"`
	LibraryID    string    `json:"libraryId"`
	Trigger      string    `json:"trigger"`
	CursorItemID *string   `json:"cursorItemId"`
	Applied      int       `json:"applied"`
	Unchanged    int       `json:"unchanged"`
	Review       int       `json:"review"`
	Unmatched    int       `json:"unmatched"`
	Skipped      int       `json:"skipped"`
	Failed       int       `json:"failed"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Review states. A pending review waits for an administrator; a rejected one
// keeps automatic refreshes away from the item until the administrator
// applies a match by hand or accepts a later review.
const (
	MetadataReviewPending  = "pending"
	MetadataReviewRejected = "rejected"
)

// MetadataRefreshTarget is one item a run visits, with what storage knows
// about it beyond its metadata: the file-name title and year, whether its
// kind is locked by an identity correction and the state of its review.
type MetadataRefreshTarget struct {
	ItemID     string
	Kind       string
	ScanTitle  string
	ScanYear   int
	KindLocked bool
	Review     string
}

// MetadataRefreshBatch is one read of a run: the job's actor (the
// administrator who queued it, or whose job queued it, bound to a live
// session), the library's settings and the next targets.
type MetadataRefreshBatch struct {
	Actor    Actor
	Settings MetadataRefreshSettings
	Items    []MetadataRefreshTarget
}

// MetadataRefreshResource maps an item kind to the provider resource a
// refresh matches it with; other kinds are not refreshed.
func MetadataRefreshResource(kind string) string {
	switch kind {
	case "Movie", "HomeVideo":
		return "movie"
	case "Series":
		return "series"
	}
	return ""
}

// MetadataRefreshCandidate is a provider candidate kept with a review.
// Similarity is the title similarity in percent; identifier matches carry
// 100.
type MetadataRefreshCandidate struct {
	ProviderID    int32  `json:"providerId"`
	Title         string `json:"title"`
	OriginalTitle string `json:"originalTitle"`
	Date          string `json:"date"`
	Similarity    int    `json:"similarity"`
}

// ValidMetadataRefreshCandidate checks one candidate before it is stored.
func ValidMetadataRefreshCandidate(c MetadataRefreshCandidate) bool {
	return c.ProviderID > 0 && ValidItemMetadataValue("originalTitle", c.Title) && ValidItemMetadataValue("originalTitle", c.OriginalTitle) &&
		ValidItemMetadataValue("date", c.Date) && c.Similarity >= 0 && c.Similarity <= 100
}

// MetadataRefreshMatch is the outcome of matching one item. ProviderID is the
// chosen candidate of a high or medium match and zero otherwise.
type MetadataRefreshMatch struct {
	Resource   string
	Confidence string
	Reason     string
	ProviderID int32
	Candidates []MetadataRefreshCandidate
}

// MetadataRefreshSubject is what a refresh matches an item by.
type MetadataRefreshSubject struct {
	ItemID             string
	Resource           string
	Title              string
	Year               int
	TMDBID             int32
	IMDbID             string
	TVDBID             string
	ExistingProviderID int32
	Existing           MetadataRefreshCandidate
	Protected          bool
}

// metadataRefreshFields are the fields a provider apply writes.
var metadataRefreshFields = []string{"title", "originalTitle", "overview", "date"}

// NewMetadataRefreshSubject derives the match subject of target from its
// stored metadata. The search title is a curated title (NFO or manual) when
// there is one, otherwise the file-name title, otherwise the current title;
// the year likewise comes from the year fact, the file name or the date.
// External identifiers are read only when useIdentifiers is set. Protected
// is true when every provider field is locked or manual, so an apply could
// not change anything.
func NewMetadataRefreshSubject(target MetadataRefreshTarget, md ItemMetadata, useIdentifiers bool) MetadataRefreshSubject {
	s := MetadataRefreshSubject{ItemID: target.ItemID, Resource: MetadataRefreshResource(target.Kind)}
	fields := map[string]ItemMetadataField{}
	for _, f := range md.Fields {
		fields[f.Field] = f
	}
	title := fields["title"]
	switch {
	case strings.TrimSpace(title.Value) != "" && (title.Source == "nfo" || title.Source == "manual"):
		s.Title = title.Value
	case strings.TrimSpace(target.ScanTitle) != "":
		s.Title = target.ScanTitle
	default:
		s.Title = title.Value
	}
	s.Title = strings.TrimSpace(s.Title)
	for _, fact := range md.Facts {
		if fact.Field == "year" {
			var year int
			if json.Unmarshal(fact.Value, &year) == nil && year >= 1000 && year <= 9999 {
				s.Year = year
			}
		}
	}
	if s.Year == 0 && target.ScanYear >= 1000 && target.ScanYear <= 9999 {
		s.Year = target.ScanYear
	}
	if date := fields["date"].Value; s.Year == 0 && len(date) == 10 {
		if year, err := strconv.Atoi(date[:4]); err == nil && year >= 1000 {
			s.Year = year
		}
	}
	if useIdentifiers {
		for _, fact := range md.Facts {
			if fact.Field == "uniqueIds" {
				s.TMDBID, s.IMDbID, s.TVDBID = metadataRefreshIdentifiers(fact.Value)
			}
		}
	}
	for _, name := range metadataRefreshFields {
		f, ok := fields[name]
		// A provider origin survives a lock but not a manual edit, so it
		// names the provider ID an earlier apply chose.
		if !ok || f.ProviderOrigin == nil {
			continue
		}
		if f.ProviderOrigin.Resource == s.Resource && f.ProviderOrigin.ProviderID > 0 {
			s.ExistingProviderID = f.ProviderOrigin.ProviderID
			break
		}
	}
	if s.ExistingProviderID > 0 {
		s.Existing = MetadataRefreshCandidate{ProviderID: s.ExistingProviderID, Title: fields["title"].Value, OriginalTitle: fields["originalTitle"].Value, Date: fields["date"].Value, Similarity: 100}
		if !ValidMetadataRefreshCandidate(s.Existing) {
			s.Existing = MetadataRefreshCandidate{ProviderID: s.ExistingProviderID, Similarity: 100}
		}
	}
	s.Protected = true
	for _, name := range metadataRefreshFields {
		f, ok := fields[name]
		if !ok {
			s.Protected = false
			break
		}
		f.Field = name
		if reason := TMDBMetadataSkip(f, false); reason != "locked" && reason != "manual" {
			s.Protected = false
			break
		}
	}
	return s
}

// metadataRefreshIdentifiers reads the first valid TMDB, IMDb and TVDB
// identifiers of a uniqueIds value, preferring entries marked default.
func metadataRefreshIdentifiers(value json.RawMessage) (tmdb int32, imdb, tvdb string) {
	if !ValidMetadataUniqueIDValue(value) {
		return 0, "", ""
	}
	var ids []NFOUniqueID
	if json.Unmarshal(value, &ids) != nil {
		return 0, "", ""
	}
	sorted := make([]NFOUniqueID, 0, len(ids))
	for _, preferDefault := range []bool{true, false} {
		for _, id := range ids {
			if id.Default == preferDefault {
				sorted = append(sorted, id)
			}
		}
	}
	for _, id := range sorted {
		v := strings.TrimSpace(id.Value)
		switch strings.ToLower(strings.TrimSpace(id.Type)) {
		case "tmdb", "themoviedb":
			if tmdb == 0 && positiveInt32Decimal(v) {
				n, _ := strconv.ParseInt(v, 10, 32)
				tmdb = int32(n)
			}
		case "imdb":
			if imdb == "" && ValidExternalID(ExternalSourceIMDb, v) {
				imdb = v
			}
		case "tvdb":
			if tvdb == "" && ValidExternalID(ExternalSourceTVDB, v) {
				tvdb = v
			}
		}
	}
	return tmdb, imdb, tvdb
}

// MatchMetadataByIdentifier classifies the candidates one external
// identifier resolved to: exactly one is a high-confidence match with reason,
// several need review, none is no match.
func MatchMetadataByIdentifier(resource, reason string, candidates []MetadataRefreshCandidate) MetadataRefreshMatch {
	candidates = metadataRefreshCandidates(candidates)
	m := MetadataRefreshMatch{Resource: resource, Candidates: candidates}
	switch len(candidates) {
	case 0:
		m.Confidence, m.Reason = MetadataConfidenceNone, MetadataMatchNoCandidates
	case 1:
		m.Confidence, m.Reason, m.ProviderID = MetadataConfidenceHigh, reason, candidates[0].ProviderID
		m.Candidates[0].Similarity = 100
	default:
		m.Confidence, m.Reason = MetadataConfidenceLow, MetadataMatchExternalAmbiguous
	}
	return m
}

// MatchMetadataByTitle classifies title search candidates. A candidate is
// strong when its title or original title is at least
// MetadataRefreshTitleSimilarity percent similar and its year equals the
// item's. Exactly one strong candidate is a medium-confidence match; every
// other outcome needs review and says why. Candidates are kept best first.
func MatchMetadataByTitle(resource, title string, year int, candidates []MetadataRefreshCandidate) MetadataRefreshMatch {
	candidates = metadataRefreshCandidates(candidates)
	for i := range candidates {
		c := &candidates[i]
		c.Similarity = max(TitleSimilarity(title, c.Title), TitleSimilarity(title, c.OriginalTitle))
	}
	sameYear := func(c MetadataRefreshCandidate) bool { return year != 0 && candidateYear(c) == year }
	slices.SortStableFunc(candidates, func(a, b MetadataRefreshCandidate) int {
		if a.Similarity != b.Similarity {
			return b.Similarity - a.Similarity
		}
		if sameYear(a) != sameYear(b) {
			if sameYear(a) {
				return -1
			}
			return 1
		}
		return int(a.ProviderID) - int(b.ProviderID)
	})
	var strong, similar []MetadataRefreshCandidate
	for _, c := range candidates {
		if c.Similarity >= MetadataRefreshTitleSimilarity {
			similar = append(similar, c)
			if sameYear(c) {
				strong = append(strong, c)
			}
		}
	}
	m := MetadataRefreshMatch{Resource: resource, Candidates: candidates[:min(len(candidates), MetadataRefreshMaxCandidates)]}
	switch {
	case len(candidates) == 0:
		m.Confidence, m.Reason = MetadataConfidenceNone, MetadataMatchNoCandidates
	case len(strong) == 1:
		m.Confidence, m.Reason, m.ProviderID = MetadataConfidenceMedium, MetadataMatchTitleYear, strong[0].ProviderID
	case len(strong) > 1:
		m.Confidence, m.Reason = MetadataConfidenceLow, MetadataMatchTitleYearAmbiguous
	case len(similar) > 0 && year == 0:
		m.Confidence, m.Reason = MetadataConfidenceLow, MetadataMatchTitleOnly
	case len(similar) > 0:
		m.Confidence, m.Reason = MetadataConfidenceLow, MetadataMatchYearMismatch
	default:
		m.Confidence, m.Reason = MetadataConfidenceLow, MetadataMatchLowSimilarity
	}
	return m
}

// ReconcileMetadataMatch keeps an identifier match from silently replacing a
// different provider ID an earlier apply chose: such a match drops to low
// confidence with both candidates for review.
func ReconcileMetadataMatch(s MetadataRefreshSubject, m MetadataRefreshMatch) MetadataRefreshMatch {
	if s.ExistingProviderID <= 0 || m.ProviderID <= 0 || m.ProviderID == s.ExistingProviderID || m.Reason == MetadataMatchExistingProvider {
		return m
	}
	m.Confidence, m.Reason, m.ProviderID = MetadataConfidenceLow, MetadataMatchExternalConflict, 0
	m.Candidates = append(slices.Clone(m.Candidates[:min(len(m.Candidates), MetadataRefreshMaxCandidates-1)]), s.Existing)
	return m
}

// ExistingMetadataMatch is the high-confidence refresh of the provider ID an
// earlier apply chose.
func ExistingMetadataMatch(s MetadataRefreshSubject) MetadataRefreshMatch {
	return MetadataRefreshMatch{Resource: s.Resource, Confidence: MetadataConfidenceHigh, Reason: MetadataMatchExistingProvider, ProviderID: s.ExistingProviderID,
		Candidates: []MetadataRefreshCandidate{s.Existing}}
}

func candidateYear(c MetadataRefreshCandidate) int {
	if len(c.Date) < 4 {
		return 0
	}
	year, err := strconv.Atoi(c.Date[:4])
	if err != nil {
		return 0
	}
	return year
}

// metadataRefreshCandidates drops invalid candidates and repeated provider
// IDs, keeping order.
func metadataRefreshCandidates(in []MetadataRefreshCandidate) []MetadataRefreshCandidate {
	out := make([]MetadataRefreshCandidate, 0, len(in))
	for _, c := range in {
		c.Similarity = min(max(c.Similarity, 0), 100)
		if !ValidMetadataRefreshCandidate(c) || slices.ContainsFunc(out, func(o MetadataRefreshCandidate) bool { return o.ProviderID == c.ProviderID }) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// metadataTitleRunes bounds the title length similarity compares.
const metadataTitleRunes = 256

// NormalizeMetadataTitle folds case and width-insensitive punctuation away:
// letters and digits are kept lower-cased, every run of anything else becomes
// one space.
func NormalizeMetadataTitle(title string) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range title {
		if n >= metadataTitleRunes {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(unicode.ToLower(r))
			space = false
			n++
			continue
		}
		space = true
	}
	return b.String()
}

// TitleSimilarity is 100 minus the edit distance between the normalized
// titles in percent of the longer one; empty titles are 0 similar.
func TitleSimilarity(a, b string) int {
	x, y := []rune(NormalizeMetadataTitle(a)), []rune(NormalizeMetadataTitle(b))
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	previous := make([]int, len(y)+1)
	current := make([]int, len(y)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(x); i++ {
		current[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	longer := max(len(x), len(y))
	return (longer - previous[len(y)]) * 100 / longer
}

// Outcomes of one item in a run.
const (
	MetadataRefreshApplied   = "applied"
	MetadataRefreshUnchanged = "unchanged"
	MetadataRefreshReviewed  = "review"
	MetadataRefreshUnmatched = "unmatched"
	MetadataRefreshSkipped   = "skipped"
	MetadataRefreshFailed    = "failed"
)

// MetadataRefreshResult is the outcome of one item. Review is set only for
// the review outcome; an applied or unchanged item clears its pending review.
type MetadataRefreshResult struct {
	ItemID  string
	Outcome string
	Review  *MetadataRefreshReviewInput
}

// MetadataRefreshReviewInput is a match recorded for review.
type MetadataRefreshReviewInput struct {
	Resource   string
	Confidence string
	Reason     string
	QueryTitle string
	QueryYear  int
	Candidates []MetadataRefreshCandidate
}

// ValidMetadataRefreshResult checks one result before it reaches storage.
func ValidMetadataRefreshResult(r MetadataRefreshResult) bool {
	if !ValidID(r.ItemID) {
		return false
	}
	switch r.Outcome {
	case MetadataRefreshReviewed:
		return r.Review != nil && ValidMetadataRefreshReviewInput(*r.Review)
	case MetadataRefreshApplied, MetadataRefreshUnchanged, MetadataRefreshUnmatched, MetadataRefreshSkipped, MetadataRefreshFailed:
		return r.Review == nil
	}
	return false
}

// ValidMetadataRefreshReviewInput checks a review before it is stored.
func ValidMetadataRefreshReviewInput(v MetadataRefreshReviewInput) bool {
	if v.Resource != "movie" && v.Resource != "series" || (v.Confidence != MetadataConfidenceMedium && v.Confidence != MetadataConfidenceLow && v.Confidence != MetadataConfidenceHigh) ||
		!ValidMetadataMatchReason(v.Reason) || v.Reason == MetadataMatchNoCandidates || len(v.Candidates) < 1 || len(v.Candidates) > MetadataRefreshMaxCandidates ||
		!utf8.ValidString(v.QueryTitle) || len(v.QueryTitle) > 1024 || strings.ContainsRune(v.QueryTitle, 0) || v.QueryYear != 0 && (v.QueryYear < 1000 || v.QueryYear > 9999) {
		return false
	}
	for i, c := range v.Candidates {
		if !ValidMetadataRefreshCandidate(c) || slices.ContainsFunc(v.Candidates[:i], func(o MetadataRefreshCandidate) bool { return o.ProviderID == c.ProviderID }) {
			return false
		}
	}
	return true
}

// MetadataRefreshReview is a recorded match awaiting or refused by an
// administrator. Revision is the item's metadata revision an accept must
// name.
type MetadataRefreshReview struct {
	ItemID     string                     `json:"itemId"`
	LibraryID  string                     `json:"libraryId"`
	Kind       string                     `json:"kind"`
	Title      string                     `json:"title"`
	Revision   int64                      `json:"revision"`
	State      string                     `json:"state"`
	Resource   string                     `json:"resource"`
	Confidence string                     `json:"confidence"`
	Reason     string                     `json:"reason"`
	QueryTitle string                     `json:"queryTitle"`
	QueryYear  *int                       `json:"queryYear"`
	Candidates []MetadataRefreshCandidate `json:"candidates"`
	JobID      *string                    `json:"jobId"`
	UpdatedAt  time.Time                  `json:"updatedAt"`
}

// MetadataRefreshAcceptance chooses one candidate of a pending review.
type MetadataRefreshAcceptance struct {
	ProviderID       int32 `json:"providerId"`
	ExpectedRevision int64 `json:"expectedRevision"`
}
