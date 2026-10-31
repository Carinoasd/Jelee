package runtime

import (
	"context"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/adapter/subtitleocr"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/ocrruntime"
)

// ocrLocator is the background subtitle OCR service (subtitleocr.Service).
type ocrLocator interface {
	Locate(ctx context.Context, sourceID string, source domain.ProbeSource, index int) (subtitleocr.Item, error)
	LocateSidecar(ctx context.Context, request subtitleocr.SidecarRequest) (subtitleocr.Item, error)
	Close() error
}

// OCRAvailable reports whether OCR-derived subtitles can be listed and
// delivered: OCR enabled, the Tesseract runtime verified and the service
// running. The HTTP and compatibility layers list derived tracks only then.
func (m *matroskaService) OCRAvailable() bool { return m.Available() && m.ocr != nil }

// resolveOCR returns the cached SRT derived from a bitmap subtitle of an
// authorized Matroska source, or answers like a missing item while the
// source waits in the OCR queue (which this lookup joins) or has no text.
// The original bitmap track is never touched.
func (m *matroskaService) resolveOCR(ctx context.Context, sourceID string, source media.Source, index int) (media.Source, error) {
	if !m.OCRAvailable() {
		return media.Source{}, media.ErrNotFound
	}
	item, err := m.ocr.Locate(ctx, sourceID, domain.ProbeSource{RootPath: source.Root, RelativePath: source.RelativePath}, index)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return media.Source{}, ctx.Err()
	default:
		// Pending, nothing recognized, or not a bitmap track: all missing.
		return media.Source{}, media.ErrNotFound
	}
	// The derived file counts as playback of its authorized source.
	return media.Source{Root: m.ocrRoot, RelativePath: item.RelativePath, DeviceID: source.DeviceID, Limits: source.Limits, ShareStreams: source.ShareStreams}, nil
}

var _ media.SidecarOCRResolver = (*matroskaService)(nil)

// ResolveSidecarOCR returns the cached SRT derived from an external bitmap
// subtitle (G15.6): a ".sup" file, or a VobSub ".idx" read together with
// its paired ".sub". Both files are authorized exactly as for their own
// direct delivery first; the lookup then queues the track when there is no
// result yet, which answers like a missing item, as does every other
// external track. The external files are only read.
func (m *matroskaService) ResolveSidecarOCR(ctx context.Context, principal access.Principal, sourceID, trackID string) (media.Source, error) {
	if m == nil || m.tracks == nil {
		return media.Source{}, media.ErrNotFound
	}
	track, err := m.tracks.ResolveTrack(ctx, principal, sourceID, media.TrackSubtitle, trackID)
	if err != nil {
		return media.Source{}, err
	}
	if !m.OCRAvailable() {
		return media.Source{}, media.ErrNotFound
	}
	request := subtitleocr.SidecarRequest{SourceID: sourceID, TrackID: trackID, Language: track.Language,
		Files: []domain.ProbeSource{{RootPath: track.Root, RelativePath: track.RelativePath}}}
	switch format := strings.ToLower(strings.TrimPrefix(path.Ext(track.RelativePath), ".")); {
	case !domain.ExternalOCRSource(domain.SidecarKindSubtitle, format, track.PairedTrackID):
		return media.Source{}, media.ErrNotFound
	case format == "sup":
		request.Format = subtitleocr.SidecarPGS
	default:
		data, err := m.tracks.ResolveTrack(ctx, principal, sourceID, media.TrackSubtitle, track.PairedTrackID)
		if err != nil {
			return media.Source{}, err
		}
		if data.SubtitleKind == domain.SidecarSubtitleMicroDVD {
			return media.Source{}, media.ErrNotFound
		}
		request.Format = subtitleocr.SidecarVobSub
		request.Files = append(request.Files, domain.ProbeSource{RootPath: data.Root, RelativePath: data.RelativePath})
	}
	item, err := m.ocr.LocateSidecar(ctx, request)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return media.Source{}, ctx.Err()
	default:
		return media.Source{}, media.ErrNotFound
	}
	// The derived file counts as playback of its authorized track.
	return media.Source{Root: m.ocrRoot, RelativePath: item.RelativePath, DeviceID: track.DeviceID, Limits: track.Limits, ShareStreams: track.ShareStreams}, nil
}

// newOCRService registers the isolated Tesseract runner and starts the
// background OCR service over the verified Matroska extractor. A missing
// or altered runtime, an unusable cache root or invalid settings leave OCR
// off with one warning; extraction itself keeps working.
func newOCRService(ctx context.Context, c config.SubtitleOCRConfig, extractor *matroska.Extractor, directory string, budget app.WorkBudget, logger *slog.Logger) ocrLocator {
	reason := "runtime_unavailable"
	defer func() {
		if reason != "" {
			logger.Warn("subtitle OCR unavailable; bitmap subtitles are delivered without derived text tracks", "component", "subtitle_ocr", "code", "subtitle_ocr_"+reason)
		}
	}()
	runs, pictures, work := filepath.Join(directory, "ocr-runs"), filepath.Join(directory, "ocr-pictures"), filepath.Join(directory, "ocr-work")
	for _, path := range []string{runs, pictures, work} {
		if err := os.Mkdir(path, 0o700); err != nil {
			reason = "temporary_unavailable"
			return nil
		}
	}
	runner, err := ocrruntime.New(ctx, c.Concurrency, runs)
	if err != nil {
		return nil
	}
	identity, err := ocrruntime.Identity(c.Languages)
	if err != nil {
		return nil
	}
	recognizer, err := subtitleocr.NewRecognizer(runner, pictures)
	if err != nil {
		reason = "temporary_unavailable"
		return nil
	}
	// The service outlives this registration context; Close stops it.
	service, err := subtitleocr.New(extractor, recognizer, ocrServiceConfig(c, work, identity, budget, logger)) //nolint:contextcheck // background service with its own lifetime
	if err != nil {
		reason = "cache_unavailable"
		return nil
	}
	reason = ""
	return service
}

// ocrServiceConfig carries the validated settings into the service.
func ocrServiceConfig(c config.SubtitleOCRConfig, work, identity string, budget app.WorkBudget, logger *slog.Logger) subtitleocr.Config {
	return subtitleocr.Config{CacheRoot: c.CacheRoot, MaxCacheBytes: c.CacheMaxBytes, WorkRoot: work, Languages: c.Languages,
		PicturesPerMinute: c.PicturesPerMinute, Concurrency: c.Concurrency, QueueSize: c.QueueSize, Identity: identity, Budget: budget, Logger: logger}
}
