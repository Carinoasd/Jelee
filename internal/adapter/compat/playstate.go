package compat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Playstate module (G24.2, G23.2, G48.3): playback reports, played marks,
// user data and the continue watching list. Reports go to the server's own
// progress buffer, which coalesces them per session and writes in batches;
// user data and the resume list are read through storage with the library
// grants of the user the request reads as, so an item the user cannot see is
// never listed and has no user data.

// Playstate is the server's progress service (app.Catalog).
type Playstate interface {
	ReportPlayback(ctx context.Context, actor domain.Actor, report domain.PlaybackReport) error
	UserItemData(ctx context.Context, userID string, itemIDs []string) (map[string]domain.UserItemData, error)
	SetPlayed(ctx context.Context, userID, itemID string, played bool, at *time.Time) (domain.UserItemData, error)
	Resume(ctx context.Context, userID string, query domain.ResumeQuery) (domain.ResumePage, error)
}

func (rt *router) playstateRoutes() {
	for pattern, kind := range map[string]domain.PlaybackReportKind{
		"/Sessions/Playing":          domain.PlaybackReportStart,
		"/Sessions/Playing/Progress": domain.PlaybackReportProgress,
		"/Sessions/Playing/Stopped":  domain.PlaybackReportStop,
	} {
		rt.handle(http.MethodPost, pattern, true, rt.bounded(rt.playbackReport(kind)))
	}
	rt.handle(http.MethodPost, "/Sessions/Playing/Ping", true, rt.bounded(rt.playbackPing))
	for _, pattern := range []string{"/UserPlayedItems/{itemId}", "/Users/{id}/PlayedItems/{itemId}"} {
		rt.handle(http.MethodPost, pattern, true, rt.bounded(rt.markPlayed(true)))
		rt.handle(http.MethodDelete, pattern, true, rt.bounded(rt.markPlayed(false)))
	}
	rt.handle(http.MethodGet, "/UserItems/{itemId}/UserData", true, rt.bounded(rt.itemUserData))
	rt.handle(http.MethodGet, "/Users/{id}/Items/{itemId}/UserData", true, rt.bounded(rt.itemUserData))
	rt.handle(http.MethodGet, "/UserItems/Resume", true, rt.bounded(rt.resumeItems))
	rt.handle(http.MethodGet, "/Users/{id}/Items/Resume", true, rt.bounded(rt.resumeItems))
}

// isPlaybackReportPath reports whether the path below Prefix names a report
// route whose body is client state (see media.GuardPlaybackReport).
func isPlaybackReportPath(rest string) bool {
	rest = strings.TrimSuffix(rest, "/")
	segments := strings.Split(rest, "/")
	if len(segments) < 3 || segments[0] != "" || !strings.EqualFold(segments[1], "Sessions") || !strings.EqualFold(segments[2], "Playing") {
		return false
	}
	return len(segments) == 3 || len(segments) == 4 && (strings.EqualFold(segments[3], "Progress") || strings.EqualFold(segments[3], "Stopped"))
}

// playbackReportBody holds the members of the upstream PlaybackStartInfo,
// PlaybackProgressInfo and PlaybackStopInfo that change what is recorded.
// The others (stream indexes, volume, queue, the item, the play method) are
// ignored: the delivery is always the original file. Member names match
// case-insensitively, as upstream binds them.
type playbackReportBody struct {
	ItemID        string `json:"ItemId"`
	MediaSourceID string `json:"MediaSourceId"`
	PositionTicks *int64 `json:"PositionTicks"`
	IsPaused      bool   `json:"IsPaused"`
	PlaySessionID string `json:"PlaySessionId"`
	Failed        bool   `json:"Failed"`
}

func readJSONBody(r *http.Request, target any) error {
	if r.Body == nil {
		return errBadQuery
	}
	// The guard has bounded and restored the body.
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return errBadQuery
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(contentType, "application/json") {
		return errUnsupportedBody
	}
	if json.Unmarshal(data, target) != nil {
		return errBadQuery
	}
	return nil
}

