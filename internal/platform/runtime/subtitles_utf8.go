package runtime

import (
	"context"
	"log/slog"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/adapter/subtitles"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// trackResolver is the authorized external track lookup of direct delivery.
type trackResolver interface {
	ResolveTrack(ctx context.Context, principal access.Principal, sourceID string, kind media.TrackKind, trackID string) (media.Source, error)
}

// subtitleCacheBounds splits the configured subtitle cache bound between
// the Matroska extraction cache and the derived UTF-8 copies (G15.4): the
// copies get a sixteenth, so both together stay within cacheMaxBytes.
func subtitleCacheBounds(c config.Config) (extraction, derived int64) {
	total := c.Matroska.CacheMaxBytes
	if total <= 0 {
		total = config.DefaultMatroskaConfig().CacheMaxBytes
	}
	derived = max(total/16, subtitles.MinDerivedMaxBytes)
	return total - derived, derived
}

// utf8Service implements media.UTF8Resolver over the derived cache. The
// track is authorized first, exactly as for its original; only then is the
// cache consulted or filled.
type utf8Service struct {
	tracks trackResolver
	cache  *subtitles.DerivedCache
	logger *slog.Logger
}

var _ media.UTF8Resolver = (*utf8Service)(nil)

// newUTF8Service opens the derived cache below the subtitle cache root. An
// unusable root leaves the service without a cache: every track is then
// delivered as its original, and the native UTF-8 route answers like a
// missing track.
func newUTF8Service(c config.Config, tracks trackResolver, logger *slog.Logger) *utf8Service {
	_, bound := subtitleCacheBounds(c)
	cache, err := subtitles.NewDerivedCache(c.Matroska.CacheRoot, bound)
	if err != nil {
		logger.Warn("derived UTF-8 subtitle cache unavailable; external subtitles are delivered as they are", "component", "subtitles", "code", "subtitle_utf8_cache_unavailable")
		cache = nil
	}
	return &utf8Service{tracks: tracks, cache: cache, logger: logger}
}

// Available reports whether copies can be produced.
func (u *utf8Service) Available() bool { return u != nil && u.cache != nil }

func (u *utf8Service) ResolveUTF8Track(ctx context.Context, principal access.Principal, sourceID, trackID string) (media.Source, error) {
	source, err := u.tracks.ResolveTrack(ctx, principal, sourceID, media.TrackSubtitle, trackID)
	if err != nil {
		return media.Source{}, err
	}
	format := strings.ToLower(strings.TrimPrefix(path.Ext(source.RelativePath), "."))
	if format == "sub" && source.SubtitleKind != domain.SidecarSubtitleMicroDVD {
		return media.Source{}, u.notDerived(ctx, trackID, subtitles.ReasonFormat)
	}
	if !u.Available() {
		return media.Source{}, u.notDerived(ctx, trackID, subtitles.ReasonCacheUnavailable)
	}
	relative, err := u.cache.Locate(ctx, subtitles.DerivedRequest{SourceID: sourceID, TrackID: trackID, Root: source.Root, RelativePath: source.RelativePath, Format: format, Charset: source.Charset})
	if err != nil {
		if ctx.Err() != nil {
			return media.Source{}, ctx.Err()
		}
		reason := subtitles.DerivedReason(err)
		if reason == "" {
			reason = subtitles.ReasonCacheUnavailable
		}
		return media.Source{}, u.notDerived(ctx, trackID, reason)
	}
	// Only the location changes; device, limits and share caps stay those
	// of the authorized track, so the copy counts as its playback.
	return media.Source{Root: u.cache.CacheRoot(), RelativePath: relative, DeviceID: source.DeviceID, Limits: source.Limits, ShareStreams: source.ShareStreams,
		Charset: "UTF-8", SubtitleKind: source.SubtitleKind}, nil
}

// notDerived records why a track has no copy: only its ID and a fixed
// reason code, never names or paths. Expected cases (already UTF-8, a
// format that declares its own encoding) are debug level; an unknown or
// disproved charset, the mojibake risk of G15.4, is info.
func (u *utf8Service) notDerived(ctx context.Context, trackID, reason string) error {
	level := slog.LevelInfo
	if reason == subtitles.ReasonAlreadyUTF8 || reason == subtitles.ReasonFormat {
		level = slog.LevelDebug
	}
	u.logger.Log(ctx, level, "subtitle UTF-8 copy not offered; original kept", "component", "subtitles", "trackId", trackID, "code", reason)
	return media.ErrNotDerived
}

// derivedSweeper runs the subtitle_cache_cleanup job over both caches below
// the subtitle cache root: the Matroska extraction cache and the derived
// UTF-8 copies. Sources are the union of both listings; each cache sweeps
// only the sources it holds.
type derivedSweeper struct {
	caches []sourceSweeper
}

type sourceSweeper interface {
	app.SubtitleCacheSweeper
	HasSource(string) bool
}

var _ app.SubtitleCacheSweeper = derivedSweeper{}

func (d derivedSweeper) CountSources(ctx context.Context) (int64, error) {
	names, err := d.ListSources(ctx, "", int(^uint(0)>>1))
	return int64(len(names)), err
}

func (d derivedSweeper) ListSources(ctx context.Context, after string, limit int) ([]string, error) {
	seen := map[string]bool{}
	var merged []string
	for _, cache := range d.caches {
		names, err := cache.ListSources(ctx, after, limit)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if !seen[name] {
				seen[name] = true
				merged = append(merged, name)
			}
		}
	}
	slices.Sort(merged)
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, nil
}

func (d derivedSweeper) SweepSource(ctx context.Context, source string, live bool, idleBefore time.Time) (domain.SubtitleCacheSweep, error) {
	var total domain.SubtitleCacheSweep
	for _, cache := range d.caches {
		if !cache.HasSource(source) {
			continue
		}
		sweep, err := cache.SweepSource(ctx, source, live, idleBefore)
		total.Add(sweep)
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (d derivedSweeper) EnforceCapacity(ctx context.Context) (domain.SubtitleCacheSweep, error) {
	var total domain.SubtitleCacheSweep
	for _, cache := range d.caches {
		sweep, err := cache.EnforceCapacity(ctx)
		total.Add(sweep)
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
