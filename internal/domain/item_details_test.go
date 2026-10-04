package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildItemDetails(t *testing.T) {
	const id = "12345678-1234-4234-8234-123456789abc"
	read := time.Date(2026, 9, 1, 8, 0, 0, 0, time.FixedZone("x", 3600))
	observation := LastConfirmedNFOObservation{Version: NFOItemObservationVersion, Status: NFOItemObservedMissing, SourceID: id, RootID: id, Generation: 1,
		IdentityDigest: strings.Repeat("b", 64), CandidateDigest: NFOCandidateDigest([]string{}), ReadAt: read, AcceptedRevision: 2}
	record := ItemDetailsRecord{Item: BrowseItem{ID: id, Title: "T"}, OriginalTitle: "O", Tagline: "L",
		Genres:      json.RawMessage(`["Drama","Drama","Comedy"]`),
		UniqueIDs:   json.RawMessage(`[{"type":"tmdb","value":"1","default":true},{"type":"imdb","value":"tt1"}]`),
		Observation: &observation, Revision: 2, NFOFields: []string{"uniqueIds", "outline", "title", "genres"}}
	d := BuildItemDetails(record)
	if d.ID != id || d.OriginalTitle != "O" || d.Tagline != "L" || !reflect.DeepEqual(d.Genres, []string{"Drama", "Comedy"}) ||
		!reflect.DeepEqual(d.ExternalIDs, []ItemExternalID{{Type: "tmdb", Value: "1", Default: true}, {Type: "imdb", Value: "tt1"}}) ||
		d.NFO.Status != NFOItemObservedMissing || !d.NFO.ReadAt.Equal(read) || d.NFO.ReadAt.Location() != time.UTC ||
		!reflect.DeepEqual(d.NFO.Fields, []string{"title", "genres", "uniqueIds"}) {
		t.Fatalf("details differ: %+v", d)
	}
	for name, change := range map[string]func(*ItemDetailsRecord){
		"absent": func(r *ItemDetailsRecord) { *r = ItemDetailsRecord{Item: r.Item, Revision: 1} },
		"null facts": func(r *ItemDetailsRecord) {
			r.Genres, r.UniqueIDs, r.Observation = json.RawMessage(`null`), json.RawMessage(`null`), nil
		},
		"invalid facts": func(r *ItemDetailsRecord) {
			r.Genres, r.UniqueIDs = json.RawMessage(`[1]`), json.RawMessage(`[{"type":"tmdb"}]`)
		},
		"observation too new":  func(r *ItemDetailsRecord) { r.Revision = 1 },
		"observation invalid":  func(r *ItemDetailsRecord) { o := observation; o.Status = "valid"; r.Observation = &o },
		"observation unsigned": func(r *ItemDetailsRecord) { o := observation; o.Version = ""; r.Observation = &o },
	} {
		r := record
		change(&r)
		d := BuildItemDetails(r)
		if d.Genres == nil || d.ExternalIDs == nil || d.NFO.Fields == nil {
			t.Fatalf("%s: lists must be empty, not nil", name)
		}
		switch name {
		case "absent", "null facts", "invalid facts":
			if len(d.Genres) != 0 || len(d.ExternalIDs) != 0 {
				t.Errorf("%s: facts kept %+v", name, d)
			}
		}
		if name != "invalid facts" && name != "null facts" && (d.NFO.Status != ItemDetailsNFOUnread || !d.NFO.ReadAt.IsZero()) {
			t.Errorf("%s: observation reported %+v", name, d.NFO)
		}
	}
	names := ItemDetailsNFOFieldNames()
	names[0] = "changed"
	if ItemDetailsNFOFieldNames()[0] != "title" {
		t.Fatal("field names are shared")
	}
}
