package domain

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"time"
)

// Metadata backup (G36.4): a JSON Lines document holding what a rescan of
// the media cannot rebuild. Line one is the header, every following line one
// record {"kind":…,"data":{…}} in MetadataBackupKinds order, and the last
// line the trailer with the record counts and the SHA-256 of every byte
// before it. A file without its trailer is truncated; a digest or count
// mismatch is corruption. Readers and writers hold one line at a time.
const (
	MetadataBackupFormat = "jelee.metadata"
	// MetadataBackupFormatVersion is written by this release. Version 2
	// added collections, playlists, user interface preferences, the site
	// settings and the audit retention; a version 1 document is a version 2
	// document without those kinds and is still read.
	MetadataBackupFormatVersion = 2
	// MetadataBackupMinFormatVersion is the oldest format readers accept.
	MetadataBackupMinFormatVersion = 1
	// MetadataBackupMinSchema is the first schema whose rows format version 1
	// describes. Exports from a newer schema are refused: upgrade first.
	MetadataBackupMinSchema = 71
	// MetadataBackupMaxLine bounds one line, and so reader memory. The
	// largest rows are sealed webhook headers (64 KiB) and metadata facts.
	MetadataBackupMaxLine = 1 << 20
	// MetadataBackupMaxRecords bounds a document; a trailer may not claim more.
	MetadataBackupMaxRecords  = 1 << 32
	metadataBackupTrailerKind = "end"
)

var (
	ErrMetadataBackupCorrupt     = errors.New("metadata_backup_corrupt")
	ErrMetadataBackupTruncated   = errors.New("metadata_backup_truncated")
	ErrMetadataBackupUnsupported = errors.New("metadata_backup_unsupported")
	ErrMetadataBackupConflict    = errors.New("metadata_backup_conflict")
)

// MetadataBackupKinds is the record order. Every record refers only to
// records of an earlier kind or of its own kind.
var MetadataBackupKinds = []string{
	"library", "library_root", "user", "library_acl",
	"item", "media_source", "item_directory_source", "item_parent_link",
	"catalog_scan_item", "catalog_scan_source", "catalog_scan_item_alias", "item_version_exclusion", "item_primary_version",
	"item_metadata_state", "item_metadata_field", "item_metadata_fact", "item_nfo_field_lock", "item_image",
	"user_item_data", "user_track_preference",
	"access_policy", "parental_rating", "user_item_access_rule", "user_blocked_tag",
	"client_control_policy", "client_rule", "library_network_rule",
	"webhook", "scan_schedule",
	"collection", "collection_item", "playlist", "playlist_item", "user_preference",
	"site_appearance", "site_plugins", "audit_retention",
}

func metadataBackupKindIndex(kind string) int {
	for i, k := range MetadataBackupKinds {
		if k == kind {
			return i
		}
	}
	return -1
}

