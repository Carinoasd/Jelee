package domain

import (
	"testing"
	"time"
)

func TestTMDBMetadataPriorityAndOrigin(t *testing.T) {
	for _, c := range []struct {
		field   ItemMetadataField
		replace bool
		reason  string
	}{
		{ItemMetadataField{Field: "title", Source: "existing", Value: "Old"}, false, "existing"},
		{ItemMetadataField{Field: "title", Source: "existing", Value: "Old"}, true, ""},
		{ItemMetadataField{Field: "title", Source: "manual", Value: ""}, true, "manual"},
		{ItemMetadataField{Field: "overview", Source: "nfo", Value: "NFO"}, true, "nfo"},
		{ItemMetadataField{Field: "title", Source: "tmdb", Locked: true}, true, "locked"},
		{ItemMetadataField{Field: "title", Source: "manual", Locked: true}, true, "locked"},
		{ItemMetadataField{Field: "overview", Source: "existing", Value: ""}, false, ""},
	} {
		if got := TMDBMetadataSkip(c.field, c.replace); got != c.reason {
			t.Fatal("priority mismatch", got, c.reason)
		}
	}
	origin := MetadataProviderOrigin{Resource: "movie", ProviderID: 12, SourceURL: TMDBSourceURL("movie", 12), RequestedLanguage: "zh-TW", FetchedAt: time.Now().UTC()}
	if !ValidMetadataProviderOrigin(origin) {
		t.Fatal("valid origin rejected")
	}
	for _, mutate := range []func(*MetadataProviderOrigin){func(v *MetadataProviderOrigin) { v.SourceURL = "https://example.com" }, func(v *MetadataProviderOrigin) { v.ProviderID = 0 }, func(v *MetadataProviderOrigin) { v.Resource = "season" }, func(v *MetadataProviderOrigin) { v.RequestedLanguage = "fr-FR" }, func(v *MetadataProviderOrigin) { v.FetchedAt = time.Time{} }} {
		v := origin
		mutate(&v)
		if ValidMetadataProviderOrigin(v) {
			t.Fatal("invalid provider origin accepted")
		}
	}
	value := ItemMetadata{Fields: []ItemMetadataField{{ProviderOrigin: &origin}}}
	copy := CloneItemMetadata(value)
	copy.Fields[0].ProviderOrigin.SourceURL = "changed"
	if value.Fields[0].ProviderOrigin.SourceURL == "changed" {
		t.Fatal("provider origin shared")
	}
}
