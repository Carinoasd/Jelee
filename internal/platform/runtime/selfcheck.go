package runtime

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
)

// Startup self-check (G50.5). After the HTTP handler is assembled and before
// the listener opens, the process sends itself a few requests and inspects
// its environment. A failed invariant refuses startup; a warning is logged
// and shown as startup=warn in /readyz. docs/deployment.md (startup self-check
// and readiness) lists the checks.

const (
	selfCheckOK   = "ok"
	selfCheckWarn = "warn"
	selfCheckFail = "fail"
)

// selfCheck is the outcome of one invariant. Code is a fixed value; it never
// holds a path, address or configured value.
type selfCheck struct {
	Name, Status, Code string
}

type selfCheckReport []selfCheck

// failure returns the first failed check.
func (r selfCheckReport) failure() (selfCheck, bool) {
	for _, c := range r {
		if c.Status == selfCheckFail {
			return c, true
		}
	}
	return selfCheck{}, false
}

// status is "ok" when every check passed, "warn" when one warned and
// "fail" when one failed.
func (r selfCheckReport) status() string {
	status := selfCheckOK
	for _, c := range r {
		switch c.Status {
		case selfCheckFail:
			return selfCheckFail
		case selfCheckWarn:
			status = selfCheckWarn
		}
	}
	return status
}

// selfCheckEnvironment is what the checks read besides the handler.
type selfCheckEnvironment struct {
	config config.Config
	dev    *devmode.Controller
	// lookPath reports whether an executable named name is on PATH. It
	// only inspects file metadata; nothing is ever run.
	lookPath func(name string) bool
}

// runSelfCheck runs every check against handler and logs each outcome. It
// returns an error naming the first failed invariant.
func runSelfCheck(handler http.Handler, env selfCheckEnvironment, logger *slog.Logger) (selfCheckReport, error) {
	report := selfCheckReport{
		checkAccessGate(handler, env.config),
		checkTranscodeGuard(handler, env.config),
		checkEncoderAbsent(env),
		checkDevMode(env),
	}
	for _, c := range report {
		level := slog.LevelInfo
		switch c.Status {
		case selfCheckWarn:
			level = slog.LevelWarn
		case selfCheckFail:
			level = slog.LevelError
		}
		logger.Log(context.Background(), level, "startup self-check", "component", "selfcheck", "check", c.Name, "status", c.Status, "code", c.Code)
	}
	if failed, ok := report.failure(); ok {
		return report, errors.New("startup self-check failed: " + failed.Code)
	}
	return report, nil
}

// selfRequest sends one anonymous GET through the assembled handler, as a
// loopback client of the first allowed host, and returns the status.
func selfRequest(handler http.Handler, cfg config.Config, path string) int {
	host := "localhost"
	if len(cfg.AllowedHosts) > 0 {
		host = cfg.AllowedHosts[0]
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+path, nil)
	if err != nil {
		return 0
	}
	request.RemoteAddr = net.JoinHostPort("127.0.0.1", "1")
	recorder := &selfCheckRecorder{header: http.Header{}}
	handler.ServeHTTP(recorder, request)
	if recorder.status == 0 {
		return http.StatusOK
	}
	return recorder.status
}

// checkAccessGate requires every protected surface to refuse a request
// without credentials: the authentication middleware and the permission
// filter behind it are assembled. 401, 403 and 503 (setup_required before
// initial setup) are refusals; anything else means the gate is missing.
func checkAccessGate(handler http.Handler, cfg config.Config) selfCheck {
	var paths []string
	if cfg.EnableAccounts {
		paths = append(paths, "/api/v1/users/me")
	}
	if cfg.EnableCatalog {
		paths = append(paths, "/api/v1/items")
	}
	if cfg.EnableJobs && cfg.EnableAccounts {
		paths = append(paths, "/api/v1/jobs")
	}
	if len(paths) == 0 {
		return selfCheck{Name: "access_filter", Status: selfCheckOK, Code: "access_no_protected_routes"}
	}
	for _, path := range paths {
		switch selfRequest(handler, cfg, path) {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable:
		default:
			return selfCheck{Name: "access_filter", Status: selfCheckFail, Code: "access_filter_missing"}
		}
	}
	return selfCheck{Name: "access_filter", Status: selfCheckOK, Code: "access_filter_assembled"}
}

