package bitmapsub

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	// maxSPUScan bounds the program stream bytes read for one SPU. An SPU
	// is at most 64 KiB; the rest allows for interleaved packets.
	maxSPUScan = 1 << 20
	spuMinSize = 4
)

type idxEntry struct {
	ts  time.Duration
	pos int64
}

type vobIndex struct {
	luma    [16]uint8
	stream  int
	entries []idxEntry
}

// DecodeVobSub decodes a VobSub track: index is the .idx text and sub the
// .sub program stream of subSize bytes. Only the first "id:" section of the
// index is read. Supported index keys are "palette" (16 RGB colors, reduced
// to BT.601 luma), "id" (its "index" selects substream 0x20+index) and
// "timestamp" with "filepos"; everything else, including "delay", "size" and
// "custom colors", is ignored, because the SPU control sequence carries the
// coordinates and the colors in use. Each event starts at its timestamp plus
// the start-display delay and ends at its stop-display delay, or at the next
// event when there is none.
func DecodeVobSub(index io.Reader, sub io.ReaderAt, subSize int64, visit func(Event) error) (Stats, error) {
	if index == nil || sub == nil || visit == nil || subSize < 0 {
		return Stats{}, fmt.Errorf("%w: nil reader or visitor", ErrInvalid)
	}
	idx, err := parseIndex(index)
	if err != nil {
		return Stats{}, err
	}
	em := emitter{visit: visit}
	ps := new(psReader)
	for _, e := range idx.entries {
		if e.pos >= subSize {
			return em.stats, fmt.Errorf("%w: filepos %d beyond sub file", ErrInvalid, e.pos)
		}
		if err := em.picture(); err != nil {
			return em.stats, err
		}
		spu, stream, err := readSPU(ps, sub, e.pos, subSize, idx.stream)
		if err != nil {
			if errors.Is(err, errSkip) {
				em.stats.Skipped++
				continue
			}
			return em.stats, err
		}
		idx.stream = stream
		ev, endKnown, err := decodeSPU(&em, spu, &idx.luma)
		if errors.Is(err, errSkip) {
			em.stats.Skipped++
			continue
		}
		if err != nil {
			return em.stats, err
		}
		ev.Start += e.ts
		ev.End += e.ts
		if err := em.push(ev, endKnown); err != nil {
			return em.stats, err
		}
	}
	err = em.finish()
	return em.stats, err
}

func parseIndex(r io.Reader) (vobIndex, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxIndexBytes+1))
	if err != nil {
		return vobIndex{}, fmt.Errorf("bitmapsub: read index: %w", err)
	}
	if len(data) > MaxIndexBytes {
		return vobIndex{}, fmt.Errorf("%w: index larger than %d bytes", ErrTooLarge, MaxIndexBytes)
	}
	idx := vobIndex{stream: -1}
	palette, ids := false, 0
	for line := range strings.Lines(string(data)) {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.HasPrefix(key, "#") {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "palette":
			if idx.luma, ok = parsePalette(val); !ok {
				return vobIndex{}, fmt.Errorf("%w: palette", ErrInvalid)
			}
			palette = true
		case "id":
			if ids++; ids > 1 {
				break
			}
			if _, n, found := strings.Cut(val, "index:"); found {
				v, err := strconv.Atoi(strings.TrimSpace(n))
				if err != nil || v < 0 || v > 0x1F {
					return vobIndex{}, fmt.Errorf("%w: stream index", ErrInvalid)
				}
				idx.stream = v
			}
		case "timestamp":
			if ids > 1 {
				continue
			}
			e, ok := parseTimestamp(val)
			if !ok {
				return vobIndex{}, fmt.Errorf("%w: timestamp line", ErrInvalid)
			}
			if len(idx.entries) >= maxEvents {
				return vobIndex{}, fmt.Errorf("%w: more than %d timestamps", ErrTooLarge, maxEvents)
			}
			idx.entries = append(idx.entries, e)
		}
	}
	if !palette {
		return vobIndex{}, fmt.Errorf("%w: index without palette", ErrInvalid)
	}
	return idx, nil
}

func parsePalette(val string) ([16]uint8, bool) {
	var luma [16]uint8
	parts := strings.Split(val, ",")
	if len(parts) != 16 {
		return luma, false
	}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) != 6 {
			return luma, false
		}
		v, err := strconv.ParseUint(p, 16, 32)
		if err != nil {
			return luma, false
		}
		luma[i] = rgbLuma(uint8(v>>16&0xFF), uint8(v>>8&0xFF), uint8(v&0xFF))
	}
	return luma, true
}

// rgbLuma is BT.601 full-range luma, the matrix of standard-definition DVDs.
func rgbLuma(r, g, b uint8) uint8 {
	return clampByte((299*int(r) + 587*int(g) + 114*int(b) + 500) / 1000)
}

