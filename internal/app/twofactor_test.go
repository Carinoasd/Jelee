package app

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// twoFactorBoxFake "seals" by prefixing the context, so a value opens only
// under the context it was sealed with.
type twoFactorBoxFake struct{ contexts []string }

func (b *twoFactorBoxFake) Seal(context string, plaintext []byte) ([]byte, error) {
	b.contexts = append(b.contexts, context)
	out := append([]byte(strings.Repeat("#", 40-len(plaintext)%40)+context+"|"), plaintext...)
	return out, nil
}

func (b *twoFactorBoxFake) Open(context string, sealed []byte) ([]byte, error) {
	_, rest, ok := bytes.Cut(sealed, []byte(context+"|"))
	if !ok {
		return nil, errors.New("sealed for another context")
	}
	return append([]byte(nil), rest...), nil
}

// twoFactorRepoFake keeps one user's factor in memory and runs verifiers like
// the store: the matched step must exceed the last one.
type twoFactorRepoFake struct {
	accountRepositoryFake
	sealed    []byte
	lastStep  int64
	enabled   bool
	recovery  [][]byte
	expected  domain.Credentials
	appDigest []byte
	input     domain.SecondFactorInput
}

func (f *twoFactorRepoFake) TwoFactorStatus(context.Context, domain.Actor, string) (domain.TwoFactorStatus, error) {
	return domain.TwoFactorStatus{Enabled: f.enabled, Pending: f.sealed != nil && !f.enabled}, nil
}
func (f *twoFactorRepoFake) BeginTOTP(_ context.Context, _ domain.Actor, sealed []byte) (string, error) {
	f.sealed, f.lastStep = sealed, 0
	return "alice smith", nil
}
func (f *twoFactorRepoFake) run(verify domain.TOTPVerifier) error {
	step, err := verify(accountUserID, f.sealed, f.lastStep)
	if err != nil {
		return err
	}
	f.lastStep = step
	return nil
}
func (f *twoFactorRepoFake) ConfirmTOTP(_ context.Context, _ domain.Actor, verify domain.TOTPVerifier, recovery [][]byte) error {
	if err := f.run(verify); err != nil {
		return err
	}
	f.enabled, f.recovery = true, recovery
	return nil
}
func (f *twoFactorRepoFake) RegenerateRecoveryCodes(_ context.Context, _ domain.Actor, verify domain.TOTPVerifier, recovery [][]byte) error {
	if err := f.run(verify); err != nil {
		return err
	}
	f.recovery = recovery
	return nil
}
func (f *twoFactorRepoFake) DisableTOTP(_ context.Context, _ domain.Actor, expected domain.Credentials, verify domain.TOTPVerifier, recovery []byte) error {
	f.expected = expected
	if verify != nil {
		if err := f.run(verify); err != nil {
			return err
		}
	} else if !bytes.Equal(recovery, f.recovery[0]) {
		return domain.ErrSecondFactorMismatch
	}
	f.enabled = false
	return nil
}
func (f *twoFactorRepoFake) ResetTwoFactor(context.Context, domain.Actor, string) error { return nil }
func (f *twoFactorRepoFake) CommitSecondFactor(_ context.Context, in domain.SecondFactorInput) (domain.SessionGrant, error) {
	f.input = in
	if in.Verify != nil {
		if err := f.run(in.Verify); err != nil {
			return domain.SessionGrant{}, err
		}
	} else if !bytes.Equal(in.RecoveryDigest(accountUserID), f.recovery[1]) {
		return domain.SessionGrant{}, domain.ErrSecondFactorMismatch
	}
	return domain.SessionGrant{Token: "issued"}, nil
}
func (f *twoFactorRepoFake) CreateAppPassword(_ context.Context, _ domain.Actor, name string, digest []byte) (domain.AppPassword, error) {
	f.appDigest = digest
	return domain.AppPassword{ID: accountTargetID, Name: name}, nil
}
func (f *twoFactorRepoFake) ListAppPasswords(context.Context, domain.Actor, string) ([]domain.AppPassword, error) {
	return nil, nil
}
func (f *twoFactorRepoFake) DeleteAppPassword(context.Context, domain.Actor, string, string) error {
	return nil
}

