package images

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"golang.org/x/image/draw"
)

type Options struct {
	TempRoot           string
	MaxConcurrent      int
	MaxImageBytes      int64
	MaxSourceBytes     int64
	MaxOutputBytes     int64
	CacheBytes         int64
	MaxOutputDimension int
	CacheEntries       int
	DefaultQuality     int
	Timeout            time.Duration
	CacheTTL           time.Duration
}

func validOptions(o Options) bool {
	// Keep the public configuration and adapter bounds identical. Per-field
	// checks make the aggregate arithmetic below safe before multiplication.
	return len(o.TempRoot) <= 4096 && utf8.ValidString(o.TempRoot) && !strings.ContainsFunc(o.TempRoot, unicode.IsControl) &&
		filepath.IsAbs(o.TempRoot) &&
		o.MaxConcurrent >= 1 && o.MaxConcurrent <= 8 && o.MaxImageBytes >= 16<<20 && o.MaxImageBytes <= 256<<20 &&
		o.MaxSourceBytes >= 1 && o.MaxSourceBytes <= 64<<20 && o.MaxOutputBytes >= 64<<10 && o.MaxOutputBytes <= 8<<20 &&
		o.MaxOutputDimension >= 16 && o.MaxOutputDimension <= 2048 && o.CacheBytes >= o.MaxOutputBytes && o.CacheBytes <= 256<<20 &&
		o.CacheEntries >= 1 && o.CacheEntries <= 4096 && o.Timeout >= time.Second && o.Timeout <= 120*time.Second &&
		o.CacheTTL >= time.Second && o.CacheTTL <= 86400*time.Second && o.DefaultQuality >= 1 && o.DefaultQuality <= 100 &&
		o.MaxOutputBytes < o.MaxImageBytes && int64(o.MaxConcurrent)*o.MaxImageBytes+o.CacheBytes <= 1<<30
}

// Stats contains aggregate counts only; source identifiers and paths never
// become labels. Completed means a response was prepared, not delivered.
type Stats struct {
	Active, ReservedBytes, MaxEstimatedImageBytes int64
	Admitted, Completed, Failed, Busy             uint64
	CacheHits, CacheMisses, Decodes               uint64
	CacheEntries                                  int
	CacheBytes                                    int64
	CacheEvictions                                uint64
}

type Processor struct {
	options     Options
	lifetime    context.Context
	cancel      context.CancelFunc
	cache       *imageCache
	mu          sync.Mutex
	active      int64
	closed      bool
	done        chan struct{}
	maxEstimate int64
	admitted    atomic.Uint64
	completed   atomic.Uint64
	failed      atomic.Uint64
	busy        atomic.Uint64
	hits        atomic.Uint64
	misses      atomic.Uint64
	decodes     atomic.Uint64
	// A private seam lets lifecycle tests stop inside a synchronous decode.
	// Production always uses the pinned standard-library decoders below.
	decode func(context.Context, io.ReadSeeker, inspectedImage) (image.Image, error)
}

func New(lifetime context.Context, options Options) (*Processor, error) {
	if lifetime == nil || !validOptions(options) {
		return nil, domain.ErrInvalid
	}
	if err := lifetime.Err(); err != nil {
		return nil, err
	}
	options.TempRoot = filepath.Clean(options.TempRoot)
	lifetime, cancel := context.WithCancel(lifetime)
	return &Processor{options: options, lifetime: lifetime, cancel: cancel,
		cache: newImageCache(options.CacheBytes, options.CacheEntries, options.CacheTTL),
		done:  make(chan struct{}), decode: decodeImage}, nil
}

func (p *Processor) admit() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.lifetime.Err() != nil {
		return domain.ErrImageUnavailable
	}
	if p.active >= int64(p.options.MaxConcurrent) {
		p.busy.Add(1)
		return domain.ErrImageBusy
	}
	p.active++
	p.admitted.Add(1)
	return nil
}

func (p *Processor) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active--
	if p.closed && p.active == 0 {
		close(p.done)
	}
}

