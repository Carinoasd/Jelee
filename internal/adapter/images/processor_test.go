package images

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func processorTestOptions(scratch string) Options {
	return Options{TempRoot: scratch, MaxConcurrent: 2, MaxImageBytes: 96 << 20, MaxSourceBytes: 16 << 20,
		MaxOutputBytes: 2 << 20, CacheBytes: 32 << 20, MaxOutputDimension: 1024, CacheEntries: 128,
		DefaultQuality: 85, Timeout: 15 * time.Second, CacheTTL: 300 * time.Second}
}

func processorTestNew(t *testing.T, options Options) *Processor {
	t.Helper()
	p, err := New(context.Background(), options)
	if err != nil {
		t.Fatal("create image processor", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Shutdown(ctx); err != nil {
			t.Error("image processor did not stop", err)
		}
	})
	return p
}

func processorTestPNG(t *testing.T, source domain.LocalImageSource, pixel color.NRGBA) string {
	t.Helper()
	input := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	for y := range 32 {
		for x := range 64 {
			input.SetNRGBA(x, y, pixel)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, input); err != nil {
		t.Fatal("encode fixture")
	}
	path := filepath.Join(source.RootPath, "movie", "poster.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal("write image fixture")
	}
	return path
}

func TestImageProcessorPNGResizeWhiteAlphaAndCache(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	processorTestPNG(t, source, color.NRGBA{R: 255, A: 0})
	p := processorTestNew(t, processorTestOptions(scratch))
	request := domain.ImageRequest{Width: 16, Height: 16}
	first, err := p.Render(context.Background(), source, request)
	if err != nil {
		t.Fatal("render PNG", err)
	}
	defer first.Body.Close()
	data, err := io.ReadAll(first.Body)
	if err != nil || first.Width != 16 || first.Height != 8 || first.ContentType != "image/jpeg" || first.Size != int64(len(data)) {
		t.Fatal("wrong thumbnail response")
	}
	digest := sha256.Sum256(data)
	if first.ETag != `"`+hex.EncodeToString(digest[:])+`"` {
		t.Fatal("ETag is not a strong content digest")
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal("response is not JPEG")
	}
	r, g, b, _ := decoded.At(8, 4).RGBA()
	if r < 65000 || g < 65000 || b < 65000 {
		t.Fatal("transparent source was not composited over white")
	}
	second, err := p.Render(context.Background(), source, request)
	if err != nil {
		t.Fatal("cached render", err)
	}
	defer second.Body.Close()
	cached, err := io.ReadAll(second.Body)
	if err != nil || !bytes.Equal(data, cached) || second.ETag != first.ETag {
		t.Fatal("cache changed encoded image")
	}
	if _, err := first.Body.Seek(0, io.SeekStart); err != nil {
		t.Fatal("body is not independently seekable")
	}
	if replay, err := io.ReadAll(first.Body); err != nil || !bytes.Equal(replay, data) {
		t.Fatal("cached reader shares read position")
	}
	stats := p.Stats()
	if stats.CacheHits != 1 || stats.Decodes != 1 || stats.Active != 2 || stats.ReservedBytes != 2*p.options.MaxImageBytes {
		t.Fatal("cache or reservation statistics are incorrect")
	}
	imageScratchEmpty(t, scratch)
}

func TestImageProcessorJPEGAndNoEnlargement(t *testing.T) {
	for _, gray := range []bool{false, true} {
		t.Run(map[bool]string{false: "ycbcr", true: "gray"}[gray], func(t *testing.T) {
			source, scratch := imageSourceFixture(t)
			var input image.Image = image.NewRGBA(image.Rect(0, 0, 9, 7))
			if gray {
				input = image.NewGray(image.Rect(0, 0, 9, 7))
			}
			var encoded bytes.Buffer
			if jpeg.Encode(&encoded, input, nil) != nil {
				t.Fatal("encode JPEG fixture")
			}
			imageWrite(t, filepath.Join(source.RootPath, "movie", "poster.jpg"), encoded.String())
			p := processorTestNew(t, processorTestOptions(scratch))
			result, err := p.Render(context.Background(), source, domain.ImageRequest{})
			if err != nil {
				t.Fatal("render JPEG", err)
			}
			defer result.Body.Close()
			if result.Width != 9 || result.Height != 7 {
				t.Fatal("small image was enlarged")
			}
		})
	}
}

func TestImageProcessorDecodesProgressiveJPEG(t *testing.T) {
	// A one-pixel grayscale JPEG: one DC scan and one AC scan, each using a
	// single zero/EOB Huffman symbol. No image-generation dependency is needed.
	data := []byte{0xff, 0xd8}
	segment := func(marker byte, payload []byte) {
		data = append(data, 0xff, marker, byte((len(payload)+2)>>8), byte(len(payload)+2))
		data = append(data, payload...)
	}
	quantization := make([]byte, 65)
	for index := 1; index < len(quantization); index++ {
		quantization[index] = 1
	}
	segment(0xdb, quantization)
	segment(0xc2, []byte{8, 0, 1, 0, 1, 1, 1, 0x11, 0})
	for _, table := range []byte{0, 0x10} {
		counts := make([]byte, 18)
		counts[0], counts[1] = table, 1
		segment(0xc4, counts)
	}
	segment(0xda, []byte{1, 1, 0, 0, 0, 0})
	data = append(data, 0x7f)
	segment(0xda, []byte{1, 1, 0, 1, 63, 0})
	data = append(data, 0x7f, 0xff, 0xd9)
	source, scratch := imageSourceFixture(t)
	imageWrite(t, filepath.Join(source.RootPath, "movie", "poster.jpg"), string(data))
	p := processorTestNew(t, processorTestOptions(scratch))
	result, err := p.Render(context.Background(), source, domain.ImageRequest{})
	if err != nil {
		t.Fatal("progressive JPEG failed", err)
	}
	defer result.Body.Close()
	decoded, err := jpeg.Decode(result.Body)
	if err != nil || decoded.Bounds() != image.Rect(0, 0, 1, 1) {
		t.Fatal("progressive JPEG response invalid")
	}
}

func TestImageProcessorDecodesPNG16AndAdam7(t *testing.T) {
	for _, interlaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "rgba16", true: "adam7-rgba16"}[interlaced], func(t *testing.T) {
			// Fixed RGBA16 pixels make all seven pass layouts explicit. The
			// compressed fixture remains tiny while invoking the real decoder.
			passes := [][4]int{{0, 0, 1, 1}}
			if interlaced {
				passes = [][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}}
			}
			var compressed bytes.Buffer
			compressor := zlib.NewWriter(&compressed)
			for _, pass := range passes {
				for y := pass[1]; y < 8; y += pass[3] {
					compressor.Write([]byte{0})
					for x := pass[0]; x < 8; x += pass[2] {
						compressor.Write([]byte{255, 255, 0, 0, 0, 0, 255, 255})
					}
				}
			}
			if compressor.Close() != nil {
				t.Fatal("compress PNG fixture")
			}
			data := []byte("\x89PNG\r\n\x1a\n")
			chunk := func(kind string, payload []byte) {
				var length [4]byte
				binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
				data = append(data, length[:]...)
				start := len(data)
				data = append(data, kind...)
				data = append(data, payload...)
				binary.BigEndian.PutUint32(length[:], crc32.ChecksumIEEE(data[start:]))
				data = append(data, length[:]...)
			}
			header := []byte{0, 0, 0, 8, 0, 0, 0, 8, 16, 6, 0, 0, 0}
			if interlaced {
				header[12] = 1
			}
			chunk("IHDR", header)
			chunk("IDAT", compressed.Bytes())
			chunk("IEND", nil)
			source, scratch := imageSourceFixture(t)
			imageWrite(t, filepath.Join(source.RootPath, "movie", "poster.png"), string(data))
			p := processorTestNew(t, processorTestOptions(scratch))
			result, err := p.Render(context.Background(), source, domain.ImageRequest{Width: 4})
			if err != nil {
				t.Fatal("16-bit PNG failed", err)
			}
			defer result.Body.Close()
			decoded, err := jpeg.Decode(result.Body)
			if err != nil || decoded.Bounds() != image.Rect(0, 0, 4, 4) {
				t.Fatal("16-bit PNG response invalid")
			}
			r, g, b, _ := decoded.At(2, 2).RGBA()
			if r < 64000 || g > 1000 || b > 1000 {
				t.Fatal("16-bit PNG colors changed")
			}
		})
	}
}

