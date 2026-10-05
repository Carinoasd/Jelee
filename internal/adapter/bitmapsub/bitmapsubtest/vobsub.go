package bitmapsubtest

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"time"
)

// SPU describes one DVD subpicture unit. Pixels holds 2-bit values
// (row-major, Width*Height); Palette maps each value to an .idx palette
// entry and Alpha gives its opacity (0..15). Start and Stop are the
// display delays relative to the timestamp; Stop < 0 omits the stop command.
type SPU struct {
	Pixels              []uint8
	Width, Height, X, Y int
	Palette, Alpha      [4]uint8
	Start, Stop         time.Duration
	Forced              bool
}

// PackSize is the DVD program stream pack size.
const PackSize = 2048

// Ticks converts a delay to SPU control units (1024/90000 s), rounded.
func Ticks(d time.Duration) (uint16, error) {
	t := (int64(d)*90000/1024 + int64(time.Second)/2) / int64(time.Second)
	if d < 0 || t > 0xFFFF {
		return 0, errors.New("bitmapsubtest: delay out of range")
	}
	return u16(int(t)), nil
}

type nibbleWriter struct {
	b    []byte
	half bool
}

func (n *nibbleWriter) put(v uint8) {
	if n.half {
		n.b[len(n.b)-1] |= v & 0xF
	} else {
		n.b = append(n.b, v<<4)
	}
	n.half = !n.half
}

func (n *nibbleWriter) code(run int, c uint8) {
	v := run<<2 | int(c)
	digits := 4
	switch {
	case run == 0:
	case run < 4:
		digits = 1
	case run < 16:
		digits = 2
	case run < 64:
		digits = 3
	}
	for i := digits - 1; i >= 0; i-- {
		n.put(uint8(v >> (4 * i) & 0xF))
	}
}

// field encodes every second row starting at first. A run that reaches the
// end of the row uses the run-to-end code (run 0).
func (s SPU) field(n *nibbleWriter, first int) {
	for y := first; y < s.Height; y += 2 {
		row := s.Pixels[y*s.Width : (y+1)*s.Width]
		for x := 0; x < s.Width; {
			c, r := row[x], 1
			for x+r < s.Width && row[x+r] == c {
				r++
			}
			switch {
			case x+r == s.Width:
				n.code(0, c)
			case r > 255:
				r = 255
				n.code(r, c)
			default:
				n.code(r, c)
			}
			x += r
		}
		if n.half {
			n.put(0)
		}
	}
}

// Encode builds the SPU bytes: header, top field, bottom field and one or
// two control sequences.
func (s SPU) Encode() ([]byte, error) {
	if s.Width <= 0 || s.Height <= 0 || len(s.Pixels) != s.Width*s.Height || s.X < 0 || s.Y < 0 ||
		s.X+s.Width > 0x1000 || s.Y+s.Height > 0x1000 {
		return nil, errors.New("bitmapsubtest: invalid SPU geometry")
	}
	start, err := Ticks(s.Start)
	if err != nil {
		return nil, err
	}
	n := &nibbleWriter{b: make([]byte, 4)}
	s.field(n, 0)
	bottom := len(n.b)
	s.field(n, 1)
	b := n.b
	ctrl := len(b)
	x2, y2 := s.X+s.Width-1, s.Y+s.Height-1
	seq := []byte{0, 0, 0, 0}
	if s.Forced {
		seq = append(seq, 0x00)
	}
	seq = append(seq,
		0x03, s.Palette[3]<<4|s.Palette[2]&0xF, s.Palette[1]<<4|s.Palette[0]&0xF,
		0x04, s.Alpha[3]<<4|s.Alpha[2]&0xF, s.Alpha[1]<<4|s.Alpha[0]&0xF,
		0x05, b8(s.X>>4), b8(s.X<<4|x2>>8&0xF), b8(x2), b8(s.Y>>4), b8(s.Y<<4|y2>>8&0xF), b8(y2),
		0x06, 0, 4, b8(bottom>>8), b8(bottom),
		0x01, 0xFF)
	put16(seq, int(start))
	next := ctrl
	if s.Stop >= 0 {
		next = ctrl + len(seq)
	}
	put16(seq[2:], next)
	b = append(b, seq...)
	if s.Stop >= 0 {
		stop, err := Ticks(s.Stop)
		if err != nil {
			return nil, err
		}
		last := []byte{0, 0, 0, 0, 0x02, 0xFF}
		put16(last, int(stop))
		put16(last[2:], next)
		b = append(b, last...)
	}
	if len(b) > 0xFFFF {
		return nil, errors.New("bitmapsubtest: SPU larger than 64 KiB")
	}
	put16(b, len(b))
	put16(b[2:], ctrl)
	return b, nil
}

