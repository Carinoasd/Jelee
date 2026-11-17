package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func refreshCandidate(id int32, title, date string) MetadataRefreshCandidate {
	return MetadataRefreshCandidate{ProviderID: id, Title: title, Date: date}
}

func TestMatchMetadataByTitleConfidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		title      string
		year       int
		candidates []MetadataRefreshCandidate
		confidence string
		reason     string
		provider   int32
	}{
		{"no candidates", "Heat", 1995, nil, MetadataConfidenceNone, MetadataMatchNoCandidates, 0},
		{"unique title and year", "Heat", 1995, []MetadataRefreshCandidate{refreshCandidate(949, "Heat", "1995-12-15"), refreshCandidate(1, "Heat", "1986-03-14")}, MetadataConfidenceMedium, MetadataMatchTitleYear, 949},
		{"punctuation and case", "the dark knight", 2008, []MetadataRefreshCandidate{refreshCandidate(155, "The Dark Knight", "2008-07-16")}, MetadataConfidenceMedium, MetadataMatchTitleYear, 155},
		{"original title", "千と千尋の神隠し", 2001, []MetadataRefreshCandidate{{ProviderID: 129, Title: "Spirited Away", OriginalTitle: "千と千尋の神隠し", Date: "2001-07-20"}}, MetadataConfidenceMedium, MetadataMatchTitleYear, 129},
		{"two strong candidates", "Heat", 1995, []MetadataRefreshCandidate{refreshCandidate(949, "Heat", "1995-12-15"), refreshCandidate(950, "Heat!", "1995-01-01")}, MetadataConfidenceLow, MetadataMatchTitleYearAmbiguous, 0},
		{"year differs", "Heat", 2001, []MetadataRefreshCandidate{refreshCandidate(949, "Heat", "1995-12-15")}, MetadataConfidenceLow, MetadataMatchYearMismatch, 0},
		{"no year", "Heat", 0, []MetadataRefreshCandidate{refreshCandidate(949, "Heat", "1995-12-15")}, MetadataConfidenceLow, MetadataMatchTitleOnly, 0},
		{"dissimilar title", "Heat", 1995, []MetadataRefreshCandidate{refreshCandidate(5, "Heathers", "1995-01-01")}, MetadataConfidenceLow, MetadataMatchLowSimilarity, 0},
		{"missing date", "Heat", 1995, []MetadataRefreshCandidate{refreshCandidate(949, "Heat", "")}, MetadataConfidenceLow, MetadataMatchYearMismatch, 0},
		{"invalid candidates dropped", "Heat", 1995, []MetadataRefreshCandidate{refreshCandidate(0, "Heat", "1995-12-15"), refreshCandidate(949, "Heat", "1995-13-40"), refreshCandidate(7, "Heat", "1995-12-15")}, MetadataConfidenceMedium, MetadataMatchTitleYear, 7},
		{"duplicate provider kept once", "Heat", 1995, []MetadataRefreshCandidate{refreshCandidate(949, "Heat", "1995-12-15"), refreshCandidate(949, "Heat", "1995-12-15")}, MetadataConfidenceMedium, MetadataMatchTitleYear, 949},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := MatchMetadataByTitle("movie", tc.title, tc.year, tc.candidates)
			if m.Confidence != tc.confidence || m.Reason != tc.reason || m.ProviderID != tc.provider || m.Resource != "movie" {
				t.Fatalf("got %s/%s/%d, want %s/%s/%d", m.Confidence, m.Reason, m.ProviderID, tc.confidence, tc.reason, tc.provider)
			}
			if len(m.Candidates) > MetadataRefreshMaxCandidates {
				t.Fatal("too many candidates", len(m.Candidates))
			}
			for i := 1; i < len(m.Candidates); i++ {
				if m.Candidates[i].Similarity > m.Candidates[i-1].Similarity {
					t.Fatal("candidates not best first", m.Candidates)
				}
			}
		})
	}
}

