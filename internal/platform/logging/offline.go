package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sort"
	"time"
)

// Redactor exposes the logging whitelist to offline artefacts such as
// diagnostic bundles, so they apply exactly the rules live sinks apply.
type Redactor struct{ red *redactor }

// NewRedactor builds a whitelist redactor. Unknown modes fall back to full
// redaction; raw addresses and absolute paths are never an option.
func NewRedactor(ip IPMode, path PathMode, roots []string) *Redactor {
	if ip != IPMask {
		ip = IPRedact
	}
	if path != PathRelative {
		path = PathRedact
	}
	return &Redactor{red: newRedactor(ip, path, roots)}
}

// Redacted is the placeholder written in place of a refused value.
const Redacted = redacted

// Attr applies the whitelist to one attribute.
func (r *Redactor) Attr(a slog.Attr) slog.Attr { return r.red.replace(nil, a) }

// Path returns a root-relative path when the path mode allows it and the
// value lies below a configured root; otherwise it returns Redacted.
func (r *Redactor) Path(value string) string {
	if r.red.pathMode == PathRelative {
		if rel, ok := r.red.relative(value); ok {
			return rel
		}
	}
	return redacted
}

// maxLineDepth bounds group nesting accepted from a log line.
const maxLineDepth = 8

// Line re-applies the whitelist to one JSON log record previously written by
// a sink. Records written by older or foreign writers are treated as
// untrusted input: every leaf passes the same rules as a live attribute.
// It reports false for lines that are not a single JSON object.
func (r *Redactor) Line(line []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var record map[string]any
	if err := dec.Decode(&record); err != nil || record == nil {
		return nil, false
	}
	if dec.More() {
		return nil, false
	}
	out, err := json.Marshal(r.object(record, 0))
	if err != nil {
		return nil, false
	}
	return out, true
}

func (r *Redactor) object(in map[string]any, depth int) map[string]any {
	out := make(map[string]any, len(in))
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := in[key]
		// Keys come from code, so anything else is foreign input that could
		// itself carry a value; it is renamed exactly as live sinks do.
		if key == "" {
			continue
		}
		key = safeKey(key)
		if group, ok := value.(map[string]any); ok && depth < maxLineDepth {
			out[key] = r.object(group, depth+1)
			continue
		}
		a := r.Attr(lineAttr(key, value, depth == 0))
		out[a.Key] = a.Value.Any()
		if level, ok := a.Value.Any().(slog.Level); ok {
			out[a.Key] = level.String()
		}
		if t, ok := a.Value.Any().(time.Time); ok {
			out[a.Key] = t.Format(time.RFC3339Nano)
		}
	}
	return out
}

// lineAttr restores the slog kinds the whitelist expects. Only top-level
// time and level keys are reconstructed; nested ones stay strings and are
// therefore redacted.
func lineAttr(key string, value any, top bool) slog.Attr {
	switch v := value.(type) {
	case string:
		if top && key == slog.TimeKey {
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				return slog.Time(key, t)
			}
		}
		if top && key == slog.LevelKey {
			var level slog.Level
			if level.UnmarshalText([]byte(v)) == nil {
				return slog.Any(key, level)
			}
		}
		return slog.String(key, v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return slog.Int64(key, n)
		}
		if f, err := v.Float64(); err == nil {
			return slog.Float64(key, f)
		}
	case bool:
		return slog.Bool(key, v)
	}
	// Arrays, null and over-deep groups are never whitelisted.
	return slog.String(key, redacted)
}

// RenderPath applies Path to a file below a library root, given as the root
// and a slash-separated root-relative path. The result is the path relative
// to a configured logging root in the relative mode, Redacted otherwise; it
// is never absolute.
func (r *Redactor) RenderPath(root, relative string) string {
	if root == "" || relative == "" || !filepath.IsAbs(root) {
		return redacted
	}
	return r.Path(filepath.Join(root, filepath.FromSlash(relative)))
}
