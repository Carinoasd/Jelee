package bitmapsub

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
)

func bitmapOf(t *testing.T, text string, size float64, st bitmapsubtest.Style) Bitmap {
	t.Helper()
	return toBitmap(render(t, text, size, st), luma709)
}

// darkFraction is the share of pixels below mid-gray.
func darkFraction(g *image.Gray) float64 {
	n := 0
	for _, v := range g.Pix {
		if v < 128 {
			n++
		}
	}
	return float64(n) / float64(len(g.Pix))
}

func TestOCRImagePolarity(t *testing.T) {
	ref := OCRImage(bitmapOf(t, "Polarity 42", 48, whiteOnBlack))
	if ref == nil {
		t.Fatal("nil image")
	}
	styles := map[string]bitmapsubtest.Style{
		"black on white":   blackOnWhite,
		"yellow on black":  {Fill: yellow, Outline: black, OutlineWidth: 2, Padding: 2},
		"white no outline": {Fill: white, Padding: 2},
		"black no outline": {Fill: black, Padding: 2},
	}
	for name, st := range styles {
		g := OCRImage(bitmapOf(t, "Polarity 42", 48, st))
		if g == nil {
			t.Fatalf("%s: nil", name)
		}
		// The ink is the glyph fill, never the outline: a few percent of
		// the area, and the background (margin) is white.
		if f := darkFraction(g); f < 0.05 || f > 0.4 {
			t.Fatalf("%s: dark fraction %.3f", name, f)
		}
		for x := range g.Rect.Dx() {
			if g.Pix[x] != 255 || g.Pix[(g.Rect.Dy()-1)*g.Stride+x] != 255 {
				t.Fatalf("%s: margin not white", name)
			}
		}
		if g.Rect == ref.Rect {
			if m := inkMismatch(g, ref); m > 0.03 {
				t.Fatalf("%s: differs from white on black by %.3f", name, m)
			}
		}
	}
	// Same glyphs and outline, inverted colors: same ink.
	if m := inkMismatch(OCRImage(bitmapOf(t, "Polarity 42", 48, blackOnWhite)), ref); m > 0.01 {
		t.Fatalf("black on white differs by %.3f", m)
	}
}

func TestOCRImageCropPadScale(t *testing.T) {
	for _, c := range []struct{ h, scale int }{{10, 3}, {23, 3}, {24, 2}, {47, 2}, {48, 1}, {200, 1}} {
		b := Bitmap{Width: 500, Height: 300, Luma: make([]uint8, 500*300), Alpha: make([]uint8, 500*300)}
		// A 30 x h opaque white block at (100, 50) with a black rim.
		for y := 50; y < 50+c.h; y++ {
			for x := 100; x < 130; x++ {
				i := y*500 + x
				b.Alpha[i] = 255
				if x > 100 && x < 129 && y > 50 && y < 50+c.h-1 {
					b.Luma[i] = 255
				}
			}
		}
		g := OCRImage(b)
		w, h := (30+2*ocrMargin)*c.scale, (c.h+2*ocrMargin)*c.scale
		if g == nil || g.Rect.Dx() != w || g.Rect.Dy() != h {
			t.Fatalf("height %d: got %v want %dx%d", c.h, g.Rect, w, h)
		}
		// The block centre is ink, a corner of the padding is paper.
		if g.Pix[(h/2)*g.Stride+w/2] > 10 || g.Pix[0] != 255 {
			t.Fatalf("height %d: centre %d corner %d", c.h, g.Pix[(h/2)*g.Stride+w/2], g.Pix[0])
		}
	}
}

func TestOCRImageNothingVisible(t *testing.T) {
	cases := []Bitmap{
		{},
		{Width: 2, Height: 2, Luma: make([]uint8, 3), Alpha: make([]uint8, 4)},
		{Width: 2, Height: 2, Luma: make([]uint8, 4), Alpha: []uint8{0, ocrVisible, 10, 0}},
		{Width: MaxCanvas + 1, Height: 1},
	}
	for i, b := range cases {
		if OCRImage(b) != nil {
			t.Fatalf("case %d: want nil", i)
		}
	}
}

func TestUpscaleKeepsFlatAreas(t *testing.T) {
	src := []uint8{10, 10, 200, 200}
	g := upscale(src, 2, 2, 3)
	if g.Rect.Dx() != 6 || g.Pix[0] != 10 || g.Pix[g.Stride*5+5] != 200 || g.Pix[g.Stride*2] <= 10 || g.Pix[g.Stride*2] >= 200 {
		t.Fatalf("upscale: %v", g.Pix)
	}
}

type failWriter struct{ after int }

func (f *failWriter) Write(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, errors.New("full")
	}
	f.after--
	return len(p), nil
}

func TestEncodePGM(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 4, 3))
	for i := range img.Pix {
		img.Pix[i] = uint8(i)
	}
	sub := img.SubImage(image.Rect(1, 1, 3, 3)).(*image.Gray)
	var b bytes.Buffer
	if err := EncodePGM(&b, sub); err != nil {
		t.Fatal(err)
	}
	want := append([]byte("P5\n2 2\n255\n"), 5, 6, 9, 10)
	if !bytes.Equal(b.Bytes(), want) {
		t.Fatalf("pgm %q", b.Bytes())
	}
	if EncodePGM(&b, nil) == nil || EncodePGM(nil, img) == nil || EncodePGM(&b, &image.Gray{}) == nil {
		t.Fatal("empty input accepted")
	}
	big := image.NewGray(image.Rect(0, 0, 5000, 2))
	for _, n := range []int{0, 1} {
		if err := EncodePGM(&failWriter{after: n}, big); err == nil {
			t.Fatalf("write failure %d not reported", n)
		}
	}
	if err := EncodePGM(&failWriter{after: 0}, image.NewGray(image.Rect(0, 0, 1, 1))); err == nil {
		t.Fatal("flush failure not reported")
	}
}

func ExampleOCRImage() {
	img, _ := bitmapsubtest.RenderText(nil, "Hi", 20, bitmapsubtest.Style{Fill: color.NRGBA{R: 255, G: 255, B: 255, A: 255}, Outline: color.NRGBA{A: 255}, OutlineWidth: 1})
	g := OCRImage(toBitmap(img, luma709))
	var pgm bytes.Buffer
	_ = EncodePGM(&pgm, g)
	fmt.Println(bytes.HasPrefix(pgm.Bytes(), []byte("P5\n")), g.Rect.Dy() > img.Rect.Dy())
	// Output: true true
}
