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
	"runtime/debug"
	"runtime/metrics"
	"strconv"
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
	Budget             app.WorkBudget
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
	// Store optionally persists originals and variants (G40.5). Index mirrors
	// variants in the database and drives their eviction; it needs Store. The
	// processor never closes either; their owner does after Shutdown.
	Store *Store
	Index app.ImageVariantIndex
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
		o.MaxOutputBytes < o.MaxImageBytes && int64(o.MaxConcurrent)*o.MaxImageBytes+o.CacheBytes <= 1<<30 &&
		(o.Index == nil || o.Store != nil)
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
	// Persistent store: variant hits, failed store or index writes (requests
	// still succeed) and variants removed through the index.
	VariantHits, StoreFailures, IndexFailures, IndexEvictions uint64
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
	variantHits atomic.Uint64
	// Store or index writes that failed; the request itself still succeeded.
	storeFailures  atomic.Uint64
	indexFailures  atomic.Uint64
	indexEvictions atomic.Uint64
	evictWake      chan struct{}
	evictDone      chan struct{}
	// A private seam lets lifecycle tests stop inside a synchronous decode.
	// Production always uses the pinned decoders in decodeImage.
	decode  func(context.Context, io.ReadSeeker, inspectedImage) (image.Image, error)
	reclaim func()
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
	p := &Processor{options: options, lifetime: lifetime, cancel: cancel,
		cache: newImageCache(options.CacheBytes, options.CacheEntries, options.CacheTTL),
		done:  make(chan struct{}), decode: decodeImage, evictDone: make(chan struct{}),
		reclaim: func() { reclaimImageMemory(int64(options.MaxConcurrent)*options.MaxImageBytes + options.CacheBytes) }}
	if options.Index != nil {
		p.evictWake = make(chan struct{}, 1)
		go p.evictLoop()
	} else {
		close(p.evictDone)
	}
	return p, nil
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
	// Eviction uses the database index; it must stop before the pool closes.
	for _, done := range []chan struct{}{p.done, p.evictDone} {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
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
	result.VariantHits, result.StoreFailures = p.variantHits.Load(), p.storeFailures.Load()
	result.IndexFailures, result.IndexEvictions = p.indexFailures.Load(), p.indexEvictions.Load()
	return result
}

// StoreStats reports the persistent store's aggregate counts. ok is false
// when the processor runs without a store; that never changes after New.
func (p *Processor) StoreStats() (stats StoreStats, ok bool) {
	if p == nil || p.options.Store == nil {
		return StoreStats{}, false
	}
	return p.options.Store.Stats(), true
}

// OriginalStore returns the persistent original store, or nil when the
// processor runs without one. Embedded cover extraction (G40.4) writes the
// originals it copies there; the processor renders them like fetched images.
func (p *Processor) OriginalStore() *Store {
	if p == nil {
		return nil
	}
	return p.options.Store
}