// pts33 is the 90 kHz clock; the 33-bit field wraps like a real stream.
func pts33(d time.Duration) int {
	return int(max(int64(d), 0) * 9 / 100000)
}

// WritePS writes an SPU as MPEG-2 program stream packs of PackSize bytes
// (substream 0x20+stream, a PTS on the first packet, padding packets or PES
// stuffing to fill each pack) and returns the bytes written.
func WritePS(w io.Writer, pts time.Duration, stream int, spu []byte) (int64, error) {
	if stream < 0 || stream > 0x1F {
		return 0, errors.New("bitmapsubtest: substream out of range")
	}
	var out bytes.Buffer
	t := pts33(pts)
	for off, first := 0, true; off < len(spu); first = false {
		hdrData := 0
		if first {
			hdrData = 5
		}
		avail := PackSize - 14 - 9 - hdrData - 1
		n := min(avail, len(spu)-off)
		left := avail - n
		stuff := 0
		if left > 0 && left < 6 {
			stuff, left = left, 0
		}
		out.Write([]byte{0, 0, 1, 0xBA,
			0x44 | b8(t>>27&0x38) | b8(t>>28&0x03), b8(t >> 20), b8(t>>12&0xF8) | 0x04 | b8(t>>13&0x03),
			b8(t >> 5), b8(t<<3&0xF8) | 0x04, 0x01, 0x01, 0x89, 0xC3, 0xF8})
		pes := []byte{0, 0, 1, 0xBD, 0, 0, 0x81, 0, b8(hdrData + stuff)}
		put16(pes[4:], 3+hdrData+stuff+1+n)
		if first {
			pes[7] = 0x80
			pes = append(pes, 0x21|b8(t>>29&0x0E), b8(t>>22), b8(t>>14&0xFE)|1, b8(t>>7), b8(t<<1&0xFE)|1)
		}
		pes = append(pes, bytes.Repeat([]byte{0xFF}, stuff)...)
		pes = append(pes, b8(0x20+stream))
		out.Write(pes)
		out.Write(spu[off : off+n])
		if left > 0 {
			pad := []byte{0, 0, 1, 0xBE, 0, 0}
			put16(pad[4:], left-6)
			out.Write(pad)
			out.Write(bytes.Repeat([]byte{0xFF}, left-6))
		}
		off += n
	}
	return out.WriteTo(w)
}

// Quantize4 reduces an image to the four VobSub values: 0 transparent
// background (alpha < 64), 1 the darker, 2 the brighter and 3 the
// intermediate (anti-aliasing) luma band of the visible pixels. It returns
// the values and the mean color of each band.
func Quantize4(img *image.NRGBA) ([]uint8, [4][3]uint8) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	px := func(x, y int) []uint8 { return img.Pix[img.PixOffset(img.Rect.Min.X+x, img.Rect.Min.Y+y):] }
	luma := func(p []uint8) int { return (299*int(p[0]) + 587*int(p[1]) + 114*int(p[2])) / 1000 }
	lo, hi := 255, 0
	for y := range h {
		for x := range w {
			if p := px(x, y); p[3] >= 192 {
				lo, hi = min(lo, luma(p)), max(hi, luma(p))
			}
		}
	}
	vals := make([]uint8, w*h)
	var sum [4][4]int
	for y := range h {
		for x := range w {
			p := px(x, y)
			if p[3] < 64 {
				continue
			}
			v := uint8(3)
			switch l := luma(p); {
			case hi <= lo || l > hi-(hi-lo)/3:
				v = 2
			case l < lo+(hi-lo)/3:
				v = 1
			}
			vals[y*w+x] = v
			for c := range 3 {
				sum[v][c] += int(p[c])
			}
			sum[v][3]++
		}
	}
	var colors [4][3]uint8
	for v := 1; v < 4; v++ {
		if n := sum[v][3]; n > 0 {
			for c := range 3 {
				colors[v][c] = b8(sum[v][c] / n)
			}
		}
	}
	return vals, colors
}