// playbackReport answers the three report routes with 204. A report of an
// item the caller cannot see, of a missing item or of a session that already
// ended changes nothing and is answered like a recorded one, so the routes
// never tell whether an item exists.
func (rt *router) playbackReport(kind domain.PlaybackReportKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := access.PrincipalFromContext(r.Context())
		if !ok || principal.Kind != access.ClientNative {
			writeError(w, http.StatusUnauthorized)
			return
		}
		var body playbackReportBody
		if err := readJSONBody(r, &body); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errUnsupportedBody) {
				status = http.StatusUnsupportedMediaType
			}
			writeError(w, status)
			return
		}
		report := domain.PlaybackReport{Kind: kind, Paused: body.IsPaused && kind != domain.PlaybackReportStop, Failed: body.Failed && kind == domain.PlaybackReportStop}
		if body.ItemID != "" {
			id, err := ParseID(body.ItemID)
			if err != nil {
				writeError(w, http.StatusBadRequest)
				return
			}
			report.ItemID = id
		}
		if body.MediaSourceID != "" && kind == domain.PlaybackReportStart {
			// Upstream clients sometimes send a non-identifier media source
			// (a live stream or a path); only an identifier selects a version.
			if id, err := ParseID(body.MediaSourceID); err == nil {
				report.SourceID = id
			}
		}
		if body.PositionTicks != nil {
			if *body.PositionTicks < 0 || *body.PositionTicks > domain.PlaybackPositionMax {
				writeError(w, http.StatusBadRequest)
				return
			}
			report.PositionTicks, report.PositionKnown = *body.PositionTicks, true
		}
		report.PlayKey = body.PlaySessionID
		if !domain.ValidPlayKey(report.PlayKey) {
			if report.ItemID == "" {
				// Nothing names the session: upstream ignores such a report.
				w.WriteHeader(http.StatusNoContent)
				return
			}
			report.PlayKey = domain.DerivedPlayKey(principal.SessionID, report.ItemID)
		}
		err := rt.opts.Library.Playstate.ReportPlayback(r.Context(), rt.playbackActor(r, principal), report)
		rt.writeReportResult(w, err)
	}
}

func (rt *router) writeReportResult(w http.ResponseWriter, err error) {
	switch {
	case err == nil, errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrConflict):
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, domain.ErrInvalid):
		writeError(w, http.StatusBadRequest)
	case errors.Is(err, domain.ErrPlaybackBusy), errors.Is(err, domain.ErrDatabase), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, http.StatusServiceUnavailable)
	default:
		writeError(w, http.StatusInternalServerError)
	}
}

// playbackPing keeps a session alive. playSessionId is required, as
// upstream.
func (rt *router) playbackPing(w http.ResponseWriter, r *http.Request) {
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok || principal.Kind != access.ClientNative {
		writeError(w, http.StatusUnauthorized)
		return
	}
	key := readQuery(r).get("playsessionid")
	if key == "" {
		writeError(w, http.StatusBadRequest)
		return
	}
	if !domain.ValidPlayKey(key) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	err := rt.opts.Library.Playstate.ReportPlayback(r.Context(), rt.playbackActor(r, principal), domain.PlaybackReport{Kind: domain.PlaybackReportPing, PlayKey: key})
	rt.writeReportResult(w, err)
}

// userItemDataDto mirrors the upstream UserItemDataDto. Rating, Likes and
// UnplayedItemCount have no source and are omitted; favourites are not kept.
type userItemData struct {
	PlayedPercentage      *float64 `json:"PlayedPercentage,omitempty"`
	PlaybackPositionTicks int64    `json:"PlaybackPositionTicks"`
	PlayCount             int      `json:"PlayCount"`
	IsFavorite            bool     `json:"IsFavorite"`
	LastPlayedDate        string   `json:"LastPlayedDate,omitempty"`
	Played                bool     `json:"Played"`
	Key                   string   `json:"Key"`
	ItemID                string   `json:"ItemId"`
}

// userDataDto maps stored user data of the item with wire identifier id.
func userDataDto(id string, d domain.UserItemData, runtime int64) userItemData {
	dto := userItemData{PlaybackPositionTicks: d.ResumeTicks, PlayCount: d.PlayCount, Played: d.Played, Key: id, ItemID: id}
	if d.LastPlayedAt != nil {
		dto.LastPlayedDate = d.LastPlayedAt.UTC().Format(wireTime)
	}
	if runtime > 0 && d.ResumeTicks > 0 {
		percentage := min(100, float64(d.ResumeTicks)/float64(runtime)*100)
		dto.PlayedPercentage = &percentage
	}
	return dto
}

// parseDatePlayed reads the optional datePlayed member: an ISO 8601 time or
// the upstream legacy compact form yyyyMMddHHmmss (UTC).
func parseDatePlayed(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999", "20060102150405"} {
		if t, err := time.Parse(layout, raw); err == nil && t.Year() >= 1900 && t.Year() <= 9999 {
			utc := t.UTC()
			return &utc, nil
		}
	}
	return nil, errBadQuery
}

