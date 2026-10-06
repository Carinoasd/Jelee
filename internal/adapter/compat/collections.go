package compat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Collection and playlist module (G02.1, G24.2, G48.3). Collections appear
// as BoxSet items and playlists as Playlist items; their contents are listed
// through the usual item routes and the upstream playlist routes. The layer
// only adapts the server's own collection service (app.Catalog): every read
// binds the caller's session and applies the unified visibility filter in
// storage, so a hidden member is never listed or counted, and every playlist
// change is the native change with the native rules (only the owner changes
// a playlist; another user's public playlist is read-only, a private one
// does not exist for them).
//
// The native service reads as the session's own user and has no "read as"
// form, so an administrator reading as another user ({userId}) sees no
// collection or playlist rather than the administrator's own.

// Collections is the server's collection and playlist service.
type Collections interface {
	ListCollections(ctx context.Context, actor domain.Actor, cursor string, limit int) (domain.CollectionPage, error)
	Collection(ctx context.Context, actor domain.Actor, id string) (domain.CollectionView, error)
	ListPlaylists(ctx context.Context, actor domain.Actor, cursor string, limit int) (domain.PlaylistPage, error)
	Playlist(ctx context.Context, actor domain.Actor, id string) (domain.PlaylistView, error)
	CreatePlaylist(ctx context.Context, actor domain.Actor, in domain.PlaylistInput) (domain.PlaylistView, error)
	DeletePlaylist(ctx context.Context, actor domain.Actor, id string) error
	AddPlaylistItems(ctx context.Context, actor domain.Actor, id string, itemIDs []string) (domain.PlaylistView, error)
	RemovePlaylistEntry(ctx context.Context, actor domain.Actor, id, entryID string) (domain.PlaylistView, error)
	MovePlaylistEntry(ctx context.Context, actor domain.Actor, id, entryID, beforeEntryID string) (domain.PlaylistView, error)
}

const (
	// containerReadMax bounds the collections, and separately the
	// playlists, one listing reads; TotalRecordCount counts at most these.
	containerReadMax = domain.CollectionsMax
	// playlistBodyLimit bounds a playlist creation body: a name and up to
	// MembershipBatchMax identifiers with the members clients add.
	playlistBodyLimit = 32 << 10
	// Names of the virtual folders, as upstream names them in English.
	viewNameCollections = "Collections"
	viewNamePlaylists   = "Playlists"
)

// virtualViews holds the identifiers (native form) of the two virtual
// folders that hold the collections and the playlists.
type virtualViews struct {
	boxsets, playlists string
}

// newVirtualViews derives the folder identifiers from the server
// identifier, so they are stable for one server and never name a stored
// entity.
func newVirtualViews(serverID string) virtualViews {
	return virtualViews{boxsets: deriveViewID(serverID, collectionTypeBoxSets), playlists: deriveViewID(serverID, collectionTypePlaylists)}
}

func deriveViewID(serverID, name string) string {
	sum := sha256.Sum256([]byte("jelee-compat-view-v1\x00" + serverID + "\x00" + name))
	// A version 4, variant 1 identifier: never all zero.
	sum[6] = sum[6]&0x0f | 0x40
	sum[8] = sum[8]&0x3f | 0x80
	id, _ := ParseID(hex.EncodeToString(sum[:16]))
	return id
}

// container is one collection or playlist as the reading user sees it.
type container struct {
	id, name, overview string
	// kind is itemTypeBoxSet or itemTypePlaylist.
	kind    string
	count   int
	created time.Time
}

func collectionContainer(c domain.Collection) container {
	return container{id: c.ID, name: c.Name, overview: c.Overview, kind: itemTypeBoxSet, count: c.ItemCount, created: c.CreatedAt}
}

func playlistContainer(p domain.Playlist) container {
	return container{id: p.ID, name: p.Name, kind: itemTypePlaylist, count: p.ItemCount, created: p.CreatedAt}
}