// parseTimestamp reads "HH:MM:SS:mmm, filepos: hex".
func parseTimestamp(val string) (idxEntry, bool) {
	ts, pos, ok := strings.Cut(val, ",")
	if !ok {
		return idxEntry{}, false
	}
	key, hex, ok := strings.Cut(strings.TrimSpace(pos), ":")
	if !ok || strings.TrimSpace(key) != "filepos" {
		return idxEntry{}, false
	}
	off, err := strconv.ParseInt(strings.TrimSpace(hex), 16, 64)
	if err != nil || off < 0 {
		return idxEntry{}, false
	}
	f := strings.Split(strings.TrimSpace(ts), ":")
	if len(f) != 4 {
		return idxEntry{}, false
	}
	limits := [4]int{1 << 20, 59, 59, 999}
	var n [4]int
	for i, s := range f {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 || v > limits[i] || s[0] == '+' {
			return idxEntry{}, false
		}
		n[i] = v
	}
	d := time.Duration(n[0])*time.Hour + time.Duration(n[1])*time.Minute +
		time.Duration(n[2])*time.Second + time.Duration(n[3])*time.Millisecond
	return idxEntry{ts: d, pos: off}, true
}

// psReader reads MPEG program stream packets from a bounded section.
type psReader struct {
	r   *bufio.Reader
	buf [0xFFFF]byte
}

// readSPU collects one SPU starting at the pack at pos. Packets of other
// streams are skipped; stream < 0 accepts the first subpicture substream.
func readSPU(ps *psReader, sub io.ReaderAt, pos, size int64, stream int) ([]byte, int, error) {
	section := io.NewSectionReader(sub, pos, min(size-pos, maxSPUScan))
	if ps.r == nil {
		ps.r = bufio.NewReaderSize(section, 4096)
	} else {
		ps.r.Reset(section)
	}
	var spu []byte
	total := -1
	for {
		code, err := ps.startCode()
		if err != nil {
			return nil, stream, err
		}
		switch {
		case code == 0xBA:
			if err := ps.packHeader(); err != nil {
				return nil, stream, err
			}
			continue
		case code < 0xBB:
			// Program end or a code that is not a packet.
			return nil, stream, errSkip
		}
		p, err := ps.packet()
		if err != nil {
			return nil, stream, err
		}
		if code != 0xBD {
			continue
		}
		data, ok := pesPayload(p)
		if !ok {
			return nil, stream, errSkip
		}
		id := int(data[0])
		if id < 0x20 || id > 0x3F || (stream >= 0 && id != 0x20+stream) {
			continue
		}
		stream = id - 0x20
		data = data[1:]
		if total < 0 {
			if len(data) < 2 {
				return nil, stream, errSkip
			}
			total = int(be16(data))
			if total < spuMinSize {
				return nil, stream, errSkip
			}
			spu = make([]byte, 0, total)
		}
		spu = append(spu, data[:min(len(data), total-len(spu))]...)
		if len(spu) == total {
			return spu, stream, nil
		}
	}
}

func (ps *psReader) read(n int) ([]byte, error) {
	b := ps.buf[:n]
	if _, err := io.ReadFull(ps.r, b); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, errSkip
		}
		return nil, fmt.Errorf("bitmapsub: read sub: %w", err)
	}
	return b, nil
}

func (ps *psReader) startCode() (byte, error) {
	b, err := ps.read(4)
	if err != nil {
		return 0, err
	}
	if b[0] != 0 || b[1] != 0 || b[2] != 1 {
		return 0, errSkip
	}
	return b[3], nil
}

// packHeader skips an MPEG-2 (14 bytes plus stuffing) or MPEG-1 (12 bytes)
// pack header whose start code was read.
func (ps *psReader) packHeader() error {
	b, err := ps.read(1)
	if err != nil {
		return err
	}
	switch {
	case b[0]>>6 == 1:
		rest, err := ps.read(9)
		if err != nil {
			return err
		}
		_, err = ps.read(int(rest[8] & 7))
		return err
	case b[0]>>4 == 2:
		_, err := ps.read(7)
		return err
	}
	return errSkip
}

// packet reads the body of a packet with a 16-bit length field.
func (ps *psReader) packet() ([]byte, error) {
	l, err := ps.read(2)
	if err != nil {
		return nil, err
	}
	return ps.read(int(be16(l)))
}

// pesPayload strips an MPEG-2 or MPEG-1 PES header and returns the payload,
// which starts with the substream id byte.
func pesPayload(p []byte) ([]byte, bool) {
	if len(p) >= 3 && p[0]&0xC0 == 0x80 {
		n := 3 + int(p[2])
		return p[min(n, len(p)):], n < len(p)
	}
	i := 0
	for i < len(p) && i < 16 && p[i] == 0xFF {
		i++
	}
	if i < len(p) && p[i]&0xC0 == 0x40 {
		i += 2
	}
	if i >= len(p) {
		return nil, false
	}
	switch {
	case p[i]>>4 == 2:
		i += 5
	case p[i]>>4 == 3:
		i += 10
	case p[i] == 0x0F:
		i++
	default:
		return nil, false
	}
	return p[min(i, len(p)):], i < len(p)
}

