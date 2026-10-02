package nfo

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.NFOItemFieldsReader = (*SummaryReader)(nil)

// ReadItemFields uses the same safe root-relative full-byte reader as validation.
// The returned observation does not prove that the path belongs to an item.
func (r *SummaryReader) ReadItemFields(ctx context.Context, path domain.NFOSource, kind string) (domain.NFOItemFields, error) {
	if ctx == nil || (kind != "Movie" && kind != "Series" && kind != "HomeVideo") {
		return domain.NFOItemFields{}, domain.ErrInvalid
	}
	observed, err := r.Read(ctx, path)
	if err != nil {
		return domain.NFOItemFields{}, err
	}
	fields, err := r.projectItemFields(ctx, observed.(*summarySource), kind)
	if err != nil {
		if ctx.Err() != nil {
			return domain.NFOItemFields{}, ctx.Err()
		}
		return domain.NFOItemFields{}, domain.ErrMetadataUnavailable
	}
	return fields, nil
}

func (r *SummaryReader) projectItemFields(ctx context.Context, source *summarySource, kind string) (domain.NFOItemFields, error) {
	document, err := source.source.Parse(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return domain.NFOItemFields{}, ctx.Err()
		}
		return domain.NFOItemFields{}, err
	}
	summary, err := projectSummary(ctx, document)
	root := "movie"
	resultKind := "Movie"
	if kind == "Series" {
		root, resultKind = "tvshow", "Series"
	}
	if err != nil || summary.Status != domain.NFOStatusValid || summary.Root != root || len(document.Entries) != 1 || document.Entries[0].Root != root {
		if ctx.Err() != nil {
			return domain.NFOItemFields{}, ctx.Err()
		}
		return domain.NFOItemFields{}, domain.ErrMetadataUnavailable
	}
	// The general parser retains compatibility with repeated fields. Writes use
	// a stricter view: ambiguous singleton fields cannot silently choose a value.
	if err := uniqueItemFields(ctx, source.source.original); err != nil {
		return domain.NFOItemFields{}, err
	}
	metadata := document.Entries[0]
	// A write cannot choose between different IDs for the same provider.
	// The compatibility parser still retains both entries for read-only use.
	providerIDs := make(map[string]string)
	for _, id := range metadata.UniqueIDs {
		provider := strings.ToLower(strings.TrimSpace(id.Type))
		if previous, exists := providerIDs[provider]; exists && previous != id.Value {
			return domain.NFOItemFields{}, domain.ErrMetadataUnavailable
		}
		providerIDs[provider] = id.Value
	}
	result := domain.NFOItemFields{Version: domain.NFOItemFieldsVersion, Kind: resultKind, Identity: r.Identity(), Stamp: source.Stamp(), ReadAt: time.Now().UTC(), Fields: []domain.NFOTextField{}, LockedFields: slices.Clone(metadata.LockedFields)}
	for _, name := range metadata.LockedFields {
		if strings.EqualFold(strings.TrimSpace(name), "OfficialRating") {
			result.Version = domain.NFOItemTextFieldsVersion
		}
	}
	if metadata.LockData != nil {
		result.LockData = *metadata.LockData
	}
	if strings.TrimSpace(metadata.SortTitle) != "" && result.Version != domain.NFOItemTextFieldsVersion {
		result.Version = domain.NFOItemSortFieldsVersion
	}
	for _, name := range metadata.LockedFields {
		if strings.EqualFold(strings.TrimSpace(name), "SortName") && result.Version != domain.NFOItemTextFieldsVersion {
			result.Version = domain.NFOItemSortFieldsVersion
		}
	}
	if strings.TrimSpace(metadata.Tagline) != "" {
		result.Version = domain.NFOItemTextFieldsVersion
	}
	for _, name := range metadata.LockedFields {
		if strings.EqualFold(strings.TrimSpace(name), "Tagline") {
			result.Version = domain.NFOItemTextFieldsVersion
		}
	}
	if strings.TrimSpace(metadata.Outline) != "" {
		result.Version = domain.NFOItemTextFieldsVersion
	}
	for _, name := range metadata.LockedFields {
		if strings.EqualFold(strings.TrimSpace(name), "outline") {
			result.Version = domain.NFOItemTextFieldsVersion
		}
	}
	if strings.TrimSpace(metadata.MPAA) != "" {
		result.Version = domain.NFOItemTextFieldsVersion
	}
	for _, name := range metadata.LockedFields {
		if strings.EqualFold(strings.TrimSpace(name), "mpaa") {
			result.Version = domain.NFOItemTextFieldsVersion
		}
	}
	if strings.TrimSpace(metadata.Certification) != "" {
		result.Version = domain.NFOItemTextFieldsVersion
	}
	for _, name := range metadata.LockedFields {
		if strings.EqualFold(strings.TrimSpace(name), "certification") {
			result.Version = domain.NFOItemTextFieldsVersion
		}
	}
	for _, field := range []domain.NFOTextField{{Field: "title", Value: metadata.Title}, {Field: "originalTitle", Value: metadata.OriginalTitle}, {Field: "overview", Value: metadata.Plot}, {Field: "date", Value: metadata.Premiered}, {Field: "sortTitle", Value: metadata.SortTitle}, {Field: "tagline", Value: metadata.Tagline}, {Field: "outline", Value: metadata.Outline}, {Field: "mpaa", Value: metadata.MPAA}, {Field: "certification", Value: metadata.Certification}} {
		if strings.TrimSpace(field.Value) != "" {
			result.Fields = append(result.Fields, field)
		}
	}
	if metadata.Year != nil {
		result.Version = domain.NFOItemYearFieldsVersion
		result.Facts = []domain.NFOIntegerFact{{Field: "year", Value: *metadata.Year}}
	}
	for _, name := range metadata.LockedFields {
		if strings.EqualFold(strings.TrimSpace(name), "year") || strings.EqualFold(strings.TrimSpace(name), "productionyear") {
			result.Version = domain.NFOItemYearFieldsVersion
		}
	}
	if metadata.RuntimeMinutes != nil {
		result.Version = domain.NFOItemNumericFieldsVersion
		result.Facts = append(result.Facts, domain.NFOIntegerFact{Field: "runtimeMinutes", Value: *metadata.RuntimeMinutes})
	}
	for _, name := range metadata.LockedFields {
		if strings.EqualFold(strings.TrimSpace(name), "runtime") || strings.EqualFold(strings.TrimSpace(name), "runtimeMinutes") {
			result.Version = domain.NFOItemNumericFieldsVersion
		}
	}
	if metadata.Rating != nil {
		result.Version = domain.NFOItemNumericFieldsVersion
		result.NumberFacts = []domain.NFONumberFact{{Field: "rating", Value: *metadata.Rating}}
	}
	for _, name := range metadata.LockedFields {
		if strings.EqualFold(strings.TrimSpace(name), "rating") || strings.EqualFold(strings.TrimSpace(name), "communityRating") {
			result.Version = domain.NFOItemNumericFieldsVersion
		}
	}
	if metadata.UserRating != nil {
		result.Version = domain.NFOItemNumericFieldsVersion
		result.NumberFacts = append(result.NumberFacts, domain.NFONumberFact{Field: "userRating", Value: *metadata.UserRating})
	}
	for _, name := range metadata.LockedFields {
		if strings.EqualFold(strings.TrimSpace(name), "userRating") {
			result.Version = domain.NFOItemNumericFieldsVersion
		}
	}
	for _, list := range []domain.NFOStringList{
		{Field: "genres", Values: metadata.Genres}, {Field: "tags", Values: metadata.Tags},
		{Field: "studios", Values: metadata.Studios}, {Field: "countries", Values: metadata.Countries},
		{Field: "languages", Values: metadata.Languages}, {Field: "directors", Values: metadata.Directors},
		{Field: "writers", Values: metadata.Writers}, {Field: "producers", Values: metadata.Producers},
	} {
		if len(list.Values) > 0 {
			result.Version = domain.NFOItemListFieldsVersion
			list.Values = slices.Clone(list.Values)
			result.Lists = append(result.Lists, list)
		}
	}
	for _, name := range metadata.LockedFields {
		if domain.MetadataListFieldForLock(name) != "" {
			result.Version = domain.NFOItemListFieldsVersion
		}
	}
	if len(metadata.Actors) > 0 {
		result.Version = domain.NFOItemActorFieldsVersion
		for _, actor := range metadata.Actors {
			result.Actors = append(result.Actors, domain.NFOActor{Name: actor.Name, Role: actor.Role, Thumb: actor.Thumb, Order: actor.Order})
		}
		result.Actors = domain.CloneNFOActors(result.Actors)
	}
	for _, name := range metadata.LockedFields {
		if strings.EqualFold(strings.TrimSpace(name), "Actor") || strings.EqualFold(strings.TrimSpace(name), "Actors") || strings.EqualFold(strings.TrimSpace(name), "Cast") {
			result.Version = domain.NFOItemActorFieldsVersion
		}
	}
	if len(metadata.UniqueIDs) > 0 {
		result.Version = domain.NFOItemIdentifierFieldsVersion
		for _, id := range metadata.UniqueIDs {
			result.UniqueIDs = append(result.UniqueIDs, domain.NFOUniqueID{Type: id.Type, Value: id.Value, Default: id.Default})
		}
	}
	for _, name := range metadata.LockedFields {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "uniqueid", "uniqueids", "providerids", "imdbid", "tmdbid", "tvdbid":
			result.Version = domain.NFOItemIdentifierFieldsVersion
		}
	}
	if len(result.Fields) == 0 && len(result.Facts) == 0 && len(result.NumberFacts) == 0 && len(result.Lists) == 0 && result.Version != domain.NFOItemIdentifierFieldsVersion && result.Version != domain.NFOItemActorFieldsVersion && result.Version != domain.NFOItemListFieldsVersion && result.Version != domain.NFOItemNumericFieldsVersion && result.Version != domain.NFOItemYearFieldsVersion && result.Version != domain.NFOItemSortFieldsVersion && result.Version != domain.NFOItemTextFieldsVersion && domain.HasNFOItemFieldLock(result) {
		result.Version = domain.NFOItemLockFieldsVersion
	}
	if !domain.ValidNFOItemFields(result) {
		return domain.NFOItemFields{}, domain.ErrMetadataUnavailable
	}
	if err := ctx.Err(); err != nil {
		return domain.NFOItemFields{}, err
	}
	return result, nil
}

