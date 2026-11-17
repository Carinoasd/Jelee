package runtime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/adapter/subtitleocr"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type matroskaSources struct {
	source media.Source
	err    error
	calls  int
}

func (m *matroskaSources) Resolve(context.Context, access.Principal, string) (media.Source, error) {
	m.calls++
	return m.source, m.err
}

type matroskaLocator struct {
	item  matroska.Item
	err   error
	calls []matroska.Kind
	seen  domain.ProbeSource
}

func (m *matroskaLocator) Locate(_ context.Context, _ string, source domain.ProbeSource, kind matroska.Kind, _ int) (matroska.Item, error) {
	m.calls = append(m.calls, kind)
	m.seen = source
	return m.item, m.err
}

func TestMatroskaServiceAuthorizesBeforeExtracting(t *testing.T) {
	sources := &matroskaSources{source: media.Source{Root: "/library", RelativePath: "Film.mkv", ContentType: "video/x-matroska", DeviceID: "device", Limits: domain.DeliveryLimits{}, ShareStreams: 2}}
	locator := &matroskaLocator{item: matroska.Item{RelativePath: "id/rev/t1.srt"}}
	service := &matroskaService{sources: sources, extractor: locator, cacheRoot: "/cache"}
	principal := access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientNative}
	got, err := service.ResolveExtracted(context.Background(), principal, "id", media.ExtractedSubtitle, 1)
	if err != nil || got.Root != "/cache" || got.RelativePath != "id/rev/t1.srt" || got.DeviceID != "device" || got.ShareStreams != 2 || locator.seen.RootPath != "/library" || locator.seen.RelativePath != "Film.mkv" {
		t.Fatalf("%v %+v", err, got)
	}
	for kind, want := range map[media.ExtractedKind]matroska.Kind{media.ExtractedAttachment: matroska.KindAttachment, media.ExtractedAttachmentStream: matroska.KindAttachmentStream,
		media.ExtractedVobSubIndex: matroska.KindVobSubIndex, media.ExtractedVobSubData: matroska.KindVobSubData} {
		locator.calls = nil
		if _, err := service.ResolveExtracted(context.Background(), principal, "id", kind, 1); err != nil || locator.calls[0] != want {
			t.Fatalf("%s mapped to %v", kind, locator.calls)
		}
	}
	// A refused lookup never reaches the extractor.
	locator.calls = nil
	sources.err = media.ErrNotFound
	if _, err := service.ResolveExtracted(context.Background(), principal, "id", media.ExtractedSubtitle, 1); err != media.ErrNotFound || len(locator.calls) != 0 {
		t.Fatal("unauthorized lookup reached the extractor")
	}
	sources.err = nil
	sources.source.ContentType = "video/mp4"
	if _, err := service.ResolveExtracted(context.Background(), principal, "id", media.ExtractedSubtitle, 1); err != media.ErrNotFound || len(locator.calls) != 0 {
		t.Fatal("non-Matroska source reached the extractor")
	}
	sources.source.ContentType = "video/webm"
	if _, err := service.ResolveExtracted(context.Background(), principal, "id", "other", 1); err != media.ErrNotFound {
		t.Fatal("unknown kind accepted")
	}
	for err, want := range map[error]error{matroska.ErrNotFound: media.ErrNotFound, matroska.ErrTooLarge: media.ErrNotFound, matroska.ErrBusy: media.ErrBusy, matroska.ErrUnavailable: media.ErrIO, matroska.ErrChanged: media.ErrIO} {
		locator.err = err
		if _, got := service.ResolveExtracted(context.Background(), principal, "id", media.ExtractedSubtitle, 1); !errors.Is(got, want) {
			t.Fatalf("%v mapped to %v", err, got)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	locator.err = context.Canceled
	if _, err := service.ResolveExtracted(cancelled, principal, "id", media.ExtractedSubtitle, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not reported")
	}
}

func TestMatroskaServiceWithoutRuntimeStaysWiredButUnavailable(t *testing.T) {
	var logs bytes.Buffer
	cfg := config.Config{Matroska: config.MatroskaConfig{EnableExtraction: true, CacheRoot: t.TempDir(), CacheMaxBytes: 1 << 30}}
	sources := &matroskaSources{source: media.Source{ContentType: "video/x-matroska"}}
	service := newMatroskaService(cfg, sources, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	defer func() {
		if err := service.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	if service.Available() || !strings.Contains(logs.String(), "matroska_runtime_unavailable") {
		t.Fatalf("available=%v logs=%s", service.Available(), logs.String())
	}
	if _, err := service.ResolveExtracted(context.Background(), access.Principal{Kind: access.ClientNative}, "id", media.ExtractedSubtitle, 1); err != media.ErrNotFound || sources.calls != 1 {
		t.Fatal("unavailable runtime must answer like a missing item after authorization")
	}
	var absent *matroskaService
	if absent.Available() || absent.Close() != nil {
		t.Fatal("nil service")
	}
}

type fakeOCR struct {
	item    subtitleocr.Item
	err     error
	calls   int
	closed  bool
	seen    domain.ProbeSource
	request subtitleocr.SidecarRequest
}

func (f *fakeOCR) Locate(_ context.Context, _ string, source domain.ProbeSource, _ int) (subtitleocr.Item, error) {
	f.calls++
	f.seen = source
	return f.item, f.err
}

func (f *fakeOCR) LocateSidecar(_ context.Context, request subtitleocr.SidecarRequest) (subtitleocr.Item, error) {
	f.calls++
	f.request = request
	return f.item, f.err
}

func (f *fakeOCR) Close() error { f.closed = true; return nil }

func TestMatroskaServiceResolvesOCRAfterAuthorization(t *testing.T) {
	sources := &matroskaSources{source: media.Source{Root: "/library", RelativePath: "Film.mkv", ContentType: "video/x-matroska", DeviceID: "device", ShareStreams: 3}}
	ocr := &fakeOCR{item: subtitleocr.Item{RelativePath: "id/rev/s5.srt"}}
	service := &matroskaService{sources: sources, extractor: &matroskaLocator{}, cacheRoot: "/cache", ocr: ocr, ocrRoot: "/ocr"}
	principal := access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientNative}
	got, err := service.ResolveExtracted(context.Background(), principal, "id", media.ExtractedOCRSubtitle, 5)
	if err != nil || got.Root != "/ocr" || got.RelativePath != "id/rev/s5.srt" || got.DeviceID != "device" || got.ShareStreams != 3 || ocr.seen.RootPath != "/library" || !service.OCRAvailable() {
		t.Fatalf("%v %+v", err, got)
	}
	for _, err := range []error{subtitleocr.ErrPending, subtitleocr.ErrNotFound, subtitleocr.ErrUnavailable} {
		ocr.err = err
		if _, got := service.ResolveExtracted(context.Background(), principal, "id", media.ExtractedOCRSubtitle, 5); got != media.ErrNotFound {
			t.Fatalf("%v mapped to %v", err, got)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ResolveExtracted(cancelled, principal, "id", media.ExtractedOCRSubtitle, 5); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not reported")
	}
	// Refused lookups and non-Matroska sources never reach OCR.
	calls := ocr.calls
	sources.err = media.ErrNotFound
	if _, err := service.ResolveExtracted(context.Background(), principal, "id", media.ExtractedOCRSubtitle, 5); err != media.ErrNotFound || ocr.calls != calls {
		t.Fatal("unauthorized lookup reached OCR")
	}
	sources.err = nil
	sources.source.ContentType = "video/mp4"
	if _, err := service.ResolveExtracted(context.Background(), principal, "id", media.ExtractedOCRSubtitle, 5); err != media.ErrNotFound || ocr.calls != calls {
		t.Fatal("non-Matroska source reached OCR")
	}
	// Without OCR the kind answers like a missing item.
	plain := &matroskaService{sources: &matroskaSources{source: media.Source{ContentType: "video/x-matroska"}}, extractor: &matroskaLocator{}}
	if plain.OCRAvailable() {
		t.Fatal("OCR available without a service")
	}
	if _, err := plain.ResolveExtracted(context.Background(), principal, "id", media.ExtractedOCRSubtitle, 5); err != media.ErrNotFound {
		t.Fatal("OCR kind without OCR")
	}
	if err := service.Close(); err != nil || !ocr.closed {
		t.Fatal("close did not stop OCR")
	}
}

func TestOCRServiceWithoutRuntimeLeavesOCROff(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	c := config.DefaultSubtitleOCRConfig()
	c.Enable, c.CacheRoot = true, t.TempDir()
	directory := t.TempDir()
	if service := newOCRService(context.Background(), c, nil, directory, nil, logger); service != nil || !strings.Contains(logs.String(), "subtitle_ocr_runtime_unavailable") {
		t.Fatalf("service %v logs %s", service, logs.String())
	}
	// The scratch subdirectories exist already: a second registration in
	// the same directory is refused before touching the runtime.
	logs.Reset()
	if service := newOCRService(context.Background(), c, nil, directory, nil, logger); service != nil || !strings.Contains(logs.String(), "subtitle_ocr_temporary_unavailable") {
		t.Fatalf("logs %s", logs.String())
	}
}

// sidecarTracks is the authorized external track lookup: a .sup, a VobSub
// .idx paired with its .sub, an unpaired .idx and a text subtitle.
type sidecarTracks struct {
	tracks map[string]media.Source
	calls  []string
	deny   string
}

func (s *sidecarTracks) ResolveTrack(_ context.Context, _ access.Principal, _ string, kind media.TrackKind, trackID string) (media.Source, error) {
	s.calls = append(s.calls, trackID)
	source, ok := s.tracks[trackID]
	if !ok || kind != media.TrackSubtitle || trackID == s.deny {
		return media.Source{}, media.ErrNotFound
	}
	return source, nil
}

func TestMatroskaServiceResolvesSidecarOCRAfterAuthorization(t *testing.T) {
	const sup, idx, sub, lone, srt = "sup", "idx", "sub", "lone", "srt"
	tracks := &sidecarTracks{tracks: map[string]media.Source{
		sup:  {Root: "/library", RelativePath: "Film.chi.SUP", Language: "chi", DeviceID: "device", ShareStreams: 4},
		idx:  {Root: "/library", RelativePath: "Film.eng.idx", Language: "eng", PairedTrackID: sub},
		sub:  {Root: "/library", RelativePath: "Film.eng.sub", SubtitleKind: domain.SidecarSubtitleVobSub},
		lone: {Root: "/library", RelativePath: "Other.idx"},
		srt:  {Root: "/library", RelativePath: "Film.srt"},
	}}
	ocr := &fakeOCR{item: subtitleocr.Item{RelativePath: "track/rev/s0.srt"}}
	service := &matroskaService{sources: &matroskaSources{}, tracks: tracks, extractor: &matroskaLocator{}, cacheRoot: "/cache", ocr: ocr, ocrRoot: "/ocr"}
	principal := access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientNative}
	got, err := service.ResolveSidecarOCR(context.Background(), principal, "source", sup)
	if err != nil || got.Root != "/ocr" || got.RelativePath != "track/rev/s0.srt" || got.DeviceID != "device" || got.ShareStreams != 4 {
		t.Fatalf("%v %+v", err, got)
	}
	if r := ocr.request; r.Format != subtitleocr.SidecarPGS || r.Language != "chi" || r.SourceID != "source" || r.TrackID != sup || len(r.Files) != 1 || r.Files[0].RelativePath != "Film.chi.SUP" {
		t.Fatalf("request %+v", r)
	}
	// The .sub half is authorized on its own before OCR reads it.
	tracks.calls = nil
	if _, err := service.ResolveSidecarOCR(context.Background(), principal, "source", idx); err != nil || !slices.Equal(tracks.calls, []string{idx, sub}) {
		t.Fatalf("%v %v", err, tracks.calls)
	}
	if r := ocr.request; r.Format != subtitleocr.SidecarVobSub || len(r.Files) != 2 || r.Files[1].RelativePath != "Film.eng.sub" {
		t.Fatalf("request %+v", r)
	}
	calls := ocr.calls
	tracks.deny = sub
	if _, err := service.ResolveSidecarOCR(context.Background(), principal, "source", idx); err != media.ErrNotFound || ocr.calls != calls {
		t.Fatal("an unauthorized .sub reached OCR")
	}
	tracks.deny = ""
	for _, id := range []string{lone, srt, "missing"} {
		if _, err := service.ResolveSidecarOCR(context.Background(), principal, "source", id); err != media.ErrNotFound || ocr.calls != calls {
			t.Fatalf("%s reached OCR", id)
		}
	}
	for _, err := range []error{subtitleocr.ErrPending, subtitleocr.ErrNotFound} {
		ocr.err = err
		if _, got := service.ResolveSidecarOCR(context.Background(), principal, "source", sup); got != media.ErrNotFound {
			t.Fatalf("%v mapped to %v", err, got)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ResolveSidecarOCR(cancelled, principal, "source", sup); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not reported")
	}
	// Without OCR, or without the track lookup, nothing is derived; the
	// track is still authorized first.
	tracks.calls = nil
	plain := &matroskaService{tracks: tracks, extractor: &matroskaLocator{}}
	if _, err := plain.ResolveSidecarOCR(context.Background(), principal, "source", sup); err != media.ErrNotFound || len(tracks.calls) != 1 {
		t.Fatal("sidecar OCR without OCR")
	}
	if _, err := (&matroskaService{ocr: ocr}).ResolveSidecarOCR(context.Background(), principal, "source", sup); err != media.ErrNotFound {
		t.Fatal("sidecar OCR without the track lookup")
	}
}

func TestOCRServiceConfigCarriesEverySetting(t *testing.T) {
	c := config.SubtitleOCRConfig{Enable: true, CacheRoot: "/ocr", CacheMaxBytes: 1 << 30, Languages: []string{"chi_tra", "eng"}, PicturesPerMinute: 90, Concurrency: 3, QueueSize: 7}
	logger := slog.New(slog.DiscardHandler)
	got := ocrServiceConfig(c, "/work", "identity", nil, logger)
	if got.CacheRoot != "/ocr" || got.MaxCacheBytes != 1<<30 || got.WorkRoot != "/work" || !slices.Equal(got.Languages, c.Languages) || got.PicturesPerMinute != 90 ||
		got.Concurrency != 3 || got.QueueSize != 7 || got.Identity != "identity" || got.Logger != logger {
		t.Fatalf("%+v", got)
	}
}
