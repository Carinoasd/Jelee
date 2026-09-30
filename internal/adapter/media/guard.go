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
func GuardProduction(r *http.Request) error {
	if IsForbiddenDeliveryRoute(r.URL.EscapedPath()) {
		return ErrTranscodeDisabled
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return ErrInvalidRequest
	}
	if err := inspectValues(values); err != nil {
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
		return inspectJSON(body)
	case "application/x-www-form-urlencoded":
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return ErrInvalidRequest
		}
		return inspectValues(values)
	default:
		return ErrUnsupportedMediaType
	}
}

func normalizeKey(key string) string {
	key = strings.ToLower(key)
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(key)
}

func forbiddenField(key string, value any) bool {
	key = normalizeKey(key)
	switch key {
	case "videocodec", "audiocodec", "maxvideobitrate", "maxaudiobitrate", "maxstreamingbitrate", "videobitrate", "audiobitrate", "transcodereasons", "transcodingreasons", "maxwidth", "maxheight", "width", "height", "framerate", "maxframerate", "videoprofile", "videolevel":
		return true
	case "transcodingprofiles":
		profiles, ok := value.([]any)
		return !ok || len(profiles) != 0
	case "enabletranscoding", "allowvideostreamcopy", "allowaudiostreamcopy":
		// Explicitly disabling transcoding or permitting stream copy is safe.
		if key == "enabletranscoding" {
			return !isFalse(value)
		}
		return isFalse(value)
	case "static":
		return isFalse(value)
	case "protocol", "streamingprotocol", "container":
		text, _ := value.(string)
		text = strings.ToLower(strings.TrimSpace(text))
		return text == "hls" || text == "dash" || text == "m3u8" || text == "mpd"
	case "subtitlemethod", "subtitledeliverymethod":
		text, _ := value.(string)
		return strings.EqualFold(text, "encode") || strings.EqualFold(text, "burnin") || strings.EqualFold(text, "burn-in")
	}
	return strings.HasPrefix(key, "segment") || strings.HasPrefix(key, "hls") || strings.HasPrefix(key, "dash") || strings.HasPrefix(key, "transcode") || strings.HasPrefix(key, "transcoding")
}

func isFalse(value any) bool {
	switch v := value.(type) {
	case bool:
		return !v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "false") || v == "0"
	}
	return false
}

func inspectValues(values url.Values) error {
	for key, entries := range values {
		for _, value := range entries {
			if forbiddenField(key, value) {
				return ErrTranscodeDisabled
			}
			if normalizeKey(key) == "deviceprofile" {
				if err := inspectJSON([]byte(value)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func inspectJSON(body []byte) error {
	// Decode tokens instead of maps so repeated JSON keys cannot hide an earlier
	// transformation request. Limit both nesting and total document bytes.
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := inspectJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalidRequest
	}
	return nil
}

func inspectJSONValue(decoder *json.Decoder, depth int) error {
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
			if forbiddenField(key, value) {
				return ErrTranscodeDisabled
			}
			child := json.NewDecoder(bytes.NewReader(raw))
			child.UseNumber()
			if err := inspectJSONValue(child, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := inspectJSONValue(decoder, depth+1); err != nil {
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
