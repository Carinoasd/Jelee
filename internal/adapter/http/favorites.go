package httpapi

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// favoriteRoutes registers favourites (G24.2). Marking and unmarking take
// the item like the played routes and answer its user data; the list pages
// the caller's own favourites. Every read and change binds the item to the
// caller through the unified visibility filter: a hidden item is answered
// like a missing one and never listed.
func (s *Server) favoriteRoutes(r chi.Router) {
	for method, favorite := range map[string]bool{http.MethodPut: true, http.MethodDelete: false} {
		r.Method(method, "/api/v1/items/{id}/favorite", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			d, err := s.catalog.SetFavorite(r.Context(), a.UserID, chi.URLParam(r, "id"), favorite)
			if err != nil {
				return nil, 0, s.hiddenContentError(err)
			}
			return d, http.StatusOK, nil
		}))
	}
	r.Get("/api/v1/users/me/favorites", s.accountEndpoint(false, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		list, err := listQuery(r, "/api/v1/users/me/favorites")
		if err != nil {
			return nil, 0, err
		}
		q := domain.FavoriteQuery{Offset: list.Offset, Limit: list.Limit}
		if raw, ok := list.Filters["libraryId"]; ok {
			if !domain.ValidID(raw) {
				return nil, 0, domain.ErrInvalid
			}
			q.LibraryID = raw
		}
		if raw, ok := list.Filters["type"]; ok {
			for kind := range strings.SplitSeq(raw, ",") {
				if !domain.BrowseItemKind(kind) {
					return nil, 0, domain.ErrInvalid
				}
				if !slices.Contains(q.Kinds, kind) {
					q.Kinds = append(q.Kinds, kind)
				}
			}
		}
		page, err := s.catalog.Favorites(r.Context(), a.UserID, q)
		if err != nil {
			return nil, 0, err
		}
		items := make([]map[string]any, 0, len(page.Items))
		for _, e := range page.Items {
			entry := catalogItemJSON(e.Item)
			entry["favoritedAt"] = e.FavoritedAt.Format(time.RFC3339Nano)
			items = append(items, entry)
		}
		next := ""
		if q.Offset+len(items) < page.Total && len(items) > 0 {
			next = offsetCursor(q.Offset + len(items))
		}
		return listData(list, map[string]any{"items": items, "total": page.Total, "offset": q.Offset, "limit": q.Limit, "nextCursor": next})
	}))
}
