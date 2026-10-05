package bitmapsub

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
)

// VobSub timestamps have millisecond precision and the stop delay is in
// 1024/90000 s units (11.4 ms), rounded: start is within 1 ms and the end
// within 1 ms + 5.7 ms of the cue.
const (
	vobStartTolerance = time.Millisecond
	vobEndTolerance   = 7 * time.Millisecond
)

func TestVobSubRoundTrip(t *testing.T) {
	big := render(t, "A long first line of text\nand a second one below", 30, whiteOnBlack)
	small := render(t, "Yes", 26, bitmapsubtest.Style{Fill: yellow, Outline: black, OutlineWidth: 2, Padding: 1})
	inverse := render(t, "Dark", 30, blackOnWhite)
	cues := []bitmapsubtest.Cue{
		{Start: 1234567 * time.Microsecond, End: 3 * time.Second, Image: big, X: 20, Y: 360},
		{Start: 3*time.Second + 500*time.Millisecond, End: 5*time.Second + 777*time.Millisecond, Image: small, X: 300, Y: 400, Forced: true},
		{Start: 7 * time.Second, End: 9 * time.Second, Image: inverse, X: 300, Y: 400},
	}
	idx, sub := encodeVob(t, cues)
	// The first SPU is larger than one pack, so it spans several PES packets.
	if !bytes.Contains(sub[bitmapsubtest.PackSize:], []byte{0, 0, 1, 0xBD}) || bytes.Count(sub, []byte{0, 0, 1, 0xBA}) < 4 {
		t.Fatalf("expected a multi-pack SPU, sub is %d bytes", len(sub))
	}
	evs, st, err := decodeVob(t, idx, sub)
	if err != nil || len(evs) != 3 || st.Skipped != 0 {
		t.Fatalf("decode: %v %+v %d", err, st, len(evs))
	}
	for i, c := range cues {
		e := evs[i]
		if !near(e.Start, c.Start, vobStartTolerance) || !near(e.End, c.End, vobEndTolerance) || e.Forced != c.Forced {
			t.Fatalf("event %d %v-%v forced=%v want %v-%v", i, e.Start, e.End, e.Forced, c.Start, c.End)
		}
		if e.Bitmap.Width != c.Image.Rect.Dx() || e.Bitmap.Height != c.Image.Rect.Dy() {
			t.Fatalf("event %d size %dx%d", i, e.Bitmap.Width, e.Bitmap.Height)
		}
		// Four colors cannot match pixel values; the OCR ink must.
		want := OCRImage(toBitmap(c.Image, func(r, g, b uint8) uint8 {
			return uint8((299*int(r) + 587*int(g) + 114*int(b) + 500) / 1000)
		}))
		if m := inkMismatch(OCRImage(e.Bitmap), want); m > 0.02 {
			t.Fatalf("event %d OCR ink mismatch %.4f", i, m)
		}
	}
}

// TestVobSubExactPixels covers every code length, run-to-end-of-line, runs
// longer than one code and field interlacing with known values.
func TestVobSubExactPixels(t *testing.T) {
	const w, h = 300, 5
	px := make([]uint8, w*h)
	fill := func(y, x0, n int, v uint8) {
		for x := x0; x < x0+n; x++ {
			px[y*w+x] = v
		}
	}
	fill(0, 0, 2, 1)    // 1-nibble code
	fill(0, 2, 5, 2)    // 2 nibbles
	fill(0, 7, 20, 3)   // 3 nibbles
	fill(0, 27, 100, 1) // 4 nibbles; the rest of row 0 is a run to the end
	fill(1, 0, 300, 2)  // odd row: bottom field, whole row
	fill(2, 0, 280, 3)  // longer than one code
	fill(2, 280, 20, 1)
	fill(3, 150, 3, 1)
	fill(4, 299, 1, 2)
	spu := mustSPU(t, bitmapsubtest.SPU{
		Pixels: px, Width: w, Height: h, X: 10, Y: 20,
		Palette: [4]uint8{0, 1, 2, 3}, Alpha: [4]uint8{0, 15, 8, 15}, Stop: 2 * time.Second,
	})
	evs, _, err := decodeVob(t, idxText(0, stamp(time.Second, 0)), psSPU(t, spu, 0))
	if err != nil || len(evs) != 1 {
		t.Fatalf("decode: %v %d", err, len(evs))
	}
	e := evs[0]
	if e.Start != time.Second || !near(e.End, 3*time.Second, 6*time.Millisecond) || e.Bitmap.Width != w || e.Bitmap.Height != h {
		t.Fatalf("event %v-%v %dx%d", e.Start, e.End, e.Bitmap.Width, e.Bitmap.Height)
	}
	luma := [4]uint8{0, 255, 128, 76}
	alpha := [4]uint8{0, 255, 136, 255}
	for i, v := range px {
		if e.Bitmap.Alpha[i] != alpha[v] || (alpha[v] != 0 && e.Bitmap.Luma[i] != luma[v]) {
			t.Fatalf("pixel %d,%d luma %d alpha %d want value %d", i%w, i/w, e.Bitmap.Luma[i], e.Bitmap.Alpha[i], v)
		}
	}
}

