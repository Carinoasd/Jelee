package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// TwoFactorRepository stores the optional second factor and application
// passwords (G07.8). Every method re-authorizes the actor in storage;
// self-service changes require a web session there.
type TwoFactorRepository interface {
	TwoFactorStatus(ctx context.Context, actor domain.Actor, userID string) (domain.TwoFactorStatus, error)
	BeginTOTP(ctx context.Context, actor domain.Actor, sealed []byte) (string, error)
	ConfirmTOTP(ctx context.Context, actor domain.Actor, verify domain.TOTPVerifier, recovery [][]byte) error
	RegenerateRecoveryCodes(ctx context.Context, actor domain.Actor, verify domain.TOTPVerifier, recovery [][]byte) error
	DisableTOTP(ctx context.Context, actor domain.Actor, expected domain.Credentials, verify domain.TOTPVerifier, recovery []byte) error
	ResetTwoFactor(ctx context.Context, actor domain.Actor, userID string) error
	CommitSecondFactor(ctx context.Context, in domain.SecondFactorInput) (domain.SessionGrant, error)
	CreateAppPassword(ctx context.Context, actor domain.Actor, name string, digest []byte) (domain.AppPassword, error)
	ListAppPasswords(ctx context.Context, actor domain.Actor, userID string) ([]domain.AppPassword, error)
	DeleteAppPassword(ctx context.Context, actor domain.Actor, userID, id string) error
}

// SecretBox seals authenticator secrets with the server master key. The
// context binds a sealed value to its user and purpose.
type SecretBox interface {
	Seal(context string, plaintext []byte) ([]byte, error)
	Open(context string, sealed []byte) ([]byte, error)
}

func (a *Accounts) now() time.Time {
	if a.options.Now != nil {
		return a.options.Now()
	}
	return time.Now()
}

func (a *Accounts) twoFactorRepository() (TwoFactorRepository, error) {
	if a.twoFactor == nil {
		return nil, domain.ErrSecondFactorUnavailable
	}
	return a.twoFactor, nil
}

// totpVerifier opens the user's sealed secret and matches code against the
// current time. Without a master key no code can be checked.
func (a *Accounts) totpVerifier(code string) domain.TOTPVerifier {
	return func(userID string, sealed []byte, lastStep int64) (int64, error) {
		if a.options.Box == nil {
			return 0, domain.ErrSecondFactorUnavailable
		}
		key, err := a.options.Box.Open(domain.TOTPSealContext(userID), sealed)
		if err != nil {
			// A secret sealed under another master key cannot be checked; that
			// is a configuration fault, not a wrong code.
			return 0, domain.ErrSecondFactorUnavailable
		}
		defer clear(key)
		step, ok := domain.MatchTOTP(key, code, a.now(), lastStep)
		if !ok {
			return 0, domain.ErrSecondFactorMismatch
		}
		return step, nil
	}
}

