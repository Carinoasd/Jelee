package runtime

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/mkvruntime"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/MoYuanCN/Jelee/internal/platform/scratch"
)

// sourceResolver is the authorized source lookup of direct delivery.
type sourceResolver interface {
	Resolve(context.Context, access.Principal, string) (media.Source, error)
}

type extractor interface {
	Locate(context.Context, string, domain.ProbeSource, matroska.Kind, int) (matroska.Item, error)
}

// matroskaService implements media.ExtractedResolver over the isolated
// mkvtoolnix runtime (E4, G15.5, G15.7). Without the runtime it stays wired
// and answers every item like a missing one, so the configured routes keep
// their meaning while doctor reports the missing tool.
type matroskaService struct {
	sources   sourceResolver
	extractor extractor
	cacheRoot string
	reason    string
	cleanup   func() error
	// ocr derives SRT tracks from bitmap subtitles (G15.6) when enabled and
	// the Tesseract runtime verified; nil otherwise.
	ocr     ocrLocator
	ocrRoot string
}

var _ media.ExtractedResolver = (*matroskaService)(nil)

func (m *matroskaService) Available() bool { return m != nil && m.extractor != nil }

// matroskaContainers are the stored content types the extraction accepts.
var matroskaContainers = map[string]bool{"video/x-matroska": true, "video/webm": true, "audio/x-matroska": true, "audio/webm": true}

func (m *matroskaService) ResolveExtracted(ctx context.Context, principal access.Principal, sourceID string, kind media.ExtractedKind, index int) (media.Source, error) {
	// The same live session and ACL statement as direct delivery, first.
	source, err := m.sources.Resolve(ctx, principal, sourceID)
	if err != nil {
		return media.Source{}, err
	}
	if !m.Available() || !matroskaContainers[source.ContentType] {
		return media.Source{}, media.ErrNotFound
	}
	if kind == media.ExtractedOCRSubtitle {
		return m.resolveOCR(ctx, sourceID, source, index)
	}
	kinds := map[media.ExtractedKind]matroska.Kind{media.ExtractedSubtitle: matroska.KindSubtitle, media.ExtractedAttachment: matroska.KindAttachment, media.ExtractedAttachmentStream: matroska.KindAttachmentStream}
	selected, ok := kinds[kind]
	if !ok {
		return media.Source{}, media.ErrNotFound
	}
	item, err := m.extractor.Locate(ctx, sourceID, domain.ProbeSource{RootPath: source.Root, RelativePath: source.RelativePath}, selected, index)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return media.Source{}, ctx.Err()
	case errors.Is(err, matroska.ErrNotFound), errors.Is(err, matroska.ErrTooLarge):
		return media.Source{}, media.ErrNotFound
	case errors.Is(err, matroska.ErrBusy):
		return media.Source{}, media.ErrBusy
	default:
		return media.Source{}, media.ErrIO
	}
	// Only the cache location changes; device, limits and share caps stay
	// those of the authorized source, so an item counts as its playback.
	return media.Source{Root: m.cacheRoot, RelativePath: item.RelativePath, DeviceID: source.DeviceID, Limits: source.Limits, ShareStreams: source.ShareStreams}, nil
}

func (m *matroskaService) Close() error {
	if m == nil {
		return nil
	}
	if m.ocr != nil {
		// Stops the background OCR job before its scratch directory goes.
		_ = m.ocr.Close()
	}
	if m.cleanup == nil {
		return nil
	}
	return m.cleanup()
}

// newMatroskaService registers the isolated identification and extraction
// runners. A missing tool, an unusable cache root or an unsupported
// platform disables only this capability, with a fixed reason.
func newMatroskaService(c config.Config, sources sourceResolver, budget app.WorkBudget, logger *slog.Logger) *matroskaService {
	service := &matroskaService{sources: sources, cacheRoot: c.Matroska.CacheRoot, reason: "runtime_unavailable"}
	defer func() {
		if service.extractor == nil {
			logger.Warn("matroska extraction unavailable; embedded subtitles and fonts are not served", "component", "matroska", "code", "matroska_"+service.reason)
		}
	}()
	directory, err := scratch.MkdirOwned("", scratch.ServiceMKV)
	if err != nil {
		service.reason = "temporary_unavailable"
		return service
	}
	service.cleanup = func() error { return os.RemoveAll(directory) }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identify, err := mkvruntime.New(ctx, sandbox.ToolIdentify, directory)
	if err != nil {
		return service
	}
	extract, err := mkvruntime.New(ctx, sandbox.ToolExtract, directory)
	if err != nil {
		return service
	}
	extractor, err := matroska.New(identify, extract, matroska.Config{CacheRoot: c.Matroska.CacheRoot, MaxCacheBytes: c.Matroska.CacheMaxBytes})
	if err != nil {
		service.reason = "cache_unavailable"
		return service
	}
	service.extractor, service.reason = extractor, ""
	if c.SubtitleOCR.Enable {
		service.ocr = newOCRService(ctx, c.SubtitleOCR, extractor, directory, budget, logger)
		service.ocrRoot = c.SubtitleOCR.CacheRoot
	}
	return service
}
