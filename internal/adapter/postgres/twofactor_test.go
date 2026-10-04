package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// twoFactorSeal stands in for the sealing box: the key behind a fixed
// 20-byte header, so the stored value has the sealed length range.
func twoFactorSeal(key []byte) []byte { return append(make([]byte, 20), key...) }

// twoFactorVerifier matches code against the "sealed" key at now, like the
// application's verifier.
func twoFactorVerifier(t *testing.T, wantUser, code string, now time.Time) domain.TOTPVerifier {
	return func(userID string, sealed []byte, lastStep int64) (int64, error) {
		if userID != wantUser {
			t.Errorf("verifier called for %s", userID)
		}
		step, ok := domain.MatchTOTP(sealed[20:], code, now, lastStep)
		if !ok {
			return 0, domain.ErrSecondFactorMismatch
		}
		return step, nil
	}
}

// wrongTOTPCode is a well-formed code outside the accepted window at now.
func wrongTOTPCode(key []byte, now time.Time) string {
	step := domain.TOTPStep(now)
	for _, candidate := range []string{"000000", "111111", "222222", "333333"} {
		if candidate != domain.TOTPCode(key, step-1) && candidate != domain.TOTPCode(key, step) && candidate != domain.TOTPCode(key, step+1) {
			return candidate
		}
	}
	panic("no wrong code")
}

func twoFactorRecovery(t *testing.T, userID string) ([]string, [][]byte) {
	t.Helper()
	codes, digests := []string{}, [][]byte{}
	for range domain.RecoveryCodeCount {
		random := make([]byte, domain.RecoveryCodeBytes)
		if _, err := rand.Read(random); err != nil {
			t.Fatal(err)
		}
		code := domain.FormatSecretCode(random)
		codes, digests = append(codes, code), append(digests, domain.RecoveryCodeDigest(userID, code))
	}
	return codes, digests
}

func twoFactorSecondFactor(challenge string) domain.SecondFactorInput {
	return domain.SecondFactorInput{ChallengeDigest: domain.ChallengeDigest(challenge), IP: "127.0.0.1", MaxSessions: 100, SessionTTL: time.Hour, LockAfter: 3, LockFor: time.Minute}
}