// member is one visible member of a collection or entry of a playlist.
type member struct {
	item domain.Item
	// entryID names the playlist entry; empty for a collection member.
	entryID string
}

// containerTypes reports whether names include the BoxSet and the Playlist
// type. itemKinds has already refused malformed names.
func containerTypes(names []string) (boxsets, playlists bool) {
	for _, name := range names {
		boxsets = boxsets || strings.EqualFold(name, itemTypeBoxSet)
		playlists = playlists || strings.EqualFold(name, itemTypePlaylist)
	}
	return boxsets, playlists
}

// containersReadable reports whether the request may read collections and
// playlists: the module is wired and the request reads as the caller.
func (rt *router) containersReadable(principal access.Principal, userID string) bool {
	return rt.opts.Library.Collections != nil && userID == principal.UserID
}

// virtualViewDtos returns the virtual folders the caller has content in:
// Collections when a collection has a visible member, Playlists when a
// playlist is readable.
func (rt *router) virtualViewDtos(r *http.Request, principal access.Principal, userID string) ([]baseItemDto, error) {
	if !rt.containersReadable(principal, userID) {
		return nil, nil
	}
	collections := rt.opts.Library.Collections
	actor := rt.playbackActor(r, principal)
	var out []baseItemDto
	// Administrators also get collections without a visible member; one
	// with a visible member is enough.
	cursor := ""
	for read := 0; read < containerReadMax; read += domain.CollectionPageMax {
		page, err := collections.ListCollections(r.Context(), actor, cursor, domain.CollectionPageMax)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(page.Collections, func(c domain.Collection) bool { return c.ItemCount > 0 }) {
			out = append(out, rt.virtualViewDto(rt.views.boxsets))
			break
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	page, err := collections.ListPlaylists(r.Context(), actor, "", 1)
	if err != nil {
		return nil, err
	}
	if len(page.Playlists) > 0 {
		out = append(out, rt.virtualViewDto(rt.views.playlists))
	}
	return out, nil
}

// virtualViewByID answers the virtual folders by identifier.
func (rt *router) virtualViewByID(principal access.Principal, userID, id string) (baseItemDto, bool) {
	if !rt.containersReadable(principal, userID) || id != rt.views.boxsets && id != rt.views.playlists {
		return baseItemDto{}, false
	}
	return rt.virtualViewDto(id), true
}

func (rt *router) virtualViewDto(id string) baseItemDto {
	wire, _ := FormatID(id)
	name, collectionType := viewNameCollections, collectionTypeBoxSets
	if id == rt.views.playlists {
		name, collectionType = viewNamePlaylists, collectionTypePlaylists
	}
	dto := rt.baseDto(wire, name)
	dto.IsFolder = true
	dto.Type = itemTypeCollectionFolder
	dto.CollectionType = collectionType
	dto.MediaType = mediaTypeUnknown
	return dto
}

// containerByID reads one collection (when boxsets) or playlist (when
// playlists) the caller can see. A collection without a visible member is
// not found, also for an administrator.
func (rt *router) containerByID(r *http.Request, principal access.Principal, userID, id string, boxsets, playlists bool) (container, bool, error) {
	if !rt.containersReadable(principal, userID) {
		return container{}, false, nil
	}
	_, c, ok, err := rt.readContainer(r, principal, id, boxsets, playlists)
	return c, ok, err
}

// readContainer reads one collection or playlist with its visible members.
func (rt *router) readContainer(r *http.Request, principal access.Principal, id string, boxsets, playlists bool) ([]member, container, bool, error) {
	collections := rt.opts.Library.Collections
	actor := rt.playbackActor(r, principal)
	if boxsets {
		view, err := collections.Collection(r.Context(), actor, id)
		switch {
		case err == nil && view.Collection.ItemCount > 0:
			members := make([]member, 0, len(view.Items))
			for _, m := range view.Items {
				members = append(members, member{item: m.Item})
			}
			return members, collectionContainer(view.Collection), true, nil
		case err != nil && !errors.Is(err, domain.ErrNotFound):
			return nil, container{}, false, err
		}
	}
	if playlists {
		view, err := collections.Playlist(r.Context(), actor, id)
		switch {
		case err == nil:
			members := make([]member, 0, len(view.Entries))
			for _, e := range view.Entries {
				members = append(members, member{item: e.Item, entryID: e.EntryID})
			}
			return members, playlistContainer(view.Playlist), true, nil
		case !errors.Is(err, domain.ErrNotFound):
			return nil, container{}, false, err
		}
	}
	return nil, container{}, false, nil
}

// containerDto maps a collection to a BoxSet and a playlist to a Playlist.
// Overview, SortName and ParentId (the virtual folder) follow Fields like
// on items. Neither has images of its own.
func (rt *router) containerDto(c container, fields map[string]bool, images imageOptions) (baseItemDto, error) {
	id, err := FormatID(c.id)
	if err != nil {
		return baseItemDto{}, err
	}
	dto := rt.baseDto(id, c.name)
	dto.Type = c.kind
	dto.IsFolder = true
	count := c.count
	dto.ChildCount = &count
	dto.MediaType = mediaTypeUnknown
	parent := rt.views.boxsets
	if c.kind == itemTypePlaylist {
		// Playlists hold playable videos only.
		dto.MediaType = mediaTypeVideo
		parent = rt.views.playlists
	}
	if !c.created.IsZero() {
		dto.DateCreated = c.created.UTC().Format(wireTime)
	}
	if fields[fieldOverview] {
		dto.Overview = c.overview
	}
	if fields[fieldSortName] {
		dto.SortName = strings.ToLower(c.name)
	}
	if fields[fieldParentID] {
		dto.ParentID, _ = FormatID(parent)
	}
	if !images.enabled {
		dto.ImageTags = nil
	}
	if images.typeLimit(imageTypeBackdrop) == 0 {
		dto.BackdropImageTags = nil
	}
	return dto, nil
}

// containerListing answers listings of collections or playlists: a type
// filter of BoxSet and/or Playlist only, or a virtual folder as the parent.
// Like upstream, a BoxSet-only filter ignores ParentId. It reports false for
// any other listing, which the catalog answers.
func (rt *router) containerListing(w http.ResponseWriter, r *http.Request, principal access.Principal, userID string, req itemsRequest) bool {
	if rt.opts.Library.Collections == nil {
		return false
	}
	var boxsets, playlists bool
	switch {
	case req.parentID != "" && req.parentID == rt.views.boxsets:
		boxsets = req.boxsets
	case req.parentID != "" && req.parentID == rt.views.playlists:
		playlists = req.playlists
	case req.containersOnly():
		// Playlists live in no library: a parent other than their
		// folder holds none.
		boxsets = req.boxsets && (!req.playlists || req.parentID == "")
		playlists = req.playlists && req.parentID == ""
	default:
		return false
	}
	result := queryResult{Items: []baseItemDto{}, StartIndex: req.offset}
	if !rt.containersReadable(principal, userID) || !boxsets && !playlists {
		writeJSON(w, result)
		return true
	}
	all, err := rt.listContainers(r, principal, boxsets, playlists)
	if err != nil {
		rt.writeContainerError(w, err)
		return true
	}
	needle := strings.ToLower(req.search)
	matched := all[:0:0]
	for _, c := range all {
		if strings.Contains(strings.ToLower(c.name), needle) {
			matched = append(matched, c)
		}
	}
	slices.SortStableFunc(matched, func(a, b container) int {
		if req.nameDescending {
			a, b = b, a
		}
		if n := strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)); n != 0 {
			return n
		}
		return strings.Compare(a.id, b.id)
	})
	result.TotalRecordCount = len(matched)
	for i := req.offset; i < len(matched) && i < req.offset+req.limit; i++ {
		dto, err := rt.containerDto(matched[i], req.fields, req.images)
		if err != nil {
			writeError(w, http.StatusInternalServerError)
			return true
		}
		result.Items = append(result.Items, dto)
	}
	writeJSON(w, result)
	return true
}

