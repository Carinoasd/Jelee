// Package bitmapsubtest renders known text and encodes it as PGS and VobSub
// tracks, so tests can produce bitmap subtitles whose content is known. It is
// test support: the encoders favour clarity over compression.
package bitmapsubtest

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Style describes how RenderText draws text.
type Style struct {
	Fill, Outline color.NRGBA
	// OutlineWidth is the outline radius in pixels (0 = no outline).
	OutlineWidth int
	// Padding is a transparent border outside the outline.
	Padding int
	// FontIndex selects a face inside a collection (.ttc).
	FontIndex int
}

// Cue is one subtitle picture placed at X, Y on the video canvas.
type Cue struct {
	Start, End time.Duration
	Image      *image.NRGBA
	X, Y       int
	Forced     bool
}

const maxRender = 4096

// RenderText draws text (lines separated by "\n", centred) with an outline,
// like broadcast subtitles, on a transparent canvas sized to fit. fontData is
// any sfnt font or collection; nil selects Go Bold. size is in pixels.
func RenderText(fontData []byte, text string, size float64, st Style) (*image.NRGBA, error) {
	if fontData == nil {
		fontData = gobold.TTF
	}
	if text == "" || size <= 0 || size > 512 || st.OutlineWidth < 0 || st.OutlineWidth > 32 || st.Padding < 0 || st.Padding > 256 {
		return nil, errors.New("bitmapsubtest: invalid render request")
	}
	coll, err := opentype.ParseCollection(fontData)
	if err != nil {
		return nil, fmt.Errorf("bitmapsubtest: parse font: %w", err)
	}
	if st.FontIndex < 0 || st.FontIndex >= coll.NumFonts() {
		return nil, errors.New("bitmapsubtest: font index out of range")
	}
	f, err := coll.Font(st.FontIndex)
	if err != nil {
		return nil, fmt.Errorf("bitmapsubtest: font: %w", err)
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, fmt.Errorf("bitmapsubtest: face: %w", err)
	}
	defer func() { _ = face.Close() }()
	m := face.Metrics()
	lineH := max(m.Height, m.Ascent+m.Descent).Ceil()
	lines := strings.Split(text, "\n")
	widths := make([]int, len(lines))
	maxW := 1
	for i, l := range lines {
		widths[i] = font.MeasureString(face, l).Ceil()
		maxW = max(maxW, widths[i])
	}
	border := st.OutlineWidth + st.Padding
	w, h := maxW+2*border, len(lines)*lineH+2*border
	if w > maxRender || h > maxRender {
		return nil, errors.New("bitmapsubtest: text does not fit a canvas")
	}
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	d := font.Drawer{Dst: mask, Src: image.Opaque, Face: face}
	for i, l := range lines {
		d.Dot = fixed.P(border+(maxW-widths[i])/2, border+i*lineH+m.Ascent.Ceil())
		d.DrawString(l)
	}
	outline := dilate(mask, st.OutlineWidth)
	out := image.NewNRGBA(mask.Rect)
	for i, fa := range mask.Pix {
		af := int(st.Fill.A) * int(fa) / 255
		ao := int(st.Outline.A) * int(outline[i]) / 255 * (255 - af) / 255
		a := af + ao
		if a == 0 {
			continue
		}
		mix := func(f, o uint8) uint8 { return b8((int(f)*af + int(o)*ao + a/2) / a) }
		out.Pix[4*i] = mix(st.Fill.R, st.Outline.R)
		out.Pix[4*i+1] = mix(st.Fill.G, st.Outline.G)
		out.Pix[4*i+2] = mix(st.Fill.B, st.Outline.B)
		out.Pix[4*i+3] = b8(a)
	}
	return out, nil
}

// dilate returns the maximum of mask over a disk of radius r per pixel.
func dilate(mask *image.Alpha, r int) []uint8 {
	w, h := mask.Rect.Dx(), mask.Rect.Dy()
	out := make([]uint8, len(mask.Pix))
	if r == 0 {
		return out
	}
	for y := range h {
		for x := range w {
			best := uint8(0)
			for dy := -r; dy <= r; dy++ {
				for dx := -r; dx <= r; dx++ {
					sx, sy := x+dx, y+dy
					if dx*dx+dy*dy > r*r || sx < 0 || sy < 0 || sx >= w || sy >= h {
						continue
					}
					best = max(best, mask.Pix[sy*mask.Stride+sx])
				}
			}
			out[y*w+x] = best
		}
	}
	return out
}
