package diag

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// Bundle limits. The log window and byte cap are caller-selectable within
// these bounds; the whole archive is capped independently.
const (
	DefaultSince       = 24 * time.Hour
	MaxSince           = 30 * 24 * time.Hour
	DefaultMaxLogBytes = 1 << 20
	MaxLogBytesLimit   = 16 << 20
	maxBundleBytes     = 32 << 20
	maxLogLineBytes    = 64 << 10
	maxTables          = 256
	maxJobGroups       = 256
	// The tail scan reads at most this multiple of the byte cap.
	logScanFactor = 8
)

// ExportOptions configures one bundle.
type ExportOptions struct {
	Out         string
	Since       time.Duration
	MaxLogBytes int64
}

// ExportError carries a fixed code for the CLI.
type ExportError struct{ Code string }

func (e *ExportError) Error() string { return e.Code }

func exportErr(code string) error { return &ExportError{Code: code} }

// BundleFormat versions the archive layout.
const BundleFormat = 1

type manifest struct {
	Format      int               `json:"format"`
	GeneratedAt time.Time         `json:"generatedAt"`
	Since       time.Time         `json:"since"`
	WindowHours float64           `json:"windowHours"`
	MaxLogBytes int64             `json:"maxLogBytes"`
	Platform    string            `json:"platform"`
	GoVersion   string            `json:"goVersion"`
	Files       []string          `json:"files"`
	Sections    map[string]string `json:"sections"`
}

// ConfigSummary is a whitelist of non-secret settings. Secrets and paths
// are reduced to whether they are configured.
type ConfigSummary struct {
	Valid                 bool            `json:"valid"`
	ListenScope           string          `json:"listenScope"`
	ListenPort            string          `json:"listenPort,omitempty"`
	AllowedHosts          int             `json:"allowedHosts"`
	TrustedProxies        int             `json:"trustedProxies"`
	DatabaseConfigured    bool            `json:"databaseConfigured"`
	TMDBConfigured        bool            `json:"tmdbConfigured"`
	MaxConnections        int32           `json:"maxConnections"`
	MaxStreams            int             `json:"maxStreams"`
	RequestTimeoutSeconds int             `json:"requestTimeoutSeconds"`
	Features              map[string]bool `json:"features"`
	Logging               map[string]any  `json:"logging"`
	Images                map[string]any  `json:"images"`
	Jobs                  map[string]any  `json:"jobs"`
	Resources             map[string]any  `json:"resources"`
}

func summarizeConfig(cfg config.Config, cfgErr error) ConfigSummary {
	s := ConfigSummary{
		Valid: cfgErr == nil, ListenScope: "invalid", AllowedHosts: len(cfg.AllowedHosts), TrustedProxies: len(cfg.TrustedProxies),
		DatabaseConfigured: cfg.DatabaseURL != "", TMDBConfigured: cfg.TMDBAPIKey != "",
		MaxConnections: cfg.MaxConnections, MaxStreams: cfg.MaxStreams, RequestTimeoutSeconds: cfg.RequestTimeoutSeconds,
		Features: map[string]bool{
			"catalog": cfg.EnableCatalog, "direct": cfg.EnableDirect, "accounts": cfg.EnableAccounts, "metrics": cfg.EnableMetrics,
			"images": cfg.EnableImages, "jobs": cfg.EnableJobs, "probe": cfg.EnableProbe, "familyIgnore": cfg.EnableFamilyIgnore, "nfoWrite": cfg.EnableNFOWrite,
		},
	}
	if host, port, err := net.SplitHostPort(cfg.Listen); err == nil {
		if addr, err := netip.ParseAddr(host); err == nil {
			s.ListenPort = port
			switch {
			case addr.IsLoopback():
				s.ListenScope = "loopback"
			case addr.IsUnspecified():
				s.ListenScope = "all"
			case addr.IsPrivate() || addr.IsLinkLocalUnicast():
				s.ListenScope = "private"
			default:
				s.ListenScope = "public"
			}
		}
	}
	l := cfg.Logging
	s.Logging = map[string]any{
		"level": enumOr(l.Level, "info", "debug", "warn", "error"), "format": enumOr(l.Format, "json", "console"), "output": enumOr(l.Output, "stdout", "file", "both"),
		"ipMode": enumOr(l.IPMode, "redact", "mask"), "pathMode": enumOr(l.PathMode, "redact", "relative"), "pathRoots": len(l.PathRoots),
		"components": len(l.Components), "fileConfigured": l.File.Path != "", "maxSizeMB": l.File.MaxSizeMB, "rotateHours": l.File.RotateHours,
		"maxBackups": l.File.MaxBackups, "compress": l.File.Compress, "bufferEntries": l.BufferEntries,
	}
	i := cfg.Images
	s.Images = map[string]any{
		"tempRootConfigured": i.TempRoot != "", "storeRootConfigured": i.StoreRoot != "", "maxConcurrent": i.MaxConcurrent,
		"cacheBytes": i.CacheBytes, "cacheEntries": i.CacheEntries, "storeOriginalBytes": i.StoreOriginalBytes, "storeVariantBytes": i.StoreVariantBytes,
	}
	j := cfg.Jobs
	s.Jobs = map[string]any{"workers": j.Workers, "queueLimit": j.QueueLimit, "historyLimit": j.HistoryLimit, "maxAttempts": j.MaxAttempts, "maxRuntimeSeconds": j.MaxRuntimeSeconds, "windowConfigured": j.WindowStart != ""}
	r := cfg.Resources
	s.Resources = map[string]any{"cpuFactor": r.CPUFactor, "io": r.IO, "total": r.Total, "queue": r.Queue}
	return s
}

