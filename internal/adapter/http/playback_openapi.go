package httpapi

func playbackSpecification(paths, schemas map[string]any) {
	nonNegative := map[string]any{"type": "integer", "minimum": 0}
	boolean := map[string]any{"type": "boolean"}
	uuid := map[string]any{"type": "string", "format": "uuid"}
	array := func(items any) map[string]any { return map[string]any{"type": "array", "items": items} }
	token := map[string]any{"type": "string", "minLength": 1, "maxLength": 32, "pattern": "^[A-Za-z0-9._-]+$"}
	list := func(description string) map[string]any {
		return map[string]any{"type": "array", "maxItems": 32, "items": token, "description": description}
	}
	reason := map[string]any{"type": "string", "enum": []string{"container_unsupported", "source_not_probed", "video_codec_unsupported", "audio_codec_unsupported", "bitrate_exceeds_client"}}
	trackReason := map[string]any{"type": "string", "enum": []string{"audio_codec_unsupported", "subtitle_format_unsupported", "track_not_probed"}}

	schemas["PlaybackDelivery"] = objectSchema(map[string]any{
		"directPlay": map[string]any{"const": true}, "transcoding": map[string]any{"const": false},
		"hls": map[string]any{"const": false}, "dash": map[string]any{"const": false}, "remux": map[string]any{"const": false},
	}, "directPlay", "transcoding", "hls", "dash", "remux")
	schemas["PlaybackSource"] = mediaSourceSchema(true)
	schemas["ClientCapabilities"] = map[string]any{
		"type": "object", "additionalProperties": false,
		"description": "What the client can decode. These are declarations, not conversion requests: the server never converts, so a source the client cannot decode is only reported. Field names differ from the upstream transformation parameters, which stay rejected with 409 transcode_disabled anywhere in this body. Tokens are case-insensitive; common aliases are accepted (matroska→mkv, h265/hvc1→hevc, avc→h264, ec3→eac3, dca→dts, subrip→srt, vtt→webvtt, sup→pgs, idx→vobsub). An omitted or empty list declares nothing.",
		"properties": map[string]any{
			"containers":      list("Containers, for example mp4, mkv, webm, mov, avi, mpegts."),
			"videoCodecs":     list("Video codecs in probe vocabulary, for example h264, hevc, av1, vp9."),
			"audioCodecs":     list("Audio codecs, for example aac, ac3, eac3, dts, truehd, flac, opus; pcm covers every PCM sample format."),
			"subtitleFormats": list("Subtitle formats, for example srt, ass, ssa, webvtt, ttml, sami, pgs, vobsub, dvb, mov_text."),
			"maxBitrate":      map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000000000, "description": "Highest average bit rate the client sustains, in bits per second; 0 or omitted declares no ceiling. Only used to report bitrate_exceeds_client."},
		},
	}
	schemas["PlaybackDecision"] = objectSchema(map[string]any{
		"sourceId":   uuid,
		"directPlay": boolean,
		"code":       map[string]any{"type": "string", "enum": []string{"direct_play_unsupported"}, "description": "Present exactly when directPlay is false."},
		"reasons":    map[string]any{"type": "array", "items": reason, "description": "Empty exactly when directPlay is true, otherwise in the listed enum order. No conversion is ever suggested."},
		"tracks": array(objectSchema(map[string]any{
			"kind": map[string]any{"type": "string", "enum": []string{"audio", "subtitle"}}, "index": nonNegative, "id": uuid,
			"external": boolean, "supported": boolean, "reason": trackReason,
			"code": map[string]any{"type": "string", "enum": []string{"direct_play_unsupported"}, "description": "Present exactly when supported is false: the client cannot direct play this track. No conversion is offered in its place."},
			"url":  map[string]any{"type": "string", "description": "Direct delivery route of an external track's original file; absent for embedded streams."},
		}, "kind", "external", "supported")),
	}, "sourceId", "directPlay", "reasons", "tracks")

	common := func(op map[string]any) {
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["x-jelee-session"] = "native"
		op["parameters"] = []any{idParameter()}
		responses := op["responses"].(map[string]any)
		responses["403"] = map[string]any{"description": "web_playback_disabled for a web session; forbidden when hidden content is configured as 403."}
		responses["404"] = map[string]any{"description": "The item is missing or not visible to the caller; both are answered alike."}
		responses["409"] = map[string]any{"description": "transcode_disabled: the query or body carries a transformation parameter (G10.3)."}
	}
	info := operation("Describe the original resources of an item", "200", "400", "401", "403", "404", "408", "409", "413", "415", "503")
	info["description"] = "Native sessions only. Lists every media source of the item with container, size, duration, bit rate, version labels, embedded video, audio and subtitle streams from the current probe result, and external subtitle and audio files. Sources without a current probe result are listed with probed=false. No query parameters are accepted; transformation parameters are rejected with 409 before anything else."
	common(info)
	info["responses"].(map[string]any)["200"].(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": objectSchema(map[string]any{
		"itemId": uuid, "delivery": schemaRef("PlaybackDelivery"), "sources": array(schemaRef("PlaybackSource")),
	}, "itemId", "delivery", "sources")}, "data")}}
	paths["/api/v1/items/{id}/playback"] = map[string]any{"get": info}

	check := operation("Decide direct play against declared client capabilities", "200", "400", "401", "403", "404", "408", "409", "413", "415", "503")
	check["description"] = "Native sessions only. Decides for every source of the item whether the client can play the original as it is (G10.5): the container, the primary video codec and at least one embedded audio codec must be declared, and a known bit rate must not exceed maxBitrate. A source without a current probe result is never confirmed (source_not_probed). Subtitles and external audio never change the source verdict; each unreadable track is reported in tracks. The response is 200 even when nothing is playable; the server never offers a conversion."
	common(check)
	check["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef("ClientCapabilities")}}}
	check["responses"].(map[string]any)["200"].(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": objectSchema(map[string]any{
		"itemId": uuid, "delivery": schemaRef("PlaybackDelivery"),
		"directPlayable": map[string]any{"type": "boolean", "description": "True when at least one source is direct playable."},
		"decisions":      array(schemaRef("PlaybackDecision")),
	}, "itemId", "delivery", "directPlayable", "decisions")}, "data")}}
	paths["/api/v1/items/{id}/playback/check"] = map[string]any{"post": check}
}