func TestMatchMetadataByTitleKeepsBestFive(t *testing.T) {
	var candidates []MetadataRefreshCandidate
	for i := range 8 {
		candidates = append(candidates, refreshCandidate(int32(i+1), strings.Repeat("x", i+1), "2000-01-01"))
	}
	candidates = append(candidates, refreshCandidate(99, "Alien", "1979-05-25"))
	m := MatchMetadataByTitle("movie", "Alien", 1979, candidates)
	if len(m.Candidates) != MetadataRefreshMaxCandidates || m.Candidates[0].ProviderID != 99 || m.Candidates[0].Similarity != 100 || m.ProviderID != 99 {
		t.Fatal("best candidate not first", m)
	}
}

func TestMatchMetadataByIdentifier(t *testing.T) {
	one := MatchMetadataByIdentifier("series", MetadataMatchNFOTVDB, []MetadataRefreshCandidate{refreshCandidate(1399, "Game of Thrones", "2011-04-17")})
	if one.Confidence != MetadataConfidenceHigh || one.Reason != MetadataMatchNFOTVDB || one.ProviderID != 1399 || one.Candidates[0].Similarity != 100 {
		t.Fatal("single identifier match", one)
	}
	many := MatchMetadataByIdentifier("series", MetadataMatchNFOIMDb, []MetadataRefreshCandidate{refreshCandidate(1, "A", ""), refreshCandidate(2, "B", "")})
	if many.Confidence != MetadataConfidenceLow || many.Reason != MetadataMatchExternalAmbiguous || many.ProviderID != 0 {
		t.Fatal("ambiguous identifier", many)
	}
	none := MatchMetadataByIdentifier("movie", MetadataMatchNFOIMDb, []MetadataRefreshCandidate{refreshCandidate(-1, "A", "")})
	if none.Confidence != MetadataConfidenceNone || none.Reason != MetadataMatchNoCandidates {
		t.Fatal("no identifier match", none)
	}
}

func TestReconcileMetadataMatch(t *testing.T) {
	s := MetadataRefreshSubject{Resource: "movie", ExistingProviderID: 10, Existing: refreshCandidate(10, "Old", "2000-01-01")}
	same := ReconcileMetadataMatch(s, MatchMetadataByIdentifier("movie", MetadataMatchNFOTMDB, []MetadataRefreshCandidate{refreshCandidate(10, "Old", "")}))
	if same.Confidence != MetadataConfidenceHigh || same.ProviderID != 10 {
		t.Fatal("same provider demoted", same)
	}
	other := ReconcileMetadataMatch(s, MatchMetadataByIdentifier("movie", MetadataMatchNFOTMDB, []MetadataRefreshCandidate{refreshCandidate(11, "New", "")}))
	if other.Confidence != MetadataConfidenceLow || other.Reason != MetadataMatchExternalConflict || other.ProviderID != 0 || len(other.Candidates) != 2 || other.Candidates[1].ProviderID != 10 {
		t.Fatal("conflicting provider not sent to review", other)
	}
	fresh := ReconcileMetadataMatch(MetadataRefreshSubject{Resource: "movie"}, MatchMetadataByIdentifier("movie", MetadataMatchNFOTMDB, []MetadataRefreshCandidate{refreshCandidate(11, "New", "")}))
	if fresh.Confidence != MetadataConfidenceHigh {
		t.Fatal("unmatched item demoted", fresh)
	}
	existing := ExistingMetadataMatch(s)
	if existing.Confidence != MetadataConfidenceHigh || existing.Reason != MetadataMatchExistingProvider || existing.ProviderID != 10 {
		t.Fatal("existing provider", existing)
	}
}

func TestMetadataAutoApplies(t *testing.T) {
	for _, tc := range []struct {
		threshold, confidence string
		want                  bool
	}{
		{MetadataAutoApplyHigh, MetadataConfidenceHigh, true},
		{MetadataAutoApplyHigh, MetadataConfidenceMedium, false},
		{MetadataAutoApplyHigh, MetadataConfidenceLow, false},
		{MetadataAutoApplyMedium, MetadataConfidenceHigh, true},
		{MetadataAutoApplyMedium, MetadataConfidenceMedium, true},
		{MetadataAutoApplyMedium, MetadataConfidenceLow, false},
		{MetadataAutoApplyMedium, MetadataConfidenceNone, false},
		{MetadataAutoApplyNever, MetadataConfidenceHigh, false},
		{"bogus", MetadataConfidenceHigh, false},
	} {
		if got := MetadataAutoApplies(tc.threshold, tc.confidence); got != tc.want {
			t.Errorf("%s/%s = %t", tc.threshold, tc.confidence, got)
		}
	}
}

