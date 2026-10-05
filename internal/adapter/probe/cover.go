package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"strconv"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

// CoverAdapter reads embedded cover pictures (G40.4) through the sealed
// sandboxed ffprobe cover operations. It copies the stored stream payload
// byte for byte; there is no decoder, encoder or ffmpeg invocation.
type CoverAdapter struct{ runner executor }

// NewCoverAdapter accepts only the concrete isolated runner; tests in this
// package replace the executor directly.
func NewCoverAdapter(runner *process.IsolatedRunner) (*CoverAdapter, error) {
	if runner == nil {
		return nil, ErrInvalidInput
	}
	return &CoverAdapter{runner: runner}, nil
}

// ExtractCover opens the candidate's media file once, checks that it still
// has the probed size, mtime and edge fingerprint, runs the cover read for
// the candidate's video-relative stream on that descriptor, and checks the
// file again afterwards. The returned bytes are the attached picture packet,
// verified against ffprobe's own SHA-256 of the packet, a known picture
// signature and a header-only dimension precheck.
func (a *CoverAdapter) ExtractCover(ctx context.Context, candidate domain.EmbeddedCoverCandidate) (cover domain.EmbeddedCover, resultErr error) {
	if a == nil || a.runner == nil || ctx == nil || !domain.ValidEmbeddedCoverCandidate(candidate) {
		return domain.EmbeddedCover{}, ErrInvalidInput
	}
	// The candidate check bounds VideoIndex to the sealed operation range.
	operation := process.CoverOperation(candidate.VideoIndex)
	input, err := Open(ctx, candidate.Source())
	if err != nil {
		return domain.EmbeddedCover{}, workerError(err)
	}
	defer func() {
		if err := input.Close(); err != nil && resultErr == nil {
			cover, resultErr = domain.EmbeddedCover{}, domain.ErrProbeInputUnavailable
		}
	}()
	// The held descriptor must still show the probed size, mtime and edge
	// fingerprint before and after the child reads it.
	check := func() (os.FileInfo, error) {
		info, err := input.Stdin().Stat()
		if err != nil || !matches(info, input.Metadata()) || info.Size() != candidate.Stamp.Size || info.ModTime().UnixNano() != candidate.Stamp.ModifiedUnixNano {
			return nil, domain.ErrProbeSourceChanged
		}
		fingerprint, err := edgeFingerprint(ctx, input.Stdin(), info.Size())
		if err != nil || fingerprint != candidate.Stamp.Fingerprint {
			return nil, domain.ErrProbeSourceChanged
		}
		return info, nil
	}
	before, err := check()
	if err != nil {
		return domain.EmbeddedCover{}, err
	}
	result, err := a.runner.Run(ctx, process.Request{Tool: "ffprobe", Operation: operation, Stdin: input.Stdin()})
	if err := ctx.Err(); err != nil {
		return domain.EmbeddedCover{}, err
	}
	if err != nil {
		return domain.EmbeddedCover{}, coverProcessError(err)
	}
	cover, err = parseCoverOutput(result.Stdout, candidate.StreamIndex)
	if err != nil {
		return domain.EmbeddedCover{}, err
	}
	if _, err := check(); err != nil {
		return domain.EmbeddedCover{}, err
	}
	// Re-resolve the authorized path to reject replacement of the catalog
	// target while ffprobe read the original descriptor.
	current, err := Open(ctx, candidate.Source())
	if err != nil {
		return domain.EmbeddedCover{}, domain.ErrProbeSourceChanged
	}
	currentInfo, statErr := current.Stdin().Stat()
	closeErr := current.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(before, currentInfo) || !matches(currentInfo, input.Metadata()) {
		return domain.EmbeddedCover{}, domain.ErrProbeSourceChanged
	}
	return cover, nil
}

func coverProcessError(err error) error {
	switch {
	case errors.Is(err, process.ErrOutputLimit):
		return domain.ErrEmbeddedCoverTooLarge
	case errors.Is(err, process.ErrExit):
		// The verified helper ran ffprobe and it rejected this input.
		return domain.ErrEmbeddedCoverInvalid
	}
	return workerError(classifyProcessError(err))
}

