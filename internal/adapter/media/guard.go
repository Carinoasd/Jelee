package media

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const maxPlaybackBody = 64 << 10

// IsForbiddenDeliveryRoute can run before routing, including on unknown paths.
// RawPath/encoded forms are accepted for defense against proxy decoding variance.
// A .ts original-file stream is allowed; HLS route segments are always rejected.
func IsForbiddenDeliveryRoute(route string) bool {
	decoded, err := decodeRoute(route)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(strings.ToLower(strings.ReplaceAll(decoded, "\\", "/")), "/") {
		switch part {
		case "hls", "hls1", "dash", "transcode", "transcoding", "segment", "segments":
			return true
		}
		if strings.HasSuffix(part, ".m3u8") || strings.HasSuffix(part, ".mpd") || strings.HasSuffix(part, ".m4s") {
			return true
		}
	}
	return false
}

func decodeRoute(value string) (string, error) {
	if len(value) > 16<<10 {
		return "", ErrInvalidRequest
	}
	for i := 0; i < 8; i++ {
		if !strings.Contains(value, "%") {
			return value, nil
		}
		next, err := url.PathUnescape(value)
		if err != nil {
			return "", ErrInvalidRequest
		}
		if next == value {
			return value, nil
		}
		value = next
	}
	return "", ErrInvalidRequest
}

// GuardProduction rejects requests for transformations on a playback surface.
// Do not mount it on metadata or search endpoints: codec fields there are data.
// Accepted JSON/form bodies are restored byte for byte for the next handler.
func GuardProduction(r *http.Request) error { return guard(r, nil) }

// GuardPlaybackInfo is GuardProduction for the upstream PlaybackInfo request,
// whose documented members (playbackInfoDeclarations) state what the client
// can play or would accept: a bit rate ceiling, stream preferences, the
// direct/transcode switches and the DeviceProfile. They decide only whether
// an original can be offered, never what is delivered, so they are not
// inspected as transformation parameters; the DeviceProfile subtree is still
// parsed for syntax, depth and size. Every other member, in the query, the
// form or the JSON body (nested included), is inspected exactly as
// GuardProduction does, so a real transformation request (videoCodec,
// segmentContainer, static=false, transcodingProtocol, ...) on PlaybackInfo
// is still 409. Mount it on the PlaybackInfo route only; the stream routes
// keep GuardProduction.
func GuardPlaybackInfo(r *http.Request) error { return guard(r, playbackInfoDeclarations) }

// GuardPlaybackReport is GuardProduction for the upstream playback state
// reports (playing, progress, stopped). Their JSON body only describes what
// the client is doing (position, pause state, the item it shows, its queue,
// the method it believes it uses) and never selects what the server
// delivers, so it is checked for syntax, depth and size only; upstream
// clients include members such as MaxStreamingBitrate and whole item
// descriptions with transcoding fields there. The path, the query and a form
// body are inspected exactly as GuardProduction does. Mount it on the report
// routes only.
func GuardPlaybackReport(r *http.Request) error { return guardMode(r, nil, inspectSyntax) }

// playbackInfoDeclarations are the members of the upstream PlaybackInfo
// query and request body (media info controller, PlaybackInfoDto) that are
// declarations, not requests: upstream itself only uses them to choose a
// play method, and this server only to decide direct play.
var playbackInfoDeclarations = normalizedSet([]string{
	"userId", "mediaSourceId", "liveStreamId", "autoOpenLiveStream", "startTimeTicks",
	"audioStreamIndex", "subtitleStreamIndex", "maxStreamingBitrate", "maxAudioChannels",
	"enableDirectPlay", "enableDirectStream", "enableTranscoding", "allowVideoStreamCopy", "allowAudioStreamCopy",
	"alwaysBurnInSubtitleWhenTranscoding", deviceProfileKey,
})

const deviceProfileKey = "deviceProfile"

func guard(r *http.Request, declarations map[string]bool) error {
	return guardMode(r, declarations, inspectAll)
}