type MetadataBackupHeader struct {
	Format        string    `json:"format"`
	FormatVersion int       `json:"formatVersion"`
	SchemaVersion int       `json:"schemaVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	// PasswordHashes says user records carry password_hash. Off by default;
	// sessions, tokens and creation keys are never exported.
	PasswordHashes bool `json:"passwordHashes"`
}

type MetadataBackupTrailer struct {
	Kind    string           `json:"kind"`
	Records int64            `json:"records"`
	Counts  map[string]int64 `json:"counts"`
	SHA256  string           `json:"sha256"`
}

type metadataBackupLine struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// MetadataExportOptions selects what an export carries.
type MetadataExportOptions struct {
	IncludePasswordHashes bool
}

type MetadataExportSummary struct {
	FormatVersion  int              `json:"formatVersion"`
	SchemaVersion  int              `json:"schemaVersion"`
	CreatedAt      time.Time        `json:"createdAt"`
	PasswordHashes bool             `json:"passwordHashes"`
	Records        int64            `json:"records"`
	Counts         map[string]int64 `json:"counts"`
	SHA256         string           `json:"sha256"`
}

type MetadataImportOptions struct {
	// DryRun runs the whole import, then rolls it back.
	DryRun bool
	// SkipConflicts imports what does not conflict and reports the rest;
	// otherwise any conflict aborts before a row is written.
	SkipConflicts bool
}

// MetadataKindReport counts one record kind. Records = Inserted + Updated +
// Unchanged + the sum of Skipped.
type MetadataKindReport struct {
	Records   int64            `json:"records"`
	Inserted  int64            `json:"inserted"`
	Updated   int64            `json:"updated"`
	Unchanged int64            `json:"unchanged"`
	Skipped   map[string]int64 `json:"skipped,omitempty"`
}

// MetadataConflict names one exported record the target cannot take as is.
// ID is the exported identifier, never a secret or a path.
type MetadataConflict struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// MetadataImportConflictSample bounds the conflicts listed in a report; the
// totals per reason are always complete.
const MetadataImportConflictSample = 100

type MetadataImportReport struct {
	FormatVersion       int                            `json:"formatVersion"`
	SourceSchemaVersion int                            `json:"sourceSchemaVersion"`
	TargetSchemaVersion int                            `json:"targetSchemaVersion"`
	SHA256              string                         `json:"sha256"`
	PasswordHashes      bool                           `json:"passwordHashes"`
	DryRun              bool                           `json:"dryRun"`
	Committed           bool                           `json:"committed"`
	Kinds               map[string]*MetadataKindReport `json:"kinds"`
	ConflictTotals      map[string]int64               `json:"conflictTotals,omitempty"`
	Conflicts           []MetadataConflict             `json:"conflicts,omitempty"`
}

// Kind returns the report row of kind, creating it.
func (r *MetadataImportReport) Kind(kind string) *MetadataKindReport {
	if r.Kinds == nil {
		r.Kinds = map[string]*MetadataKindReport{}
	}
	k := r.Kinds[kind]
	if k == nil {
		k = &MetadataKindReport{}
		r.Kinds[kind] = k
	}
	return k
}

// Skip adds n skipped records of kind for reason.
func (k *MetadataKindReport) Skip(reason string, n int64) {
	if n <= 0 {
		return
	}
	if k.Skipped == nil {
		k.Skipped = map[string]int64{}
	}
	k.Skipped[reason] += n
}

// MetadataBackupWriter writes one document. Write records in kind order,
// then Close; nothing is buffered beyond the current line.
type MetadataBackupWriter struct {
	out     *bufio.Writer
	digest  hash.Hash
	counts  map[string]int64
	records int64
	kind    int
	closed  bool
}

func NewMetadataBackupWriter(w io.Writer, header MetadataBackupHeader) (*MetadataBackupWriter, error) {
	if w == nil || header.SchemaVersion < MetadataBackupMinSchema {
		return nil, ErrInvalid
	}
	header.Format, header.FormatVersion = MetadataBackupFormat, MetadataBackupFormatVersion
	header.CreatedAt = header.CreatedAt.UTC()
	line, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	b := &MetadataBackupWriter{out: bufio.NewWriterSize(w, 64<<10), digest: sha256.New(), counts: map[string]int64{}}
	return b, b.line(line)
}

func (b *MetadataBackupWriter) line(p []byte) error {
	if _, err := b.out.Write(p); err != nil {
		return err
	}
	if err := b.out.WriteByte('\n'); err != nil {
		return err
	}
	b.digest.Write(p)
	b.digest.Write([]byte{'\n'})
	return nil
}

var (
	recordPrefix = []byte(`{"kind":"`)
	recordMiddle = []byte(`","data":`)
)

// Write appends one record. data must be one compact JSON object; the
// writer checks its framing, not its fields.
func (b *MetadataBackupWriter) Write(kind string, data []byte) error {
	index := metadataBackupKindIndex(kind)
	if b.closed || index < b.kind || index < 0 || len(data) < 2 || data[0] != '{' || data[len(data)-1] != '}' ||
		bytes.IndexByte(data, '\n') >= 0 || len(data)+len(kind)+len(recordPrefix)+len(recordMiddle)+2 > MetadataBackupMaxLine {
		return ErrInvalid
	}
	b.kind = index
	for _, part := range [][]byte{recordPrefix, []byte(kind), recordMiddle, data, {'}', '\n'}} {
		if _, err := b.out.Write(part); err != nil {
			return err
		}
		b.digest.Write(part)
	}
	b.counts[kind]++
	b.records++
	return nil
}

// Close writes the trailer and flushes. It returns the trailer written.
func (b *MetadataBackupWriter) Close() (MetadataBackupTrailer, error) {
	if b.closed {
		return MetadataBackupTrailer{}, ErrInvalid
	}
	b.closed = true
	t := MetadataBackupTrailer{Kind: metadataBackupTrailerKind, Records: b.records, Counts: b.counts, SHA256: hex.EncodeToString(b.digest.Sum(nil))}
	line, err := json.Marshal(t)
	if err != nil {
		return t, err
	}
	if _, err = b.out.Write(append(line, '\n')); err != nil {
		return t, err
	}
	return t, b.out.Flush()
}

// MetadataBackupReader reads and verifies one document. Next returns io.EOF
// only after the trailer matched every byte and count before it, so a caller
// that applies records as they arrive must keep them uncommitted until then.
type MetadataBackupReader struct {
	in      *bufio.Reader
	digest  hash.Hash
	counts  map[string]int64
	records int64
	kind    int
	buf     []byte
	done    bool
	header  MetadataBackupHeader
	trailer MetadataBackupTrailer
}

// NewMetadataBackupReader reads the header. It accepts format versions
// MetadataBackupMinFormatVersion to MetadataBackupFormatVersion from schema
// MetadataBackupMinSchema up to maxSchema.
func NewMetadataBackupReader(r io.Reader, maxSchema int) (*MetadataBackupReader, MetadataBackupHeader, error) {
	b := &MetadataBackupReader{in: bufio.NewReaderSize(r, 64<<10), digest: sha256.New(), counts: map[string]int64{}}
	line, err := b.readLine()
	if err != nil {
		return nil, MetadataBackupHeader{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var h MetadataBackupHeader
	if err = dec.Decode(&h); err != nil || dec.More() || h.Format != MetadataBackupFormat {
		return nil, MetadataBackupHeader{}, ErrMetadataBackupCorrupt
	}
	if h.FormatVersion < MetadataBackupMinFormatVersion || h.FormatVersion > MetadataBackupFormatVersion || h.SchemaVersion < MetadataBackupMinSchema || h.SchemaVersion > maxSchema {
		return nil, h, ErrMetadataBackupUnsupported
	}
	b.digest.Write(line)
	b.header = h
	return b, h, nil
}

// readLine returns the next line with its newline, at most MaxLine bytes.
// End of input before a newline is truncation.
func (b *MetadataBackupReader) readLine() ([]byte, error) {
	b.buf = b.buf[:0]
	for {
		chunk, err := b.in.ReadSlice('\n')
		if len(b.buf)+len(chunk) > MetadataBackupMaxLine {
			return nil, ErrMetadataBackupCorrupt
		}
		b.buf = append(b.buf, chunk...)
		switch {
		case err == nil:
			return b.buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return nil, ErrMetadataBackupTruncated
		default:
			return nil, err
		}
	}
}

// Next returns the next record. The data slice is valid until the next call.
func (b *MetadataBackupReader) Next() (string, json.RawMessage, error) {
	if b.done {
		return "", nil, io.EOF
	}
	line, err := b.readLine()
	if err != nil {
		return "", nil, err
	}
	var rec metadataBackupLine
	if err = json.Unmarshal(line, &rec); err != nil {
		return "", nil, ErrMetadataBackupCorrupt
	}
	if rec.Kind == metadataBackupTrailerKind {
		return "", nil, b.finish(line)
	}
	index := metadataBackupKindIndex(rec.Kind)
	if index < b.kind || len(rec.Data) < 2 || rec.Data[0] != '{' || b.records >= MetadataBackupMaxRecords {
		return "", nil, ErrMetadataBackupCorrupt
	}
	b.kind = index
	b.digest.Write(line)
	b.counts[rec.Kind]++
	b.records++
	return rec.Kind, rec.Data, nil
}

func (b *MetadataBackupReader) finish(line []byte) error {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var t MetadataBackupTrailer
	if err := dec.Decode(&t); err != nil || dec.More() {
		return ErrMetadataBackupCorrupt
	}
	if t.SHA256 != hex.EncodeToString(b.digest.Sum(nil)) || t.Records != b.records || len(t.Counts) != len(b.counts) {
		return ErrMetadataBackupCorrupt
	}
	for kind, n := range b.counts {
		if t.Counts[kind] != n {
			return ErrMetadataBackupCorrupt
		}
	}
	// Nothing may follow the trailer.
	if _, err := b.in.ReadByte(); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrMetadataBackupCorrupt
		}
		return err
	}
	b.done, b.trailer = true, t
	return io.EOF
}

// Trailer is the verified trailer once Next returned io.EOF.
func (b *MetadataBackupReader) Trailer() (MetadataBackupTrailer, bool) {
	return b.trailer, b.done
}
