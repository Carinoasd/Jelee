package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"golang.org/x/image/webp"
)

func pipelineTestPNG(t *testing.T, width, height int, pixel func(x, y int) color.NRGBA) []byte {
	t.Helper()
	picture := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			picture.SetNRGBA(x, y, pixel(x, y))
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal("encode pipeline fixture")
	}
	return encoded.Bytes()
}

// pipelineTestStored keeps data in the store as an upload and returns the
// row that renders it.
func pipelineTestStored(t *testing.T, p *Processor, data []byte) domain.ItemImage {
	t.Helper()
	content, err := p.IngestItemImage(context.Background(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal("store pipeline fixture", err)
	}
	return domain.ItemImage{ID: "10000000-0000-4000-8000-0000000000c1", ItemID: "10000000-0000-4000-8000-0000000000c2",
		LibraryID: "10000000-0000-4000-8000-0000000000c3", Type: "Primary", SourceKind: domain.ImageSourceUpload, Locked: true, Content: &content}
}

func pipelineTestRender(t *testing.T, p *Processor, row domain.ItemImage, request domain.ImageRequest) ([]byte, string, string, int, int) {
	t.Helper()
	request.Type = row.Type
	result, err := p.RenderItemImage(context.Background(), row, request)
	if err != nil {
		t.Fatal("render", request, err)
	}
	return assetTestRead(t, result.Body), result.ContentType, result.ETag, result.Width, result.Height
}

func TestImagePlanOutputFitAndFill(t *testing.T) {
	for _, c := range []struct {
		name          string
		width, height int
		request       domain.ImageRequest
		mode          string
		outW, outH    int
		crop          image.Rectangle
		smart         bool
		maximum       int
	}{
		{"fit keeps ratio", 400, 200, domain.ImageRequest{Width: 100, Height: 100}, CropSmart, 100, 50, image.Rectangle{}, false, 1024},
		{"fill wide source", 400, 200, domain.ImageRequest{Width: 100, Height: 100, Crop: true}, CropSmart, 100, 100, image.Rect(100, 0, 300, 200), true, 1024},
		{"fill tall source", 200, 400, domain.ImageRequest{Width: 100, Height: 50, Crop: true}, CropCenter, 100, 50, image.Rect(0, 150, 200, 250), false, 1024},
		{"fill never enlarges", 400, 200, domain.ImageRequest{Width: 800, Height: 800, Crop: true}, CropSmart, 200, 200, image.Rect(100, 0, 300, 200), true, 1024},
		{"fill obeys maximum", 4000, 2000, domain.ImageRequest{Width: 2000, Height: 1000, Crop: true}, CropSmart, 1024, 512, image.Rect(0, 0, 4000, 2000), false, 1024},
		{"same ratio is not smart", 400, 200, domain.ImageRequest{Width: 40, Height: 20, Crop: true}, CropSmart, 40, 20, image.Rect(0, 0, 400, 200), false, 1024},
		{"crop off fits", 400, 200, domain.ImageRequest{Width: 100, Height: 100, Crop: true}, CropOff, 100, 50, image.Rectangle{}, false, 1024},
		{"one pixel source", 1, 1, domain.ImageRequest{Width: 300, Height: 100, Crop: true}, CropSmart, 1, 1, image.Rect(0, 0, 1, 1), false, 1024},
	} {
		plan := planOutput(c.width, c.height, c.request, c.maximum, c.mode)
		if plan.width != c.outW || plan.height != c.outH || plan.crop != c.crop || plan.smart != c.smart {
			t.Fatalf("%s: %+v", c.name, plan)
		}
		if !plan.crop.Empty() && (plan.crop.Min.X < 0 || plan.crop.Min.Y < 0 || plan.crop.Max.X > c.width || plan.crop.Max.Y > c.height) {
			t.Fatalf("%s: crop outside the image %v", c.name, plan.crop)
		}
	}
}

func TestImageCropTransformMatchesStoredPoints(t *testing.T) {
	const w, h = 6, 4
	for orientation := 1; orientation <= 8; orientation++ {
		uw, uh := w, h
		if orientation >= 5 {
			uw, uh = h, w
		}
		crop := image.Rect(1, 1, uw-1, uh)
		m := cropTransform(orientation, w, h, crop, 2*crop.Dx(), 3*crop.Dy())
		for v := crop.Min.Y; v < crop.Max.Y; v++ {
			for u := crop.Min.X; u < crop.Max.X; u++ {
				x, y := storedPoint(orientation, w, h, u, v)
				if x < 0 || y < 0 || x >= w || y >= h {
					t.Fatalf("orientation %d: (%d,%d) maps outside to (%d,%d)", orientation, u, v, x, y)
				}
				sx, sy := float64(x)+0.5, float64(y)+0.5
				gotX, gotY := m[0]*sx+m[1]*sy+m[2], m[3]*sx+m[4]*sy+m[5]
				wantX, wantY := 2*(float64(u-crop.Min.X)+0.5), 3*(float64(v-crop.Min.Y)+0.5)
				if math.Abs(gotX-wantX) > 1e-9 || math.Abs(gotY-wantY) > 1e-9 {
					t.Fatalf("orientation %d: (%d,%d) -> (%v,%v), want (%v,%v)", orientation, u, v, gotX, gotY, wantX, wantY)
				}
			}
		}
	}
}

func TestImageSmartCropFollowsDetail(t *testing.T) {
	ctx := context.Background()
	detail := image.NewNRGBA(image.Rect(0, 0, 300, 100))
	for y := range 100 {
		for x := range 300 {
			value := uint8(128)
			if x >= 200 && (x/3+y/3)%2 == 0 {
				value = 0
			}
			detail.SetNRGBA(x, y, color.NRGBA{value, value, value, 255})
		}
	}
	input := inspectedImage{format: "png", width: 300, height: 100}
	got, err := smartCrop(ctx, detail, input, image.Rect(100, 0, 200, 100))
	if err != nil || got.Dx() != 100 || got.Dy() != 100 || got.Min.X < 190 {
		t.Fatal("smart crop missed the detailed region", got, err)
	}
	// Rotated 90° clockwise (EXIF 6): the stored image is 100x300 and the
	// detail sits at the top of the stored image, the right of the upright.
	rotated := image.NewNRGBA(image.Rect(0, 0, 100, 300))
	for v := range 100 {
		for u := range 300 {
			x, y := storedPoint(6, 100, 300, u, v)
			rotated.SetNRGBA(x, y, detail.NRGBAAt(u, v))
		}
	}
	got, err = smartCrop(ctx, rotated, inspectedImage{format: "png", width: 100, height: 300, orientation: 6}, image.Rect(100, 0, 200, 100))
	if err != nil || got.Min.X < 190 || got.Dx() != 100 {
		t.Fatal("smart crop ignored the orientation", got, err)
	}
	// A flat image keeps the centred region.
	flat := image.NewNRGBA(image.Rect(0, 0, 100, 300))
	got, err = smartCrop(ctx, flat, inspectedImage{format: "png", width: 100, height: 300}, image.Rect(0, 100, 100, 200))
	if err != nil || got != image.Rect(0, 100, 100, 200) {
		t.Fatal("flat image was not centred", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := smartCrop(cancelled, detail, input, image.Rect(100, 0, 200, 100)); err != context.Canceled {
		t.Fatal("smart crop ignored cancellation", err)
	}
}

func TestImageColorsAverageAndDominant(t *testing.T) {
	ctx := context.Background()
	solid := image.NewUniform(color.NRGBA{R: 255, A: 255})
	sized := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for y := range 10 {
		for x := range 10 {
			sized.SetNRGBA(x, y, solid.C.(color.NRGBA))
		}
	}
	if average, dominant, err := imageColors(ctx, sized); err != nil || average != 0xff0000 || dominant != 0xff0000 {
		t.Fatalf("solid red: %06x %06x %v", average, dominant, err)
	}
	// Three quarters blue, one quarter red: the average mixes, the dominant
	// color is blue. Transparent pixels count as white.
	mixed := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := range 100 {
		for x := range 100 {
			c := color.NRGBA{B: 255, A: 255}
			if x < 25 {
				c = color.NRGBA{R: 255, A: 255}
			}
			mixed.SetNRGBA(x, y, c)
		}
	}
	average, dominant, err := imageColors(ctx, mixed)
	if err != nil || dominant != 0x0000ff || average>>16 < 0x30 || average>>16 > 0x50 || average&0xff < 0xb0 {
		t.Fatalf("mixed: %06x %06x %v", average, dominant, err)
	}
	transparent := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	if average, dominant, err := imageColors(ctx, transparent); err != nil || average != 0xffffff || dominant != 0xffffff {
		t.Fatalf("transparent: %06x %06x %v", average, dominant, err)
	}
	if _, _, err := imageColors(ctx, image.NewNRGBA(image.Rectangle{})); err == nil {
		t.Fatal("empty image accepted")
	}
}

func TestImageWebPOutputAndFillCrop(t *testing.T) {
	p, store, _, index := uploadTestProcessor(t)
	// Left half transparent, right half opaque red.
	data := pipelineTestPNG(t, 120, 60, func(x, _ int) color.NRGBA {
		if x < 60 {
			return color.NRGBA{}
		}
		return color.NRGBA{R: 255, A: 255}
	})
	row := pipelineTestStored(t, p, data)
	if c := row.Content; c.AverageColor == nil || c.DominantColor == nil || *c.DominantColor != 0xffffff && *c.DominantColor != 0xff0000 {
		t.Fatal("upload colors were not sampled", c.AverageColor, c.DominantColor)
	}
	webpData, contentType, webpETag, width, height := pipelineTestRender(t, p, row, domain.ImageRequest{Width: 60, Format: domain.ImageFormatWebP})
	if contentType != "image/webp" || width != 60 || height != 30 {
		t.Fatal("webp representation", contentType, width, height)
	}
	decoded, err := webp.Decode(bytes.NewReader(webpData))
	if err != nil || decoded.Bounds() != image.Rect(0, 0, 60, 30) {
		t.Fatal("webp output does not decode", err)
	}
	if _, _, _, a := decoded.At(5, 15).RGBA(); a != 0 {
		t.Fatal("webp output lost transparency", a)
	}
	if r, g, b, a := decoded.At(55, 15).RGBA(); r>>8 < 0xf0 || g>>8 > 0x10 || b>>8 > 0x10 || a != 0xffff {
		t.Fatal("webp output changed opaque pixels", r, g, b, a)
	}
	// The same size as JPEG is another representation with its own ETag;
	// quality never splits lossless WebP variants.
	jpegData, contentType, jpegETag, _, _ := pipelineTestRender(t, p, row, domain.ImageRequest{Width: 60})
	if contentType != "image/jpeg" || jpegETag == webpETag || bytes.Equal(jpegData, webpData) || !bytes.HasPrefix(jpegData, []byte{0xff, 0xd8}) {
		t.Fatal("format is not part of the representation")
	}
	decodes := p.Stats().Decodes
	_, _, again, _, _ := pipelineTestRender(t, p, row, domain.ImageRequest{Width: 60, Quality: 10, Format: domain.ImageFormatWebP})
	if again != webpETag || p.Stats().Decodes != decodes {
		t.Fatal("webp quality created another variant")
	}
	// A fill covers its box: 30x30 from the 120x60 image.
	_, _, fillETag, width, height := pipelineTestRender(t, p, row, domain.ImageRequest{Width: 30, Height: 30, Crop: true})
	if width != 30 || height != 30 || fillETag == jpegETag {
		t.Fatal("fill crop size", width, height)
	}
	// A fresh processor finds the WebP variant in the store and checks it as
	// WebP, without decoding the original.
	options := processorTestOptions(t.TempDir())
	options.Store, options.Index = store, index
	fresh := processorTestNew(t, options)
	stored, contentType, storedETag, _, _ := pipelineTestRender(t, fresh, row, domain.ImageRequest{Width: 60, Format: domain.ImageFormatWebP})
	if contentType != "image/webp" || storedETag != webpETag || !bytes.Equal(stored, webpData) || fresh.Stats().Decodes != 0 || fresh.Stats().VariantHits != 1 {
		t.Fatal("stored webp variant was not reused", fresh.Stats())
	}
	// Formats can be limited, and cropping disabled.
	limited := processorTestOptions(t.TempDir())
	limited.Store, limited.OutputFormats, limited.Crop = store, []string{domain.ImageFormatJPEG}, CropOff
	jpegOnly := processorTestNew(t, limited)
	request := domain.ImageRequest{Type: "Primary", Width: 60, Format: domain.ImageFormatWebP}
	if _, err := jpegOnly.RenderItemImage(context.Background(), row, request); err != domain.ErrImageUnsupported {
		t.Fatal("disabled webp output accepted", err)
	}
	if _, _, _, width, height := pipelineTestRender(t, jpegOnly, row, domain.ImageRequest{Width: 30, Height: 30, Crop: true}); width != 30 || height != 15 {
		t.Fatal("crop off did not fit", width, height)
	}
	for _, bad := range []Options{
		func() Options { o := limited; o.OutputFormats = []string{"avif"}; return o }(),
		func() Options { o := limited; o.OutputFormats = []string{"jpeg", "jpeg"}; return o }(),
		func() Options { o := limited; o.Crop = "edges"; return o }(),
	} {
		if p, err := New(context.Background(), bad); p != nil || err != domain.ErrInvalid {
			t.Fatal("invalid output options accepted", bad.OutputFormats, bad.Crop)
		}
	}
}

func TestImageWebPEstimateRefusesBeforeDecoding(t *testing.T) {
	p, _, _, _ := uploadTestProcessor(t)
	data := pipelineTestPNG(t, 1024, 1024, func(x, y int) color.NRGBA { return color.NRGBA{uint8(x), uint8(y), 0, 255} })
	row := pipelineTestStored(t, p, data)
	options := processorTestOptions(t.TempDir())
	options.Store, options.MaxImageBytes = p.options.Store, 32<<20
	small := processorTestNew(t, options)
	request := domain.ImageRequest{Type: "Primary", Width: 1024, Height: 1024}
	result, err := small.RenderItemImage(context.Background(), row, request)
	if err != nil {
		t.Fatal("jpeg within budget refused", err)
	}
	_ = result.Body.Close()
	decodes := small.Stats().Decodes
	request.Format = domain.ImageFormatWebP
	if _, err := small.RenderItemImage(context.Background(), row, request); err != domain.ErrImageTooLarge || small.Stats().Decodes != decodes {
		t.Fatal("webp encoder memory was not reserved", err)
	}
}

func TestImageDescribeStoredImage(t *testing.T) {
	p, _, _, _ := uploadTestProcessor(t)
	data := pipelineTestPNG(t, 40, 20, func(int, int) color.NRGBA { return color.NRGBA{G: 255, A: 255} })
	row := pipelineTestStored(t, p, data)
	digest := sha256.Sum256(data)
	content, err := p.DescribeStoredImage(context.Background(), digest)
	if err != nil || !bytes.Equal(content.SHA256, digest[:]) || content.Format != "png" || content.Width != 40 || content.Height != 20 ||
		content.Bytes != int64(len(data)) || content.AverageColor == nil || *content.AverageColor != 0x00ff00 || *content.DominantColor != 0x00ff00 {
		t.Fatal("describe stored original", content, err)
	}
	if *row.Content.AverageColor != 0x00ff00 || *row.Content.DominantColor != 0x00ff00 {
		t.Fatal("upload and description disagree")
	}
	if _, err := p.DescribeStoredImage(context.Background(), [32]byte{1}); err != domain.ErrNotFound && err != domain.ErrImageUnavailable {
		t.Fatal("missing original described", err)
	}
	plain := processorTestNew(t, processorTestOptions(t.TempDir()))
	if _, err := plain.DescribeStoredImage(context.Background(), digest); err != domain.ErrImageUnavailable {
		t.Fatal("description without a store", err)
	}
	if stats := p.Stats(); stats.Active != 0 {
		t.Fatal("description kept an admission slot")
	}
}

func TestImageCacheAndVariantKeysBindFormatAndCrop(t *testing.T) {
	p := processorTestNew(t, processorTestOptions(t.TempDir()))
	var source [32]byte
	base := domain.ImageRequest{Type: "Primary", Format: domain.ImageFormatJPEG, Width: 100, Height: 100, Quality: 80}
	variants := []domain.ImageRequest{base}
	webpRequest := base
	webpRequest.Format = domain.ImageFormatWebP
	fill := base
	fill.Crop = true
	variants = append(variants, webpRequest, fill)
	cache, stored := map[[32]byte]bool{}, map[[32]byte]bool{}
	for _, request := range variants {
		cache[imageCacheKey(source, request, CropSmart)] = true
		stored[p.variantKey(request)] = true
	}
	cache[imageCacheKey(source, fill, CropCenter)] = true
	if len(cache) != 4 || len(stored) != 3 {
		t.Fatal("format or crop does not separate representations", len(cache), len(stored))
	}
	// A JPEG fit keeps the key it had before formats and crops existed, so
	// stored variants stay valid across the upgrade.
	if p.variantKey(base) != StoreVariantKey("fit-v1/max=1024", "jpeg", 100, 100, 80) {
		t.Fatal("jpeg fit variant key changed")
	}
}
