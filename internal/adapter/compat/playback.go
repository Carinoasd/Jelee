package compat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Playback module (G24.2, G10.4). PlaybackInfo describes the original
// resources a client can play as they are; the stream and subtitle routes
// hand them to the server's direct delivery handler. Nothing is ever
// converted: no response carries a transcoding member, SupportsTranscoding
// and SupportsDirectStream are always false, and a client that cannot play
// any original is told so with ErrorCode NoCompatibleStream instead of being
// offered a conversion.
//
// The production guard stays in front of every route (router.boundary).
// PlaybackInfo uses media.GuardPlaybackInfo, which reads the documented
// PlaybackInfo members and the DeviceProfile as the client's declaration;
// the stream and subtitle routes use the unmodified media.GuardProduction.

func (rt *router) playbackRoutes() {
	rt.handle(http.MethodGet, "/Items/{itemId}/PlaybackInfo", true, rt.bounded(rt.playbackInfo))
	rt.handle(http.MethodPost, "/Items/{itemId}/PlaybackInfo", true, rt.bounded(rt.playbackInfo))
	if rt.opts.Library.Delivery == nil {
		return
	}
	// Streams are not bounded by the request timeout: only their lookups
	// are, and the delivery handler applies its own write deadlines.
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rt.handle(method, "/Videos/{itemId}/stream", true, rt.videoStream)
		rt.handle(method, "/Videos/{itemId}/stream.{container}", true, rt.videoStream)
		rt.handle(method, "/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/Stream.{format}", true, rt.subtitleStream)
		rt.handle(method, "/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/{startPositionTicks}/Stream.{format}", true, rt.subtitleStream)
		if rt.opts.Library.Extracted != nil {
			rt.handle(method, "/Videos/{itemId}/{mediaSourceId}/Attachments/{index}", true, rt.attachmentStream)
		}
		// The catalog has no audio items, so every audio stream is a
		// missing item.
		rt.handle(method, "/Audio/{itemId}/stream", true, rt.audioStream)
		rt.handle(method, "/Audio/{itemId}/stream.{container}", true, rt.audioStream)
	}
}

// playbackInfoResponse mirrors the upstream PlaybackInfoResponse.
// MediaSources lists only sources the client can play directly.
type playbackInfoResponse struct {
	MediaSources  []mediaSourceInfo `json:"MediaSources"`
	PlaySessionID string            `json:"PlaySessionId,omitempty"`
	ErrorCode     string            `json:"ErrorCode,omitempty"`
}

// playbackInfoBody holds the PlaybackInfoDto members that influence the
// answer. The others (stream indexes, start time, live stream, the direct
// stream and transcoding switches) are accepted and ignored: they choose
// among conversions this server does not offer. Member names match
// case-insensitively, as upstream binds them.
type playbackInfoBody struct {
	UserID              string         `json:"UserId"`
	MaxStreamingBitrate *int64         `json:"MaxStreamingBitrate"`
	MediaSourceID       string         `json:"MediaSourceId"`
	EnableDirectPlay    *bool          `json:"EnableDirectPlay"`
	DeviceProfile       *deviceProfile `json:"DeviceProfile"`
}

// deviceProfile is the part of the upstream DeviceProfile that decides
// direct play. Transcoding, container, codec and subtitle profiles are not
// read: the first describe conversions, and the conditions of the others
// are not evaluated (see docs/compat-matrix.md).
type deviceProfile struct {
	MaxStreamingBitrate *int64              `json:"MaxStreamingBitrate"`
	DirectPlayProfiles  []directPlayProfile `json:"DirectPlayProfiles"`
}

type directPlayProfile struct {
	Container  string      `json:"Container"`
	AudioCodec string      `json:"AudioCodec"`
	VideoCodec string      `json:"VideoCodec"`
	Type       profileType `json:"Type"`
}

// profileType is the upstream DlnaProfileType, sent by name or by number.
type profileType string

func (p *profileType) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		*p = profileType(name)
		return nil
	}
	var number int
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	if number == dlnaProfileTypeVideoNumber {
		*p = dlnaProfileTypeVideo
	} else {
		*p = profileType(strconv.Itoa(number))
	}
	return nil
}

