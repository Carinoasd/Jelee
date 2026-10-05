package matroska

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// Bounds of one bitmap extraction for OCR (G15.6). Each extracted file is
// also bounded by the sandbox's RLIMIT_FSIZE (sandbox.ExtractFileLimit).
const (
	MaxBitmapTracks = 8
	MaxBitmapBytes  = 256 << 20
)

// BitmapTrack is one PGS or VobSub track copied, unconverted, into a
// caller's directory. ID is the mkvmerge track ID, which is the probe
// stream index. Files are the names written: "t<id>.sup" for PGS,
// "t<id>.idx" and "t<id>.sub" for VobSub.
type BitmapTrack struct {
	ID       int
	Format   string
	Language string
	Files    []string
}

// bitmapOutputs maps what mkvextract writes for an output name without an
// extension to the names kept in the caller's directory.
func bitmapOutputs(id int, format string) map[string]string {
	name := sandbox.ExtractTrackName(id)
	if format == BitmapVobSub {
		// mkvextract writes the VobSub index and the MPEG program stream
		// next to each other, deriving both names from the given one.
		return map[string]string{name + ".idx": name + ".idx", name + ".sub": name + ".sub"}
	}
	return map[string]string{name: name + ".sup"}
}

// ExtractBitmaps identifies an opened source and copies its PGS and VobSub
// tracks (at most MaxBitmapTracks) into directory, an existing private
// directory the caller owns and removes. The text cache is not touched and
// nothing is decoded here. A source with no such track returns no tracks;
// a source modified while it was read returns ErrChanged.
func (e *Extractor) ExtractBitmaps(ctx context.Context, source domain.ProbeSource, input *probe.Input, directory string) ([]BitmapTrack, error) {
	if e == nil || ctx == nil || input == nil {
		return nil, ErrUnavailable
	}
	target, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = target.Close() }()
	result, err := e.identify.Run(ctx, process.ToolRequest{Stdin: input.Stdin()})
	if err != nil {
		return nil, toolError(err)
	}
	container, err := ParseIdentify(result.Stdout)
	if err != nil {
		return nil, nil
	}
	var tracks []BitmapTrack
	var plan sandbox.Extraction
	for _, track := range container.Tracks {
		format, ok := track.BitmapFormat()
		if !ok || len(tracks) >= MaxBitmapTracks || track.ID > sandbox.MaxExtractTrackID {
			continue
		}
		tracks = append(tracks, BitmapTrack{ID: track.ID, Format: format, Language: track.Language})
		plan.Tracks = append(plan.Tracks, track.ID)
	}
	if len(tracks) == 0 {
		return nil, nil
	}
	collect := func(private string) error {
		output, err := os.OpenRoot(private)
		if err != nil {
			return ErrUnavailable
		}
		defer func() { _ = output.Close() }()
		var total int64
		kept := tracks[:0]
		for _, track := range tracks {
			outputs := bitmapOutputs(track.ID, track.Format)
			complete := true
			for source := range outputs {
				if _, err := output.Lstat(source); err != nil {
					complete = false
				}
			}
			if !complete {
				// An empty track writes nothing; a VobSub without both files
				// cannot be decoded.
				continue
			}
			for source, name := range outputs {
				written, err := copyBounded(output, source, target, name, MaxBitmapBytes-total)
				if err != nil {
					return err
				}
				total += written
				track.Files = append(track.Files, name)
			}
			kept = append(kept, track)
		}
		tracks = kept
		return nil
	}
	if _, err := e.extract.Run(ctx, process.ToolRequest{Stdin: input.Stdin(), Extraction: plan, Collect: collect}); err != nil {
		if errors.Is(err, ErrTooLarge) {
			return nil, ErrTooLarge
		}
		return nil, toolError(err)
	}
	if err := unchanged(ctx, source, input); err != nil {
		return nil, err
	}
	for i := range tracks {
		slices.Sort(tracks[i].Files)
	}
	return tracks, nil
}

// copyBounded copies one regular file between two roots, refusing a file
// larger than the remaining budget or the sandbox's per-file limit.
func copyBounded(from *os.Root, source string, to *os.Root, name string, budget int64) (int64, error) {
	info, err := from.Lstat(source)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, ErrUnavailable
	}
	if err != nil || !info.Mode().IsRegular() {
		return 0, ErrUnavailable
	}
	if info.Size() > sandbox.ExtractFileLimit || info.Size() > budget {
		return 0, ErrTooLarge
	}
	in, err := from.Open(source)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer func() { _ = in.Close() }()
	out, err := to.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, ErrUnavailable
	}
	written, copyErr := io.Copy(out, io.LimitReader(in, info.Size()+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil || written != info.Size() {
		return 0, ErrUnavailable
	}
	return written, nil
}
