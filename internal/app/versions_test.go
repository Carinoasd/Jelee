package app

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// versionRepositoryStub records calls and answers with fixed values.
type versionRepositoryStub struct {
	calls []string
	set   domain.TrackPreferenceSet
	err   error
	last  domain.TrackPreference
}

func (s *versionRepositoryStub) record(name string) error {
	s.calls = append(s.calls, name)
	return s.err
}

func (s *versionRepositoryStub) SplitVersion(_ context.Context, _ domain.Actor, item string, in domain.SplitVersionInput) (domain.VersionOperation, error) {
	return domain.VersionOperation{Kind: domain.VersionOpSplit, ItemID: item, SourceIDs: []string{in.SourceID}}, s.record("split")
}
func (s *versionRepositoryStub) MergeItems(_ context.Context, _ domain.Actor, target, source string) (domain.VersionOperation, error) {
	return domain.VersionOperation{Kind: domain.VersionOpMerge, ItemID: target, OtherItemID: source}, s.record("merge")
}
func (s *versionRepositoryStub) SetPrimaryVersion(_ context.Context, _ domain.Actor, item, source string) (domain.VersionOperation, error) {
	return domain.VersionOperation{Kind: domain.VersionOpPrimary, ItemID: item}, s.record("primary:" + source)
}
func (s *versionRepositoryStub) RemoveVersionExclusion(_ context.Context, _ domain.Actor, item, _ string) (domain.VersionOperation, error) {
	return domain.VersionOperation{Kind: domain.VersionOpUnexclude, ItemID: item}, s.record("unexclude")
}
func (s *versionRepositoryStub) UndoVersionOperation(_ context.Context, _ domain.Actor, id string) (domain.VersionOperation, error) {
	return domain.VersionOperation{ID: id}, s.record("undo")
}
func (s *versionRepositoryStub) VersionOverview(_ context.Context, _ domain.Actor, item string) (domain.VersionOverview, error) {
	return domain.VersionOverview{ItemID: item}, s.record("overview")
}
func (s *versionRepositoryStub) TrackPreferences(_ context.Context, _ domain.Actor, item string) (domain.TrackPreferenceView, error) {
	return domain.TrackPreferenceView{ItemID: item}, s.record("tracks")
}
func (s *versionRepositoryStub) SetTrackPreference(_ context.Context, _ domain.Actor, item, _ string, p domain.TrackPreference) (domain.TrackPreferenceView, error) {
	s.last = p
	return domain.TrackPreferenceView{ItemID: item}, s.record("set-tracks")
}
func (s *versionRepositoryStub) UserTrackPreference(context.Context, domain.Actor) (*domain.TrackPreference, error) {
	return nil, s.record("user-tracks")
}
func (s *versionRepositoryStub) SetUserTrackPreference(_ context.Context, _ domain.Actor, p domain.TrackPreference) (*domain.TrackPreference, error) {
	s.last = p
	return &p, s.record("set-user-tracks")
}
func (s *versionRepositoryStub) EffectiveTrackPreferences(context.Context, domain.Actor, string) (domain.TrackPreferenceSet, error) {
	return s.set, s.record("effective")
}

