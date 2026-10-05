package compat

import (
	"context"
	"errors"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
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
	// Images is the server's image pipeline. Nil leaves the image routes
	// unregistered and every item without image tags.
	Images Images
	// Delivery is the server's direct delivery handler. Nil leaves the
	// stream and subtitle routes unregistered and external subtitles
	// unlisted; set it only with direct delivery enabled.
	Delivery Delivery
	// Extracted serves embedded text subtitles and font attachments copied
	// unconverted out of Matroska sources (G15.5, G15.7). Nil keeps embedded
	// subtitles Embed-only and lists no attachments; set it only with
	// Delivery.
	Extracted media.ExtractedResolver
	// Playstate is the server's progress service. Nil leaves the report,
	// played and resume routes unregistered and every item unplayed.
	Playstate Playstate
}

// Delivery is the server's one direct delivery handler (media.Handler):
// authorization, revocation, concurrency and bandwidth limits, Range and
// HEAD all happen there. The layer never streams on its own.
type Delivery interface {
	ServeSource(w http.ResponseWriter, r *http.Request, sourceID string)
	ServeTrack(w http.ResponseWriter, r *http.Request, sourceID string, kind media.TrackKind, trackID string)
	ServeExtracted(w http.ResponseWriter, r *http.Request, resolver media.ExtractedResolver, sourceID string, kind media.ExtractedKind, index int)
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
// BackdropImageTags list the item's image slots (see attachImages); they are
// null (omitted) when the request turns images off, as upstream.
type baseItemDto struct {
	Name           string            `json:"Name"`
	ServerID       string            `json:"ServerId"`
	ID             string            `json:"Id"`
	SortName       string            `json:"SortName,omitempty"`
	PremiereDate   string            `json:"PremiereDate,omitempty"`
	MediaSources   []mediaSourceInfo `json:"MediaSources,omitempty"`
	Overview       string            `json:"Overview,omitempty"`
	RunTimeTicks   *int64            `json:"RunTimeTicks,omitempty"`
	ProductionYear int               `json:"ProductionYear,omitempty"`
	IsFolder       bool              `json:"IsFolder"`
	ParentID       string            `json:"ParentId,omitempty"`
	Type           string            `json:"Type"`
	UserData       userItemData      `json:"UserData"`
	CollectionType string            `json:"CollectionType,omitempty"`
	// PrimaryImageAspectRatio is width over height of the primary image,
	// sent when asked for in Fields and the size is known.
	PrimaryImageAspectRatio *float64          `json:"PrimaryImageAspectRatio,omitempty"`
	ImageTags               map[string]string `json:"ImageTags,omitzero"`
	BackdropImageTags       []string          `json:"BackdropImageTags,omitzero"`
	LocationType            string            `json:"LocationType"`
	MediaType               string            `json:"MediaType"`
}

// mediaSourceInfo describes one original resource for direct delivery only
// (G10.4). SupportsTranscoding is always false. SupportsDirectStream is
// false too: upstream clients use direct streaming to ask for a remuxed
// container, which this server never produces. Path, file names and every
// transcoding member (TranscodingUrl, TranscodingContainer, ...) are omitted;
// the struct deliberately has no field for them.
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
	// DefaultAudioStreamIndex is the embedded audio stream the user's track
	// preferences pick (G16.5), else the default one, else the first one.
	DefaultAudioStreamIndex *int `json:"DefaultAudioStreamIndex,omitempty"`
	// DefaultSubtitleStreamIndex is the subtitle stream the preferences
	// pick, embedded or external, and -1 for none; absent without
	// preference information.
	DefaultSubtitleStreamIndex *int `json:"DefaultSubtitleStreamIndex,omitempty"`
	HasSegments                bool `json:"HasSegments"`
	// MediaAttachments lists Matroska attachments known from the MediaInfo
	// supplement, with a DeliveryUrl for fonts when extraction is wired.
	MediaAttachments []mediaAttachment `json:"MediaAttachments,omitempty"`
}

