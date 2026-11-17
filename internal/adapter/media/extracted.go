package media

import (
	"context"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ExtractedKind names an item copied out of a Matroska source.
type ExtractedKind string

const (
	// ExtractedSubtitle is an embedded text or PGS subtitle by probe stream
	// index. A PGS track is the raw .sup stream as Matroska stores it.
	ExtractedSubtitle ExtractedKind = "subtitle"
	// ExtractedVobSubIndex is the .idx text index of an embedded VobSub
	// track by probe stream index.
	ExtractedVobSubIndex ExtractedKind = "vobsub_index"
	// ExtractedVobSubData is the .sub MPEG program stream mkvextract writes
	// next to the .idx of the same VobSub track.
	ExtractedVobSubData ExtractedKind = "vobsub_data"
	// ExtractedAttachment is a font attachment by 1-based Matroska ID.
	ExtractedAttachment ExtractedKind = "attachment"
	// ExtractedAttachmentStream is a font attachment by probe stream index,
	// the numbering the compatibility layer uses.
	ExtractedAttachmentStream ExtractedKind = "attachment_stream"
	// ExtractedOCRSubtitle is the SubRip text subtitle OCR derived from a
	// bitmap subtitle (G15.6), by the bitmap track's probe stream index. It
	// is an additional track; the bitmap track itself is unchanged.
	ExtractedOCRSubtitle ExtractedKind = "ocr_subtitle"
)

func (k ExtractedKind) valid() bool {
	return k == ExtractedSubtitle || k == ExtractedAttachment || k == ExtractedAttachmentStream || k == ExtractedOCRSubtitle ||
		k == ExtractedVobSubIndex || k == ExtractedVobSubData
}

// bitmapExtensions are the cached file extensions of bitmap subtitle
// tracks; their bytes are not text in any charset.
var bitmapExtensions = map[string]bool{"sup": true, "idx": true, "sub": true}

// ExtractedResolver is implemented outside the delivery packages, which can
// never start a process (G10.11). It applies the same authorization as
// Resolver.Resolve to the source, makes sure the rebuildable cache holds the
// item (running the isolated mkvextract when needed, which copies bytes and
// never converts) and returns the cached file as a Source rooted at the
// cache. A missing, invisible or non-extractable item is ErrNotFound alike.
type ExtractedResolver interface {
	ResolveExtracted(ctx context.Context, principal access.Principal, sourceID string, kind ExtractedKind, index int) (Source, error)
}

// DefaultExtractTimeout bounds one lookup that may extract a whole source.
const DefaultExtractTimeout = 10 * time.Minute

// fontTypes is the fixed content type table of extracted font attachments.
var fontTypes = map[string]string{"ttf": "font/ttf", "otf": "font/otf", "ttc": "font/collection", "otc": "font/collection", "woff": "font/woff", "woff2": "font/woff2"}

// ExtractedContentType returns the content type of an extracted item from
// its cached file extension. Matroska text subtitles are UTF-8 by
// specification and mkvextract writes them as they are stored. Bitmap
// tracks get the external track types without a charset: application/x-pgs
// for .sup, text/plain for the VobSub .idx and application/octet-stream
// for its .sub.
func ExtractedContentType(kind ExtractedKind, extension string) string {
	ext := strings.ToLower(strings.TrimPrefix(extension, "."))
	if kind == ExtractedVobSubIndex || kind == ExtractedVobSubData || kind == ExtractedSubtitle && bitmapExtensions[ext] {
		return TrackContentType(TrackSubtitle, ext, "", domain.SidecarSubtitleVobSub)
	}
	if kind == ExtractedSubtitle || kind == ExtractedOCRSubtitle {
		return TrackContentType(TrackSubtitle, ext, "UTF-8", "")
	}
	if contentType, ok := fontTypes[ext]; ok {
		return contentType
	}
	return "application/octet-stream"
}

// ServeExtracted delivers one extracted embedded text or bitmap subtitle or
// font attachment as it was stored in the source (G15.5, G15.7), through the same
// production guard, native-only rule, ACL, limits, revocation and copy path
// as ServeTrack. Web sessions never receive it.
func (h *Handler) ServeExtracted(w http.ResponseWriter, r *http.Request, resolver ExtractedResolver, sourceID string, kind ExtractedKind, index int) {
	if resolver == nil || !kind.valid() || index < 0 {
		sourceID = ""
	}
	timeout := h.options.ExtractTimeout
	if timeout <= 0 {
		timeout = DefaultExtractTimeout
	}
	h.serveWithin(w, r, sourceID, timeout, func(ctx context.Context, principal access.Principal) (Source, error) {
		source, err := resolver.ResolveExtracted(ctx, principal, sourceID, kind, index)
		if err != nil {
			return Source{}, err
		}
		source.ETag = ""
		source.ContentType = ExtractedContentType(kind, path.Ext(source.RelativePath))
		return source, nil
	})
}