func TestImageProcessorRevalidatesSourceAndInvalidatesContent(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	path := processorTestPNG(t, source, color.NRGBA{R: 255, A: 255})
	p := processorTestNew(t, processorTestOptions(scratch))
	first, err := p.Render(context.Background(), source, domain.ImageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	stamp := time.Unix(1600000000, 0)
	if os.Chtimes(path, stamp, stamp) != nil {
		t.Fatal("set fixture timestamp")
	}
	processorTestPNG(t, source, color.NRGBA{B: 255, A: 255})
	if os.Chtimes(path, stamp, stamp) != nil {
		t.Fatal("restore fixture timestamp")
	}
	second, err := p.Render(context.Background(), source, domain.ImageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.ETag == first.ETag || p.Stats().Decodes != 2 {
		t.Fatal("modified image reused stale cache")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal("remove owned poster")
	}
	if result, err := p.Render(context.Background(), source, domain.ImageRequest{}); err != domain.ErrNotFound || result.Body != nil {
		t.Fatal("cache bypassed source existence")
	}
}

func TestImageProcessorRejectsSourceChangedDuringDecode(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	path := processorTestPNG(t, source, color.NRGBA{R: 255, A: 255})
	p := processorTestNew(t, processorTestOptions(scratch))
	p.decode = func(ctx context.Context, reader io.ReadSeeker, input inspectedImage) (image.Image, error) {
		decoded, err := decodeImage(ctx, reader, input)
		imageWrite(t, path, "changed during synchronous decode")
		return decoded, err
	}
	if result, err := p.Render(context.Background(), source, domain.ImageRequest{}); err != domain.ErrImageUnavailable || result.Body != nil {
		t.Fatal("response accepted a replaced source", err)
	}
	if stats := p.Stats(); stats.Active != 0 || stats.CacheEntries != 0 {
		t.Fatal("failed source verification retained resources")
	}
	imageScratchEmpty(t, scratch)
}

func TestImageProcessorSlotHeldUntilBodyClose(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	processorTestPNG(t, source, color.NRGBA{A: 255})
	options := processorTestOptions(scratch)
	options.MaxConcurrent = 1
	p := processorTestNew(t, options)
	result, err := p.Render(context.Background(), source, domain.ImageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	if _, err := io.ReadAll(result.Body); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Render(context.Background(), source, domain.ImageRequest{}); err != domain.ErrImageBusy {
		t.Fatal("EOF released response reservation")
	}
	if result.Body.Close() != nil || result.Body.Close() != nil {
		t.Fatal("close is not idempotent")
	}
	if _, err := result.Body.Read(make([]byte, 1)); err != io.ErrClosedPipe {
		t.Fatal("closed body remained readable")
	}
	if _, err := result.Body.Seek(0, io.SeekStart); err != io.ErrClosedPipe {
		t.Fatal("closed body remained seekable")
	}
	next, err := p.Render(context.Background(), source, domain.ImageRequest{})
	if err != nil {
		t.Fatal("close did not release slot", err)
	}
	next.Body.Close()
	if stats := p.Stats(); stats.Active != 0 || stats.Busy != 1 {
		t.Fatal("incorrect slot accounting")
	}
}

func TestImageProcessorCancellationDoesNotAbandonDecode(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	processorTestPNG(t, source, color.NRGBA{A: 255})
	options := processorTestOptions(scratch)
	options.MaxConcurrent = 1
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
	options.Budget = budget
	p := processorTestNew(t, options)
	entered, unblock, finished := make(chan context.Context, 1), make(chan struct{}), make(chan error, 1)
	p.decode = func(ctx context.Context, _ io.ReadSeeker, _ inspectedImage) (image.Image, error) {
		entered <- ctx
		<-unblock
		return image.NewRGBA(image.Rect(0, 0, 64, 32)), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		result, err := p.Render(ctx, source, domain.ImageRequest{})
		if result.Body != nil {
			result.Body.Close()
		}
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(unblock)
		t.Fatal("decode did not begin")
	}
	cancel()
	if p.Stats().Active != 1 || budget.Stats() != (resources.Stats{CPU: 1, Total: 1}) {
		close(unblock)
		t.Fatal("cancellation released an executing decode")
	}
	if _, err := p.Render(context.Background(), source, domain.ImageRequest{}); err != domain.ErrImageBusy {
		close(unblock)
		t.Fatal("another decode entered before cancellation completed")
	}
	select {
	case <-finished:
		close(unblock)
		t.Fatal("render abandoned synchronous decode")
	default:
	}
	close(unblock)
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation did not win", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("decode did not join")
	}
	if p.Stats().Active != 0 || budget.Stats() != (resources.Stats{}) {
		t.Fatal("finished cancellation retained slot")
	}
	imageScratchEmpty(t, scratch)
}

func TestImageProcessorTimeoutKeepsReservationUntilDecodeReturns(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	processorTestPNG(t, source, color.NRGBA{A: 255})
	options := processorTestOptions(scratch)
	options.Timeout = time.Second
	options.MaxConcurrent = 1
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
	options.Budget = budget
	p := processorTestNew(t, options)
	timedOut, unblock, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	p.decode = func(ctx context.Context, _ io.ReadSeeker, _ inspectedImage) (image.Image, error) {
		<-ctx.Done()
		close(timedOut)
		<-unblock
		return image.NewRGBA(image.Rect(0, 0, 64, 32)), nil
	}
	go func() {
		result, err := p.Render(context.Background(), source, domain.ImageRequest{})
		if result.Body != nil {
			result.Body.Close()
		}
		finished <- err
	}()
	select {
	case <-timedOut:
	case <-time.After(5 * time.Second):
		close(unblock)
		t.Fatal("operation timeout did not reach decoder")
	}
	if p.Stats().Active != 1 || budget.Stats() != (resources.Stats{CPU: 1, Total: 1}) {
		close(unblock)
		t.Fatal("timeout prematurely released decoder")
	}
	if _, err := p.Render(context.Background(), source, domain.ImageRequest{}); err != domain.ErrImageBusy {
		close(unblock)
		t.Fatal("timeout allowed overlapping decoder")
	}
	close(unblock)
	select {
	case err := <-finished:
		if err != context.DeadlineExceeded {
			t.Fatal("timeout result lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed-out decoder did not finish")
	}
	if p.Stats().Active != 0 || budget.Stats() != (resources.Stats{}) {
		t.Fatal("joined timeout retained reservation")
	}
}

func TestImageProcessorShutdownWaitsForOwnedBody(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	processorTestPNG(t, source, color.NRGBA{A: 255})
	p := processorTestNew(t, processorTestOptions(scratch))
	result, err := p.Render(context.Background(), source, domain.ImageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Shutdown(ctx); err != context.Canceled {
		t.Fatal("shutdown did not wait for live body")
	}
	if stats := p.Stats(); stats.Active != 1 || stats.CacheBytes != 0 {
		t.Fatal("shutdown lost live body or retained cache")
	}
	if _, err := p.Render(context.Background(), source, domain.ImageRequest{}); err != domain.ErrImageUnavailable {
		t.Fatal("shutdown allowed new render")
	}
	result.Body.Close()
	if err := p.Shutdown(context.Background()); err != nil || p.Stats().Active != 0 {
		t.Fatal("shutdown failed after body close")
	}
}

func TestImageProcessorEvictionKeepsResponseAndVariantsSeparate(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	processorTestPNG(t, source, color.NRGBA{R: 255, A: 255})
	options := processorTestOptions(scratch)
	options.CacheEntries = 1
	p := processorTestNew(t, options)
	first, err := p.Render(context.Background(), source, domain.ImageRequest{Width: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	second, err := p.Render(context.Background(), source, domain.ImageRequest{Width: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if first.Width != 16 || second.Width != 8 || first.ETag == second.ETag || p.Stats().Decodes != 2 {
		t.Fatal("request dimensions collided in cache")
	}
	if data, err := io.ReadAll(first.Body); err != nil || int64(len(data)) != first.Size {
		t.Fatal("eviction invalidated active response")
	}
	if stats := p.Stats(); stats.CacheEntries != 1 || stats.Active != 2 {
		t.Fatal("evicted response lost memory reservation")
	}
}

func TestImageProcessorRejectsBudgetBeforeDecode(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	path := processorTestPNG(t, source, color.NRGBA{A: 255})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read image fixture")
	}
	binary.BigEndian.PutUint32(data[16:20], 6000)
	binary.BigEndian.PutUint32(data[20:24], 6000)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	if os.WriteFile(path, data, 0600) != nil {
		t.Fatal("write large-dimension fixture")
	}
	options := processorTestOptions(scratch)
	options.MaxImageBytes = 16 << 20
	p := processorTestNew(t, options)
	p.decode = func(context.Context, io.ReadSeeker, inspectedImage) (image.Image, error) {
		t.Error("over-budget image reached decoder")
		return nil, errors.New("unreachable")
	}
	if result, err := p.Render(context.Background(), source, domain.ImageRequest{}); err != domain.ErrImageTooLarge || result.Body != nil {
		t.Fatal("oversized image did not fail before decode", err)
	}
	if p.Stats().Active != 0 {
		t.Fatal("budget rejection leaked a slot")
	}
	imageScratchEmpty(t, scratch)
}

func TestImageProcessorErrorsAreBoundedAndReleaseResources(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	path := filepath.Join(source.RootPath, "movie", "poster.jpg")
	imageWrite(t, path, "private malformed image")
	p := processorTestNew(t, processorTestOptions(scratch))
	if result, err := p.Render(context.Background(), source, domain.ImageRequest{}); err != domain.ErrImageUnsupported || result.Body != nil {
		t.Fatal("unsupported format error leaked codec details")
	}
	processorTestPNG(t, source, color.NRGBA{A: 255})
	if os.Remove(path) != nil {
		t.Fatal("remove malformed fixture")
	}
	p.decode = func(context.Context, io.ReadSeeker, inspectedImage) (image.Image, error) {
		return nil, errors.New("PRIVATE secret host path")
	}
	if result, err := p.Render(context.Background(), source, domain.ImageRequest{}); err != domain.ErrImageUnavailable || result.Body != nil {
		t.Fatal("decoder error was not redacted")
	}
	if p.Stats().Active != 0 {
		t.Fatal("failed render retained reservation")
	}
	imageScratchEmpty(t, scratch)
}

func TestImageProcessorOptionsAndRequestBounds(t *testing.T) {
	_, scratch := imageSourceFixture(t)
	valid := processorTestOptions(scratch)
	for _, mutate := range []func(*Options){
		func(o *Options) { o.TempRoot = "relative" }, func(o *Options) { o.TempRoot += "\x00" },
		func(o *Options) { o.MaxConcurrent = 0 }, func(o *Options) { o.MaxConcurrent = 9 },
		func(o *Options) { o.MaxImageBytes = math.MaxInt64 }, func(o *Options) { o.MaxSourceBytes = 0 },
		func(o *Options) { o.MaxOutputBytes = 63 << 10 }, func(o *Options) { o.MaxOutputDimension = 2049 },
		func(o *Options) { o.CacheBytes = o.MaxOutputBytes - 1 }, func(o *Options) { o.CacheEntries = 0 },
		func(o *Options) { o.Timeout = 121 * time.Second }, func(o *Options) { o.CacheTTL = 0 },
		func(o *Options) { o.DefaultQuality = 101 }, func(o *Options) { o.MaxConcurrent = 8; o.MaxImageBytes = 256 << 20 },
	} {
		value := valid
		mutate(&value)
		if p, err := New(context.Background(), value); p != nil || err != domain.ErrInvalid {
			if p != nil {
				p.Shutdown(context.Background())
			}
			t.Fatal("invalid options accepted")
		}
	}
	if p, err := New(nil, valid); p != nil || err != domain.ErrInvalid {
		t.Fatal("nil lifetime accepted")
	}
	// IsAbs paths with legal mixed separators are normalized, not rejected.
	valid.TempRoot = filepath.ToSlash(scratch)
	p := processorTestNew(t, valid)
	if p.options.TempRoot != filepath.Clean(scratch) {
		t.Fatal("absolute temp root was not normalized")
	}
	for _, request := range []domain.ImageRequest{{Type: "Backdrop"}, {Format: "png"}, {Width: -1}, {Height: 2049}, {Quality: 101}} {
		if result, err := p.Render(context.Background(), domain.LocalImageSource{}, request); err != domain.ErrInvalid || result.Body != nil {
			t.Fatal("invalid request reached image source")
		}
	}
}

func TestImageMemoryEstimateIncludesProgressiveAndAdam7(t *testing.T) {
	baseline := inspectedImage{format: "jpeg", width: 16, height: 16, components: 3, maxH: 2, maxV: 2, sumHV: 6}
	plain, err := estimateImageBytes(baseline, 8, 8, 100, 1024)
	if err != nil {
		t.Fatal(err)
	}
	baseline.progressive = true
	progressive, err := estimateImageBytes(baseline, 8, 8, 100, 1024)
	if err != nil || progressive-plain != 6*64*4 {
		t.Fatal("progressive coefficient blocks not reserved")
	}
	pngImage := inspectedImage{format: "png", width: 16, height: 16}
	plain, err = estimateImageBytes(pngImage, 8, 8, 100, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pngImage.interlaced = true
	interlaced, err := estimateImageBytes(pngImage, 8, 8, 100, 1024)
	if err != nil || interlaced-plain != 8*16*16+12*(1+8*16) {
		t.Fatal("Adam7 pass images or row allocations omitted")
	}
	for _, input := range []inspectedImage{{format: "png", width: math.MaxInt, height: 2}, {format: "jpeg", width: math.MaxInt, height: 1, components: 3, maxH: 4, maxV: 4, sumHV: 48}} {
		if _, err := estimateImageBytes(input, 8, 8, 1, 1024); err != domain.ErrImageTooLarge {
			t.Fatal("overflow accepted")
		}
	}
	for _, pair := range [][2]int64{{math.MaxInt64, 1024}, {1, math.MaxInt64}} {
		if _, err := estimateImageBytes(pngImage, 8, 8, pair[0], pair[1]); err != domain.ErrImageTooLarge {
			t.Fatal("sum or encoded-buffer overflow accepted")
		}
	}
}

func TestImageBoundedOutputWriter(t *testing.T) {
	w := boundedImageWriter{ctx: context.Background(), limit: 4}
	if n, err := w.Write([]byte("1234")); n != 4 || err != nil {
		t.Fatal("exact output bound rejected")
	}
	if n, err := w.Write([]byte("5")); n != 0 || err != domain.ErrImageTooLarge || string(w.data) != "1234" {
		t.Fatal("overflow grew output allocation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.ctx = ctx
	if _, err := w.Write(nil); err != context.Canceled {
		t.Fatal("writer ignored cancellation")
	}
	if string(w.data) != "1234" {
		t.Fatal("cancelled write changed existing output")
	}
	growing := boundedImageWriter{ctx: context.Background(), limit: 10001}
	var expected bytes.Buffer
	for _, size := range []int{3, 4094, 4100, 1804} {
		chunk := bytes.Repeat([]byte{byte(size)}, size)
		expected.Write(chunk)
		if n, err := growing.Write(chunk); n != size || err != nil || cap(growing.data) > growing.limit || !bytes.Equal(growing.data, expected.Bytes()) {
			t.Fatal("bounded growth changed bytes or exceeded the limit")
		}
	}
	if _, err := growing.Write([]byte{1}); err != domain.ErrImageTooLarge || !bytes.Equal(growing.data, expected.Bytes()) {
		t.Fatal("overflow changed output at a non-power-of-two limit")
	}
}

func TestImageSmallThumbnailAllocationBudget(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	processorTestPNG(t, source, color.NRGBA{R: 255, A: 255})
	options := processorTestOptions(scratch)
	options.CacheEntries = 1
	measurement := testing.Benchmark(func(b *testing.B) {
		p, err := New(context.Background(), options)
		if err != nil {
			b.Fatal(err)
		}
		defer p.Shutdown(context.Background())
		for i := 0; i < b.N; i++ {
			// Alternating qualities evict the sole entry, so every render must
			// decode and encode instead of measuring cache hits.
			result, err := p.Render(context.Background(), source, domain.ImageRequest{Width: 16, Height: 16, Quality: 80 + i%2})
			if err != nil {
				b.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, result.Body); err != nil {
				b.Fatal(err)
			}
			if err := result.Body.Close(); err != nil {
				b.Fatal(err)
			}
		}
		if p.Stats().Decodes != uint64(b.N) {
			b.Fatal("allocation measurement included a cache hit")
		}
	})
	if measurement.N == 0 || measurement.AllocedBytesPerOp() == 0 {
		t.Fatal("small thumbnail allocation benchmark did not complete")
	}
	t.Logf("small thumbnail: %d bytes per cold render", measurement.AllocedBytesPerOp())
	if measurement.AllocedBytesPerOp() > 1<<20 {
		t.Fatal("small thumbnail allocated over 1 MiB per cold render")
	}
}

func jpegGateFixture(marker byte, ids []byte, extra []byte) []byte {
	data := []byte{0xff, 0xd8, 0xff, marker, 0, byte(8 + 3*len(ids)), 8, 0, 16, 0, 16, byte(len(ids))}
	for _, id := range ids {
		data = append(data, id, 0x11, 0)
	}
	data = append(data, 0xff, 0xda, 0, 2, 0x12, 0xff, 0, 0x34) // entropy stuffing is skipped, not decoded.
	data = append(data, extra...)
	return append(data, 0xff, 0xd9)
}

func TestImageJPEGMarkerGateInspectsLateColorDeclarations(t *testing.T) {
	adobe := []byte{0xff, 0xee, 0, 14, 'A', 'd', 'o', 'b', 'e', 0, 0, 0, 0, 0, 0, 0}
	app0 := []byte{0xff, 0xe0, 0, 7, 'o', 't', 'h', 'e', 'r'}
	for _, data := range [][]byte{
		jpegGateFixture(0xc0, []byte{'R', 'G', 'B'}, nil),
		jpegGateFixture(0xc0, []byte{1, 2, 3, 4}, nil),
		jpegGateFixture(0xc0, []byte{1, 2, 3}, adobe),
		jpegGateFixture(0xc2, []byte{1, 2, 3}, append(app0, adobe...)),
	} {
		if _, err := inspectJPEG(context.Background(), bytes.NewReader(data)); err != domain.ErrImageUnsupported {
			t.Fatal("RGB/CMYK declaration passed complete marker gate", err)
		}
	}
	for _, marker := range []byte{0xc0, 0xc1, 0xc2} {
		input, err := inspectJPEG(context.Background(), bytes.NewReader(jpegGateFixture(marker, []byte{1, 2, 3}, nil)))
		if err != nil || input.progressive != (marker == 0xc2) || input.width != 16 {
			t.Fatal("supported marker structure rejected", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inspectImage(ctx, bytes.NewReader([]byte(strings.Repeat("x", 100)))); err == nil {
		t.Fatal("preflight ignored canceled input")
	}
}
