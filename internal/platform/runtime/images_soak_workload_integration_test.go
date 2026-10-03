//go:build jelee_probe_tests

package runtime

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

// This verifies the real scan aggregates and rotation helpers, using tiny scan
// files. It does not decode images or substitute for the fixed 24h fixtures.
func TestImagesSoakWorkloadPostgresIntegration(t *testing.T) {
	ctx, store, _, runtimeDSN, _ := metricsIntegrationStore(t)
	root := t.TempDir()
	var fixtureBytes int64
	write := func(path string) {
		t.Helper()
		path = filepath.Join(root, filepath.FromSlash(path))
		if os.MkdirAll(filepath.Dir(path), 0700) != nil || os.WriteFile(path, []byte("fixture\n"), 0600) != nil {
			t.Fatal("write private scan fixture")
		}
		fixtureBytes += 8
	}
	for i := 0; i < 1000; i++ {
		write(imagesMemoryMediaPath(i))
		write(imagesMemoryPosterPath(i, 1000))
	}
	for _, name := range []string{"unsupported", "corrupt", "oversized-source", "oversized-dimensions"} {
		write("negative/" + name + "/clip.mkv")
		write("negative/" + name + "/clip-poster.jpg")
	}
	registration, err := store.RegisterLibrary(ctx, "soak scan contract", root)
	if err != nil {
		t.Fatal("register scan fixtures")
	}
	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		t.Fatal("create production KDF")
	}
	const secret = "soak-integration-private-password"
	hash, err := hasher.Hash(ctx, secret)
	if err != nil {
		t.Fatal("hash fixture credential")
	}
	if _, err = store.BootstrapAdmin(ctx, domain.UserInput{Name: "SoakAdmin", DisplayName: "Soak admin", Locale: "en-US", PasswordHash: hash}); err != nil {
		t.Fatal("bootstrap fixture admin")
	}
	values := map[string]string{"JELEE_DATABASE_URL": runtimeDSN, "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_CATALOG": "true", "JELEE_ENABLE_JOBS": "true", "JELEE_JOB_WORKERS": "1", "JELEE_MAX_CONNECTIONS": "8"}
	cfg, err := config.LoadWith(func(key string) (string, bool) { v, ok := values[key]; return v, ok })
	if err != nil {
		t.Fatal("load runtime")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	life := newLifetime(logger)
	address := ""
	life.listen = func(c context.Context, network, _ string) (net.Listener, error) {
		l, e := (&net.ListenConfig{}).Listen(c, network, "127.0.0.1:0")
		if e == nil {
			address = "http://" + l.Addr().String()
		}
		return l, e
	}
	app := newWithLifetime(cfg, logger, life)
	if app.Err() != nil {
		t.Fatal("build runtime")
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if app.Stop(c) != nil {
			t.Error("stop runtime")
		}
	})
	if app.Start(ctx) != nil {
		t.Fatal("start runtime")
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	login := func(name string) domain.SessionGrant {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"name": name, "password": secret, "deviceName": "soak fixture"})
		var grant domain.SessionGrant
		if imagesMemoryAPI(ctx, client, address, "POST", "/api/v1/auth/login", body, "", "", 200, &grant) != nil {
			t.Fatal("login real account")
		}
		return grant
	}
	admin := login("SoakAdmin")
	actor := domain.Actor{UserID: admin.User.ID, SessionID: admin.Session.ID, IP: "127.0.0.1"}
	if _, _, err := store.CreateUser(ctx, actor, domain.UserInput{Name: "SoakViewer", DisplayName: "Soak viewer", Locale: "en-US", PasswordHash: hash}, "soak-viewer"); err != nil {
		t.Fatal("create viewer")
	}
	viewer := login("SoakViewer")
	for _, grant := range []*domain.SessionGrant{&admin, &viewer} {
		updated, evidence, err := rotateImagesSoakSession(ctx, client, address, *grant)
		if err != nil || !evidence.Rotated || !evidence.OldRejected || !evidence.NewAccepted {
			t.Fatal("real rotation contract failed")
		}
		*grant = updated
		var active int
		if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1::uuid AND revoked_at IS NULL AND expires_at>now()`, grant.User.ID).Scan(&active); err != nil || active != 1 {
			t.Fatal("rotation retained extra live sessions")
		}
	}
	for round := 0; round < 2; round++ {
		result, code := runImagesSoakScan(ctx, client, address, admin.Token, store, registration, round, fixtureBytes)
		if code != "" {
			t.Fatalf("real mixed scan rejected: %s", code)
		}
		if result.Files != 2008 || result.Video != 1004 || result.Image != 1004 || result.DoneDirectories != 106 || !result.Published {
			t.Fatal("mixed scan evidence incomplete")
		}
	}
}