// guardMode is guard with the inspection applied to a JSON body.
func guardMode(r *http.Request, declarations map[string]bool, jsonMode inspectMode) error {
	if IsForbiddenDeliveryRoute(r.URL.EscapedPath()) {
		return ErrTranscodeDisabled
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return ErrInvalidRequest
	}
	if err := inspectValues(values, declarations); err != nil {
		return err
	}
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	if r.ContentLength > maxPlaybackBody {
		return ErrBodyTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPlaybackBody+1))
	if err != nil {
		return ErrInvalidRequest
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > maxPlaybackBody {
		return ErrBodyTooLarge
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return ErrUnsupportedMediaType
	}
	switch strings.ToLower(contentType) {
	case "application/json":
		return inspectJSON(body, jsonMode, declarations)
	case "application/x-www-form-urlencoded":
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return ErrInvalidRequest
		}
		return inspectValues(values, declarations)
	default:
		return ErrUnsupportedMediaType
	}
}

func normalizeKey(key string) string {
	key = strings.ToLower(key)
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(key)
}

// Parameter names below keep the upstream spelling so reviews can diff them
// against the upstream API controllers (video stream, audio stream, dynamic
// HLS, media info/PlaybackInfo, universal audio), the streaming request DTOs,
// the encoding job options and the transcoding profile model. Matching is
// case-insensitive and ignores '-', '_' and spaces (see normalizeKey).
var (
	// transformParams only steer the encoder, the remuxer or the segmenter, so
	// their presence with any value is a request to change the delivered bytes.
	transformParams = []string{
		// Codec selection, including "copy", which is remuxing (off by default).
		"videoCodec", "audioCodec", "subtitleCodec",
		// Bitrate ceilings and targets.
		"maxVideoBitrate", "maxAudioBitrate", "maxStreamingBitrate", "videoBitRate", "audioBitRate",
		// Geometry, frame rate and video encoder constraints.
		"width", "height", "maxWidth", "maxHeight", "framerate", "maxFramerate",
		"profile", "level", "videoProfile", "videoLevel", "videoRangeType", "codecTag", "rotation",
		"maxRefFrames", "maxVideoBitDepth", "videoBitDepth", "requireAvc", "requireNonAnamorphic", "deInterlace",
		// Audio encoder constraints.
		"audioSampleRate", "maxAudioSampleRate", "audioChannels", "maxAudioChannels",
		"maxAudioBitDepth", "audioBitDepth", "transcodingMaxAudioChannels", "transcodingAudioChannels",
		"enableAudioVbrEncoding",
		// Muxer and timestamp handling.
		"copyTimestamps", "breakOnNonKeyFrames", "enableMpegtsM2TsMode", "estimateContentLength", "cpuCoreLimit",
		// Positional legacy encoder settings ("params" is split on ';').
		"params",
		// Segmenting, adaptive streaming and manifest-embedded subtitles.
		"minSegments", "actualSegmentLengthTicks", "enableAdaptiveBitrateStreaming", "enableSubtitlesInManifest",
		"alwaysBurnInSubtitleWhenTranscoding",
		// Transcode bookkeeping.
		"transcodeReasons", "transcodingReasons",
	}
	// directDefaultTrue are switches whose true value (or the upstream default
	// when omitted/null) means original delivery; any other value disables it.
	directDefaultTrue = []string{"enableDirectPlay", "enableDirectStream", "allowVideoStreamCopy", "allowAudioStreamCopy", "enableAutoStreamCopy"}
	// staticParam is false by default upstream, so only an explicit true is direct.
	staticParam = "static"
	// streamOptionNames are codec-qualified options ("h264-profile=high") the
	// upstream request parser forwards from any lower-case query key.
	streamOptionNames = []string{"profile", "level", "rangeType", "codecTag", "rotation", "maxRefFrames", "videoBitDepth", "audioBitDepth", "audioChannels", "deInterlace"}
	// forbiddenPrefixes catch whole families such as segmentLength,
	// segmentContainer, hlsSegmentLength, transcodingProtocol and
	// transcodingContainer.
	forbiddenPrefixes = []string{"segment", "hls", "dash", "transcode", "transcoding"}

	transformSet     = normalizedSet(transformParams)
	directDefaultSet = normalizedSet(directDefaultTrue)
	streamOptionSet  = normalizedSet(streamOptionNames)
)

func normalizedSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[normalizeKey(name)] = true
	}
	return set
}

