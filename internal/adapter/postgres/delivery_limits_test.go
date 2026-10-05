package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

// TestDeliveryLimitsAndRevocationOverHTTP runs the real store, HTTP router and
// loopback TCP: per-user overrides through the admin API, the playback limit,
// and a revocation that cuts a running download within one check interval.
func TestDeliveryLimitsAndRevocationOverHTTP(t *testing.T) {
	f := newJobFixture(t)
	admin := accountLogin(t, f.ctx, f.s, "job-admin")
	user := createAccount(t, f.ctx, f.s, f.a, "limited-viewer")
	if _, err := f.s.SetNativeAccess(f.ctx, f.a, user.ID, true); err != nil {
		t.Fatal(err)
	}
	creds, err := f.s.Credentials(f.ctx, "limited-viewer")
	if err != nil {
		t.Fatal(err)
	}
	native, err := f.s.CommitLogin(f.ctx, nativeLoginInput(creds, nativeTestClient))
	if err != nil {
		t.Fatal(err)
	}
	var root string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("jelee-limit-"), (8<<20)/12)
	sources := map[string]string{}
	for _, name := range []string{"a", "b"} {
		if err = os.WriteFile(filepath.Join(root, name+".mkv"), content, 0o600); err != nil {
			t.Fatal(err)
		}
		var item, source string
		if err = f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,'Movie') RETURNING id::text`, f.registration.Library.ID, "Limit "+name).Scan(&item); err != nil {
			t.Fatal(err)
		}
		if err = f.s.Pool.QueryRow(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska') RETURNING id::text`,
			item, f.registration.Library.ID, f.registration.RootID, name+".mkv").Scan(&source); err != nil {
			t.Fatal(err)
		}
		sources[name] = source
	}
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, user.ID, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}

	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(f.s, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	streaming := config.DefaultStreamingConfig()
	streaming.RevokeCheckSeconds = 1
	cfg := config.Config{Resources: config.DefaultResourcesConfig(), Streaming: streaming, Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test",
		MaxConnections: 16, MaxStreams: 4, RequestTimeoutSeconds: 5, EnableAccounts: true, Accounts: config.DefaultAccountsConfig(), EnableCatalog: true, EnableDirect: true}
	handler, err := httpapi.NewWithJobs(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	send := func(method, path, body, token string) *http.Response {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		r, err := http.NewRequestWithContext(f.ctx, method, server.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		r.Host = "localhost"
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	request := func(method, path, body, token string) (int, string) {
		t.Helper()
		response := send(method, path, body, token)
		defer response.Body.Close()
		value, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return response.StatusCode, strings.TrimSpace(string(value))
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := f.s.Pool.QueryRow(f.ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	limits := "/api/v1/users/" + user.ID + "/delivery-limits"
	if status, _ := request("GET", limits, "", native.Token); status != 403 {
		t.Fatal("plain user read delivery limits", status)
	}
	if status, _ := request("PUT", limits, `{"maxStreams":0}`, native.Token); status != 403 {
		t.Fatal("plain user changed delivery limits", status)
	}
	if status, body := request("GET", limits, "", admin.Token); status != 200 || body != `{"data":{}}` {
		t.Fatalf("default overrides %d: %s", status, body)
	}
	for _, invalid := range []string{`{"maxStreams":-1}`, `{"maxStreams":129}`, `{"maxKbps":10000001}`, `{"maxStreams":null}`, `{"maxStreams":"1"}`, `{"streams":1}`} {
		if status, _ := request("PUT", limits, invalid, admin.Token); status != 400 {
			t.Fatalf("invalid overrides %s accepted: %d", invalid, status)
		}
	}
	if status, _ := request("PUT", "/api/v1/users/00000000-0000-0000-0000-000000000001/delivery-limits", `{}`, admin.Token); status != 404 {
		t.Fatal("unknown user", status)
	}
	// One playback at 8000 kbps (1 MB/s) keeps an 8 MiB download running.
	if status, body := request("PUT", limits, `{"maxStreams":1,"maxKbps":8000}`, admin.Token); status != 200 || body != `{"data":{"maxStreams":1,"maxKbps":8000}}` {
		t.Fatalf("set overrides %d: %s", status, body)
	}
	if status, _ := request("PUT", limits, `{"maxKbps":8000,"maxStreams":1}`, admin.Token); status != 200 {
		t.Fatal("unchanged overrides", status)
	}
	if n := count(`SELECT count(*) FROM audit_logs WHERE event='user.delivery_limits_changed' AND target_id=$1::uuid AND actor_id=$2::uuid
 AND before_state='{"maxKbps":null,"maxStreams":null}'::jsonb AND after_state='{"maxKbps":8000,"maxStreams":1}'::jsonb`, user.ID, f.a.UserID); n != 1 {
		t.Fatal("override change audited", n)
	}
	if n := count(`SELECT count(*) FROM audit_logs WHERE event='user.delivery_limits_changed'`); n != 1 {
		t.Fatal("unchanged overrides audited", n)
	}

	stream := send("GET", "/api/v1/sources/"+sources["a"]+"/stream", "", native.Token)
	defer stream.Body.Close()
	if stream.StatusCode != 200 || stream.ContentLength != int64(len(content)) || stream.Header.Values("Content-Security-Policy")[0] != "sandbox; default-src 'none'" || len(stream.Header.Values("Content-Security-Policy")) != 1 {
		t.Fatal("stream", stream.StatusCode, stream.Header.Values("Content-Security-Policy"))
	}
	head := make([]byte, 256<<10)
	if _, err = io.ReadFull(stream.Body, head); err != nil {
		t.Fatal(err)
	}
	status, body := request("GET", "/api/v1/sources/"+sources["b"]+"/stream", "", native.Token)
	if status != 429 || !strings.Contains(body, `"code":"user_stream_limit"`) {
		t.Fatalf("second playback over the override %d: %s", status, body)
	}
	if active, err := f.s.SessionActive(f.ctx, user.ID, native.Session.ID); err != nil || !active {
		t.Fatal("live session inactive", err)
	}
	if status, _ = request("DELETE", "/api/v1/users/"+user.ID+"/sessions/"+native.Session.ID, "", admin.Token); status != 204 {
		t.Fatal("revoke", status)
	}
	revoked := time.Now()
	rest, err := io.ReadAll(stream.Body)
	cut := time.Since(revoked)
	if err == nil || len(head)+len(rest) >= len(content) {
		t.Fatalf("download survived revocation: bytes=%d err=%v", len(head)+len(rest), err)
	}
	t.Logf("download cut %v after revocation, %d of %d bytes delivered", cut, len(head)+len(rest), len(content))
	if cut > 2500*time.Millisecond {
		t.Fatalf("revoked download ended after %v, check interval is 1s", cut)
	}
	if active, err := f.s.SessionActive(f.ctx, user.ID, native.Session.ID); err != nil || active {
		t.Fatal("revoked session active", err)
	}
	if status, _ = request("GET", "/api/v1/sources/"+sources["a"]+"/stream", "", native.Token); status != 401 {
		t.Fatal("revoked token streamed", status)
	}
	// Disabled users and malformed IDs are inactive too.
	if active, err := f.s.SessionActive(f.ctx, f.a.UserID, f.a.SessionID); err != nil || !active {
		t.Fatal("admin session inactive", err)
	}
	if active, err := f.s.SessionActive(f.ctx, user.ID, f.a.SessionID); err != nil || active {
		t.Fatal("session matched another user", err)
	}
	if active, err := f.s.SessionActive(f.ctx, "not-a-uuid", f.a.SessionID); err != nil || active {
		t.Fatal("malformed ID", err)
	}
	second, err := f.s.CommitLogin(f.ctx, nativeLoginInput(creds, nativeTestClient))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, user.ID); err != nil {
		t.Fatal(err)
	}
	if active, err := f.s.SessionActive(f.ctx, user.ID, second.Session.ID); err != nil || active {
		t.Fatal("disabled user's session active", err)
	}

	if status, body = request("PUT", limits, `{}`, admin.Token); status != 200 || body != `{"data":{}}` {
		t.Fatalf("reset overrides %d: %s", status, body)
	}
	var data struct {
		Data domain.DeliveryLimits `json:"data"`
	}
	if status, body = request("PUT", limits, `{"maxStreams":0}`, admin.Token); status != 200 || json.Unmarshal([]byte(body), &data) != nil || data.Data.MaxStreams == nil || *data.Data.MaxStreams != 0 || data.Data.MaxKbps != nil {
		t.Fatalf("zero override %d: %s", status, body)
	}
}

func TestDeliveryLimitsMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	user := createAccount(t, f.ctx, f.s, f.a, "migrating-viewer")
	streams, kbps := 2, int64(4000)
	if _, err := f.s.SetDeliveryLimits(f.ctx, f.a, user.ID, domain.DeliveryLimits{MaxStreams: &streams, MaxKbps: &kbps}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET max_streams=129 WHERE id=$1::uuid`, user.ID); err == nil {
		t.Fatal("out-of-range override stored")
	}
	dsn := f.s.Pool.Config().ConnString()
	want := downgradeAboveMigration(t, f, "user_delivery_limits")
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade below delivery limits: version=%d dirty=%t error=%v", version, dirty, err)
	}
	var removed bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema()
 AND table_name='users' AND column_name IN ('max_streams','max_kbps'))`).Scan(&removed); err != nil || !removed {
		t.Fatal("downgrade left delivery limit columns", err)
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: version=%d dirty=%t error=%v", version, dirty, err)
	}
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("store not ready after round trip", err)
	}
	// Overrides return as "follow the server-wide value".
	limits, err := f.s.GetDeliveryLimits(f.ctx, f.a, user.ID)
	if err != nil || limits.MaxStreams != nil || limits.MaxKbps != nil {
		t.Fatalf("overrides after round trip: %+v %v", limits, err)
	}
	if _, err = f.s.SetDeliveryLimits(f.ctx, f.a, user.ID, domain.DeliveryLimits{MaxStreams: &streams}); err != nil {
		t.Fatal("set after round trip", err)
	}
	invalid := -1
	if _, err = f.s.SetDeliveryLimits(f.ctx, f.a, user.ID, domain.DeliveryLimits{MaxStreams: &invalid}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid override", err)
	}
}