func (p *Processor) Shutdown(ctx context.Context) error {
	if p == nil || ctx == nil {
		return domain.ErrInvalid
	}
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		p.cancel()
		if p.active == 0 {
			close(p.done)
		}
	}
	p.mu.Unlock()
	p.cache.shutdown()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Processor) Stats() Stats {
	if p == nil {
		return Stats{}
	}
	p.mu.Lock()
	result := Stats{Active: p.active, ReservedBytes: p.active * p.options.MaxImageBytes, MaxEstimatedImageBytes: p.maxEstimate}
	p.mu.Unlock()
	result.Admitted, result.Completed, result.Failed, result.Busy = p.admitted.Load(), p.completed.Load(), p.failed.Load(), p.busy.Load()
	result.CacheHits, result.CacheMisses, result.Decodes = p.hits.Load(), p.misses.Load(), p.decodes.Load()
	result.CacheEntries, result.CacheBytes, result.CacheEvictions = p.cache.stats()
	return result
}

func (p *Processor) Render(ctx context.Context, source domain.LocalImageSource, request domain.ImageRequest) (result app.ImageResult, err error) {
	if p == nil || ctx == nil {
		return result, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	request, err = domain.NormalizeImageRequest(request)
	if err != nil {
		return result, err
	}
	if request.Quality == 0 {
		request.Quality = p.options.DefaultQuality
	}
	if err = p.admit(); err != nil {
		return result, err
	}
	operation, cancel := context.WithTimeout(ctx, p.options.Timeout)
	stopLifetime := context.AfterFunc(p.lifetime, cancel)
	var once sync.Once
	release := func() { once.Do(func() { stopLifetime(); cancel(); p.release() }) }
	success := false
	defer func() {
		if !success {
			p.failed.Add(1)
			release()
		}
	}()
	// Staging can occupy tmpfs pages before image dimensions are known. Leave
	// space for scratch and both encoded buffers even at this first boundary.
	stageLimit := min(p.options.MaxSourceBytes, p.options.MaxImageBytes-(1<<20)-2*p.options.MaxOutputBytes)
	if stageLimit <= 0 {
		return result, domain.ErrImageTooLarge
	}
	staged, err := stageLocalPrimary(operation, source, p.options.TempRoot, stageLimit)
	if err != nil {
		return result, imageError(operation, err)
	}
	stageClosed := false
	defer func() {
		if !stageClosed {
			if closeErr := staged.Close(); closeErr != nil {
				err = domain.ErrImageUnavailable
			}
		}
	}()
	key := imageCacheKey(staged.Key(), request)
	value, hit := p.cache.get(key)
	if hit {
		p.hits.Add(1)
	} else {
		p.misses.Add(1)
		inspected, inspectErr := inspectImage(operation, staged.Reader())
		if inspectErr != nil {
			return result, imageError(operation, inspectErr)
		}
		width, height := targetSize(inspected.width, inspected.height, request, p.options.MaxOutputDimension)
		estimate, estimateErr := estimateImageBytes(inspected, width, height, staged.Size(), p.options.MaxOutputBytes)
		if estimateErr != nil || estimate > p.options.MaxImageBytes {
			return result, domain.ErrImageTooLarge
		}
		p.mu.Lock()
		p.maxEstimate = max(p.maxEstimate, estimate)
		p.mu.Unlock()
		p.decodes.Add(1)
		decoded, decodeErr := p.decode(operation, staged.Reader(), inspected)
		if decodeErr != nil {
			return result, imageError(operation, decodeErr)
		}
		if err := operation.Err(); err != nil {
			return result, err
		}
		if decoded.Bounds() != image.Rect(0, 0, inspected.width, inspected.height) {
			return result, domain.ErrImageUnavailable
		}
		var encodeInput image.Image
		if width == inspected.width && height == inspected.height {
			encodeInput, err = sameSizeJPEGImage(operation, decoded)
			if err != nil {
				return result, imageError(operation, err)
			}
		} else {
			thumbnail := image.NewRGBA(image.Rect(0, 0, width, height))
			draw.Draw(thumbnail, thumbnail.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
			// ApproxBiLinear has no source-sized intermediate kernel buffer.
			draw.ApproxBiLinear.Scale(thumbnail, thumbnail.Bounds(), decoded, decoded.Bounds(), draw.Over, nil)
			encodeInput = thumbnail
		}
		if err := operation.Err(); err != nil {
			return result, err
		}
		output := boundedImageWriter{ctx: operation, limit: int(p.options.MaxOutputBytes)}
		if err := jpeg.Encode(&output, encodeInput, &jpeg.Options{Quality: request.Quality}); err != nil {
			return result, imageError(operation, err)
		}
		// Tighten capacity once. Both allocations are included in the estimate;
		// a small cached JPEG does not pin the entire output-byte allowance.
		encoded := make([]byte, len(output.data))
		copy(encoded, output.data)
		digest := sha256.Sum256(encoded)
		value = encodedImage{data: encoded, width: width, height: height, etag: `"` + hex.EncodeToString(digest[:]) + `"`}
	}
	if err := staged.Verify(operation); err != nil {
		return result, imageError(operation, err)
	}
	if err := operation.Err(); err != nil {
		return result, err
	}
	if err := staged.Close(); err != nil {
		return result, domain.ErrImageUnavailable
	}
	stageClosed = true
	if !hit {
		p.cache.put(key, value)
	}
	if err := operation.Err(); err != nil {
		return result, err
	}
	result = app.ImageResult{Body: &imageBody{ctx: operation, reader: bytes.NewReader(value.data), release: release},
		ContentType: "image/jpeg", ETag: value.etag, Size: int64(len(value.data)), Width: value.width, Height: value.height}
	p.completed.Add(1)
	success = true
	return result, nil
}

func imageError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, allowed := range []error{domain.ErrInvalid, domain.ErrNotFound, domain.ErrImageTooLarge, domain.ErrImageUnsupported, domain.ErrImageUnavailable} {
		if errors.Is(err, allowed) {
			return allowed
		}
	}
	return domain.ErrImageUnavailable
}

type imageBody struct {
	mu      sync.Mutex
	ctx     context.Context
	reader  *bytes.Reader
	release func()
}

func (b *imageBody) Read(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reader == nil {
		return 0, io.ErrClosedPipe
	}
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.reader.Read(data)
}

func (b *imageBody) Seek(offset int64, whence int) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reader == nil {
		return 0, io.ErrClosedPipe
	}
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.reader.Seek(offset, whence)
}

