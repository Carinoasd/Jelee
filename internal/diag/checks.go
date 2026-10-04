package diag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/proberuntime"
	"github.com/MoYuanCN/Jelee/tools"
)

// ---- configuration ----

// secretFileVariables name files that hold configuration or credentials.
var secretFileVariables = []string{"JELEE_CONFIG", "JELEE_DATABASE_URL_FILE", "TMDB_API_KEY_FILE"}

func (s *Session) checkConfig(context.Context) Result {
	var findings []Finding
	switch {
	case s.env.ConfigErr == nil:
		findings = append(findings, okf("", CodeConfigOK))
	case s.env.Config.DatabaseURL == "" && !s.databaseVariableSet():
		f := failf("", CodeConfigDatabaseMissing)
		f.Detail = safeDetail(s.env.ConfigErr.Error())
		findings = append(findings, f)
	default:
		f := failf("", CodeConfigInvalid)
		f.Detail = safeDetail(s.env.ConfigErr.Error())
		findings = append(findings, f)
	}
	for _, name := range secretFileVariables {
		path, ok := s.env.Lookup(name)
		if !ok || path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && groupOrOtherAccess(info) {
			findings = append(findings, warnf(name, CodeConfigSecretFileMode))
		}
	}
	return newResult("config", findings...)
}

// databaseVariableSet distinguishes a missing setting from a load error that
// stopped before the database variables were read.
func (s *Session) databaseVariableSet() bool {
	for _, name := range []string{"JELEE_DATABASE_URL", "JELEE_DATABASE_URL_FILE"} {
		if value, ok := s.env.Lookup(name); ok && value != "" {
			return true
		}
	}
	return false
}

// safeDetail passes configuration error text only when it is plain prose.
// Config errors never include values, but this keeps the guarantee local.
func safeDetail(text string) string {
	if len(text) > 160 {
		return ""
	}
	for _, c := range text {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(" _.,'-", c)) {
			return ""
		}
	}
	return text
}

// ---- database ----

func (s *Session) dbFinding(err error) Finding {
	switch {
	case errors.Is(err, errDBNotConfigured):
		return failf("", CodeDBNotConfigured)
	case errors.Is(err, ErrDBConfig):
		return failf("", CodeDBConfigInvalid)
	case errors.Is(err, ErrDBAuth):
		return failf("", CodeDBAuthFailed)
	}
	return failf("", CodeDBUnreachable)
}

func (s *Session) checkDatabase(ctx context.Context) Result {
	db, err := s.database(ctx)
	if err != nil {
		return newResult("database", s.dbFinding(err))
	}
	if err := db.Ping(ctx); err != nil {
		return newResult("database", failf("", CodeDBUnreachable))
	}
	return newResult("database", okf("", CodeDBConnected))
}

func (s *Session) checkMigrations(ctx context.Context) Result {
	db, err := s.database(ctx)
	if err != nil {
		return newResult("migrations", warnf("", CodeDBUnchecked))
	}
	version, dirty, present, err := db.Migration(ctx)
	r := Result{Facts: map[string]string{"required": strconv.FormatInt(s.env.SchemaVersion, 10)}}
	switch {
	case err != nil:
		r.Findings = []Finding{failf("", CodeDBQueryFailed)}
	case !present:
		r.Findings = []Finding{failf("", CodeSchemaMissing)}
	default:
		r.Facts["version"] = strconv.FormatInt(version, 10)
		r.Facts["dirty"] = strconv.FormatBool(dirty)
		switch {
		case dirty:
			r.Findings = []Finding{failf("", CodeMigrationDirty)}
		case version < s.env.SchemaVersion:
			r.Findings = []Finding{failf("", CodeMigrationBehind)}
		case version > s.env.SchemaVersion:
			r.Findings = []Finding{failf("", CodeSchemaNewer)}
		default:
			r.Findings = []Finding{okf("", CodeSchemaCurrent)}
		}
	}
	r.Check = "migrations"
	return finish(r)
}

// ---- library roots ----

// rootTimeout bounds the filesystem calls for one root.
const rootTimeout = 3 * time.Second

