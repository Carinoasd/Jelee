package compat

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Catalog is the server's own catalog service. The library module only
// adapts its results: every read is evaluated for one user and applies the
// library grants in storage, so the layer has no authorization of its own
// beyond deciding which user a request reads as.
type Catalog interface {
	LibraryViews(ctx context.Context, userID string) ([]domain.LibraryView, error)
	Browse(ctx context.Context, userID string, query domain.BrowseQuery) (domain.BrowsePage, error)
	BrowseItem(ctx context.Context, userID, id string) (domain.BrowseItem, error)
	PlaybackSources(ctx context.Context, actor domain.Actor, itemID string) ([]domain.PlaybackSource, error)
}

// LibraryOptions connects the library module to the server's catalog.
type LibraryOptions struct {
	Catalog Catalog
	// HiddenStatus answers a missing or invisible item: 404, or 403 when the
	// server is configured to (G48.3). Both cases always look the same.
	HiddenStatus int
	// DirectPlay reports whether the server delivers original resources
	// (direct delivery enabled). Nothing is ever converted.
	DirectPlay bool
	// ClientIP returns the client address as the server derived it.
	ClientIP func(*http.Request) string
}

func (o *LibraryOptions) valid() bool {
	return o.Catalog != nil && o.ClientIP != nil && (o.HiddenStatus == http.StatusNotFound || o.HiddenStatus == http.StatusForbidden)
}

// Listing bounds. Upstream has no page bound; here a missing or larger
// Limit is clamped and TotalRecordCount tells clients to page.
const (
	itemsPageMax = domain.BrowseLimitMax
	// itemIDsMax bounds an Ids lookup.
	itemIDsMax = 100
)

// queryResult mirrors the upstream QueryResult.
type queryResult struct {
	Items            []baseItemDto `json:"Items"`
	TotalRecordCount int           `json:"TotalRecordCount"`
	StartIndex       int           `json:"StartIndex"`
}

// baseItemDto carries the members Jelee has a source for. Null-able members
// without one (image tags beyond the empty maps, people, genres, provider
// identifiers, series links, child counts, dates the catalog does not keep)
// are omitted, which is the same as null on the wire. ImageTags and
// BackdropImageTags are empty because the layer has no image route yet.
type baseItemDto struct {
	Name              string            `json:"Name"`
	ServerID          string            `json:"ServerId"`
	ID                string            `json:"Id"`
	SortName          string            `json:"SortName,omitempty"`
	PremiereDate      string            `json:"PremiereDate,omitempty"`
	MediaSources      []mediaSourceInfo `json:"MediaSources,omitempty"`
	Overview          string            `json:"Overview,omitempty"`
	RunTimeTicks      *int64            `json:"RunTimeTicks,omitempty"`
	ProductionYear    int               `json:"ProductionYear,omitempty"`
	IsFolder          bool              `json:"IsFolder"`
	ParentID          string            `json:"ParentId,omitempty"`
	Type              string            `json:"Type"`
	UserData          userItemData      `json:"UserData"`
	CollectionType    string            `json:"CollectionType,omitempty"`
	ImageTags         map[string]string `json:"ImageTags"`
	BackdropImageTags []string          `json:"BackdropImageTags"`
	LocationType      string            `json:"LocationType"`
	MediaType         string            `json:"MediaType"`
}

// userItemData is the minimal user data: Jelee records no playback
// progress, play counts or favourites yet, so every item reads as unplayed.
type userItemData struct {
	PlaybackPositionTicks int64  `json:"PlaybackPositionTicks"`
	PlayCount             int    `json:"PlayCount"`
	IsFavorite            bool   `json:"IsFavorite"`
	Played                bool   `json:"Played"`
	Key                   string `json:"Key"`
	ItemID                string `json:"ItemId"`
}

