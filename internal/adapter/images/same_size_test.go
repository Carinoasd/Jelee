package images

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func sameSizeFixture(kind string, bounds image.Rectangle) image.Image {
	outer := image.Rect(0, 0, bounds.Max.X+3, bounds.Max.Y+2)
	var input draw.Image
	palette := color.Palette{
		color.NRGBA64{R: 0xc123, G: 0x4567, B: 0xabcd, A: 0},
		color.NRGBA64{R: 0xc123, G: 0x4567, B: 0xabcd, A: 0x8000},
		color.NRGBA64{R: 0xc123, G: 0x4567, B: 0xabcd, A: 0xffff},
	}
	switch kind {
	case "rgba":
		input = image.NewRGBA(outer).SubImage(bounds).(*image.RGBA)
	case "nrgba":
		input = image.NewNRGBA(outer).SubImage(bounds).(*image.NRGBA)
	case "rgba64":
		input = image.NewRGBA64(outer).SubImage(bounds).(*image.RGBA64)
	case "nrgba64":
		input = image.NewNRGBA64(outer).SubImage(bounds).(*image.NRGBA64)
	case "gray":
		input = image.NewGray(outer).SubImage(bounds).(*image.Gray)
	case "gray16":
		input = image.NewGray16(outer).SubImage(bounds).(*image.Gray16)
	case "paletted":
		input = image.NewPaletted(outer, palette).SubImage(bounds).(*image.Paletted)
	default:
		panic("unknown same-size fixture")
	}
	alphas := [...]uint16{0, 0x0101, 0x7f7f, 0x8080, 0xfefe, 0xffff, 0x8001}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			input.Set(x, y, color.NRGBA64{R: 0xc123, G: 0x4567, B: 0xabcd, A: alphas[(x+y)%len(alphas)]})
		}
	}
	return input
}

func sameSizeStorage(input image.Image) ([]byte, int) {
	switch input := input.(type) {
	case *image.RGBA:
		return input.Pix, input.Stride
	case *image.NRGBA:
		return input.Pix, input.Stride
	case *image.RGBA64:
		return input.Pix, input.Stride
	case *image.NRGBA64:
		return input.Pix, input.Stride
	case *image.Gray:
		return input.Pix, input.Stride
	case *image.Gray16:
		return input.Pix, input.Stride
	case *image.Paletted:
		return input.Pix, input.Stride
	default:
		panic("unknown same-size storage")
	}
}

func sameSizeWhiteReference(input image.Image) *image.RGBA {
	out := image.NewRGBA(input.Bounds())
	draw.Draw(out, out.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(out, out.Bounds(), input, input.Bounds().Min, draw.Over)
	return out
}

func TestImageSameSizeSharesPixelsAndMatchesWhite(t *testing.T) {
	for _, kind := range []string{"rgba", "nrgba", "rgba64", "nrgba64", "gray", "gray16", "paletted"} {
		t.Run(kind, func(t *testing.T) {
			for _, bounds := range []image.Rectangle{image.Rect(0, 0, 1, 1), image.Rect(3, 5, 18, 9)} {
				input := sameSizeFixture(kind, bounds)
				want := sameSizeWhiteReference(input)
				pixels, stride := sameSizeStorage(input)
				got, err := sameSizeJPEGImage(context.Background(), input)
				if err != nil {
					t.Fatal("prepare same-size image", err)
				}
				gotPixels, gotStride := sameSizeStorage(got)
				if got.Bounds() != bounds || &gotPixels[0] != &pixels[0] || len(gotPixels) != len(pixels) || gotStride != stride {
					t.Fatal("same-size path copied pixels or changed bounds/stride")
				}
				for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
					for x := bounds.Min.X; x < bounds.Max.X; x++ {
						if color.RGBAModel.Convert(got.At(x, y)) != want.RGBAAt(x, y) {
							t.Fatal("in-place white composition changed a pixel")
						}
					}
				}
			}
		})
	}
	for _, input := range []image.Image{image.NewGray(image.Rect(0, 0, 9, 7)), image.NewYCbCr(image.Rect(0, 0, 9, 7), image.YCbCrSubsampleRatio420)} {
		got, err := sameSizeJPEGImage(context.Background(), input)
		if err != nil || got != input {
			t.Fatal("opaque JPEG representation was replaced")
		}
	}
}

type sameSizeCancellingContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *sameSizeCancellingContext) Err() error {
	c.checks--
	if c.checks == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestImageSameSizeCancellationAndUnsupportedType(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, kind := range []string{"rgba", "nrgba", "rgba64", "nrgba64", "gray", "gray16", "paletted"} {
		input := sameSizeFixture(kind, image.Rect(0, 0, 9, 3))
		pixels, _ := sameSizeStorage(input)
		before := append([]byte(nil), pixels...)
		if got, err := sameSizeJPEGImage(cancelled, input); got != nil || !errors.Is(err, context.Canceled) || !bytes.Equal(before, pixels) {
			t.Fatal("already-cancelled conversion changed pixels")
		}
	}
	for _, kind := range []string{"nrgba64", "gray16"} {
		input := sameSizeFixture(kind, image.Rect(0, 0, 9, 3))
		pixels, stride := sameSizeStorage(input)
		laterRows := append([]byte(nil), pixels[stride:]...)
		base, stop := context.WithCancel(context.Background())
		ctx := &sameSizeCancellingContext{Context: base, cancel: stop, checks: 3}
		got, err := sameSizeJPEGImage(ctx, input)
		stop()
		if got != nil || !errors.Is(err, context.Canceled) || !bytes.Equal(laterRows, pixels[stride:]) {
			t.Fatal("conversion ignored cancellation at a row boundary")
		}
	}
	for _, input := range []image.Image{image.NewAlpha(image.Rect(0, 0, 1, 1)), image.NewPaletted(image.Rect(0, 0, 1, 1), nil)} {
		if got, err := sameSizeJPEGImage(context.Background(), input); got != nil || err != domain.ErrImageUnavailable {
			t.Fatal("unsupported decoder representation was accepted")
		}
	}
}

func TestImageSameSizeEncodingDoesNotAllocatePerPixel(t *testing.T) {
	for _, kind := range []string{"nrgba", "nrgba64", "paletted"} {
		t.Run(kind, func(t *testing.T) {
			measure := func(size int) float64 {
				input := sameSizeFixture(kind, image.Rect(0, 0, size, size))
				pixels, _ := sameSizeStorage(input)
				original := append([]byte(nil), pixels...)
				var palette color.Palette
				if p, ok := input.(*image.Paletted); ok {
					palette = append(color.Palette(nil), p.Palette...)
				}
				return testing.AllocsPerRun(3, func() {
					copy(pixels, original)
					if p, ok := input.(*image.Paletted); ok {
						copy(p.Palette, palette)
					}
					prepared, err := sameSizeJPEGImage(context.Background(), input)
					if err != nil || jpeg.Encode(io.Discard, prepared, &jpeg.Options{Quality: 85}) != nil {
						t.Fatal("same-size allocation fixture failed")
					}
				})
			}
			small, large := measure(32), measure(256)
			if large > small+4 {
				t.Fatalf("encoding allocations grew with pixels: small=%g large=%g", small, large)
			}
		})
	}
}

func TestImageProcessorSameSizeColdAllocationBudget(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	var encoded bytes.Buffer
	if jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 640, 960)), &jpeg.Options{Quality: 85}) != nil {
		t.Fatal("encode same-size JPEG fixture")
	}
	imageWrite(t, filepath.Join(source.RootPath, "movie", "poster.jpg"), encoded.String())
	options := processorTestOptions(scratch)
	options.CacheEntries = 1
	measurement := testing.Benchmark(func(b *testing.B) {
		p, err := New(context.Background(), options)
		if err != nil {
			b.Fatal(err)
		}
		defer p.Shutdown(context.Background())
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			result, err := p.Render(context.Background(), source, domain.ImageRequest{Width: 640, Height: 960, Quality: 80 + i%2})
			if err != nil {
				b.Fatal(err)
			}
			_, readErr := io.Copy(io.Discard, result.Body)
			closeErr := result.Body.Close()
			if readErr != nil || closeErr != nil || result.Width != 640 || result.Height != 960 {
				b.Fatal("same-size cold response failed")
			}
		}
		b.StopTimer()
		if p.Stats().Decodes != uint64(b.N) {
			b.Fatal("same-size allocation measurement included a cache hit")
		}
	})
	if measurement.N == 0 || measurement.AllocedBytesPerOp() == 0 {
		t.Fatal("same-size allocation benchmark did not complete")
	}
	t.Logf("same-size JPEG: %d bytes per cold render", measurement.AllocedBytesPerOp())
	// A second 640x960 RGBA bitmap alone costs 2,457,600 bytes.
	if measurement.AllocedBytesPerOp() > 2<<20 {
		t.Fatal("same-size JPEG allocated over 2 MiB per cold render")
	}
	imageScratchEmpty(t, scratch)
}

func TestImageProcessorSameSizePNGPreservesSource(t *testing.T) {
	for _, kind := range []string{"nrgba", "nrgba64", "paletted"} {
		t.Run(kind, func(t *testing.T) {
			source, scratch := imageSourceFixture(t)
			var original bytes.Buffer
			if png.Encode(&original, sameSizeFixture(kind, image.Rect(0, 0, 33, 17))) != nil {
				t.Fatal("encode source PNG")
			}
			path := filepath.Join(source.RootPath, "movie", "poster.png")
			imageWrite(t, path, original.String())
			decoded, err := png.Decode(bytes.NewReader(original.Bytes()))
			if err != nil {
				t.Fatal("decode reference PNG")
			}
			var want bytes.Buffer
			if jpeg.Encode(&want, sameSizeWhiteReference(decoded), &jpeg.Options{Quality: 85}) != nil {
				t.Fatal("encode white reference")
			}
			p := processorTestNew(t, processorTestOptions(scratch))
			result, err := p.Render(context.Background(), source, domain.ImageRequest{})
			if err != nil {
				t.Fatal("render same-size PNG", err)
			}
			data, readErr := io.ReadAll(result.Body)
			closeErr := result.Body.Close()
			after, sourceErr := os.ReadFile(path)
			if readErr != nil || closeErr != nil || sourceErr != nil || !bytes.Equal(data, want.Bytes()) || !bytes.Equal(after, original.Bytes()) {
				t.Fatal("same-size PNG changed output semantics or source bytes")
			}
			if result.Width != 33 || result.Height != 17 || p.Stats().Active != 0 || p.Stats().Decodes != 1 {
				t.Fatal("same-size PNG changed bounds or leaked a reservation")
			}
			imageScratchEmpty(t, scratch)
		})
	}
}