// mediaAttachment mirrors the upstream MediaAttachment. Index is the probe
// stream index of the attachment.
type mediaAttachment struct {
	Index       int    `json:"Index"`
	FileName    string `json:"FileName,omitempty"`
	MimeType    string `json:"MimeType,omitempty"`
	DeliveryURL string `json:"DeliveryUrl,omitempty"`
}

// mediaStream is one embedded stream of a probed source or, with direct
// delivery, one external subtitle file. External audio files are not
// listed: upstream has no route that delivers them.
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
	// Subtitle members, set only with direct delivery. DeliveryMethod is
	// Embed for a track inside the original file and External for a
	// sidecar file, whose DeliveryUrl serves it as it is.
	Title                  string `json:"Title,omitempty"`
	IsHearingImpaired      bool   `json:"IsHearingImpaired,omitempty"`
	IsTextSubtitleStream   bool   `json:"IsTextSubtitleStream,omitempty"`
	SupportsExternalStream bool   `json:"SupportsExternalStream,omitempty"`
	DeliveryMethod         string `json:"DeliveryMethod,omitempty"`
	DeliveryURL            string `json:"DeliveryUrl,omitempty"`
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
	raw := chi.URLParam(r, "id")
	if raw == "" {
		raw = q.get("userid")
	}
	return rt.readAs(w, r, raw)
}