// enrollTwoFactor enables a factor for actor at now and returns the key and
// the recovery codes.
func enrollTwoFactor(ctx context.Context, t *testing.T, s *Store, actor domain.Actor, now time.Time) ([]byte, []string) {
	t.Helper()
	key := make([]byte, domain.TOTPSecretBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginTOTP(ctx, actor, twoFactorSeal(key)); err != nil {
		t.Fatal("begin:", err)
	}
	codes, digests := twoFactorRecovery(t, actor.UserID)
	if err := s.ConfirmTOTP(ctx, actor, twoFactorVerifier(t, actor.UserID, domain.TOTPCode(key, domain.TOTPStep(now)), now), digests); err != nil {
		t.Fatal("confirm:", err)
	}
	return key, codes
}

func TestTwoFactorStorage(t *testing.T) {
	ctx, s, _ := accountTestStore(t)
	if _, err := s.BootstrapAdmin(ctx, accountInput("TFRoot")); err != nil {
		t.Fatal(err)
	}
	adminGrant := accountLogin(t, ctx, s, "tfroot")
	admin := accountActor(adminGrant)
	user := createAccount(t, ctx, s, admin, "tf-user")
	if _, err := s.SetNativeAccess(ctx, admin, user.ID, true); err != nil {
		t.Fatal(err)
	}
	creds := func() domain.Credentials {
		c, err := s.Credentials(ctx, "tf-user")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := s.Pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(query, err)
		}
		return n
	}
	alive := func(token string) bool {
		_, err := s.Authenticate(ctx, token)
		return err == nil
	}
	web := accountLogin(t, ctx, s, "tf-user")
	actor := accountActor(web)
	nativeGrant, err := s.CommitLogin(ctx, nativeLoginInput(creds(), domain.NativeClient{Name: "Player", DeviceID: "device-1"}))
	if err != nil {
		t.Fatal(err)
	}
	nativeActor := accountActor(nativeGrant)
	now := time.Unix(1800000000, 0)

	// Self-service needs a web session.
	key := make([]byte, domain.TOTPSecretBytes)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginTOTP(ctx, nativeActor, twoFactorSeal(key)); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("native session enrolled: %v", err)
	}
	if _, err = s.CreateAppPassword(ctx, nativeActor, "TV", domain.AppPasswordDigest(user.ID, domain.FormatSecretCode(make([]byte, 32)))); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("native session created an application password: %v", err)
	}
	if name, err := s.BeginTOTP(ctx, actor, twoFactorSeal(key)); err != nil || name != "tf-user" {
		t.Fatalf("begin: %q %v", name, err)
	}
	if status, err := s.TwoFactorStatus(ctx, actor, user.ID); err != nil || status.Enabled || !status.Pending {
		t.Fatalf("pending status: %+v %v", status, err)
	}
	if _, err = s.TwoFactorStatus(ctx, accountActor(accountLogin(t, ctx, s, "tfroot")), user.ID); err != nil {
		t.Fatalf("administrator status: %v", err)
	}
	// A pending factor changes no login.
	if g, err := s.CommitLogin(ctx, accountLoginInput(creds(), true)); err != nil || g.Challenge != nil {
		t.Fatalf("pending factor changed login: %+v %v", g, err)
	}
	_, digests := twoFactorRecovery(t, user.ID)
	step := domain.TOTPStep(now)
	if err = s.ConfirmTOTP(ctx, actor, twoFactorVerifier(t, user.ID, wrongTOTPCode(key, now), now), digests); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("wrong confirmation: %v", err)
	}
	codes, digests := twoFactorRecovery(t, user.ID)
	if err = s.ConfirmTOTP(ctx, actor, twoFactorVerifier(t, user.ID, domain.TOTPCode(key, step), now), digests); err != nil {
		t.Fatal(err)
	}
	// Other sessions were established with the password alone.
	if alive(nativeGrant.Token) || !alive(web.Token) {
		t.Fatal("enabling did not revoke exactly the other sessions")
	}
	if status, err := s.TwoFactorStatus(ctx, actor, user.ID); err != nil || !status.Enabled || status.Pending || status.RecoveryCodesRemaining != 10 || status.EnabledAt == nil {
		t.Fatalf("enabled status: %+v %v", status, err)
	}
	if _, err = s.BeginTOTP(ctx, actor, twoFactorSeal(key)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("re-enrolled while enabled: %v", err)
	}
	if count(`SELECT count(*) FROM audit_logs WHERE event='user.two_factor_enabled' AND target_id=$1::uuid AND category='security'`, user.ID) != 1 {
		t.Fatal("enabling not audited")
	}

	// Password logins: web gets a challenge, native the application
	// password refusal; neither issues a session or resets the counter.
	if _, err = s.CommitLogin(ctx, accountLoginInput(creds(), false)); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal(err)
	}
	sessions := count(`SELECT count(*) FROM sessions WHERE user_id=$1::uuid`, user.ID)
	g, err := s.CommitLogin(ctx, accountLoginInput(creds(), true))
	if err != nil || g.Challenge == nil || g.Token != "" || g.Session.ID != "" || len(g.Challenge.Token) != 43 {
		t.Fatalf("challenge: %+v %v", g, err)
	}
	challenge := g.Challenge.Token
	if _, err = s.CommitLogin(ctx, nativeLoginInput(creds(), domain.NativeClient{Name: "Player", DeviceID: "device-1"})); !errors.Is(err, domain.ErrSecondFactorRequired) {
		t.Fatalf("native password login: %v", err)
	}
	if count(`SELECT count(*) FROM sessions WHERE user_id=$1::uuid`, user.ID) != sessions || count(`SELECT failed_login FROM users WHERE id=$1::uuid`, user.ID) != 1 ||
		count(`SELECT count(*) FROM audit_logs WHERE event='login.app_password_required' AND target_id=$1::uuid`, user.ID) != 1 {
		t.Fatal("refused logins changed sessions, counters or audit")
	}

	// Second step: the confirmation step is spent; a wrong code counts; the
	// next step works once and spends the challenge.
	in := twoFactorSecondFactor(challenge)
	in.Verify = twoFactorVerifier(t, user.ID, domain.TOTPCode(key, step), now)
	if _, err = s.CommitSecondFactor(ctx, in); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("replayed confirmation code: %v", err)
	}
	if count(`SELECT failed_login FROM users WHERE id=$1::uuid`, user.ID) != 2 || count(`SELECT attempts FROM login_challenges WHERE token_digest=$1`, domain.ChallengeDigest(challenge)) != 1 ||
		count(`SELECT count(*) FROM audit_logs WHERE event='login.second_factor_failed' AND target_id=$1::uuid`, user.ID) != 1 {
		t.Fatal("wrong code not counted")
	}
	in.Verify = twoFactorVerifier(t, user.ID, domain.TOTPCode(key, step+1), now)
	second, err := s.CommitSecondFactor(ctx, in)
	if err != nil || second.Session.ClientKind != "web" || second.User.ID != user.ID || second.Session.DeviceName != "contract-test" || !alive(second.Token) {
		t.Fatalf("second step: %+v %v", second, err)
	}
	if count(`SELECT failed_login FROM users WHERE id=$1::uuid`, user.ID) != 0 {
		t.Fatal("completed login kept the failure count")
	}
	in.Verify = twoFactorVerifier(t, user.ID, domain.TOTPCode(key, step+1), now.Add(30*time.Second))
	if _, err = s.CommitSecondFactor(ctx, in); !errors.Is(err, domain.ErrChallengeInvalid) {
		t.Fatalf("challenge reused: %v", err)
	}
	newChallenge := func() string {
		t.Helper()
		g, err := s.CommitLogin(ctx, accountLoginInput(creds(), true))
		if err != nil || g.Challenge == nil {
			t.Fatalf("challenge: %+v %v", g, err)
		}
		return g.Challenge.Token
	}
	now = now.Add(time.Minute) // step+2
	for name, spoil := range map[string]string{
		"expired":   `UPDATE login_challenges SET expires_at=clock_timestamp()-interval '1 second' WHERE token_digest=$1`,
		"exhausted": `UPDATE login_challenges SET attempts=5 WHERE token_digest=$1`,
		"stale":     `UPDATE users SET auth_version=auth_version+1 WHERE id=(SELECT user_id FROM login_challenges WHERE token_digest=$1)`,
	} {
		c := newChallenge()
		if _, err = s.Pool.Exec(ctx, spoil, domain.ChallengeDigest(c)); err != nil {
			t.Fatal(err)
		}
		in = twoFactorSecondFactor(c)
		in.Verify = twoFactorVerifier(t, user.ID, domain.TOTPCode(key, domain.TOTPStep(now)), now)
		if _, err = s.CommitSecondFactor(ctx, in); !errors.Is(err, domain.ErrChallengeInvalid) {
			t.Fatalf("%s challenge: %v", name, err)
		}
	}
	if _, err = s.CommitSecondFactor(ctx, twoFactorSecondFactor(newChallenge())); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("completion without a factor accepted")
	}
	// At most five live challenges per user.
	for range 7 {
		newChallenge()
	}
	if count(`SELECT count(*) FROM login_challenges WHERE user_id=$1::uuid`, user.ID) != 5 {
		t.Fatal("challenges not bounded")
	}

	// Codes and passwords share the lock (LockAfter 3).
	c := newChallenge()
	in = twoFactorSecondFactor(c)
	in.Verify = twoFactorVerifier(t, user.ID, wrongTOTPCode(key, now), now)
	for range 2 {
		if _, err = s.CommitSecondFactor(ctx, in); !errors.Is(err, domain.ErrSecondFactorMismatch) {
			t.Fatal(err)
		}
	}
	if _, err = s.CommitLogin(ctx, accountLoginInput(creds(), false)); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal(err)
	}
	in.Verify = twoFactorVerifier(t, user.ID, domain.TOTPCode(key, domain.TOTPStep(now)), now)
	if _, err = s.CommitSecondFactor(ctx, in); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("locked account completed a login: %v", err)
	}
	if _, err = s.CommitLogin(ctx, accountLoginInput(creds(), true)); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("locked account got a challenge: %v", err)
	}
	if err = s.UnlockUser(ctx, admin, user.ID); err != nil {
		t.Fatal(err)
	}

	// Concurrent completions with the same code: exactly one wins.
	first, other := newChallenge(), newChallenge()
	now = now.Add(time.Minute)
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, c := range []string{first, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := twoFactorSecondFactor(c)
			in.Verify = twoFactorVerifier(t, user.ID, domain.TOTPCode(key, domain.TOTPStep(now)), now)
			_, results[i] = s.CommitSecondFactor(ctx, in)
		}()
	}
	wg.Wait()
	if (results[0] == nil) == (results[1] == nil) || !errors.Is(errors.Join(results...), domain.ErrSecondFactorMismatch) {
		t.Fatalf("concurrent replay: %v", results)
	}

	// Recovery codes work once each.
	c = newChallenge()
	in = twoFactorSecondFactor(c)
	in.RecoveryDigest = func(userID string) []byte { return domain.RecoveryCodeDigest(userID, codes[0]) }
	if _, err = s.CommitSecondFactor(ctx, in); err != nil {
		t.Fatalf("recovery login: %v", err)
	}
	in = twoFactorSecondFactor(newChallenge())
	in.RecoveryDigest = func(userID string) []byte { return domain.RecoveryCodeDigest(userID, codes[0]) }
	if _, err = s.CommitSecondFactor(ctx, in); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("recovery code reused: %v", err)
	}
	in.RecoveryDigest = func(string) []byte { return nil }
	if _, err = s.CommitSecondFactor(ctx, in); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("malformed recovery code: %v", err)
	}
	if status, _ := s.TwoFactorStatus(ctx, actor, user.ID); status.RecoveryCodesRemaining != 9 ||
		count(`SELECT count(*) FROM audit_logs WHERE event='login.recovery_code_used' AND target_id=$1::uuid`, user.ID) != 1 {
		t.Fatalf("recovery bookkeeping: %+v", status)
	}
	if err = s.UnlockUser(ctx, admin, user.ID); err != nil {
		t.Fatal(err)
	}

	// Application passwords: native logins without a code, revocable with
	// every session they issued, kept across rotation.
	password := domain.FormatSecretCode(make([]byte, domain.AppPasswordBytes))
	record, err := s.CreateAppPassword(ctx, actor, "Living room TV", domain.AppPasswordDigest(user.ID, password))
	if err != nil || record.Name != "Living room TV" || record.LastUsedAt != nil {
		t.Fatalf("create: %+v %v", record, err)
	}
	native := nativeLoginInput(creds(), domain.NativeClient{Name: "Player", DeviceID: "device-2"})
	native.PasswordOK, native.AppPasswordDigest = false, domain.AppPasswordDigest(user.ID, password)
	appGrant, err := s.CommitLogin(ctx, native)
	if err != nil || appGrant.Session.ClientKind != "native" {
		t.Fatalf("application password login: %+v %v", appGrant, err)
	}
	if count(`SELECT count(*) FROM audit_logs WHERE event='login.app_password_used' AND target_id=$1::uuid`, user.ID) != 1 {
		t.Fatal("application password use not audited")
	}
	// An application password never completes a web login.
	webApp := accountLoginInput(creds(), false)
	webApp.AppPasswordDigest = native.AppPasswordDigest
	if _, err = s.CommitLogin(ctx, webApp); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("web login with application password digest: %v", err)
	}
	rotated, err := s.RotateSession(ctx, accountActor(appGrant), "Renamed", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListAppPasswords(ctx, actor, user.ID)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == nil {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err = s.ListAppPasswords(ctx, accountActor(accountLogin(t, ctx, s, "tfroot")), user.ID); err != nil {
		t.Fatalf("administrator list: %v", err)
	}
	if err = s.DeleteAppPassword(ctx, actor, user.ID, record.ID); err != nil {
		t.Fatal(err)
	}
	if alive(rotated.Token) || alive(appGrant.Token) || !alive(web.Token) {
		t.Fatal("revocation missed the rotated session or hit others")
	}
	if _, err = s.CommitLogin(ctx, native); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("revoked application password: %v", err)
	}
	if err = s.DeleteAppPassword(ctx, actor, user.ID, record.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("double revoke: %v", err)
	}
	for i := range domain.AppPasswordLimit {
		random := make([]byte, domain.AppPasswordBytes)
		random[0] = byte(i)
		if _, err = rand.Read(random[1:]); err != nil {
			t.Fatal(err)
		}
		if _, err = s.CreateAppPassword(ctx, actor, "device", domain.AppPasswordDigest(user.ID, domain.FormatSecretCode(random))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.CreateAppPassword(ctx, actor, "one too many", domain.AppPasswordDigest(user.ID, password)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("limit: %v", err)
	}
	if err = s.UnlockUser(ctx, admin, user.ID); err != nil {
		t.Fatal(err)
	}

	// Disabling checks the verified credential snapshot and one factor.
	stale := creds()
	stale.Version--
	now = now.Add(time.Minute)
	if err = s.DisableTOTP(ctx, actor, stale, nil, domain.RecoveryCodeDigest(user.ID, codes[1])); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale credentials: %v", err)
	}
	if err = s.DisableTOTP(ctx, nativeActor, creds(), nil, domain.RecoveryCodeDigest(user.ID, codes[1])); err == nil {
		t.Fatal("revoked native session disabled the factor")
	}
	if err = s.DisableTOTP(ctx, actor, creds(), twoFactorVerifier(t, user.ID, domain.TOTPCode(key, domain.TOTPStep(now)), now), nil); err != nil {
		t.Fatal(err)
	}
	if status, _ := s.TwoFactorStatus(ctx, actor, user.ID); status.Enabled || status.Pending || count(`SELECT count(*) FROM user_recovery_codes WHERE user_id=$1::uuid`, user.ID) != 0 {
		t.Fatal("disable left factor data")
	}
	if g, err := s.CommitLogin(ctx, accountLoginInput(creds(), true)); err != nil || g.Challenge != nil {
		t.Fatalf("login after disable: %+v %v", g, err)
	}

	// Administrator reset, which an application password session cannot do.
	enrollTwoFactor(ctx, t, s, actor, now.Add(time.Hour))
	if err = s.ResetTwoFactor(ctx, actor, user.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("user reset: %v", err)
	}
	adminNative, err := s.Provision(ctx, "tf-native-admin", access.ClientNative, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, adminNative)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ResetTwoFactor(ctx, domain.Actor{UserID: p.UserID, SessionID: p.SessionID}, user.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("native administrator reset: %v", err)
	}
	admin = accountActor(accountLogin(t, ctx, s, "tfroot"))
	if err = s.ResetTwoFactor(ctx, admin, user.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ResetTwoFactor(ctx, admin, user.ID); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT count(*) FROM audit_logs WHERE event='user.two_factor_reset' AND target_id=$1::uuid`, user.ID) != 1 {
		t.Fatal("reset not audited exactly once")
	}
	// Operator recovery from the command line.
	enrollTwoFactor(ctx, t, s, admin, now.Add(2*time.Hour))
	if u, changed, err := s.ResetLocalTwoFactor(ctx, "TFROOT"); err != nil || !changed || u.ID != admin.UserID {
		t.Fatalf("local reset: %v", err)
	}
	if _, changed, err := s.ResetLocalTwoFactor(ctx, "tfroot"); err != nil || changed {
		t.Fatalf("second local reset: %v", err)
	}
}

func TestTwoFactorMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	actor := accountActor(grant)
	key := make([]byte, domain.TOTPSecretBytes)
	if _, err := f.s.BeginTOTP(f.ctx, actor, twoFactorSeal(key)); err != nil {
		t.Fatal(err)
	}
	password := domain.FormatSecretCode(make([]byte, domain.AppPasswordBytes))
	if _, err := f.s.CreateAppPassword(f.ctx, actor, "TV", domain.AppPasswordDigest(actor.UserID, password)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetNativeAccess(f.ctx, actor, actor.UserID, true); err != nil {
		t.Fatal(err)
	}
	c, err := f.s.Credentials(f.ctx, "job-admin")
	if err != nil {
		t.Fatal(err)
	}
	native := nativeLoginInput(c, domain.NativeClient{Name: "Player", DeviceID: "device-1"})
	native.PasswordOK, native.AppPasswordDigest = false, domain.AppPasswordDigest(actor.UserID, password)
	appGrant, err := f.s.CommitLogin(f.ctx, native)
	if err != nil {
		t.Fatal(err)
	}
	// Only a pending enrollment: the downgrade drops the tables; the session
	// an application password issued stays valid like any other.
	want := downgradeAboveMigration(t, f, "two_factor")
	// The previous migration number is not assumed: branches merge.
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version >= want {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('user_totp','user_recovery_codes','login_challenges','app_passwords')`) != 0 ||
		syncCount(t, f, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='sessions' AND column_name='app_password_id'`) != 0 {
		t.Fatal("downgrade left two-factor schema objects")
	}
	if syncCount(t, f, `SELECT count(*) FROM sessions WHERE id=$1::uuid AND revoked_at IS NULL`, appGrant.Session.ID) != 1 {
		t.Fatal("downgrade revoked sessions")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if status, err := f.s.TwoFactorStatus(f.ctx, actor, actor.UserID); err != nil || status.Enabled || status.Pending {
		t.Fatalf("status after round trip: %+v %v", status, err)
	}
	enrollTwoFactor(f.ctx, t, f.s, actor, time.Unix(1800000000, 0))
}

func TestTwoFactorMigrationRefusesEnabledDowngrade(t *testing.T) {
	f := newJobFixture(t)
	actor := accountActor(accountLogin(t, f.ctx, f.s, "job-admin"))
	enrollTwoFactor(f.ctx, t, f.s, actor, time.Unix(1800000000, 0))
	want := downgradeAboveMigration(t, f, "two_factor")
	dsn := f.s.Pool.Config().ConnString()
	if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
		t.Fatal("an enabled second factor was downgraded away")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "status"); err != nil || !dirty || version >= want {
		t.Fatalf("refused downgrade state: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM user_totp WHERE enabled_at IS NOT NULL`) != 1 {
		t.Fatal("refused downgrade lost the enrollment")
	}
}