// mediaSourceSchema describes one original resource. The playback form
// (withURL) lists the direct delivery route of each external track; the file
// information form has no URL at all, so a web client cannot obtain one.
func mediaSourceSchema(withURL bool) map[string]any {
	integer := map[string]any{"type": "integer"}
	nonNegative := map[string]any{"type": "integer", "minimum": 0}
	boolean := map[string]any{"type": "boolean"}
	str := map[string]any{"type": "string"}
	uuid := map[string]any{"type": "string", "format": "uuid"}
	rational := objectSchema(map[string]any{"numerator": nonNegative, "denominator": map[string]any{"type": "integer", "minimum": 1}}, "numerator", "denominator")
	array := func(items any) map[string]any { return map[string]any{"type": "array", "items": items} }
	external := map[string]any{
		"id": uuid, "kind": map[string]any{"type": "string", "enum": []string{"subtitle", "audio"}},
		"format":   map[string]any{"type": "string", "description": "File extension of the sidecar file."},
		"codec":    map[string]any{"type": "string", "description": "Canonical subtitle format or audio codec implied by the extension; absent for mka, m4a, ogg, oga and .sub, which only probing can tell."},
		"language": str, "languages": array(str), "title": str, "forced": boolean, "sdh": boolean, "default": boolean, "commentary": boolean,
		"charset":   map[string]any{"type": "string", "description": "Detected charset of a text subtitle. Reported only; the file is delivered unconverted."},
		"sizeBytes": nonNegative,
	}
	externalRequired := []string{"id", "kind", "format", "forced", "sdh", "default", "commentary", "sizeBytes"}
	if withURL {
		external["url"] = map[string]any{"type": "string", "description": "Direct delivery route of the original file: /api/v1/sources/{id}/subtitles/{trackId} or /api/v1/sources/{id}/audio/{trackId}. Native sessions only."}
		externalRequired = append(externalRequired, "url")
	}
	return objectSchema(map[string]any{
		"id":             uuid,
		"container":      map[string]any{"type": "string", "enum": []string{"mp4", "mkv", "webm", "mov", "avi", "mpegts"}, "description": "Container token derived from the stored content type."},
		"contentType":    str,
		"probed":         map[string]any{"type": "boolean", "description": "False when no current probe result exists (never probed, failed, expired, or the file changed since). Stream lists are then empty and the version labels come from the file name only."},
		"sizeBytes":      nonNegative,
		"durationMicros": nonNegative,
		"bitRate":        map[string]any{"type": "integer", "minimum": 1, "description": "Bits per second as probed, or size × 8 ÷ duration when the container states none."},
		"version": map[string]any{"type": "object", "description": "G20.2 version labels with qualityScore and displayName; sources are listed by qualityScore descending. Other label fields may be added.",
			"properties": map[string]any{"displayName": str, "qualityScore": integer}, "required": []string{"displayName", "qualityScore"}, "additionalProperties": true},
		"videoTracks": array(objectSchema(map[string]any{
			"index": nonNegative, "codec": str, "profile": str, "level": integer, "width": integer, "height": integer,
			"frameRate": rational, "bitRate": integer, "default": boolean,
			"primary": map[string]any{"type": "boolean", "description": "The stream the direct play decision checks. Cover art is not listed."},
		}, "index", "default", "primary")),
		"audioTracks": array(objectSchema(map[string]any{
			"index": nonNegative, "codec": str, "profile": str, "language": str, "channels": integer, "channelLayout": str,
			"sampleRate": integer, "bitRate": integer, "default": boolean, "forced": boolean, "atmos": boolean,
		}, "index", "default", "forced", "atmos")),
		"subtitleTracks": array(objectSchema(map[string]any{
			"index": nonNegative, "codec": str, "format": map[string]any{"type": "string", "description": "Canonical format clients declare: srt, ass, ssa, webvtt, mov_text, pgs, vobsub, dvb, eia_608 or text."},
			"language": str, "default": boolean, "forced": boolean,
		}, "index", "default", "forced")),
		"externalTracks": array(objectSchema(external, externalRequired...)),
	}, "id", "container", "contentType", "probed", "version", "videoTracks", "audioTracks", "subtitleTracks", "externalTracks")
}
