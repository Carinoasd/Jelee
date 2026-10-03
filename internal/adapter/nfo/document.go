// Package nfo reads and edits local metadata without fetching referenced URLs.
package nfo

import (
	"context"
	"errors"
	"io"
	"slices"
)

const (
	DefaultMaxBytes int64 = 8 << 20
	MaxAllowedBytes int64 = 32 << 20
)

var (
	ErrInvalidInput        = errors.New("nfo_invalid_input")
	ErrTooLarge            = errors.New("nfo_too_large")
	ErrTooComplex          = errors.New("nfo_too_complex")
	ErrInvalidXML          = errors.New("nfo_invalid_xml")
	ErrUnsafeXML           = errors.New("nfo_unsafe_xml")
	ErrInvalidEncoding     = errors.New("nfo_invalid_encoding")
	ErrUnsupportedEncoding = errors.New("nfo_unsupported_encoding")
	ErrRead                = errors.New("nfo_read_failed")
	ErrWrite               = errors.New("nfo_copy_failed")
	ErrNotFound            = errors.New("nfo_not_found")
	ErrChanged             = errors.New("nfo_changed")
)

type Issue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Field    string `json:"field"`
	Entry    int    `json:"entry"`
}

type UniqueID struct {
	Type    string `json:"type"`
	Value   string `json:"value"`
	Default bool   `json:"default"`
}

type Artwork struct {
	Kind     string `json:"kind"`
	Location string `json:"location"`
	Preview  string `json:"preview,omitempty"`
	Season   *int   `json:"season,omitempty"`
}

type Person struct {
	Name  string `json:"name"`
	Role  string `json:"role,omitempty"`
	Thumb string `json:"thumb,omitempty"`
	Order *int   `json:"order,omitempty"`
}

type Rating struct {
	Name    string   `json:"name"`
	Value   *float64 `json:"value,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Votes   *int     `json:"votes,omitempty"`
	Default bool     `json:"default"`
}

// Metadata is an extracted view. Editing it does not edit the original document.
// Text and artwork locations remain untrusted; consumers must escape text and
// separately authorize any future filesystem access or outbound request.
type Metadata struct {
	Root               string     `json:"root"`
	Title              string     `json:"title"`
	OriginalTitle      string     `json:"originalTitle,omitempty"`
	SortTitle          string     `json:"sortTitle,omitempty"`
	Plot               string     `json:"plot,omitempty"`
	Outline            string     `json:"outline,omitempty"`
	Tagline            string     `json:"tagline,omitempty"`
	Year               *int       `json:"year,omitempty"`
	Season             *int       `json:"season,omitempty"`
	Episode            *int       `json:"episode,omitempty"`
	DisplaySeason      *int       `json:"displaySeason,omitempty"`
	DisplayEpisode     *int       `json:"displayEpisode,omitempty"`
	RuntimeMinutes     *int       `json:"runtimeMinutes,omitempty"`
	Premiered          string     `json:"premiered,omitempty"`
	Aired              string     `json:"aired,omitempty"`
	DateAdded          string     `json:"dateAdded,omitempty"`
	MPAA               string     `json:"mpaa,omitempty"`
	Certification      string     `json:"certification,omitempty"`
	Status             string     `json:"status,omitempty"`
	AirsDayOfWeek      string     `json:"airsDayOfWeek,omitempty"`
	AirsTime           string     `json:"airsTime,omitempty"`
	ShowTitle          string     `json:"showTitle,omitempty"`
	Collection         string     `json:"collection,omitempty"`
	CollectionOverview string     `json:"collectionOverview,omitempty"`
	Genres             []string   `json:"genres,omitempty"`
	Tags               []string   `json:"tags,omitempty"`
	Studios            []string   `json:"studios,omitempty"`
	Countries          []string   `json:"countries,omitempty"`
	Languages          []string   `json:"languages,omitempty"`
	Directors          []string   `json:"directors,omitempty"`
	Writers            []string   `json:"writers,omitempty"`
	Producers          []string   `json:"producers,omitempty"`
	Trailers           []string   `json:"trailers,omitempty"`
	Actors             []Person   `json:"actors,omitempty"`
	UniqueIDs          []UniqueID `json:"uniqueIds,omitempty"`
	Art                []Artwork  `json:"art,omitempty"`
	Rating             *float64   `json:"rating,omitempty"`
	UserRating         *float64   `json:"userRating,omitempty"`
	Ratings            []Rating   `json:"ratings,omitempty"`
	LockData           *bool      `json:"lockData,omitempty"`
	LockedFields       []string   `json:"lockedFields,omitempty"`
}

type Document struct {
	Root         string `json:"root"`
	Encoding     string `json:"encoding"`
	OriginalSize int64  `json:"originalSize"`
	// Metadata is the first entry in source order, also present in Entries.
	Metadata Metadata   `json:"metadata"`
	Entries  []Metadata `json:"entries"`
	Issues   []Issue    `json:"issues"`
	original []byte
}

// Validate returns the findings recorded while reading this original document.
// It does not validate later caller edits to the extracted metadata view.
func (d *Document) Validate() []Issue {
	if d == nil {
		return []Issue{{Severity: "error", Code: "nfo_document_missing", Field: "document", Entry: -1}}
	}
	return slices.Clone(d.Issues)
}

// WriteOriginal copies the original bytes, including encoding, BOM, comments,
// unknown elements/attributes, whitespace and ordering. It never serializes the
// extracted metadata. The caller owns the destination and must not target the
// source file. A blocking custom writer must provide its own deadline support.
func (d *Document) WriteOriginal(ctx context.Context, writer io.Writer) error {
	if d == nil || ctx == nil || writer == nil {
		return ErrInvalidInput
	}
	// Never lend the retained source bytes to a caller-owned writer. The small
	// scratch buffer also protects shared Source data from a misbehaving writer.
	buffer := make([]byte, min(32<<10, len(d.original)))
	for offset := 0; offset < len(d.original); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(offset+(32<<10), len(d.original))
		chunk := buffer[:end-offset]
		copy(chunk, d.original[offset:end])
		n, err := writer.Write(chunk)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || n < 0 || n > end-offset {
			return ErrWrite
		}
		if n != end-offset {
			return io.ErrShortWrite
		}
		offset += n
	}
	return ctx.Err()
}