func (p *Processor) Render(ctx context.Context, source domain.LocalImageSource, request domain.ImageRequest) (result app.ImageResult, err error) {
	if p == nil || ctx == nil {
		return result, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	request, err = domain.NormalizeImageRequest(request)
	if err != nil || request.Type != "Primary" || request.Index != 0 {
		return result, domain.ErrInvalid
	}
	return p.render(ctx, request, source.RootPath, func(operation context.Context, limit int64) (renderSource, error) {
		staged, err := stageLocalPrimary(operation, source, p.options.TempRoot, limit)
		if err != nil {
			return nil, err
		}
		return staged, nil
	})
}

// RenderItemImage renders one authorized item_images row. Local and NFO file
// rows are read from the library like the Primary poster. Rows that name no
// image file (remote, embedded) are served only from bytes already in the
// persistent store; nothing is fetched or extracted on the request path, and
// a row without stored bytes is ErrNotFound so the caller can fall back.
func (p *Processor) RenderItemImage(ctx context.Context, value domain.ItemImage, request domain.ImageRequest) (result app.ImageResult, err error) {
	if p == nil || ctx == nil {
		return result, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	request, err = domain.NormalizeImageRequest(request)
	if err != nil || request.Type != value.Type || request.Index != value.Index {
		return result, domain.ErrInvalid
	}
	if itemImageFile(value) {
		if !validItemImageFile(value) {
			return result, domain.ErrNotFound
		}
		return p.render(ctx, request, value.RootPath, func(operation context.Context, limit int64) (renderSource, error) {
			staged, err := stageItemImage(operation, value, p.options.TempRoot, limit)
			if err != nil {
				return nil, err
			}
			return staged, nil
		})
	}
	if p.options.Store == nil || value.Content == nil || len(value.Content.SHA256) != sha256.Size {
		return result, domain.ErrNotFound
	}
	var digest [32]byte
	copy(digest[:], value.Content.SHA256)
	return p.render(ctx, request, "", func(context.Context, int64) (renderSource, error) {
		return &storedOriginal{store: p.options.Store, digest: digest}, nil
	})
}

// renderSource is one original prepared for rendering. Key identifies the
// in-memory cache entry, Content is the digest of the original bytes and
// names the store bucket. Open is called only on a cache miss.
type renderSource interface {
	Key() [32]byte
	Content() [32]byte
	Open(ctx context.Context, limit int64) (io.ReadSeeker, int64, error)
	// Verify binds the decoded bytes to the current source before delivery.
	Verify(context.Context) error
	// Persist stores the original in the persistent store if it is not there.
	Persist(context.Context, *Store) error
	Close() error
}

func (s *stagedImage) Content() [32]byte { return s.content }

func (s *stagedImage) Open(_ context.Context, _ int64) (io.ReadSeeker, int64, error) {
	return s.Reader(), s.size, nil
}

func (s *stagedImage) Persist(ctx context.Context, store *Store) error {
	if store.HasOriginal(s.content) {
		return nil
	}
	reader := s.Reader()
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return err
	}
	digest, _, err := store.PutOriginal(ctx, reader, s.size)
	if err == nil && digest != s.content {
		err = domain.ErrImageUnavailable
	}
	return err
}

// storedOriginal reads a content-addressed original from the store. The
// digest is rechecked when the bytes are opened for decoding.
type storedOriginal struct {
	store  *Store
	digest [32]byte
	object *StoreObject
}

func (s *storedOriginal) Key() [32]byte {
	key := sha256.New()
	imageHashString(key, "stored-original-v1")
	_, _ = key.Write(s.digest[:])
	var value [32]byte
	copy(value[:], key.Sum(nil))
	return value
}

func (s *storedOriginal) Content() [32]byte { return s.digest }

func (s *storedOriginal) Open(ctx context.Context, limit int64) (io.ReadSeeker, int64, error) {
	if s.object == nil {
		object, err := s.store.OpenOriginal(ctx, s.digest, true)
		if err != nil {
			return nil, 0, err
		}
		s.object = object
	}
	if s.object.Size() > limit {
		return nil, 0, domain.ErrImageTooLarge
	}
	return storeContextReadSeeker{ctx: ctx, object: s.object}, s.object.Size(), nil
}

func (*storedOriginal) Verify(context.Context) error          { return nil }
func (*storedOriginal) Persist(context.Context, *Store) error { return nil }

func (s *storedOriginal) Close() error {
	if s.object == nil {
		return nil
	}
	return s.object.Close()
}

type storeContextReadSeeker struct {
	ctx    context.Context
	object *StoreObject
}

func (r storeContextReadSeeker) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.object.Read(data)
}

func (r storeContextReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.object.Seek(offset, whence)
}

