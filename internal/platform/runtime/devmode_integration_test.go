package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// G45 end to end on PostgreSQL: a stored session is not inherited by a
// restart, the loopback entry issues a token, a CLI-side enable reaches the
// running instance (header and system info), verbose logging follows its
// toggle, and an explicit disable restores everything.
func TestDevModeRuntimePostgres(t *testing.T) {
	ctx, store, _, runtimeDSN, _ := metricsIntegrationStore(t)
	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash(ctx, "devmode-fixture-password-2026")
	if err != nil {
		t.Fatal(err)
	}
	// An administrator adopts the instance, so setup does not gate it.
	if _, err = store.BootstrapAdmin(ctx, domain.UserInput{Name: "dev-admin", DisplayName: "Dev", Locale: "en-US", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	capable := devmode.Inputs{EnvFlag: true, ConfigEnabled: true}
	cli, err := devmode.NewController(devmode.ControllerOptions{Store: store, Local: capable})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := cli.IssueToken(ctx, devmode.Actor{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cli.Enable(ctx, devmode.Actor{}, token, "cli", "", 0); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, []byte(`{"dev":{"enabled":true,"ttlMinutes":60}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"JELEE_DATABASE_URL": runtimeDSN, "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_CATALOG": "true", "JELEE_MAX_CONNECTIONS": "6",
		"JELEE_CONFIG": path, "JELEE_DEV_MODE": "true"}
	cfg, err := config.LoadWith(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil || !cfg.Dev.Capable() {
		t.Fatal("load developer runtime configuration", err)
	}
	// The production log router, wired the way NewWithLogs wires it, so
	// the developer logs are checked against the real whitelist.
	logs := &syncBuffer{}
	router, err := logging.Open(logging.Options{}, logs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = router.Close() })
	// plain records what call sites write before the whitelist, for the
	// startup lines and for proving that nothing was written at all.
	plain := &syncBuffer{}
	logger := slog.New(slog.NewMultiHandler(router.Logger().Handler(), slog.NewJSONHandler(plain, nil)))
	life := newLifetime(logger)
	levels := router
	life.levels = levels
	life.devLogs = router
	flushed := func() string {
		_ = router.Flush()
		return logs.String()
	}
	address := ""
	life.listen = func(listenCtx context.Context, network, _ string) (net.Listener, error) {
		listener, err := (&net.ListenConfig{}).Listen(listenCtx, network, "127.0.0.1:0")
		if err == nil {
			address = listener.Addr().String()
		}
		return listener, err
	}
	application := newWithLifetime(cfg, logger, life)
	if application.Err() != nil {
		t.Fatal("build developer runtime", application.Err())
	}
	startup, stopStartup := context.WithTimeout(ctx, 15*time.Second)
	err = application.Start(startup)
	stopStartup()
	if err != nil {
		t.Fatal("start developer runtime")
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := application.Stop(stopCtx); err != nil {
			t.Error("stop developer runtime")
		}
	})
	if rec, err := store.LoadDevSession(ctx); err != nil || rec.Active {
		t.Fatalf("restart inherited the session: %+v %v", rec, err)
	}
	if out := plain.String(); !strings.Contains(out, "never run this configuration in production") || !strings.Contains(out, "process restart") {
		t.Fatalf("startup WARN lines missing: %s", out)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, target, body string) *http.Response {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, "http://"+address+target, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "localhost"
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	devHeader := func() string {
		resp := request("GET", "/healthz", "")
		resp.Body.Close()
		return resp.Header.Get("X-Jelee-Dev-Mode")
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if devHeader() != "false" {
		t.Fatal("inactive instance reports developer mode")
	}
	resp := request("POST", "/api/v1/dev/token", "{}")
	var issued struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	err = json.NewDecoder(resp.Body).Decode(&issued)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("token over loopback: %d %v", resp.StatusCode, err)
	}
	if _, err = cli.Enable(ctx, devmode.Actor{}, issued.Data.Token, "cli", "", 0); err != nil {
		t.Fatal(err)
	}
	waitFor("the developer header", func() bool { return devHeader() == "true" })
	resp = request("GET", "/api/v1/system", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"devMode":true`) || !strings.Contains(string(body), `"devModeExpiresAt"`) {
		t.Fatalf("system info: %s", body)
	}
	// The running instance wires verbose logging, so the toggle exists.
	if _, err = life.dev.SetToggle(ctx, devmode.Actor{}, devmode.DebugVerboseLogging, true, devmode.Confirmation{}, "test"); err != nil {
		t.Fatal(err)
	}
	waitFor("the debug level", func() bool { return levels.Level(logging.GlobalComponent) == slog.LevelDebug })
	// SQL and body logs pass the production whitelist while their toggles
	// are on (G45.5), with secrets masked.
	if _, err = life.dev.SetToggle(ctx, devmode.Actor{}, devmode.DebugSQLLogging, true, devmode.Confirmation{}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err = life.dev.SetToggle(ctx, devmode.Actor{}, devmode.DebugBodyLogging, true, devmode.Confirmation{IUnderstand: true}, "test"); err != nil {
		t.Fatal(err)
	}
	resp = request("POST", "/api/v1/auth/login", `{"name":"dev-admin","password":"wrong-devmode-password-77"}`)
	_ = resp.Body.Close()
	waitFor("developer SQL and body log lines", func() bool {
		out := flushed()
		return strings.Contains(out, `"code":"devmode_sql_log","statement":"SELECT`) && strings.Contains(out, `"code":"devmode_body_log"`)
	})
	if out := flushed(); strings.Contains(out, "wrong-devmode-password-77") || !strings.Contains(out, `\"name\":\"dev-admin\"`) || !strings.Contains(out, `"route":"/api/v1/auth/login"`) {
		t.Fatalf("developer logs: %s", out)
	}
	if err = cli.Disable(ctx, devmode.Actor{}, "cli", "test"); err != nil {
		t.Fatal(err)
	}
	waitFor("production defaults", func() bool {
		return devHeader() == "false" && levels.Level(logging.GlobalComponent) == slog.LevelInfo
	})
	// Off again: no developer field reaches the log.
	mark, plainMark := len(flushed()), len(plain.String())
	resp = request("POST", "/api/v1/auth/login", `{"name":"dev-admin","password":"wrong-devmode-password-78"}`)
	_ = resp.Body.Close()
	out, written := flushed()[mark:], plain.String()[plainMark:]
	if strings.Contains(out, "devmode_sql_log") || strings.Contains(out, "devmode_body_log") || strings.Contains(out, `"statement"`) || strings.Contains(out, "requestBody") ||
		strings.Contains(written, "developer mode SQL log") || strings.Contains(written, "developer mode body log") {
		t.Fatalf("developer logs after disable: %s", out)
	}
}