func (s *Session) checkRoots(ctx context.Context) Result {
	db, err := s.database(ctx)
	if err != nil {
		return newResult("library_roots", warnf("", CodeRootsUnchecked))
	}
	roots, err := db.LibraryRoots(ctx, s.env.MaxRoots+1)
	if err != nil {
		return newResult("library_roots", failf("", CodeDBQueryFailed))
	}
	if len(roots) == 0 {
		return newResult("library_roots", okf("", CodeRootsNone))
	}
	var findings []Finding
	truncated := len(roots) > s.env.MaxRoots
	if truncated {
		roots = roots[:s.env.MaxRoots]
	}
	for _, root := range roots {
		subject := "root:" + root.ID
		if ctx.Err() != nil {
			findings = append(findings, failf(subject, CodeRootTimeout))
			continue
		}
		code := probeRoot(ctx, root.Path)
		findings = append(findings, finding(rootStatus(code), subject, code))
	}
	if truncated {
		findings = append(findings, warnf("", CodeRootsTruncated))
	}
	r := newResult("library_roots", findings...)
	r.Facts = map[string]string{"checked": strconv.Itoa(len(roots)), "limit": strconv.Itoa(s.env.MaxRoots)}
	return r
}

func rootStatus(code string) Status {
	if code == CodeRootOK {
		return StatusOK
	}
	return StatusFail
}

// probeRoot returns a root code. A blocked filesystem call is abandoned after
// rootTimeout; its goroutine only writes to a buffered channel.
func probeRoot(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, rootTimeout)
	defer cancel()
	done := make(chan string, 1)
	go func() { done <- inspectRoot(path) }()
	select {
	case code := <-done:
		return code
	case <-ctx.Done():
		return CodeRootTimeout
	}
}

func inspectRoot(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return CodeRootMissing
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return CodeRootMissing
	case err != nil:
		return CodeRootUnreadable
	case !info.IsDir():
		return CodeRootNotDirectory
	}
	dir, err := os.Open(path) //nolint:gosec // G304: path comes from validated server configuration
	if err != nil {
		return CodeRootUnreadable
	}
	defer dir.Close()
	if _, err := dir.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return CodeRootUnreadable
	}
	return CodeRootOK
}

// ---- external tools ----

// maxToolBytes matches the identity diagnostic's executable bound.
const maxToolBytes int64 = 512 << 20

func (s *Session) toolCandidates(spec tools.FFprobeSpecification) []ToolCandidate {
	if s.env.Tools != nil {
		return s.env.Tools
	}
	candidates := []ToolCandidate{{Label: "runtime", Path: proberuntime.FFprobePath}}
	if s.env.Project != "" && filepath.IsAbs(s.env.Project) {
		candidates = append(candidates, ToolCandidate{Label: "project", Path: filepath.Join(s.env.Project, filepath.FromSlash(spec.ExecutablePath))})
	}
	return candidates
}

func (s *Session) checkTools(ctx context.Context) Result {
	lookup := s.env.ToolSpec
	if lookup == nil {
		lookup = func() (tools.FFprobeSpecification, error) {
			return tools.FFprobeSpec(runtime.GOOS + "-" + runtime.GOARCH)
		}
	}
	spec, err := lookup()
	if err != nil {
		if err.Error() == "tool_platform_unsupported" {
			return newResult("tools", warnf("ffprobe", CodeToolUnsupported))
		}
		return newResult("tools", failf("ffprobe", CodeToolManifest))
	}
	facts := map[string]string{"expectedVersion": spec.VendorVersion, "platform": spec.Platform}
	var findings []Finding
	for _, candidate := range s.toolCandidates(spec) {
		subject := "ffprobe:" + candidate.Label
		code, found := verifyTool(ctx, candidate.Path, spec.SHA256)
		if !found {
			continue
		}
		if code == CodeToolVerified {
			findings = append(findings, okf(subject, code))
			facts[candidate.Label+".sha256"] = spec.SHA256[:16]
		} else {
			findings = append(findings, failf(subject, code))
		}
	}
	if s.env.Config.EnableEmbeddedCovers {
		// The cover pass reads attached pictures through the same sandboxed
		// ffprobe; without a verified one it stays off at runtime.
		code, verified := CodeEmbeddedCoversNoTool, false
		for _, f := range findings {
			verified = verified || f.Code == CodeToolVerified
		}
		if verified {
			code = CodeEmbeddedCoversReady
			findings = append(findings, okf("embedded-covers", code))
		} else {
			findings = append(findings, failf("embedded-covers", code))
		}
	}
	if len(findings) == 0 {
		if s.env.Config.EnableProbe {
			findings = append(findings, failf("ffprobe", CodeToolMissing))
		} else {
			findings = append(findings, warnf("ffprobe", CodeToolMissing))
		}
	}
	r := newResult("tools", findings...)
	r.Facts = facts
	return r
}