func randomCode(bytes int) (string, error) {
	random := make([]byte, bytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	return domain.FormatSecretCode(random), nil
}

func newRecoveryCodes(userID string) ([]string, [][]byte, error) {
	codes := make([]string, 0, domain.RecoveryCodeCount)
	digests := make([][]byte, 0, domain.RecoveryCodeCount)
	seen := map[string]bool{}
	for len(codes) < domain.RecoveryCodeCount {
		code, err := randomCode(domain.RecoveryCodeBytes)
		if err != nil {
			return nil, nil, err
		}
		if seen[code] {
			continue
		}
		seen[code] = true
		codes = append(codes, code)
		digests = append(digests, domain.RecoveryCodeDigest(userID, code))
	}
	return codes, digests, nil
}

// TwoFactor reads a user's second factor state (self or administrator).
func (a *Accounts) TwoFactor(ctx context.Context, actor domain.Actor, userID string) (domain.TwoFactorStatus, error) {
	if !validTarget(actor, userID) {
		return domain.TwoFactorStatus{}, domain.ErrNotFound
	}
	repo, err := a.twoFactorRepository()
	if err != nil {
		return domain.TwoFactorStatus{}, err
	}
	status, err := repo.TwoFactorStatus(ctx, actor, userID)
	status.Available = a.options.Box != nil
	return status, err
}

// BeginTwoFactor creates a pending 160-bit authenticator secret for the
// caller and returns it once, as text and as an otpauth URI.
func (a *Accounts) BeginTwoFactor(ctx context.Context, actor domain.Actor) (domain.TwoFactorEnrollment, error) {
	if !validActor(actor) {
		return domain.TwoFactorEnrollment{}, domain.ErrInvalid
	}
	repo, err := a.twoFactorRepository()
	if err != nil || a.options.Box == nil {
		return domain.TwoFactorEnrollment{}, domain.ErrSecondFactorUnavailable
	}
	key := make([]byte, domain.TOTPSecretBytes)
	defer clear(key)
	if _, err = rand.Read(key); err != nil {
		return domain.TwoFactorEnrollment{}, fmt.Errorf("generate authenticator secret: %w", err)
	}
	sealed, err := a.options.Box.Seal(domain.TOTPSealContext(actor.UserID), key)
	if err != nil {
		return domain.TwoFactorEnrollment{}, domain.ErrSecondFactorUnavailable
	}
	name, err := repo.BeginTOTP(ctx, actor, sealed)
	if err != nil {
		return domain.TwoFactorEnrollment{}, err
	}
	return domain.TwoFactorEnrollment{Secret: domain.EncodeTOTPSecret(key), URI: domain.TOTPURI(name, key)}, nil
}

// ConfirmTwoFactor enables the pending factor when code verifies and returns
// the recovery codes, which are never shown again.
func (a *Accounts) ConfirmTwoFactor(ctx context.Context, actor domain.Actor, code string) (domain.RecoveryCodes, error) {
	if !validActor(actor) {
		return domain.RecoveryCodes{}, domain.ErrInvalid
	}
	if _, ok := domain.NormalizeTOTPCode(code); !ok {
		return domain.RecoveryCodes{}, domain.ErrSecondFactorMismatch
	}
	repo, err := a.twoFactorRepository()
	if err != nil {
		return domain.RecoveryCodes{}, err
	}
	codes, digests, err := newRecoveryCodes(actor.UserID)
	if err != nil {
		return domain.RecoveryCodes{}, err
	}
	if err = repo.ConfirmTOTP(ctx, actor, a.totpVerifier(code), digests); err != nil {
		return domain.RecoveryCodes{}, err
	}
	return domain.RecoveryCodes{Codes: codes}, nil
}

// RegenerateRecoveryCodes replaces the caller's recovery codes after a
// current authenticator code verifies.
func (a *Accounts) RegenerateRecoveryCodes(ctx context.Context, actor domain.Actor, code string) (domain.RecoveryCodes, error) {
	if !validActor(actor) {
		return domain.RecoveryCodes{}, domain.ErrInvalid
	}
	if _, ok := domain.NormalizeTOTPCode(code); !ok {
		return domain.RecoveryCodes{}, domain.ErrSecondFactorMismatch
	}
	repo, err := a.twoFactorRepository()
	if err != nil {
		return domain.RecoveryCodes{}, err
	}
	codes, digests, err := newRecoveryCodes(actor.UserID)
	if err != nil {
		return domain.RecoveryCodes{}, err
	}
	if err = repo.RegenerateRecoveryCodes(ctx, actor, a.totpVerifier(code), digests); err != nil {
		return domain.RecoveryCodes{}, err
	}
	return domain.RecoveryCodes{Codes: codes}, nil
}

// DisableTwoFactor removes the caller's factor. It needs the password and
// either a current code or an unused recovery code.
func (a *Accounts) DisableTwoFactor(ctx context.Context, actor domain.Actor, password, code, recoveryCode string) error {
	if !validActor(actor) || !utf8.ValidString(password) || len(password) > 1024 || (code == "") == (recoveryCode == "") {
		return domain.ErrInvalid
	}
	var verify domain.TOTPVerifier
	var recovery []byte
	if code != "" {
		if _, ok := domain.NormalizeTOTPCode(code); !ok {
			return domain.ErrSecondFactorMismatch
		}
		verify = a.totpVerifier(code)
	} else if recovery = domain.RecoveryCodeDigest(actor.UserID, recoveryCode); recovery == nil {
		return domain.ErrSecondFactorMismatch
	}
	repo, err := a.twoFactorRepository()
	if err != nil {
		return err
	}
	credentials, err := a.verifyOwnPassword(ctx, actor, password)
	if err != nil {
		return err
	}
	return repo.DisableTOTP(ctx, actor, credentials, verify, recovery)
}

// verifyOwnPassword checks the caller's current password and returns the
// credential snapshot it verified, like ChangePassword.
func (a *Accounts) verifyOwnPassword(ctx context.Context, actor domain.Actor, password string) (domain.Credentials, error) {
	credentials, err := a.repository.CredentialsFor(ctx, actor, actor.UserID)
	if err != nil {
		return domain.Credentials{}, fmt.Errorf("read own credentials: %w", err)
	}
	if credentials.PasswordHash == "" {
		if err = a.passwords.DummyVerify(ctx, password); err != nil {
			return domain.Credentials{}, err
		}
		return domain.Credentials{}, domain.ErrPasswordMismatch
	}
	matched, err := a.passwords.Verify(ctx, password, credentials.PasswordHash)
	if err != nil {
		if ctx.Err() != nil {
			return domain.Credentials{}, ctx.Err()
		}
		return domain.Credentials{}, domain.ErrPasswordMismatch
	}
	if !matched {
		return domain.Credentials{}, domain.ErrPasswordMismatch
	}
	return credentials, nil
}

// ResetTwoFactor removes a user's factor and recovery codes (administrator).
func (a *Accounts) ResetTwoFactor(ctx context.Context, actor domain.Actor, userID string) error {
	if !validTarget(actor, userID) {
		return domain.ErrNotFound
	}
	repo, err := a.twoFactorRepository()
	if err != nil {
		return err
	}
	return repo.ResetTwoFactor(ctx, actor, userID)
}

// CompleteLogin finishes a web login challenge with an authenticator code
// or a recovery code (exactly one) and issues the web session.
func (a *Accounts) CompleteLogin(ctx context.Context, challenge, code, recoveryCode, ip string) (domain.SessionGrant, error) {
	digest := domain.ChallengeDigest(challenge)
	if digest == nil {
		return domain.SessionGrant{}, domain.ErrChallengeInvalid
	}
	if (code == "") == (recoveryCode == "") || len(ip) > 45 || len(code) > 64 || len(recoveryCode) > 64 {
		return domain.SessionGrant{}, domain.ErrInvalid
	}
	repo, err := a.twoFactorRepository()
	if err != nil {
		return domain.SessionGrant{}, err
	}
	input := domain.SecondFactorInput{ChallengeDigest: digest, IP: ip, MaxSessions: a.options.MaxSessions, SessionTTL: a.options.SessionTTL, LockAfter: a.options.LockAfter, LockFor: a.options.LockFor}
	if code != "" {
		input.Verify = a.totpVerifier(code)
	} else {
		input.RecoveryDigest = func(userID string) []byte { return domain.RecoveryCodeDigest(userID, recoveryCode) }
	}
	grant, err := repo.CommitSecondFactor(ctx, input)
	if err != nil {
		return domain.SessionGrant{}, fmt.Errorf("complete second factor: %w", err)
	}
	return grant, nil
}

// CreateAppPassword creates an application password for the caller's
// devices that cannot ask for a second factor. The password is returned
// once.
func (a *Accounts) CreateAppPassword(ctx context.Context, actor domain.Actor, name string) (domain.NewAppPassword, error) {
	if !validActor(actor) || !domain.ValidAppPasswordName(name) {
		return domain.NewAppPassword{}, domain.ErrInvalid
	}
	repo, err := a.twoFactorRepository()
	if err != nil {
		return domain.NewAppPassword{}, err
	}
	password, err := randomCode(domain.AppPasswordBytes)
	if err != nil {
		return domain.NewAppPassword{}, err
	}
	record, err := repo.CreateAppPassword(ctx, actor, name, domain.AppPasswordDigest(actor.UserID, password))
	if err != nil {
		return domain.NewAppPassword{}, err
	}
	return domain.NewAppPassword{AppPassword: record, Password: password}, nil
}

// AppPasswords lists a user's application passwords (self or administrator).
func (a *Accounts) AppPasswords(ctx context.Context, actor domain.Actor, userID string) ([]domain.AppPassword, error) {
	if !validTarget(actor, userID) {
		return nil, domain.ErrNotFound
	}
	repo, err := a.twoFactorRepository()
	if err != nil {
		return nil, err
	}
	return repo.ListAppPasswords(ctx, actor, userID)
}

// DeleteAppPassword revokes an application password and its sessions.
func (a *Accounts) DeleteAppPassword(ctx context.Context, actor domain.Actor, userID, id string) error {
	if !validTarget(actor, userID) || !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	repo, err := a.twoFactorRepository()
	if err != nil {
		return err
	}
	return repo.DeleteAppPassword(ctx, actor, userID, id)
}
