package httpapi

import (
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

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

	schemas["VersionSelection"] = objectSchema(map[string]any{
		"defaultSourceId": map[string]any{"type": "string", "format": "uuid", "description": "The version a client should start with; absent when no version is playable."},
		"reason": map[string]any{"type": "string", "enum": []string{"only_version", "only_playable", "bandwidth_preference", "resolution_preference", "dynamic_range_preference", "size_preference", "primary_version", "quality_score", "stable_order", "none_playable", "no_version"},
			"description": "The rule that put the default ahead of the next playable version (G20.2), in precedence order: the client's declared capabilities (only_playable), the user's bandwidth, resolution, dynamic range and size preferences, the main version, the quality score, the source ID."},
		"preferences": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"bandwidth", "resolution", "dynamic_range", "size"}}, "description": "The caller's version preference rules that are set."},
		"excluded":    array(objectSchema(map[string]any{"sourceId": uuid, "reasons": array(reason)}, "sourceId", "reasons")),
	}, "reason", "preferences", "excluded")
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
	info["description"] = "Native sessions only. Lists every media source of the item, in the caller's default version order with the selection that explains the default (G20.2), with container, size, duration, bit rate, version labels, embedded video, audio and subtitle streams from the current probe result, and external subtitle and audio files. Sources without a current probe result are listed with probed=false. No query parameters are accepted; transformation parameters are rejected with 409 before anything else."
	common(info)
	info["responses"].(map[string]any)["200"].(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": objectSchema(map[string]any{
		"itemId": uuid, "delivery": schemaRef("PlaybackDelivery"),
		"sources":   map[string]any{"type": "array", "items": schemaRef("PlaybackSource"), "description": "In the caller's default version order: the user's version preference, then the main version, the quality score and the source ID."},
		"selection": schemaRef("VersionSelection"),
	}, "itemId", "delivery", "sources", "selection")}, "data")}}
	paths["/api/v1/items/{id}/playback"] = map[string]any{"get": info}

	check := operation("Decide direct play against declared client capabilities", "200", "400", "401", "403", "404", "408", "409", "413", "415", "503")
	check["description"] = "Native sessions only. Decides for every source of the item whether the client can play the original as it is (G10.5): the container, the primary video codec and at least one embedded audio codec must be declared, and a known bit rate must not exceed maxBitrate. A source without a current probe result is never confirmed (source_not_probed). Subtitles and external audio never change the source verdict; each unreadable track is reported in tracks. The response is 200 even when nothing is playable; the server never offers a conversion."
	common(check)
	check["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef("ClientCapabilities")}}}
	check["responses"].(map[string]any)["200"].(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": objectSchema(map[string]any{
		"itemId": uuid, "delivery": schemaRef("PlaybackDelivery"),
		"directPlayable": map[string]any{"type": "boolean", "description": "True when at least one source is direct playable."},
		"decisions":      array(schemaRef("PlaybackDecision")),
		"selection":      map[string]any{"allOf": []any{schemaRef("VersionSelection")}, "description": "The default version among the direct playable ones; the declaration only excludes, it never makes a conversion acceptable."},
	}, "itemId", "delivery", "directPlayable", "decisions", "selection")}, "data")}}
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
		"codec":    map[string]any{"type": "string", "description": "The codec the sandboxed probe found once probeState is ready; before that the canonical subtitle format or audio codec implied by the extension, or for .sub the content check (microdvd or vobsub). Absent when only probing can tell (mka, m4a, ogg, oga, or a failed probe)."},
		"language": str, "languages": array(str), "title": str, "forced": boolean, "sdh": boolean, "default": boolean, "commentary": boolean,
		"charset":   map[string]any{"type": "string", "description": "Detected charset of a text subtitle. Reported only; the file is delivered unconverted."},
		"sizeBytes": nonNegative,
		"probeState": map[string]any{"type": "string", "enum": []string{domain.SidecarProbePending, domain.SidecarProbeReady, domain.SidecarProbeFailed, domain.SidecarProbeNotApplicable},
			"description": "Sandboxed probe of an audio, .sub or .sup file: pending until a catalog sync probes it, failed when it could not be read (the track stays listed, its facts unknown), not_applicable for text subtitles."},
		"profile":        map[string]any{"type": "string", "description": "Probed audio profile, such as DTS-HD MA."},
		"channels":       map[string]any{"type": "integer", "minimum": 1, "maximum": 256},
		"channelLayout":  str,
		"sampleRate":     map[string]any{"type": "integer", "minimum": 1},
		"bitRate":        map[string]any{"type": "integer", "minimum": 1},
		"durationMicros": nonNegative,
		"atmos":          map[string]any{"type": "boolean", "description": "Probed E-AC-3 or TrueHD audio whose profile names Dolby Atmos."},
		"pairedTrackId":  map[string]any{"type": "string", "format": "uuid", "description": "On a VobSub .idx track: the .sub data file with the same name, delivered through the same subtitle route. That .sub is not listed as a track of its own."},
	}
	externalRequired := []string{"id", "kind", "format", "forced", "sdh", "default", "commentary", "sizeBytes", "probeState"}
	if withURL {
		external["url"] = map[string]any{"type": "string", "description": "Direct delivery route of the original file: /api/v1/sources/{id}/subtitles/{trackId} or /api/v1/sources/{id}/audio/{trackId}. Native sessions only."}
		externalRequired = append(externalRequired, "url")
	}
	subtitleTrack := map[string]any{
		"index": nonNegative, "codec": str, "format": map[string]any{"type": "string", "description": "Canonical format clients declare: srt, ass, ssa, webvtt, mov_text, pgs, vobsub, dvb, eia_608 or text."},
		"language": str, "default": boolean, "forced": boolean,
		"sdh":         map[string]any{"type": "boolean", "description": "The hearing_impaired or captions disposition of the embedded track."},
		"charset":     map[string]any{"type": "string", "description": "UTF-8 for an embedded text track; absent for bitmap and closed-caption tracks."},
		"title":       map[string]any{"type": "string", "description": "The track's own title tag, else the Matroska track name from the optional MediaInfo supplement. At most 256 bytes of plain text."},
		"extractable": map[string]any{"type": "boolean", "description": "A SubRip, ASS, SSA, WebVTT, PGS or VobSub track inside a Matroska/WebM source that the optional mkvtoolnix runtime can copy unconverted (G15.5). False for DVB bitmaps, MP4 mov_text and every track of another container: those are delivered only inside the original file. Bitmap tracks are copied as pictures; nothing is recognized, rendered or converted."},
	}
	attachment := map[string]any{
		"id":          map[string]any{"type": "integer", "minimum": 1, "description": "1-based Matroska attachment ID."},
		"streamIndex": map[string]any{"type": "integer", "minimum": 0, "description": "Probe stream index of the same attachment, when the probe listed exactly as many attachment streams."},
		"fileName":    str, "font": boolean,
	}
	if withURL {
		subtitleTrack["url"] = map[string]any{"type": "string", "description": "Present when extraction is enabled and available: /api/v1/sources/{id}/embedded-subtitles/{index}, or /api/v1/sources/{id}/embedded-subtitles/{index}.idx for VobSub. Native sessions only."}
		subtitleTrack["dataUrl"] = map[string]any{"type": "string", "description": "VobSub only, with url: /api/v1/sources/{id}/embedded-subtitles/{index}.sub, the program stream next to the .idx. Native sessions only."}
		attachment["url"] = map[string]any{"type": "string", "description": "Present for fonts when extraction is enabled and available: /api/v1/sources/{id}/attachments/{attachmentId}. Native sessions only."}
		ocr := objectSchema(map[string]any{
			"format": map[string]any{"type": "string", "enum": []string{"srt"}},
			"title":  map[string]any{"type": "string", "description": "The bitmap track's title or language followed by \" (OCR)\", marking the text as recognized, not authored."},
			"url":    map[string]any{"type": "string", "description": "/api/v1/sources/{id}/ocr-subtitles/{index}. Native sessions only."},
		}, "format", "title", "url")
		subtitleTrack["ocr"] = ocr
		ocr["description"] = "Present on a PGS or VobSub track of a Matroska source once subtitle OCR (G15.6, off by default) has derived an additional SRT from it. Listing playback info queues the source for OCR; the bitmap track itself is unchanged and still delivered as it is."
	}
	properties := map[string]any{
		"id":             uuid,
		"container":      map[string]any{"type": "string", "enum": []string{"mp4", "mkv", "webm", "mov", "avi", "mpegts"}, "description": "Container token derived from the stored content type."},
		"contentType":    str,
		"probed":         map[string]any{"type": "boolean", "description": "False when no current probe result exists (never probed, failed, expired, or the file changed since). Stream lists are then empty and the version labels come from the file name only."},
		"sizeBytes":      nonNegative,
		"durationMicros": nonNegative,
		"bitRate":        map[string]any{"type": "integer", "minimum": 1, "description": "Bits per second as probed, or size × 8 ÷ duration when the container states none."},
		"version": map[string]any{"type": "object", "description": "G20.2 version labels with qualityScore and displayName. Playback information lists sources in the caller's default version order (version preference, main version, qualityScore descending). Other label fields may be added.",
			"properties": map[string]any{"displayName": str, "qualityScore": integer}, "required": []string{"displayName", "qualityScore"}, "additionalProperties": true},
		"videoTracks": array(objectSchema(map[string]any{
			"index": nonNegative, "codec": str, "profile": str, "level": integer, "width": integer, "height": integer,
			"frameRate": rational, "bitRate": integer, "default": boolean,
			"hdr":                map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"dolby_vision", "hdr10plus", "hdr10", "hlg"}}, "description": "HDR formats, strongest first. ffprobe's findings come first; the optional MediaInfo supplement only adds formats the probe did not name."},
			"dolbyVisionProfile": integer, "dolbyVisionLevel": integer,
			"primary": map[string]any{"type": "boolean", "description": "The stream the direct play decision checks. Cover art is not listed."},
		}, "index", "default", "primary")),
		"audioTracks": array(objectSchema(map[string]any{
			"index": nonNegative, "codec": str, "profile": str, "language": str, "channels": integer, "channelLayout": str,
			"sampleRate": integer, "bitRate": integer, "default": boolean, "forced": boolean, "atmos": boolean,
			"title":          map[string]any{"type": "string", "description": "The track's own title tag, else the Matroska track name from the optional MediaInfo supplement. At most 256 bytes of plain text."},
			"commentary":     map[string]any{"type": "boolean", "description": "The comment disposition: a commentary track."},
			"visualImpaired": map[string]any{"type": "boolean", "description": "The visual_impaired or descriptions disposition: an audio description track."},
			"features":       map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"dolby_atmos", "dolby_truehd", "dts_x", "dts_hd_ma"}}, "description": "Commercial audio formats from the probe's codec and profile, plus the optional MediaInfo supplement."},
		}, "index", "default", "forced", "atmos", "commentary", "visualImpaired")),
		"subtitleTracks": array(objectSchema(subtitleTrack, "index", "default", "forced", "sdh")),
		"attachments":    array(objectSchema(attachment, "id", "fileName", "font")),
		"chapters": map[string]any{"type": "array", "description": "Container chapters in start order (G09.1). Start times come from the probe; titles from the optional MediaInfo supplement when it lists the same chapters. Absent when the file has none.",
			"items": objectSchema(map[string]any{"index": nonNegative, "startMicros": nonNegative, "title": str}, "index", "startMicros")},
		"externalTracks": array(objectSchema(external, externalRequired...)),
		"primary":        map[string]any{"type": "boolean", "description": "The administrator's main version (G20.3); listed first, then by qualityScore."},
	}
	if withURL {
		selection := map[string]any{"oneOf": []any{objectSchema(map[string]any{
			"kind":  map[string]any{"type": "string", "enum": []string{"embedded", "external"}},
			"index": map[string]any{"type": "integer", "minimum": 0, "description": "Stream index of an embedded track."},
			"id":    map[string]any{"type": "string", "format": "uuid", "description": "ID of an external track (externalTracks)."},
		}, "kind"), map[string]any{"type": "null"}}}
		properties["defaultTracks"] = objectSchema(map[string]any{
			"audio":    selection,
			"subtitle": map[string]any{"oneOf": selection["oneOf"], "description": "null: start without subtitles."},
			"basis":    map[string]any{"type": "string", "enum": []string{"version", "item", "user", "locale", "source"}, "description": "The most specific preference level that decided; locale when only the account language applied, source when the file's own default flags did."},
		}, "audio", "subtitle", "basis")
		properties["defaultTracks"].(map[string]any)["description"] = "Tracks the caller's preferences pick (G16.5, G20.4). A client may start with others; nothing is converted."
	}
	required := []string{"id", "container", "contentType", "probed", "version", "videoTracks", "audioTracks", "subtitleTracks", "externalTracks", "primary"}
	// A part is described like a version of its own (G21.1), without the
	// version-level members.
	part := make(map[string]any, len(properties)+2)
	for k, v := range properties {
		if k != "defaultTracks" {
			part[k] = v
		}
	}
	part["number"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 99, "description": "1-based play order; part 1 is the version itself."}
	part["startMicros"] = map[string]any{"type": "integer", "minimum": 0, "description": "Where the part begins on the version's combined timeline (sum of the earlier parts' durations); absent when an earlier part's duration is unknown."}
	properties["parts"] = map[string]any{"type": "array", "minItems": 2, "items": objectSchema(part, append(slices.Clone(required), "number")...),
		"description": "Every file of a multi-part version (G21.1: movie.cd1.mkv, movie.cd2.mkv in one directory) in play order; absent for a single file. The source's other members describe part 1. Parts are played one after the other, each delivered as it is from its own source routes; nothing is joined or converted. Playback progress of such a version is a position on the combined timeline."}
	properties["totalDurationMicros"] = map[string]any{"type": "integer", "minimum": 0, "description": "Combined duration of all parts; absent for a single file or when a part's duration is unknown."}
	return objectSchema(properties, required...)
}
