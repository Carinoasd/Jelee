package httpapi

import (
	"context"
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// maxPlaybackReportBody bounds a playback report; the members are a few
// identifiers and numbers.
const maxPlaybackReportBody = 4 << 10

// playbackReportInput is the body of the native report routes. A client
// either sends the playSessionId it got from start or, without one, the
// itemId: the session is then one per authenticated session and item.
type playbackReportInput struct {
	PlaySessionID string `json:"playSessionId"`
	ItemID        string `json:"itemId"`
	SourceID      string `json:"sourceId"`
	PositionTicks *int64 `json:"positionTicks"`
	Paused        bool   `json:"paused"`
	Failed        bool   `json:"failed"`
	FailureReason string `json:"failureReason"`
}

func (s *Server) progressRoutes(r chi.Router) {
	r.Post("/api/v1/playback/start", s.playbackReport(domain.PlaybackReportStart))
	r.Post("/api/v1/playback/progress", s.playbackReport(domain.PlaybackReportProgress))
	r.Post("/api/v1/playback/stop", s.playbackReport(domain.PlaybackReportStop))
	r.Get("/api/v1/playback/sessions", s.accountEndpoint(true, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		return memoryList(r, "/api/v1/playback/sessions", func() (any, error) { return s.catalog.ActivePlayback(r.Context(), a) })
	}))
	r.Get("/api/v1/items/{id}/user-data", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		id := chi.URLParam(r, "id")
		if !domain.ValidID(id) {
			return nil, 0, s.hiddenContentError(domain.ErrNotFound)
		}
		data, err := s.catalog.UserItemData(r.Context(), a.UserID, []string{id})
		if err != nil {
			return nil, 0, err
		}
		d, ok := data[id]
		if !ok {
			return nil, 0, s.hiddenContentError(domain.ErrNotFound)
		}
		return d, 200, nil
	}))
	for method, played := range map[string]bool{http.MethodPut: true, http.MethodDelete: false} {
		r.Method(method, "/api/v1/items/{id}/played", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			d, err := s.catalog.SetPlayed(r.Context(), a.UserID, chi.URLParam(r, "id"), played, nil)
			if err != nil {
				return nil, 0, s.hiddenContentError(err)
			}
			return d, 200, nil
		}))
	}
	r.Get("/api/v1/users/me/resume", s.accountEndpoint(false, true, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		list, err := listQuery(r, "/api/v1/users/me/resume")
		if err != nil {
			return nil, 0, err
		}
		q := domain.ResumeQuery{Offset: list.Offset, Limit: list.Limit}
		page, err := s.catalog.Resume(r.Context(), a.UserID, q)
		if err != nil {
			return nil, 0, err
		}
		items := make([]map[string]any, 0, len(page.Items))
		for _, e := range page.Items {
			entry := map[string]any{"id": e.Item.ID, "libraryId": e.Item.LibraryID, "kind": e.Item.Kind, "title": e.Item.Title, "userData": e.Data}
			if e.Item.ParentID != "" && e.Item.ParentID != e.Item.LibraryID {
				entry["parentId"] = e.Item.ParentID
			}
			items = append(items, entry)
		}
		next := ""
		if q.Offset+len(items) < page.Total && len(items) > 0 {
			next = offsetCursor(q.Offset + len(items))
		}
		return listData(list, map[string]any{"items": items, "total": page.Total, "offset": q.Offset, "limit": q.Limit, "nextCursor": next})
	}))
	r.Delete("/api/v1/users/me/playback-history", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		return nil, 204, s.catalog.ClearPlaybackHistory(r.Context(), a)
	}))
}

// playbackReport handles the native report routes. Like playback
// information they are native-only (web sessions get 403
// web_playback_disabled) and pass the production guard first.
func (s *Server) playbackReport(kind domain.PlaybackReportKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := s.playbackActor(w, r)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
		defer cancel()
		var input playbackReportInput
		if err := DecodeJSON(w, r.WithContext(ctx), &input, maxPlaybackReportBody); err != nil {
			WriteError(w, r, err)
			return
		}
		report := domain.PlaybackReport{Kind: kind, PlayKey: input.PlaySessionID, ItemID: input.ItemID, SourceID: input.SourceID,
			Paused: input.Paused, Failed: input.Failed, FailureReason: input.FailureReason}
		if input.PositionTicks != nil {
			report.PositionTicks, report.PositionKnown = *input.PositionTicks, true
		}
		if kind == domain.PlaybackReportStart && input.ItemID == "" || input.PlaySessionID == "" && !domain.ValidID(input.ItemID) {
			WriteError(w, r, domain.ErrInvalid)
			return
		}
		if report.PlayKey == "" {
			report.PlayKey = domain.DerivedPlayKey(actor.SessionID, input.ItemID)
		}
		if err := s.catalog.ReportPlayback(ctx, actor, report); err != nil {
			WriteError(w, r, s.hiddenContentError(err))
			return
		}
		if kind != domain.PlaybackReportStart {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"playSessionId": report.PlayKey, "itemId": input.ItemID,
			"reportIntervalSeconds": int(s.catalog.PlaybackReportInterval().Seconds())}})
	}
}
