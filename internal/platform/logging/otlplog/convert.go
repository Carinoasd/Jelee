package otlplog

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logpb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// Keys of the router's JSON records that become fields of the log record
// instead of attributes.
const (
	keyTime  = "time"
	keyLevel = "level"
	keyMsg   = "msg"
	keyTrace = "traceId"
	keySpan  = "spanId"
)

// maxDepth bounds nested groups; the router's records are flat or a few
// groups deep.
const maxDepth = 16

var errDepth = errors.New("record nests too deep")

func unixNano() int64 { return time.Now().UnixNano() }

func stringValue(s string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s}}
}

// convert maps one redacted JSON record: msg is the body, level the severity
// number and text, time the timestamp, traceId and spanId the trace context;
// every other field, in record order, is an attribute with the same key and
// a typed value (groups become key-value lists). The record passed the log
// whitelist before it was written, so nothing here adds information the log
// file does not hold. A record that is not a JSON object (the router never
// writes one) is sent whole as the body.
func convert(raw []byte, observed int64) *logpb.LogRecord {
	rec := &logpb.LogRecord{ObservedTimeUnixNano: uint64(observed)} //nolint:gosec // G115: wall clock nanoseconds are positive
	if err := decodeRecord(raw, rec); err != nil {
		return &logpb.LogRecord{ObservedTimeUnixNano: uint64(observed), Body: stringValue(string(raw))} //nolint:gosec // G115: as above
	}
	return rec
}

func decodeRecord(raw []byte, rec *logpb.LogRecord) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("record is not an object")
	}
	rec.Attributes = make([]*commonpb.KeyValue, 0, 16)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := keyTok.(string)
		valTok, err := dec.Token()
		if err != nil {
			return err
		}
		if s, ok := valTok.(string); ok && field(key, s, rec) {
			continue
		}
		v, err := decodeValue(dec, valTok, 1)
		if err != nil {
			return err
		}
		rec.Attributes = append(rec.Attributes, &commonpb.KeyValue{Key: key, Value: v})
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	// A span belongs to a trace; without one the span ID stays a field.
	if len(rec.TraceId) == 0 && len(rec.SpanId) > 0 {
		rec.Attributes = append(rec.Attributes, &commonpb.KeyValue{Key: keySpan, Value: stringValue(hex.EncodeToString(rec.SpanId))})
		rec.SpanId = nil
	}
	return nil
}

// field applies a string field that maps to the log record itself and
// reports whether it did. A time, traceId or spanId that does not parse
// stays an attribute.
func field(key, value string, rec *logpb.LogRecord) bool {
	switch key {
	case keyMsg:
		rec.Body = stringValue(value)
	case keyLevel:
		rec.SeverityNumber, rec.SeverityText = severity(value)
	case keyTime:
		at, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || at.UnixNano() <= 0 {
			return false
		}
		rec.TimeUnixNano = uint64(at.UnixNano()) //nolint:gosec // G115: checked positive
	case keyTrace:
		id, ok := hexID(value, 16)
		if !ok {
			return false
		}
		rec.TraceId = id
	case keySpan:
		id, ok := hexID(value, 8)
		if !ok {
			return false
		}
		rec.SpanId = id
	default:
		return false
	}
	return true
}

func decodeValue(dec *json.Decoder, tok json.Token, depth int) (*commonpb.AnyValue, error) {
	switch v := tok.(type) {
	case string:
		return stringValue(v), nil
	case bool:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v}}, nil
	case json.Number:
		if n, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: n}}, nil
		}
		if f, err := strconv.ParseFloat(string(v), 64); err == nil {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: f}}, nil
		}
		return stringValue(string(v)), nil
	case nil:
		// An empty AnyValue is OTLP's null.
		return &commonpb.AnyValue{}, nil
	case json.Delim:
		if depth >= maxDepth {
			return nil, errDepth
		}
		if v == '[' {
			list := &commonpb.ArrayValue{}
			for dec.More() {
				t, err := dec.Token()
				if err != nil {
					return nil, err
				}
				item, err := decodeValue(dec, t, depth+1)
				if err != nil {
					return nil, err
				}
				list.Values = append(list.Values, item)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: list}}, nil
		}
		group := &commonpb.KeyValueList{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := k.(string)
			t, err := dec.Token()
			if err != nil {
				return nil, err
			}
			member, err := decodeValue(dec, t, depth+1)
			if err != nil {
				return nil, err
			}
			group.Values = append(group.Values, &commonpb.KeyValue{Key: key, Value: member})
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_KvlistValue{KvlistValue: group}}, nil
	}
	return nil, errors.New("unexpected JSON token")
}

// severity maps a slog level name ("INFO", "WARN", "DEBUG-2", "ERROR+4") to
// the OpenTelemetry severity number, which the data model defines as the
// slog level plus 9 (DEBUG 5, INFO 9, WARN 13, ERROR 17, clamped to 1-24),
// and keeps the name as the severity text. Anything else is INFO.
func severity(level string) (logpb.SeverityNumber, string) {
	base, offset := level, ""
	if i := strings.IndexAny(level, "+-"); i > 0 {
		base, offset = level[:i], level[i:]
	}
	var n int
	switch base {
	case "DEBUG":
		n = -4
	case "INFO":
		n = 0
	case "WARN":
		n = 4
	case "ERROR":
		n = 8
	default:
		return logpb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"
	}
	if offset != "" {
		d, err := strconv.Atoi(offset)
		if err != nil || d < -32 || d > 32 {
			return logpb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"
		}
		n += d
	}
	n = min(max(n+9, int(logpb.SeverityNumber_SEVERITY_NUMBER_TRACE)), int(logpb.SeverityNumber_SEVERITY_NUMBER_FATAL4))
	return logpb.SeverityNumber(n), level //nolint:gosec // G115: clamped to 1-24
}

// hexID decodes exactly 2*n lowercase hexadecimal digits that are not all
// zero (the invalid trace and span ID).
func hexID(s string, n int) ([]byte, bool) {
	if len(s) != 2*n {
		return nil, false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return nil, false
		}
	}
	id, err := hex.DecodeString(s)
	if err != nil || bytes.Count(id, []byte{0}) == n {
		return nil, false
	}
	return id, true
}