func smallSPU(t testing.TB, start, stop time.Duration, forced bool) []byte {
	t.Helper()
	return mustSPU(t, bitmapsubtest.SPU{
		Pixels: []uint8{1, 2, 3, 1, 2, 3}, Width: 3, Height: 2,
		Palette: [4]uint8{0, 1, 2, 3}, Alpha: [4]uint8{0, 15, 15, 15}, Start: start, Stop: stop, Forced: forced,
	})
}

func TestVobSubTimingAndStreams(t *testing.T) {
	var sub bytes.Buffer
	var stamps []string
	add := func(ts time.Duration, stream int, spu []byte) {
		stamps = append(stamps, stamp(ts, sub.Len()))
		sub.Write(psSPU(t, spu, stream))
	}
	add(time.Second, 0, smallSPU(t, 100*time.Millisecond, -1, false))    // no stop: ends at the next start
	add(2*time.Second, 0, smallSPU(t, 0, 2*time.Minute, true))           // clamped to MaxDuration
	add(time.Second, 0, smallSPU(t, 0, time.Second, false))              // backwards: dropped
	add(100*time.Second, 0, smallSPU(t, 500*time.Millisecond, 0, false)) // stop before start: default
	add(200*time.Second, 0, smallSPU(t, 0, -1, false))                   // last without stop: default
	idx := idxText(0, stamps...)
	// A second id section is ignored.
	idx = append(idx, []byte("id: de, index: 1\n"+stamp(500*time.Second, 0)+"\n")...)
	evs, st, err := decodeVob(t, idx, sub.Bytes())
	if err != nil || len(evs) != 4 || st.Skipped != 1 {
		t.Fatalf("decode: %v %+v %d", err, st, len(evs))
	}
	want := [][2]time.Duration{
		{1100 * time.Millisecond, 2 * time.Second},
		{2 * time.Second, 2*time.Second + MaxDuration},
		{100*time.Second + 500*time.Millisecond, 105*time.Second + 500*time.Millisecond},
		{200 * time.Second, 200*time.Second + DefaultDuration},
	}
	for i, e := range evs {
		if !near(e.Start, want[i][0], 6*time.Millisecond) || !near(e.End, want[i][1], 6*time.Millisecond) {
			t.Fatalf("event %d %v-%v want %v", i, e.Start, e.End, want[i])
		}
	}
	if !evs[1].Forced || evs[0].Forced {
		t.Fatal("forced flag")
	}
}

