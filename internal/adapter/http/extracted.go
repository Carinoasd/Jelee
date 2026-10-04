package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Embedded item routes (G15.5, G15.7). They exist only when an extraction
// resolver is wired (mkvtoolnix installed and enabled) and share the
// production guard, the native-only rule and the whole direct delivery path
// of the external track routes. Web sessions never receive the bytes.
const (
	embeddedSubtitleRoute = "/api/v1/sources/{id}/embedded-subtitles/{index}"
	attachmentRoute       = "/api/v1/sources/{id}/attachments/{attachmentId}"
)

// maxExtractedIndex bounds the numeric path segments; Matroska stream
// indices and attachment IDs are far smaller.
const maxExtractedIndex = 4096

// WithExtracted wires the embedded subtitle and attachment extraction.
func WithExtracted(resolver media.ExtractedResolver) Option {
	return func(s *Server) { s.extracted = resolver }
}

func embeddedSubtitleURL(sourceID string, index int) string {
	return "/api/v1/sources/" + sourceID + "/embedded-subtitles/" + strconv.Itoa(index)
}

func attachmentURL(sourceID string, id int) string {
	return "/api/v1/sources/" + sourceID + "/attachments/" + strconv.Itoa(id)
}

// parseExtractedIndex accepts a canonical decimal: no sign, no leading zero.
func parseExtractedIndex(value string) (int, bool) {
	if value == "" || len(value) > 4 || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	index, err := strconv.Atoi(value)
	return index, err == nil && index <= maxExtractedIndex
}

func (s *Server) extractedRoute(kind media.ExtractedKind, param string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := media.GuardProduction(r); err != nil {
			WriteError(w, r, err)
			return
		}
		if _, err := strictQuery(r); err != nil {
			WriteError(w, r, err)
			return
		}
		id := chi.URLParam(r, "id")
		index, ok := parseExtractedIndex(chi.URLParam(r, param))
		if !domain.ValidID(id) || !ok || kind == media.ExtractedAttachment && index < 1 {
			WriteError(w, r, domain.ErrNotFound)
			return
		}
		s.delivery.ServeExtracted(w, r, s.extractedResolver(), id, kind, index)
	}
}

// extractedResolver applies the hidden content status opt-in like the
// other source lookups.
func (s *Server) extractedResolver() media.ExtractedResolver {
	if s.cfg.Access.HiddenContentStatus() == http.StatusForbidden {
		return hiddenExtracted{s.extracted}
	}
	return s.extracted
}

// unavailableExtracted stands in when extraction is configured but no
// runtime resolver was wired.
type unavailableExtracted struct{}

func (unavailableExtracted) Available() bool { return false }

func (unavailableExtracted) ResolveExtracted(context.Context, access.Principal, string, media.ExtractedKind, int) (media.Source, error) {
	return media.Source{}, media.ErrNotFound
}

type hiddenExtracted struct{ media.ExtractedResolver }

func (h hiddenExtracted) Available() bool { return extractionAvailable(h.ExtractedResolver) }

// extractionAvailable asks a resolver that reports its runtime state.
func extractionAvailable(resolver media.ExtractedResolver) bool {
	if resolver == nil {
		return false
	}
	if available, ok := resolver.(interface{ Available() bool }); ok {
		return available.Available()
	}
	return true
}

func (h hiddenExtracted) ResolveExtracted(ctx context.Context, p access.Principal, sourceID string, kind media.ExtractedKind, index int) (media.Source, error) {
	source, err := h.ExtractedResolver.ResolveExtracted(ctx, p, sourceID, kind, index)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, media.ErrNotFound) {
		return media.Source{}, domain.ErrForbidden
	}
	return source, err
}

// decorateExtracted adds the delivery URLs of extractable embedded text
// subtitles and font attachments when extraction is wired.
func (s *Server) decorateExtracted(source *domain.PlaybackSource) {
	if !extractionAvailable(s.extracted) {
		return
	}
	for i := range source.Subtitles {
		if track := &source.Subtitles[i]; track.Extractable {
			track.URL = embeddedSubtitleURL(source.ID, track.Index)
		}
	}
	for i := range source.Attachments {
		if attachment := &source.Attachments[i]; attachment.Font {
			attachment.URL = attachmentURL(source.ID, attachment.ID)
		}
	}
}

// extractedSpecification documents the embedded item routes.
func extractedSpecification(paths map[string]any) {
	for route, item := range map[string]struct{ summary, param, description string }{
		embeddedSubtitleRoute: {"Read an embedded text subtitle copied out of a Matroska source", "index",
			"The probe stream index of an embedded SubRip, ASS, SSA or WebVTT (S_TEXT/WEBVTT) subtitle listed with extractable true and a url under subtitleTracks of GET /api/v1/items/{id}/playback. On first use the isolated mkvextract copies the track, unconverted, into a rebuildable cache (G15.5); Content-Type is the format's fixed type with charset=UTF-8, as Matroska stores text subtitles. Bitmap subtitles and D_WEBVTT tracks are not extracted."},
		attachmentRoute: {"Read a font attachment copied out of a Matroska source", "attachmentId",
			"The 1-based Matroska attachment ID of a font listed with a url under attachments of GET /api/v1/items/{id}/playback. On first use the isolated mkvextract copies the font, unconverted, into a rebuildable cache (G15.7); clients use it to render ASS subtitles themselves. Content-Type comes from a fixed table keyed by the font extension."},
	} {
		op := operation(item.summary, "200", "206", "403", "404", "408", "409", "416", "503")
		op["description"] = "Native sessions only; web sessions get 403 web_playback_disabled. " + item.description + " Nothing is rendered, burned in, re-encoded or remuxed, and the original file is never written. A missing, invisible or non-extractable item, or one whose tool is not installed, is answered like a missing source. Range, HEAD, conditional requests, playback and bandwidth limits and revocation behave as for /api/v1/sources/{id}/stream. Responses carry X-Content-Type-Options: nosniff and a sandbox Content-Security-Policy."
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["x-jelee-session"] = "native"
		op["parameters"] = []any{idParameter(), map[string]any{"name": item.param, "in": "path", "required": true, "schema": map[string]any{"type": "integer", "minimum": 0, "maximum": maxExtractedIndex}}, map[string]any{"name": "Range", "in": "header", "schema": map[string]any{"type": "string"}}, map[string]any{"name": "If-Range", "in": "header", "schema": map[string]any{"type": "string"}}}
		paths[route] = map[string]any{"get": op, "head": op}
	}
}