func uniqueItemFields(ctx context.Context, original []byte) error {
	decoded, encoding, _, err := decodeEncoding(original)
	if err != nil {
		return domain.ErrMetadataUnavailable
	}
	decoder := xml.NewDecoder(contextReader{ctx, bytes.NewReader(decoded)})
	decoder.CharsetReader = decodedCharset(encoding)
	depth := 0
	seen := map[string]bool{}
	var actorFields map[string]bool
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return domain.ErrMetadataUnavailable
		}
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 3 && actorFields != nil {
				name := elementName(token.Name)
				switch name {
				case "name", "role", "thumb", "order":
					if actorFields[name] {
						return domain.ErrMetadataUnavailable
					}
					actorFields[name] = true
				}
			}
			if depth != 2 {
				continue
			}
			name := elementName(token.Name)
			if name == "actor" {
				actorFields = map[string]bool{}
			}
			// Count aliases by their scalar destination, matching metadata parsing.
			switch name {
			case "name", "localtitle", "seasonname":
				name = "title"
			case "releasedate":
				name = "premiered"
			case "sortname":
				name = "sorttitle"
			case "communityrating":
				name = "rating"
			}
			switch name {
			case "title", "originaltitle", "plot", "premiered", "sorttitle", "tagline", "outline", "mpaa", "certification", "year", "runtime", "rating", "userrating", "lockdata", "lockedfields":
				if seen[name] {
					return domain.ErrMetadataUnavailable
				}
				seen[name] = true
			}
		case xml.EndElement:
			if depth == 2 {
				actorFields = nil
			}
			depth--
		}
	}
}