func TestVobSubStreamSelectionAndPacketKinds(t *testing.T) {
	spu := smallSPU(t, 0, time.Second, false)
	var sub bytes.Buffer
	// Packets of other streams before the selected substream 0x21.
	sub.Write(psSPU(t, smallSPU(t, 0, time.Second, false), 0))
	sub.Write([]byte{0, 0, 1, 0xE0, 0, 3, 1, 2, 3})
	sub.Write([]byte{0, 0, 1, 0xBE, 0, 2, 0xFF, 0xFF})
	sub.Write([]byte{0, 0, 1, 0xBD, 0, 4, 0x81, 0, 0, 0x80}) // substream 0x80 (AC-3)
	// An MPEG-1 pack and PES (stuffing, STD buffer, PTS) carrying the SPU in
	// two packets; the second uses the no-PTS marker.
	sub.Write([]byte{0, 0, 1, 0xBA, 0x21, 0, 1, 0, 1, 0x80, 0, 1})
	half := len(spu) / 2
	pes1 := append([]byte{0xFF, 0xFF, 0x40, 0x20, 0x21, 0, 1, 0, 1, 0x21}, spu[:half]...)
	sub.Write(append([]byte{0, 0, 1, 0xBD, byte(len(pes1) >> 8), byte(len(pes1))}, pes1...))
	pes2 := append([]byte{0x0F, 0x21}, spu[half:]...)
	sub.Write(append([]byte{0, 0, 1, 0xBD, byte(len(pes2) >> 8), byte(len(pes2))}, pes2...))
	idx := idxText(1, stamp(time.Second, 0))
	evs, st, err := decodeVob(t, idx, sub.Bytes())
	if err != nil || len(evs) != 1 || st.Skipped != 0 || evs[0].Bitmap.Width != 3 {
		t.Fatalf("decode: %v %+v %d", err, st, len(evs))
	}
	// Without an index the first subpicture substream is taken.
	noIndex := bytes.Replace(idx, []byte("id: en, index: 1"), []byte("id: en"), 1)
	if evs, _, err := decodeVob(t, noIndex, sub.Bytes()); err != nil || len(evs) != 1 {
		t.Fatalf("no index: %v %d", err, len(evs))
	}
	// MPEG-1 PES with a 0x30 (PTS+DTS) marker.
	pes3 := append([]byte{0x31, 0, 1, 0, 1, 0x11, 0, 1, 0, 1, 0x20}, spu...)
	alt := append([]byte{0, 0, 1, 0xBD, byte(len(pes3) >> 8), byte(len(pes3))}, pes3...)
	if evs, _, err := decodeVob(t, idxText(0, stamp(time.Second, 0)), alt); err != nil || len(evs) != 1 {
		t.Fatalf("mpeg-1 pts+dts: %v %d", err, len(evs))
	}
}

// rawSPU assembles an SPU from a body (after the 4-byte header) and a
// control sequence offset.
func rawSPU(ctrl int, body []byte) []byte {
	b := append([]byte{0, 0, byte(ctrl >> 8), byte(ctrl)}, body...)
	b[0], b[1] = byte(len(b)>>8), byte(len(b))
	return b
}

func TestVobSubCorruptPicturesAreSkipped(t *testing.T) {
	good := smallSPU(t, 0, time.Second, false)
	// Each variant: 4 header bytes, two one-byte RLE rows at 4 and 5, the
	// control sequence at 6.
	coords := func(x1, x2, y1, y2 int) []byte {
		return []byte{0x05, byte(x1 >> 4), byte(x1<<4 | x2>>8), byte(x2), byte(y1 >> 4), byte(y1<<4 | y2>>8), byte(y2)}
	}
	ctl := func(next int, cmds ...byte) []byte {
		return append([]byte{0, 0, byte(next >> 8), byte(next)}, cmds...)
	}
	rle := []byte{0x40, 0x40} // two 1-pixel rows: run 1 color 0, aligned
	body := func(cmds []byte) []byte { return append(append([]byte{}, rle...), cmds...) }
	fields := []byte{0x06, 0, 4, 0, 5}
	valid := append(append(coords(0, 0, 0, 1), fields...), 0x01, 0xFF)
	cases := map[string][]byte{
		"ctrl offset too small": rawSPU(2, body(ctl(6, valid...))),
		"ctrl offset beyond":    rawSPU(200, body(ctl(6, valid...))),
		"unknown command":       rawSPU(6, body(ctl(6, 0x07, 0xFF))),
		"unterminated commands": rawSPU(6, body(ctl(6, 0x01))),
		"cut argument":          rawSPU(6, body(ctl(6, 0x05, 1, 2))),
		"no coordinates":        rawSPU(6, body(ctl(6, append(fields, 0xFF)...))),
		"no rle offsets":        rawSPU(6, body(ctl(6, append(coords(0, 0, 0, 1), 0xFF)...))),
		"inverted coordinates":  rawSPU(6, body(ctl(6, append(append(coords(5, 0, 0, 1), fields...), 0xFF)...))),
		"too large":             rawSPU(6, body(ctl(6, append(append(coords(0, 4095, 0, 4095), fields...), 0xFF)...))),
		"rle offset beyond":     rawSPU(6, body(ctl(6, append(append(coords(0, 0, 0, 1), 0x06, 0, 4, 0, 99), 0xFF)...))),
		"rle wider than row":    rawSPU(6, append([]byte{0x84, 0x84}, ctl(6, valid...)...)),
		"rle truncated":         rawSPU(6, body(ctl(6, append(append(coords(0, 600, 0, 1), 0x06, 0, 22, 0, 22), 0xFF)...))),
		"next beyond end":       rawSPU(6, body(ctl(400, valid...))),
	}
	var sub bytes.Buffer
	var stamps []string
	n := 0
	for _, spu := range cases {
		stamps = append(stamps, stamp(time.Duration(n)*time.Second, sub.Len()))
		sub.Write(psSPU(t, spu, 0))
		n++
	}
	stamps = append(stamps, stamp(time.Duration(n)*time.Second, sub.Len()))
	sub.Write(psSPU(t, good, 0))
	evs, st, err := decodeVob(t, idxText(0, stamps...), sub.Bytes())
	if err != nil || len(evs) != 1 || st.Skipped != len(cases) {
		t.Fatalf("decode: %v %+v %d", err, st, len(evs))
	}
}