func TestCatalogVersionsValidationAndWiring(t *testing.T) {
	ctx := context.Background()
	actor := domain.Actor{UserID: catalogUserID, SessionID: catalogUserID}
	unwired := NewCatalog(catalogRepositoryStub{})
	if _, err := unwired.VersionOverview(ctx, actor, catalogItemID); !errors.Is(err, domain.ErrDatabase) {
		t.Fatal("unwired", err)
	}
	if _, err := unwired.WithVersions(nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil repository accepted")
	}
	repo := &versionRepositoryStub{}
	catalog, err := unwired.WithVersions(repo)
	if err != nil || unwired.versions != nil {
		t.Fatal("wiring", err)
	}
	if _, err = catalog.SplitVersion(ctx, domain.Actor{}, catalogItemID, domain.SplitVersionInput{SourceID: catalogItemID}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous", err)
	}
	other := "abcdef12-abcd-abcd-abcd-abcdef123457"
	refused := map[string]error{
		"split bad item": func() error {
			_, e := catalog.SplitVersion(ctx, actor, "x", domain.SplitVersionInput{SourceID: other})
			return e
		}(),
		"split bad input": func() error {
			_, e := catalog.SplitVersion(ctx, actor, catalogItemID, domain.SplitVersionInput{SourceID: "x"})
			return e
		}(),
		"merge bad target": func() error { _, e := catalog.MergeItems(ctx, actor, "x", other); return e }(),
		"merge self":       func() error { _, e := catalog.MergeItems(ctx, actor, catalogItemID, catalogItemID); return e }(),
		"primary bad":      func() error { _, e := catalog.SetPrimaryVersion(ctx, actor, catalogItemID, "x"); return e }(),
		"primary item":     func() error { _, e := catalog.SetPrimaryVersion(ctx, actor, "x", ""); return e }(),
		"unexclude":        func() error { _, e := catalog.RemoveVersionExclusion(ctx, actor, catalogItemID, "x"); return e }(),
		"undo":             func() error { _, e := catalog.UndoVersionOperation(ctx, actor, "x"); return e }(),
		"overview":         func() error { _, e := catalog.VersionOverview(ctx, actor, "x"); return e }(),
		"tracks":           func() error { _, e := catalog.TrackPreferences(ctx, actor, "x"); return e }(),
		"set tracks item": func() error {
			_, e := catalog.SetTrackPreference(ctx, actor, "x", "", domain.TrackPreference{})
			return e
		}(),
		"set tracks src": func() error {
			_, e := catalog.SetTrackPreference(ctx, actor, catalogItemID, "x", domain.TrackPreference{})
			return e
		}(),
		"set item track": func() error {
			track := "e:1"
			_, e := catalog.SetTrackPreference(ctx, actor, catalogItemID, "", domain.TrackPreference{AudioTrack: &track})
			return e
		}(),
		"user track": func() error {
			track := "e:1"
			_, e := catalog.SetUserTrackPreference(ctx, actor, domain.TrackPreference{AudioTrack: &track})
			return e
		}(),
	}
	for name, err := range refused {
		if !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(repo.calls) != 0 {
		t.Fatal("invalid input reached storage", repo.calls)
	}
	language := "eng"
	calls := []func() error{
		func() error {
			_, e := catalog.SplitVersion(ctx, actor, catalogItemID, domain.SplitVersionInput{SourceID: other})
			return e
		},
		func() error { _, e := catalog.MergeItems(ctx, actor, catalogItemID, other); return e },
		func() error { _, e := catalog.SetPrimaryVersion(ctx, actor, catalogItemID, ""); return e },
		func() error { _, e := catalog.RemoveVersionExclusion(ctx, actor, catalogItemID, other); return e },
		func() error { _, e := catalog.UndoVersionOperation(ctx, actor, other); return e },
		func() error { _, e := catalog.VersionOverview(ctx, actor, catalogItemID); return e },
		func() error { _, e := catalog.TrackPreferences(ctx, actor, catalogItemID); return e },
		func() error {
			_, e := catalog.SetTrackPreference(ctx, actor, catalogItemID, other, domain.TrackPreference{AudioLanguage: &language})
			return e
		},
		func() error { _, e := catalog.UserTrackPreference(ctx, actor); return e },
		func() error {
			_, e := catalog.SetUserTrackPreference(ctx, actor, domain.TrackPreference{AudioLanguage: &language})
			return e
		},
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(repo.calls) != len(calls) || *repo.last.AudioLanguage != "en" {
		t.Fatalf("storage calls %v, normalized %v", repo.calls, *repo.last.AudioLanguage)
	}
}

func TestCatalogPlaybackSourcesApplyPreferencesAndMainVersion(t *testing.T) {
	ctx := context.Background()
	actor := domain.Actor{UserID: catalogUserID, SessionID: catalogUserID}
	low, high := "a0000000-0000-4000-8000-000000000000", "b0000000-0000-4000-8000-000000000000"
	sidecar := domain.SidecarTrackRecord{ID: "c0000000-0000-4000-8000-000000000000", Track: domain.SidecarTrack{Kind: "subtitle", Format: "srt", Language: "de"}}
	records := []domain.PlaybackSourceRecord{
		{ID: high, ContentType: "video/x-matroska", FileName: "Film.2160p.mkv"},
		{ID: low, ContentType: "video/mp4", FileName: "Film.720p.mp4", Primary: true, Sidecars: []domain.SidecarTrackRecord{sidecar}},
	}
	catalog, err := NewCatalog(catalogRepositoryStub{}).WithPlayback(playbackRepositoryFunc(func(context.Context, domain.Actor, string) ([]domain.PlaybackSourceRecord, error) {
		return records, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	sources, err := catalog.PlaybackSources(ctx, actor, catalogItemID)
	if err != nil || len(sources) != 2 || sources[0].ID != low || sources[0].DefaultTracks != nil {
		t.Fatal("main version first, no preferences without the repository", err)
	}
	mode, language := domain.SubtitleModeAlways, "de"
	repo := &versionRepositoryStub{set: domain.TrackPreferenceSet{Item: &domain.TrackPreference{SubtitleMode: &mode, SubtitleLanguage: &language}}}
	if catalog, err = catalog.WithVersions(repo); err != nil {
		t.Fatal(err)
	}
	sources, err = catalog.PlaybackSources(ctx, actor, catalogItemID)
	if err != nil || sources[0].DefaultTracks == nil || sources[0].DefaultTracks.Subtitle == nil || sources[0].DefaultTracks.Subtitle.ID != sidecar.ID ||
		sources[0].DefaultTracks.Basis != domain.TrackBasisItem || sources[1].DefaultTracks.Subtitle != nil {
		t.Fatalf("preferences not applied: %+v %v", sources[0].DefaultTracks, err)
	}
	repo.err = domain.ErrDatabase
	if _, err = catalog.PlaybackSources(ctx, actor, catalogItemID); !errors.Is(err, domain.ErrDatabase) {
		t.Fatal("preference failure hidden", err)
	}
}