// listContainers reads the collections with a visible member and the
// readable playlists, at most containerReadMax of each.
func (rt *router) listContainers(r *http.Request, principal access.Principal, boxsets, playlists bool) ([]container, error) {
	collections := rt.opts.Library.Collections
	actor := rt.playbackActor(r, principal)
	var out []container
	if boxsets {
		cursor := ""
		for read := 0; read < containerReadMax; read += domain.CollectionPageMax {
			page, err := collections.ListCollections(r.Context(), actor, cursor, domain.CollectionPageMax)
			if err != nil {
				return nil, err
			}
			for _, c := range page.Collections {
				// Administrators also get collections without a visible
				// member; the layer lists none of those.
				if c.ItemCount > 0 {
					out = append(out, collectionContainer(c))
				}
			}
			if cursor = page.NextCursor; cursor == "" {
				break
			}
		}
	}
	if playlists {
		cursor := ""
		for read := 0; read < containerReadMax; read += domain.CollectionPageMax {
			page, err := collections.ListPlaylists(r.Context(), actor, cursor, domain.CollectionPageMax)
			if err != nil {
				return nil, err
			}
			for _, p := range page.Playlists {
				out = append(out, playlistContainer(p))
			}
			if cursor = page.NextCursor; cursor == "" {
				break
			}
		}
	}
	return out, nil
}

