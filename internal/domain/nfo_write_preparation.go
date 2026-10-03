package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const NFOWritePreparationVersion = 1

// This request describes controlled edits, never a caller-supplied path or XML.
type NFOWriteTextEdit struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

type NFOWritePrepareRequest struct {
	ItemID        string             `json:"itemId"`
	Revision      int64              `json:"revision"`
	Edits         []NFOWriteTextEdit `json:"edits"`
	CreateMissing bool               `json:"createMissing"`
	BOM           string             `json:"bom"`
	Indent        string             `json:"indent"`
	MaxBytes      int64              `json:"maxBytes"`
	Backups       int                `json:"backups"`
}

func (NFOWritePrepareRequest) String() string   { return "nfo write request (data redacted)" }
func (NFOWritePrepareRequest) GoString() string { return "nfo write request (data redacted)" }

func CloneNFOWritePrepareRequest(v NFOWritePrepareRequest) NFOWritePrepareRequest {
	v.Edits = slices.Clone(v.Edits)
	return v
}

func ValidateNFOWritePrepareRequest(v NFOWritePrepareRequest) error {
	if !ValidID(v.ItemID) || v.Revision < 1 || v.Revision >= ItemMetadataRevisionMax || v.MaxBytes < 1 || v.MaxBytes > NFOMaxSourceBytes || v.Backups < 0 || v.Backups > 16 || len(v.Edits) > 10 || len(v.Indent) > 8 || strings.Trim(v.Indent, " \t") != "" || (v.BOM != "" && v.BOM != "preserve" && v.BOM != "include" && v.BOM != "omit") {
		return ErrInvalid
	}
	seen := map[string]bool{}
	var valuesBytes int64
	for _, edit := range v.Edits {
		switch edit.Field {
		case "title", "originaltitle", "sorttitle", "plot", "outline", "tagline", "showtitle", "status", "mpaa", "certification":
		default:
			return ErrInvalid
		}
		if seen[edit.Field] || !utf8.ValidString(edit.Value) || int64(len(edit.Value)) > v.MaxBytes {
			return ErrInvalid
		}
		valuesBytes += int64(len(edit.Value))
		if valuesBytes > NFOMaxSourceBytes {
			return ErrInvalid
		}
		seen[edit.Field] = true
	}
	if nfoWriteRequestBytes(v) > NFOMaxSourceBytes {
		return ErrInvalid
	}
	return nil
}

// Count JSON's escaped content before allocating the complete request. The
// empty-value envelope is small; valid requests still use encoding/json for
// their actual canonical bytes and digest.
func nfoWriteRequestBytes(v NFOWritePrepareRequest) int64 {
	envelope := CloneNFOWritePrepareRequest(v)
	for i := range envelope.Edits {
		envelope.Edits[i].Value = ""
	}
	encoded, _ := json.Marshal(envelope)
	total := int64(len(encoded))
	for _, edit := range v.Edits {
		total += int64(len(edit.Value))
		if total > NFOMaxSourceBytes {
			return total
		}
		for _, r := range edit.Value {
			switch r {
			case '"', '\\', '\b', '\f', '\n', '\r', '\t':
				total++
			case '<', '>', '&':
				total += 5
			case '\u2028', '\u2029':
				total += 3
			default:
				if r < 0x20 {
					total += 5
				}
			}
			if total > NFOMaxSourceBytes {
				return total
			}
		}
	}
	return total
}

func NFOWriteRequestDigest(v NFOWritePrepareRequest) (string, error) {
	if err := ValidateNFOWritePrepareRequest(v); err != nil {
		return "", err
	}
	encoded, _ := json.Marshal(v)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

// Frozen bytes are private preparation data. A preparation grants no filesystem
// execution authority. A future job must recheck policy, lease, scope and media.
type NFOWritePreparation struct {
	ID                string                      `json:"-"`
	Version           int                         `json:"-"`
	Request           NFOWritePrepareRequest      `json:"-"`
	Scope             NFOItemScope                `json:"-"`
	NativeObservation NFONativePreparationReceipt `json:"-"`
	Stamp             NFOStamp                    `json:"-"`
	Original          []byte                      `json:"-"`
	Replacement       []byte                      `json:"-"`
	CreatedAt         time.Time                   `json:"-"`
	ExpiresAt         time.Time                   `json:"-"`
}

func (NFOWritePreparation) String() string   { return "nfo write preparation (data redacted)" }
func (NFOWritePreparation) GoString() string { return "nfo write preparation (data redacted)" }

func CloneNFOWritePreparation(v NFOWritePreparation) NFOWritePreparation {
	v.Request = CloneNFOWritePrepareRequest(v.Request)
	v.NativeObservation = CloneNFONativePreparationReceipt(v.NativeObservation)
	v.Original = slices.Clone(v.Original)
	v.Replacement = slices.Clone(v.Replacement)
	return v
}

func ValidateNFOWritePreparation(v NFOWritePreparation) error {
	if v.Version != NFOWritePreparationVersion || ValidateNFOWritePrepareRequest(v.Request) != nil || !ValidNFOItemScope(v.Scope) || v.Scope.ItemID != v.Request.ItemID || v.Scope.Revision != v.Request.Revision || len(v.Original) < 1 || len(v.Replacement) < 1 || int64(len(v.Original)) > v.Request.MaxBytes || int64(len(v.Replacement)) > v.Request.MaxBytes || v.Stamp.Size != int64(len(v.Original)) || v.Stamp.FingerprintVersion != NFOFingerprintVersion {
		return ErrInvalid
	}
	if !v.NativeObservation.Empty() {
		kind := byte(1)
		if v.Scope.DirectoryPath != "" {
			kind = 2
		}
		if v.Scope.RootGeneration < 1 || ValidateNFONativePreparationReceipt(v.NativeObservation) != nil || v.NativeObservation.MediaIdentity()[2] != kind {
			return ErrInvalid
		}
	}
	hash := sha256.Sum256(v.Original)
	if v.Stamp.SHA256 != hex.EncodeToString(hash[:]) {
		return ErrInvalid
	}
	return nil
}
