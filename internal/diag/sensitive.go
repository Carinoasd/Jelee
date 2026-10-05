package diag

import (
	"bytes"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// Scanner rejects output that contains a known secret or path of this
// deployment, or anything shaped like a credential or absolute path. It is
// the final gate for doctor output and diagnostic bundles.
type Scanner struct {
	needles [][]byte
}

// minNeedle avoids rejecting output for short common substrings. Shorter
// secrets still cannot appear because values are never written in the first
// place; the scanner is defence in depth.
const minNeedle = 4

var sensitivePatterns = []*regexp.Regexp{
	// Connection strings and URL credentials.
	regexp.MustCompile(`(?i)postgres(?:ql)?://`),
	regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s/@"]*:[^\s/@"]*@`),
	regexp.MustCompile(`(?i)\b(?:password|passwd|pwd)\s*=\s*[^\s"'&,;}]{2,}`),
	// Authorization material.
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|token|secret)\s*[=:]\s*"?[a-z0-9._~+/-]{8,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{4,}`),
	// Absolute Unix paths with at least two components.
	regexp.MustCompile(`(?:^|[\s"'=(\[,:])/[^\s"'/\\]+/`),
	// Windows drive and UNC paths, raw or JSON-escaped.
	regexp.MustCompile(`(?:^|[^A-Za-z0-9])[A-Za-z]:(?:\\\\|\\|/)[^\s"]`),
	regexp.MustCompile(`(?:^|[\s"'=(])\\\\\\?\\?[A-Za-z0-9._-]+\\`),
}

// NewScanner collects needles from the configuration and environment.
func NewScanner(cfg config.Config, lookup func(string) (string, bool)) *Scanner {
	s := &Scanner{}
	s.AddDSN(cfg.DatabaseURL)
	s.Add(cfg.TMDBAPIKey)
	s.AddPath(cfg.Images.TempRoot, cfg.Images.StoreRoot, cfg.Logging.File.Path)
	s.AddPath(cfg.Logging.PathRoots...)
	if lookup != nil {
		for _, name := range []string{"JELEE_DATABASE_URL", "TMDB_API_KEY"} {
			if value, ok := lookup(name); ok {
				s.AddDSN(value)
				s.Add(value)
			}
		}
		for _, name := range append([]string{"JELEE_IMAGE_TEMP_ROOT", "JELEE_IMAGE_STORE_ROOT", "JELEE_LOG_FILE", "HOME", "USERPROFILE"}, secretFileVariables...) {
			if value, ok := lookup(name); ok {
				s.AddPath(value)
			}
		}
	}
	if wd, err := os.Getwd(); err == nil {
		s.AddPath(wd)
	}
	if home, err := os.UserHomeDir(); err == nil {
		s.AddPath(home)
	}
	return s
}

// Add registers literal secret values.
func (s *Scanner) Add(values ...string) {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if len(value) >= minNeedle {
			s.needles = append(s.needles, []byte(value))
		}
	}
}

// AddPath registers absolute paths in native, slash and JSON-escaped forms.
func (s *Scanner) AddPath(paths ...string) {
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		clean := filepath.Clean(path)
		if len(clean) < minNeedle {
			continue
		}
		s.Add(clean, filepath.ToSlash(clean), strings.ReplaceAll(clean, `\`, `\\`))
	}
}

// AddDSN registers a connection string and its password and non-loopback
// host. Both URL and key=value forms are recognised.
func (s *Scanner) AddDSN(dsn string) {
	if dsn == "" {
		return
	}
	s.Add(dsn)
	if u, err := url.Parse(dsn); err == nil && u.Host != "" {
		if password, ok := u.User.Password(); ok {
			s.Add(password, url.QueryEscape(password))
		}
		if host := u.Hostname(); !loopbackHost(host) {
			s.Add(host)
		}
		for _, key := range []string{"password", "host"} {
			if value := u.Query().Get(key); value != "" && !loopbackHost(value) {
				s.Add(value)
			}
		}
		return
	}
	for _, field := range strings.Fields(dsn) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `'"`)
		switch strings.ToLower(key) {
		case "password":
			s.Add(value)
		case "host":
			if !loopbackHost(value) {
				s.Add(value)
			}
		}
	}
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasPrefix(host, "/") {
		// Unix socket directories are absolute paths and already matched by
		// the path pattern.
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// Clean reports whether data contains no needle and no sensitive pattern.
func (s *Scanner) Clean(data []byte) bool {
	for _, needle := range s.needles {
		if bytes.Contains(data, needle) {
			return false
		}
	}
	for _, pattern := range sensitivePatterns {
		if pattern.Match(data) {
			return false
		}
	}
	return true
}