// containerChildren answers a listing whose parent is a collection or a
// playlist: the visible members, filtered by type and search term. Members
// of a collection are in title order (reversed by a descending name sort),
// entries of a playlist in playlist order. It reports false when the parent
// is neither, and the caller answers as for any unknown parent.
func (rt *router) containerChildren(w http.ResponseWriter, r *http.Request, principal access.Principal, userID string, req itemsRequest) bool {
	if !rt.containersReadable(principal, userID) {
		return false
	}
	members, c, ok, err := rt.readContainer(r, principal, req.parentID, true, true)
	if err != nil {
		rt.writeContainerError(w, err)
		return true
	}
	if !ok {
		return false
	}
	needle := strings.ToLower(req.search)
	matched := members[:0:0]
	for _, m := range members {
		if slices.Contains(req.kinds, m.item.Kind) && strings.Contains(strings.ToLower(m.item.Title), needle) {
			matched = append(matched, m)
		}
	}
	if c.kind == itemTypeBoxSet && req.nameDescending {
		slices.Reverse(matched)
	}
	rt.writeMembers(w, r, userID, matched, req)
	return true
}

// writeMembers answers one page of members with user data and image tags.
func (rt *router) writeMembers(w http.ResponseWriter, r *http.Request, userID string, members []member, req itemsRequest) {
	result := queryResult{Items: []baseItemDto{}, TotalRecordCount: len(members), StartIndex: req.offset}
	var ids []string
	var items []bool
	for i := req.offset; i < len(members) && i < req.offset+req.limit; i++ {
		m := members[i]
		parent := m.item.ParentID
		if parent == "" {
			parent = m.item.LibraryID
		}
		dto, err := rt.itemDto(domain.BrowseItem{ID: m.item.ID, LibraryID: m.item.LibraryID, ParentID: parent, Kind: m.item.Kind, Title: m.item.Title}, req.fields)
		if err != nil {
			writeError(w, http.StatusInternalServerError)
			return
		}
		if m.entryID != "" {
			if dto.PlaylistItemID, err = FormatID(m.entryID); err != nil {
				writeError(w, http.StatusInternalServerError)
				return
			}
		}
		result.Items = append(result.Items, dto)
		ids = append(ids, m.item.ID)
		items = append(items, true)
	}
	if err := rt.decorate(r.Context(), userID, result.Items, ids, items, req.images, req.fields[fieldPrimaryImageAspectRatio]); err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	writeJSON(w, result)
}

