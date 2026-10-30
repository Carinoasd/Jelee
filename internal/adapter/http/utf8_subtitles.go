package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// utf8SubtitleRoute serves the UTF-8 copy of an external text subtitle
// (G15.4). It exists with the subtitle cache (matroska.enableExtraction),
// shares the production guard, the native-only rule and the direct
// delivery path of the external track routes, and takes no query.
const utf8SubtitleRoute = "/api/v1/sources/{id}/subtitles/{trackId}/utf8"

// WithUTF8Subtitles wires the derived UTF-8 subtitle copies.
func WithUTF8Subtitles(resolver media.UTF8Resolver) Option {
	return func(s *Server) { s.utf8 = resolver }
}

func utf8SubtitleURL(sourceID, trackID string) string {
	return trackURL(sourceID, domain.SidecarKindSubtitle, trackID) + "/utf8"
}

func (s *Server) utf8Subtitle(w http.ResponseWriter, r *http.Request) {
	if err := media.GuardProduction(r); err != nil {
		WriteError(w, r, err)
		return
	}
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	id, trackID := chi.URLParam(r, "id"), chi.URLParam(r, "trackId")
	if !domain.ValidID(id) || !domain.ValidID(trackID) {
		WriteError(w, r, domain.ErrNotFound)
		return
	}
	s.delivery.ServeUTF8Track(w, r, s.utf8Resolver(), id, trackID, false)
}

// utf8Resolver applies the hidden content status opt-in like the other
// source lookups.
func (s *Server) utf8Resolver() media.UTF8Resolver {
	if s.cfg.Access.HiddenContentStatus() == http.StatusForbidden {
		return hiddenUTF8{s.utf8}
	}
	return s.utf8
}

// utf8Available asks a resolver that reports whether copies can be made.
func utf8Available(resolver media.UTF8Resolver) bool {
	if resolver == nil {
		return false
	}
	if available, ok := resolver.(interface{ Available() bool }); ok {
		return available.Available()
	}
	return true
}

// unavailableUTF8 stands in when the subtitle cache is configured but no
// resolver was wired: every track has no copy.
type unavailableUTF8 struct{}

func (unavailableUTF8) Available() bool { return false }

func (unavailableUTF8) ResolveUTF8Track(context.Context, access.Principal, string, string) (media.Source, error) {
	return media.Source{}, media.ErrNotFound
}

type hiddenUTF8 struct{ media.UTF8Resolver }

func (h hiddenUTF8) Available() bool { return utf8Available(h.UTF8Resolver) }

func (h hiddenUTF8) ResolveUTF8Track(ctx context.Context, p access.Principal, sourceID, trackID string) (media.Source, error) {
	source, err := h.UTF8Resolver.ResolveUTF8Track(ctx, p, sourceID, trackID)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, media.ErrNotFound) {
		return media.Source{}, domain.ErrForbidden
	}
	return source, err
}

// decorateUTF8 offers the UTF-8 copy of every external text subtitle whose
// recorded charset is another one. The original stays the default url.
// Whether the copy can really be made is only known on first request:
// conversion that disproves the charset withholds it (404) and logs why.
func (s *Server) decorateUTF8(source *domain.PlaybackSource) {
	if !utf8Available(s.utf8) {
		return
	}
	for i := range source.External {
		if track := &source.External[i]; track.CharsetState == domain.SidecarCharsetConvertible {
			track.UTF8URL = utf8SubtitleURL(source.ID, track.ID)
		}
	}
}

// utf8Specification documents the UTF-8 copy route.
func utf8Specification(paths map[string]any) {
	op := operation("Read the UTF-8 copy of an external text subtitle", "200", "206", "403", "404", "408", "409", "416", "503")
	op["description"] = "Native sessions only; web sessions get 403 web_playback_disabled. trackId is an external subtitle listed with charsetState convertible and a utf8Url under externalTracks of GET /api/v1/items/{id}/playback. On first use the subtitle text is decoded from its recorded charset and written as UTF-8 (no byte order mark, line endings kept) into a rebuildable cache below the subtitle cache root (G15.4); the original file is never changed and stays the default url, byte for byte. This is a character set conversion of subtitle text, not a media conversion. The charset is detected again on the current bytes first: when it is no longer confidently the recorded one, when more than 1% of the input bytes do not decode, when the format declares its own encoding (TTML, DFXP) or the charset is unknown or already UTF-8, there is no copy and the answer is that of a missing track; the reason is logged with the track ID only. A changed original gets a new copy. Content-Type is the format's fixed type with charset=UTF-8. Range, HEAD, conditional requests, playback and bandwidth limits and revocation behave as for /api/v1/sources/{id}/stream. Responses carry X-Content-Type-Options: nosniff and a sandbox Content-Security-Policy."
	op["security"] = []any{map[string]any{"bearer": []string{}}}
	op["x-jelee-session"] = "native"
	op["parameters"] = []any{idParameter(), map[string]any{"name": "trackId", "in": "path", "required": true, "schema": map[string]any{"type": "string", "format": "uuid"}}, map[string]any{"name": "Range", "in": "header", "schema": map[string]any{"type": "string"}}, map[string]any{"name": "If-Range", "in": "header", "schema": map[string]any{"type": "string"}}}
	paths[utf8SubtitleRoute] = map[string]any{"get": op, "head": op}
}
