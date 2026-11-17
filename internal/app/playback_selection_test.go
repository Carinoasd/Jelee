package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// TestCatalogPlaybackChoiceOrdersByPreference: the version preference
// reorders before the main version, a declaration only excludes, and the
// selection explains the default (G20.2).
func TestCatalogPlaybackChoiceOrdersByPreference(t *testing.T) {
	ctx := context.Background()
	actor := domain.Actor{UserID: catalogUserID, SessionID: catalogUserID}
	sdr, hdr := "a0000000-0000-4000-8000-000000000000", "b0000000-0000-4000-8000-000000000000"
	records := []domain.PlaybackSourceRecord{
		{ID: hdr, ContentType: "video/x-matroska", FileName: "Film.2160p.HDR.mkv"},
		{ID: sdr, ContentType: "video/mp4", FileName: "Film.1080p.mp4", Primary: true},
	}
	catalog, err := NewCatalog(catalogRepositoryStub{}).WithPlayback(playbackRepositoryFunc(func(context.Context, domain.Actor, string) ([]domain.PlaybackSourceRecord, error) {
		return records, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	hdrRange := domain.VersionRangeHDR
	repo := &versionRepositoryStub{set: domain.TrackPreferenceSet{VersionPreference: domain.VersionPreference{DynamicRange: &hdrRange}}}
	if catalog, err = catalog.WithVersions(repo); err != nil {
		t.Fatal(err)
	}
	sources, selection, err := catalog.PlaybackChoice(ctx, actor, catalogItemID, nil)
	if err != nil || sources[0].ID != hdr || selection.DefaultSourceID != hdr || selection.Reason != domain.VersionReasonRange {
		t.Fatalf("HDR preference before the main version: %v %+v", err, selection)
	}
	if sources, err = catalog.PlaybackSources(ctx, actor, catalogItemID); err != nil || sources[0].ID != hdr {
		t.Fatal("PlaybackSources keeps the preference order", err)
	}
	sources, selection, err = catalog.PlaybackChoice(ctx, actor, catalogItemID, func(s domain.PlaybackSource) []string {
		if s.ID == hdr {
			return []string{domain.PlaybackReasonVideoCodec}
		}
		return nil
	})
	if err != nil || len(sources) != 2 || selection.DefaultSourceID != sdr || selection.Reason != domain.VersionReasonOnlyPlayable ||
		len(selection.Excluded) != 1 || selection.Excluded[0].SourceID != hdr {
		t.Fatalf("declaration excludes, never converts: %v %+v", err, selection)
	}
	// The check route: an mkv-only client cannot play the mp4, and an
	// unprobed source is never confirmed, so nothing is playable.
	decisions, selection, err := catalog.CheckPlaybackChoice(ctx, actor, catalogItemID, domain.ClientCapabilities{Containers: []string{"mkv"}})
	if err != nil || len(decisions) != 2 || selection.DefaultSourceID != "" || selection.Reason != domain.VersionReasonNonePlayable {
		t.Fatalf("check: %v %+v", err, selection)
	}
	if _, _, err = catalog.CheckPlaybackChoice(ctx, actor, catalogItemID, domain.ClientCapabilities{MaxBitrate: -1}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid declaration", err)
	}

	// Version preference API: validated before storage.
	height := 1000
	if _, err = catalog.SetVersionPreference(ctx, actor, domain.VersionPreference{MaxHeight: &height}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid height stored", err)
	}
	height = 2160
	if p, err := catalog.SetVersionPreference(ctx, actor, domain.VersionPreference{MaxHeight: &height}); err != nil || *p.MaxHeight != 2160 {
		t.Fatal(err)
	}
	if p, err := catalog.VersionPreference(ctx, actor); err != nil || *p.MaxHeight != 2160 {
		t.Fatal(err)
	}
	if _, err = NewCatalog(catalogRepositoryStub{}).VersionPreference(ctx, actor); !errors.Is(err, domain.ErrDatabase) {
		t.Fatal("unwired", err)
	}
}

type sessionTrackSources struct{}

func (sessionTrackSources) ListPlaybackSources(_ context.Context, _ domain.Actor, _ string) ([]domain.PlaybackSourceRecord, error) {
	str := func(v string) *string { return &v }
	micros := int64(time.Hour / time.Microsecond)
	return []domain.PlaybackSourceRecord{{ID: progressSource, ContentType: "video/x-matroska", FileName: "a.mkv", Metadata: &domain.MediaMetadata{
		Format: domain.MediaFormat{DurationMicros: &micros},
		Streams: []domain.MediaStream{
			{Index: 0, Kind: "video", Codec: str("h264")},
			{Index: 1, Kind: "audio", Codec: str("aac"), Language: str("eng")},
			{Index: 2, Kind: "audio", Codec: str("aac"), Language: str("jpn")},
			{Index: 3, Kind: "subtitle", Codec: str("subrip"), Language: str("chi")},
		}}}}, nil
}

// TestCatalogRecordsSessionTracks: a track change in a report is stored
// once per change for the version the session plays (G20.4); unchanged and
// unusable reports write nothing, and a failing store never fails the
// report.
func TestCatalogRecordsSessionTracks(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	progress, err := NewProgress(newFakeProgressRepo(), sessionTrackSources{}, ProgressOptions{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	repo := &versionRepositoryStub{}
	catalog, err := NewCatalog(catalogRepositoryStub{}).WithProgress(progress)
	if err != nil {
		t.Fatal(err)
	}
	if catalog, err = catalog.WithVersions(repo); err != nil {
		t.Fatal(err)
	}
	actor, item := progressActor(1), progressItem(1)
	japanese, off, chinese := 2, -1, 3
	start := report(domain.PlaybackReportStart, "play-1", item, 0)
	start.Tracks = &domain.PlaybackTrackReport{AudioIndex: &japanese}
	if err := catalog.ReportPlayback(ctx, actor, start); err != nil {
		t.Fatal(err)
	}
	progressReport := report(domain.PlaybackReportProgress, "play-1", "", time.Minute)
	progressReport.Tracks = &domain.PlaybackTrackReport{AudioIndex: &japanese}
	if err := catalog.ReportPlayback(ctx, actor, progressReport); err != nil {
		t.Fatal(err)
	}
	if len(repo.session) != 1 || repo.session[0].item != item || repo.session[0].playKey != "play-1" ||
		repo.session[0].tracks != (domain.SessionTracks{SourceID: progressSource, AudioTrack: "e:2"}) {
		t.Fatalf("first choice %+v", repo.session)
	}
	progressReport.Tracks = &domain.PlaybackTrackReport{AudioIndex: &japanese, SubtitleIndex: &chinese}
	if err := catalog.ReportPlayback(ctx, actor, progressReport); err != nil {
		t.Fatal(err)
	}
	// A failing store is retried with the next report.
	repo.err = domain.ErrDatabase
	progressReport.Tracks = &domain.PlaybackTrackReport{AudioIndex: &japanese, SubtitleIndex: &off}
	if err := catalog.ReportPlayback(ctx, actor, progressReport); err != nil {
		t.Fatal("store failure failed the report", err)
	}
	repo.err = nil
	stop := report(domain.PlaybackReportStop, "play-1", item, 2*time.Minute)
	stop.Tracks = progressReport.Tracks
	if err := catalog.ReportPlayback(ctx, actor, stop); err != nil {
		t.Fatal(err)
	}
	if len(repo.session) != 4 || repo.session[1].tracks.SubtitleTrack != "e:3" || repo.session[3].tracks.SubtitleTrack != domain.SubtitleTrackOff {
		t.Fatalf("changes %+v", repo.session)
	}
	// Nothing usable (an index the version does not have) and reports
	// without tracks write nothing.
	unknown := 9
	other := report(domain.PlaybackReportStart, "play-2", item, 0)
	other.Tracks = &domain.PlaybackTrackReport{AudioIndex: &unknown}
	if err := catalog.ReportPlayback(ctx, actor, other); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ReportPlayback(ctx, actor, report(domain.PlaybackReportProgress, "play-2", item, time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(repo.session) != 4 {
		t.Fatalf("unusable choice stored %+v", repo.session)
	}
	bad := report(domain.PlaybackReportProgress, "play-2", item, time.Minute)
	bad.Tracks = &domain.PlaybackTrackReport{SubtitleIndex: &unknown, AudioTrack: "nope"}
	if err := catalog.ReportPlayback(ctx, actor, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("malformed tracks accepted", err)
	}
}