type coverOutput struct {
	Packets []struct {
		StreamIndex *int    `json:"stream_index"`
		Size        *string `json:"size"`
		Data        *string `json:"data"`
		DataHash    *string `json:"data_hash"`
	} `json:"packets"`
	Streams []struct {
		Index       *int    `json:"index"`
		CodecType   *string `json:"codec_type"`
		CodecName   *string `json:"codec_name"`
		Disposition struct {
			AttachedPic *int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
}

// parseCoverOutput accepts exactly one selected stream, which must be the
// expected attached picture, and exactly one packet of it.
func parseCoverOutput(data []byte, stream int) (domain.EmbeddedCover, error) {
	var out coverOutput
	if len(data) == 0 || json.Unmarshal(data, &out) != nil {
		return domain.EmbeddedCover{}, domain.ErrEmbeddedCoverInvalid
	}
	if len(out.Streams) != 1 {
		return domain.EmbeddedCover{}, domain.ErrEmbeddedCoverInvalid
	}
	s := out.Streams[0]
	if s.Index == nil || *s.Index != stream || s.CodecType == nil || *s.CodecType != "video" || s.Disposition.AttachedPic == nil || *s.Disposition.AttachedPic != 1 {
		return domain.EmbeddedCover{}, domain.ErrEmbeddedCoverAbsent
	}
	format := ""
	if s.CodecName != nil {
		format = domain.EmbeddedCoverFormat(*s.CodecName)
	}
	if format == "" {
		return domain.EmbeddedCover{}, domain.ErrEmbeddedCoverAbsent
	}
	if len(out.Packets) != 1 {
		return domain.EmbeddedCover{}, domain.ErrEmbeddedCoverInvalid
	}
	p := out.Packets[0]
	if p.StreamIndex == nil || *p.StreamIndex != stream || p.Size == nil || p.Data == nil || p.DataHash == nil {
		return domain.EmbeddedCover{}, domain.ErrEmbeddedCoverInvalid
	}
	size, err := strconv.ParseInt(*p.Size, 10, 64)
	if err != nil || size < 1 {
		return domain.EmbeddedCover{}, domain.ErrEmbeddedCoverInvalid
	}
	if size > domain.EmbeddedCoverMaxBytes {
		return domain.EmbeddedCover{}, domain.ErrEmbeddedCoverTooLarge
	}
	payload, err := decodeHexDump(*p.Data, int(size))
	if err != nil {
		return domain.EmbeddedCover{}, err
	}
	digest := sha256.Sum256(payload)
	if *p.DataHash != "SHA256:"+hex.EncodeToString(digest[:]) {
		return domain.EmbeddedCover{}, domain.ErrEmbeddedCoverInvalid
	}
	width, height, err := coverDimensions(payload, format)
	if err != nil {
		return domain.EmbeddedCover{}, err
	}
	return domain.EmbeddedCover{Data: payload, SHA256: digest, Format: format, Width: width, Height: height}, nil
}

// decodeHexDump reads ffprobe's data dump: lines of "%08x: " offset, up to
// sixteen bytes as hex pairs grouped by two, padding and an ASCII column. The
// offsets must be consecutive and the total must equal the packet size.
func decodeHexDump(text string, size int) ([]byte, error) {
	if size < 1 || size > domain.EmbeddedCoverMaxBytes || !strings.HasPrefix(text, "\n") {
		return nil, domain.ErrEmbeddedCoverInvalid
	}
	out := make([]byte, 0, size)
	rest := text[1:]
	for len(rest) > 0 {
		line, next, found := strings.Cut(rest, "\n")
		if !found {
			return nil, domain.ErrEmbeddedCoverInvalid
		}
		rest = next
		if len(line) < 10 || line[8] != ':' || line[9] != ' ' {
			return nil, domain.ErrEmbeddedCoverInvalid
		}
		offset, err := strconv.ParseUint(line[:8], 16, 32)
		if err != nil || offset != uint64(len(out)) {
			return nil, domain.ErrEmbeddedCoverInvalid
		}
		count := min(16, size-len(out))
		// The hex column is 41 characters wide, padded with spaces.
		if count < 1 || len(line) != 10+41+count {
			return nil, domain.ErrEmbeddedCoverInvalid
		}
		column := line[10 : 10+41]
		var digits [32]byte
		n := 0
		for i := 0; i < len(column); i++ {
			c := column[i]
			if c == ' ' {
				continue
			}
			if n == len(digits) || !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return nil, domain.ErrEmbeddedCoverInvalid
			}
			digits[n] = c
			n++
		}
		if n != 2*count {
			return nil, domain.ErrEmbeddedCoverInvalid
		}
		for i := 0; i < count; i++ {
			out = append(out, hexNibble(digits[2*i])<<4|hexNibble(digits[2*i+1]))
		}
	}
	if len(out) != size {
		return nil, domain.ErrEmbeddedCoverInvalid
	}
	return out, nil
}

// coverDimensions requires the picture signature of the declared codec and
// reads only the image header; no pixels are decoded here.
func coverDimensions(data []byte, format string) (int, int, error) {
	var config image.Config
	var err error
	switch {
	case format == "jpeg" && bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		config, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case format == "png" && bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		config, err = png.DecodeConfig(bytes.NewReader(data))
	default:
		return 0, 0, domain.ErrEmbeddedCoverInvalid
	}
	if err != nil || config.Width < 1 || config.Height < 1 {
		return 0, 0, domain.ErrEmbeddedCoverInvalid
	}
	if config.Width > domain.EmbeddedCoverMaxDimension || config.Height > domain.EmbeddedCoverMaxDimension || int64(config.Width)*int64(config.Height) > domain.EmbeddedCoverMaxPixels {
		return 0, 0, domain.ErrEmbeddedCoverTooLarge
	}
	return config.Width, config.Height, nil
}

// hexNibble decodes one lowercase hex digit already checked by the caller.
func hexNibble(c byte) byte {
	if c >= 'a' {
		return c - 'a' + 10
	}
	return c - '0'
}