// render runs admission, the memory cache, the optional persistent variant
// store and the bounded decoder for one prepared source. mediaRoot is the
// library root the source is read from, or empty for stored originals.
func (p *Processor) render(ctx context.Context, request domain.ImageRequest, mediaRoot string, prepare func(context.Context, int64) (renderSource, error)) (result app.ImageResult, err error) {
	if request.Quality == 0 {
		request.Quality = p.options.DefaultQuality
	}
	if err = p.admit(); err != nil {
		return result, err
	}
	operation, cancel := context.WithTimeout(ctx, p.options.Timeout)
	stopLifetime := context.AfterFunc(p.lifetime, cancel) //nolint:contextcheck // the processor lifetime also cancels the request-derived operation context
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
	store := p.options.Store
	// The store must never sit inside a library root or contain one; roots
	// can be added after startup, so check the one this request reads.
	if store != nil && mediaRoot != "" && store.CheckMediaRoot(mediaRoot) != nil {
		return result, domain.ErrImageUnavailable
	}
	// Shared permits cover active processing, while the existing image memory
	// reservation remains held until the response body closes.
	var sharedRelease func()
	defer func() {
		if sharedRelease != nil {
			sharedRelease()
		}
	}()
	dropShared := func() { sharedRelease(); sharedRelease = nil }
	sharedRelease, err = p.acquireWork(operation, app.WorkIO)
	if err != nil {
		return result, err
	}
	source, err := prepare(operation, stageLimit)
	if err != nil {
		return result, imageError(operation, err)
	}
	dropShared()
	sourceClosed := false
	defer func() {
		if !sourceClosed {
			if closeErr := source.Close(); closeErr != nil {
				err = domain.ErrImageUnavailable
			}
		}
	}()
	content := source.Content()
	variant := p.variantKey(request)
	key := imageCacheKey(source.Key(), request)
	value, hit := p.cache.get(key)
	stored := false
	if hit {
		p.hits.Add(1)
	} else {
		p.misses.Add(1)
		if store != nil {
			sharedRelease, err = p.acquireWork(operation, app.WorkIO)
			if err != nil {
				return result, err
			}
			value, stored, err = p.readVariant(operation, store, content, variant)
			if err != nil {
				return result, err
			}
			dropShared()
		}
	}
	if !hit && !stored {
		sharedRelease, err = p.acquireWork(operation, app.WorkCPU)
		if err != nil {
			return result, err
		}
		reader, size, openErr := source.Open(operation, stageLimit)
		if openErr != nil {
			return result, imageError(operation, openErr)
		}
		inspected, inspectErr := inspectImage(operation, reader)
		if inspectErr != nil {
			return result, imageError(operation, inspectErr)
		}
		displayWidth, displayHeight := inspected.displaySize()
		width, height := targetSize(displayWidth, displayHeight, request, p.options.MaxOutputDimension)
		estimate, estimateErr := estimateImageBytes(inspected, width, height, size, p.options.MaxOutputBytes)
		if estimateErr != nil || estimate > p.options.MaxImageBytes {
			return result, domain.ErrImageTooLarge
		}
		p.mu.Lock()
		p.maxEstimate = max(p.maxEstimate, estimate)
		p.mu.Unlock()
		p.decodes.Add(1)
		value, err = p.renderDecoded(operation, reader, inspected, width, height, request.Quality)
		// Large decoder objects are now outside the active stack frame. GC can
		// collect them and return their pages before another request reuses this
		// reservation. A GC alone may leave hundreds of MiB resident. Reclaim
		// only under pressure; keep the CPU permit and image slot through it, even
		// after a failed or cancelled decode; never abandon it in a goroutine.
		if estimate >= min(int64(64<<20), p.options.MaxImageBytes/2) {
			p.reclaim()
		}
		if err != nil {
			return result, imageError(operation, err)
		}
	}
	if sharedRelease != nil {
		dropShared()
	}
	sharedRelease, err = p.acquireWork(operation, app.WorkIO)
	if err != nil {
		return result, err
	}
	if err := source.Verify(operation); err != nil {
		return result, imageError(operation, err)
	}
	if err := operation.Err(); err != nil {
		return result, err
	}
	if store != nil && !hit && !stored {
		// Best effort: a failed write leaves a miss that the next request
		// repeats; it never fails a representation that is already verified.
		p.persist(operation, store, source, content, variant, value.data)
		if err := operation.Err(); err != nil {
			return result, err
		}
	}
	if err := source.Close(); err != nil {
		return result, domain.ErrImageUnavailable
	}
	sourceClosed = true
	dropShared()
	if !hit {
		p.cache.put(key, value)
	}
	if err := operation.Err(); err != nil {
		return result, err
	}
	result = app.ImageResult{Body: &imageBody{ctx: operation, reader: bytes.NewReader(value.data), release: release},
		ContentType: "image/jpeg", ETag: value.etag, Size: int64(len(value.data)), Width: value.width, Height: value.height, ContentSHA256: content}
	p.completed.Add(1)
	success = true
	return result, nil
}