// decorate attaches user data and image tags to the catalog items among
// dtos (items[i] is true); collections and playlists are left as they are.
func (rt *router) decorate(ctx context.Context, userID string, dtos []baseItemDto, ids []string, items []bool, images imageOptions, aspect bool) error {
	var subset []baseItemDto
	var subsetIDs []string
	for i := range dtos {
		if items[i] {
			subset = append(subset, dtos[i])
			subsetIDs = append(subsetIDs, ids[i])
		}
	}
	if err := rt.attachUserData(ctx, userID, subset, subsetIDs); err != nil {
		return err
	}
	if err := rt.attachImages(ctx, userID, subset, subsetIDs, images, aspect); err != nil {
		return err
	}
	j := 0
	for i := range dtos {
		if items[i] {
			dtos[i] = subset[j]
			j++
		}
	}
	return nil
}

// writeContainerError maps collection service errors: a change of another
// user's public playlist is 403, a refused change (limits, a type a
// playlist cannot hold) the generic 400; the rest as for the catalog.
func (rt *router) writeContainerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrForbidden):
		writeError(w, http.StatusForbidden)
	case errors.Is(err, domain.ErrConflict):
		writeError(w, http.StatusBadRequest)
	case errors.Is(err, domain.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized)
	default:
		rt.writeLibraryError(w, err)
	}
}

func (rt *router) playlistRoutes() {
	rt.handle(http.MethodGet, "/Playlists/{playlistId}/Items", true, rt.bounded(rt.playlistItems))
	rt.handle(http.MethodPost, "/Playlists", true, rt.bounded(rt.createPlaylist))
	rt.handle(http.MethodPost, "/Playlists/{playlistId}/Items", true, rt.bounded(rt.addToPlaylist))
	rt.handle(http.MethodDelete, "/Playlists/{playlistId}/Items", true, rt.bounded(rt.removeFromPlaylist))
	rt.handle(http.MethodPost, "/Playlists/{playlistId}/Items/{itemId}/Move/{newIndex}", true, rt.bounded(rt.movePlaylistItem))
}

