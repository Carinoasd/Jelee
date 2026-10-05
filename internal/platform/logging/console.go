package logging

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const consoleTime = "2006-01-02T15:04:05.000Z07:00"

// consoleHandler renders one human-readable line per record for development
// terminals: "time LEVEL message key=value group.key=value". Every leaf value
// passes through the same redactor as the JSON handler.
type consoleHandler struct {
	w      io.Writer
	mu     *sync.Mutex
	red    *redactor
	groups []string
	pre    []byte
}

func newConsoleHandler(w io.Writer, red *redactor) *consoleHandler {
	return &consoleHandler{w: w, mu: &sync.Mutex{}, red: red}
}

func (h *consoleHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	buf := make([]byte, 0, 256)
	if !r.Time.IsZero() {
		buf = r.Time.AppendFormat(buf, consoleTime)
		buf = append(buf, ' ')
	}
	level := r.Level.String()
	buf = append(buf, level...)
	for i := len(level); i < 5; i++ {
		buf = append(buf, ' ')
	}
	buf = append(buf, ' ')
	msg := h.red.replace(nil, slog.String(slog.MessageKey, r.Message))
	buf = append(buf, msg.Value.String()...)
	buf = append(buf, h.pre...)
	r.Attrs(func(a slog.Attr) bool {
		buf = h.appendAttr(buf, h.groups, a)
		return true
	})
	buf = append(buf, '\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf)
	return err
}

func (h *consoleHandler) appendAttr(buf []byte, groups []string, a slog.Attr) []byte {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return buf
	}
	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		if len(members) == 0 {
			return buf
		}
		if a.Key != "" {
			groups = append(groups[:len(groups):len(groups)], a.Key)
		}
		for _, member := range members {
			buf = h.appendAttr(buf, groups, member)
		}
		return buf
	}
	a = h.red.replace(groups, a)
	buf = append(buf, ' ')
	for _, g := range groups {
		buf = append(buf, g...)
		buf = append(buf, '.')
	}
	buf = append(buf, a.Key...)
	buf = append(buf, '=')
	return appendConsoleValue(buf, a.Value)
}

func appendConsoleValue(buf []byte, v slog.Value) []byte {
	switch v.Kind() {
	case slog.KindString:
		s := v.String()
		if s == "" || strings.IndexFunc(s, func(r rune) bool { return r == '"' || r == '=' || unicode.IsSpace(r) || !unicode.IsPrint(r) }) >= 0 {
			return strconv.AppendQuote(buf, s)
		}
		return append(buf, s...)
	case slog.KindTime:
		return v.Time().AppendFormat(buf, time.RFC3339Nano)
	case slog.KindDuration:
		return append(buf, v.Duration().String()...)
	default:
		return append(buf, v.String()...)
	}
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	pre := append([]byte(nil), h.pre...)
	for _, a := range attrs {
		pre = h.appendAttr(pre, h.groups, a)
	}
	return &consoleHandler{w: h.w, mu: h.mu, red: h.red, groups: h.groups, pre: pre}
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	groups := append(h.groups[:len(h.groups):len(h.groups)], name)
	return &consoleHandler{w: h.w, mu: h.mu, red: h.red, groups: groups, pre: h.pre}
}