// markPlayed answers POST (played) and DELETE (unplayed) of
// /UserPlayedItems/{itemId} and the legacy /Users/{id}/PlayedItems/{itemId}.
// The user is the caller unless an administrator names another one.
func (rt *router) markPlayed(played bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := readQuery(r)
		_, userID, ok := rt.libraryUser(w, r, q)
		if !ok {
			return
		}
		itemID, err := ParseID(chi.URLParam(r, "itemId"))
		if err != nil {
			writeError(w, http.StatusBadRequest)
			return
		}
		var at *time.Time
		if played {
			if at, err = parseDatePlayed(q.get("dateplayed")); err != nil {
				writeError(w, http.StatusBadRequest)
				return
			}
		}
		d, err := rt.opts.Library.Playstate.SetPlayed(r.Context(), userID, itemID, played, at)
		if err != nil {
			rt.writeLibraryError(w, err)
			return
		}
		id, err := FormatID(itemID)
		if err != nil {
			writeError(w, http.StatusInternalServerError)
			return
		}
		writeJSON(w, userDataDto(id, d, 0))
	}
}

// itemUserData answers the user data of one visible item.
func (rt *router) itemUserData(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	_, userID, ok := rt.libraryUser(w, r, q)
	if !ok {
		return
	}
	itemID, err := ParseID(chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	data, err := rt.opts.Library.Playstate.UserItemData(r.Context(), userID, []string{itemID})
	if err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	d, found := data[itemID]
	if !found {
		writeError(w, rt.opts.Library.HiddenStatus)
		return
	}
	id, err := FormatID(itemID)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	writeJSON(w, userDataDto(id, d, 0))
}

// resumeItems answers the continue watching list: visible movies, episodes
// and home videos with a resume point that are not played, most recently
// played first. StartIndex, Limit, IncludeItemTypes, ExcludeItemTypes and
// Fields are honoured; ParentId and the other filters are ignored.
func (rt *router) resumeItems(w http.ResponseWriter, r *http.Request) {
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
	result := queryResult{Items: []baseItemDto{}, StartIndex: req.offset}
	if len(req.kinds) == 0 {
		writeJSON(w, result)
		return
	}
	query := domain.ResumeQuery{Offset: req.offset, Limit: max(req.limit, 1)}
	if req.typed || len(req.kinds) < len(itemTypeByKind) {
		query.Kinds = req.kinds
	}
	page, err := rt.opts.Library.Playstate.Resume(r.Context(), userID, query)
	if err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	result.TotalRecordCount = page.Total
	var ids []string
	if req.limit > 0 {
		for _, entry := range page.Items {
			dto, err := rt.itemDto(entry.Item, req.fields)
			if err != nil {
				writeError(w, http.StatusInternalServerError)
				return
			}
			dto.UserData = userDataDto(dto.ID, entry.Data, entry.RuntimeTicks)
			if entry.RuntimeTicks > 0 {
				runtime := entry.RuntimeTicks
				dto.RunTimeTicks = &runtime
			}
			result.Items = append(result.Items, dto)
			ids = append(ids, entry.Item.ID)
		}
	}
	if err := rt.attachImages(r.Context(), userID, result.Items, ids, req.images, req.fields[fieldPrimaryImageAspectRatio]); err != nil {
		rt.writeLibraryError(w, err)
		return
	}
	writeJSON(w, result)
}

// attachUserData fills the user data of the playable items among dtos,
// whose native identifiers are ids, for userID in one read. Without the
// playstate module every item reads as unplayed.
func (rt *router) attachUserData(ctx context.Context, userID string, dtos []baseItemDto, ids []string) error {
	if rt.opts.Library.Playstate == nil {
		return nil
	}
	var wanted []string
	for i := range dtos {
		if dtos[i].MediaType == mediaTypeVideo {
			wanted = append(wanted, ids[i])
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	data, err := rt.opts.Library.Playstate.UserItemData(ctx, userID, wanted)
	if err != nil {
		return err
	}
	for i := range dtos {
		if d, ok := data[ids[i]]; ok && dtos[i].MediaType == mediaTypeVideo {
			runtime := int64(0)
			if dtos[i].RunTimeTicks != nil {
				runtime = *dtos[i].RunTimeTicks
			}
			dtos[i].UserData = userDataDto(dtos[i].ID, d, runtime)
		}
	}
	return nil
}