func TestVobSubControlLoopTerminates(t *testing.T) {
	// Sequence A at 6 points to B at 12; B points back to A.
	rle := []byte{0x40, 0x40}
	a := []byte{0, 0, 0, 12, 0xFF}
	pad := []byte{0}
	b := append([]byte{0, 0, 0, 6, 0x05, 0, 0, 0, 0, 0, 1, 0x06, 0, 4, 0, 5, 0x01}, 0xFF)
	spu := rawSPU(6, append(append(append(rle, a...), pad...), b...))
	evs, _, err := decodeVob(t, idxText(0, stamp(time.Second, 0)), psSPU(t, spu, 0))
	if err != nil || len(evs) != 1 {
		t.Fatalf("decode: %v %d", err, len(evs))
	}
}

func TestVobSubStreamFaults(t *testing.T) {
	spu := smallSPU(t, 0, time.Second, false)
	ps := psSPU(t, spu, 0)
	cases := map[string][]byte{
		"no start code":      append([]byte{1, 2, 3, 4}, ps...),
		"bad pack header":    {0, 0, 1, 0xBA, 0x00, 0, 0, 0},
		"program end":        {0, 0, 1, 0xB9},
		"short pack":         {0, 0, 1, 0xBA, 0x44, 0},
		"bad mpeg-1 pes":     {0, 0, 1, 0xBD, 0, 3, 0x55, 0x20, 0},
		"mpeg-1 pes no data": {0, 0, 1, 0xBD, 0, 2, 0xFF, 0xFF},
		"mpeg-2 header cut":  {0, 0, 1, 0xBD, 0, 3, 0x81, 0, 9},
		"spu size cut":       {0, 0, 1, 0xBD, 0, 5, 0x81, 0, 0, 0x20, 0},
		"spu size too small": {0, 0, 1, 0xBD, 0, 6, 0x81, 0, 0, 0x20, 0, 2},
		"spu never complete": {0, 0, 1, 0xBD, 0, 8, 0x81, 0, 0, 0x20, 0, 50, 0, 6},
	}
	for name, sub := range cases {
		_, st, err := decodeVob(t, idxText(0, stamp(time.Second, 0)), sub)
		if err != nil || st.Skipped != 1 {
			t.Fatalf("%s: %v %+v", name, err, st)
		}
	}
}

