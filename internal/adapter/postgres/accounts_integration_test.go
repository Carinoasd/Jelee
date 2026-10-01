package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Repository tests supply the trusted external KDF result directly. Password
// verification itself belongs to the password/app contracts, not PostgreSQL.
const accountTestHash = "$argon2id$v=19$m=8192,t=1,p=1$c2FsdC1maXh0dXJl$dGVzdC1kaWdlc3QtZml4dHVyZQ"
const changedAccountTestHash = "$argon2id$v=19$m=8192,t=1,p=1$b3RoZXItc2FsdA$b3RoZXItZGlnaWVzdA"

func accountTestStore(t *testing.T) (context.Context, *Store, string) {
	t.Helper()
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required account integration database is unavailable")
		}
		t.Skip("account PostgreSQL integration NOT RUN: JELEE_TEST_DATABASE_URL unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/jelee_test" || u.Hostname() == "" {
		t.Fatal("account integration requires dedicated jelee_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect dedicated account test database")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var random [10]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal("generate schema identifier")
	}
	schema := "jelee_account_it_" + hex.EncodeToString(random[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("create owned account test schema")
	}
	t.Cleanup(func() {
		cleanCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := admin.Exec(cleanCtx, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error("remove owned account test schema")
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	if version, dirty, e := Migrate(ctx, u.String(), "up"); e != nil || dirty || version != SchemaVersion {
		t.Fatal("migrate account fixture to current schema")
	}
	store, err := Open(ctx, u.String(), 16)
	if err != nil {
		t.Fatal("open account test store")
	}
	t.Cleanup(store.Pool.Close)
	return ctx, store, u.String()
}

func accountInput(name string) domain.UserInput {
	return domain.UserInput{Name: name, DisplayName: name, Locale: "en-US", PasswordHash: accountTestHash}
}
func accountLoginInput(c domain.Credentials, valid bool) domain.LoginInput {
	return domain.LoginInput{Credentials: c, PasswordOK: valid, DeviceName: "contract-test", IP: "127.0.0.1", MaxSessions: 100, SessionTTL: time.Hour, LockAfter: 3, LockFor: time.Minute}
}
func accountActor(g domain.SessionGrant) domain.Actor {
	return domain.Actor{UserID: g.User.ID, SessionID: g.Session.ID, IP: "127.0.0.1"}
}
func accountLogin(t *testing.T, ctx context.Context, s *Store, name string) domain.SessionGrant {
	t.Helper()
	c, err := s.Credentials(ctx, name)
	if err != nil {
		t.Fatal("read login credential snapshot")
	}
	g, err := s.CommitLogin(ctx, accountLoginInput(c, true))
	if err != nil {
		t.Fatalf("commit valid login: %v", err)
	}
	return g
}
func createAccount(t *testing.T, ctx context.Context, s *Store, a domain.Actor, name string) domain.User {
	t.Helper()
	u, replayed, err := s.CreateUser(ctx, a, accountInput(name), "create-"+name)
	if err != nil || replayed {
		t.Fatalf("create test account: %v", err)
	}
	return u
}

func TestAccountIntegration(t *testing.T) {
	ctx, s, _ := accountTestStore(t)
	root, err := s.BootstrapAdmin(ctx, accountInput("RootAdmin"))
	if err != nil || !root.Admin {
		t.Fatal("bootstrap first administrator")
	}
	if _, err = s.BootstrapAdmin(ctx, accountInput("AnotherRoot")); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("second bootstrap must fail atomically")
	}
	rootGrant := accountLogin(t, ctx, s, "rootadmin")
	admin := accountActor(rootGrant)

	t.Run("casefold_idempotency_and_live_admin_rechecks", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make(chan domain.User, 12)
		failures := make(chan error, 12)
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				u, _, e := s.CreateUser(ctx, admin, accountInput("OneCreation"), "same-key")
				results <- u
				failures <- e
			}()
		}
		wg.Wait()
		close(results)
		close(failures)
		for e := range failures {
			if e != nil {
				t.Fatalf("concurrent idempotent create: %v", e)
			}
		}
		id := ""
		for u := range results {
			if id == "" {
				id = u.ID
			}
			if u.ID != id {
				t.Fatal("idempotency created multiple users")
			}
		}
		altered := accountInput("IgnoredReplayName")
		altered.PasswordHash = changedAccountTestHash
		u, replayed, e := s.CreateUser(ctx, admin, altered, "same-key")
		if e != nil || !replayed || u.ID != id || u.Name != "OneCreation" {
			t.Fatal("replay must return original resource without mutation")
		}
		credentials, e := s.Credentials(ctx, "ONECREATION")
		if e != nil || credentials.PasswordHash != accountTestHash {
			t.Fatal("replay changed password or casefold lookup failed")
		}
		if _, _, e = s.CreateUser(ctx, admin, accountInput("onecreation"), "different-key"); !errors.Is(e, domain.ErrConflict) {
			t.Fatal("case-only duplicate name accepted")
		}
		var count int
		if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE event='user.created' AND target_id=$1::uuid`, id).Scan(&count); e != nil || count != 1 {
			t.Fatal("idempotent retries repeated mutation/audit")
		}
		regular := accountActor(accountLogin(t, ctx, s, "onecreation"))
		if _, _, e = s.CreateUser(ctx, regular, accountInput("Forbidden"), "same-key"); !errors.Is(e, domain.ErrForbidden) {
			t.Fatal("nonadministrator can create account")
		}
		forged := domain.Actor{UserID: admin.UserID, SessionID: regular.SessionID}
		if _, _, e = s.CreateUser(ctx, forged, accountInput("Forged"), "same-key"); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("cross-user session spoof accepted")
		}
		input := accountInput("RevokedAdmin")
		input.Admin = true
		other, _, e := s.CreateUser(ctx, admin, input, "other-admin")
		if e != nil {
			t.Fatal(e)
		}
		otherActor := accountActor(accountLogin(t, ctx, s, other.Name))
		if _, _, e = s.CreateUser(ctx, otherActor, accountInput("BeforeRevocation"), "replay-after-revoke"); e != nil {
			t.Fatal(e)
		}
		if e = s.RevokeSession(ctx, admin, other.ID, otherActor.SessionID); e != nil {
			t.Fatal(e)
		}
		if _, _, e = s.CreateUser(ctx, otherActor, accountInput("AfterRevocation"), "replay-after-revoke"); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("revoked admin replay bypassed authentication")
		}
		input.Admin = false
		if _, e = s.UpdateUser(ctx, admin, other.ID, input); e != nil {
			t.Fatal(e)
		}
	})

	t.Run("self_only_profile_credentials_and_sessions", func(t *testing.T) {
		u := createAccount(t, ctx, s, admin, "PrivateUser")
		g := accountLogin(t, ctx, s, u.Name)
		actor := accountActor(g)
		if own, e := s.GetUser(ctx, actor, u.ID); e != nil || own.ID != u.ID {
			t.Fatal("own user unavailable")
		}
		if _, e := s.GetUser(ctx, actor, root.ID); !errors.Is(e, domain.ErrForbidden) {
			t.Fatal("ordinary user saw another account")
		}
		if _, e := s.ListUsers(ctx, actor, "", 10, false); !errors.Is(e, domain.ErrForbidden) {
			t.Fatal("ordinary user enumerated accounts")
		}
		if _, e := s.CredentialsFor(ctx, actor, root.ID); !errors.Is(e, domain.ErrForbidden) {
			t.Fatal("ordinary user accessed another password hash")
		}
		if _, e := s.ListSessions(ctx, actor, root.ID); !errors.Is(e, domain.ErrForbidden) {
			t.Fatal("ordinary user accessed another session list")
		}
		updated, e := s.UpdateProfile(ctx, actor, domain.ProfileInput{DisplayName: "Local Profile", Locale: "zh-TW", Hidden: true})
		if e != nil || !updated.Hidden || updated.Locale != "zh-TW" || updated.Admin {
			t.Fatal("profile update changed wrong fields")
		}
		if _, e = s.GetUser(ctx, admin, u.ID); e != nil {
			t.Fatal("hidden user disappeared from administrator")
		}
		principal, e := s.Authenticate(ctx, g.Token)
		if e != nil || principal.Locale != "zh-TW" {
			t.Fatal("persisted profile locale missing from authentication")
		}
		if _, e = s.UpdateUser(ctx, actor, u.ID, accountInput("CannotPromote")); !errors.Is(e, domain.ErrForbidden) {
			t.Fatal("ordinary user accessed admin edit")
		}
	})

	t.Run("trusted_local_password_reset_revokes_legacy_credentials", func(t *testing.T) {
		token, e := s.Provision(ctx, "LocalMigration", access.ClientNative, false)
		if e != nil {
			t.Fatal(e)
		}
		c, e := s.Credentials(ctx, "localmigration")
		if e != nil || c.PasswordHash != "" {
			t.Fatal("legacy credential state")
		}
		u, e := s.SetLocalPassword(ctx, "LOCALMIGRATION", changedAccountTestHash)
		if e != nil || u.ID != c.UserID {
			t.Fatal("local password migration failed")
		}
		if _, e = s.Authenticate(ctx, token); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("local password reset did not revoke legacy token")
		}
		updated, e := s.Credentials(ctx, u.Name)
		if e != nil || updated.Version <= c.Version || updated.PasswordHash != changedAccountTestHash {
			t.Fatal("local reset did not update credential version")
		}
		var localAudit bool
		if e = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE target_id=$1::uuid AND event='user.password_reset_local' AND actor_id IS NULL)`, u.ID).Scan(&localAudit); e != nil || !localAudit {
			t.Fatal("trusted operator audit missing")
		}
		if e = s.DeleteUser(ctx, admin, u.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = s.SetLocalPassword(ctx, u.Name, accountTestHash); !errors.Is(e, domain.ErrNotFound) {
			t.Fatal("local reset resurrected deleted user")
		}
		if _, e = s.Credentials(ctx, "UnknownLoginName"); !errors.Is(e, domain.ErrNotFound) {
			t.Fatal("unknown credential lookup must reach application dummy KDF path")
		}
	})

	t.Run("oversized_session_and_ACL_results_fail_explicitly", func(t *testing.T) {
		u := createAccount(t, ctx, s, admin, "BoundedLists")
		if _, e := s.Pool.Exec(ctx, `INSERT INTO sessions(user_id,token_hash,client_kind,expires_at) SELECT $1::uuid,decode(lpad(to_hex(i),64,'0'),'hex'),'web',now()+interval '1 hour' FROM generate_series(1,1001) i`, u.ID); e != nil {
			t.Fatal("prepare legacy excessive sessions")
		}
		if _, e := s.ListSessions(ctx, admin, u.ID); !errors.Is(e, domain.ErrConflict) {
			t.Fatal("oversized sessions silently truncated or unbounded")
		}
		if e := s.RevokeSessions(ctx, admin, u.ID); e != nil {
			t.Fatal(e)
		}
		if _, e := s.Pool.Exec(ctx, `WITH inserted AS (INSERT INTO libraries(name) SELECT 'Bounded Library '||i FROM generate_series(1,1001) i RETURNING id) INSERT INTO library_acl(user_id,library_id) SELECT $1::uuid,id FROM inserted`, u.ID); e != nil {
			t.Fatal("prepare legacy excessive ACL")
		}
		if _, e := s.GetLibraryAccess(ctx, admin, u.ID); !errors.Is(e, domain.ErrConflict) {
			t.Fatal("oversized ACL silently truncated or unbounded")
		}
		if e := s.ReplaceLibraryAccess(ctx, admin, u.ID, nil); !errors.Is(e, domain.ErrConflict) {
			t.Fatal("oversized existing ACL replaced without complete audit")
		}
		var count int
		if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM library_acl WHERE user_id=$1::uuid`, u.ID).Scan(&count); e != nil || count != 1001 {
			t.Fatal("failed oversized ACL replacement mutated grants")
		}
	})

	t.Run("login_failure_counter_lock_and_credential_CAS", func(t *testing.T) {
		u := createAccount(t, ctx, s, admin, "LockTarget")
		snapshot, e := s.Credentials(ctx, u.Name)
		if e != nil {
			t.Fatal(e)
		}
		var wg sync.WaitGroup
		results := make(chan error, 12)
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, e := s.CommitLogin(ctx, accountLoginInput(snapshot, false)); results <- e }()
		}
		wg.Wait()
		close(results)
		for e := range results {
			if !errors.Is(e, domain.ErrUnauthenticated) {
				t.Fatalf("failed login leaked different error: %v", e)
			}
		}
		var failures int
		var locked bool
		if e = s.Pool.QueryRow(ctx, `SELECT failed_login,locked_until>now() FROM users WHERE id=$1::uuid`, u.ID).Scan(&failures, &locked); e != nil || failures != 3 || !locked {
			t.Fatal("concurrent failures lost increments or bypassed lock")
		}
		if _, e = s.CommitLogin(ctx, accountLoginInput(snapshot, true)); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("valid KDF bypassed active lock")
		}
		if e = s.UnlockUser(ctx, admin, u.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = s.CommitLogin(ctx, accountLoginInput(snapshot, true)); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("stale pre-unlock credential snapshot accepted")
		}
		g := accountLogin(t, ctx, s, u.Name)
		actor := accountActor(g)
		current, e := s.CredentialsFor(ctx, actor, u.ID)
		if e != nil {
			t.Fatal(e)
		}
		wrong := current
		wrong.Version++
		if e = s.ReplacePassword(ctx, actor, u.ID, wrong, changedAccountTestHash); !errors.Is(e, domain.ErrConflict) {
			t.Fatal("password CAS mismatch accepted")
		}
		if e = s.ReplacePassword(ctx, actor, u.ID, current, changedAccountTestHash); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Authenticate(ctx, g.Token); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("password change failed to revoke session")
		}
		if _, e = s.CommitLogin(ctx, accountLoginInput(current, true)); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("KDF result from old password committed")
		}
		newCredentials, e := s.Credentials(ctx, u.Name)
		if e != nil || newCredentials.PasswordHash != changedAccountTestHash || newCredentials.Version <= current.Version {
			t.Fatal("password/version not atomically updated")
		}
	})

	t.Run("session_limit_rotation_and_revoke_are_atomic", func(t *testing.T) {
		u := createAccount(t, ctx, s, admin, "SessionTarget")
		c, e := s.Credentials(ctx, u.Name)
		if e != nil {
			t.Fatal(e)
		}
		in := accountLoginInput(c, true)
		in.MaxSessions = 1
		var wg sync.WaitGroup
		grants := make(chan domain.SessionGrant, 12)
		errs := make(chan error, 12)
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); g, e := s.CommitLogin(ctx, in); grants <- g; errs <- e }()
		}
		wg.Wait()
		close(grants)
		close(errs)
		success := 0
		for e := range errs {
			if e == nil {
				success++
			} else if !errors.Is(e, domain.ErrSessionLimit) {
				t.Fatalf("unexpected limit error: %v", e)
			}
		}
		if success != 1 {
			t.Fatal("concurrent logins bypassed maximum sessions")
		}
		var initial domain.SessionGrant
		for g := range grants {
			if g.Token != "" {
				initial = g
			}
		}
		actor := accountActor(initial)
		rotations := make(chan domain.SessionGrant, 8)
		rotationErrors := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				g, e := s.RotateSession(ctx, actor, "replacement", time.Hour)
				rotations <- g
				rotationErrors <- e
			}()
		}
		wg.Wait()
		close(rotations)
		close(rotationErrors)
		success = 0
		for e := range rotationErrors {
			if e == nil {
				success++
			} else if !errors.Is(e, domain.ErrUnauthenticated) {
				t.Fatalf("unexpected rotate error: %v", e)
			}
		}
		if success != 1 {
			t.Fatal("one credential rotated more than once")
		}
		var rotated domain.SessionGrant
		for g := range rotations {
			if g.Token != "" {
				rotated = g
			}
		}
		if rotated.Session.ClientKind != "web" || rotated.Token == initial.Token {
			t.Fatal("rotation changed client trust or reused token")
		}
		if _, e = s.Authenticate(ctx, initial.Token); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("rotated token remains valid")
		}
		if _, e = s.Authenticate(ctx, rotated.Token); e != nil {
			t.Fatal("replacement token unavailable")
		}
		list, e := s.ListSessions(ctx, accountActor(rotated), u.ID)
		if e != nil || len(list) != 1 || list[0].ID != rotated.Session.ID {
			t.Fatal("active session listing is inconsistent")
		}
		if e = s.RevokeSessions(ctx, accountActor(rotated), u.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Authenticate(ctx, rotated.Token); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("revoke-all did not invalidate token")
		}
		if _, e = s.CommitLogin(ctx, in); e != nil {
			t.Fatal("revoked session still consumed session budget")
		}
	})

	t.Run("ACL_replace_and_soft_delete_close_all_catalog_paths", func(t *testing.T) {
		token, nativeErr := s.Provision(ctx, "NativeLegacy", access.ClientNative, false)
		if nativeErr != nil {
			t.Fatal(nativeErr)
		}
		p, e := s.Authenticate(ctx, token)
		if e != nil {
			t.Fatal(e)
		}
		nativeActor := domain.Actor{UserID: p.UserID, SessionID: p.SessionID}
		sourceID, e := s.ImportVideo(ctx, "AccountLibrary", "/synthetic/account-library", "sample.mp4", "Sample", "video/mp4")
		if e != nil {
			t.Fatal(e)
		}
		var libraryID, itemID string
		if e = s.Pool.QueryRow(ctx, `SELECT library_id::text,item_id::text FROM media_sources WHERE id=$1::uuid`, sourceID).Scan(&libraryID, &itemID); e != nil {
			t.Fatal("read imported identifiers")
		}
		if e = s.ReplaceLibraryAccess(ctx, admin, p.UserID, []string{libraryID}); e != nil {
			t.Fatal(e)
		}
		if e = s.ReplaceLibraryAccess(ctx, nativeActor, p.UserID, nil); !errors.Is(e, domain.ErrForbidden) {
			t.Fatal("ordinary user changed own ACL")
		}
		if e = s.ReplaceLibraryAccess(ctx, admin, p.UserID, []string{"00000000-0000-0000-0000-000000000001"}); !errors.Is(e, domain.ErrNotFound) {
			t.Fatal("missing library accepted")
		}
		grants, e := s.GetLibraryAccess(ctx, nativeActor, p.UserID)
		if e != nil || len(grants) != 1 || grants[0].LibraryID != libraryID {
			t.Fatal("failed replacement removed old ACL")
		}
		if _, e = s.Resolve(ctx, p, sourceID); e != nil {
			t.Fatal("authorized source not visible before deletion")
		}
		if e = s.DeleteUser(ctx, admin, p.UserID); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Authenticate(ctx, token); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("deleted user authenticated")
		}
		items, e := s.ListItems(ctx, p.UserID, "", 10)
		if e != nil || len(items) != 0 {
			t.Fatal("deleted user listed catalog")
		}
		if _, e = s.GetItem(ctx, p.UserID, itemID); !errors.Is(e, domain.ErrNotFound) {
			t.Fatal("deleted user accessed item")
		}
		if _, e = s.Resolve(ctx, p, sourceID); !errors.Is(e, media.ErrNotFound) {
			t.Fatal("deleted user resolved source")
		}
		restored, e := s.RestoreUser(ctx, admin, p.UserID)
		if e != nil || restored.DeletedAt != nil || restored.ID != p.UserID {
			t.Fatal("restore lost user identity")
		}
		if _, e = s.Authenticate(ctx, token); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("restore resurrected revoked token")
		}
		c, e := s.Credentials(ctx, "NativeLegacy")
		if e != nil || c.PasswordHash != "" {
			t.Fatal("legacy provision password must stay null")
		}
		if _, e = s.CommitLogin(ctx, accountLoginInput(c, true)); !errors.Is(e, domain.ErrUnauthenticated) {
			t.Fatal("passwordless legacy account logged in")
		}
		if e = s.ReplaceLibraryAccess(ctx, admin, p.UserID, nil); e != nil {
			t.Fatal(e)
		}
		grants, e = s.GetLibraryAccess(ctx, admin, p.UserID)
		if e != nil || len(grants) != 0 {
			t.Fatal("empty ACL replacement failed")
		}
	})

	t.Run("audit_never_contains_credentials", func(t *testing.T) {
		var unsafe int
		if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE before_state::text LIKE '%argon2%' OR after_state::text LIKE '%argon2%' OR before_state::text LIKE '%passwordHash%' OR after_state::text LIKE '%passwordHash%' OR before_state::text LIKE '%'||$1||'%' OR after_state::text LIKE '%'||$1||'%'`, rootGrant.Token).Scan(&unsafe); e != nil || unsafe != 0 {
			t.Fatal("audit contains credential material")
		}
		var recorded int
		if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE actor_id IS NOT NULL AND actor_ip='127.0.0.1'::inet AND occurred_at<=now()`).Scan(&recorded); e != nil || recorded < 10 {
			t.Fatal("audit omitted actor/IP/time")
		}
	})

	t.Run("last_active_admin_survives_concurrent_self_demotion", func(t *testing.T) {
		input := accountInput(root.Name)
		if _, e := s.UpdateUser(ctx, admin, root.ID, input); !errors.Is(e, domain.ErrLastAdmin) {
			t.Fatal("last administrator demoted")
		}
		input.Admin = true
		input.Disabled = true
		if _, e := s.UpdateUser(ctx, admin, root.ID, input); !errors.Is(e, domain.ErrLastAdmin) {
			t.Fatal("last administrator disabled")
		}
		if e := s.DeleteUser(ctx, admin, root.ID); !errors.Is(e, domain.ErrLastAdmin) {
			t.Fatal("last administrator deleted")
		}
		secondInput := accountInput("SecondFinalAdmin")
		secondInput.Admin = true
		second, _, e := s.CreateUser(ctx, admin, secondInput, "final-admin")
		if e != nil {
			t.Fatal(e)
		}
		secondActor := accountActor(accountLogin(t, ctx, s, second.Name))
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, pair := range []struct {
			actor domain.Actor
			user  domain.User
		}{{admin, root}, {secondActor, second}} {
			wg.Add(1)
			go func(a domain.Actor, u domain.User) {
				defer wg.Done()
				_, e := s.UpdateUser(ctx, a, u.ID, accountInput(u.Name))
				errs <- e
			}(pair.actor, pair.user)
		}
		wg.Wait()
		close(errs)
		success, rejected := 0, 0
		for e := range errs {
			if e == nil {
				success++
			} else if errors.Is(e, domain.ErrLastAdmin) {
				rejected++
			} else {
				t.Fatalf("unexpected concurrent demotion error: %v", e)
			}
		}
		if success != 1 || rejected != 1 {
			t.Fatal("last-admin check was not serialized")
		}
		var count int
		if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE is_admin AND NOT disabled AND deleted_at IS NULL`).Scan(&count); e != nil || count != 1 {
			t.Fatal("last active administrator was lost")
		}
	})
}