func TestTwoFactorNeedsStorageAndKey(t *testing.T) {
	ctx := context.Background()
	plain := accountService(t, accountRepositoryFake{}, accountPasswordFake{})
	if _, err := plain.BeginTwoFactor(ctx, accountTestActor()); !errors.Is(err, domain.ErrSecondFactorUnavailable) {
		t.Fatalf("without storage: %v", err)
	}
	if _, err := plain.CompleteLogin(ctx, strings.Repeat("a", 43), "123456", "", "192.0.2.1"); !errors.Is(err, domain.ErrSecondFactorUnavailable) {
		t.Fatalf("complete without storage: %v", err)
	}
	repo := &twoFactorRepoFake{}
	keyless, err := NewAccounts(repo, accountPasswordFake{}, accountTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = keyless.BeginTwoFactor(ctx, accountTestActor()); !errors.Is(err, domain.ErrSecondFactorUnavailable) {
		t.Fatalf("without key: %v", err)
	}
	if status, err := keyless.TwoFactor(ctx, accountTestActor(), accountUserID); err != nil || status.Available {
		t.Fatalf("keyless status: %+v %v", status, err)
	}
	// An enrolled account without the key: codes cannot be checked, which is
	// not counted as a wrong code.
	repo.sealed, repo.enabled = []byte("sealed"), true
	if _, err = keyless.CompleteLogin(ctx, strings.Repeat("a", 43), "123456", "", "192.0.2.1"); !errors.Is(err, domain.ErrSecondFactorUnavailable) {
		t.Fatalf("keyless code: %v", err)
	}
}

func TestTwoFactorEnrollmentAndLogin(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	box := &twoFactorBoxFake{}
	repo := &twoFactorRepoFake{}
	options := accountTestOptions()
	options.Box, options.Now = box, func() time.Time { return now }
	passwords := accountPasswordFake{verify: func(_ context.Context, password, _ string) (bool, error) { return password == "right password", nil }}
	repo.credentialsFor = func(context.Context, domain.Actor, string) (domain.Credentials, error) {
		return domain.Credentials{UserID: accountUserID, PasswordHash: "$argon2id$v=19$hash", Version: 3}, nil
	}
	a, err := NewAccounts(repo, passwords, options)
	if err != nil {
		t.Fatal(err)
	}
	actor := accountTestActor()

	enrollment, err := a.BeginTwoFactor(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	if err != nil || len(key) != domain.TOTPSecretBytes || !strings.HasPrefix(enrollment.URI, "otpauth://totp/Jelee:alice%20smith?secret="+enrollment.Secret) {
		t.Fatalf("enrollment %+v %v", enrollment, err)
	}
	if len(box.contexts) != 1 || box.contexts[0] != domain.TOTPSealContext(accountUserID) || bytes.Contains(repo.sealed, []byte(enrollment.Secret)) {
		t.Fatalf("sealing %v", box.contexts)
	}
	step := domain.TOTPStep(now)
	if _, err = a.ConfirmTwoFactor(ctx, actor, domain.TOTPCode(key, step+5)); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("wrong confirmation code: %v", err)
	}
	if _, err = a.ConfirmTwoFactor(ctx, actor, "12a456"); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("malformed confirmation code: %v", err)
	}
	codes, err := a.ConfirmTwoFactor(ctx, actor, domain.TOTPCode(key, step))
	if err != nil || len(codes.Codes) != domain.RecoveryCodeCount || len(repo.recovery) != domain.RecoveryCodeCount || repo.lastStep != step {
		t.Fatalf("confirm %+v %v", codes, err)
	}
	seen := map[string]bool{}
	for i, code := range codes.Codes {
		if seen[code] || !bytes.Equal(repo.recovery[i], domain.RecoveryCodeDigest(accountUserID, code)) {
			t.Fatalf("recovery code %d", i)
		}
		seen[code] = true
	}

	// The same step cannot complete a login; the next one can, once.
	challenge := strings.Repeat("c", 43)
	if _, err = a.CompleteLogin(ctx, challenge, domain.TOTPCode(key, step), "", "192.0.2.1"); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("replayed code: %v", err)
	}
	grant, err := a.CompleteLogin(ctx, challenge, domain.TOTPCode(key, step+1), "", "192.0.2.1")
	if err != nil || grant.Token != "issued" || repo.lastStep != step+1 || repo.input.MaxSessions != options.MaxSessions || repo.input.LockAfter != options.LockAfter || repo.input.IP != "192.0.2.1" {
		t.Fatalf("login %+v %v %+v", grant, err, repo.input)
	}
	if _, err = a.CompleteLogin(ctx, challenge, domain.TOTPCode(key, step+1), "", "192.0.2.1"); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("replayed future step: %v", err)
	}
	// The clock moves on: two steps later the window is step+1..step+3.
	now = now.Add(2 * domain.TOTPPeriod)
	if _, err = a.CompleteLogin(ctx, challenge, domain.TOTPCode(key, step+3), "", "192.0.2.1"); err != nil {
		t.Fatalf("future step within skew: %v", err)
	}
	if _, err = a.CompleteLogin(ctx, challenge, "", strings.ToUpper(codes.Codes[1]), "192.0.2.1"); err != nil {
		t.Fatalf("recovery login: %v", err)
	}
	for _, bad := range []struct{ challenge, code, recovery string }{{"short", "123456", ""}, {challenge, "", ""}, {challenge, "123456", codes.Codes[0]}} {
		if _, err = a.CompleteLogin(ctx, bad.challenge, bad.code, bad.recovery, "192.0.2.1"); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err = a.CompleteLogin(ctx, "short", "123456", "", ""); !errors.Is(err, domain.ErrChallengeInvalid) {
		t.Fatalf("malformed challenge: %v", err)
	}

	// Recovery codes are replaced only with a current code.
	if _, err = a.RegenerateRecoveryCodes(ctx, actor, domain.TOTPCode(key, step+3)); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("regenerate with used step: %v", err)
	}
	fresh, err := a.RegenerateRecoveryCodes(ctx, actor, domain.TOTPCode(key, domain.TOTPStep(now)+1+1))
	if err == nil || len(fresh.Codes) != 0 {
		t.Fatalf("regenerate beyond skew: %+v %v", fresh, err)
	}
	now = now.Add(3 * domain.TOTPPeriod)
	if fresh, err = a.RegenerateRecoveryCodes(ctx, actor, domain.TOTPCode(key, domain.TOTPStep(now))); err != nil || fresh.Codes[0] == codes.Codes[0] {
		t.Fatalf("regenerate: %v", err)
	}

	// Disabling needs the password first, then exactly one factor.
	if err = a.DisableTwoFactor(ctx, actor, "wrong password", "", fresh.Codes[0]); !errors.Is(err, domain.ErrPasswordMismatch) {
		t.Fatalf("disable with wrong password: %v", err)
	}
	if err = a.DisableTwoFactor(ctx, actor, "right password", "123456", fresh.Codes[0]); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("disable with two factors: %v", err)
	}
	if err = a.DisableTwoFactor(ctx, actor, "right password", "", "not-a-code"); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("disable with malformed recovery code: %v", err)
	}
	if err = a.DisableTwoFactor(ctx, actor, "right password", "", fresh.Codes[0]); err != nil || repo.enabled || repo.expected.Version != 3 {
		t.Fatalf("disable: %v %+v", err, repo.expected)
	}
}