// mediaSourceInfo describes one original resource for direct delivery only
// (G10.4). SupportsTranscoding is always false. SupportsDirectStream is
// false too: upstream clients use direct streaming to ask for a remuxed
// container, which this server never produces. Path, file names and every
// transcoding member are omitted.
type mediaSourceInfo struct {
	Protocol              string        `json:"Protocol"`
	ID                    string        `json:"Id"`
	Type                  string        `json:"Type"`
	Container             string        `json:"Container,omitempty"`
	Size                  *int64        `json:"Size,omitempty"`
	Name                  string        `json:"Name"`
	IsRemote              bool          `json:"IsRemote"`
	RunTimeTicks          *int64        `json:"RunTimeTicks,omitempty"`
	ReadAtNativeFramerate bool          `json:"ReadAtNativeFramerate"`
	IgnoreDts             bool          `json:"IgnoreDts"`
	IgnoreIndex           bool          `json:"IgnoreIndex"`
	GenPtsInput           bool          `json:"GenPtsInput"`
	SupportsTranscoding   bool          `json:"SupportsTranscoding"`
	SupportsDirectStream  bool          `json:"SupportsDirectStream"`
	SupportsDirectPlay    bool          `json:"SupportsDirectPlay"`
	IsInfiniteStream      bool          `json:"IsInfiniteStream"`
	RequiresOpening       bool          `json:"RequiresOpening"`
	RequiresClosing       bool          `json:"RequiresClosing"`
	RequiresLooping       bool          `json:"RequiresLooping"`
	SupportsProbing       bool          `json:"SupportsProbing"`
	MediaStreams          []mediaStream `json:"MediaStreams"`
	Bitrate               *int64        `json:"Bitrate,omitempty"`
	HasSegments           bool          `json:"HasSegments"`
}

// mediaStream is one embedded stream of a probed source. External tracks
// are not listed: the layer has no route to deliver them yet.
type mediaStream struct {
	Codec      string `json:"Codec,omitempty"`
	Language   string `json:"Language,omitempty"`
	BitRate    *int64 `json:"BitRate,omitempty"`
	Channels   *int64 `json:"Channels,omitempty"`
	SampleRate *int64 `json:"SampleRate,omitempty"`
	IsDefault  bool   `json:"IsDefault"`
	IsForced   bool   `json:"IsForced"`
	Height     *int64 `json:"Height,omitempty"`
	Width      *int64 `json:"Width,omitempty"`
	Profile    string `json:"Profile,omitempty"`
	Type       string `json:"Type"`
	Index      int    `json:"Index"`
	IsExternal bool   `json:"IsExternal"`
}

func (rt *router) libraryRoutes() {
	rt.handle(http.MethodGet, "/UserViews", true, rt.bounded(rt.userViews))
	rt.handle(http.MethodGet, "/Users/{id}/Views", true, rt.bounded(rt.userViews))
	rt.handle(http.MethodGet, "/Items", true, rt.bounded(rt.items))
	rt.handle(http.MethodGet, "/Users/{id}/Items", true, rt.bounded(rt.items))
	rt.handle(http.MethodGet, "/Items/{itemId}", true, rt.bounded(rt.itemByID))
	rt.handle(http.MethodGet, "/Users/{id}/Items/{itemId}", true, rt.bounded(rt.itemByID))
}

// bounded applies the request timeout to reads, writes and the work itself.
func (rt *router) bounded(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deadline := time.Now().Add(rt.opts.Timeout)
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(deadline)
		_ = controller.SetWriteDeadline(deadline)
		ctx, cancel := context.WithDeadline(r.Context(), deadline)
		defer cancel()
		h(w, r.WithContext(ctx))
	}
}

// browseQuery holds the query with lower-case keys: upstream binds query
// members case-insensitively. Unknown members are ignored like upstream.
type browseQuery map[string][]string

func readQuery(r *http.Request) browseQuery {
	q := browseQuery{}
	for key, values := range r.URL.Query() {
		lower := strings.ToLower(key)
		q[lower] = append(q[lower], values...)
	}
	return q
}

// get returns the first non-empty value; upstream treats an empty value as
// absent.
func (q browseQuery) get(name string) string {
	for _, v := range q[name] {
		if v != "" {
			return v
		}
	}
	return ""
}