func (b *imageBody) Close() error {
	b.mu.Lock()
	if b.reader == nil {
		b.mu.Unlock()
		return nil
	}
	b.reader = nil
	release := b.release
	b.release = nil
	b.mu.Unlock()
	release()
	return nil
}

type contextImageReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextImageReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

type boundedImageWriter struct {
	ctx   context.Context
	data  []byte
	limit int
}

func (w *boundedImageWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if len(data) > w.limit-len(w.data) {
		return 0, domain.ErrImageTooLarge
	}
	needed := len(w.data) + len(data)
	if needed > cap(w.data) {
		// Small thumbnails need only a few KiB. Grow explicitly so append
		// cannot round capacity above the output limit. Old and new storage
		// together stay within the existing two-output-buffer estimate.
		capacity := min(w.limit, max(needed, max(4<<10, 2*cap(w.data))))
		grown := make([]byte, len(w.data), capacity)
		copy(grown, w.data)
		w.data = grown
	}
	w.data = append(w.data, data...)
	return len(data), nil
}

func imageCacheKey(source [32]byte, request domain.ImageRequest) [32]byte {
	var data [56]byte
	copy(data[:32], source[:])
	binary.BigEndian.PutUint64(data[32:40], uint64(request.Width))
	binary.BigEndian.PutUint64(data[40:48], uint64(request.Height))
	binary.BigEndian.PutUint64(data[48:56], uint64(request.Quality))
	return sha256.Sum256(data[:])
}

type inspectedImage struct {
	format                        string
	width, height                 int
	progressive, interlaced       bool
	components, maxH, maxV, sumHV int64
}

func inspectImage(ctx context.Context, source io.ReadSeeker) (inspectedImage, error) {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return inspectedImage{}, err
	}
	var header [33]byte
	n, err := io.ReadFull(contextImageReader{ctx, source}, header[:])
	if err != nil && n < 8 {
		return inspectedImage{}, domain.ErrImageUnsupported
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return inspectedImage{}, err
	}
	reader := contextImageReader{ctx, source}
	if bytes.Equal(header[:8], []byte("\x89PNG\r\n\x1a\n")) {
		if n < len(header) || binary.BigEndian.Uint32(header[8:12]) != 13 || string(header[12:16]) != "IHDR" {
			return inspectedImage{}, domain.ErrImageUnavailable
		}
		configuration, err := png.DecodeConfig(reader)
		if err != nil {
			return inspectedImage{}, err
		}
		return inspectedImage{format: "png", width: configuration.Width, height: configuration.Height, interlaced: header[28] == 1}, nil
	}
	if header[0] == 0xff && header[1] == 0xd8 {
		return inspectJPEG(ctx, source)
	}
	return inspectedImage{}, domain.ErrImageUnsupported
}

