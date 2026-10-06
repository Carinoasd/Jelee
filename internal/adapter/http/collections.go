package httpapi

import (
	"net/http"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// collectionBodyLimit bounds collection and playlist bodies: a name, an
// overview of at most 16 KiB or up to 100 item IDs.
const collectionBodyLimit = 24 << 10

// collectionPageDefault is the page size without a limit query.
const collectionPageDefault = 50

// collectionRoutes registers collections and playlists (G02.1). Every
// member item is read through the unified visibility filter: hidden items
// are never listed, and naming one is answered like a missing item.
// Collection changes are administrator-only; playlists are changed by
// their owner only.
func (s *Server) collectionRoutes(r chi.Router) {
	r.Get("/api/v1/collections", s.accountEndpoint(false, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		cursor, limit, err := pageQuery(r)
		if err != nil {
			return nil, 0, err
		}
		page, err := s.catalog.ListCollections(r.Context(), a, cursor, limit)
		return page, http.StatusOK, err
	}))
	r.Post("/api/v1/collections", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		input, err := decodeCollectionInput(w, r)
		if err != nil {
			return nil, 0, err
		}
		view, err := s.catalog.CreateCollection(r.Context(), a, input)
		return view, http.StatusCreated, err
	}))
	r.Post("/api/v1/collections/nfo-sync", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		result, err := s.catalog.SyncNFOCollections(r.Context(), a)
		return result, http.StatusOK, err
	}))
	r.Get("/api/v1/collections/{id}", s.accountEndpoint(false, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		view, err := s.catalog.Collection(r.Context(), a, chi.URLParam(r, "id"))
		return view, http.StatusOK, s.hiddenContentError(err)
	}))
	r.Put("/api/v1/collections/{id}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		input, err := decodeCollectionInput(w, r)
		if err != nil {
			return nil, 0, err
		}
		view, err := s.catalog.UpdateCollection(r.Context(), a, chi.URLParam(r, "id"), input)
		return view, http.StatusOK, s.hiddenContentError(err)
	}))
	r.Delete("/api/v1/collections/{id}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		return nil, http.StatusNoContent, s.hiddenContentError(s.catalog.DeleteCollection(r.Context(), a, chi.URLParam(r, "id")))
	}))
	r.Post("/api/v1/collections/{id}/items", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		ids, err := decodeItemIDs(w, r)
		if err != nil {
			return nil, 0, err
		}
		view, err := s.catalog.AddCollectionItems(r.Context(), a, chi.URLParam(r, "id"), ids)
		return view, http.StatusOK, s.hiddenContentError(err)
	}))
	r.Delete("/api/v1/collections/{id}/items/{itemId}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		view, err := s.catalog.RemoveCollectionItem(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "itemId"))
		return view, http.StatusOK, s.hiddenContentError(err)
	}))

	r.Get("/api/v1/playlists", s.accountEndpoint(false, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		cursor, limit, err := pageQuery(r)
		if err != nil {
			return nil, 0, err
		}
		page, err := s.catalog.ListPlaylists(r.Context(), a, cursor, limit)
		return page, http.StatusOK, err
	}))
	r.Post("/api/v1/playlists", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input domain.PlaylistInput
		if err := DecodeJSON(w, r, &input, collectionBodyLimit); err != nil {
			return nil, 0, err
		}
		view, err := s.catalog.CreatePlaylist(r.Context(), a, input)
		return view, http.StatusCreated, err
	}))
	r.Get("/api/v1/playlists/{id}", s.accountEndpoint(false, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		view, err := s.catalog.Playlist(r.Context(), a, chi.URLParam(r, "id"))
		return view, http.StatusOK, s.hiddenContentError(err)
	}))
	r.Put("/api/v1/playlists/{id}", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input domain.PlaylistInput
		if err := DecodeJSON(w, r, &input, collectionBodyLimit); err != nil {
			return nil, 0, err
		}
		view, err := s.catalog.UpdatePlaylist(r.Context(), a, chi.URLParam(r, "id"), input)
		return view, http.StatusOK, s.hiddenContentError(err)
	}))
	r.Delete("/api/v1/playlists/{id}", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		return nil, http.StatusNoContent, s.hiddenContentError(s.catalog.DeletePlaylist(r.Context(), a, chi.URLParam(r, "id")))
	}))
	r.Post("/api/v1/playlists/{id}/items", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		ids, err := decodeItemIDs(w, r)
		if err != nil {
			return nil, 0, err
		}
		view, err := s.catalog.AddPlaylistItems(r.Context(), a, chi.URLParam(r, "id"), ids)
		return view, http.StatusOK, s.hiddenContentError(err)
	}))
	r.Delete("/api/v1/playlists/{id}/entries/{entryId}", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		view, err := s.catalog.RemovePlaylistEntry(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "entryId"))
		return view, http.StatusOK, s.hiddenContentError(err)
	}))
	r.Post("/api/v1/playlists/{id}/entries/{entryId}/move", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			BeforeEntryID *string `json:"beforeEntryId"`
		}
		if err := decodeJSONNullable(w, r, &input, collectionBodyLimit, func(path string) bool { return path == "/BEFOREENTRYID" }); err != nil {
			return nil, 0, err
		}
		before := ""
		if input.BeforeEntryID != nil {
			if *input.BeforeEntryID == "" {
				return nil, 0, domain.ErrInvalid
			}
			before = *input.BeforeEntryID
		}
		view, err := s.catalog.MovePlaylistEntry(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "entryId"), before)
		return view, http.StatusOK, s.hiddenContentError(err)
	}))
}

// pageQuery reads the cursor and limit of a collection or playlist listing.
func pageQuery(r *http.Request) (string, int, error) {
	query, err := strictQuery(r, "cursor", "limit")
	if err != nil {
		return "", 0, err
	}
	limit := collectionPageDefault
	if raw, ok := query["limit"]; ok {
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 || limit > domain.CollectionPageMax {
			return "", 0, domain.ErrInvalid
		}
	}
	cursor, ok := query["cursor"]
	if ok && !domain.ValidID(cursor) {
		return "", 0, domain.ErrInvalid
	}
	return cursor, limit, nil
}

func decodeCollectionInput(w http.ResponseWriter, r *http.Request) (domain.CollectionInput, error) {
	var input domain.CollectionInput
	err := decodeJSONNullable(w, r, &input, collectionBodyLimit, func(path string) bool { return path == "/NFONAME" })
	return input, err
}

func decodeItemIDs(w http.ResponseWriter, r *http.Request) ([]string, error) {
	var input struct {
		ItemIDs []string `json:"itemIds"`
	}
	if err := DecodeJSON(w, r, &input, collectionBodyLimit); err != nil {
		return nil, err
	}
	return input.ItemIDs, nil
}
