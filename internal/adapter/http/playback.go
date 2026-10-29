package httpapi

import (
	"context"
	"net/http"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// maxPlaybackCheckBody bounds a capability declaration. Four lists of at
// most 32 short tokens fit well inside it.
const maxPlaybackCheckBody = 16 << 10

// playbackDelivery is the fixed delivery declaration of G10.4: original
// resources only, no conversion of any kind.
var playbackDelivery = map[string]any{"directPlay": true, "transcoding": false, "hls": false, "dash": false, "remux": false}

// playbackActor applies the playback surface rules in order: the production
// guard first (transformation parameters are 409 for every caller, also in a
// capability body), then native-only playback, then a query without fields.
func (s *Server) playbackActor(w http.ResponseWriter, r *http.Request) (domain.Actor, bool) {
	if err := media.GuardProduction(r); err != nil {
		WriteError(w, r, err)
		return domain.Actor{}, false
	}
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok {
		WriteError(w, r, domain.ErrUnauthenticated)
		return domain.Actor{}, false
	}
	if principal.Kind != access.ClientNative {
		WriteError(w, r, media.ErrPlaybackDenied)
		return domain.Actor{}, false
	}
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return domain.Actor{}, false
	}
	return domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID, IP: requestClientIP(r)}, true
}

func (s *Server) playbackInfo(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.playbackActor(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	defer cancel()
	id := chi.URLParam(r, "id")
	sources, selection, err := s.catalog.PlaybackChoice(ctx, actor, id, nil)
	if err != nil {
		WriteError(w, r, s.hiddenContentError(err))
		return
	}
	for i := range sources {
		for j := range sources[i].External {
			track := &sources[i].External[j]
			track.URL = trackURL(sources[i].ID, track.Kind, track.ID)
		}
		s.decorateExtracted(&sources[i])
		if principal, ok := access.PrincipalFromContext(r.Context()); ok {
			s.decorateOCR(ctx, principal, &sources[i])
		}
	}
	s.logSelection(r, w, selection)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"itemId": id, "delivery": playbackDelivery, "sources": sources, "selection": selection}})
}

// logSelection records the default version choice (G20.2) at debug level:
// only source IDs and fixed reason codes, never names or paths.
func (s *Server) logSelection(r *http.Request, w http.ResponseWriter, selection domain.VersionSelection) {
	s.logger.DebugContext(r.Context(), "default version chosen", "component", "playback", "requestId", w.Header().Get("X-Request-ID"),
		"sourceId", selection.DefaultSourceID, "reason", selection.Reason, "preferences", selection.Preferences, "excluded", len(selection.Excluded))
}

func (s *Server) playbackCheck(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.playbackActor(w, r)
	if !ok {
		return
	}
	var caps domain.ClientCapabilities
	if err := DecodeJSON(w, r, &caps, maxPlaybackCheckBody); err != nil {
		WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	defer cancel()
	id := chi.URLParam(r, "id")
	decisions, selection, err := s.catalog.CheckPlaybackChoice(ctx, actor, id, caps)
	if err != nil {
		WriteError(w, r, s.hiddenContentError(err))
		return
	}
	playable, unsupported := false, 0
	reasons := []string{}
	for i := range decisions {
		d := &decisions[i]
		for j := range d.Tracks {
			if track := &d.Tracks[j]; track.External {
				track.URL = trackURL(d.SourceID, track.Kind, track.ID)
			}
		}
		playable = playable || d.DirectPlay
		if !d.DirectPlay {
			unsupported++
		}
		for _, reason := range d.Reasons {
			if !slices.Contains(reasons, reason) {
				reasons = append(reasons, reason)
			}
		}
	}
	if unsupported > 0 {
		// G10.5: undecodable sources must be traceable in logs. Only fixed
		// reason codes and counts are logged, never names or paths.
		slices.Sort(reasons)
		s.logger.InfoContext(r.Context(), "direct play unsupported", "component", "playback", "requestId", w.Header().Get("X-Request-ID"),
			"sources", len(decisions), "unsupported", unsupported, "reasons", reasons)
	}
	s.logSelection(r, w, selection)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"itemId": id, "delivery": playbackDelivery, "directPlayable": playable, "decisions": decisions, "selection": selection}})
}
