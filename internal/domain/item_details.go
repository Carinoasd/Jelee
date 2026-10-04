package domain

import (
	"encoding/json"
	"slices"
	"time"
)

// Item details (G34.3): the display metadata of one visible item for every
// user who may see it. Unlike the administrator metadata view it carries no
// provenance beyond "this value came from an NFO file": no source or root
// identifiers, digests, provider URLs or file paths.

// ItemDetailsNFOUnread reports that no NFO observation was confirmed for the
// item yet; the other states are the confirmed observation statuses.
const ItemDetailsNFOUnread = "unread"

// itemDetailsNFOFields are the displayed fields whose NFO origin is
// reported. Other persisted fields are not part of the public view.
var itemDetailsNFOFields = []string{"title", "originalTitle", "sortTitle", "tagline", "overview", "date", "year", "genres", "uniqueIds"}

// ItemDetailsNFOFieldNames lists the fields whose NFO origin may be
// reported, in display order.
func ItemDetailsNFOFieldNames() []string { return slices.Clone(itemDetailsNFOFields) }

type ItemExternalID struct {
	Type    string
	Value   string
	Default bool
}

// ItemDetailsNFO is the NFO read state of an item. ReadAt is zero when
// Status is ItemDetailsNFOUnread.
type ItemDetailsNFO struct {
	Status string
	ReadAt time.Time
	// Fields names the displayed fields whose current value came from an
	// NFO file, in ItemDetailsNFOFieldNames order.
	Fields []string
}

// ItemDetails is BrowseItem with the overview always filled, plus the
// remaining display metadata.
type ItemDetails struct {
	BrowseItem
	OriginalTitle string
	Tagline       string
	Genres        []string
	ExternalIDs   []ItemExternalID
	NFO           ItemDetailsNFO
}

// ItemDetailsRecord is what storage returns before the facts are decoded.
// Raw values are nil when the fact is absent.
type ItemDetailsRecord struct {
	Item          BrowseItem
	OriginalTitle string
	Tagline       string
	Genres        json.RawMessage
	UniqueIDs     json.RawMessage
	// Observation is the last confirmed NFO observation, nil when none is
	// stored. Revision is the item's metadata revision it must not exceed.
	Observation *LastConfirmedNFOObservation
	Revision    int64
	NFOFields   []string
}

// BuildItemDetails decodes a storage record. A fact that no longer passes
// validation is left out instead of failing the whole view, and an
// observation that does not validate is reported as unread.
func BuildItemDetails(r ItemDetailsRecord) ItemDetails {
	d := ItemDetails{BrowseItem: r.Item, OriginalTitle: r.OriginalTitle, Tagline: r.Tagline,
		Genres: []string{}, ExternalIDs: []ItemExternalID{}, NFO: ItemDetailsNFO{Status: ItemDetailsNFOUnread, Fields: []string{}}}
	if r.Genres != nil && ValidItemMetadataFactValue("genres", r.Genres) {
		var genres []string
		if json.Unmarshal(r.Genres, &genres) == nil {
			for _, genre := range genres {
				if genre != "" && !slices.Contains(d.Genres, genre) {
					d.Genres = append(d.Genres, genre)
				}
			}
		}
	}
	if r.UniqueIDs != nil && ValidMetadataUniqueIDValue(r.UniqueIDs) {
		var ids []NFOUniqueID
		if json.Unmarshal(r.UniqueIDs, &ids) == nil {
			for _, id := range ids {
				d.ExternalIDs = append(d.ExternalIDs, ItemExternalID(id))
			}
		}
	}
	if o := r.Observation; o != nil && ValidLastConfirmedNFOObservation(*o) && o.AcceptedRevision <= r.Revision {
		d.NFO.Status, d.NFO.ReadAt = o.Status, o.ReadAt.UTC()
	}
	for _, field := range itemDetailsNFOFields {
		if slices.Contains(r.NFOFields, field) {
			d.NFO.Fields = append(d.NFO.Fields, field)
		}
	}
	return d
}
