package domain

import (
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Image source kinds in G40.10 priority order: local files beat NFO
// references, which beat external fetches; embedded artwork is last.
const (
	ImageSourceLocal    = "local"
	ImageSourceNFO      = "nfo"
	ImageSourceRemote   = "remote"
	ImageSourceEmbedded = "embedded"
)

const (
	ItemImageMaxIndex         = 9999
	ItemImageMaxURLBytes      = 2048
	ItemImageMaxPathBytes     = 1024
	ItemImageMaxDimension     = 65535
	ItemImageMaxAverageColor  = 0xFFFFFF
	ItemImageVariantListLimit = 1000
)

// itemImageTypes follows G40.1. Fanart is stored as Backdrop.
var itemImageTypes = map[string]bool{"Primary": true, "Backdrop": true, "Logo": true, "ClearLogo": true, "Banner": true,
	"ClearArt": true, "Art": true, "Disc": true, "Thumb": true, "Landscape": true, "Chapter": true, "Box": true,
	"BoxRear": true, "Menu": true, "Profile": true}

var itemImageFormats = map[string]bool{"jpeg": true, "png": true, "webp": true, "avif": true, "gif": true, "bmp": true, "tiff": true}

var itemImageExtensions = [...]string{".jpg", ".jpeg", ".png", ".webp", ".avif", ".gif", ".bmp", ".tiff"}

func ValidItemImageType(value string) bool { return itemImageTypes[value] }

// ValidItemImageSlot accepts a non-zero index only for gallery types.
func ValidItemImageSlot(imageType string, index int) bool {
	return ValidItemImageType(imageType) && index >= 0 && index <= ItemImageMaxIndex &&
		(index == 0 || imageType == "Backdrop" || imageType == "Chapter")
}

func ValidItemImageSourceKind(value string) bool {
	return value == ImageSourceLocal || value == ImageSourceNFO || value == ImageSourceRemote || value == ImageSourceEmbedded
}

// ItemImageSourcePriority is the G40.10 rank; lower wins after the lock.
func ItemImageSourcePriority(kind string) int {
	switch kind {
	case ImageSourceLocal:
		return 0
	case ImageSourceNFO:
		return 1
	case ImageSourceRemote:
		return 2
	case ImageSourceEmbedded:
		return 3
	}
	return 4
}

// ItemImageContent describes the bytes kept in the image store. A nil
// ContentSHA256 means the source has not been read or fetched yet.
type ItemImageContent struct {
	SHA256        []byte
	Width, Height int
	Format        string
	Bytes         int64
	AverageColor  *int
	FetchedAt     time.Time
}

// ItemImageInput is one source observation for an item slot. Manual marks a
// user action; only manual input may replace a locked row or set Locked.
type ItemImageInput struct {
	ItemID, Type string
	Index        int
	SourceKind   string
	RootID       string
	RelativePath string
	RemoteURL    string
	// SourceModifiedUnixNano and SourceSize are optional file attributes of a
	// root reference; set both or neither.
	SourceModifiedUnixNano *int64
	SourceSize             *int64
	Content                *ItemImageContent
	Locked                 bool
	Manual                 bool
}

// ItemImage is an authorized catalog row. RootPath is the absolute library
// root for root references and is redacted from diagnostics.
type ItemImage struct {
	ID, ItemID, LibraryID, Type string
	Index                       int
	SourceKind                  string
	RootID, RootPath            string
	RelativePath, RemoteURL     string
	SourceModifiedUnixNano      *int64
	SourceSize                  *int64
	Content                     *ItemImageContent
	Locked                      bool
	CreatedAt, UpdatedAt        time.Time
}

func (ItemImage) String() string   { return "item image (redacted)" }
func (ItemImage) GoString() string { return "item image (redacted)" }

// ItemImageUpsert reports whether the stored row changed. A locked row that
// rejected a non-manual refresh is returned unchanged with Skipped set.
type ItemImageUpsert struct {
	Image   ItemImage
	Created bool
	Skipped bool
}

// ImageVariant is one entry of the variant store index.
type ImageVariant struct {
	ContentSHA256, VariantKey [32]byte
	Bytes                     int64
	CreatedAt, LastAccess     time.Time
}

// ValidItemImageRelativePath mirrors the database check: a clean slash path
// inside its root without control characters, colons or backslashes.
func ValidItemImageRelativePath(value string) bool {
	if len(value) == 0 || len(value) > ItemImageMaxPathBytes || !utf8.ValidString(value) || strings.ContainsAny(value, ":\\") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") || path.Clean(value) != value {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) > 128 {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." {
			return false
		}
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validItemImageFileName(value string) bool {
	lower := strings.ToLower(value)
	for _, extension := range itemImageExtensions {
		if strings.HasSuffix(lower, extension) {
			return true
		}
	}
	return false
}

// ValidItemImageRemoteURL accepts absolute HTTPS URLs without credentials.
// Address policy (SSRF) is enforced by the fetcher, not here.
func ValidItemImageRemoteURL(value string) bool {
	if len(value) < len("https://x") || len(value) > ItemImageMaxURLBytes || !strings.HasPrefix(value, "https://") {
		return false
	}
	for _, r := range value {
		if r <= ' ' || r == 0x7f || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	// Hand-written authority and escape checks keep net/url out of the domain.
	for i := 0; i < len(value); i++ {
		if value[i] == '%' && (i+2 >= len(value) || !isHexDigit(value[i+1]) || !isHexDigit(value[i+2])) {
			return false
		}
	}
	host, _, _ := strings.Cut(value[len("https://"):], "/")
	host, _, _ = strings.Cut(host, "?")
	host, _, _ = strings.Cut(host, "#")
	if host == "" || strings.Contains(host, "@") {
		return false
	}
	if strings.HasPrefix(host, "[") {
		end := strings.IndexByte(host, ']')
		if end < 2 {
			return false
		}
		for _, c := range host[1:end] {
			if !isHexDigit(byte(c)) && c != ':' && c != '.' || c > 0x7f {
				return false
			}
		}
		return validItemImagePort(host[end+1:])
	}
	name, port, hasPort := strings.Cut(host, ":")
	if name == "" || hasPort && !validItemImagePort(":"+port) {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '%' || c > 0x7f) {
			return false
		}
	}
	return true
}

func validItemImagePort(value string) bool {
	if value == "" {
		return true
	}
	if len(value) < 2 || len(value) > 6 || value[0] != ':' {
		return false
	}
	for _, c := range value[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func validItemImageContent(value *ItemImageContent) bool {
	if value == nil {
		return true
	}
	return len(value.SHA256) == 32 && itemImageFormats[value.Format] && value.Bytes > 0 && !value.FetchedAt.IsZero() &&
		(value.Width == 0) == (value.Height == 0) && value.Width >= 0 && value.Width <= ItemImageMaxDimension &&
		value.Height >= 0 && value.Height <= ItemImageMaxDimension &&
		(value.AverageColor == nil || *value.AverageColor >= 0 && *value.AverageColor <= ItemImageMaxAverageColor)
}

// ValidItemImageInput checks the same source-kind consistency as the schema
// so invalid observations fail before a transaction is opened.
func ValidItemImageInput(value ItemImageInput) bool {
	if !ValidID(value.ItemID) || !ValidItemImageSlot(value.Type, value.Index) || !ValidItemImageSourceKind(value.SourceKind) ||
		!validItemImageContent(value.Content) || value.Locked && !value.Manual {
		return false
	}
	root := value.RootID != "" || value.RelativePath != ""
	if root && (!ValidID(value.RootID) || !ValidItemImageRelativePath(value.RelativePath)) {
		return false
	}
	if value.RemoteURL != "" && !ValidItemImageRemoteURL(value.RemoteURL) {
		return false
	}
	if (value.SourceModifiedUnixNano == nil) != (value.SourceSize == nil) || value.SourceSize != nil && (*value.SourceSize < 0 || !root) {
		return false
	}
	remote := value.RemoteURL != ""
	switch value.SourceKind {
	case ImageSourceLocal:
		return root && !remote && validItemImageFileName(value.RelativePath)
	case ImageSourceEmbedded:
		return root && !remote
	case ImageSourceRemote:
		return remote && !root
	default:
		return root != remote && (!root || validItemImageFileName(value.RelativePath))
	}
}

// ValidImageVariantLimit bounds one eviction page.
func ValidImageVariantLimit(limit int) bool { return limit >= 1 && limit <= ItemImageVariantListLimit }
