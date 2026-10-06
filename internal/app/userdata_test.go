package app

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type userDataRepoFake struct {
	accountRepositoryFake
	purged   []string
	proof    *domain.UserPurgeProof
	exported string
}

func (f *userDataRepoFake) ExportUserData(_ context.Context, _ domain.Actor, userID string, begin func(domain.UserDataExportHeader) error, write func(domain.UserDataRecord) error) error {
	f.exported = userID
	if err := begin(domain.UserDataExportHeader{UserID: userID}); err != nil {
		return err
	}
	return write(domain.UserDataRecord{Type: "account", Data: []byte(`{}`)})
}

func (f *userDataRepoFake) PurgeUser(_ context.Context, _ domain.Actor, userID string, proof *domain.UserPurgeProof) error {
	f.purged, f.proof = append(f.purged, userID), proof
	return nil
}

func TestUserDataExportAndPurgeValidation(t *testing.T) {
	const other = "22222222-2222-4222-8222-222222222222"
	snapshot := domain.Credentials{UserID: accountUserID, PasswordHash: "hash", Version: 3}
	repo := &userDataRepoFake{accountRepositoryFake: accountRepositoryFake{credentialsFor: func(context.Context, domain.Actor, string) (domain.Credentials, error) {
		return snapshot, nil
	}}}
	passwords := accountPasswordFake{verify: func(_ context.Context, password, _ string) (bool, error) { return password == "right", nil }}
	a := accountService(t, repo, passwords)
	ctx, actor := context.Background(), accountTestActor()
	noop := func(domain.UserDataExportHeader) error { return nil }
	write := func(domain.UserDataRecord) error { return nil }

	if err := a.ExportUserData(ctx, actor, "bad", noop, write); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("invalid target: %v", err)
	}
	if err := a.ExportUserData(ctx, actor, other, nil, write); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing begin: %v", err)
	}
	if err := a.ExportUserData(ctx, actor, other, noop, write); err != nil || repo.exported != other {
		t.Fatalf("export: %v %q", err, repo.exported)
	}

	if err := a.PurgeUser(ctx, actor, actor.UserID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("own account through the administrator path: %v", err)
	}
	if err := a.PurgeUser(ctx, actor, "bad"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("invalid target: %v", err)
	}
	if err := a.PurgeUser(ctx, actor, other); err != nil || repo.proof != nil || repo.purged[len(repo.purged)-1] != other {
		t.Fatalf("administrator purge: %v", err)
	}

	for name, run := range map[string]func() error{
		"code and recovery": func() error { return a.PurgeSelf(ctx, actor, "right", "123456", "abcd-efgh-ijkl-mnop") },
		"long password":     func() error { return a.PurgeSelf(ctx, actor, string(make([]byte, 1025)), "", "") },
		"invalid actor":     func() error { return a.PurgeSelf(ctx, domain.Actor{}, "right", "", "") },
	} {
		if err := run(); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := a.PurgeSelf(ctx, actor, "right", "12x", ""); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("malformed code: %v", err)
	}
	if err := a.PurgeSelf(ctx, actor, "right", "", "!"); !errors.Is(err, domain.ErrSecondFactorMismatch) {
		t.Fatalf("malformed recovery code: %v", err)
	}
	count := len(repo.purged)
	if err := a.PurgeSelf(ctx, actor, "wrong", "", ""); !errors.Is(err, domain.ErrPasswordMismatch) || len(repo.purged) != count {
		t.Fatalf("wrong password: %v", err)
	}
	if err := a.PurgeSelf(ctx, actor, "right", "123 456", ""); err != nil || repo.proof == nil || repo.proof.Verify == nil || repo.proof.Recovery != nil || repo.proof.Expected != snapshot {
		t.Fatalf("self purge with code: %v %+v", err, repo.proof)
	}
	if err := a.PurgeSelf(ctx, actor, "right", "", ""); err != nil || repo.proof.Verify != nil || repo.proof.Recovery != nil || repo.purged[len(repo.purged)-1] != actor.UserID {
		t.Fatalf("self purge: %v", err)
	}

	// A repository without data rights answers not ready.
	plain := accountService(t, accountRepositoryFake{}, passwords)
	if err := plain.PurgeUser(ctx, actor, other); !errors.Is(err, domain.ErrDatabase) {
		t.Fatalf("missing repository: %v", err)
	}
	if err := plain.ExportUserData(ctx, actor, other, noop, write); !errors.Is(err, domain.ErrDatabase) {
		t.Fatalf("missing repository: %v", err)
	}
	if err := plain.PurgeSelf(ctx, actor, "right", "", ""); !errors.Is(err, domain.ErrDatabase) {
		t.Fatalf("missing repository: %v", err)
	}
}