func forbiddenField(rawKey string, value any) bool {
	key := normalizeKey(rawKey)
	if transformSet[key] {
		return true
	}
	if directDefaultSet[key] {
		return !isTrue(value) && !isNullish(value)
	}
	if i := strings.LastIndexByte(rawKey, '-'); i > 0 && streamOptionSet[normalizeKey(rawKey[i+1:])] {
		return true
	}
	switch key {
	case "transcodingprofiles":
		profiles, ok := value.([]any)
		return !ok || len(profiles) != 0
	case "enabletranscoding":
		// Explicitly disabling transcoding is safe; the upstream default is on.
		return !isFalse(value)
	case staticParam:
		return !isTrue(value)
	case "protocol", "streamingprotocol", "container":
		text, _ := value.(string)
		text = strings.ToLower(strings.TrimSpace(text))
		return text == "hls" || text == "dash" || text == "m3u8" || text == "mpd"
	case "subtitlemethod", "subtitledeliverymethod":
		// External sidecar files and tracks already embedded in the original
		// file are direct; Encode/BurnIn, Hls and Drop need a new output.
		// Numeric forms are the upstream enum values Embed=1 and External=2.
		switch v := value.(type) {
		case nil:
			return false
		case string:
			switch normalizeKey(strings.TrimSpace(v)) {
			case "", "external", "embed", "1", "2":
				return false
			}
		case float64:
			return v != 1 && v != 2
		}
		return true
	}
	for _, prefix := range forbiddenPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func isFalse(value any) bool {
	switch v := value.(type) {
	case bool:
		return !v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "false") || strings.TrimSpace(v) == "0"
	}
	return false
}

func isTrue(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true") || strings.TrimSpace(v) == "1"
	}
	return false
}

func isNullish(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	}
	return false
}

// inspectValues checks query or form members. A member named in
// declarations is not a transformation parameter; a DeviceProfile value is
// then still parsed, as data.
func inspectValues(values url.Values, declarations map[string]bool) error {
	for key, entries := range values {
		normalized := normalizeKey(key)
		declared := declarations[normalized]
		for _, value := range entries {
			if !declared && forbiddenField(key, value) {
				return ErrTranscodeDisabled
			}
			if normalized == "deviceprofile" {
				mode := inspectAll
				if declared {
					mode = inspectSyntax
				}
				if err := inspectJSON([]byte(value), mode, nil); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// inspectMode selects which JSON members are checked for transformation
// parameters.
type inspectMode int

const (
	// inspectAll checks every member at every depth.
	inspectAll inspectMode = iota
	// inspectSyntax checks only syntax, depth and duplicate-safe decoding:
	// the value is a declaration (a DeviceProfile on PlaybackInfo).
	inspectSyntax
)

// inspectJSON checks a document. With declarations, members of the top-level
// object named there are declarations: they are not transformation
// parameters, the DeviceProfile subtree is checked for syntax only, and any
// other nested value is still checked in full.
func inspectJSON(body []byte, mode inspectMode, declarations map[string]bool) error {
	// Decode tokens instead of maps so repeated JSON keys cannot hide an earlier
	// transformation request. Limit both nesting and total document bytes.
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := inspectJSONValue(decoder, 0, mode, declarations); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalidRequest
	}
	return nil
}

// inspectJSONValue checks one value. declarations applies to the members of
// this object only and is never passed to children.
func inspectJSONValue(decoder *json.Decoder, depth int, mode inspectMode, declarations map[string]bool) error {
	if depth > 32 {
		return ErrInvalidRequest
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrInvalidRequest
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return ErrInvalidRequest
			}
			key, ok := token.(string)
			if !ok {
				return ErrInvalidRequest
			}
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return ErrInvalidRequest
			}
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return ErrInvalidRequest
			}
			childMode, declared := mode, declarations[normalizeKey(key)]
			if mode == inspectAll && !declared && forbiddenField(key, value) {
				return ErrTranscodeDisabled
			}
			if declared && normalizeKey(key) == "deviceprofile" {
				childMode = inspectSyntax
			}
			child := json.NewDecoder(bytes.NewReader(raw))
			child.UseNumber()
			if err := inspectJSONValue(child, depth+1, childMode, nil); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := inspectJSONValue(decoder, depth+1, mode, nil); err != nil {
				return err
			}
		}
	default:
		return ErrInvalidRequest
	}
	if _, err := decoder.Token(); err != nil {
		return ErrInvalidRequest
	}
	return nil
}
