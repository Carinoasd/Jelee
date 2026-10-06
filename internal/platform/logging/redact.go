package logging

import (
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const redacted = "[redacted]"

// IPMode selects how client addresses are written. Raw addresses are never
// an option: the requirement only allows removal or masking.
type IPMode string

const (
	IPRedact IPMode = "redact"
	IPMask   IPMode = "mask"
)

// PathMode selects how local filesystem paths are written. Absolute paths are
// never written; "relative" keeps only the part below a configured root.
type PathMode string

const (
	PathRedact   PathMode = "redact"
	PathRelative PathMode = "relative"
)

// ipKeys and pathKeys are the only attribute names that may carry an address
// or a filesystem path. Every other unknown key is still redacted outright.
var (
	ipKeys   = map[string]bool{"clientIp": true}
	pathKeys = map[string]bool{"path": true}
)

// redactor is the whitelist shared by every handler. It is safe for
// concurrent use; path roots can be replaced at runtime.
type redactor struct {
	ipMode   IPMode
	pathMode PathMode
	roots    atomic.Pointer[[]string]
	// dev holds the developer mode switches; nil refuses every developer
	// mode field (see devlog.go).
	dev atomic.Pointer[devSwitches]
}

func newRedactor(ip IPMode, path PathMode, roots []string) *redactor {
	r := &redactor{ipMode: ip, pathMode: path}
	r.setRoots(roots)
	return r
}

func (r *redactor) setRoots(roots []string) {
	clean := make([]string, 0, len(roots))
	for _, root := range roots {
		if root != "" && filepath.IsAbs(root) {
			clean = append(clean, filepath.Clean(root))
		}
	}
	r.roots.Store(&clean)
}

// replace is a slog ReplaceAttr function. It deliberately permits only
// bounded operational fields. Request text, credentials, paths and arbitrary
// error strings are never safe log attributes.
func (r *redactor) replace(groups []string, a slog.Attr) slog.Attr {
	if out, ok := r.developer(a); ok {
		return out
	}
	switch a.Key {
	case slog.LevelKey:
		if _, ok := a.Value.Any().(slog.Level); ok {
			return a
		}
	case slog.TimeKey, "status", "durationMs", "count":
		switch a.Value.Kind() {
		case slog.KindTime, slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool, slog.KindDuration:
			return a
		}
	case slog.MessageKey:
		if a.Value.Kind() == slog.KindString && safeMessage(a.Value.String()) {
			return a
		}
	case "component":
		if a.Value.Kind() == slog.KindString && knownComponent(a.Value.String()) {
			return a
		}
	case traceKey, "linkTraceId":
		if a.Value.Kind() == slog.KindString && hexID(a.Value.String(), 32) {
			return a
		}
	case spanKey, "parentSpanId":
		if a.Value.Kind() == slog.KindString && hexID(a.Value.String(), 16) {
			return a
		}
	case "span":
		if a.Value.Kind() == slog.KindString && safeToken(a.Value.String(), 64, "._") {
			return a
		}
	case "forced", "linked":
		if a.Value.Kind() == slog.KindBool {
			return a
		}
	case "linkKind", "outcome", "source":
		if a.Value.Kind() == slog.KindString && safeLowerIdent(a.Value.String()) {
			return a
		}
	case "eventId":
		if a.Value.Kind() == slog.KindString && safeToken(a.Value.String(), 64, "_-") {
			return a
		}
	case "deliveryId", "webhookId":
		if a.Value.Kind() == slog.KindString && domain.ValidID(a.Value.String()) {
			return a
		}
	case "cpuLimit", "ioLimit", "totalLimit", "percent", "pressure", "auditRows", "securityRows", "batches":
		switch a.Value.Kind() {
		case slog.KindInt64, slog.KindUint64, slog.KindFloat64:
			return a
		}
	case "requestId":
		if a.Value.Kind() == slog.KindString && safeToken(a.Value.String(), 64, "-") {
			return a
		}
	case "event":
		if a.Value.Kind() == slog.KindString && safeLowerIdent(a.Value.String()) {
			return a
		}
	case "method":
		if a.Value.Kind() == slog.KindString && SafeMethod(a.Value.String()) == a.Value.String() {
			return a
		}
	case "taskId":
		if a.Value.Kind() == slog.KindString && domain.ValidID(a.Value.String()) {
			return a
		}
	case "state":
		if a.Value.Kind() == slog.KindString {
			switch a.Value.String() {
			case domain.JobQueued, domain.JobRunning, domain.JobSucceeded, domain.JobFailed, domain.JobCancelled:
				return a
			}
		}
	case "code":
		if a.Value.Kind() == slog.KindString {
			switch a.Value.String() {
			case "", "scan_unavailable", "scan_io", "scan_limit", "scan_failed", "job_timeout", "job_attempts_exhausted", "job_lease_lost":
				return a
			}
			if r.developerCode(a.Value.String()) {
				return a
			}
		}
	default:
		if ipKeys[a.Key] && r.ipMode == IPMask && a.Value.Kind() == slog.KindString {
			if masked, ok := maskIP(a.Value.String()); ok {
				return slog.String(a.Key, masked)
			}
		}
		if pathKeys[a.Key] && r.pathMode == PathRelative && a.Value.Kind() == slog.KindString {
			if rel, ok := r.relative(a.Value.String()); ok {
				return slog.String(a.Key, rel)
			}
		}
	}
	return slog.String(a.Key, redacted)
}

// maskIP keeps the IPv4 /24 or IPv6 /48 network so operators can correlate
// abuse without retaining a host address.
func maskIP(value string) (string, bool) {
	addr, err := netip.ParseAddr(value)
	if err != nil {
		ap, perr := netip.ParseAddrPort(value)
		if perr != nil {
			return "", false
		}
		addr = ap.Addr()
	}
	addr = addr.Unmap().WithZone("")
	bits := 24
	if addr.Is6() {
		bits = 48
	}
	prefix, err := addr.Prefix(bits)
	if err != nil {
		return "", false
	}
	return prefix.String(), true
}

// relative maps an absolute path below a configured root to a slash-separated
// path relative to that root. Anything else (relative input, traversal,
// paths outside every root) is refused.
func (r *redactor) relative(value string) (string, bool) {
	if value == "" || !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") {
		return "", false
	}
	clean := filepath.Clean(value)
	for _, root := range *r.roots.Load() {
		rel, err := filepath.Rel(root, clean)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
			continue
		}
		return filepath.ToSlash(rel), true
	}
	return "", false
}

// safeMessage accepts the constant prose used by log call sites. Messages
// carrying URLs, key=value pairs, paths or long opaque words are replaced, so
// a formatted message cannot smuggle credentials past the attribute whitelist.
func safeMessage(s string) bool {
	if s == "" || len(s) > 200 {
		return s == ""
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune(" ;,.'()_-", c):
		default:
			return false
		}
	}
	for _, word := range strings.Fields(s) {
		if len(word) >= 20 || (len(word) >= 12 && strings.ContainsAny(word, "0123456789")) {
			return false
		}
	}
	return true
}

func safeToken(s string, max int, extra string) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(extra, c)) {
			return false
		}
	}
	return true
}

// hexID accepts exactly n lowercase hexadecimal digits.
func hexID(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func safeLowerIdent(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c == '_') {
			return false
		}
	}
	return true
}

var defaultRedactor = newRedactor(IPRedact, PathRedact, nil)