// readAs applies the libraryUser rule to an identifier taken from the path,
// the query or a request body.
func (rt *router) readAs(w http.ResponseWriter, r *http.Request, raw string) (access.Principal, string, bool) {
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok || principal.Kind != access.ClientNative {
		writeError(w, http.StatusUnauthorized)
		return principal, "", false
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
	images         imageOptions
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
	if req.images, err = parseImageOptions(q); err != nil {
		return req, err
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
	var ids []string
	if req.limit > 0 {
		for _, item := range page.Items {
			dto, err := rt.itemDto(item, req.fields)
			if err != nil {
				writeError(w, http.StatusInternalServerError)
				return
			}
			result.Items = append(result.Items, dto)
			ids = append(ids, item.ID)
		}
	}
	if err := rt.attachUserData(ctx, userID, result.Items, ids); err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	if err := rt.attachImages(ctx, userID, result.Items, ids, req.images, req.fields[fieldPrimaryImageAspectRatio]); err != nil {
		rt.writeLibraryError(w, err)
		return
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
	var ids []string
	for i := req.offset; i < len(found) && i < req.offset+req.limit; i++ {
		dto, err := rt.browseDto(found[i], req.fields)
		if err != nil {
			writeError(w, http.StatusInternalServerError)
			return
		}
		result.Items = append(result.Items, dto)
		ids = append(ids, found[i].ID)
	}
	if err := rt.attachUserData(r.Context(), userID, result.Items, ids); err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	if err := rt.attachImages(r.Context(), userID, result.Items, ids, req.images, req.fields[fieldPrimaryImageAspectRatio]); err != nil {
		rt.writeLibraryError(w, err)
		return
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
	all := map[string]bool{fieldOverview: true, fieldSortName: true, fieldParentID: true, fieldMediaSources: true, fieldPrimaryImageAspectRatio: true}
	dto, err := rt.browseDto(item, all)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	if dto.MediaType == mediaTypeVideo {
		sources, err := catalog.PlaybackSources(r.Context(), rt.playbackActor(r, principal), item.ID)
		if err != nil {
			rt.writeLibraryError(w, err)
			return
		}
		dto.MediaSources = make([]mediaSourceInfo, 0, len(sources))
		for _, source := range sources {
			info, err := rt.mediaSource(dto.ID, source, item.Title)
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
	single := []baseItemDto{dto}
	if err := rt.attachUserData(r.Context(), userID, single, []string{item.ID}); err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	if err := rt.attachImages(r.Context(), userID, single, []string{item.ID}, defaultImageOptions(), true); err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	writeJSON(w, single[0])
}

// playbackActor is the caller's own session: sources are always looked up
// with it, also when an administrator reads as another user.
func (rt *router) playbackActor(r *http.Request, principal access.Principal) domain.Actor {
	return domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID, IP: rt.opts.Library.ClientIP(r)}
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

// mediaSource describes one source of the item with wire identifier itemID.
func (rt *router) mediaSource(itemID string, source domain.PlaybackSource, name string) (mediaSourceInfo, error) {
	id, err := FormatID(source.ID)
	if err != nil {
		return mediaSourceInfo{}, err
	}
	delivery := rt.opts.Library.Delivery != nil
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
	if audio, ok := defaultAudio(source); ok {
		index := audio.Index
		info.DefaultAudioStreamIndex = &index
	}
	extracted := delivery && rt.extractionAvailable()
	for _, s := range source.Subtitles {
		stream := mediaStream{Codec: s.Format, Language: s.Language, Title: s.Title, IsDefault: s.Default, IsForced: s.Forced, Type: mediaStreamSubtitle, Index: s.Index}
		if delivery {
			stream.DeliveryMethod = subtitleDeliveryEmbed
			stream.IsTextSubtitleStream = textSubtitleFormats[s.Format]
			// An extractable embedded text track is delivered as the copy
			// the server keeps in its cache, byte for byte (G15.5).
			if extension, ok := domain.ExtractableSubtitleCodecs[s.Codec]; extracted && s.Extractable && ok {
				stream.DeliveryMethod = subtitleDeliveryExternal
				stream.SupportsExternalStream = true
				stream.DeliveryURL = subtitleURL(itemID, id, s.Index, extension)
			}
		}
		info.MediaStreams = append(info.MediaStreams, stream)
	}
	for _, a := range source.Attachments {
		if a.StreamIndex == nil {
			continue
		}
		attachment := mediaAttachment{Index: *a.StreamIndex, FileName: a.FileName}
		if a.Font {
			attachment.MimeType = media.ExtractedContentType(media.ExtractedAttachment, path.Ext(a.FileName))
			if extracted {
				attachment.DeliveryURL = attachmentURL(itemID, id, *a.StreamIndex)
			}
		}
		info.MediaAttachments = append(info.MediaAttachments, attachment)
	}
	info.DefaultSubtitleStreamIndex = defaultSubtitleIndex(source, delivery)
	if !delivery {
		return info, nil
	}
	for _, sub := range externalSubtitles(source) {
		t := sub.track
		info.MediaStreams = append(info.MediaStreams, mediaStream{
			Codec: t.Format, Language: t.Language, Title: t.Title, IsDefault: t.Default, IsForced: t.Forced, IsHearingImpaired: t.SDH,
			Type: mediaStreamSubtitle, Index: sub.index, IsExternal: true, IsTextSubtitleStream: textSubtitleFormats[t.Codec],
			SupportsExternalStream: true, DeliveryMethod: subtitleDeliveryExternal,
			DeliveryURL: subtitleURL(itemID, id, sub.index, t.Format),
		})
	}
	return info, nil
}

// defaultSubtitleIndex maps the preferred subtitle to its stream index: an
// embedded stream by its own index, an external file by its upstream
// number, which only exists with direct delivery. Without preference
// information the member stays absent and clients use the stream flags.
func defaultSubtitleIndex(source domain.PlaybackSource, delivery bool) *int {
	tracks := source.DefaultTracks
	if tracks == nil {
		return nil
	}
	none := -1
	sub := tracks.Subtitle
	switch {
	case sub == nil:
		return &none
	case sub.Kind == domain.TrackEmbedded && sub.Index != nil:
		index := *sub.Index
		return &index
	case sub.Kind == domain.TrackExternal && delivery:
		for _, external := range externalSubtitles(source) {
			if external.track.ID == sub.ID {
				index := external.index
				return &index
			}
		}
	}
	return &none
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