// variantKey binds every output parameter, including the configured output
// bound, which changes the result for the same request. Bump the pipeline
// version whenever decoding or encoding output changes.
func (p *Processor) variantKey(request domain.ImageRequest) [32]byte {
	return StoreVariantKey("fit-v1/max="+strconv.Itoa(p.options.MaxOutputDimension), "jpeg", request.Width, request.Height, request.Quality)
}

// readVariant returns a stored representation. A missing, corrupt or
// implausible object is a miss; only cancellation is an error.
func (p *Processor) readVariant(ctx context.Context, store *Store, content, variant [32]byte) (encodedImage, bool, error) {
	object, err := store.OpenVariant(ctx, content, variant)
	if err != nil {
		if ctx.Err() != nil {
			return encodedImage{}, false, ctx.Err()
		}
		if !errors.Is(err, domain.ErrNotFound) {
			p.storeFailures.Add(1)
		}
		return encodedImage{}, false, nil
	}
	defer object.Close()
	size := object.Size()
	if size < 1 || size > p.options.MaxOutputBytes {
		p.storeFailures.Add(1)
		return encodedImage{}, false, nil
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(storeContextReadSeeker{ctx: ctx, object: object}, data); err != nil {
		if ctx.Err() != nil {
			return encodedImage{}, false, ctx.Err()
		}
		p.storeFailures.Add(1)
		return encodedImage{}, false, nil
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > p.options.MaxOutputDimension || config.Height > p.options.MaxOutputDimension {
		p.storeFailures.Add(1)
		return encodedImage{}, false, nil
	}
	p.variantHits.Add(1)
	if index := p.options.Index; index != nil {
		// Keep the database recency in step with the store; an entry the index
		// lost (a failed write or a rebuilt store) is added back.
		err := index.TouchImageVariant(ctx, content, variant)
		if errors.Is(err, domain.ErrNotFound) {
			err = index.PutImageVariant(ctx, content, variant, size)
		}
		if err != nil {
			if ctx.Err() != nil {
				return encodedImage{}, false, ctx.Err()
			}
			p.indexFailures.Add(1)
		}
	}
	digest := sha256.Sum256(data)
	return encodedImage{data: data, width: config.Width, height: config.Height, etag: `"` + hex.EncodeToString(digest[:]) + `"`}, true, nil
}

// persist writes the original (once per content digest), the variant and
// its index row, then wakes eviction. The index row follows the file, so an
// index entry never names a variant that was not written.
func (p *Processor) persist(ctx context.Context, store *Store, source renderSource, content, variant [32]byte, data []byte) {
	if err := source.Persist(ctx, store); err != nil {
		p.storeFailures.Add(1)
	}
	if _, err := store.PutVariant(ctx, content, variant, bytes.NewReader(data), int64(len(data))); err != nil {
		p.storeFailures.Add(1)
		return
	}
	if index := p.options.Index; index != nil {
		if err := index.PutImageVariant(ctx, content, variant, int64(len(data))); err != nil {
			p.indexFailures.Add(1)
		}
		p.wakeEviction()
	}
}

// No decoded bitmap escapes this frame. Only the tightly sized JPEG is retained
// by the cache or response body. Keeping this frame separate also makes large
// decoder allocations collectible before Render releases its processing slot.
func (p *Processor) renderDecoded(ctx context.Context, source io.ReadSeeker, input inspectedImage, width, height, quality int) (encodedImage, error) {
	decoded, err := p.decode(ctx, source, input)
	if err != nil {
		return encodedImage{}, err
	}
	if err := ctx.Err(); err != nil {
		return encodedImage{}, err
	}
	if decoded.Bounds() != image.Rect(0, 0, input.width, input.height) {
		return encodedImage{}, domain.ErrImageUnavailable
	}
	var encodeInput image.Image
	if width == input.width && height == input.height && !input.oriented() {
		encodeInput, err = sameSizeJPEGImage(ctx, decoded)
		if err != nil {
			return encodedImage{}, err
		}
	} else {
		// An EXIF-oriented image always takes this path, even at full size:
		// the output RGBA is the only bitmap besides the decoded one, and the
		// estimate already reserves it as the output-sized thumbnail.
		thumbnail := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.Draw(thumbnail, thumbnail.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		if input.oriented() {
			// The affine map rotates or mirrors while it scales, writing
			// straight into the output; no rotated source copy exists.
			draw.ApproxBiLinear.Transform(thumbnail, orientationTransform(input.orientation, input.width, input.height, width, height), decoded, decoded.Bounds(), draw.Over, nil)
		} else {
			// ApproxBiLinear has no source-sized intermediate kernel buffer.
			draw.ApproxBiLinear.Scale(thumbnail, thumbnail.Bounds(), decoded, decoded.Bounds(), draw.Over, nil)
		}
		encodeInput = thumbnail
	}
	if err := ctx.Err(); err != nil {
		return encodedImage{}, err
	}
	output := boundedImageWriter{ctx: ctx, limit: int(p.options.MaxOutputBytes)}
	if err := jpeg.Encode(&output, encodeInput, &jpeg.Options{Quality: quality}); err != nil {
		return encodedImage{}, err
	}
	// Tighten capacity once. Both allocations are included in the estimate;
	// a small cached JPEG does not pin the entire output-byte allowance.
	encoded := make([]byte, len(output.data))
	copy(encoded, output.data)
	digest := sha256.Sum256(encoded)
	return encodedImage{data: encoded, width: width, height: height, etag: `"` + hex.EncodeToString(digest[:]) + `"`}, nil
}

// Large allocations can leave resident heap pages after their bitmaps die.
// Compare all Go-managed, unreleased memory with this processor's existing
// aggregate reservation. Below it, ordinary GC/scavenging can keep working;
// above it, synchronous reclamation runs before another image reuses a slot.
// This changes neither GOGC/GOMEMLIMIT nor the configured image concurrency.
func reclaimImageMemory(reservation int64) {
	samples := []metrics.Sample{{Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	metrics.Read(samples)
	if samples[0].Value.Kind() != metrics.KindUint64 || samples[1].Value.Kind() != metrics.KindUint64 {
		debug.FreeOSMemory()
		return
	}
	total, released := samples[0].Value.Uint64(), samples[1].Value.Uint64()
	if total < released || total-released >= uint64(reservation) {
		debug.FreeOSMemory()
	}
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
	// EXIF/TIFF orientation 1-8; zero means none was declared.
	orientation int
	// WebP: VP8L stream, and ALPH chunk kind (webpAlphaRaw/webpAlphaLossless).
	lossless bool
	alpha    uint8
	// BMP/TIFF decoded bytes per pixel; BMP row buffer; TIFF strip or tile
	// pixels, block count and first-IFD entry count.
	pixelBytes, rowBytes, blockPixels, blocks, entries int64
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
	if format := sniffAdditionalFormat(header[:n]); format != "" {
		return inspectAdditionalFormat(ctx, source, format)
	}
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
	inScan, sawScan, sawExif := false, false, false
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
		if marker == 0xe1 && !sawExif && kept >= 6 && string(segment[:6]) == "Exif\x00\x00" {
			// Only the first Exif APP1 counts. A segment is at most 64 KiB and
			// every IFD offset in it is bounds-checked; nothing is followed
			// outside the segment.
			sawExif = true
			exif := make([]byte, size)
			copy(exif, segment[:kept])
			if _, err := io.ReadFull(r, exif[kept:]); err != nil {
				return inspectedImage{}, err
			}
			result.orientation = exifOrientation(exif[6:])
		} else if _, err := r.Discard(size - kept); err != nil {
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
	case "webp", "gif", "bmp", "tiff":
		decoded, ok = estimateAdditionalFormatBytes(input, sourceBytes)
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
	// WebP/GIF/BMP/TIFF: see estimateAdditionalFormatBytes.
	// 1 MiB additionally covers decoder/zlib/encoder state, 16 KiB preflight
	// (plus at most one 64 KiB JPEG Exif segment, released before decoding),
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
	switch input.format {
	case "png":
		return png.Decode(reader)
	case "jpeg":
		return jpeg.Decode(reader)
	}
	return decodeAdditionalFormat(ctx, source, input)
}

func (p *Processor) acquireWork(ctx context.Context, class app.WorkClass) (func(), error) {
	if p.options.Budget == nil {
		return func() {}, ctx.Err()
	}
	release, err := p.options.Budget.Acquire(ctx, class)
	if errors.Is(err, domain.ErrResourceBusy) {
		p.busy.Add(1)
		return nil, domain.ErrImageBusy
	}
	if err != nil {
		return nil, imageError(ctx, err)
	}
	return release, nil
}
