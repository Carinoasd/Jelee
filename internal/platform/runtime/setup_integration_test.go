package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// G18 end to end: a production runtime on a fresh database serves nothing
// but the wizard, prints a one-time token, completes over HTTP and then
// serves normally with the administrator the wizard created.
func TestSetupRuntimePostgresWizardOpensGate(t *testing.T) {
	ctx, _, _, runtimeDSN, _ := metricsIntegrationStore(t)
	output := captureSetupOutput(t)
	values := map[string]string{"JELEE_DATABASE_URL": runtimeDSN, "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_CATALOG": "true", "JELEE_MAX_CONNECTIONS": "6"}
	cfg, err := config.LoadWith(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil {
		t.Fatal("load setup runtime configuration")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	life := newLifetime(logger)
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
		t.Fatal("build setup runtime")
	}
	startup, stopStartup := context.WithTimeout(ctx, 15*time.Second)
	err = application.Start(startup)
	stopStartup()
	if err != nil {
		t.Fatal("start setup runtime")
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := application.Stop(stopCtx); err != nil {
			t.Error("stop setup runtime")
		}
	})
	line := strings.TrimSpace(output.String())
	token := line[strings.LastIndex(line, " ")+1:]
	if len(token) != 43 || strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("setup token not printed once: %q", output.String())
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	call := func(method, path, setupToken, body string) (int, string) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = bytes.NewBufferString(body)
		}
		request, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if setupToken != "" {
			request.Header.Set("X-Jelee-Setup-Token", setupToken)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}
	expect := func(method, path, setupToken, body string, status int, fragment string) {
		t.Helper()
		if got, text := call(method, path, setupToken, body); got != status || !strings.Contains(text, fragment) {
			t.Fatalf("%s %s: %d %s", method, path, got, text)
		}
	}
	expect("GET", "/api/v1/items", "", "", 503, `"setup_required"`)
	expect("POST", "/api/v1/auth/login", "", `{"name":"owner","password":"x"}`, 503, `"setup_required"`)
	expect("GET", "/readyz", "", "", 200, `"setup":"required"`)
	// G50.5: dependency states are fixed codes; the startup self-check ran
	// against this very handler before the listener opened.
	for _, fragment := range []string{`"database":"ok"`, `"schema":"current"`, `"jobs":"disabled"`, `"devMode":"off"`, `"startup":"`} {
		expect("GET", "/readyz", "", "", 200, fragment)
	}
	if _, text := call("GET", "/readyz", "", ""); strings.Contains(text, runtimeDSN) || strings.Contains(text, "version\"") {
		t.Fatalf("readiness leaked configuration: %s", text)
	}
	expect("GET", "/api/v1/setup", "", "", 401, `"setup_token_invalid"`)
	expect("GET", "/api/v1/setup", token, "", 200, `"current":"language"`)
	media := t.TempDir()
	mediaJSON, _ := json.Marshal(media)
	for _, step := range []struct{ name, body string }{
		{"language", `{"locale":"en-US"}`},
		{"admin", `{"name":"owner","displayName":"Owner","password":"correct horse battery"}`},
		{"database", ``},
		{"media", `{"libraries":[{"name":"Movies","path":` + string(mediaJSON) + `}]}`},
		{"tmdb", `{"enabled":false}`},
		{"toolchain", `{"acceptDegraded":true}`},
		{"metadata-policy", `{"nfoRead":"read-only","nfoWrite":"off","imageFetch":false,"imageWriteBack":false}`},
		{"network", `{"mode":"local","listen":"127.0.0.1:8097","allowedHosts":["localhost","127.0.0.1"],"privacyAcknowledged":false}`},
	} {
		expect("POST", "/api/v1/setup/steps/"+step.name, token, step.body, 200, `"version"`)
	}
	// The administrator exists but the instance stays closed until completion.
	expect("POST", "/api/v1/auth/login", "", `{"name":"owner","password":"correct horse battery"}`, 503, `"setup_required"`)
	expect("POST", "/api/v1/setup/complete", token, "", 200, `"completedAt"`)
	expect("GET", "/api/v1/setup/status", "", "", 410, `"setup_completed"`)
	expect("GET", "/readyz", "", "", 200, `"setup":"completed"`)
	expect("GET", "/api/v1/items", "", "", 401, `"authentication_required"`)
	expect("POST", "/api/v1/auth/login", "", `{"name":"owner","password":"correct horse battery"}`, 200, `"token"`)
}