// This walks marker structure only, not entropy coefficients. Inspect the
// whole staged JPEG: APP0 may clear JFIF and APP14 may declare RGB after SOF or
// SOS, beyond DecodeConfig's early return. Reject formats that cause the Go
// decoder to allocate an additional full-size RGB/CMYK conversion image.
func inspectJPEG(ctx context.Context, source io.ReadSeeker) (inspectedImage, error) {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return inspectedImage{}, err
	}
	r := bufio.NewReaderSize(contextImageReader{ctx, source}, 16<<10)
	var start [2]byte
	if _, err := io.ReadFull(r, start[:]); err != nil || start != [2]byte{0xff, 0xd8} {
		return inspectedImage{}, domain.ErrImageUnavailable
	}
	result := inspectedImage{format: "jpeg"}
	inScan, sawScan := false, false
	for {
		prefix, err := r.ReadByte()
		if err != nil {
			return inspectedImage{}, err
		}
		if prefix != 0xff {
			if inScan {
				continue
			}
			return inspectedImage{}, domain.ErrImageUnavailable
		}
		marker, err := r.ReadByte()
		if err != nil {
			return inspectedImage{}, err
		}
		for marker == 0xff {
			marker, err = r.ReadByte()
			if err != nil {
				return inspectedImage{}, err
			}
		}
		if inScan && (marker == 0 || marker >= 0xd0 && marker <= 0xd7) {
			continue
		}
		inScan = false
		if marker == 0xd9 {
			if result.width == 0 || !sawScan {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			return result, nil
		}
		if marker == 0 || marker == 0xd8 || marker >= 0xd0 && marker <= 0xd7 {
			return inspectedImage{}, domain.ErrImageUnavailable
		}
		var length [2]byte
		if _, err := io.ReadFull(r, length[:]); err != nil {
			return inspectedImage{}, err
		}
		size := int(binary.BigEndian.Uint16(length[:])) - 2
		if size < 0 {
			return inspectedImage{}, domain.ErrImageUnavailable
		}
		var segment [32]byte
		kept := min(size, len(segment))
		if _, err := io.ReadFull(r, segment[:kept]); err != nil {
			return inspectedImage{}, err
		}
		if _, err := r.Discard(size - kept); err != nil {
			return inspectedImage{}, err
		}
		switch {
		case marker == 0xc0 || marker == 0xc1 || marker == 0xc2:
			if result.width != 0 || size < 6 || segment[0] != 8 {
				return inspectedImage{}, domain.ErrImageUnsupported
			}
			components := int(segment[5])
			if components != 1 && components != 3 || size != 6+3*components {
				return inspectedImage{}, domain.ErrImageUnsupported
			}
			result.width, result.height = int(binary.BigEndian.Uint16(segment[3:5])), int(binary.BigEndian.Uint16(segment[1:3]))
			if result.width == 0 || result.height == 0 {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			result.components, result.progressive = int64(components), marker == 0xc2
			if components == 3 && segment[6] == 'R' && segment[9] == 'G' && segment[12] == 'B' {
				return inspectedImage{}, domain.ErrImageUnsupported
			}
			for index := 0; index < components; index++ {
				h, v := int64(segment[7+3*index]>>4), int64(segment[7+3*index]&15)
				if h != 1 && h != 2 && h != 4 || v != 1 && v != 2 && v != 4 {
					return inspectedImage{}, domain.ErrImageUnsupported
				}
				if components == 1 {
					h, v = 1, 1
				}
				result.maxH, result.maxV, result.sumHV = max(result.maxH, h), max(result.maxV, v), result.sumHV+h*v
			}
		case marker == 0xda:
			if result.width == 0 {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			inScan, sawScan = true, true
		case marker == 0xee:
			if size >= 12 && string(segment[:5]) == "Adobe" && segment[11] != 1 {
				return inspectedImage{}, domain.ErrImageUnsupported
			}
		case marker >= 0xe0 && marker <= 0xef || marker == 0xfe || marker == 0xc4 || marker == 0xdb || marker == 0xdd:
			// Metadata and table contents remain the standard decoder's job.
		default:
			return inspectedImage{}, domain.ErrImageUnsupported
		}
	}
}

func checkedMultiply(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a != 0 && b > math.MaxInt64/a {
		return 0, false
	}
	return a * b, true
}

func checkedSum(values ...int64) (int64, bool) {
	var result int64
	for _, value := range values {
		if value < 0 || result > math.MaxInt64-value {
			return 0, false
		}
		result += value
	}
	return result, true
}

func estimateImageBytes(input inspectedImage, width, height int, sourceBytes, outputBytes int64) (int64, error) {
	if input.width <= 0 || input.height <= 0 || width <= 0 || height <= 0 || sourceBytes < 0 || outputBytes <= 0 {
		return 0, domain.ErrImageTooLarge
	}
	w, h := int64(input.width), int64(input.height)
	var decoded, rows, coefficients int64
	var ok bool
	switch input.format {
	case "png":
		pixels, valid := checkedMultiply(w, h)
		if !valid {
			return 0, domain.ErrImageTooLarge
		}
		factor, rowFactor := int64(8), int64(2)
		if input.interlaced {
			factor, rowFactor = 16, 14
		}
		decoded, ok = checkedMultiply(pixels, factor)
		if !ok {
			return 0, domain.ErrImageTooLarge
		}
		row, valid := checkedMultiply(w, 8)
		if !valid || row == math.MaxInt64 {
			return 0, domain.ErrImageTooLarge
		}
		rows, ok = checkedMultiply(row+1, rowFactor)
	case "jpeg":
		if input.maxH < 1 || input.maxH > 4 || input.maxV < 1 || input.maxV > 4 || input.sumHV < 1 || input.sumHV > 48 || input.components != 1 && input.components != 3 {
			return 0, domain.ErrImageTooLarge
		}
		if w > math.MaxInt64-31 || h > math.MaxInt64-31 {
			return 0, domain.ErrImageTooLarge
		}
		mxx, myy := (w+8*input.maxH-1)/(8*input.maxH), (h+8*input.maxV-1)/(8*input.maxV)
		mcus, valid := checkedMultiply(mxx, myy)
		if !valid {
			return 0, domain.ErrImageTooLarge
		}
		decoded, ok = checkedMultiply(mcus, 64*input.maxH*input.maxV*input.components)
		if input.progressive {
			coefficients, valid = checkedMultiply(mcus, input.sumHV*64*4)
			if !valid {
				return 0, domain.ErrImageTooLarge
			}
		}
	default:
		return 0, domain.ErrImageUnsupported
	}
	if !ok {
		return 0, domain.ErrImageTooLarge
	}
	thumbnail, ok := checkedMultiply(int64(width), int64(height))
	if !ok {
		return 0, domain.ErrImageTooLarge
	}
	thumbnail, ok = checkedMultiply(thumbnail, 4)
	if !ok {
		return 0, domain.ErrImageTooLarge
	}
	encoded, ok := checkedMultiply(outputBytes, 2)
	if !ok {
		return 0, domain.ErrImageTooLarge
	}
	// Go 1.27.1 PNG: full 8-byte pixels, plus all Adam7 pass images (their
	// disjoint pixels total <= original pixels), and two rows for every pass.
	// JPEG: padded planes and progressive [64]int32 coefficients per MCU.
	// 1 MiB additionally covers decoder/zlib/encoder state, 16 KiB preflight,
	// 32 KiB staging hash/copy buffers and bounded 128-entry directory batches.
	total, ok := checkedSum(sourceBytes, decoded, rows, coefficients, thumbnail, encoded, 1<<20)
	if !ok {
		return 0, domain.ErrImageTooLarge
	}
	return total, nil
}

func targetSize(width, height int, request domain.ImageRequest, maximum int) (int, int) {
	w, h := min(maximum, width), min(maximum, height)
	if request.Width > 0 {
		w = min(w, request.Width)
	}
	if request.Height > 0 {
		h = min(h, request.Height)
	}
	if int64(width)*int64(h) > int64(height)*int64(w) {
		h = max(1, int(int64(height)*int64(w)/int64(width)))
	} else {
		w = max(1, int(int64(width)*int64(h)/int64(height)))
	}
	return w, h
}

func decodeImage(ctx context.Context, source io.ReadSeeker, input inspectedImage) (image.Image, error) {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader := contextImageReader{ctx, source}
	if input.format == "png" {
		return png.Decode(reader)
	}
	return jpeg.Decode(reader)
}
