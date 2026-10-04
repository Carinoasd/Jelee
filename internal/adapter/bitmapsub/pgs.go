package bitmapsub

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"
)

// PGS segment types.
const (
	pgsPDS = 0x14
	pgsODS = 0x15
	pgsPCS = 0x16
	pgsWDS = 0x17
	pgsEND = 0x80
)

const (
	pgsHeaderSize  = 13
	pgsEpochStart  = 0x80
	pgsFirstInSeq  = 0x80
	pgsLastInSeq   = 0x40
	pgsObjCropped  = 0x80
	pgsObjForced   = 0x40
	pgsPCSFixed    = 11
	pgsODSFirstHdr = 11
	pgsODSNextHdr  = 4
)

var (
	errRLE = errors.New("bitmapsub: corrupt run-length data")
	// errSkip marks a picture that cannot be decoded; decoding continues.
	errSkip = errors.New("bitmapsub: unreadable picture")
)

type pgsEntry struct{ y, a uint8 }

type pgsPalette [256]pgsEntry

type pgsObject struct {
	w, h  int
	limit int
	data  []byte
	open  bool
	done  bool
}

type pgsPlacement struct {
	id             uint16
	forced         bool
	x, y           int
	cropped        bool
	cx, cy, cw, ch int
}

type pgsComposition struct {
	pts       time.Duration
	paletteID uint8
	objects   []pgsPlacement
}

type pgsDecoder struct {
	em       emitter
	palettes map[uint8]*pgsPalette
	objects  map[uint16]*pgsObject
	held     int
	comp     *pgsComposition
}

// DecodePGS decodes a HDMV PGS segment stream (.sup) and calls visit for
// every displayed picture once its end time is known. A display set takes
// effect at its END segment; a composition without objects clears the screen.
// The stream may end at any segment boundary; a cut inside a segment is
// ErrInvalid.
func DecodePGS(r io.Reader, visit func(Event) error) (Stats, error) {
	if r == nil || visit == nil {
		return Stats{}, fmt.Errorf("%w: nil reader or visitor", ErrInvalid)
	}
	d := pgsDecoder{
		em:       emitter{visit: visit},
		palettes: map[uint8]*pgsPalette{},
		objects:  map[uint16]*pgsObject{},
	}
	var hdr [pgsHeaderSize]byte
	// Segment sizes are 16 bit, so one fixed buffer holds any payload.
	buf := make([]byte, 0xFFFF)
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				break
			}
			return d.em.stats, readError(err)
		}
		if hdr[0] != 'P' || hdr[1] != 'G' {
			return d.em.stats, fmt.Errorf("%w: missing segment magic", ErrInvalid)
		}
		pts := pts90k(be32(hdr[2:]))
		payload := buf[:be16(hdr[11:])]
		if _, err := io.ReadFull(r, payload); err != nil {
			return d.em.stats, readError(err)
		}
		if err := d.segment(hdr[10], pts, payload); err != nil {
			return d.em.stats, err
		}
	}
	err := d.em.finish()
	return d.em.stats, err
}

func readError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: truncated", ErrInvalid)
	}
	return fmt.Errorf("bitmapsub: read: %w", err)
}

func (d *pgsDecoder) segment(typ byte, pts time.Duration, p []byte) error {
	switch typ {
	case pgsPCS:
		c, epoch, err := parsePCS(pts, p)
		if err != nil {
			return err
		}
		if epoch {
			// Epoch start: the decoder buffers are emptied before this
			// display set's palettes and objects arrive.
			clear(d.palettes)
			clear(d.objects)
			d.held = 0
		}
		d.comp = &c
	case pgsPDS:
		return d.palette(p)
	case pgsODS:
		return d.object(p)
	case pgsEND:
		if d.comp == nil {
			return nil
		}
		c := *d.comp
		d.comp = nil
		return d.present(c)
	}
	// WDS and unknown segments carry nothing needed for OCR.
	return nil
}

