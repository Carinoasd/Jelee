package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"slices"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// ChapterAdapter reads stored chapter pictures (G40.1) through the sealed
// sandboxed ffprobe chapter operations. Like the cover read it copies stored
// packet payloads byte for byte; there is no decoder, encoder or ffmpeg
// invocation, so it cannot generate a picture for a chapter without one.
type ChapterAdapter struct{ runner executor }

// NewChapterAdapter accepts only the concrete isolated runner; tests in this
// package replace the executor directly.
func NewChapterAdapter(runner *process.IsolatedRunner) (*ChapterAdapter, error) {
	if runner == nil {
		return nil, ErrInvalidInput
	}
	return &ChapterAdapter{runner: runner}, nil
}

// ExtractChapterImages tries the candidate's picture streams in order on a
// file that must keep its probed stamp before and after each read. The first
// stream that is a chapter picture track (timed_thumbnails) wins; a stream
// that is not one moves on to the next. Each returned picture is a packet
// verified against ffprobe's own SHA-256, a known picture signature and a
// header-only dimension precheck, assigned to the chapter its time falls in.
func (a *ChapterAdapter) ExtractChapterImages(ctx context.Context, candidate domain.ChapterImageCandidate) ([]domain.ChapterImage, error) {
	if a == nil || a.runner == nil || ctx == nil || !domain.ValidChapterImageCandidate(candidate) {
		return nil, ErrInvalidInput
	}
	for _, stream := range candidate.Streams {
		// The candidate check bounds VideoIndex to the sealed operation range.
		operation := process.ChapterOperation(stream.VideoIndex)
		var images []domain.ChapterImage
		err := readStableSource(ctx, candidate.Source(), candidate.Stamp, func(stdin *os.File) error {
			result, err := a.runner.Run(ctx, process.Request{Tool: "ffprobe", Operation: operation, Stdin: stdin})
			if err := ctx.Err(); err != nil {
				return err
			}
			if err != nil {
				return chapterProcessError(err)
			}
			images, err = parseChapterOutput(result.Stdout, stream.StreamIndex)
			return err
		})
		if errors.Is(err, domain.ErrChapterImagesAbsent) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return images, nil
	}
	return nil, domain.ErrChapterImagesAbsent
}

func chapterProcessError(err error) error {
	switch {
	case errors.Is(err, process.ErrOutputLimit):
		return domain.ErrChapterImagesTooLarge
	case errors.Is(err, process.ErrExit):
		return domain.ErrChapterImagesInvalid
	}
	return workerError(classifyProcessError(err))
}

type chapterOutput struct {
	Packets []struct {
		StreamIndex *int    `json:"stream_index"`
		PTSTime     *string `json:"pts_time"`
		Size        *string `json:"size"`
		Data        *string `json:"data"`
		DataHash    *string `json:"data_hash"`
	} `json:"packets"`
	Streams []struct {
		Index       *int    `json:"index"`
		CodecType   *string `json:"codec_type"`
		CodecName   *string `json:"codec_name"`
		Disposition struct {
			AttachedPic     *int `json:"attached_pic"`
			TimedThumbnails *int `json:"timed_thumbnails"`
		} `json:"disposition"`
	} `json:"streams"`
	Chapters []struct {
		StartTime *string `json:"start_time"`
		EndTime   *string `json:"end_time"`
	} `json:"chapters"`
}

type chapterSpan struct{ start, end int64 }