// verifyTool hashes a candidate without executing it. found is false when
// nothing exists at the path.
func verifyTool(ctx context.Context, path, want string) (code string, found bool) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxToolBytes {
		return CodeToolUnreadable, true
	}
	file, err := os.Open(path) //nolint:gosec // G304: path comes from validated server configuration
	if err != nil {
		return CodeToolUnreadable, true
	}
	defer file.Close()
	hash := sha256.New()
	reader := io.LimitReader(file, maxToolBytes+1)
	buf := make([]byte, 1<<20)
	var total int64
	for {
		if ctx.Err() != nil {
			return CodeToolUnreadable, true
		}
		n, err := reader.Read(buf)
		hash.Write(buf[:n])
		total += int64(n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return CodeToolUnreadable, true
		}
	}
	if total > maxToolBytes {
		return CodeToolUnreadable, true
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(want) {
		return CodeToolHashMismatch, true
	}
	return CodeToolVerified, true
}

// ---- disk ----

// DiskUsage is a platform-neutral free-space reading.
type DiskUsage struct {
	TotalBytes, FreeBytes   uint64
	TotalInodes, FreeInodes uint64
	// InodesKnown is false where the platform has no inode counts.
	InodesKnown bool
}

// DiskThresholds select warn and fail levels. A byte level applies as the
// smaller of its absolute value and its percentage of the volume, so small
// dedicated volumes (such as a 64 MiB tmpfs) are judged by ratio. Inode
// levels are percentages of the total.
type DiskThresholds struct {
	LowBytes, CriticalBytes           uint64
	LowBytesPercent, CriticalBytesPct float64
	LowInodePercent, CriticalInodePct float64
}

func DefaultDiskThresholds() DiskThresholds {
	return DiskThresholds{LowBytes: 2 << 30, CriticalBytes: 256 << 20, LowBytesPercent: 10, CriticalBytesPct: 2, LowInodePercent: 5, CriticalInodePct: 1}
}

func byteLevel(absolute uint64, percent float64, total uint64) uint64 {
	if relative := uint64(float64(total) * percent / 100); total > 0 && relative < absolute {
		return relative
	}
	return absolute
}

// diskTarget names a directory by its configuration key.
type diskTarget struct{ subject, path string }

func (s *Session) writableTargets() []diskTarget {
	targets := []diskTarget{{"tempdir", s.env.TempDir}}
	if s.env.Config.Images.TempRoot != "" {
		targets = append(targets, diskTarget{"images.tempRoot", s.env.Config.Images.TempRoot})
	}
	if s.env.Config.Images.StoreRoot != "" {
		targets = append(targets, diskTarget{"images.storeRoot", s.env.Config.Images.StoreRoot})
	}
	if logFileEnabled(s.env.Config.Logging.Output) && s.env.Config.Logging.File.Path != "" {
		targets = append(targets, diskTarget{"logging.file", filepath.Dir(s.env.Config.Logging.File.Path)})
	}
	return targets
}

func logFileEnabled(output string) bool { return output == "file" || output == "both" }

func (s *Session) checkDisk(context.Context) Result {
	var findings []Finding
	facts := map[string]string{}
	t := s.env.Disk
	for _, target := range s.writableTargets() {
		usage, err := s.env.Statfs(target.path)
		if err != nil {
			findings = append(findings, warnf(target.subject, CodeDiskUnavailable))
			continue
		}
		facts[target.subject+".freeBytes"] = strconv.FormatUint(usage.FreeBytes, 10)
		switch {
		case usage.FreeBytes < byteLevel(t.CriticalBytes, t.CriticalBytesPct, usage.TotalBytes):
			findings = append(findings, failf(target.subject, CodeDiskSpaceCritical))
		case usage.FreeBytes < byteLevel(t.LowBytes, t.LowBytesPercent, usage.TotalBytes):
			findings = append(findings, warnf(target.subject, CodeDiskSpaceLow))
		}
		if !usage.InodesKnown || usage.TotalInodes == 0 {
			if !usage.InodesKnown {
				findings = append(findings, okf(target.subject, CodeDiskNoInodes))
			}
			continue
		}
		facts[target.subject+".freeInodes"] = strconv.FormatUint(usage.FreeInodes, 10)
		percent := float64(usage.FreeInodes) * 100 / float64(usage.TotalInodes)
		switch {
		case percent < t.CriticalInodePct:
			findings = append(findings, failf(target.subject, CodeDiskInodesCrit))
		case percent < t.LowInodePercent:
			findings = append(findings, warnf(target.subject, CodeDiskInodesLow))
		}
	}
	if len(findings) == 0 {
		findings = append(findings, okf("", CodeDiskOK))
	}
	r := newResult("disk", findings...)
	r.Facts = facts
	return r
}