// playbackRequest is a parsed PlaybackInfo request. Query members take
// precedence over body members, as upstream.
type playbackRequest struct {
	userID        string
	mediaSourceID string
	maxBitrate    int64
	directPlay    bool
	profile       *deviceProfile
}

var errUnsupportedBody = errors.New("compat: unsupported body type")

func parsePlaybackRequest(r *http.Request, q browseQuery) (playbackRequest, error) {
	req := playbackRequest{directPlay: true}
	var body playbackInfoBody
	if r.Method == http.MethodPost && r.Body != nil {
		// The guard has bounded and restored the body.
		data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			return req, errBadQuery
		}
		if strings.TrimSpace(string(data)) != "" {
			contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || !strings.EqualFold(contentType, "application/json") {
				return req, errUnsupportedBody
			}
			if err := json.Unmarshal(data, &body); err != nil {
				return req, errBadQuery
			}
		}
	}
	req.userID = cmpOr(q.get("userid"), body.UserID)
	req.mediaSourceID = cmpOr(q.get("mediasourceid"), body.MediaSourceID)
	if req.mediaSourceID != "" {
		id, err := ParseID(req.mediaSourceID)
		if err != nil {
			return req, errBadQuery
		}
		req.mediaSourceID = id
	}
	direct, set, err := q.boolean("enabledirectplay")
	if err != nil {
		return req, err
	}
	switch {
	case set:
		req.directPlay = direct
	case body.EnableDirectPlay != nil:
		req.directPlay = *body.EnableDirectPlay
	}
	req.profile = body.DeviceProfile
	switch raw := q.get("maxstreamingbitrate"); {
	case raw != "":
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return req, errBadQuery
		}
		req.maxBitrate = v
	case body.MaxStreamingBitrate != nil:
		req.maxBitrate = *body.MaxStreamingBitrate
	case req.profile != nil && req.profile.MaxStreamingBitrate != nil:
		req.maxBitrate = *req.profile.MaxStreamingBitrate
	}
	return req, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// playbackInfo answers GET and POST /Items/{itemId}/PlaybackInfo.