func parsePCS(pts time.Duration, p []byte) (pgsComposition, bool, error) {
	if len(p) < pgsPCSFixed {
		return pgsComposition{}, false, fmt.Errorf("%w: short composition", ErrInvalid)
	}
	n := int(p[10])
	if n > MaxObjectsPerComposition {
		return pgsComposition{}, false, fmt.Errorf("%w: %d objects in one composition", ErrTooLarge, n)
	}
	c := pgsComposition{pts: pts, paletteID: p[9]}
	off := pgsPCSFixed
	for range n {
		if off+8 > len(p) {
			return pgsComposition{}, false, fmt.Errorf("%w: short composition object", ErrInvalid)
		}
		o := pgsPlacement{
			id:     be16(p[off:]),
			forced: p[off+3]&pgsObjForced != 0,
			x:      int(be16(p[off+4:])),
			y:      int(be16(p[off+6:])),
		}
		cropped := p[off+3]&pgsObjCropped != 0
		off += 8
		if cropped {
			if off+8 > len(p) {
				return pgsComposition{}, false, fmt.Errorf("%w: short crop rectangle", ErrInvalid)
			}
			o.cropped = true
			o.cx, o.cy = int(be16(p[off:])), int(be16(p[off+2:]))
			o.cw, o.ch = int(be16(p[off+4:])), int(be16(p[off+6:]))
			off += 8
		}
		c.objects = append(c.objects, o)
	}
	return c, p[7]&pgsEpochStart != 0, nil
}

func (d *pgsDecoder) palette(p []byte) error {
	if len(p) < 2 || (len(p)-2)%5 != 0 {
		return fmt.Errorf("%w: palette length %d", ErrInvalid, len(p))
	}
	pal := d.palettes[p[0]]
	if pal == nil {
		// Entries never defined stay fully transparent.
		pal = new(pgsPalette)
		d.palettes[p[0]] = pal
	}
	for e := p[2:]; len(e) >= 5; e = e[5:] {
		pal[e[0]] = pgsEntry{y: studioToFull(e[1]), a: e[4]}
	}
	return nil
}

// studioToFull expands limited-range (16..235) luma, which Blu-ray palettes
// use, to 0..255.
func studioToFull(y uint8) uint8 {
	if y <= 16 {
		return 0
	}
	return clampByte((int(y) - 16) * 255 / 219)
}

func (d *pgsDecoder) object(p []byte) error {
	if len(p) < pgsODSNextHdr {
		return fmt.Errorf("%w: short object", ErrInvalid)
	}
	id, seq := be16(p), p[3]
	var chunk []byte
	obj := d.objects[id]
	if seq&pgsFirstInSeq != 0 {
		if len(p) < pgsODSFirstHdr {
			return fmt.Errorf("%w: short object header", ErrInvalid)
		}
		declared := int(p[4])<<16 | int(p[5])<<8 | int(p[6])
		w, h := int(be16(p[7:])), int(be16(p[9:]))
		if w == 0 || h == 0 || declared < 4 {
			return fmt.Errorf("%w: empty object", ErrInvalid)
		}
		if w > MaxCanvas || h > MaxCanvas || w*h > MaxPixels {
			return fmt.Errorf("%w: object %dx%d", ErrTooLarge, w, h)
		}
		// Even a wasteful encoder needs at most three bytes per pixel and an
		// end-of-line code per row; anything beyond that is not a picture.
		limit := min(MaxObjectBytes, 3*w*h+2*h+16)
		if declared-4 > limit {
			return fmt.Errorf("%w: object data %d bytes", ErrTooLarge, declared-4)
		}
		if obj != nil {
			d.held -= len(obj.data)
		} else if len(d.objects) >= MaxEpochObjects {
			return fmt.Errorf("%w: more than %d objects", ErrTooLarge, MaxEpochObjects)
		}
		// The buffer grows with the data actually read, never with the
		// declared length.
		obj = &pgsObject{w: w, h: h, limit: limit, open: true}
		d.objects[id] = obj
		chunk = p[pgsODSFirstHdr:]
	} else {
		if obj == nil || !obj.open {
			return fmt.Errorf("%w: object continuation without start", ErrInvalid)
		}
		chunk = p[pgsODSNextHdr:]
	}
	if len(obj.data)+len(chunk) > obj.limit || d.held+len(chunk) > MaxEpochBytes {
		return fmt.Errorf("%w: object data exceeds bound", ErrTooLarge)
	}
	obj.data = append(obj.data, chunk...)
	d.held += len(chunk)
	if seq&pgsLastInSeq != 0 {
		obj.open, obj.done = false, true
	}
	return nil
}

func (d *pgsDecoder) present(c pgsComposition) error {
	if len(c.objects) == 0 {
		return d.em.end(c.pts)
	}
	if err := d.em.picture(); err != nil {
		return err
	}
	ev, err := d.compose(c)
	if errors.Is(err, errSkip) {
		d.em.stats.Skipped++
		return d.em.end(c.pts)
	}
	if err != nil {
		return err
	}
	if d.em.has && !d.em.endKnown && ev.Start >= d.em.pending.Start && sameEvent(d.em.pending, ev) {
		// A repeated screen (acquisition point) continues the event.
		return nil
	}
	return d.em.push(ev, false)
}

