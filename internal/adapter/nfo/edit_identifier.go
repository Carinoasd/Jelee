package nfo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

var ErrIDGeneration = errors.New("nfo_id_generation_failed")

func lockedIdentifierName(name string) bool {
	switch name {
	case "id", "uniqueid", "uniqueids", "providerids", "imdbid", "tmdbid", "tvdbid":
		return true
	}
	return false
}

// EnsureID inserts a random UUID v4 only when this entry has no recognized ID.
// Existing identifiers, including manual provider IDs, are never overwritten.
func (d *Document) EnsureID(ctx context.Context, entry int, maxBytes int64, options TextEditOptions) (*Document, error) {
	return d.ensureID(ctx, entry, "", maxBytes, options)
}

// EnsureIDValue freezes an application-owned canonical UUID for replayable
// writes. It still adds nothing when the source already has an identifier.
func (d *Document) EnsureIDValue(ctx context.Context, entry int, id string, maxBytes int64, options TextEditOptions) (*Document, error) {
	if !domain.ValidID(id) {
		return nil, ErrInvalidInput
	}
	return d.ensureID(ctx, entry, id, maxBytes, options)
}

func (d *Document) ensureID(ctx context.Context, entry int, id string, maxBytes int64, options TextEditOptions) (*Document, error) {
	if d == nil || ctx == nil || entry < 0 || maxBytes < 1 || maxBytes > MaxAllowedBytes || options.BOM != "" && options.BOM != "preserve" && options.BOM != "include" && options.BOM != "omit" || len(options.Indent) > 8 {
		return nil, ErrInvalidInput
	}
	for _, r := range options.Indent {
		if r != ' ' && r != '\t' {
			return nil, ErrInvalidInput
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(d.original)) > maxBytes {
		return nil, ErrTooLarge
	}
	base, err := parseOriginal(ctx, d.original)
	if err != nil {
		return nil, err
	}
	if entry >= len(base.Entries) {
		return nil, ErrInvalidInput
	}
	for _, issue := range base.Issues {
		if issue.Severity == "error" {
			return nil, ErrInvalidInput
		}
	}
	if len(base.Entries[entry].UniqueIDs) > 0 {
		base.edited, base.editBaseHash = d.edited, d.editBaseHash
		return base, nil
	}
	if id == "" {
		var data [16]byte
		if _, err := rand.Read(data[:]); err != nil {
			return nil, ErrIDGeneration
		}
		data[6] = (data[6] & 0x0f) | 0x40
		data[8] = (data[8] & 0x3f) | 0x80
		encoded := hex.EncodeToString(data[:])
		id = encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
	}
	options.CreateMissing = true
	return d.withTextOptions(ctx, entry, "uniqueid", id, maxBytes, options, true)
}