// list splits every value on commas, dropping empty entries.
func (q browseQuery) list(name string) []string {
	var out []string
	for _, v := range q[name] {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// errBadQuery is answered with the generic 400.
var errBadQuery = errors.New("compat: bad query")

func (q browseQuery) boolean(name string) (value, set bool, err error) {
	raw := q.get(name)
	if raw == "" {
		return false, false, nil
	}
	switch {
	case strings.EqualFold(raw, "true"):
		return true, true, nil
	case strings.EqualFold(raw, "false"):
		return false, true, nil
	}
	return false, false, errBadQuery
}

func (q browseQuery) integer(name string) (value int, set bool, err error) {
	raw := q.get(name)
	if raw == "" {
		return 0, false, nil
	}
	v, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || v < 0 {
		return 0, false, errBadQuery
	}
	return int(v), true, nil
}

// identifier reports whether s looks like an upstream enum name. A name the
// layer does not support is ignored; anything else is a malformed request.
func identifier(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// libraryUser decides which user a request reads as. The {id} path member
// or the userId query member must name the caller, unless the caller is an
// administrator; otherwise 403 before any catalog read. An absent or
// all-zero identifier means the caller, as upstream treats an empty one.
func (rt *router) libraryUser(w http.ResponseWriter, r *http.Request, q browseQuery) (access.Principal, string, bool) {
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok || principal.Kind != access.ClientNative {
		writeError(w, http.StatusUnauthorized)
		return principal, "", false
	}
	raw := chi.URLParam(r, "id")
	if raw == "" {
		raw = q.get("userid")
	}
	if raw == "" || (len(raw) == 32 || len(raw) == 36) && isNilID(raw) {
		return principal, principal.UserID, true
	}
	id, err := ParseID(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return principal, "", false
	}
	if id != principal.UserID && !principal.Admin {
		writeError(w, http.StatusForbidden)
		return principal, "", false
	}
	return principal, id, true
}

func (rt *router) userViews(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	_, userID, ok := rt.libraryUser(w, r, q)
	if !ok {
		return
	}
	views, err := rt.opts.Library.Catalog.LibraryViews(r.Context(), userID)
	if err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	result := queryResult{Items: make([]baseItemDto, 0, len(views)), TotalRecordCount: len(views)}
	for _, view := range views {
		dto, err := rt.viewDto(view.ID, view.Name, view.ContentKinds)
		if err != nil {
			writeError(w, http.StatusInternalServerError)
			return
		}
		result.Items = append(result.Items, dto)
	}
	writeJSON(w, result)
}

// itemsRequest is a parsed listing request.
type itemsRequest struct {
	parentID string
	// recursive is nil when the client did not choose.
	recursive *bool
	// kinds are the catalog kinds asked for; views reports whether library
	// folders may be listed. typed is true when IncludeItemTypes was given.
	kinds  []string
	views  bool
	typed  bool
	search string
	sort   []domain.BrowseSort
	// nameDescending orders library folders, which only sort by name.
	nameDescending bool
	offset         int
	limit          int
	fields         map[string]bool
	ids            []string
}

func parseItemsRequest(q browseQuery) (itemsRequest, error) {
	req := itemsRequest{fields: map[string]bool{}, limit: itemsPageMax}
	if raw := q.get("parentid"); raw != "" {
		id, err := ParseID(raw)
		if err != nil {
			return req, errBadQuery
		}
		req.parentID = id
	}
	recursive, set, err := q.boolean("recursive")
	if err != nil {
		return req, err
	}
	if set {
		req.recursive = &recursive
	}
	include, includeViews, err := itemKinds(q.list("includeitemtypes"))
	if err != nil {
		return req, err
	}
	exclude, excludeViews, err := itemKinds(q.list("excludeitemtypes"))
	if err != nil {
		return req, err
	}
	req.typed = len(q.list("includeitemtypes")) > 0
	if !req.typed {
		include, includeViews = []string{"Movie", "Series", "Season", "Episode", "HomeVideo"}, true
	}
	for _, kind := range include {
		if !slices.Contains(exclude, kind) {
			req.kinds = append(req.kinds, kind)
		}
	}
	slices.Sort(req.kinds)
	req.views = includeViews && !excludeViews
	req.search = q.get("searchterm")
	if utf8.RuneCountInString(req.search) > domain.BrowseSearchMax || !utf8.ValidString(req.search) || strings.ContainsRune(req.search, 0) {
		return req, errBadQuery
	}
	var orders []bool
	for _, order := range q.list("sortorder") {
		switch strings.ToLower(order) {
		case sortOrderAscending:
			orders = append(orders, false)
		case sortOrderDescending:
			orders = append(orders, true)
		default:
			return req, errBadQuery
		}
	}
	// Like upstream, a key without its own order takes the first order
	// given, else ascending.
	for i, name := range q.list("sortby") {
		if !identifier(name) {
			return req, errBadQuery
		}
		descending := len(orders) > 0 && orders[0]
		if i < len(orders) {
			descending = orders[i]
		}
		var key domain.BrowseSortKey
		switch strings.ToLower(name) {
		case sortBySortName, sortByName:
			key = domain.BrowseSortName
		case sortByPremiereDate:
			key = domain.BrowseSortPremiereDate
		case sortByProductionYear:
			key = domain.BrowseSortProductionYear
		default:
			// Supported upstream but not here (DateCreated, Random, ...):
			// ignored, the remaining keys still apply.
			continue
		}
		if !slices.ContainsFunc(req.sort, func(s domain.BrowseSort) bool { return s.Key == key }) && len(req.sort) < domain.BrowseSortMax {
			req.sort = append(req.sort, domain.BrowseSort{Key: key, Descending: descending})
		}
	}
	if len(req.sort) > 0 && req.sort[0].Key == domain.BrowseSortName {
		req.nameDescending = req.sort[0].Descending
	}
	if req.offset, _, err = q.integer("startindex"); err != nil || req.offset > domain.BrowseOffsetMax {
		return req, errBadQuery
	}
	limit, set, err := q.integer("limit")
	if err != nil {
		return req, err
	}
	if set {
		req.limit = min(limit, itemsPageMax)
	}
	for _, field := range q.list("fields") {
		req.fields[strings.ToLower(field)] = true
	}
	if len(q.list("ids")) > itemIDsMax {
		return req, errBadQuery
	}
	for _, raw := range q.list("ids") {
		id, err := ParseID(raw)
		if err != nil {
			return req, errBadQuery
		}
		if !slices.Contains(req.ids, id) {
			req.ids = append(req.ids, id)
		}
	}
	return req, nil
}

// itemKinds maps upstream item types to catalog kinds. CollectionFolder
// names library folders. Well-formed types the catalog does not have (Audio,
// BoxSet, Folder, ...) match nothing.
func itemKinds(names []string) (kinds []string, views bool, err error) {
	for _, name := range names {
		if !identifier(name) {
			return nil, false, errBadQuery
		}
		if strings.EqualFold(name, itemTypeCollectionFolder) {
			views = true
			continue
		}
		for kind, wire := range itemTypeByKind {
			if strings.EqualFold(name, wire) && !slices.Contains(kinds, kind) {
				kinds = append(kinds, kind)
			}
		}
	}
	slices.Sort(kinds)
	return kinds, views, nil
}

func (rt *router) items(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	_, userID, ok := rt.libraryUser(w, r, q)
	if !ok {
		return
	}
	req, err := parseItemsRequest(q)
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	catalog := rt.opts.Library.Catalog
	ctx := r.Context()
	if len(req.ids) > 0 {
		rt.itemsByIDs(w, r, userID, req)
		return
	}
	recursive := req.recursive != nil && *req.recursive
	if req.parentID != "" && req.recursive == nil && req.typed {
		// Upstream lists a library's contents recursively when types are
		// given and the client did not choose.
		parent, err := catalog.BrowseItem(ctx, userID, req.parentID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			writeJSON(w, queryResult{Items: []baseItemDto{}, StartIndex: req.offset})
			return
		case err != nil:
			rt.writeLibraryError(w, err)
			return
		}
		recursive = parent.Kind == domain.BrowseKindLibrary
	}
	if req.parentID == "" && !recursive {
		rt.itemsAtRoot(ctx, w, userID, req)
		return
	}
	result := queryResult{Items: []baseItemDto{}, StartIndex: req.offset}
	if len(req.kinds) == 0 {
		// Only library folders or unsupported types were asked for: the
		// catalog has no matching item.
		writeJSON(w, result)
		return
	}
	query := domain.BrowseQuery{ParentID: req.parentID, SearchTerm: req.search, Sort: req.sort, Offset: req.offset, Limit: max(req.limit, 1), WithOverview: req.fields[fieldOverview]}
	if len(req.kinds) < len(itemTypeByKind) {
		query.Kinds = req.kinds
	}
	switch {
	case req.parentID == "":
		query.Scope = domain.BrowseAll
	case recursive:
		query.Scope = domain.BrowseParentRecursive
	default:
		query.Scope = domain.BrowseParent
	}
	page, err := catalog.Browse(ctx, userID, query)
	if err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	result.TotalRecordCount = page.Total
	if req.limit > 0 {
		for _, item := range page.Items {
			dto, err := rt.itemDto(item, req.fields)
			if err != nil {
				writeError(w, http.StatusInternalServerError)
				return
			}
			result.Items = append(result.Items, dto)
		}
	}
	writeJSON(w, result)
}

// itemsAtRoot answers a non-recursive listing without a parent: upstream
// lists the children of the user's root folder, which are the library
// folders. They are filtered by type and search term and sorted by name.
func (rt *router) itemsAtRoot(ctx context.Context, w http.ResponseWriter, userID string, req itemsRequest) {
	result := queryResult{Items: []baseItemDto{}, StartIndex: req.offset}
	if !req.views {
		writeJSON(w, result)
		return
	}
	views, err := rt.opts.Library.Catalog.LibraryViews(ctx, userID)
	if err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	needle := strings.ToLower(req.search)
	matched := views[:0:0]
	for _, view := range views {
		if strings.Contains(strings.ToLower(view.Name), needle) {
			matched = append(matched, view)
		}
	}
	slices.SortStableFunc(matched, func(a, b domain.LibraryView) int {
		if req.nameDescending {
			a, b = b, a
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	result.TotalRecordCount = len(matched)
	for i := req.offset; i < len(matched) && i < req.offset+req.limit; i++ {
		dto, err := rt.viewDto(matched[i].ID, matched[i].Name, matched[i].ContentKinds)
		if err != nil {
			writeError(w, http.StatusInternalServerError)
			return
		}
		result.Items = append(result.Items, dto)
	}
	writeJSON(w, result)
}

// itemsByIDs answers an Ids listing: the visible ones in the order asked,
// filtered by type. Invisible and missing identifiers are both left out.
func (rt *router) itemsByIDs(w http.ResponseWriter, r *http.Request, userID string, req itemsRequest) {
	result := queryResult{Items: []baseItemDto{}, StartIndex: req.offset}
	var found []domain.BrowseItem
	for _, id := range req.ids {
		item, err := rt.opts.Library.Catalog.BrowseItem(r.Context(), userID, id)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			rt.writeLibraryError(w, err)
			return
		}
		if item.Kind == domain.BrowseKindLibrary && req.views || slices.Contains(req.kinds, item.Kind) {
			found = append(found, item)
		}
	}
	result.TotalRecordCount = len(found)
	for i := req.offset; i < len(found) && i < req.offset+req.limit; i++ {
		dto, err := rt.browseDto(found[i], req.fields)
		if err != nil {
			writeError(w, http.StatusInternalServerError)
			return
		}
		result.Items = append(result.Items, dto)
	}
	writeJSON(w, result)
}

// itemByID answers one item or library folder with every member it has,
// as upstream answers a single item with all fields. Playable items carry
// their direct delivery sources.
func (rt *router) itemByID(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	principal, userID, ok := rt.libraryUser(w, r, q)
	if !ok {
		return
	}
	id, err := ParseID(chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	catalog := rt.opts.Library.Catalog
	item, err := catalog.BrowseItem(r.Context(), userID, id)
	if err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	all := map[string]bool{fieldOverview: true, fieldSortName: true, fieldParentID: true, fieldMediaSources: true}
	dto, err := rt.browseDto(item, all)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	if dto.MediaType == mediaTypeVideo {
		actor := domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID, IP: rt.opts.Library.ClientIP(r)}
		sources, err := catalog.PlaybackSources(r.Context(), actor, item.ID)
		if err != nil {
			rt.writeLibraryError(w, err)
			return
		}
		dto.MediaSources = make([]mediaSourceInfo, 0, len(sources))
		for _, source := range sources {
			info, err := rt.mediaSource(source, item.Title)
			if err != nil {
				writeError(w, http.StatusInternalServerError)
				return
			}
			dto.MediaSources = append(dto.MediaSources, info)
		}
		if len(sources) > 0 {
			dto.RunTimeTicks = ticks(sources[0].DurationMicros)
		}
	}
	writeJSON(w, dto)
}

func (rt *router) browseDto(item domain.BrowseItem, fields map[string]bool) (baseItemDto, error) {
	if item.Kind == domain.BrowseKindLibrary {
		return rt.viewDto(item.ID, item.Title, item.ContentKinds)
	}
	return rt.itemDto(item, fields)
}

// viewDto is a library folder. CollectionType is reported only when the
// library holds a single kind of content; a mixed or empty library has none.
func (rt *router) viewDto(libraryID, name string, contentKinds []string) (baseItemDto, error) {
	id, err := FormatID(libraryID)
	if err != nil {
		return baseItemDto{}, err
	}
	dto := rt.baseDto(id, name)
	dto.IsFolder = true
	dto.Type = itemTypeCollectionFolder
	dto.MediaType = mediaTypeUnknown
	switch {
	case slices.Equal(contentKinds, []string{"Movie"}):
		dto.CollectionType = collectionTypeMovies
	case len(contentKinds) > 0 && !slices.ContainsFunc(contentKinds, func(k string) bool { return k != "Series" && k != "Episode" }):
		dto.CollectionType = collectionTypeTvShows
	case slices.Equal(contentKinds, []string{"HomeVideo"}):
		dto.CollectionType = collectionTypeHomeVideos
	}
	return dto, nil
}

// itemDto maps one catalog item. Like upstream, Overview, SortName and
// ParentId are only sent when asked for in Fields; release date and year
// always are.
func (rt *router) itemDto(item domain.BrowseItem, fields map[string]bool) (baseItemDto, error) {
	id, err := FormatID(item.ID)
	if err != nil {
		return baseItemDto{}, err
	}
	itemType, ok := itemTypeByKind[item.Kind]
	if !ok {
		return baseItemDto{}, ErrInvalidID
	}
	dto := rt.baseDto(id, item.Title)
	dto.Type = itemType
	dto.IsFolder = item.Kind == "Series" || item.Kind == "Season"
	dto.MediaType = mediaTypeUnknown
	if !dto.IsFolder {
		dto.MediaType = mediaTypeVideo
	}
	if date, err := time.Parse(time.DateOnly, item.PremiereDate); err == nil {
		dto.PremiereDate = date.Format(wireTime)
	}
	dto.ProductionYear = item.Year
	if fields[fieldOverview] {
		dto.Overview = item.Overview
	}
	if fields[fieldSortName] {
		dto.SortName = strings.ToLower(item.Title)
		if item.SortTitle != "" {
			dto.SortName = strings.ToLower(item.SortTitle)
		}
	}
	if fields[fieldParentID] && item.ParentID != "" {
		if dto.ParentID, err = FormatID(item.ParentID); err != nil {
			return baseItemDto{}, err
		}
	}
	return dto, nil
}

func (rt *router) baseDto(id, name string) baseItemDto {
	return baseItemDto{
		Name:              name,
		ServerID:          rt.opts.ServerID,
		ID:                id,
		UserData:          userItemData{Key: id, ItemID: id},
		ImageTags:         map[string]string{},
		BackdropImageTags: []string{},
		LocationType:      locationFileSystem,
	}
}

func (rt *router) mediaSource(source domain.PlaybackSource, name string) (mediaSourceInfo, error) {
	id, err := FormatID(source.ID)
	if err != nil {
		return mediaSourceInfo{}, err
	}
	info := mediaSourceInfo{
		Protocol:           mediaProtocolFile,
		ID:                 id,
		Type:               mediaSourceDefault,
		Container:          source.Container,
		Size:               source.SizeBytes,
		Name:               name,
		RunTimeTicks:       ticks(source.DurationMicros),
		SupportsDirectPlay: rt.opts.Library.DirectPlay,
		MediaStreams:       []mediaStream{},
		Bitrate:            source.BitRate,
	}
	for _, v := range source.Video {
		info.MediaStreams = append(info.MediaStreams, mediaStream{Codec: v.Codec, BitRate: v.BitRate, IsDefault: v.Default, Height: v.Height, Width: v.Width, Profile: v.Profile, Type: mediaStreamVideo, Index: v.Index})
	}
	for _, a := range source.Audio {
		info.MediaStreams = append(info.MediaStreams, mediaStream{Codec: a.Codec, Language: a.Language, BitRate: a.BitRate, Channels: a.Channels, SampleRate: a.SampleRate, IsDefault: a.Default, IsForced: a.Forced, Profile: a.Profile, Type: mediaStreamAudio, Index: a.Index})
	}
	for _, s := range source.Subtitles {
		info.MediaStreams = append(info.MediaStreams, mediaStream{Codec: s.Format, Language: s.Language, IsDefault: s.Default, IsForced: s.Forced, Type: mediaStreamSubtitle, Index: s.Index})
	}
	return info, nil
}

// ticks converts microseconds to the upstream 100-nanosecond ticks.
func ticks(micros *int64) *int64 {
	if micros == nil || *micros < 0 || *micros > 1<<62/10 {
		return nil
	}
	v := *micros * 10
	return &v
}

// writeLibraryError maps catalog errors to the layer's error forms. A
// missing and an invisible item get the same configured status.
func (rt *router) writeLibraryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, rt.opts.Library.HiddenStatus)
	case errors.Is(err, domain.ErrInvalid):
		writeError(w, http.StatusBadRequest)
	case errors.Is(err, domain.ErrDatabase), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, http.StatusServiceUnavailable)
	default:
		writeError(w, http.StatusInternalServerError)
	}
}