func TestVobSubIndexErrors(t *testing.T) {
	pal := "palette: " + strings.Repeat("000000, ", 15) + "ffffff\n"
	sub := psSPU(t, smallSPU(t, 0, time.Second, false), 0)
	cases := []struct {
		name, idx string
		want      error
	}{
		{"no palette", "id: en, index: 0\n", ErrInvalid},
		{"short palette", "palette: 000000, ffffff\n", ErrInvalid},
		{"bad palette hex", "palette: " + strings.Repeat("00000g, ", 15) + "ffffff\n", ErrInvalid},
		{"long palette entry", "palette: " + strings.Repeat("0000000, ", 15) + "ffffff\n", ErrInvalid},
		{"stream index", pal + "id: en, index: 40\n", ErrInvalid},
		{"timestamp without filepos", pal + "timestamp: 00:00:01:000\n", ErrInvalid},
		{"filepos key", pal + "timestamp: 00:00:01:000, offset: 0\n", ErrInvalid},
		{"filepos hex", pal + "timestamp: 00:00:01:000, filepos: zz\n", ErrInvalid},
		{"timestamp fields", pal + "timestamp: 00:01:000, filepos: 0\n", ErrInvalid},
		{"timestamp minutes", pal + "timestamp: 00:61:00:000, filepos: 0\n", ErrInvalid},
		{"timestamp sign", pal + "timestamp: +0:00:00:000, filepos: 0\n", ErrInvalid},
		{"negative timestamp", pal + "timestamp: -1:00:00:000, filepos: 0\n", ErrInvalid},
		{"filepos beyond", pal + "timestamp: 00:00:01:000, filepos: ffffff\n", ErrInvalid},
		{"oversized index", strings.Repeat("#", MaxIndexBytes+1), ErrTooLarge},
	}
	for _, c := range cases {
		if _, _, err := decodeVob(t, []byte(c.idx), sub); !errors.Is(err, c.want) {
			t.Fatalf("%s: got %v want %v", c.name, err, c.want)
		}
	}
	// Comments, blank lines and unknown keys are ignored.
	ok := "# comment\n\nsize: 720x480\ndelay: 0\nnot a key line\n" + pal + "id: en, index: 0\n" + stamp(time.Second, 0) + "\n"
	if evs, _, err := decodeVob(t, []byte(ok), sub); err != nil || len(evs) != 1 {
		t.Fatalf("lenient index: %v %d", err, len(evs))
	}
	if _, err := DecodeVobSub(nil, nil, -1, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil args: %v", err)
	}
	if _, err := DecodeVobSub(errReader{}, bytes.NewReader(sub), 1, func(Event) error { return nil }); err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("index read error: %v", err)
	}
}

type errReaderAt struct{}

func (errReaderAt) ReadAt([]byte, int64) (int, error) { return 0, errors.New("bad sector") }

func TestVobSubBoundsAndCancel(t *testing.T) {
	var sub bytes.Buffer
	var stamps []string
	for i := range 4 {
		stamps = append(stamps, stamp(time.Duration(i)*time.Second, sub.Len()))
		sub.Write(psSPU(t, smallSPU(t, 0, 500*time.Millisecond, false), 0))
	}
	idx := idxText(0, stamps...)
	stop := errors.New("stop")
	st, err := DecodeVobSub(bytes.NewReader(idx), bytes.NewReader(sub.Bytes()), int64(sub.Len()), func(Event) error { return stop })
	if !errors.Is(err, stop) || st.Events != 1 {
		t.Fatalf("visit error: %v %+v", err, st)
	}
	if _, err := DecodeVobSub(bytes.NewReader(idx), errReaderAt{}, int64(sub.Len()), func(Event) error { return nil }); err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("sub read error: %v", err)
	}
	setMaxEvents(t, 3)
	if _, _, err := decodeVob(t, idx, sub.Bytes()); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("timestamp bound: %v", err)
	}
	// The picture bound also holds when the index is within the bound.
	setMaxEvents(t, 4)
	em := emitter{visit: func(Event) error { return nil }}
	for range 4 {
		if err := em.picture(); err != nil {
			t.Fatal(err)
		}
	}
	if err := em.picture(); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("picture bound: %v", err)
	}
}

func TestVobSubTruncatedPrefixes(t *testing.T) {
	big := render(t, "Truncate\nthis", 30, whiteOnBlack)
	idx, sub := encodeVob(t, []bitmapsubtest.Cue{
		{Start: time.Second, End: 2 * time.Second, Image: big, X: 10, Y: 10},
		{Start: 3 * time.Second, End: 4 * time.Second, Image: big, X: 10, Y: 10},
	})
	for n := range len(sub) {
		_, _, err := decodeVob(t, idx, sub[:n])
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Fatalf("sub prefix %d: %v", n, err)
		}
	}
	for n := range len(idx) {
		_, _, err := decodeVob(t, idx[:n], sub)
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Fatalf("idx prefix %d: %v", n, err)
		}
	}
}