// spuArgs is the argument length of a control command.
func spuArgs(cmd byte) int {
	switch cmd {
	case 0x03, 0x04:
		return 2
	case 0x05:
		return 6
	case 0x06:
		return 4
	}
	return 0
}

// spuTicks converts a control sequence delay (1024/90000 s units).
func spuTicks(n uint16) time.Duration {
	return time.Duration(n) * 1024 * time.Second / 90000
}

// decodeSPU parses the control sequences of one subpicture unit and decodes
// its interlaced bitmap. Start and End are relative to the SPU timestamp;
// endKnown reports a stop-display command. A corrupt SPU yields errSkip.
func decodeSPU(em *emitter, spu []byte, luma *[16]uint8) (Event, bool, error) {
	size := len(spu)
	ctrl := int(be16(spu[2:]))
	if ctrl < spuMinSize || ctrl+4 > size {
		return Event{}, false, errSkip
	}
	var (
		ev                      Event
		hasStop, coords, fields bool
		pmap                    = [4]uint8{0, 1, 2, 3}
		amap                    = [4]uint8{0, 15, 15, 15}
		x1, x2, y1, y2          int
		top, bottom             int
	)
	nibbles := func(p []byte, m *[4]uint8) {
		m[3], m[2], m[1], m[0] = p[0]>>4, p[0]&0xF, p[1]>>4, p[1]&0xF
	}
	for off := ctrl; ; {
		if off+4 > size {
			return Event{}, false, errSkip
		}
		delay := spuTicks(be16(spu[off:]))
		next := int(be16(spu[off+2:]))
		for i, done := off+4, false; !done; {
			if i >= size {
				return Event{}, false, errSkip
			}
			cmd := spu[i]
			i++
			arg := spuArgs(cmd)
			if i+arg > size {
				return Event{}, false, errSkip
			}
			a := spu[i : i+arg]
			i += arg
			switch cmd {
			case 0x00:
				ev.Forced = true
			case 0x01:
				ev.Start = delay
			case 0x02:
				ev.End, hasStop = delay, true
			case 0x03:
				nibbles(a, &pmap)
			case 0x04:
				nibbles(a, &amap)
			case 0x05:
				x1, x2 = int(a[0])<<4|int(a[1]>>4), int(a[1]&0xF)<<8|int(a[2])
				y1, y2 = int(a[3])<<4|int(a[4]>>4), int(a[4]&0xF)<<8|int(a[5])
				coords = true
			case 0x06:
				top, bottom = int(be16(a)), int(be16(a[2:]))
				fields = true
			case 0xFF:
				done = true
			default:
				return Event{}, false, errSkip
			}
		}
		// The last sequence points at itself; a pointer that does not move
		// forward would loop, so it ends the chain as well.
		if next <= off {
			break
		}
		off = next
	}
	if !coords || !fields || x2 < x1 || y2 < y1 ||
		top < spuMinSize || top >= size || bottom < spuMinSize || bottom >= size {
		return Event{}, false, errSkip
	}
	bm, err := em.newBitmap(x2-x1+1, y2-y1+1)
	if err != nil {
		return Event{}, false, err
	}
	var lut [4]pgsEntry
	for c := range lut {
		lut[c] = pgsEntry{y: luma[pmap[c]], a: amap[c] * 17}
	}
	even := nibbleReader{data: spu, pos: 2 * top}
	odd := nibbleReader{data: spu, pos: 2 * bottom}
	for y := range bm.Height {
		r := &even
		if y%2 == 1 {
			r = &odd
		}
		if !r.row(bm, y, &lut) {
			return Event{}, false, errSkip
		}
	}
	ev.Bitmap = bm
	return ev, hasStop, nil
}

type nibbleReader struct {
	data []byte
	pos  int
	bad  bool
}

func (r *nibbleReader) next() int {
	if r.pos/2 >= len(r.data) {
		r.bad = true
		return 0
	}
	b := r.data[r.pos/2]
	r.pos++
	if r.pos%2 == 1 {
		return int(b >> 4)
	}
	return int(b & 0xF)
}

// row decodes one line of 1-4 nibble codes (run<<2 | color, run 0 = to the
// end of the line) and realigns to a byte boundary.
func (r *nibbleReader) row(bm Bitmap, y int, lut *[4]pgsEntry) bool {
	base := y * bm.Width
	for x := 0; x < bm.Width; {
		v := r.next()
		if v < 0x4 {
			v = v<<4 | r.next()
			if v < 0x10 {
				v = v<<4 | r.next()
				if v < 0x40 {
					v = v<<4 | r.next()
				}
			}
		}
		if r.bad {
			return false
		}
		n, e := v>>2, lut[v&3]
		if n == 0 {
			n = bm.Width - x
		}
		if n > bm.Width-x {
			return false
		}
		if e.a != 0 {
			for i := base + x; i < base+x+n; i++ {
				bm.Luma[i], bm.Alpha[i] = e.y, e.a
			}
		}
		x += n
	}
	r.pos += r.pos % 2
	return true
}
