package bitmapsub

import (
	"bytes"
	"image"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
)

func fuzzVisit(t *testing.T) func(Event) error {
	return func(e Event) error {
		checkEvent(t, e)
		// Every decoded picture must be safe to prepare for OCR.
		if g := OCRImage(e.Bitmap); g != nil && len(g.Pix) > 9*(MaxCanvas+2*ocrMargin)*(MaxCanvas+2*ocrMargin) {
			t.Fatalf("OCR image too large: %v", g.Rect)
		}
		return nil
	}
}

func FuzzDecodePGS(f *testing.F) {
	img := render(f, "Fz", 16, whiteOnBlack)
	f.Add(encodePGS(f, []bitmapsubtest.Cue{
		{Start: time.Second, End: 2 * time.Second, Image: img, X: 4, Y: 4},
		{Start: 3 * time.Second, End: 4 * time.Second, Image: img, X: 40, Y: 4, Forced: true},
	}))
	// Two objects, one cropped, in a single composition.
	var multi bytes.Buffer
	w := &bitmapsubtest.PGSWriter{W: &multi}
	crop := image.Rect(1, 1, 3, 3)
	_ = w.PCS(0, 64, 64, 0, bitmapsubtest.StateEpochStart, 0, []bitmapsubtest.PCSObject{{ID: 0}, {ID: 1, X: 8, Y: 8, Crop: &crop}})
	_ = w.PDS(0, 0, 0, []bitmapsubtest.PaletteEntry{{Index: 1, Y: 235, A: 255}, {Index: 2, Y: 16, A: 255}})
	_ = w.ODS(0, 0, 0, 4, 2, []byte{1, 2, 0, 2, 0, 0, 0, 0x84, 1, 0, 0}, 3)
	_ = w.ODS(0, 1, 0, 4, 4, []byte{0, 0xC0, 4, 2, 0, 0}, 0)
	_ = w.END(0)
	f.Add(multi.Bytes())
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodePGS(bytes.NewReader(data), fuzzVisit(t))
	})
}

func FuzzDecodeVobSub(f *testing.F) {
	img := render(f, "Fz", 16, whiteOnBlack)
	idx, sub := encodeVob(f, []bitmapsubtest.Cue{
		{Start: time.Second, End: 2 * time.Second, Image: img, X: 4, Y: 4},
		{Start: 3 * time.Second, End: 4 * time.Second, Image: img, X: 40, Y: 4, Forced: true},
	})
	f.Add(idx, sub)
	f.Add(idxText(0, stamp(time.Second, 0)), psSPU(f, smallSPU(f, 0, -1, false), 0))
	f.Fuzz(func(t *testing.T, idx, sub []byte) {
		_, _ = DecodeVobSub(bytes.NewReader(idx), bytes.NewReader(sub), int64(len(sub)), fuzzVisit(t))
	})
}
