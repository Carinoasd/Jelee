package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// DecodeJSON accepts exactly one JSON object with known fields. Body storage is
// bounded by maxBytes, nesting by 64 levels, and null values and duplicate keys are
// rejected at every level, including case-folded aliases Go struct decoding
// treats as the same field. Errors never contain body text or field names.
// The caller owns the request body and its transport read deadline.
func DecodeJSON(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	return decodeJSON(w, r, target, maxBytes, nil)
}

// Manual facts allow clears and nullable structured values. Domain validation
// checks that nullable members belong to the selected fact type.
func decodeItemMetadataJSON(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	return decodeJSON(w, r, target, maxBytes, factNullable)
}

func factNullable(path string) bool {
	return path == "/FACTS/*/VALUE" || path == "/FACTS/*/VALUE/*/ORDER" || path == "/FACTS/*/VALUE/*/MAX" || path == "/FACTS/*/VALUE/*/VOTES" || path == "/FACTS/*/VALUE/*/SEASON"
}

// decodeJSONNullable is DecodeJSON with null accepted where nullable
// reports true. Paths are slash-separated case-folded keys (upper case for
// ASCII) with "*" for array elements, for example "/SETTINGS/X.Y/KEY".
func decodeJSONNullable(w http.ResponseWriter, r *http.Request, target any, maxBytes int64, nullable func(string) bool) error {
	return decodeJSON(w, r, target, maxBytes, nullable)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, maxBytes int64, nullable func(string) bool) error {
	if r == nil || r.Body == nil || target == nil || maxBytes < 1 {
		return domain.ErrInvalid
	}
	if err := r.Context().Err(); err != nil {
		return err
	}
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return media.ErrUnsupportedMediaType
	}
	contentType, params, err := mime.ParseMediaType(values[0])
	if err != nil || contentType != "application/json" || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		return media.ErrUnsupportedMediaType
	}
	if r.ContentLength > maxBytes {
		return media.ErrBodyTooLarge
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	body, err := io.ReadAll(r.Body)
	if ctxErr := r.Context().Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return media.ErrBodyTooLarge
		}
		return domain.ErrInvalid
	}
	if !utf8.Valid(body) {
		return domain.ErrInvalid
	}
	validator := json.NewDecoder(bytes.NewReader(body))
	validator.UseNumber()
	first, err := validator.Token()
	if err != nil || first != json.Delim('{') || !checkJSONObjectAt(validator, 1, "", nullable) {
		return domain.ErrInvalid
	}
	if _, err := validator.Token(); err != io.EOF {
		return domain.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domain.ErrInvalid
	}
	return r.Context().Err()
}

func checkJSONObject(decoder *json.Decoder, depth int) bool {
	return checkJSONObjectAt(decoder, depth, "", nil)
}

func checkJSONObjectAt(decoder *json.Decoder, depth int, path string, nullable func(string) bool) bool {
	if depth > 64 {
		return false
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		key = foldJSONKey(key)
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
		if !checkJSONValueAt(decoder, depth, path+"/"+key, nullable) {
			return false
		}
	}
	end, err := decoder.Token()
	return err == nil && end == json.Delim('}')
}

func checkJSONValue(decoder *json.Decoder, parentDepth int) bool {
	return checkJSONValueAt(decoder, parentDepth, "", nil)
}

func checkJSONValueAt(decoder *json.Decoder, parentDepth int, path string, nullable func(string) bool) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	if token == nil {
		return nullable != nil && nullable(path)
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return true
	}
	depth := parentDepth + 1
	if depth > 64 {
		return false
	}
	switch delim {
	case '{':
		return checkJSONObjectAt(decoder, depth, path, nullable)
	case '[':
		for decoder.More() {
			if !checkJSONValueAt(decoder, depth, path+"/*", nullable) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}

// Use the smallest rune in each Unicode simple-fold cycle, matching the
// equivalence used by encoding/json (including the Kelvin sign and long s).
func foldJSONKey(key string) string {
	return strings.Map(func(r rune) rune {
		smallest := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < smallest {
				smallest = next
			}
		}
		return smallest
	}, key)
}