// checkTranscodeGuard requires the transformation routes to answer 409
// transcode_disabled (G10.11): the delivery path cannot reach an encoder.
func checkTranscodeGuard(handler http.Handler, cfg config.Config) selfCheck {
	for _, path := range []string{"/transcode", "/Videos/00000000-0000-4000-8000-000000000000/hls/master.m3u8"} {
		if selfRequest(handler, cfg, path) != http.StatusConflict {
			return selfCheck{Name: "transcode_unreachable", Status: selfCheckFail, Code: "transcode_guard_missing"}
		}
	}
	return selfCheck{Name: "transcode_unreachable", Status: selfCheckOK, Code: "transcode_guard_active"}
}

// checkEncoderAbsent warns when an encoder executable is on PATH. Jelee
// never runs one (the delivery packages cannot start a process at all, see
// TestDeliveryPackagesCannotRunEncoders), and the delivery image ships none;
// its presence means the host or image is not the shipped one.
func checkEncoderAbsent(env selfCheckEnvironment) selfCheck {
	lookPath := env.lookPath
	if lookPath == nil {
		lookPath = executableOnPath
	}
	if lookPath("ffmpeg") {
		return selfCheck{Name: "encoder_absent", Status: selfCheckWarn, Code: "encoder_on_path"}
	}
	return selfCheck{Name: "encoder_absent", Status: selfCheckOK, Code: "encoder_not_found"}
}

// checkDevMode reports developer mode (G45): a capable instance or an active
// session is a warning, never a refusal, because an operator enabled it.
func checkDevMode(env selfCheckEnvironment) selfCheck {
	switch {
	case env.dev != nil && env.dev.Active():
		return selfCheck{Name: "dev_mode", Status: selfCheckWarn, Code: "dev_session_active"}
	case env.dev != nil && env.dev.Capable(), env.config.Dev.Capable():
		return selfCheck{Name: "dev_mode", Status: selfCheckWarn, Code: "dev_capable"}
	}
	return selfCheck{Name: "dev_mode", Status: selfCheckOK, Code: "dev_disabled"}
}

// executableOnPath looks for name in every PATH directory by metadata only.
func executableOnPath(name string) bool {
	names := []string{name}
	if goruntime.GOOS == "windows" {
		names = []string{name + ".exe"}
	}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if directory == "" || !filepath.IsAbs(directory) {
			continue
		}
		for _, n := range names {
			info, err := os.Stat(filepath.Join(directory, n)) //nolint:gosec // G703: metadata of a fixed name in each absolute PATH directory; nothing is opened or run

			if err == nil && info.Mode().IsRegular() && (goruntime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0) {
				return true
			}
		}
	}
	return false
}

// selfCheckRecorder keeps the status of an in-process response and
// discards its body.
type selfCheckRecorder struct {
	header http.Header
	status int
}

func (r *selfCheckRecorder) Header() http.Header { return r.header }
func (r *selfCheckRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *selfCheckRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return len(data), nil
}

// selfCheckState hands the report to /readyz once the checks ran.
type selfCheckState struct {
	report selfCheckReport
}

func (s *selfCheckState) status() string {
	if s == nil || s.report == nil {
		return "pending"
	}
	return s.report.status()
}

// readinessStates composes the dependency states of /readyz: the database,
// schema and job queue from the store, the probe runtime, the image store,
// developer mode and the startup self-check. Every value is a fixed code.
func readinessStates(c config.Config, store readinessStore, jobs *app.Jobs, processor *imageadapter.Processor, dev *devmode.Controller, checks *selfCheckState) func(context.Context) map[string]string {
	return func(ctx context.Context) map[string]string {
		states := store.Readiness(ctx)
		if !c.EnableJobs || jobs == nil {
			states["jobs"], states["probe"] = "disabled", "disabled"
		} else {
			switch capability := jobs.ProbeCapability(); {
			case capability.Available:
				states["probe"] = "available"
			case capability.Enabled:
				states["probe"] = "unavailable"
			default:
				states["probe"] = "disabled"
			}
		}
		switch {
		case !c.EnableImages:
			states["images"] = "disabled"
		case processor.OriginalStore() != nil:
			states["images"] = "ok"
		default:
			states["images"] = "no_store"
		}
		states["devMode"] = "off"
		if dev != nil && dev.Active() {
			states["devMode"] = "active"
		}
		states["startup"] = checks.status()
		return states
	}
}

// readinessStore is the store's part of /readyz.
type readinessStore interface {
	Readiness(ctx context.Context) map[string]string
}
