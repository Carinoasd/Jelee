package app

import (
	"context"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type preferencesRepositoryFake struct {
	AccountRepository
	stored domain.UserPreferences
	calls  int
}

func (f *preferencesRepositoryFake) GetPreferences(context.Context, domain.Actor) (domain.UserPreferences, error) {
	f.calls++
	return f.stored, nil
}

func (f *preferencesRepositoryFake) SetPreferences(_ context.Context, _ domain.Actor, p domain.UserPreferences) (domain.UserPreferences, error) {
	f.calls++
	f.stored = p
	return p, nil
}

func TestAccountPreferencesValidateBeforeStorage(t *testing.T) {
	repo := &preferencesRepositoryFake{stored: domain.DefaultUserPreferences()}
	a := accountService(t, repo, accountPasswordFake{})
	ctx := context.Background()
	for _, bad := range []domain.UserPreferences{{}, {Theme: "auto", Density: domain.DensityCompact}, {Theme: domain.ThemeDark, Density: "tiny"}} {
		if _, err := a.SetPreferences(ctx, accountTestActor(), bad); err != domain.ErrInvalid {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if repo.calls != 0 {
		t.Fatal("invalid preferences reached storage")
	}
	want := domain.UserPreferences{Theme: domain.ThemeDark, Density: domain.DensityCompact}
	if got, err := a.SetPreferences(ctx, accountTestActor(), want); err != nil || got != want {
		t.Fatalf("set: %+v %v", got, err)
	}
	if got, err := a.Preferences(ctx, accountTestActor()); err != nil || got != want {
		t.Fatalf("get: %+v %v", got, err)
	}
}
