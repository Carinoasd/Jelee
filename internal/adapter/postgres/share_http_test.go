package postgres

import (
	"bytes"
	"encoding/json"
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

// TestShareGuestsOverHTTP runs share links end to end over loopback TCP
// with the real store and router (G48.6): creation, web and native
// redemption, the guest gate and read-only shares, the share's stream cap
// with the server-wide stream limit switched off, the access audit, and a
// revocation that cuts a running guest download within one check interval.
// It also drives a network rule through a trusted proxy (G48.5).
func TestShareGuestsOverHTTP(t *testing.T) {
	f := newJobFixture(t)
	admin := accountLogin(t, f.ctx, f.s, "job-admin")
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	lib := f.registration.Library.ID
	content := bytes.Repeat([]byte("jelee-share-"), (8<<20)/12)
	items, sources := map[string]string{}, map[string]string{}
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name+".mkv"), content, 0o600); err != nil {
			t.Fatal(err)
		}
		var item, source string
		if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,'Movie') RETURNING id::text`, lib, "Share "+name).Scan(&item); err != nil {
			t.Fatal(err)
		}
		if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska') RETURNING id::text`,
			item, lib, f.registration.RootID, name+".mkv").Scan(&source); err != nil {
			t.Fatal(err)
		}
		items[name], sources[name] = item, source
	}
	other, err := f.s.RegisterLibrary(f.ctx, "share-other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var hidden string
	if err = f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Outside Share Qz9','Movie') RETURNING id::text`, other.Library.ID).Scan(&hidden); err != nil {
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
	// The stream limit switch is off: only the share's own cap limits
	// guests. One MB/s keeps an 8 MiB download running.
	streaming := config.DefaultStreamingConfig()
	streaming.RevokeCheckSeconds, streaming.EnableStreamLimit, streaming.MaxKbpsPerUser = 1, false, 8000
	cfg := config.Config{Resources: config.DefaultResourcesConfig(), Streaming: streaming, Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test",
		MaxConnections: 16, MaxStreams: 4, RequestTimeoutSeconds: 5, EnableAccounts: true, Accounts: config.DefaultAccountsConfig(), EnableCatalog: true, EnableDirect: true,
		TrustedProxies: []string{"127.0.0.1/32", "::1/128"}}
	handler, err := httpapi.NewWithJobs(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	send := func(method, path, body, token string, header map[string]string) *http.Response {
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
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range header {
			r.Header.Set(k, v)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	request := func(method, path, body, token string, header map[string]string) (int, string) {
		t.Helper()
		response := send(method, path, body, token, header)
		defer func() { _ = response.Body.Close() }()
		value, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return response.StatusCode, strings.TrimSpace(string(value))
	}
	expect := func(what string, status int, body string, want int, code string) {
		t.Helper()
		if status != want || code != "" && !strings.Contains(body, `"code":"`+code+`"`) {
			t.Fatalf("%s: %d %s, want %d %s", what, status, body, want, code)
		}
	}
	create := func(body string) domain.ShareGrant {
		t.Helper()
		status, raw := request("POST", "/api/v1/shares", body, admin.Token, nil)
		var grant struct {
			Data domain.ShareGrant `json:"data"`
		}
		if status != 201 || json.Unmarshal([]byte(raw), &grant) != nil || len(grant.Data.Token) != 43 {
			t.Fatalf("create share %d: %s", status, raw)
		}
		return grant.Data
	}
	redeem := func(token string) string {
		t.Helper()
		status, raw := request("POST", "/api/v1/shares/redeem/native", `{"token":"`+token+`","client":"Guest Player","deviceId":"guest-1"}`, "", nil)
		var grant struct {
			Data domain.SessionGrant `json:"data"`
		}
		if status != 200 || json.Unmarshal([]byte(raw), &grant) != nil || grant.Data.Session.ClientKind != "native" {
			t.Fatalf("redeem %d: %s", status, raw)
		}
		return grant.Data.Token
	}
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	// Only administrators create shares; inputs are validated.
	status, body := request("POST", "/api/v1/shares", `{"libraryId":"`+lib+`","expiresAt":"`+expires+`","readOnly":true,"allowPlayback":false}`, "", nil)
	expect("anonymous create", status, body, 401, "authentication_required")
	for _, invalid := range []string{`{"libraryId":"` + lib + `","expiresAt":"` + expires + `","readOnly":true}`, `{"libraryId":"` + lib + `","itemId":"` + items["a"] + `","expiresAt":"` + expires + `","readOnly":true,"allowPlayback":false}`,
		`{"libraryId":"` + lib + `","expiresAt":"` + time.Now().Add(100*24*time.Hour).UTC().Format(time.RFC3339) + `","readOnly":true,"allowPlayback":false}`, `{"libraryId":"` + lib + `","expiresAt":"` + expires + `","readOnly":true,"allowPlayback":false,"maxStreams":0}`} {
		status, body = request("POST", "/api/v1/shares", invalid, admin.Token, nil)
		expect("invalid share "+invalid, status, body, 400, "invalid_request")
	}

	playing := create(`{"libraryId":"` + lib + `","expiresAt":"` + expires + `","readOnly":false,"allowPlayback":true,"maxStreams":1,"note":"family"}`)
	guest := redeem(playing.Token)
	// The native redemption refuses browsers, like the native login.
	status, body = request("POST", "/api/v1/shares/redeem/native", `{"token":"`+playing.Token+`","client":"x","deviceId":"y"}`, "", map[string]string{"Origin": "https://localhost"})
	expect("browser native redemption", status, body, 403, "forbidden")
	// A web redemption sets the session cookie.
	web := send("POST", "/api/v1/shares/redeem", `{"token":"`+playing.Token+`","deviceName":"browser"}`, "", nil)
	_ = web.Body.Close()
	if web.StatusCode != 200 || !strings.Contains(web.Header.Get("Set-Cookie"), "__Host-") {
		t.Fatalf("web redemption %d %q", web.StatusCode, web.Header.Get("Set-Cookie"))
	}
	status, body = request("POST", "/api/v1/shares/redeem", `{"token":"`+strings.Repeat("A", 43)+`"}`, "", nil)
	expect("unknown token", status, body, 404, "share_unavailable")

	// The guest sees the share's library only, and only guest routes.
	status, body = request("GET", "/api/v1/items?limit=50", "", guest, nil)
	if status != 200 || !strings.Contains(body, items["a"]) || !strings.Contains(body, items["b"]) || strings.Contains(body, hidden) {
		t.Fatalf("guest items %d: %s", status, body)
	}
	status, body = request("GET", "/api/v1/items/"+hidden, "", guest, nil)
	expect("guest item outside share", status, body, 404, "not_found")
	status, body = request("GET", "/api/v1/users", "", guest, nil)
	expect("guest user list", status, body, 403, "share_forbidden")
	status, body = request("GET", "/api/v1/shares", "", guest, nil)
	expect("guest share list", status, body, 403, "share_forbidden")
	status, body = request("GET", "/api/v1/shares/current", "", guest, nil)
	if status != 200 || !strings.Contains(body, playing.Share.ID) {
		t.Fatalf("guest current share %d: %s", status, body)
	}
	status, body = request("GET", "/api/v1/shares/current", "", admin.Token, nil)
	expect("administrator current share", status, body, 404, "not_found")

	// The share's cap holds with the stream limit switched off.
	stream := send("GET", "/api/v1/sources/"+sources["a"]+"/stream", "", guest, nil)
	defer func() { _ = stream.Body.Close() }()
	if stream.StatusCode != 200 {
		t.Fatal("guest stream", stream.StatusCode)
	}
	head := make([]byte, 256<<10)
	if _, err = io.ReadFull(stream.Body, head); err != nil {
		t.Fatal(err)
	}
	status, body = request("GET", "/api/v1/sources/"+sources["b"]+"/stream", "", guest, nil)
	expect("second guest playback", status, body, 429, "user_stream_limit")

	// Revoking the share cuts the running download.
	status, body = request("POST", "/api/v1/shares/"+playing.Share.ID+"/revoke", "{}", admin.Token, nil)
	if status != 200 || !strings.Contains(body, `"state":"revoked"`) {
		t.Fatalf("revoke %d: %s", status, body)
	}
	revoked := time.Now()
	rest, err := io.ReadAll(stream.Body)
	cut := time.Since(revoked)
	if err == nil || len(head)+len(rest) >= len(content) {
		t.Fatalf("guest download survived revocation: bytes=%d err=%v", len(head)+len(rest), err)
	}
	if cut > 2500*time.Millisecond {
		t.Fatalf("revoked guest download ended after %v, check interval is 1s", cut)
	}
	t.Logf("guest download cut %v after the share was revoked, %d of %d bytes delivered", cut, len(head)+len(rest), len(content))
	status, body = request("GET", "/api/v1/items", "", guest, nil)
	expect("revoked guest", status, body, 401, "authentication_required")
	status, body = request("POST", "/api/v1/shares/redeem/native", `{"token":"`+playing.Token+`","client":"Guest Player","deviceId":"guest-2"}`, "", nil)
	expect("revoked token", status, body, 404, "share_unavailable")

	// Every use is in the share's access records.
	status, body = request("GET", "/api/v1/shares/"+playing.Share.ID+"/access?limit=100", "", admin.Token, nil)
	if status != 200 {
		t.Fatalf("access records %d: %s", status, body)
	}
	for _, want := range []string{`"event":"share.created"`, `"event":"share.redeemed"`, `"event":"share.revoked"`, `"event":"share.redeem_refused"`, `"reason":"revoked"`,
		`"route":"GET /api/v1/items"`, `"route":"GET /api/v1/sources/{id}/stream"`, `"event":"share.access_refused"`, `"route":"GET /api/v1/users"`} {
		if !strings.Contains(body, want) {
			t.Errorf("access records lack %s: %s", want, body)
		}
	}
	if strings.Contains(body, playing.Token) {
		t.Fatal("access records contain the share token")
	}

	// A read-only share refuses writes; a share without playback issues no
	// native session.
	readOnly := create(`{"itemId":"` + items["b"] + `","expiresAt":"` + expires + `","readOnly":true,"allowPlayback":true}`)
	reader := redeem(readOnly.Token)
	status, body = request("PUT", "/api/v1/items/"+items["b"]+"/played", "{}", reader, nil)
	expect("read-only guest write", status, body, 403, "share_read_only")
	status, body = request("GET", "/api/v1/items/"+items["a"], "", reader, nil)
	expect("item guest outside item", status, body, 404, "not_found")
	browse := create(`{"libraryId":"` + lib + `","expiresAt":"` + expires + `","readOnly":true,"allowPlayback":false}`)
	status, body = request("POST", "/api/v1/shares/redeem/native", `{"token":"`+browse.Token+`","client":"Guest Player","deviceId":"guest-3"}`, "", nil)
	expect("native redemption without playback", status, body, 403, "share_playback_disabled")

	// A LAN-only network rule, judged on the address the trusted proxy
	// reports.
	status, body = request("POST", "/api/v1/access/network-rules", `{"libraryId":"`+lib+`","network":"lan","enabled":true}`, admin.Token, nil)
	if status != 201 {
		t.Fatalf("network rule %d: %s", status, body)
	}
	status, body = request("GET", "/api/v1/items/"+items["a"], "", reader, map[string]string{"X-Forwarded-For": "203.0.113.9"})
	expect("guest from the Internet", status, body, 404, "not_found")
	status, body = request("GET", "/api/v1/items/"+items["b"], "", reader, map[string]string{"X-Forwarded-For": "192.168.1.5"})
	if status != 200 {
		t.Fatalf("guest from the LAN %d: %s", status, body)
	}
	status, body = request("GET", "/api/v1/access/network-rules", "", admin.Token, nil)
	if status != 200 || !strings.Contains(body, `"network":"lan"`) {
		t.Fatalf("network rules %d: %s", status, body)
	}
}