func TestMetadataRefreshSettingsValidation(t *testing.T) {
	library := "11111111-1111-4111-8111-111111111111"
	if !ValidMetadataRefreshSettings(DefaultMetadataRefreshSettings(library)) {
		t.Fatal("default settings invalid")
	}
	for _, sources := range [][]string{{"tmdb"}, {"tmdb", "nfo"}, {"nfo", "tmdb"}} {
		if !ValidMetadataRefreshSources(sources) {
			t.Error("valid sources rejected", sources)
		}
	}
	for _, sources := range [][]string{nil, {}, {"nfo"}, {"tmdb", "tmdb"}, {"nfo", "tmdb", "omdb"}, {"omdb"}, {"TMDB"}} {
		if ValidMetadataRefreshSources(sources) {
			t.Error("invalid sources accepted", sources)
		}
	}
	bad := DefaultMetadataRefreshSettings(library)
	bad.AutoApply = "low"
	if ValidMetadataRefreshSettings(bad) {
		t.Fatal("unknown threshold accepted")
	}
	if got := MetadataRefreshProviders(); len(got) != 1 || got[0] != MetadataSourceTMDB {
		t.Fatal("providers", got)
	}
}

func TestTitleSimilarity(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		min  int
		max  int
	}{
		{"Heat", "heat", 100, 100},
		{"Spider-Man: No Way Home", "spider man no way home", 100, 100},
		{"Heat", "Heathers", 0, 60},
		{"", "Heat", 0, 0},
		{"Amélie", "Amelie", 80, 95},
		{"攻殻機動隊", "攻殻機動隊", 100, 100},
	} {
		if got := TitleSimilarity(tc.a, tc.b); got < tc.min || got > tc.max {
			t.Errorf("%q/%q = %d", tc.a, tc.b, got)
		}
	}
	long := strings.Repeat("a", 5000)
	if NormalizeMetadataTitle(long) != strings.Repeat("a", metadataTitleRunes) {
		t.Fatal("title not bounded")
	}
}