// ---- network ----

func (s *Session) checkNetwork(context.Context) Result {
	var findings []Finding
	host, port, err := net.SplitHostPort(s.env.Config.Listen)
	n, perr := strconv.Atoi(port)
	if err != nil || net.ParseIP(host) == nil || perr != nil || n < 1 || n > 65535 {
		findings = append(findings, failf("listen", CodeNetListen))
	}
	prefixes, err := s.env.Config.TrustedProxyPrefixes()
	facts := map[string]string{"trustedProxies": strconv.Itoa(len(s.env.Config.TrustedProxies))}
	switch {
	case err != nil:
		findings = append(findings, failf("trustedProxies", CodeNetProxyInvalid))
	case len(prefixes) == 0:
		findings = append(findings, okf("trustedProxies", CodeNetProxyNone))
	default:
		for _, prefix := range prefixes {
			if prefix.Addr().Is4() && prefix.Bits() < 8 || prefix.Addr().Is6() && prefix.Bits() < 16 {
				findings = append(findings, failf("trustedProxies", CodeNetProxyBroad))
				break
			}
		}
	}
	if len(findings) == 0 || allOK(findings) {
		findings = append([]Finding{okf("", CodeNetOK)}, findings...)
	}
	r := newResult("network", findings...)
	r.Facts = facts
	return r
}

func allOK(findings []Finding) bool {
	for _, f := range findings {
		if f.Status != StatusOK {
			return false
		}
	}
	return true
}

// ---- directories ----

func (s *Session) checkDirectories(ctx context.Context) Result {
	var findings []Finding
	findings = append(findings, inspectDir("tempdir", s.env.TempDir, dirShared))
	images := s.env.Config.Images
	switch {
	case images.TempRoot != "":
		findings = append(findings, inspectDir("images.tempRoot", images.TempRoot, dirPrivate))
	case s.env.Config.EnableImages:
		findings = append(findings, failf("images.tempRoot", CodeDirMissing))
	}
	if images.StoreRoot != "" {
		findings = append(findings, inspectDir("images.storeRoot", images.StoreRoot, dirPrivate))
	}
	if logFileEnabled(s.env.Config.Logging.Output) && s.env.Config.Logging.File.Path != "" {
		path := s.env.Config.Logging.File.Path
		dir := inspectDir("logging.file", filepath.Dir(path), dirLog)
		if dir.Code == CodeDirMissing {
			// The rotating writer creates its directory with mode 0700.
			dir = warnf("logging.file", CodeDirMissing)
		}
		findings = append(findings, dir)
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && groupOrOtherAccess(info) {
			findings = append(findings, warnf("logging.file", CodeLogFileMode))
		}
	}
	return newResult("directories", findings...)
}

type dirPolicy int

const (
	// dirShared is a system temporary directory: it may be world-writable
	// but then must carry the sticky bit.
	dirShared dirPolicy = iota
	// dirPrivate must be owned by the service account with no group or
	// other access.
	dirPrivate
	// dirLog must be writable; broad permissions only warn.
	dirLog
)

func inspectDir(subject, path string, policy dirPolicy) Finding {
	if path == "" || !filepath.IsAbs(path) {
		return failf(subject, CodeDirMissing)
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return failf(subject, CodeDirMissing)
	case err != nil:
		return failf(subject, CodeDirNotWritable)
	case info.Mode()&fs.ModeSymlink != 0 && policy == dirPrivate:
		return failf(subject, CodeDirSymlink)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		// Shared temp and log directories may be reached through a link.
		if info, err = os.Stat(path); err != nil {
			return failf(subject, CodeDirMissing)
		}
	}
	if !info.IsDir() {
		return failf(subject, CodeDirNotDirectory)
	}
	if !writable(path) {
		return failf(subject, CodeDirNotWritable)
	}
	mode := inspectMode(info)
	if !mode.checked {
		return okf(subject, CodeDirModeUnchecked)
	}
	switch policy {
	case dirPrivate:
		if !mode.ownedBySelf {
			return failf(subject, CodeDirOwner)
		}
		if mode.groupOrOther {
			return failf(subject, CodeDirPermissive)
		}
	case dirShared:
		if mode.otherWrite && !mode.sticky {
			return warnf(subject, CodeDirNotSticky)
		}
	case dirLog:
		if mode.groupOrOther {
			return warnf(subject, CodeDirPermissive)
		}
	}
	return okf(subject, CodeDirOK)
}

