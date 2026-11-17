package subtitleocr

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub"
	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// External bitmap subtitles (G15.6 for sidecar files). A ".sup" file is the
// raw PGS segment stream, a VobSub ".idx" names the pictures of the MPEG
// program stream in its paired ".sub". Both are read in place through the
// same decoders as tracks extracted from Matroska, without mkvextract; only
// the derived SRT is written, to the OCR cache, under the track's ID.

// Sidecar formats.
const (
	SidecarPGS    = matroska.BitmapPGS
	SidecarVobSub = matroska.BitmapVobSub
	// MaxSidecarBytes bounds each external file OCR reads (the .sup, or
	// the .sub of a VobSub pair; the .idx has the decoder's own 4 MiB
	// bound). A larger file is not queued and gets no derived track.
	MaxSidecarBytes = 256 << 20
	// sidecarIndex is the entry index of the one track an external job
	// publishes.
	sidecarIndex = 0
)

// SidecarRequest names one external bitmap subtitle the caller has
// authorized: Files is the ".sup", or the ".idx" followed by its ".sub",
// all below the library root; Language is the track's recorded language.
type SidecarRequest struct {
	SourceID, TrackID string
	Format            string
	Language          string
	Files             []domain.ProbeSource
}

func (r SidecarRequest) valid() bool {
	if !domain.ValidID(r.SourceID) || !domain.ValidID(r.TrackID) {
		return false
	}
	switch r.Format {
	case SidecarPGS:
		return len(r.Files) == 1
	case SidecarVobSub:
		return len(r.Files) == 2
	}
	return false
}

// openSidecar opens every file of a request through the probe input, which
// resolves it below its root without following links out of it.
func openSidecar(ctx context.Context, files []domain.ProbeSource) ([]*probe.Input, error) {
	inputs := make([]*probe.Input, 0, len(files))
	for _, file := range files {
		input, err := probe.Open(ctx, file)
		if err != nil {
			closeInputs(inputs)
			return nil, err
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

func closeInputs(inputs []*probe.Input) {
	for _, input := range inputs {
		_ = input.Close()
	}
}

// oversized reports a file beyond MaxSidecarBytes.
func oversized(inputs []*probe.Input) bool {
	for _, input := range inputs {
		if input.Metadata().Size > MaxSidecarBytes {
			return true
		}
	}
	return false
}

// sidecarRevision names the OCR state of one external track: its source
// and track, format, language, every file's path, size and modification
// time, the recognizer identity and the decoder version.
func (s *Service) sidecarRevision(request SidecarRequest, inputs []*probe.Input) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("jelee-subtitle-ocr-sidecar-v1\x00" + request.SourceID + "\x00" + request.TrackID + "\x00" + request.Format + "\x00" + request.Language + "\x00" +
		s.config.Identity + "\x00" + decoderVersion + "\x00"))
	var number [8]byte
	for i, input := range inputs {
		_, _ = hash.Write([]byte(request.Files[i].RelativePath + "\x00"))
		metadata := input.Metadata()
		binary.BigEndian.PutUint64(number[:], uint64(metadata.Size)) //nolint:gosec // G115: sizes are non-negative
		_, _ = hash.Write(number[:])
		binary.BigEndian.PutUint64(number[:], uint64(metadata.ModifiedUnixNano)) //nolint:gosec // G115: bit pattern only
		_, _ = hash.Write(number[:])
	}
	return hex.EncodeToString(hash.Sum(nil))[:32]
}

// LocateSidecar returns the cached SRT derived from an external bitmap
// subtitle of its current revision. Without a result it queues the track
// and returns ErrPending. A file that cannot be opened or exceeds
// MaxSidecarBytes is ErrNotFound and is not queued.
func (s *Service) LocateSidecar(ctx context.Context, request SidecarRequest) (Item, error) {
	if s == nil || ctx == nil || !request.valid() {
		return Item{}, ErrNotFound
	}
	inputs, err := openSidecar(ctx, request.Files)
	if err != nil {
		if ctx.Err() != nil {
			return Item{}, ctx.Err()
		}
		return Item{}, ErrNotFound
	}
	stamp, tooLarge := s.sidecarRevision(request, inputs), oversized(inputs)
	closeInputs(inputs)
	if tooLarge {
		return Item{}, ErrNotFound
	}
	value, err := s.cached(request.TrackID, stamp)
	if err != nil {
		queued := request
		queued.Files = append([]domain.ProbeSource(nil), request.Files...)
		s.enqueue(job{key: request.TrackID + "/" + stamp, sourceID: request.SourceID, dir: request.TrackID, stamp: stamp, sidecar: &queued})
		return Item{}, ErrPending
	}
	for _, track := range value.Tracks {
		if track.Index == sidecarIndex && track.File != "" {
			return Item{RelativePath: request.TrackID + "/" + stamp + "/" + track.File}, nil
		}
	}
	return Item{}, ErrNotFound
}

// runSidecar recognizes one queued external bitmap subtitle, reading its
// files in place, and publishes the SRT under the track's ID.
func (s *Service) runSidecar(ctx context.Context, next job) (summary, error) {
	request := *next.sidecar
	inputs, err := openSidecar(ctx, request.Files)
	if err != nil {
		return summary{}, matroska.ErrChanged
	}
	defer closeInputs(inputs)
	if s.sidecarRevision(request, inputs) != next.stamp {
		// Replaced since it was queued: the next lookup queues the new one.
		return summary{}, nil
	}
	if oversized(inputs) {
		return summary{}, bitmapsub.ErrTooLarge
	}
	if _, err := s.cached(next.dir, next.stamp); err == nil {
		return summary{}, nil
	}
	track := bitmapInput{index: sidecarIndex, format: request.Format, language: request.Language}
	if request.Format == SidecarVobSub {
		index, data := inputs[0], inputs[1]
		track.decode = func(visit func(bitmapsub.Event) error) (bitmapsub.Stats, error) {
			return bitmapsub.DecodeVobSub(readerOnly{index.Stdin()}, data.Stdin(), data.Metadata().Size, visit)
		}
	} else {
		track.decode = func(visit func(bitmapsub.Event) error) (bitmapsub.Stats, error) {
			return bitmapsub.DecodePGS(readerOnly{inputs[0].Stdin()}, visit)
		}
	}
	return s.produce(ctx, next, []bitmapInput{track}, inputs)
}
