package bitmapsub

import (
	"bufio"
	"errors"
	"fmt"
	"image"
	"io"
)

const (
	// ocrVisible is the alpha above which a pixel belongs to the text.
	ocrVisible = 32
	// ocrMargin is the white border Tesseract needs around a text block.
	ocrMargin = 16
	// Cropped pictures lower than these are upscaled 3x and 2x, so that
	// small DVD glyphs reach a size Tesseract reads well. Larger pictures
	// stay as they are: measured on 2026-10-05 (docs/subtitle-ocr.md),
	// doubling 50-80 px high CJK lines cost 5-10 points of accuracy while
	// these bounds left Latin results unchanged or better.
	ocrTripleBelow = 24
	ocrDoubleBelow = 48
	// ocrPolarityMargin is the luma difference between rim and body that
	// decides polarity; below it the mean brightness decides.
	ocrPolarityMargin = 16
)

// OCRImage prepares a decoded picture for Tesseract and returns dark ink on a
// white background, or nil when nothing is visible or b is inconsistent.
//
// The picture is cropped to the bounding box of pixels with alpha above
// ocrVisible. Polarity: subtitles are a fill with an outline (white or yellow
// on black, sometimes black on white). The alpha-weighted mean luma of rim
// pixels (visible pixels touching a transparent pixel or the border, i.e. the
// outline) is compared with that of the remaining interior pixels. A rim
// darker than the interior means a bright fill, so ink = luma; a brighter
// rim means a dark fill, so ink = 255-luma, which turns the outline into
// background. Without a clear difference (no outline) a bright picture is
// read as bright text. Each output pixel is 255 - ink*alpha/255, so
// transparent pixels become white.
//
// The crop is padded by ocrMargin pixels and, when short, upscaled 2x or 3x
// with bilinear filtering. The result is at most
// 3*(MaxCanvas+2*ocrMargin) by 3*(ocrTripleBelow+2*ocrMargin) pixels or
// (MaxCanvas+2*ocrMargin) squared.
func OCRImage(b Bitmap) *image.Gray {
	w, h := b.Width, b.Height
	if w <= 0 || h <= 0 || w > MaxCanvas || h > MaxCanvas || w*h > MaxPixels || len(b.Luma) != w*h || len(b.Alpha) != w*h {
		return nil
	}
	visible := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < w && y < h && b.Alpha[y*w+x] > ocrVisible
	}
	minX, minY, maxX, maxY := w, h, -1, -1
	var rimSum, rimW, bodySum, bodyW int64
	for y := range h {
		for x := range w {
			if !visible(x, y) {
				continue
			}
			minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			a := int64(b.Alpha[y*w+x])
			l := int64(b.Luma[y*w+x]) * a
			if !visible(x-1, y) || !visible(x+1, y) || !visible(x, y-1) || !visible(x, y+1) {
				rimSum, rimW = rimSum+l, rimW+a
			} else {
				bodySum, bodyW = bodySum+l, bodyW+a
			}
		}
	}
	if maxX < 0 {
		return nil
	}
	invert := false
	diff := int64(0)
	if rimW > 0 && bodyW > 0 {
		diff = rimSum/rimW - bodySum/bodyW
	}
	switch {
	case diff > ocrPolarityMargin:
		invert = true
	case diff < -ocrPolarityMargin:
	default:
		invert = (rimSum+bodySum)/(rimW+bodyW) < 128
	}
	cw, ch := maxX-minX+1, maxY-minY+1
	pw, ph := cw+2*ocrMargin, ch+2*ocrMargin
	base := make([]uint8, pw*ph)
	for i := range base {
		base[i] = 255
	}
	for y := range ch {
		for x := range cw {
			i := (minY+y)*w + minX + x
			ink := int(b.Luma[i])
			if invert {
				ink = 255 - ink
			}
			base[(ocrMargin+y)*pw+ocrMargin+x] = clampByte(255 - ink*int(b.Alpha[i])/255)
		}
	}
	scale := 1
	switch {
	case ch < ocrTripleBelow:
		scale = 3
	case ch < ocrDoubleBelow:
		scale = 2
	}
	if scale == 1 {
		return &image.Gray{Pix: base, Stride: pw, Rect: image.Rect(0, 0, pw, ph)}
	}
	return upscale(base, pw, ph, scale)
}

// upscale enlarges a gray image by an integer factor with bilinear
// interpolation between source pixel centres.
func upscale(src []uint8, w, h, s int) *image.Gray {
	out := image.NewGray(image.Rect(0, 0, w*s, h*s))
	at := func(v, n int) (int, int, float64) {
		f := (float64(v)+0.5)/float64(s) - 0.5
		if f <= 0 {
			return 0, 0, 0
		}
		i := int(f)
		if i >= n-1 {
			return n - 1, n - 1, 0
		}
		return i, i + 1, f - float64(i)
	}
	for y := range h * s {
		y0, y1, fy := at(y, h)
		for x := range w * s {
			x0, x1, fx := at(x, w)
			top := float64(src[y0*w+x0])*(1-fx) + float64(src[y0*w+x1])*fx
			bot := float64(src[y1*w+x0])*(1-fx) + float64(src[y1*w+x1])*fx
			out.Pix[y*out.Stride+x] = uint8(top*(1-fy) + bot*fy + 0.5)
		}
	}
	return out
}

// EncodePGM writes img as a binary PGM (P5), which Leptonica reads without
// any image library.
func EncodePGM(w io.Writer, img *image.Gray) error {
	if w == nil || img == nil || img.Rect.Empty() {
		return errors.New("bitmapsub: empty image")
	}
	bw := bufio.NewWriter(w)
	r := img.Rect
	if _, err := fmt.Fprintf(bw, "P5\n%d %d\n255\n", r.Dx(), r.Dy()); err != nil {
		return err
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		i := img.PixOffset(r.Min.X, y)
		if _, err := bw.Write(img.Pix[i : i+r.Dx()]); err != nil {
			return err
		}
	}
	return bw.Flush()
}