// writable creates and removes a probe file. The name is unique and never
// matches a scratch kind, so a crash leaves only an inert empty file.
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".jelee-doctor-*")
	if err != nil {
		return false
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	return closeErr == nil && removeErr == nil
}

// ---- privacy ----

func (s *Session) checkPrivacy(context.Context) Result {
	var findings []Finding
	cfg := s.env.Config
	facts := map[string]string{}
	host, _, err := net.SplitHostPort(cfg.Listen)
	addr, perr := netip.ParseAddr(host)
	switch {
	case err != nil || perr != nil:
		// The network check reports the malformed address.
	case addr.IsLoopback():
		findings = append(findings, okf("listen", CodePrivacyLoopback))
		facts["listenScope"] = "loopback"
	case addr.IsUnspecified() || addr.IsPrivate() || addr.IsLinkLocalUnicast():
		findings = append(findings, warnf("listen", CodePrivacyExposed))
		facts["listenScope"] = "network"
	default:
		findings = append(findings, warnf("listen", CodePrivacyPublic))
		facts["listenScope"] = "public"
	}
	debug := strings.EqualFold(cfg.Logging.Level, "debug")
	for _, level := range cfg.Logging.Components {
		debug = debug || strings.EqualFold(level, "debug")
	}
	if debug {
		findings = append(findings, warnf("logging.level", CodePrivacyDebugLog))
	}
	if logging.IPMode(cfg.Logging.IPMode) == logging.IPMask {
		findings = append(findings, okf("logging.ipMode", CodePrivacyIPMask))
	}
	if logging.PathMode(cfg.Logging.PathMode) == logging.PathRelative {
		findings = append(findings, okf("logging.pathMode", CodePrivacyRelPath))
	}
	if cfg.TMDBAPIKey != "" {
		findings = append(findings, okf("tmdb", CodePrivacyTMDB))
	}
	r := newResult("privacy", findings...)
	r.Facts = facts
	return r
}

// ---- developer mode ----

func (s *Session) checkDevMode(context.Context) Result {
	in := devmode.ReadEnvironment(s.env.Lookup)
	in.ConfigEnabled = s.env.Config.Dev.Enabled
	r := Result{Facts: map[string]string{"devMode": "off"}}
	// Mirrors config.LoadWith: only empty, true and false are accepted.
	raw, _ := s.env.Lookup(devmode.EnvDevMode)
	switch v := strings.ToLower(strings.TrimSpace(raw)); {
	case v != "" && v != "true" && v != "false":
		r.Findings = append(r.Findings, failf(devmode.EnvDevMode, CodeDevEnvInvalid))
	case in.IsProduction():
		r.Findings = append(r.Findings, okf(devmode.EnvEnvironment, CodeDevProduction))
		if in.EnvFlag || in.ConfigEnabled {
			r.Findings = append(r.Findings, warnf(devmode.EnvEnvironment, CodeDevProductionIgnored))
		}
	case in.EnvFlag && in.ConfigEnabled:
		// The session itself lives in the database and needs a one-time
		// token; doctor only reports that this instance could open one.
		r.Facts["devMode"] = "capable"
		r.Findings = append(r.Findings, warnf(devmode.EnvDevMode, CodeDevCapable))
	case in.EnvFlag || in.ConfigEnabled:
		r.Findings = append(r.Findings, okf("", CodeDevDisabled), warnf(devmode.EnvDevMode, CodeDevEnvSet))
	default:
		r.Findings = append(r.Findings, okf("", CodeDevDisabled))
	}
	r.Check = "devmode"
	return finish(r)
}

// ---- external connectivity ----

func (s *Session) checkExternal(ctx context.Context) Result {
	if s.env.Config.TMDBAPIKey == "" || s.env.TMDB == nil {
		return newResult("external", okf("tmdb", CodeExternalNotConfigured))
	}
	switch s.env.TMDB(ctx) {
	case ExternalOK:
		return newResult("external", okf("tmdb", CodeExternalOK))
	case ExternalCredentials:
		return newResult("external", failf("tmdb", CodeExternalCredentials))
	case ExternalRateLimited:
		return newResult("external", warnf("tmdb", CodeExternalRateLimited))
	}
	return newResult("external", failf("tmdb", CodeExternalUnreachable))
}

var errDiskUnavailable = errors.New("disk usage unavailable")

// ReadDisk reads the filesystem usage of path, as the disk check does. It
// is shared with the storage metrics of the default alert rules (G50.6).
func ReadDisk(path string) (DiskUsage, error) { return statfs(path) }
