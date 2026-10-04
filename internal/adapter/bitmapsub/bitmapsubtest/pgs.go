package bitmapsubtest

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"time"
)

// PGS segment types and flags, for tests that assemble streams by hand.
const (
	SegPDS = 0x14
	SegODS = 0x15
	SegPCS = 0x16
	SegWDS = 0x17
	SegEND = 0x80

	StateNormal      = 0x00
	StateAcquisition = 0x40
	StateEpochStart  = 0x80
)

// PaletteEntry is one PGS palette entry (limited-range Y, Cr, Cb).
type PaletteEntry struct{ Index, Y, Cr, Cb, A uint8 }

// PCSObject places an object in a composition; Crop, when set, is the part
// of the object shown at X, Y.
type PCSObject struct {
	ID     uint16
	Window uint8
	Forced bool
	X, Y   int
	Crop   *image.Rectangle
}

// PGSWriter writes HDMV PGS segments.
type PGSWriter struct {
	W io.Writer
}

// PTS converts a time to 90 kHz ticks, rounded.
func PTS(d time.Duration) (uint32, error) {
	t := (int64(d)*9 + 50000) / 100000
	if d < 0 || t > 0xFFFFFFFF {
		return 0, errors.New("bitmapsubtest: time out of PTS range")
	}
	return uint32(t & 0xFFFFFFFF), nil
}

// Segment writes one segment.
func (p *PGSWriter) Segment(pts time.Duration, typ byte, payload []byte) error {
	t, err := PTS(pts)
	if err != nil {
		return err
	}
	if len(payload) > 0xFFFF {
		return errors.New("bitmapsubtest: segment too large")
	}
	hdr := []byte{'P', 'G', 0, 0, 0, 0, 0, 0, 0, 0, typ, 0, 0}
	binary.BigEndian.PutUint32(hdr[2:], t)
	put16(hdr[11:], len(payload))
	if _, err := p.W.Write(hdr); err != nil {
		return err
	}
	_, err = p.W.Write(payload)
	return err
}

// PCS writes a presentation composition segment.
func (p *PGSWriter) PCS(pts time.Duration, width, height int, number uint16, state, paletteID uint8, objs []PCSObject) error {
	b := []byte{0, 0, 0, 0, 0x10, 0, 0, state, 0, paletteID, b8(len(objs))}
	put16(b[0:], width)
	put16(b[2:], height)
	binary.BigEndian.PutUint16(b[5:], number)
	for _, o := range objs {
		flags := uint8(0)
		if o.Crop != nil {
			flags |= 0x80
		}
		if o.Forced {
			flags |= 0x40
		}
		e := []byte{0, 0, o.Window, flags, 0, 0, 0, 0}
		binary.BigEndian.PutUint16(e, o.ID)
		put16(e[4:], o.X)
		put16(e[6:], o.Y)
		b = append(b, e...)
		if o.Crop != nil {
			c := make([]byte, 8)
			put16(c, o.Crop.Min.X)
			put16(c[2:], o.Crop.Min.Y)
			put16(c[4:], o.Crop.Dx())
			put16(c[6:], o.Crop.Dy())
			b = append(b, c...)
		}
	}
	return p.Segment(pts, SegPCS, b)
}

// WDS writes a window definition segment.
func (p *PGSWriter) WDS(pts time.Duration, windows []image.Rectangle) error {
	b := []byte{b8(len(windows))}
	for i, r := range windows {
		e := make([]byte, 9)
		e[0] = b8(i)
		put16(e[1:], r.Min.X)
		put16(e[3:], r.Min.Y)
		put16(e[5:], r.Dx())
		put16(e[7:], r.Dy())
		b = append(b, e...)
	}
	return p.Segment(pts, SegWDS, b)
}

// PDS writes a palette definition segment.
func (p *PGSWriter) PDS(pts time.Duration, id, version uint8, entries []PaletteEntry) error {
	b := []byte{id, version}
	for _, e := range entries {
		b = append(b, e.Index, e.Y, e.Cr, e.Cb, e.A)
	}
	return p.Segment(pts, SegPDS, b)
}

// ODS writes an object as one or more object definition segments; each
// fragment carries at most maxFragment RLE bytes (0 = as many as fit).
func (p *PGSWriter) ODS(pts time.Duration, id uint16, version uint8, w, h int, rle []byte, maxFragment int) error {
	if len(rle)+4 > 0xFFFFFF {
		return errors.New("bitmapsubtest: object too large")
	}
	first := true
	for rest := rle; first || len(rest) > 0; first = false {
		hdr := []byte{0, 0, version, 0}
		binary.BigEndian.PutUint16(hdr, id)
		room := 0xFFFF - 4
		if first {
			hdr[3] |= 0x80
			n := len(rle) + 4
			hdr = append(hdr, b8(n>>16), b8(n>>8), b8(n), 0, 0, 0, 0)
			put16(hdr[7:], w)
			put16(hdr[9:], h)
			room = 0xFFFF - 11
		}
		if maxFragment > 0 {
			room = min(room, maxFragment)
		}
		n := min(room, len(rest))
		if n == len(rest) {
			hdr[3] |= 0x40
		}
		if err := p.Segment(pts, SegODS, append(hdr, rest[:n]...)); err != nil {
			return err
		}
		rest = rest[n:]
	}
	return nil
}

