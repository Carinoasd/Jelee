package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var nativeTestClient = domain.NativeClient{Name: "Player", Device: "Living room", DeviceID: "device-1", Version: "1.2.3"}

func nativeLoginInput(c domain.Credentials, client domain.NativeClient) domain.LoginInput {
	in := accountLoginInput(c, true)
	in.Native, in.Client, in.DeviceName = true, client, client.Device
	return in
}

func TestNativeLoginRequiresAllowNative(t *testing.T) {
	ctx, s, _ := accountTestStore(t)
	if _, err := s.BootstrapAdmin(ctx, accountInput("NativeRoot")); err != nil {
		t.Fatal("bootstrap administrator", err)
	}
	admin := accountActor(accountLogin(t, ctx, s, "nativeroot"))
	user := createAccount(t, ctx, s, admin, "native-user")
	if user.AllowNative {
		t.Fatal("new users must not allow native devices")
	}
	creds := func() domain.Credentials {
		c, err := s.Credentials(ctx, "native-user")
		if err != nil {
			t.Fatal("read credentials", err)
		}
		return c
	}
	count := func(query string, args ...any) int {
		var n int
		if err := s.Pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal("count", err)
		}
		return n
	}

	if _, err := s.CommitLogin(ctx, nativeLoginInput(creds(), nativeTestClient)); !errors.Is(err, domain.ErrNativeLoginDisabled) {
		t.Fatalf("native login without permission: %v", err)
	}
	if n := count(`SELECT count(*) FROM sessions WHERE user_id=$1::uuid`, user.ID); n != 0 {
		t.Fatal("refused native login created a session", n)
	}
	if n := count(`SELECT count(*) FROM audit_logs WHERE event='login.native_denied' AND target_id=$1::uuid AND category='security'`, user.ID); n != 1 {
		t.Fatal("refused native login was not audited", n)
	}
	// A wrong password is a failed login first; the permission is never revealed.
	wrong := nativeLoginInput(creds(), nativeTestClient)
	wrong.PasswordOK = false
	if _, err := s.CommitLogin(ctx, wrong); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("wrong password reported %v", err)
	}
	if n := count(`SELECT failed_login FROM users WHERE id=$1::uuid`, user.ID); n != 1 {
		t.Fatal("native login failure did not count toward lockout", n)
	}

	// A plain user cannot grant itself the permission.
	self := accountActor(accountLogin(t, ctx, s, "native-user"))
	if _, err := s.SetNativeAccess(ctx, self, user.ID, true); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("self grant: %v", err)
	}
	updated, err := s.SetNativeAccess(ctx, admin, user.ID, true)
	if err != nil || !updated.AllowNative {
		t.Fatalf("enable native: %+v %v", updated, err)
	}
	if n := count(`SELECT count(*) FROM audit_logs WHERE event='user.native_access_changed' AND target_id=$1::uuid AND actor_id=$2::uuid
 AND before_state->>'allowNative'='false' AND after_state->>'allowNative'='true'`, user.ID, admin.UserID); n != 1 {
		t.Fatal("enabling native access was not audited", n)
	}
	// Unchanged values are a no-op without a second audit row.
	if _, err = s.SetNativeAccess(ctx, admin, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM audit_logs WHERE event='user.native_access_changed'`); n != 1 {
		t.Fatal("no-op change audited", n)
	}

	grant, err := s.CommitLogin(ctx, nativeLoginInput(creds(), nativeTestClient))
	if err != nil || grant.Session.ClientKind != string(access.ClientNative) || grant.Session.Client != "Player" || grant.Session.DeviceID != "device-1" ||
		grant.Session.Version != "1.2.3" || grant.Session.DeviceName != "Living room" || !grant.User.AllowNative || len(grant.Token) != 43 {
		t.Fatalf("native login: %+v %v", grant.Session, err)
	}
	p, err := s.Authenticate(ctx, grant.Token)
	if err != nil || p.Kind != access.ClientNative {
		t.Fatalf("native token does not authenticate as native: %+v %v", p, err)
	}
	// A web login of the same user stays a web session with no client labels.
	web := accountLogin(t, ctx, s, "native-user")
	if web.Session.ClientKind != string(access.ClientWeb) || web.Session.Client != "" || web.Session.DeviceID != "" {
		t.Fatalf("web login changed: %+v", web.Session)
	}

	// Rotation keeps the native kind and the reported identity.
	rotated, err := s.RotateSession(ctx, domain.Actor{UserID: user.ID, SessionID: grant.Session.ID, IP: "127.0.0.1"}, "Bedroom", time.Hour)
	if err != nil || rotated.Session.ClientKind != "native" || rotated.Session.Client != "Player" || rotated.Session.DeviceID != "device-1" || rotated.Session.Version != "1.2.3" || rotated.Session.DeviceName != "Bedroom" {
		t.Fatalf("rotate native: %+v %v", rotated.Session, err)
	}

	// Sessions list for self and every session for the administrator.
	own, err := s.ListSessions(ctx, self, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range own {
		found = found || item.ID == rotated.Session.ID && item.Client == "Player" && item.DeviceID == "device-1" && item.Version == "1.2.3"
	}
	if !found {
		t.Fatalf("own session list lacks native client fields: %+v", own)
	}
	if _, err = s.ListAllSessions(ctx, self, "", 100); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("plain user listed all sessions: %v", err)
	}
	all, err := s.ListAllSessions(ctx, admin, "", 100)
	if err != nil || len(all) != count(`SELECT count(*) FROM sessions WHERE revoked_at IS NULL AND expires_at>now()`) {
		t.Fatalf("admin session list: %d %v", len(all), err)
	}
	var paged []domain.Session
	for cursor := ""; ; {
		page, err := s.ListAllSessions(ctx, admin, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, page...)
		if len(page) < 1 {
			break
		}
		cursor = page[0].ID
	}
	if len(paged) != len(all) {
		t.Fatal("cursor pagination lost sessions", len(paged), len(all))
	}

	// Withdrawing the permission revokes native sessions only and is audited.
	if _, err = s.SetNativeAccess(ctx, admin, user.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, rotated.Token); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("native session survived withdrawal", err)
	}
	if _, err = s.Authenticate(ctx, web.Token); err != nil {
		t.Fatal("withdrawal revoked a web session", err)
	}
	if n := count(`SELECT count(*) FROM audit_logs WHERE event='user.native_access_changed' AND after_state->>'allowNative'='false' AND (after_state->>'nativeSessionsRevoked')::int=1`); n != 1 {
		t.Fatal("withdrawal audit missing revoked count", n)
	}
	if _, err = s.CommitLogin(ctx, nativeLoginInput(creds(), nativeTestClient)); !errors.Is(err, domain.ErrNativeLoginDisabled) {
		t.Fatal("native login after withdrawal", err)
	}
}

func TestNativeLoginClientFieldLimits(t *testing.T) {
	ctx, s, _ := accountTestStore(t)
	if _, err := s.BootstrapAdmin(ctx, accountInput("NativeRoot")); err != nil {
		t.Fatal(err)
	}
	admin := accountActor(accountLogin(t, ctx, s, "nativeroot"))
	if _, err := s.SetNativeAccess(ctx, admin, admin.UserID, true); err != nil {
		t.Fatal(err)
	}
	creds, err := s.Credentials(ctx, "nativeroot")
	if err != nil {
		t.Fatal(err)
	}
	with := func(change func(*domain.NativeClient)) domain.NativeClient {
		c := nativeTestClient
		change(&c)
		return c
	}
	for _, client := range []domain.NativeClient{
		with(func(c *domain.NativeClient) { c.Name = "" }), with(func(c *domain.NativeClient) { c.DeviceID = "" }),
		with(func(c *domain.NativeClient) { c.Name = strings.Repeat("c", 129) }), with(func(c *domain.NativeClient) { c.DeviceID = strings.Repeat("d", 257) }),
		with(func(c *domain.NativeClient) { c.Device = strings.Repeat("e", 129) }), with(func(c *domain.NativeClient) { c.Version = strings.Repeat("9", 65) }),
		with(func(c *domain.NativeClient) { c.Name = "a\x00b" }), with(func(c *domain.NativeClient) { c.DeviceID = "a\u0085b" }),
		with(func(c *domain.NativeClient) { c.Version = "1\n" }), with(func(c *domain.NativeClient) { c.Device = "\xff" }),
	} {
		if _, err = s.CommitLogin(ctx, nativeLoginInput(creds, client)); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid client %q accepted: %v", client, err)
		}
	}
	// A web login cannot smuggle client labels.
	web := accountLoginInput(creds, true)
	web.Client = nativeTestClient
	if _, err = s.CommitLogin(ctx, web); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("web login stored client labels", err)
	}
	maximal := domain.NativeClient{Name: strings.Repeat("c", 128), Device: strings.Repeat("e", 128), DeviceID: strings.Repeat("d", 256), Version: strings.Repeat("9", 64)}
	grant, err := s.CommitLogin(ctx, nativeLoginInput(creds, maximal))
	if err != nil || grant.Session.DeviceID != maximal.DeviceID {
		t.Fatal("maximal client labels refused", err)
	}
	minimal, err := s.CommitLogin(ctx, nativeLoginInput(creds, domain.NativeClient{Name: "P", DeviceID: "d"}))
	if err != nil {
		t.Fatal(err)
	}
	var nulls bool
	if err = s.Pool.QueryRow(ctx, `SELECT device_name='' AND client_version IS NULL AND last_seen_at IS NULL AND last_ip IS NULL FROM sessions WHERE id=$1::uuid`, minimal.Session.ID).Scan(&nulls); err != nil || !nulls {
		t.Fatal("absent labels not stored as NULL", err)
	}
	// The schema is the backstop for writers that bypass the store checks.
	for _, statement := range []string{
		`UPDATE sessions SET client_name='' WHERE id=$1::uuid`,
		`UPDATE sessions SET client_name=repeat('c',129) WHERE id=$1::uuid`,
		`UPDATE sessions SET device_id=repeat('d',257) WHERE id=$1::uuid`,
		`UPDATE sessions SET device_id=E'a\nb' WHERE id=$1::uuid`,
		`UPDATE sessions SET client_version=repeat('9',65) WHERE id=$1::uuid`,
		`UPDATE sessions SET client_version=E'1\t' WHERE id=$1::uuid`,
		`UPDATE sessions SET last_ip='not an address' WHERE id=$1::uuid`,
		`UPDATE sessions SET last_ip=repeat('1',46) WHERE id=$1::uuid`,
		`UPDATE sessions SET last_seen_at='infinity' WHERE id=$1::uuid`,
	} {
		if _, err = s.Pool.Exec(ctx, statement, minimal.Session.ID); !errors.Is(storageError(err), domain.ErrInvalid) {
			t.Fatalf("schema accepted %s: %v", statement, err)
		}
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE users SET allow_native=NULL WHERE name='NativeRoot'`); err == nil {
		t.Fatal("schema accepted a NULL native permission")
	}
}