// parseChapterOutput accepts exactly one selected stream, which must be a
// JPEG or PNG chapter picture track, and maps its packets to chapters in start
// order: a packet belongs to the chapter whose [start, end) holds its time,
// the first packet of a chapter wins (the demuxer's attached picture copy
// repeats the first sample), and packets without a time or outside every
// chapter are ignored.
func parseChapterOutput(data []byte, stream int) ([]domain.ChapterImage, error) {
	var out chapterOutput
	if len(data) == 0 || json.Unmarshal(data, &out) != nil || len(out.Streams) != 1 || len(out.Packets) > sandbox.ChapterMaxPackets {
		return nil, domain.ErrChapterImagesInvalid
	}
	s := out.Streams[0]
	if s.Index == nil || *s.Index != stream || s.CodecType == nil || *s.CodecType != "video" ||
		s.Disposition.TimedThumbnails == nil || *s.Disposition.TimedThumbnails != 1 {
		return nil, domain.ErrChapterImagesAbsent
	}
	format := ""
	if s.CodecName != nil {
		format = domain.EmbeddedCoverFormat(*s.CodecName)
	}
	if format == "" || len(out.Chapters) == 0 {
		return nil, domain.ErrChapterImagesAbsent
	}
	chapters := make([]chapterSpan, 0, len(out.Chapters))
	for _, c := range out.Chapters {
		start, okStart := chapterMicros(c.StartTime)
		end, okEnd := chapterMicros(c.EndTime)
		if !okStart || !okEnd || end < start {
			return nil, domain.ErrChapterImagesInvalid
		}
		chapters = append(chapters, chapterSpan{start, end})
	}
	slices.SortStableFunc(chapters, func(a, b chapterSpan) int {
		switch {
		case a.start < b.start:
			return -1
		case a.start > b.start:
			return 1
		}
		return 0
	})
	images := make([]domain.ChapterImage, 0, min(len(chapters), domain.ChapterImageMaxPerItem))
	taken := make(map[int]bool, len(chapters))
	total := 0
	for _, p := range out.Packets {
		if p.StreamIndex == nil || *p.StreamIndex != stream || p.Size == nil || p.Data == nil || p.DataHash == nil {
			return nil, domain.ErrChapterImagesInvalid
		}
		size, err := strconv.ParseInt(*p.Size, 10, 64)
		if err != nil || size < 1 {
			return nil, domain.ErrChapterImagesInvalid
		}
		if size > domain.ChapterImageMaxBytes {
			return nil, domain.ErrChapterImagesTooLarge
		}
		at, ok := chapterMicros(p.PTSTime)
		if !ok {
			continue
		}
		index := chapterAt(chapters, at)
		if index < 0 || index > domain.ItemImageMaxIndex || taken[index] {
			continue
		}
		payload, err := decodeHexDump(*p.Data, int(size))
		if err != nil {
			return nil, chapterImageError(err)
		}
		digest := sha256.Sum256(payload)
		if *p.DataHash != "SHA256:"+hex.EncodeToString(digest[:]) {
			return nil, domain.ErrChapterImagesInvalid
		}
		width, height, err := coverDimensions(payload, format)
		if err != nil {
			return nil, chapterImageError(err)
		}
		if total += len(payload); total > domain.ChapterImageMaxTotalBytes || len(images) == domain.ChapterImageMaxPerItem {
			return nil, domain.ErrChapterImagesTooLarge
		}
		taken[index] = true
		images = append(images, domain.ChapterImage{Index: index, Data: payload, SHA256: digest, Format: format, Width: width, Height: height})
	}
	if len(images) == 0 {
		return nil, domain.ErrChapterImagesAbsent
	}
	slices.SortFunc(images, func(a, b domain.ChapterImage) int { return a.Index - b.Index })
	return images, nil
}

// chapterAt returns the position of the chapter whose [start, end) holds at,
// or of the last chapter starting exactly at at; -1 when none does.
func chapterAt(chapters []chapterSpan, at int64) int {
	found := -1
	for i, c := range chapters {
		if c.start > at {
			break
		}
		if at < c.end || at == c.start {
			found = i
		}
	}
	return found
}

// chapterMicros parses an ffprobe decimal time in seconds. "N/A", a missing
// value and anything outside about ±31 700 years are not times.
func chapterMicros(value *string) (int64, bool) {
	if value == nil {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(*value, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || math.Abs(seconds) > 1e12 {
		return 0, false
	}
	return int64(math.Round(seconds * 1e6)), true
}

func chapterImageError(err error) error {
	if errors.Is(err, domain.ErrEmbeddedCoverTooLarge) {
		return domain.ErrChapterImagesTooLarge
	}
	return domain.ErrChapterImagesInvalid
}