type pgsPart struct {
	obj            *pgsObject
	at             pgsPlacement
	sx, sy, sw, sh int
}

// compose draws the composition's objects onto a canvas that is the bounding
// box of their visible regions, not the whole video frame.
func (d *pgsDecoder) compose(c pgsComposition) (Event, error) {
	pal := d.palettes[c.paletteID]
	if pal == nil {
		return Event{}, errSkip
	}
	parts := make([]pgsPart, 0, len(c.objects))
	minX, minY, maxX, maxY := 0, 0, 0, 0
	forced := false
	for _, o := range c.objects {
		obj := d.objects[o.id]
		if obj == nil || !obj.done {
			return Event{}, errSkip
		}
		pt := pgsPart{obj: obj, at: o, sw: obj.w, sh: obj.h}
		if o.cropped {
			if o.cx >= obj.w || o.cy >= obj.h {
				continue
			}
			pt.sx, pt.sy = o.cx, o.cy
			pt.sw, pt.sh = min(o.cw, obj.w-o.cx), min(o.ch, obj.h-o.cy)
			if pt.sw <= 0 || pt.sh <= 0 {
				continue
			}
		}
		if len(parts) == 0 {
			minX, minY, maxX, maxY = o.x, o.y, o.x+pt.sw, o.y+pt.sh
		} else {
			minX, minY = min(minX, o.x), min(minY, o.y)
			maxX, maxY = max(maxX, o.x+pt.sw), max(maxY, o.y+pt.sh)
		}
		forced = forced || o.forced
		parts = append(parts, pt)
	}
	if len(parts) == 0 {
		return Event{}, errSkip
	}
	bm, err := d.em.newBitmap(maxX-minX, maxY-minY)
	if err != nil {
		return Event{}, err
	}
	for _, pt := range parts {
		ox, oy := pt.at.x-minX, pt.at.y-minY
		err := decodePGSRLE(pt.obj.data, pt.obj.w, pt.obj.h, func(x, y, n int, c uint8) {
			if y < pt.sy || y >= pt.sy+pt.sh {
				return
			}
			x0, x1 := max(x, pt.sx), min(x+n, pt.sx+pt.sw)
			e := pal[c]
			if x0 >= x1 || e.a == 0 {
				return
			}
			row := (oy+y-pt.sy)*bm.Width + ox - pt.sx
			for i := row + x0; i < row+x1; i++ {
				bm.Luma[i], bm.Alpha[i] = e.y, e.a
			}
		})
		if err != nil {
			return Event{}, errSkip
		}
	}
	return Event{Start: c.pts, Forced: forced, Bitmap: bm}, nil
}

// decodePGSRLE walks PGS run-length data and reports each run. A run that
// leaves the row or the picture, or a code cut short, is an error; rows that
// are never written stay transparent.
func decodePGSRLE(data []byte, w, h int, run func(x, y, n int, c uint8)) error {
	x, y := 0, 0
	for i := 0; i < len(data); {
		b := data[i]
		i++
		n, c := 1, b
		if b == 0 {
			if i >= len(data) {
				return errRLE
			}
			f := data[i]
			i++
			if f == 0 {
				x, y = 0, y+1
				continue
			}
			n, c = int(f&0x3F), 0
			if f&0x40 != 0 {
				if i >= len(data) {
					return errRLE
				}
				n = n<<8 | int(data[i])
				i++
			}
			if f&0x80 != 0 {
				if i >= len(data) {
					return errRLE
				}
				c = data[i]
				i++
			}
		}
		if y >= h || n > w-x {
			return errRLE
		}
		run(x, y, n, c)
		x += n
	}
	return nil
}

func sameEvent(a, b Event) bool {
	return a.Forced == b.Forced && a.Bitmap.Width == b.Bitmap.Width && a.Bitmap.Height == b.Bitmap.Height &&
		bytes.Equal(a.Bitmap.Luma, b.Bitmap.Luma) && bytes.Equal(a.Bitmap.Alpha, b.Bitmap.Alpha)
}

func pts90k(v uint32) time.Duration {
	return time.Duration(int64(v) * 100000 / 9)
}

func be16(p []byte) uint16 { return uint16(p[0])<<8 | uint16(p[1]) }

func be32(p []byte) uint32 {
	return uint32(p[0])<<24 | uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
}
