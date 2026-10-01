package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"unicode/utf8"
)

const (
	MaxJSONBytes       = 4 << 20
	MaxStreams         = 64
	MaxChapters        = 256
	MaxJSONDepth       = 16
	MaxJSONFields      = 1024
	MaxJSONValues      = 32768
	MaxJSONStringBytes = 64 << 10
	MaxSideData        = 32
)

var (
	ErrMetadataInvalid = errors.New("probe_metadata_invalid")
	ErrMetadataLimit   = errors.New("probe_metadata_limit")
)

type object map[string]any
type boundedJSON struct {
	decoder *json.Decoder
	values  int
}

func readJSON(data []byte) (object, error) {
	if len(data) > MaxJSONBytes {
		return nil, ErrMetadataLimit
	}
	if len(data) == 0 || !utf8.Valid(data) {
		return nil, ErrMetadataInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	reader := boundedJSON{decoder: decoder}
	value, err := reader.value(0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrMetadataInvalid
	}
	root, ok := value.(object)
	if !ok {
		return nil, ErrMetadataInvalid
	}
	return root, nil
}

func (r *boundedJSON) value(depth int) (any, error) {
	if depth > MaxJSONDepth {
		return nil, ErrMetadataLimit
	}
	r.values++
	if r.values > MaxJSONValues {
		return nil, ErrMetadataLimit
	}
	token, err := r.decoder.Token()
	if err != nil {
		return nil, ErrMetadataInvalid
	}
	switch token := token.(type) {
	case json.Delim:
		switch token {
		case '{':
			result := make(object)
			for r.decoder.More() {
				if len(result) >= MaxJSONFields {
					return nil, ErrMetadataLimit
				}
				keyToken, err := r.decoder.Token()
				if err != nil {
					return nil, ErrMetadataInvalid
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, ErrMetadataInvalid
				}
				if len(key) > MaxJSONStringBytes {
					return nil, ErrMetadataLimit
				}
				if _, exists := result[key]; exists {
					return nil, ErrMetadataInvalid
				}
				value, err := r.value(depth + 1)
				if err != nil {
					return nil, err
				}
				result[key] = value
			}
			if end, err := r.decoder.Token(); err != nil || end != json.Delim('}') {
				return nil, ErrMetadataInvalid
			}
			return result, nil
		case '[':
			result := make([]any, 0)
			for r.decoder.More() {
				if len(result) >= MaxJSONFields {
					return nil, ErrMetadataLimit
				}
				value, err := r.value(depth + 1)
				if err != nil {
					return nil, err
				}
				result = append(result, value)
			}
			if end, err := r.decoder.Token(); err != nil || end != json.Delim(']') {
				return nil, ErrMetadataInvalid
			}
			return result, nil
		default:
			return nil, ErrMetadataInvalid
		}
	case string:
		if len(token) > MaxJSONStringBytes {
			return nil, ErrMetadataLimit
		}
		return token, nil
	case json.Number:
		value, err := strconv.ParseFloat(string(token), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, ErrMetadataInvalid
		}
		return token, nil
	case bool, nil:
		return token, nil
	default:
		return nil, ErrMetadataInvalid
	}
}
