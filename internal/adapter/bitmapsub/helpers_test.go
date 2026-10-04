package bitmapsub

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
)

var (
	white  = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	black  = color.NRGBA{A: 255}
	yellow = color.NRGBA{R: 255, G: 230, A: 255}

	whiteOnBlack = bitmapsubtest.Style{Fill: white, Outline: black, OutlineWidth: 2, Padding: 2}
	blackOnWhite = bitmapsubtest.Style{Fill: black, Outline: white, OutlineWidth: 2, Padding: 2}
)

func render(t testing.TB, text string, size float64, st bitmapsubtest.Style) *image.NRGBA {
	t.Helper()
	img, err := bitmapsubtest.RenderText(nil, text, size, st)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// setMaxEvents lowers the per-track bound for one test.
func setMaxEvents(t *testing.T, n int) {
	t.Helper()
	old := maxEvents
	maxEvents = n
	t.Cleanup(func() { maxEvents = old })
}

func checkEvent(t testing.TB, e Event) {
	t.Helper()
	if e.End <= e.Start || e.End-e.Start > MaxDuration {
		t.Fatalf("event end %v not in (start %v, start+max]", e.End, e.Start)
	}
	b := e.Bitmap
	if b.Width <= 0 || b.Height <= 0 || len(b.Luma) != b.Width*b.Height || len(b.Alpha) != b.Width*b.Height {
		t.Fatalf("inconsistent bitmap %dx%d luma %d alpha %d", b.Width, b.Height, len(b.Luma), len(b.Alpha))
	}
}

func decodePGS(t testing.TB, data []byte) ([]Event, Stats, error) {
	t.Helper()
	var evs []Event
	st, err := DecodePGS(bytes.NewReader(data), func(e Event) error {
		checkEvent(t, e)
		evs = append(evs, e)
		return nil
	})
	return evs, st, err
}

func decodeVob(t testing.TB, idx, sub []byte) ([]Event, Stats, error) {
	t.Helper()
	var evs []Event
	st, err := DecodeVobSub(bytes.NewReader(idx), bytes.NewReader(sub), int64(len(sub)), func(e Event) error {
		checkEvent(t, e)
		evs = append(evs, e)
		return nil
	})
	return evs, st, err
}

func encodePGS(t testing.TB, cues []bitmapsubtest.Cue) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := bitmapsubtest.EncodePGS(&b, 1920, 1080, cues); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encodeVob(t testing.TB, cues []bitmapsubtest.Cue) ([]byte, []byte) {
	t.Helper()
	var idx, sub bytes.Buffer
	if err := bitmapsubtest.EncodeVobSub(&idx, &sub, 720, 480, cues); err != nil {
		t.Fatal(err)
	}
	return idx.Bytes(), sub.Bytes()
}

// toBitmap converts an NRGBA image with the given luma function.
func toBitmap(img *image.NRGBA, luma func(r, g, b uint8) uint8) Bitmap {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	b := Bitmap{Width: w, Height: h, Luma: make([]uint8, w*h), Alpha: make([]uint8, w*h)}
	for y := range h {
		for x := range w {
			p := img.Pix[img.PixOffset(x, y):]
			b.Luma[y*w+x], b.Alpha[y*w+x] = luma(p[0], p[1], p[2]), p[3]
		}
	}
	return b
}

func luma709(r, g, b uint8) uint8 {
	return uint8(0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b) + 0.5)
}

// inkMismatch compares two OCR images binarized at mid-gray and returns the
// fraction of differing pixels (1 when the sizes differ).
func inkMismatch(a, b *image.Gray) float64 {
	if a == nil || b == nil || a.Rect != b.Rect {
		return 1
	}
	diff := 0
	for i := range a.Pix {
		if (a.Pix[i] < 128) != (b.Pix[i] < 128) {
			diff++
		}
	}
	return float64(diff) / float64(len(a.Pix))
}

func near(a, b, tol time.Duration) bool {
	d := a - b
	return d <= tol && -d <= tol
}

// idxText builds a VobSub index with the given palette and timestamp lines.
func idxText(stream int, stamps ...string) []byte {
	var b strings.Builder
	b.WriteString("# VobSub index file, v7 (do not modify this line!)\r\n")
	b.WriteString("size: 720x480\r\npalette: 000000, ffffff, 808080, ff0000, 0000ff, 00ff00, 000000, 000000, " +
		"000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000\r\n")
	fmt.Fprintf(&b, "id: en, index: %d\r\n", stream)
	for _, s := range stamps {
		b.WriteString(s + "\r\n")
	}
	return []byte(b.String())
}

func stamp(d time.Duration, pos int) string {
	ms := d.Milliseconds()
	return fmt.Sprintf("timestamp: %02d:%02d:%02d:%03d, filepos: %09x", ms/3600000, ms/60000%60, ms/1000%60, ms%1000, pos)
}

// psSPU packs an SPU at its own filepos and returns the program stream.
func psSPU(t testing.TB, spu []byte, stream int) []byte {
	t.Helper()
	var b bytes.Buffer
	if _, err := bitmapsubtest.WritePS(&b, 0, stream, spu); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func mustSPU(t testing.TB, s bitmapsubtest.SPU) []byte {
	t.Helper()
	b, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