func TestNewMetadataRefreshSubject(t *testing.T) {
	item := "22222222-2222-4222-8222-222222222222"
	ids := json.RawMessage(`[{"type":"imdb","value":"tt0113277"},{"type":"tmdb","value":"949","default":true},{"type":"tvdb","value":"x"},{"type":"tmdb","value":"5"}]`)
	md := ItemMetadata{ItemID: item, Kind: "Movie", Fields: []ItemMetadataField{{Field: "title", Value: "Heat (1995)", Source: "scan"}},
		Facts: []ItemMetadataFact{{Field: "uniqueIds", Value: ids, Source: "nfo"}, {Field: "year", Value: json.RawMessage(`1995`)}}}
	target := MetadataRefreshTarget{ItemID: item, Kind: "Movie", ScanTitle: "Heat", ScanYear: 1994}
	s := NewMetadataRefreshSubject(target, md, true)
	if s.Resource != "movie" || s.Title != "Heat" || s.Year != 1995 || s.TMDBID != 949 || s.IMDbID != "tt0113277" || s.TVDBID != "" || s.Protected || s.ExistingProviderID != 0 {
		t.Fatalf("subject %+v", s)
	}
	if s := NewMetadataRefreshSubject(target, md, false); s.TMDBID != 0 || s.IMDbID != "" {
		t.Fatal("identifiers read although the nfo source is off", s)
	}
	// A curated title wins over the file name; the date gives the year.
	md.Fields = []ItemMetadataField{{Field: "title", Value: "Heat", Source: "manual"}, {Field: "date", Value: "1995-12-15", Source: "tmdb",
		ProviderOrigin: &MetadataProviderOrigin{Resource: "movie", ProviderID: 949}}}
	md.Facts = nil
	target.ScanTitle, target.ScanYear = "Something else", 0
	s = NewMetadataRefreshSubject(target, md, true)
	if s.Title != "Heat" || s.Year != 1995 || s.ExistingProviderID != 949 || s.Existing.ProviderID != 949 || s.Existing.Title != "Heat" || s.Protected {
		t.Fatalf("curated subject %+v", s)
	}
	// Every provider field locked or manual: nothing could change.
	md.Fields = []ItemMetadataField{{Field: "title", Value: "Heat", Source: "manual"}, {Field: "originalTitle", Value: "", Source: "tmdb", Locked: true},
		{Field: "overview", Value: "x", Source: "manual"}, {Field: "date", Value: "1995-12-15", Source: "scan", Locked: true}}
	if s := NewMetadataRefreshSubject(target, md, true); !s.Protected {
		t.Fatal("all-protected item not protected", s)
	}
	// A series uses the series resource; a season is not refreshed.
	if s := NewMetadataRefreshSubject(MetadataRefreshTarget{ItemID: item, Kind: "Series"}, ItemMetadata{}, true); s.Resource != "series" {
		t.Fatal("series resource", s)
	}
	if MetadataRefreshResource("Season") != "" || MetadataRefreshResource("HomeVideo") != "movie" {
		t.Fatal("resource mapping")
	}
	// Invalid identifier values are ignored.
	md.Facts = []ItemMetadataFact{{Field: "uniqueIds", Value: json.RawMessage(`[{"type":"tmdb","value":"0"},{"type":"imdb","value":"nm123"}]`)}}
	if s := NewMetadataRefreshSubject(target, md, true); s.TMDBID != 0 || s.IMDbID != "" {
		t.Fatal("invalid identifiers used", s)
	}
}

func TestValidMetadataRefreshResult(t *testing.T) {
	item := "22222222-2222-4222-8222-222222222222"
	review := &MetadataRefreshReviewInput{Resource: "movie", Confidence: MetadataConfidenceLow, Reason: MetadataMatchYearMismatch, QueryTitle: "Heat", QueryYear: 2001,
		Candidates: []MetadataRefreshCandidate{refreshCandidate(949, "Heat", "1995-12-15")}}
	if !ValidMetadataRefreshResult(MetadataRefreshResult{ItemID: item, Outcome: MetadataRefreshReviewed, Review: review}) {
		t.Fatal("valid review rejected")
	}
	for _, r := range []MetadataRefreshResult{
		{ItemID: "x", Outcome: MetadataRefreshApplied},
		{ItemID: item, Outcome: "bogus"},
		{ItemID: item, Outcome: MetadataRefreshReviewed},
		{ItemID: item, Outcome: MetadataRefreshApplied, Review: review},
		{ItemID: item, Outcome: MetadataRefreshReviewed, Review: &MetadataRefreshReviewInput{Resource: "movie", Confidence: MetadataConfidenceLow, Reason: MetadataMatchNoCandidates, Candidates: review.Candidates}},
		{ItemID: item, Outcome: MetadataRefreshReviewed, Review: &MetadataRefreshReviewInput{Resource: "episode", Confidence: MetadataConfidenceLow, Reason: MetadataMatchYearMismatch, Candidates: review.Candidates}},
		{ItemID: item, Outcome: MetadataRefreshReviewed, Review: &MetadataRefreshReviewInput{Resource: "movie", Confidence: MetadataConfidenceLow, Reason: MetadataMatchYearMismatch, QueryYear: 12, Candidates: review.Candidates}},
		{ItemID: item, Outcome: MetadataRefreshReviewed, Review: &MetadataRefreshReviewInput{Resource: "movie", Confidence: MetadataConfidenceLow, Reason: MetadataMatchYearMismatch,
			Candidates: []MetadataRefreshCandidate{review.Candidates[0], review.Candidates[0]}}},
	} {
		if ValidMetadataRefreshResult(r) {
			t.Error("invalid result accepted", r.Outcome)
		}
	}
}