func TestAccountMigrationRejectsCaseCollisionWithoutRenaming(t *testing.T) {
	ctx, s, dsn := accountTestStore(t)
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 16 {
		t.Fatal("baseline verification downgrade failed", version, dirty, e)
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 15 {
		t.Fatal("legacy baseline downgrade failed", version, dirty, e)
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 14 {
		t.Fatal("family scan downgrade failed", version, dirty, e)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 13 {
		t.Fatal("legacy verification downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 12 {
		t.Fatal("legacy ignore downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 11 {
		t.Fatal("ignore scan downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 10 {
		t.Fatal("verification downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 9 {
		t.Fatal("baseline comparison downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 8 {
		t.Fatal("manifest downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 7 {
		t.Fatal("rollback ignore intent schema", err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 6 {
		t.Fatal("rollback NFO worker schema", err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 5 {
		t.Fatal("rollback NFO cache schema", err)
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 4 {
		t.Fatal("downgrade probe requests schema")
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 3 {
		t.Fatal("downgrade probe cache schema")
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 2 {
		t.Fatal("downgrade jobs schema")
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 1 {
		t.Fatal("downgrade to legacy schema")
	}
	if e := s.Ready(ctx); e == nil {
		t.Fatal("legacy schema must fail current readiness")
	}
	if _, e := s.Pool.Exec(ctx, `INSERT INTO users(name) VALUES('ExistingName'),('existingname')`); e != nil {
		t.Fatal("prepare legacy case collision")
	}
	if _, _, e := Migrate(ctx, dsn, "up"); e == nil {
		t.Fatal("case collision should reject migration")
	}
	var names []string
	if e := s.Pool.QueryRow(ctx, `SELECT array_agg(name ORDER BY name) FROM users`).Scan(&names); e != nil || len(names) != 2 || names[0] != "ExistingName" || names[1] != "existingname" {
		t.Fatal("failed migration renamed or deleted users")
	}
}

func TestAccountMigrationRollbackKeepsDeletedAccountsDisabled(t *testing.T) {
	ctx, s, dsn := accountTestStore(t)
	deletedToken, err := s.Provision(ctx, "DeletedBeforeRollback", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	activeToken, err := s.Provision(ctx, "ActiveBeforeRollback", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE users SET deleted_at=now() WHERE name='DeletedBeforeRollback'`); err != nil {
		t.Fatal("prepare deleted account")
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 16 {
		t.Fatal("baseline verification downgrade failed", version, dirty, e)
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 15 {
		t.Fatal("legacy baseline downgrade failed", version, dirty, e)
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 14 {
		t.Fatal("family scan downgrade failed", version, dirty, e)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 13 {
		t.Fatal("legacy verification downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 12 {
		t.Fatal("legacy ignore downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 11 {
		t.Fatal("ignore scan downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 10 {
		t.Fatal("verification downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 9 {
		t.Fatal("baseline comparison downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 8 {
		t.Fatal("manifest downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 7 {
		t.Fatal("rollback ignore intent schema", err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 6 {
		t.Fatal("rollback NFO worker schema", err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 5 {
		t.Fatal("rollback NFO cache schema", err)
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 4 {
		t.Fatal("downgrade probe requests schema")
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 3 {
		t.Fatal("downgrade probe cache schema")
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 2 {
		t.Fatal("downgrade jobs schema")
	}
	if version, dirty, e := Migrate(ctx, dsn, "down"); e != nil || dirty || version != 1 {
		t.Fatal("downgrade account migration")
	}
	var deletedDisabled, activeDisabled bool
	if err = s.Pool.QueryRow(ctx, `SELECT disabled FROM users WHERE name='DeletedBeforeRollback'`).Scan(&deletedDisabled); err != nil || !deletedDisabled {
		t.Fatal("downgrade reactivated deleted account")
	}
	if err = s.Pool.QueryRow(ctx, `SELECT disabled FROM users WHERE name='ActiveBeforeRollback'`).Scan(&activeDisabled); err != nil || activeDisabled {
		t.Fatal("downgrade unexpectedly disabled active account")
	}
	if version, dirty, e := Migrate(ctx, dsn, "up"); e != nil || dirty || version != SchemaVersion {
		t.Fatal("upgrade after fail-closed rollback")
	}
	if _, err = s.Authenticate(ctx, deletedToken); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("re-upgrade restored access for deleted account")
	}
	if _, err = s.Authenticate(ctx, activeToken); err != nil {
		t.Fatal("active legacy session lost on migration round trip")
	}
}
