package app

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type identityRepositoryFake struct {
	jobRepositoryFake
	patches  []domain.ItemIdentityPatch
	decision domain.CatalogPendingDecision
	value    domain.ItemIdentity
}

func (f *identityRepositoryFake) ItemIdentity(context.Context, domain.Actor, string) (domain.ItemIdentity, error) {
	return f.value, nil
}
func (f *identityRepositoryFake) UpdateItemIdentity(_ context.Context, _ domain.Actor, _ string, _ int64, p []domain.ItemIdentityPatch) (domain.ItemIdentity, error) {
	f.patches = p
	*p[0].Locked = false
	return f.value, nil
}
func (f *identityRepositoryFake) AcceptCatalogPending(_ context.Context, _ domain.Actor, _, entry string, d domain.CatalogPendingDecision) (domain.CatalogPendingAcceptance, error) {
	f.decision = d
	*d.Title = "changed"
	return domain.CatalogPendingAcceptance{EntryID: entry}, nil
}
func (f *identityRepositoryFake) RejectCatalogPending(_ context.Context, _ domain.Actor, _, entry string) (domain.CatalogPendingEntry, error) {
	return domain.CatalogPendingEntry{ID: entry, Decision: domain.CatalogPendingRejected}, nil
}

func TestJobsItemIdentityCopiesAndValidates(t *testing.T) {
	ctx := context.Background()
	actor := domain.Actor{UserID: "11111111-1111-4111-8111-111111111111", SessionID: "22222222-2222-4222-8222-222222222222"}
	item := "33333333-3333-4333-8333-333333333333"
	n := 4
	fake := &identityRepositoryFake{value: domain.ItemIdentity{ItemID: item, Season: &n}}
	j := newJobService(t, fake)
	v, err := j.ItemIdentity(ctx, actor, item)
	if err != nil || v.Season == fake.value.Season || *v.Season != 4 {
		t.Fatal("read not copied", err)
	}
	locked := true
	patches := []domain.ItemIdentityPatch{{Field: "season", Locked: &locked}}
	if _, err = j.UpdateItemIdentity(ctx, actor, item, 1, patches); err != nil || !locked || fake.patches[0].Locked == &locked {
		t.Fatal("caller's lock pointer reached the repository", err)
	}
	if _, err = j.UpdateItemIdentity(ctx, actor, item, 1, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("empty correction", err)
	}
	title := "Mine"
	if a, err := j.AcceptCatalogPending(ctx, actor, item, item, domain.CatalogPendingDecision{Title: &title}); err != nil || a.EntryID != item || title != "Mine" {
		t.Fatal("decision not copied", err)
	}
	if _, err = j.AcceptCatalogPending(ctx, actor, item, "x", domain.CatalogPendingDecision{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid entry", err)
	}
	if e, err := j.RejectCatalogPending(ctx, actor, item, item); err != nil || e.Decision != domain.CatalogPendingRejected {
		t.Fatal("reject", err)
	}
	if _, err = j.RejectCatalogPending(ctx, domain.Actor{}, item, item); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("missing actor", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = j.ItemIdentity(cancelled, actor, item); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled read", err)
	}
	if _, err = j.UpdateItemIdentity(cancelled, actor, item, 1, patches); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled update", err)
	}
	if _, err = j.AcceptCatalogPending(cancelled, actor, item, item, domain.CatalogPendingDecision{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled accept", err)
	}
	if _, err = j.RejectCatalogPending(cancelled, actor, item, item); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled reject", err)
	}

	// A repository without the capability reports it unavailable.
	plain := newJobService(t, jobRepositoryFake{})
	if _, err = plain.ItemIdentity(ctx, actor, item); !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal(err)
	}
	if _, err = plain.UpdateItemIdentity(ctx, actor, item, 1, patches); !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal(err)
	}
	if _, err = plain.AcceptCatalogPending(ctx, actor, item, item, domain.CatalogPendingDecision{}); !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal(err)
	}
	if _, err = plain.RejectCatalogPending(ctx, actor, item, item); !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal(err)
	}
	var none *Jobs
	if _, err = none.ItemIdentity(ctx, actor, item); !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal(err)
	}
}

func TestPlanCatalogPendingAccept(t *testing.T) {
	text := func(s string) *string { return &s }
	number := func(n int) *int { return &n }
	// An absolute-numbered episode completed with season and episode.
	naruto := domain.CatalogPendingEntry{Kind: "episode", Title: "Naruto", Episode: number(12), EpisodeEnd: number(12), Absolute: 12}
	plan, err := PlanCatalogPendingAccept("Naruto/Naruto - 012.mkv", naruto, domain.CatalogPendingDecision{Season: number(1)})
	if err != nil || !plan.Auto || plan.Kind != "Episode" || plan.Season != 1 || plan.Episode != 12 || plan.EpisodeEnd != 12 || plan.SeriesTitle != "Naruto" || plan.SeriesDirectory != "Naruto" || plan.Title != "Naruto - 012" {
		t.Fatalf("episode plan %v %+v", err, plan)
	}
	plan, err = PlanCatalogPendingAccept("Naruto/Naruto - 012.mkv", naruto, domain.CatalogPendingDecision{Season: number(2), Episode: number(3), Title: text("The Exam")})
	if err != nil || plan.Episode != 3 || plan.EpisodeEnd != 3 || plan.Title != "The Exam" {
		t.Fatalf("corrected episode %v %+v", err, plan)
	}
	plan, err = PlanCatalogPendingAccept("Naruto/Naruto - 012.mkv", naruto, domain.CatalogPendingDecision{Season: number(2), Episode: number(3), EpisodeEnd: number(4), SeriesTitle: text("Other")})
	if err != nil || plan.EpisodeEnd != 4 || plan.SeriesTitle != "Other" || plan.SeriesDirectory != "" {
		t.Fatalf("series outside its folder %v %+v", err, plan)
	}
	if _, err = PlanCatalogPendingAccept("Naruto/Naruto - 012.mkv", naruto, domain.CatalogPendingDecision{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("episode without a season", err)
	}
	clip := domain.CatalogPendingEntry{Kind: "unknown", Title: "random clip"}
	if _, err = PlanCatalogPendingAccept("random clip.mkv", clip, domain.CatalogPendingDecision{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unknown kind accepted", err)
	}
	plan, err = PlanCatalogPendingAccept("random clip.mkv", clip, domain.CatalogPendingDecision{Kind: "movie", Year: number(2020)})
	if err != nil || plan.Kind != "Movie" || plan.Title != "random clip" || plan.Year != 2020 || plan.Directory != "." {
		t.Fatalf("movie plan %v %+v", err, plan)
	}
	if _, err = PlanCatalogPendingAccept("random clip.mkv", domain.CatalogPendingEntry{Kind: "movie"}, domain.CatalogPendingDecision{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("movie without a title", err)
	}
	if _, err = PlanCatalogPendingAccept("random clip.mkv", domain.CatalogPendingEntry{Kind: "movie", Title: "x"}, domain.CatalogPendingDecision{Season: number(1)}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("movie with numbers", err)
	}
	if _, err = PlanCatalogPendingAccept("notes.iso", clip, domain.CatalogPendingDecision{Kind: "movie"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unsupported container accepted", err)
	}
	if _, err = PlanCatalogPendingAccept("a.mkv", clip, domain.CatalogPendingDecision{Kind: "film"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid decision", err)
	}
}
