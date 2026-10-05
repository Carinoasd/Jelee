package images

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"
)

// BenchmarkRenderDecodedSmall measures decode, scale and JPEG encode of a
// fixed 640x360 gradient into a 160x90 thumbnail, entirely in memory. It skips
// source staging and the cache so every iteration renders.
func BenchmarkRenderDecodedSmall(b *testing.B) {
	input := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for y := range 360 {
		for x := range 640 {
			input.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, input, &jpeg.Options{Quality: 90}); err != nil {
		b.Fatal(err)
	}
	data := encoded.Bytes()
	ctx := context.Background()
	inspected, err := inspectImage(ctx, bytes.NewReader(data))
	if err != nil {
		b.Fatal(err)
	}
	p, err := New(context.Background(), processorTestOptions(b.TempDir()))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
	})
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		out, err := p.renderDecoded(ctx, bytes.NewReader(data), inspected, 160, 90, 85)
		if err != nil || out.width != 160 || len(out.data) == 0 {
			b.Fatal("render thumbnail", err)
		}
	}
}