// playlistItems answers GET /Playlists/{playlistId}/Items: the visible
// entries in playlist order, each with its PlaylistItemId. StartIndex,
// Limit, Fields and the image members are honoured; the playlist must be
// the caller's own or public, else the hidden status.
func (rt *router) playlistItems(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	principal, userID, ok := rt.libraryUser(w, r, q)
	if !ok {
		return
	}
	id, err := ParseID(chi.URLParam(r, "playlistId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	req, err := parseItemsRequest(q)
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	if userID != principal.UserID {
		writeError(w, rt.opts.Library.HiddenStatus)
		return
	}
	members, _, found, err := rt.readContainer(r, principal, id, false, true)
	switch {
	case err != nil:
		rt.writeContainerError(w, err)
	case !found:
		writeError(w, rt.opts.Library.HiddenStatus)
	default:
		rt.writeMembers(w, r, userID, members, req)
	}
}

// writer returns the caller of a playlist change. Playlists are changed by
// their owner only, so a userId naming anyone else (an administrator
// included) is refused with 403 before any read.
func (rt *router) writer(w http.ResponseWriter, r *http.Request, rawUser string) (access.Principal, domain.Actor, bool) {
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok || principal.Kind != access.ClientNative {
		writeError(w, http.StatusUnauthorized)
		return principal, domain.Actor{}, false
	}
	if rawUser != "" && !((len(rawUser) == 32 || len(rawUser) == 36) && isNilID(rawUser)) {
		id, err := ParseID(rawUser)
		if err != nil {
			writeError(w, http.StatusBadRequest)
			return principal, domain.Actor{}, false
		}
		if id != principal.UserID {
			writeError(w, http.StatusForbidden)
			return principal, domain.Actor{}, false
		}
	}
	return principal, rt.playbackActor(r, principal), true
}

// parseIDList parses 1..MembershipBatchMax identifiers.
func parseIDList(raw []string) ([]string, error) {
	if len(raw) == 0 || len(raw) > domain.MembershipBatchMax {
		return nil, errBadQuery
	}
	ids := make([]string, 0, len(raw))
	for _, value := range raw {
		id, err := ParseID(value)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// createPlaylistBody holds the members of the upstream CreatePlaylistDto
// that Jelee uses. MediaType is ignored (playlists hold videos), and Users
// is ignored: Jelee shares a playlist with everyone or with no one, so a
// share with named users is not granted.
type createPlaylistBody struct {
	Name     string   `json:"Name"`
	IDs      []string `json:"Ids"`
	UserID   string   `json:"UserId"`
	IsPublic *bool    `json:"IsPublic"`
}

// createPlaylist answers POST /Playlists with {"Id": ...}. The members come
// from the JSON body or, for older clients, from the name, ids and userId
// query members. A new playlist is private unless IsPublic is true. Adding
// the initial items is the native append; when it fails the new playlist is
// removed again and the failure answered.
func (rt *router) createPlaylist(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	var body createPlaylistBody
	if r.Body != nil {
		data, err := io.ReadAll(io.LimitReader(r.Body, playlistBodyLimit+1))
		switch {
		case err != nil:
			writeError(w, http.StatusBadRequest)
			return
		case len(data) > playlistBodyLimit:
			writeError(w, http.StatusRequestEntityTooLarge)
			return
		}
		if strings.TrimSpace(string(data)) != "" {
			if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || !strings.EqualFold(mediaType, "application/json") {
				writeError(w, http.StatusUnsupportedMediaType)
				return
			}
			if json.Unmarshal(data, &body) != nil {
				writeError(w, http.StatusBadRequest)
				return
			}
		}
	}
	rawUser := body.UserID
	if rawUser == "" {
		rawUser = q.get("userid")
	}
	_, actor, ok := rt.writer(w, r, rawUser)
	if !ok {
		return
	}
	name := body.Name
	if name == "" {
		name = q.get("name")
	}
	rawIDs := body.IDs
	if len(rawIDs) == 0 {
		rawIDs = q.list("ids")
	}
	var ids []string
	if len(rawIDs) > 0 {
		var err error
		if ids, err = parseIDList(rawIDs); err != nil {
			writeError(w, http.StatusBadRequest)
			return
		}
	}
	collections := rt.opts.Library.Collections
	view, err := collections.CreatePlaylist(r.Context(), actor, domain.PlaylistInput{Name: name, Public: body.IsPublic != nil && *body.IsPublic})
	if err != nil {
		rt.writeContainerError(w, err)
		return
	}
	if len(ids) > 0 {
		if _, err := collections.AddPlaylistItems(r.Context(), actor, view.Playlist.ID, ids); err != nil {
			// Best effort: the playlist is the caller's own and empty.
			_ = collections.DeletePlaylist(context.WithoutCancel(r.Context()), actor, view.Playlist.ID)
			rt.writeContainerError(w, err)
			return
		}
	}
	id, err := FormatID(view.Playlist.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	writeJSON(w, struct {
		ID string `json:"Id"`
	}{id})
}

// addToPlaylist answers POST /Playlists/{playlistId}/Items?Ids=: the items
// are appended in order with the native rules (visible movies, episodes
// and home videos; an invisible or missing item fails the whole request
// with the hidden status). 204 on success.
func (rt *router) addToPlaylist(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	_, actor, ok := rt.writer(w, r, q.get("userid"))
	if !ok {
		return
	}
	id, err := ParseID(chi.URLParam(r, "playlistId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	ids, err := parseIDList(q.list("ids"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	if _, err := rt.opts.Library.Collections.AddPlaylistItems(r.Context(), actor, id, ids); err != nil {
		rt.writeContainerError(w, err)
		return
	}
	writeNoContent(w)
}

// ownPlaylist reads a playlist for a change: the hidden status when the
// caller cannot read it, 403 when it is another user's public playlist.
func (rt *router) ownPlaylist(w http.ResponseWriter, r *http.Request, actor domain.Actor, id string) (domain.PlaylistView, bool) {
	view, err := rt.opts.Library.Collections.Playlist(r.Context(), actor, id)
	switch {
	case err != nil:
		rt.writeContainerError(w, err)
		return view, false
	case !view.Playlist.Owned:
		writeError(w, http.StatusForbidden)
		return view, false
	}
	return view, true
}

// findEntry returns the visible entry named by id: its PlaylistItemId, or
// else the first entry of the item with that identifier.
func findEntry(entries []domain.PlaylistEntry, id string) (string, bool) {
	for _, e := range entries {
		if e.EntryID == id {
			return e.EntryID, true
		}
	}
	for _, e := range entries {
		if e.Item.ID == id {
			return e.EntryID, true
		}
	}
	return "", false
}

// removeFromPlaylist answers DELETE /Playlists/{playlistId}/Items?EntryIds=.
// Each value names an entry by its PlaylistItemId or, as some clients send,
// every entry of an item by the item identifier. Values naming no visible
// entry are ignored like upstream ignores unknown entries. 204.
func (rt *router) removeFromPlaylist(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	_, actor, ok := rt.writer(w, r, q.get("userid"))
	if !ok {
		return
	}
	id, err := ParseID(chi.URLParam(r, "playlistId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	wanted, err := parseIDList(q.list("entryids"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	view, ok := rt.ownPlaylist(w, r, actor, id)
	if !ok {
		return
	}
	var entries []string
	for _, e := range view.Entries {
		if slices.Contains(wanted, e.EntryID) || slices.Contains(wanted, e.Item.ID) {
			entries = append(entries, e.EntryID)
		}
	}
	for _, entry := range entries {
		// An entry removed meanwhile is gone either way.
		if _, err := rt.opts.Library.Collections.RemovePlaylistEntry(r.Context(), actor, id, entry); err != nil && !errors.Is(err, domain.ErrNotFound) {
			rt.writeContainerError(w, err)
			return
		}
	}
	writeNoContent(w)
}

// movePlaylistItem answers POST
// /Playlists/{playlistId}/Items/{itemId}/Move/{newIndex}: the entry
// (PlaylistItemId, or the first entry of that item) moves to newIndex among
// the visible entries; an index past the end moves it to the end. 204.
func (rt *router) movePlaylistItem(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	_, actor, ok := rt.writer(w, r, q.get("userid"))
	if !ok {
		return
	}
	id, err := ParseID(chi.URLParam(r, "playlistId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	target, err := ParseID(chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	index, err := strconv.ParseInt(chi.URLParam(r, "newIndex"), 10, 32)
	if err != nil || index < 0 {
		writeError(w, http.StatusBadRequest)
		return
	}
	view, ok := rt.ownPlaylist(w, r, actor, id)
	if !ok {
		return
	}
	entry, found := findEntry(view.Entries, target)
	if !found {
		writeError(w, rt.opts.Library.HiddenStatus)
		return
	}
	var others []string
	for _, e := range view.Entries {
		if e.EntryID != entry {
			others = append(others, e.EntryID)
		}
	}
	before := ""
	if int(index) < len(others) {
		before = others[index]
	}
	if _, err := rt.opts.Library.Collections.MovePlaylistEntry(r.Context(), actor, id, entry, before); err != nil {
		rt.writeContainerError(w, err)
		return
	}
	writeNoContent(w)
}

func writeNoContent(w http.ResponseWriter) {
	w.Header().Del("Content-Type")
	w.WriteHeader(http.StatusNoContent)
}