func (rt *router) playbackInfo(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	req, err := parsePlaybackRequest(r, q)
	switch {
	case errors.Is(err, errUnsupportedBody):
		writeError(w, http.StatusUnsupportedMediaType)
		return
	case err != nil:
		writeError(w, http.StatusBadRequest)
		return
	}
	principal, userID, ok := rt.readAs(w, r, req.userID)
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
	result := playbackInfoResponse{MediaSources: []mediaSourceInfo{}}
	itemID, err := FormatID(item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	if playableKind(item.Kind) {
		sources, err := catalog.PlaybackSources(r.Context(), rt.playbackActor(r, principal), item.ID)
		if err != nil {
			rt.writeLibraryError(w, err)
			return
		}
		for _, source := range sources {
			if req.mediaSourceID != "" && source.ID != req.mediaSourceID || !rt.directPlayable(req, source) {
				continue
			}
			info, err := rt.mediaSource(itemID, source, item.Title)
			if err != nil {
				writeError(w, http.StatusInternalServerError)
				return
			}
			rt.addOCRStreams(r.Context(), principal, itemID, source, &info)
			result.MediaSources = append(result.MediaSources, info)
		}
	}
	if len(result.MediaSources) == 0 {
		// Upstream answers an item without a usable source the same way;
		// here it also covers every source the client cannot play as it
		// is, because no conversion is ever offered in its place.
		result.ErrorCode = playbackErrorNoCompatibleStream
	} else if result.PlaySessionID, err = newPlaySessionID(); err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

// newPlaySessionID returns a random upstream-style play session identifier.
// Clients echo it in their playback reports, where it keys the session.
var newPlaySessionID = func() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func playableKind(kind string) bool {
	return kind == "Movie" || kind == "Episode" || kind == "HomeVideo"
}

// directPlayable decides whether the client can play source as it is.
// Without a DeviceProfile nothing was declared and every source is offered
// when direct delivery is on. With one, some video direct play profile must
// accept the container, the primary video codec and the default audio
// codec, and a declared bit rate ceiling must not be exceeded. Values the
// server does not know (an unprobed source's codecs or bit rate) are not
// held against the source: the client may try it, and the bytes it gets are
// the original either way.
func (rt *router) directPlayable(req playbackRequest, source domain.PlaybackSource) bool {
	if !rt.opts.Library.DirectPlay || !req.directPlay {
		return false
	}
	if req.maxBitrate > 0 && source.BitRate != nil && *source.BitRate > req.maxBitrate {
		return false
	}
	if req.profile == nil {
		return true
	}
	return slices.ContainsFunc(req.profile.DirectPlayProfiles, func(p directPlayProfile) bool { return profileAccepts(p, source) })
}

func profileAccepts(p directPlayProfile, source domain.PlaybackSource) bool {
	if !strings.EqualFold(string(p.Type), string(dlnaProfileTypeVideo)) {
		return false
	}
	caps, err := domain.NormalizeClientCapabilities(domain.ClientCapabilities{
		Containers: splitList(p.Container), VideoCodecs: splitList(p.VideoCodec), AudioCodecs: splitList(p.AudioCodec),
	})
	if err != nil {
		// A list the server cannot read declares nothing it can rely on.
		return false
	}
	if source.Container != "" && len(caps.Containers) > 0 && !slices.Contains(caps.Containers, source.Container) {
		return false
	}
	for _, video := range source.Video {
		if video.Primary && video.Codec != "" && len(caps.VideoCodecs) > 0 && !slices.Contains(caps.VideoCodecs, video.Codec) {
			return false
		}
	}
	if audio, ok := defaultAudio(source); ok && audio.Codec != "" && len(caps.AudioCodecs) > 0 && !audioCodecDeclared(caps.AudioCodecs, audio.Codec) {
		return false
	}
	return true
}

// audioCodecDeclared matches like the server's own check: "pcm" covers
// every PCM sample format.
func audioCodecDeclared(declared []string, codec string) bool {
	return slices.Contains(declared, codec) || strings.HasPrefix(codec, "pcm") && slices.Contains(declared, "pcm")
}

func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// defaultAudio is the embedded audio stream a player starts with: the one
// the user's track preferences pick, else the default one, else the first.
func defaultAudio(source domain.PlaybackSource) (domain.PlaybackAudioTrack, bool) {
	if len(source.Audio) == 0 {
		return domain.PlaybackAudioTrack{}, false
	}
	if tracks := source.DefaultTracks; tracks != nil && tracks.Audio != nil && tracks.Audio.Kind == domain.TrackEmbedded && tracks.Audio.Index != nil {
		for _, audio := range source.Audio {
			if audio.Index == *tracks.Audio.Index {
				return audio, true
			}
		}
	}
	for _, audio := range source.Audio {
		if audio.Default {
			return audio, true
		}
	}
	return source.Audio[0], true
}

// externalSubtitle is an external subtitle file with its stream index.
type externalSubtitle struct {
	index int
	track domain.PlaybackExternalTrack
}

// externalSubtitles numbers the external subtitle files of a source after
// every embedded stream, as upstream does, in the stable order storage
// lists them. Files whose extension cannot appear in a route are left out.
func externalSubtitles(source domain.PlaybackSource) []externalSubtitle {
	next := 0
	for _, v := range source.Video {
		next = max(next, v.Index+1)
	}
	for _, a := range source.Audio {
		next = max(next, a.Index+1)
	}
	for _, s := range source.Subtitles {
		next = max(next, s.Index+1)
	}
	var out []externalSubtitle
	for _, track := range source.External {
		if track.Kind != domain.SidecarKindSubtitle || !routeToken(track.Format) {
			continue
		}
		out = append(out, externalSubtitle{index: next, track: track})
		next++
	}
	return out
}

func routeToken(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// subtitleURL is the upstream form of an external subtitle URL, relative to
// the server address the client was configured with. It carries no
// credential: clients authenticate it like every other request.
func subtitleURL(itemID, sourceID string, index int, format string) string {
	return fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/0/Stream.%s", itemID, sourceID, index, format)
}

// streamDirectKeys are the stream query members that never change the
// delivered bytes. Without static=true upstream sends a stream through its
// encoder, so any other member (a stream index, a start time, a context)
// is a request to produce a different stream.
var streamDirectKeys = []string{"static", "mediasourceid", "deviceid", "playsessionid", "tag", "container", "api_key", "apikey"}

// videoStream serves GET and HEAD /Videos/{itemId}/stream[.{container}]:
// the original file of one source of the item, through the server's direct
// delivery handler. The production guard has already refused every
// transformation parameter (codec, bit rate, geometry, segments, static set
// to anything but true, ...).
func (rt *router) videoStream(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	id, err := ParseID(chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	sourceID := ""
	if raw := q.get("mediasourceid"); raw != "" {
		if sourceID, err = ParseID(raw); err != nil {
			writeError(w, http.StatusBadRequest)
			return
		}
	}
	if q.get("static") == "" {
		for key := range q {
			if !slices.Contains(streamDirectKeys, key) {
				rt.opts.WriteRejection(w, r, media.ErrTranscodeDisabled)
				return
			}
		}
	}
	source, ok := rt.findSource(w, r, id, sourceID)
	if !ok {
		return
	}
	// A container other than the original's asks for a remux.
	for _, container := range []string{chi.URLParam(r, "container"), q.get("container")} {
		if container != "" && !sameContainer(container, source.Container) {
			rt.opts.WriteRejection(w, r, media.ErrTranscodeDisabled)
			return
		}
	}
	rt.opts.Library.Delivery.ServeSource(w, r, source.ID)
}

// findSource looks up the item's sources with the caller's session and
// picks the requested one, else the best. Missing and invisible items and
// sources get the configured hidden status.
func (rt *router) findSource(w http.ResponseWriter, r *http.Request, itemID, sourceID string) (domain.PlaybackSource, bool) {
	principal, ok := access.PrincipalFromContext(r.Context())
	if !ok || principal.Kind != access.ClientNative {
		writeError(w, http.StatusUnauthorized)
		return domain.PlaybackSource{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), rt.opts.Timeout)
	sources, err := rt.opts.Library.Catalog.PlaybackSources(ctx, rt.playbackActor(r, principal), itemID)
	cancel()
	if err != nil {
		rt.writeLibraryError(w, err)
		return domain.PlaybackSource{}, false
	}
	for _, source := range sources {
		if sourceID == "" || source.ID == sourceID {
			return source, true
		}
	}
	writeError(w, rt.opts.Library.HiddenStatus)
	return domain.PlaybackSource{}, false
}

// sameContainer compares a requested container with the source's,
// accepting the common aliases (matroska, ts, m2ts, m4v). An unknown
// source container matches nothing.
func sameContainer(requested, container string) bool {
	if container == "" {
		return false
	}
	caps, err := domain.NormalizeClientCapabilities(domain.ClientCapabilities{Containers: []string{requested}})
	return err == nil && caps.Containers[0] == container
}

// subtitleStream serves GET and HEAD of an external subtitle file as it
// is. The requested format must be the file's own; another format, a start
// offset or an end position asks for a converted subtitle and is refused.
// An embedded subtitle would have to be extracted from the container and is
// refused the same way.
func (rt *router) subtitleStream(w http.ResponseWriter, r *http.Request) {
	q := readQuery(r)
	itemID, err := ParseID(chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	sourceID, err := ParseID(chi.URLParam(r, "mediaSourceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil || index < 0 {
		writeError(w, http.StatusBadRequest)
		return
	}
	format := chi.URLParam(r, "format")
	if obsolete := q.get("format"); obsolete != "" {
		// Upstream lets the obsolete query member override the route.
		format = obsolete
	}
	for _, raw := range []string{chi.URLParam(r, "startPositionTicks"), q.get("startpositionticks")} {
		if raw == "" {
			continue
		}
		start, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest)
			return
		}
		if start != 0 {
			rt.opts.WriteRejection(w, r, media.ErrTranscodeDisabled)
			return
		}
	}
	if q.get("endpositionticks") != "" {
		rt.opts.WriteRejection(w, r, media.ErrTranscodeDisabled)
		return
	}
	if timeMap, _, err := q.boolean("addvtttimemap"); err != nil || timeMap {
		rt.opts.WriteRejection(w, r, media.ErrTranscodeDisabled)
		return
	}
	source, ok := rt.findSource(w, r, itemID, sourceID)
	if !ok {
		return
	}
	for _, sub := range externalSubtitles(source) {
		if sub.index != index {
			continue
		}
		if !sameSubtitleFormat(format, sub.track.Format) {
			rt.opts.WriteRejection(w, r, media.ErrTranscodeDisabled)
			return
		}
		rt.opts.Library.Delivery.ServeTrack(w, r, source.ID, media.TrackSubtitle, sub.track.ID)
		return
	}
	for _, sub := range ocrSubtitles(source) {
		if sub.index != index || !rt.ocrAvailable() {
			continue
		}
		// The SRT subtitle OCR derived from a bitmap track (G15.6), only in
		// its own format; until it exists it is answered like a missing one.
		if !sameSubtitleFormat(format, "srt") {
			rt.opts.WriteRejection(w, r, media.ErrTranscodeDisabled)
			return
		}
		rt.opts.Library.Delivery.ServeExtracted(w, r, rt.opts.Library.Extracted, source.ID, media.ExtractedOCRSubtitle, sub.track.Index)
		return
	}
	for _, sub := range source.Subtitles {
		if sub.Index != index {
			continue
		}
		// An extractable embedded text track in its own format is the
		// cached copy (G15.5); anything else would need a conversion.
		if extension, ok := domain.ExtractableSubtitleCodecs[sub.Codec]; ok && sub.Extractable && rt.extractionAvailable() && sameSubtitleFormat(format, extension) {
			rt.opts.Library.Delivery.ServeExtracted(w, r, rt.opts.Library.Extracted, source.ID, media.ExtractedSubtitle, index)
			return
		}
		rt.opts.WriteRejection(w, r, media.ErrTranscodeDisabled)
		return
	}
	writeError(w, rt.opts.Library.HiddenStatus)
}

// extractionAvailable reports whether embedded items can be delivered.
func (rt *router) extractionAvailable() bool {
	extracted := rt.opts.Library.Extracted
	if extracted == nil || rt.opts.Library.Delivery == nil {
		return false
	}
	if available, ok := extracted.(interface{ Available() bool }); ok {
		return available.Available()
	}
	return true
}

// attachmentURL is the upstream form of an attachment URL.
func attachmentURL(itemID, sourceID string, index int) string {
	return fmt.Sprintf("/Videos/%s/%s/Attachments/%d", itemID, sourceID, index)
}

// attachmentStream serves GET and HEAD of a font attachment copied out of a
// Matroska source as it is (G15.7). Index is the attachment's probe stream
// index, as MediaAttachments lists it.
func (rt *router) attachmentStream(w http.ResponseWriter, r *http.Request) {
	itemID, err := ParseID(chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	sourceID, err := ParseID(chi.URLParam(r, "mediaSourceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	raw := chi.URLParam(r, "index")
	index, err := strconv.Atoi(raw)
	if err != nil || index < 0 || index > 4096 || strconv.Itoa(index) != raw {
		writeError(w, http.StatusBadRequest)
		return
	}
	source, ok := rt.findSource(w, r, itemID, sourceID)
	if !ok {
		return
	}
	rt.opts.Library.Delivery.ServeExtracted(w, r, rt.opts.Library.Extracted, source.ID, media.ExtractedAttachmentStream, index)
}

// sameSubtitleFormat accepts the file extension itself or another name of
// the same format (vtt and webvtt, subrip and srt). Nothing is converted.
func sameSubtitleFormat(requested, extension string) bool {
	if strings.EqualFold(requested, extension) {
		return true
	}
	caps, err := domain.NormalizeClientCapabilities(domain.ClientCapabilities{SubtitleFormats: []string{requested, extension}})
	return err == nil && len(caps.SubtitleFormats) == 1
}

// audioStream answers the audio stream routes. The catalog holds no audio
// items, so after authentication every identifier is a missing item.
func (rt *router) audioStream(w http.ResponseWriter, r *http.Request) {
	if _, err := ParseID(chi.URLParam(r, "itemId")); err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	writeError(w, rt.opts.Library.HiddenStatus)
}

// textSubtitleFormats are the canonical subtitle formats that are text.
var textSubtitleFormats = map[string]bool{"srt": true, "ass": true, "ssa": true, "webvtt": true, "mov_text": true, "ttml": true, "sami": true, "text": true}