func TestAppPasswordsAndNativeLogin(t *testing.T) {
	ctx := context.Background()
	repo := &twoFactorRepoFake{}
	var inputs []domain.LoginInput
	repo.credentials = func(context.Context, string) (domain.Credentials, error) {
		return domain.Credentials{UserID: accountUserID, PasswordHash: "$argon2id$v=19$hash", Version: 1}, nil
	}
	repo.commitLogin = func(_ context.Context, in domain.LoginInput) (domain.SessionGrant, error) {
		inputs = append(inputs, in)
		return domain.SessionGrant{}, nil
	}
	passwords := accountPasswordFake{verify: func(context.Context, string, string) (bool, error) { return false, nil }}
	a, err := NewAccounts(repo, passwords, accountTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	created, err := a.CreateAppPassword(ctx, accountTestActor(), "Living room TV")
	if err != nil || !domain.LooksLikeAppPassword(created.Password) || created.AppPassword.Name != "Living room TV" || !bytes.Equal(repo.appDigest, domain.AppPasswordDigest(accountUserID, created.Password)) {
		t.Fatalf("create %+v %v", created, err)
	}
	if _, err = a.CreateAppPassword(ctx, accountTestActor(), " TV"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad name: %v", err)
	}
	client := domain.NativeClient{Name: "Player", DeviceID: "device-1"}
	if _, err = a.LoginNative(ctx, "alice", created.Password, client, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.LoginNative(ctx, "alice", "an ordinary password", client, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Login(ctx, "alice", created.Password, "Browser", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 3 || !bytes.Equal(inputs[0].AppPasswordDigest, repo.appDigest) || inputs[1].AppPasswordDigest != nil || inputs[2].AppPasswordDigest != nil {
		t.Fatal("application password digest reaches only native logins with the password form")
	}
}