// enumOr returns value when it is one of the allowed words, the first
// allowed word when empty, and "other" otherwise.
func enumOr(value string, allowed ...string) string {
	if value == "" {
		return allowed[0]
	}
	for _, a := range allowed {
		if strings.EqualFold(value, a) {
			return a
		}
	}
	return "other"
}

// Export writes the redacted bundle, rescans it and deletes it on any hit.
func (s *Session) Export(ctx context.Context, opts ExportOptions) (err error) {
	if opts.Since <= 0 {
		opts.Since = DefaultSince
	}
	if opts.Since > MaxSince {
		opts.Since = MaxSince
	}
	if opts.MaxLogBytes <= 0 {
		opts.MaxLogBytes = DefaultMaxLogBytes
	}
	if opts.MaxLogBytes > MaxLogBytesLimit {
		opts.MaxLogBytes = MaxLogBytesLimit
	}
	if opts.Out == "" || !strings.EqualFold(filepath.Ext(opts.Out), ".zip") {
		return exportErr(CodeExportPath)
	}
	out, err := filepath.Abs(opts.Out)
	if err != nil {
		return exportErr(CodeExportPath)
	}
	now := s.env.Now().UTC()
	since := now.Add(-opts.Since)
	scanner := NewScanner(s.env.Config, s.env.Lookup)
	scanner.AddPath(out, filepath.Dir(out), s.env.TempDir, s.env.Project)

	entries, man := s.collect(ctx, scanner, now, since, opts)
	if collectHook != nil {
		entries = collectHook(entries)
	}
	file, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the operator names the export file; O_EXCL refuses existing paths
	if errors.Is(err, fs.ErrExist) {
		return exportErr(CodeExportExists)
	}
	if err != nil {
		return exportErr(CodeExportFailed)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(out)
		}
	}()
	// The umask can only narrow the mode, but an explicit chmod also covers
	// filesystems that ignore the create mode.
	if runtime.GOOS != "windows" {
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return exportErr(CodeExportFailed)
		}
	}
	if err := writeBundle(file, entries, man); err != nil {
		file.Close()
		return exportErr(CodeExportFailed)
	}
	if err := file.Close(); err != nil {
		return exportErr(CodeExportFailed)
	}
	if err := VerifyBundle(out, scanner); err != nil {
		return err
	}
	keep = true
	return nil
}

// collectHook lets tests append content to prove the final scan deletes a
// leaking bundle. It is nil in production.
var collectHook func([]bundleEntry) []bundleEntry

type bundleEntry struct {
	name string
	data []byte
}

func (s *Session) collect(ctx context.Context, scanner *Scanner, now, since time.Time, opts ExportOptions) ([]bundleEntry, manifest) {
	man := manifest{
		Format: BundleFormat, GeneratedAt: now, Since: since, WindowHours: opts.Since.Hours(), MaxLogBytes: opts.MaxLogBytes,
		Platform: runtime.GOOS + "-" + runtime.GOARCH, GoVersion: runtime.Version(),
		Sections: map[string]string{"pprof": "not_collected_requires_developer_mode"},
	}
	var entries []bundleEntry
	add := func(name string, value any) {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			man.Sections[name] = "encode_failed"
			return
		}
		entries = append(entries, bundleEntry{name, append(data, '\n')})
	}
	add("config.json", summarizeConfig(s.env.Config, s.env.ConfigErr))
	add("doctor.json", s.Doctor(ctx))

	if db, err := s.database(ctx); err != nil {
		man.Sections["db-stats.json"] = "database_unavailable"
		man.Sections["jobs.json"] = "database_unavailable"
	} else {
		// Root paths are needles even though they are never written.
		if roots, err := db.LibraryRoots(ctx, MaxRootsLimit); err == nil {
			for _, root := range roots {
				scanner.AddPath(root.Path)
			}
		}
		stats := struct {
			Tables []TableStat `json:"tables"`
		}{}
		if tables, err := db.TableStats(ctx, maxTables); err == nil {
			stats.Tables = tables
			add("db-stats.json", stats)
		} else {
			man.Sections["db-stats.json"] = "query_failed"
		}
		if groups, err := db.JobSummary(ctx, since, maxJobGroups); err == nil {
			add("jobs.json", struct {
				Since  time.Time  `json:"since"`
				Groups []JobGroup `json:"groups"`
			}{since, groups})
		} else {
			man.Sections["jobs.json"] = "query_failed"
		}
	}

	l := s.env.Config.Logging
	switch {
	case !logFileEnabled(l.Output) || l.File.Path == "":
		man.Sections["logs.jsonl"] = "no_log_file_configured"
	default:
		red := logging.NewRedactor(logging.IPMode(l.IPMode), logging.PathMode(l.PathMode), l.PathRoots)
		data, status := readLogTail(l.File.Path, since, opts.MaxLogBytes, red)
		man.Sections["logs.jsonl"] = status
		if data != nil {
			entries = append(entries, bundleEntry{"logs.jsonl", data})
		}
	}
	for _, e := range entries {
		man.Files = append(man.Files, e.name)
	}
	return entries, man
}