// EncodeVobSub writes an .idx and a .sub (one SPU per cue, packed into
// 2048-byte packs like a DVD) for substream 0. Each cue is reduced with
// Quantize4; the band colors share the 16-color .idx palette (entry 0 is
// black, later colors reuse the nearest entry once it is full). Timestamps
// have millisecond precision; the stop delay is relative to the rounded
// timestamp.
func EncodeVobSub(index, sub io.Writer, width, height int, cues []Cue) error {
	palette := [][3]uint8{{0, 0, 0}}
	slot := func(c [3]uint8) uint8 {
		best, bestD := 0, -1
		for i, p := range palette {
			d := 0
			for k := range 3 {
				d += (int(p[k]) - int(c[k])) * (int(p[k]) - int(c[k]))
			}
			if bestD < 0 || d < bestD {
				best, bestD = i, d
			}
		}
		if bestD != 0 && len(palette) < 16 {
			palette = append(palette, c)
			return b8(len(palette) - 1)
		}
		return uint8(best)
	}
	var subBuf bytes.Buffer
	var stamps []string
	for i, c := range cues {
		if c.Image == nil || c.End <= c.Start || c.Start < 0 {
			return fmt.Errorf("bitmapsubtest: cue %d timing or image", i)
		}
		iw, ih := c.Image.Rect.Dx(), c.Image.Rect.Dy()
		if c.X < 0 || c.Y < 0 || c.X+iw > width || c.Y+ih > height {
			return fmt.Errorf("bitmapsubtest: cue %d outside canvas", i)
		}
		vals, colors := Quantize4(c.Image)
		ts := c.Start.Truncate(time.Millisecond)
		s := SPU{Pixels: vals, Width: iw, Height: ih, X: c.X, Y: c.Y, Alpha: [4]uint8{0, 15, 15, 15},
			Start: c.Start - ts, Stop: c.End - ts, Forced: c.Forced}
		for v := 1; v < 4; v++ {
			s.Palette[v] = slot(colors[v])
		}
		spu, err := s.Encode()
		if err != nil {
			return err
		}
		ms := ts.Milliseconds()
		stamps = append(stamps, fmt.Sprintf("timestamp: %02d:%02d:%02d:%03d, filepos: %09x\n",
			ms/3600000, ms/60000%60, ms/1000%60, ms%1000, subBuf.Len()))
		if _, err := WritePS(&subBuf, c.Start, 0, spu); err != nil {
			return err
		}
	}
	var idx bytes.Buffer
	fmt.Fprintf(&idx, "# VobSub index file, v7 (do not modify this line!)\n#\n")
	fmt.Fprintf(&idx, "size: %dx%d\norg: 0, 0\nscale: 100%%, 100%%\nalpha: 100%%\nsmooth: OFF\n", width, height)
	fmt.Fprintf(&idx, "fadein/out: 0, 0\nalign: OFF at LEFT TOP\ntime offset: 0\nforced subs: OFF\npalette: ")
	for i := range 16 {
		p := [3]uint8{}
		if i < len(palette) {
			p = palette[i]
		}
		sep := ", "
		if i == 15 {
			sep = "\n"
		}
		fmt.Fprintf(&idx, "%02x%02x%02x%s", p[0], p[1], p[2], sep)
	}
	fmt.Fprintf(&idx, "custom colors: OFF, tridx: 0000, colors: 000000, 000000, 000000, 000000\n")
	fmt.Fprintf(&idx, "langidx: 0\n\n# English\nid: en, index: 0\n")
	for _, s := range stamps {
		idx.WriteString(s)
	}
	if _, err := idx.WriteTo(index); err != nil {
		return err
	}
	_, err := subBuf.WriteTo(sub)
	return err
}
