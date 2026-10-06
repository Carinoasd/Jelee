package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Item listing, details and file information (G34.3).
//
// GET /api/v1/items keeps its original keyset form (cursor, limit) unchanged.
// Any browse parameter switches it to the offset form, which filters, sorts
// and reports a total: an ID cursor cannot continue a listing ordered by
// name or date, and a page view needs the total. Both forms answer with the
// same envelope; the offset form leaves nextCursor empty.

// itemsPageMax bounds one page in either form.
const itemsPageMax = 100

// itemsBrowseKeys are the parameters that select the offset form.
var itemsBrowseKeys = []string{"offset", "libraryId", "parentId", "type", "sort", "order", "q"}

var itemsSortKeys = map[string]domain.BrowseSortKey{
	"name": domain.BrowseSortName, "premiereDate": domain.BrowseSortPremiereDate, "productionYear": domain.BrowseSortProductionYear,
}

// parseItemsQuery reads the listing parameters. Every parameter may appear
// once except type, which may repeat; type and sort also take comma separated
// lists. browse is false for the original cursor form.
func parseItemsQuery(r *http.Request) (query domain.BrowseQuery, cursor string, browse bool, err error) {
	values, err := parseQuery(r.URL.RawQuery)
	if err != nil {
		return query, "", false, domain.ErrInvalid
	}
	for key, vs := range values {
		if key != "cursor" && key != "limit" && key != "fields" && !slices.Contains(itemsBrowseKeys, key) || len(vs) != 1 && key != "type" {
			return query, "", false, domain.ErrInvalid
		}
		browse = browse || slices.Contains(itemsBrowseKeys, key)
	}
	query.Limit = 50
	if raw := values.Get("limit"); raw != "" {
		// An empty limit keeps the default, as it always has.
		if query.Limit, err = strconv.Atoi(raw); err != nil || query.Limit < 1 || query.Limit > itemsPageMax {
			return query, "", false, domain.ErrInvalid
		}
	}
	if !browse {
		return query, values.Get("cursor"), false, nil
	}
	if _, ok := values["cursor"]; ok {
		// A keyset cursor cannot continue a filtered or sorted listing.
		return query, "", true, domain.ErrInvalid
	}
	if raw, ok := values["offset"]; ok {
		if query.Offset, err = strconv.Atoi(raw[0]); err != nil || query.Offset < 0 || query.Offset > domain.BrowseOffsetMax {
			return query, "", true, domain.ErrInvalid
		}
	}
	query.Scope = domain.BrowseAll
	if raw, ok := values["libraryId"]; ok {
		if !domain.ValidID(raw[0]) {
			return query, "", true, domain.ErrInvalid
		}
		query.LibraryID = raw[0]
	}
	if raw, ok := values["parentId"]; ok {
		if !domain.ValidID(raw[0]) {
			return query, "", true, domain.ErrInvalid
		}
		query.Scope, query.ParentID = domain.BrowseParent, raw[0]
	}
	for _, raw := range values["type"] {
		for kind := range strings.SplitSeq(raw, ",") {
			if !domain.BrowseItemKind(kind) {
				return query, "", true, domain.ErrInvalid
			}
			if !slices.Contains(query.Kinds, kind) {
				query.Kinds = append(query.Kinds, kind)
			}
		}
	}
	descending := false
	if raw, ok := values["order"]; ok {
		switch raw[0] {
		case "asc":
		case "desc":
			descending = true
		default:
			return query, "", true, domain.ErrInvalid
		}
	}
	sorts := []string{"name"}
	if raw, ok := values["sort"]; ok {
		sorts = strings.Split(raw[0], ",")
	}
	for _, name := range sorts {
		key, known := itemsSortKeys[name]
		if !known || slices.ContainsFunc(query.Sort, func(s domain.BrowseSort) bool { return s.Key == key }) {
			return query, "", true, domain.ErrInvalid
		}
		query.Sort = append(query.Sort, domain.BrowseSort{Key: key, Descending: descending})
	}
	query.SearchTerm = values.Get("q")
	if !domain.ValidBrowseQuery(query) {
		return query, "", true, domain.ErrInvalid
	}
	return query, "", true, nil
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	query, cursor, browse, err := parseItemsQuery(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	// Both forms share the unified field selection (G08.2); the forms keep
	// their own paging, sort and filter parsing above.
	list := listRequest{contract: listContracts["/api/v1/items"]}
	if raw, ok := r.URL.Query()["fields"]; ok {
		if list.Fields, err = list.contract.parseFields(raw[0]); err != nil {
			WriteError(w, r, err)
			return
		}
	}
	p, _ := access.PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	defer cancel()
	if browse {
		page, err := s.catalog.Browse(ctx, p.UserID, query)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items := make([]map[string]any, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, catalogItemJSON(item))
		}
		s.writeItemsPage(w, r, list, items, map[string]any{"nextCursor": "", "limit": query.Limit, "offset": query.Offset, "total": page.Total})
		return
	}
	items, err := s.catalog.List(ctx, p.UserID, cursor, query.Limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) == query.Limit {
		next = items[len(items)-1].ID
	}
	s.writeItemsPage(w, r, list, items, map[string]any{"nextCursor": next, "limit": query.Limit})
}