func writeBundle(w io.Writer, entries []bundleEntry, man manifest) error {
	limited := &capWriter{w: w, left: maxBundleBytes}
	zw := zip.NewWriter(limited)
	data, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	all := append([]bundleEntry{{"manifest.json", append(data, '\n')}}, entries...)
	for _, e := range all {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: man.GeneratedAt})
		if err != nil {
			return err
		}
		if _, err := fw.Write(e.data); err != nil {
			return err
		}
	}
	return zw.Close()
}

type capWriter struct {
	w    io.Writer
	left int64
}

var errBundleTooLarge = errors.New("bundle too large")

func (c *capWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, errBundleTooLarge
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

// VerifyBundle reopens a written bundle and scans every entry name and
// decompressed body. Any hit, or an unreadable archive, deletes the file.
func VerifyBundle(path string, scanner *Scanner) (err error) {
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	info, err := os.Stat(path)
	if err != nil {
		return exportErr(CodeExportFailed)
	}
	if info.Size() > maxBundleBytes {
		return exportErr(CodeExportTooLarge)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return exportErr(CodeExportFailed)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if !scanner.Clean([]byte(f.Name)) {
			return exportErr(CodeExportFound)
		}
		rc, err := f.Open()
		if err != nil {
			return exportErr(CodeExportFailed)
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxBundleBytes+1))
		rc.Close()
		if err != nil || len(data) > maxBundleBytes {
			return exportErr(CodeExportTooLarge)
		}
		if !scanner.Clean(data) {
			return exportErr(CodeExportFound)
		}
	}
	return nil
}

// readLogTail re-redacts the most recent records of the active log file
// inside the window, keeping at most maxBytes of output. Rotated backups are
// not read. The returned status is a fixed word for the manifest.
func readLogTail(path string, since time.Time, maxBytes int64, red *logging.Redactor) ([]byte, string) {
	file, err := os.Open(path) //nolint:gosec // G304: path comes from the fixed diagnostic file list
	if err != nil {
		return nil, "log_file_unavailable"
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, "log_file_unavailable"
	}
	window := maxBytes * logScanFactor
	offset := info.Size() - window
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, "log_file_unavailable"
	}
	reader := bufio.NewReaderSize(io.LimitReader(file, window), 64<<10)
	if offset > 0 {
		// Skip the partial first line.
		if _, err := reader.ReadSlice('\n'); err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return nil, "log_file_empty"
		}
	}
	var kept [][]byte
	var keptBytes int64
	for {
		line, err := readBoundedLine(reader)
		if len(line) > 0 {
			if out, ok := red.Line(line); ok && recordAfter(out, since) {
				kept = append(kept, out)
				keptBytes += int64(len(out)) + 1
				for keptBytes > maxBytes && len(kept) > 0 {
					keptBytes -= int64(len(kept[0])) + 1
					kept = kept[1:]
				}
			}
		}
		if err != nil {
			break
		}
	}
	if len(kept) == 0 {
		return nil, "no_records_in_window"
	}
	var buf bytes.Buffer
	for _, line := range kept {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), "included"
}

// readBoundedLine returns one line without its newline; overlong lines are
// discarded and returned empty.
func readBoundedLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > maxLogLineBytes {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if tooLong {
			return nil, err
		}
		return bytes.TrimRight(line, "\r\n"), err
	}
}

func recordAfter(line []byte, since time.Time) bool {
	var record struct {
		Time time.Time `json:"time"`
	}
	if json.Unmarshal(line, &record) != nil || record.Time.IsZero() {
		return false
	}
	return !record.Time.Before(since)
}