// END writes an end-of-display-set segment.
func (p *PGSWriter) END(pts time.Duration) error {
	return p.Segment(pts, SegEND, nil)
}

// EncodeRLE encodes palette indices (row-major, w*h) as PGS run-length data,
// every row ended by an end-of-line code.
func EncodeRLE(idx []uint8, w, h int) []byte {
	var b []byte
	for y := range h {
		row := idx[y*w : (y+1)*w]
		for x := 0; x < w; {
			c, n := row[x], 1
			for x+n < w && row[x+n] == c && n < 0x3FFF {
				n++
			}
			switch {
			case c != 0 && n <= 2:
				for range n {
					b = append(b, c)
				}
			case c == 0 && n < 64:
				b = append(b, 0, uint8(n))
			case c == 0:
				b = append(b, 0, 0x40|uint8(n>>8), uint8(n))
			case n < 64:
				b = append(b, 0, 0x80|uint8(n), c)
			default:
				b = append(b, 0, 0xC0|uint8(n>>8), uint8(n), c)
			}
			x += n
		}
		b = append(b, 0, 0)
	}
	return b
}

// YCbCr709 converts an RGB color to limited-range BT.709 Y, Cr, Cb, the
// convention of Blu-ray palettes.
func YCbCr709(r, g, b uint8) (y, cr, cb uint8) {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	l := 0.2126*rf + 0.7152*gf + 0.0722*bf
	q := func(v float64) uint8 { return uint8(min(max(v+0.5, 0), 255)) }
	return q(16 + 219*l), q(128 + 224*(rf-l)/1.5748), q(128 + 224*(bf-l)/1.8556)
}

// Quantize maps an image to palette indices: index 0 is fully transparent,
// colors take 1..255. Precision is reduced until at most 255 colors remain.
func Quantize(img *image.NRGBA) ([]uint8, []PaletteEntry) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	type key [4]uint8
	for shift := uint(0); ; shift++ {
		idx := make([]uint8, w*h)
		seen := map[key]uint8{}
		entries := []PaletteEntry{{Index: 0, Y: 16, Cr: 128, Cb: 128, A: 0}}
		ok := true
	rows:
		for y := range h {
			for x := range w {
				p := img.Pix[img.PixOffset(img.Rect.Min.X+x, img.Rect.Min.Y+y):]
				if p[3] == 0 {
					continue
				}
				yy, cr, cb := YCbCr709(p[0], p[1], p[2])
				k := key{yy >> shift, cr >> shift, cb >> shift, p[3] >> shift}
				i, found := seen[k]
				if !found {
					if len(entries) > 255 {
						ok = false
						break rows
					}
					i = b8(len(entries))
					seen[k] = i
					entries = append(entries, PaletteEntry{Index: i, Y: yy, Cr: cr, Cb: cb, A: p[3]})
				}
				idx[y*w+x] = i
			}
		}
		if ok {
			return idx, entries
		}
	}
}

// EncodePGS writes a .sup: per cue an epoch-start display set
// (PCS+WDS+PDS+ODS+END, the ODS fragmented when the RLE data exceeds one
// segment) at Start and a clearing display set (PCS without objects, WDS,
// END) at End. Colors are BT.709 limited range; palette entry 0 is
// transparent. Cues must be ordered and not overlap.
func EncodePGS(w io.Writer, canvasWidth, canvasHeight int, cues []Cue) error {
	p := &PGSWriter{W: w}
	prevEnd := time.Duration(0)
	for i, c := range cues {
		if c.Image == nil || c.Start < prevEnd || c.End <= c.Start {
			return fmt.Errorf("bitmapsubtest: cue %d timing or image", i)
		}
		iw, ih := c.Image.Rect.Dx(), c.Image.Rect.Dy()
		if iw == 0 || ih == 0 || c.X < 0 || c.Y < 0 || c.X+iw > canvasWidth || c.Y+ih > canvasHeight {
			return fmt.Errorf("bitmapsubtest: cue %d outside canvas", i)
		}
		prevEnd = c.End
		idx, entries := Quantize(c.Image)
		window := []image.Rectangle{image.Rect(c.X, c.Y, c.X+iw, c.Y+ih)}
		obj := []PCSObject{{ID: 0, X: c.X, Y: c.Y, Forced: c.Forced}}
		steps := []func() error{
			func() error {
				return p.PCS(c.Start, canvasWidth, canvasHeight, u16(2*i), StateEpochStart, 0, obj)
			},
			func() error { return p.WDS(c.Start, window) },
			func() error { return p.PDS(c.Start, 0, 0, entries) },
			func() error { return p.ODS(c.Start, 0, 0, iw, ih, EncodeRLE(idx, iw, ih), 0) },
			func() error { return p.END(c.Start) },
			func() error {
				return p.PCS(c.End, canvasWidth, canvasHeight, u16(2*i+1), StateNormal, 0, nil)
			},
			func() error { return p.WDS(c.End, window) },
			func() error { return p.END(c.End) },
		}
		for _, s := range steps {
			if err := s(); err != nil {
				return err
			}
		}
	}
	return nil
}

func put16(b []byte, v int) {
	binary.BigEndian.PutUint16(b, u16(v))
}

// b8 and u16 keep the low bits of a value. The encoders only pass values
// whose range they checked, or fields that are defined to wrap.
func b8(v int) uint8 { return uint8(v & 0xFF) }

func u16(v int) uint16 { return uint16(v & 0xFFFF) }