func TestSessionLastUseIsThrottled(t *testing.T) {
	ctx, s, _ := accountTestStore(t)
	if _, err := s.BootstrapAdmin(ctx, accountInput("NativeRoot")); err != nil {
		t.Fatal(err)
	}
	grant := accountLogin(t, ctx, s, "nativeroot")
	read := func() (*time.Time, string) {
		var seen *time.Time
		var ip *string
		if err := s.Pool.QueryRow(ctx, `SELECT last_seen_at,last_ip FROM sessions WHERE id=$1::uuid`, grant.Session.ID).Scan(&seen, &ip); err != nil {
			t.Fatal(err)
		}
		if ip == nil {
			return seen, ""
		}
		return seen, *ip
	}
	// Plain Authenticate never writes.
	if _, err := s.Authenticate(ctx, grant.Token); err != nil {
		t.Fatal(err)
	}
	if seen, _ := read(); seen != nil {
		t.Fatal("Authenticate recorded use")
	}
	if _, err := s.AuthenticateFrom(ctx, grant.Token, "::ffff:192.0.2.7"); err != nil {
		t.Fatal(err)
	}
	first, ip := read()
	if first == nil || ip != "192.0.2.7" {
		t.Fatalf("first use not recorded: %v %q", first, ip)
	}
	// Within the interval nothing is written, even from another address.
	for i := 0; i < 5; i++ {
		if _, err := s.AuthenticateFrom(ctx, grant.Token, "2001:db8::1"); err != nil {
			t.Fatal(err)
		}
	}
	if again, ip := read(); !again.Equal(*first) || ip != "192.0.2.7" {
		t.Fatalf("throttled use was written: %v %q", again, ip)
	}
	// Once the record is older than the interval the next use writes it.
	if _, err := s.Pool.Exec(ctx, `UPDATE sessions SET last_seen_at=last_seen_at-interval '61 seconds' WHERE id=$1::uuid`, grant.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateFrom(ctx, grant.Token, "2001:db8::1"); err != nil {
		t.Fatal(err)
	}
	later, ip := read()
	if !later.After(*first) || ip != "2001:db8::1" {
		t.Fatalf("stale use not recorded: %v %q", later, ip)
	}
	// An unparsable address records the time and keeps the last good address.
	if _, err := s.Pool.Exec(ctx, `UPDATE sessions SET last_seen_at=last_seen_at-interval '61 seconds' WHERE id=$1::uuid`, grant.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateFrom(ctx, grant.Token, "unknown"); err != nil {
		t.Fatal(err)
	}
	if latest, ip := read(); !latest.After(*later) || ip != "2001:db8::1" {
		t.Fatalf("bad address handling: %v %q", latest, ip)
	}
	// A session row locked by an account transaction is skipped, not waited on.
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE sessions SET last_seen_at=NULL WHERE id=$1::uuid`, grant.Session.ID); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err = s.AuthenticateFrom(ctx, grant.Token, "192.0.2.9"); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("session touch waited for a locked row")
	}
	_ = tx.Rollback(ctx)
	sessions, err := s.ListSessions(ctx, accountActor(grant), grant.User.ID)
	if err != nil || len(sessions) != 1 || sessions[0].LastSeenAt == nil || sessions[0].LastIP != "2001:db8::1" {
		t.Fatalf("session list lacks last use: %+v %v", sessions, err)
	}
}

func TestNativeSessionMigrationRoundTrip(t *testing.T) {
	ctx, s, dsn := accountTestStore(t)
	if _, err := s.BootstrapAdmin(ctx, accountInput("NativeRoot")); err != nil {
		t.Fatal(err)
	}
	admin := accountActor(accountLogin(t, ctx, s, "nativeroot"))
	if _, err := s.SetNativeAccess(ctx, admin, admin.UserID, true); err != nil {
		t.Fatal(err)
	}
	creds, err := s.Credentials(ctx, "nativeroot")
	if err != nil {
		t.Fatal(err)
	}
	native, err := s.CommitLogin(ctx, nativeLoginInput(creds, nativeTestClient))
	if err != nil {
		t.Fatal(err)
	}
	want := migrationVersion(t, "native_session_devices")
	version, dirty, err := Migrate(ctx, dsn, "status")
	for err == nil && !dirty && version >= want {
		version, dirty, err = Migrate(ctx, dsn, "down")
	}
	if err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade below native sessions: version=%d dirty=%t error=%v", version, dirty, err)
	}
	var removed bool
	if err = s.Pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema()
 AND (table_name='users' AND column_name='allow_native' OR table_name='sessions' AND column_name IN ('device_id','client_name','client_version','last_seen_at','last_ip')))`).Scan(&removed); err != nil || !removed {
		t.Fatal("downgrade left native session columns", err)
	}
	// Issued native sessions stay valid and revocable under the older schema.
	var kind string
	if err = s.Pool.QueryRow(ctx, `SELECT client_kind FROM sessions WHERE id=$1::uuid AND revoked_at IS NULL`, native.Session.ID).Scan(&kind); err != nil || kind != "native" {
		t.Fatal("downgrade lost the native session", kind, err)
	}
	if version, dirty, err = Migrate(ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: version=%d dirty=%t error=%v", version, dirty, err)
	}
	if err = s.Ready(ctx); err != nil {
		t.Fatal("store not ready after round trip", err)
	}
	// The permission returns closed; the old session keeps working.
	user, err := s.GetUser(ctx, admin, admin.UserID)
	if err != nil || user.AllowNative {
		t.Fatalf("permission after round trip: %+v %v", user, err)
	}
	if p, err := s.Authenticate(ctx, native.Token); err != nil || p.Kind != access.ClientNative {
		t.Fatal("native session after round trip", err)
	}
	if _, err = s.CommitLogin(ctx, nativeLoginInput(creds, nativeTestClient)); !errors.Is(err, domain.ErrNativeLoginDisabled) {
		t.Fatal("native login after round trip", err)
	}
}
