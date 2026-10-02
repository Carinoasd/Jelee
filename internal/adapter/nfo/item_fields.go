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
	result := domain.NFOItemFields{Version: domain.NFOItemFieldsVersion, Kind: resultKind, Identity: r.Identity(), Stamp: source.Stamp(), ReadAt: time.Now().UTC(), Fields: []domain.NFOTextField{}, LockedFields: slices.Clone(metadata.LockedFields)}
	if metadata.LockData != nil {
		result.LockData = *metadata.LockData
	}
	for _, field := range []domain.NFOTextField{{Field: "title", Value: metadata.Title}, {Field: "originalTitle", Value: metadata.OriginalTitle}, {Field: "overview", Value: metadata.Plot}, {Field: "date", Value: metadata.Premiered}} {
		if strings.TrimSpace(field.Value) != "" {
			result.Fields = append(result.Fields, field)
		}
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
			if depth != 2 {
				continue
			}
			name := elementName(token.Name)
			switch name {
			case "title", "originaltitle", "plot", "premiered", "lockdata", "lockedfields":
				if seen[name] {
					return domain.ErrMetadataUnavailable
				}
				seen[name] = true
			}
		case xml.EndElement:
			depth--
		}
	}
}
