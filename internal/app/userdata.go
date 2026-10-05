package app

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// UserDataRepository exports and permanently deletes the data of one user
// (G07.7). Every method re-authorizes the actor in storage.
type UserDataRepository interface {
	// ExportUserData audits the export, then reads every section from one
	// snapshot: begin is called once before the first record, write once
	// per record, in a fixed section order.
	ExportUserData(ctx context.Context, actor domain.Actor, userID string, begin func(domain.UserDataExportHeader) error, write func(domain.UserDataRecord) error) error
	// PurgeUser deletes the user and every row that belongs to them in one
	// transaction. A nil proof is the administrator path (another user, web
	// session); a proof is the user deleting their own account.
	PurgeUser(ctx context.Context, actor domain.Actor, userID string, proof *domain.UserPurgeProof) error
}

func (a *Accounts) userDataRepository() (UserDataRepository, error) {
	if a.userData == nil {
		return nil, domain.ErrDatabase
	}
	return a.userData, nil
}

// ExportUserData streams the personal data of self or, as administrator, of
// any user, deleted or not.
func (a *Accounts) ExportUserData(ctx context.Context, actor domain.Actor, userID string, begin func(domain.UserDataExportHeader) error, write func(domain.UserDataRecord) error) error {
	if !validTarget(actor, userID) {
		return domain.ErrNotFound
	}
	if begin == nil || write == nil {
		return domain.ErrInvalid
	}
	repo, err := a.userDataRepository()
	if err != nil {
		return err
	}
	return repo.ExportUserData(ctx, actor, userID, begin, write)
}

// PurgeUser permanently deletes another user (administrator). Deleting the
// own account goes through PurgeSelf, which asks for the password.
func (a *Accounts) PurgeUser(ctx context.Context, actor domain.Actor, userID string) error {
	if !validTarget(actor, userID) {
		return domain.ErrNotFound
	}
	if strings.EqualFold(userID, actor.UserID) {
		return domain.ErrForbidden
	}
	repo, err := a.userDataRepository()
	if err != nil {
		return err
	}
	return repo.PurgeUser(ctx, actor, userID, nil)
}

// PurgeSelf permanently deletes the caller's account after the password
// and, when the account has a second factor, one current authenticator code
// or one unused recovery code verify. Wrong values are refused like the
// other self-service security changes.
func (a *Accounts) PurgeSelf(ctx context.Context, actor domain.Actor, password, code, recoveryCode string) error {
	if !validActor(actor) || !utf8.ValidString(password) || len(password) > 1024 || code != "" && recoveryCode != "" || len(code) > 64 || len(recoveryCode) > 64 {
		return domain.ErrInvalid
	}
	proof := domain.UserPurgeProof{}
	switch {
	case code != "":
		if _, ok := domain.NormalizeTOTPCode(code); !ok {
			return domain.ErrSecondFactorMismatch
		}
		proof.Verify = a.totpVerifier(code)
	case recoveryCode != "":
		if proof.Recovery = domain.RecoveryCodeDigest(actor.UserID, recoveryCode); proof.Recovery == nil {
			return domain.ErrSecondFactorMismatch
		}
	}
	repo, err := a.userDataRepository()
	if err != nil {
		return err
	}
	if proof.Expected, err = a.verifyOwnPassword(ctx, actor, password); err != nil {
		return err
	}
	return repo.PurgeUser(ctx, actor, actor.UserID, &proof)
}