func (s *Server) writeItemsPage(w http.ResponseWriter, r *http.Request, list listRequest, items any, pagination map[string]any) {
	data, err := list.project(items)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": data, "pagination": pagination})
}

// catalogItemJSON writes a browse row in the CatalogItem shape. parentId is
// only present for a linked season or episode, as in the cursor form.
func catalogItemJSON(item domain.BrowseItem) map[string]any {
	out := map[string]any{"id": item.ID, "libraryId": item.LibraryID, "title": item.Title, "kind": item.Kind}
	if item.ParentID != "" && item.ParentID != item.LibraryID {
		out["parentId"] = item.ParentID
	}
	if item.PremiereDate != "" {
		out["premiereDate"] = item.PremiereDate
	}
	if item.Year > 0 {
		out["productionYear"] = item.Year
	}
	return out
}

func (s *Server) itemDetails(w http.ResponseWriter, r *http.Request) {
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	p, _ := access.PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	defer cancel()
	d, err := s.catalog.ItemDetails(ctx, p.UserID, chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, r, s.hiddenContentError(err))
		return
	}
	out := catalogItemJSON(d.BrowseItem)
	for key, value := range map[string]string{"sortTitle": d.SortTitle, "originalTitle": d.OriginalTitle, "tagline": d.Tagline, "overview": d.Overview} {
		if value != "" {
			out[key] = value
		}
	}
	ids := make([]map[string]any, 0, len(d.ExternalIDs))
	for _, id := range d.ExternalIDs {
		ids = append(ids, map[string]any{"type": id.Type, "value": id.Value, "default": id.Default})
	}
	out["genres"], out["externalIds"] = d.Genres, ids
	nfo := map[string]any{"status": d.NFO.Status, "fields": d.NFO.Fields}
	if !d.NFO.ReadAt.IsZero() {
		nfo["readAt"] = d.NFO.ReadAt.Format(time.RFC3339Nano)
	}
	out["nfo"] = nfo
	writeJSON(w, 200, map[string]any{"data": out})
}

// itemSources answers file information for every session kind. External
// tracks never carry a delivery URL here, so the response cannot be used to
// reach the native-only delivery routes.
func (s *Server) itemSources(w http.ResponseWriter, r *http.Request) {
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	p, _ := access.PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	defer cancel()
	id := chi.URLParam(r, "id")
	sources, err := s.catalog.ItemSources(ctx, domain.Actor{UserID: p.UserID, SessionID: p.SessionID, IP: requestClientIP(r)}, id)
	if err != nil {
		WriteError(w, r, s.hiddenContentError(err))
		return
	}
	for i := range sources {
		for j := range sources[i].External {
			sources[i].External[j].URL = ""
		}
	}
	writeJSON(w, 200, map[string]any{"data": map[string]any{"itemId": id, "sources": sources}})
}
