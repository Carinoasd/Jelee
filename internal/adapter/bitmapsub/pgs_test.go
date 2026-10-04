package bitmapsub

import (
	"bytes"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
)

// PTS has 1/90000 s resolution; the encoder rounds to the nearest tick.
const ptsTolerance = 12 * time.Microsecond

func TestPGSRoundTrip(t *testing.T) {
	a := render(t, "Hello, world", 48, whiteOnBlack)
	b := render(t, "Second line\nwith two rows", 40, bitmapsubtest.Style{Fill: yellow, Outline: black, OutlineWidth: 3})
	cues := []bitmapsubtest.Cue{
		{Start: time.Second, End: 3500 * time.Millisecond, Image: a, X: 100, Y: 900},
		{Start: 4 * time.Second, End: 6*time.Second + 123*time.Millisecond, Image: b, X: 300, Y: 800, Forced: true},
	}
	evs, st, err := decodePGS(t, encodePGS(t, cues))
	if err != nil || st.Events != 2 || st.Skipped != 0 || len(evs) != 2 {
		t.Fatalf("decode: %v %+v %d", err, st, len(evs))
	}
	for i, c := range cues {
		e := evs[i]
		if !near(e.Start, c.Start, ptsTolerance) || !near(e.End, c.End, ptsTolerance) {
			t.Fatalf("event %d time %v-%v want %v-%v", i, e.Start, e.End, c.Start, c.End)
		}
		if e.Forced != c.Forced {
			t.Fatalf("event %d forced %v", i, e.Forced)
		}
		want := toBitmap(c.Image, luma709)
		if e.Bitmap.Width != want.Width || e.Bitmap.Height != want.Height {
			t.Fatalf("event %d size %dx%d want %dx%d", i, e.Bitmap.Width, e.Bitmap.Height, want.Width, want.Height)
		}
		// Quantization to 255 palette entries and limited-range luma allow
		// a small error per pixel.
		for p := range want.Alpha {
			if d := int(e.Bitmap.Alpha[p]) - int(want.Alpha[p]); d > 8 || d < -8 {
				t.Fatalf("event %d pixel %d alpha %d want %d", i, p, e.Bitmap.Alpha[p], want.Alpha[p])
			}
			if want.Alpha[p] < 16 {
				continue
			}
			if d := int(e.Bitmap.Luma[p]) - int(want.Luma[p]); d > 8 || d < -8 {
				t.Fatalf("event %d pixel %d luma %d want %d", i, p, e.Bitmap.Luma[p], want.Luma[p])
			}
		}
		if m := inkMismatch(OCRImage(e.Bitmap), OCRImage(want)); m > 0.005 {
			t.Fatalf("event %d OCR image mismatch %.4f", i, m)
		}
	}
}

// pgsSet writes one display set: PCS, optional PDS and ODS, END.
type pgsObj struct {
	id  uint16
	img *image.NRGBA
}

func writeSet(t *testing.T, w *bitmapsubtest.PGSWriter, pts time.Duration, state uint8, place []bitmapsubtest.PCSObject, pal []bitmapsubtest.PaletteEntry, objs []pgsObj, frag int) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.PCS(pts, 1920, 1080, 0, state, 0, place))
	must(w.WDS(pts, []image.Rectangle{image.Rect(0, 0, 1920, 1080)}))
	if pal != nil {
		must(w.PDS(pts, 0, 0, pal))
	}
	for _, o := range objs {
		iw, ih := o.img.Rect.Dx(), o.img.Rect.Dy()
		idx := solidIndices(o.img)
		must(w.ODS(pts, o.id, 0, iw, ih, bitmapsubtest.EncodeRLE(idx, iw, ih), frag))
	}
	must(w.END(pts))
}

// grayPalette maps index i to luma i (limited range) and full opacity;
// index 0 is transparent.
func grayPalette() []bitmapsubtest.PaletteEntry {
	p := []bitmapsubtest.PaletteEntry{{Index: 0, Y: 16, Cr: 128, Cb: 128}}
	for i := 1; i < 256; i++ {
		p = append(p, bitmapsubtest.PaletteEntry{Index: uint8(i), Y: uint8(16 + (i*219)/255), Cr: 128, Cb: 128, A: 255})
	}
	return p
}

// pattern returns an image whose pixel index is a known function of (x, y).
func pattern(w, h int, seed uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			v := uint8((x/3+y*7)%5) * 50
			if v != 0 {
				v += seed
			}
			o := img.PixOffset(x, y)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = v, v, v, 255
		}
	}
	return img
}

// solidIndices uses the red channel as palette index (0 = transparent).
func solidIndices(img *image.NRGBA) []uint8 {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	idx := make([]uint8, w*h)
	for y := range h {
		for x := range w {
			idx[y*w+x] = img.Pix[img.PixOffset(x, y)]
		}
	}
	return idx
}

func indexAt(img *image.NRGBA, x, y int) uint8 { return img.Pix[img.PixOffset(x, y)] }

func expectLuma(i uint8) uint8 { return studioToFull(uint8(16 + (int(i)*219)/255)) }

func TestPGSMultiObjectCropAndReuse(t *testing.T) {
	var buf bytes.Buffer
	w := &bitmapsubtest.PGSWriter{W: &buf}
	o0, o1 := pattern(40, 10, 1), pattern(30, 12, 2)
	crop := image.Rect(5, 2, 25, 9)
	// Display set 1: two objects, the second cropped, in fragments.
	writeSet(t, w, time.Second, bitmapsubtest.StateEpochStart, []bitmapsubtest.PCSObject{
		{ID: 0, X: 100, Y: 50},
		{ID: 7, X: 120, Y: 70, Crop: &crop, Forced: true},
	}, grayPalette(), []pgsObj{{0, o0}, {7, o1}}, 33)
	// Display set 2: same epoch, object 0 reused at a new place without ODS.
	writeSet(t, w, 2*time.Second, bitmapsubtest.StateNormal, []bitmapsubtest.PCSObject{{ID: 0, X: 10, Y: 20}}, nil, nil, 0)
	// Display set 3: an acquisition point repeating the same screen merges.
	writeSet(t, w, 3*time.Second, bitmapsubtest.StateAcquisition, []bitmapsubtest.PCSObject{{ID: 0, X: 10, Y: 20}}, nil, nil, 0)
	// Display set 4: clear.
	writeSet(t, w, 4*time.Second, bitmapsubtest.StateNormal, nil, nil, nil, 0)

	evs, st, err := decodePGS(t, buf.Bytes())
	if err != nil || st.Events != 2 || len(evs) != 2 {
		t.Fatalf("decode: %v %+v", err, st)
	}
	e := evs[0]
	// Canvas: union of (100,50,140,60) and (120,70,140,77).
	if e.Bitmap.Width != 40 || e.Bitmap.Height != 27 || !e.Forced || e.Start != time.Second || e.End != 2*time.Second {
		t.Fatalf("composed %dx%d forced=%v %v-%v", e.Bitmap.Width, e.Bitmap.Height, e.Forced, e.Start, e.End)
	}
	for y := range 27 {
		for x := range 40 {
			var want uint8
			opaque := false
			switch {
			case y < 10:
				want, opaque = indexAt(o0, x, y), true
			case y >= 20 && x >= 20:
				want, opaque = indexAt(o1, x-20+crop.Min.X, y-20+crop.Min.Y), true
			}
			got := e.Bitmap.Alpha[y*40+x]
			if !opaque || want == 0 {
				if got != 0 {
					t.Fatalf("pixel %d,%d should be transparent", x, y)
				}
				continue
			}
			if got != 255 || e.Bitmap.Luma[y*40+x] != expectLuma(want) {
				t.Fatalf("pixel %d,%d luma %d alpha %d want %d", x, y, e.Bitmap.Luma[y*40+x], got, expectLuma(want))
			}
		}
	}
	e = evs[1]
	if e.Bitmap.Width != 40 || e.Bitmap.Height != 10 || e.Start != 2*time.Second || e.End != 4*time.Second || e.Forced {
		t.Fatalf("reused object event %dx%d %v-%v", e.Bitmap.Width, e.Bitmap.Height, e.Start, e.End)
	}
}

func TestPGSPaletteUpdateAndEpochReset(t *testing.T) {
	var buf bytes.Buffer
	w := &bitmapsubtest.PGSWriter{W: &buf}
	o := pattern(8, 4, 0)
	writeSet(t, w, time.Second, bitmapsubtest.StateEpochStart, []bitmapsubtest.PCSObject{{ID: 1}}, grayPalette(), []pgsObj{{1, o}}, 0)
	// A palette-only update changes the colors of the same object.
	if err := w.PCS(2*time.Second, 1920, 1080, 1, 0, 0, []bitmapsubtest.PCSObject{{ID: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := w.PDS(2*time.Second, 0, 1, []bitmapsubtest.PaletteEntry{{Index: 50, Y: 235, A: 128}}); err != nil {
		t.Fatal(err)
	}
	if err := w.END(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	// A new epoch forgets object 1: referencing it is skipped.
	writeSet(t, w, 3*time.Second, bitmapsubtest.StateEpochStart, []bitmapsubtest.PCSObject{{ID: 1}}, grayPalette(), nil, 0)
	evs, st, err := decodePGS(t, buf.Bytes())
	if err != nil || len(evs) != 2 || st.Skipped != 1 {
		t.Fatalf("decode: %v %+v %d", err, st, len(evs))
	}
	if evs[1].Bitmap.Luma[3] != 255 || evs[1].Bitmap.Alpha[3] != 128 || evs[0].Bitmap.Alpha[3] != 255 {
		t.Fatalf("palette update not applied: %d %d", evs[1].Bitmap.Luma[3], evs[1].Bitmap.Alpha[3])
	}
	if evs[1].End != 3*time.Second {
		t.Fatalf("skipped picture must end the previous event, got %v", evs[1].End)
	}
}

func TestPGSTiming(t *testing.T) {
	img := pattern(4, 2, 0)
	var buf bytes.Buffer
	w := &bitmapsubtest.PGSWriter{W: &buf}
	show := func(pts time.Duration) {
		writeSet(t, w, pts, bitmapsubtest.StateEpochStart, []bitmapsubtest.PCSObject{{ID: 0}}, grayPalette(), []pgsObj{{0, img}}, 0)
	}
	show(10 * time.Second)
	writeSet(t, w, 100*time.Second, 0, nil, nil, nil, 0) // clamp to MaxDuration
	show(200 * time.Second)
	show(150 * time.Second)                              // backwards: dropped
	writeSet(t, w, 200*time.Second, 0, nil, nil, nil, 0) // end == start: default
	show(300 * time.Second)                              // last, no end: default
	evs, st, err := decodePGS(t, buf.Bytes())
	if err != nil || len(evs) != 3 || st.Skipped != 1 {
		t.Fatalf("decode: %v %+v", err, st)
	}
	want := [][2]time.Duration{
		{10 * time.Second, 10*time.Second + MaxDuration},
		{200 * time.Second, 200*time.Second + DefaultDuration},
		{300 * time.Second, 300*time.Second + DefaultDuration},
	}
	for i, e := range evs {
		if e.Start != want[i][0] || e.End != want[i][1] {
			t.Fatalf("event %d %v-%v want %v", i, e.Start, e.End, want[i])
		}
	}
}

func TestPGSVisitErrorAndEventBound(t *testing.T) {
	img := render(t, "x", 20, whiteOnBlack)
	var cues []bitmapsubtest.Cue
	for i := range 5 {
		cues = append(cues, bitmapsubtest.Cue{Start: time.Duration(i) * time.Second, End: time.Duration(i)*time.Second + 500*time.Millisecond, Image: img})
	}
	data := encodePGS(t, cues)
	stop := errors.New("stop")
	n := 0
	st, err := DecodePGS(bytes.NewReader(data), func(Event) error { n++; return stop })
	if !errors.Is(err, stop) || n != 1 || st.Events != 1 {
		t.Fatalf("visit error: %v n=%d %+v", err, n, st)
	}
	setMaxEvents(t, 3)
	if _, _, err := decodePGS(t, data); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("event bound: %v", err)
	}
}

func TestTrackPixelBudget(t *testing.T) {
	img := render(t, "budget", 20, whiteOnBlack)
	area := int64(img.Rect.Dx() * img.Rect.Dy())
	old := maxTrackPixels
	maxTrackPixels = 2*area + 1
	t.Cleanup(func() { maxTrackPixels = old })
	var cues []bitmapsubtest.Cue
	for i := range 3 {
		cues = append(cues, bitmapsubtest.Cue{Start: time.Duration(i) * time.Second, End: time.Duration(i)*time.Second + 500*time.Millisecond, Image: img})
	}
	evs, _, err := decodePGS(t, encodePGS(t, cues))
	if !errors.Is(err, ErrTooLarge) || len(evs) != 2 {
		t.Fatalf("pgs budget: %v %d", err, len(evs))
	}
	idx, sub := encodeVob(t, cues)
	evs, _, err = decodeVob(t, idx, sub)
	if !errors.Is(err, ErrTooLarge) || len(evs) != 1 {
		t.Fatalf("vobsub budget: %v %d", err, len(evs))
	}
}

func TestDecodePGSRLE(t *testing.T) {
	type run struct {
		x, y, n int
		c       uint8
	}
	cases := []struct {
		name string
		data []byte
		w, h int
		runs []run
		ok   bool
	}{
		{"all forms", []byte{
			5,    // 1 pixel color 5
			0, 2, // 2 pixels color 0
			0, 0x40, 3, // 3 pixels color 0 (long form)
			0, 0x82, 9, // 2 pixels color 9
			0, 0xC0, 2, 7, // 2 pixels color 7 (long form)
			0, 0, // end of line
			0, 0, 0, 0, // empty lines are fine
		}, 10, 3, []run{{0, 0, 1, 5}, {1, 0, 2, 0}, {3, 0, 3, 0}, {6, 0, 2, 9}, {8, 0, 2, 7}}, true},
		{"short rows stay transparent", []byte{1, 0, 0}, 4, 4, []run{{0, 0, 1, 1}}, true},
		{"width overflow", []byte{0, 0x85, 1}, 4, 1, []run{}, false},
		{"beyond height", []byte{1, 0, 0, 1}, 4, 1, []run{{0, 0, 1, 1}}, false},
		{"cut after escape", []byte{0}, 4, 1, nil, false},
		{"cut long run", []byte{0, 0x40}, 4, 1, nil, false},
		{"cut color", []byte{0, 0x81}, 4, 1, nil, false},
	}
	for _, c := range cases {
		var got []run
		err := decodePGSRLE(c.data, c.w, c.h, func(x, y, n int, col uint8) { got = append(got, run{x, y, n, col}) })
		if (err == nil) != c.ok {
			t.Fatalf("%s: err %v", c.name, err)
		}
		if c.runs != nil && len(got) != len(c.runs) {
			t.Fatalf("%s: runs %v want %v", c.name, got, c.runs)
		}
		for i := range c.runs {
			if got[i] != c.runs[i] {
				t.Fatalf("%s: run %d %v want %v", c.name, i, got[i], c.runs[i])
			}
		}
	}
}

func segment(typ byte, payload []byte) []byte {
	var b bytes.Buffer
	w := &bitmapsubtest.PGSWriter{W: &b}
	_ = w.Segment(0, typ, payload)
	return b.Bytes()
}

func odsFirst(id uint16, declared, w, h int, data []byte, last bool) []byte {
	seq := byte(0x80)
	if last {
		seq |= 0x40
	}
	p := []byte{byte(id >> 8), byte(id), 0, seq, byte(declared >> 16), byte(declared >> 8), byte(declared), byte(w >> 8), byte(w), byte(h >> 8), byte(h)}
	return segment(bitmapsubtest.SegODS, append(p, data...))
}

func TestPGSMalformed(t *testing.T) {
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	pcs := func(n byte, extra ...byte) []byte {
		return segment(bitmapsubtest.SegPCS, append([]byte{7, 128, 4, 56, 0x10, 0, 0, 0x80, 0, 0, n}, extra...))
	}
	cont := segment(bitmapsubtest.SegODS, []byte{0, 1, 0, 0x40, 1, 2, 3})
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"bad magic", []byte("XG\x00\x00\x00\x00\x00\x00\x00\x00\x80\x00\x00"), ErrInvalid},
		{"short pcs", segment(bitmapsubtest.SegPCS, []byte{1, 2, 3}), ErrInvalid},
		{"too many objects", pcs(3), ErrTooLarge},
		{"short pcs object", pcs(1, 0, 0, 0), ErrInvalid},
		{"short crop", pcs(1, 0, 0, 0, 0x80, 0, 0, 0, 0, 1, 2), ErrInvalid},
		{"pds length", segment(bitmapsubtest.SegPDS, []byte{0, 0, 1, 2, 3}), ErrInvalid},
		{"pds empty", segment(bitmapsubtest.SegPDS, []byte{0}), ErrInvalid},
		{"short ods", segment(bitmapsubtest.SegODS, []byte{0, 1}), ErrInvalid},
		{"short ods header", segment(bitmapsubtest.SegODS, []byte{0, 1, 0, 0x80, 0, 0}), ErrInvalid},
		{"zero width", odsFirst(1, 4, 0, 10, nil, true), ErrInvalid},
		{"huge width", odsFirst(1, 100, MaxCanvas+1, 10, nil, true), ErrTooLarge},
		{"huge area", odsFirst(1, 100, 4096, 4096, nil, true), ErrTooLarge},
		{"declared too large", odsFirst(1, 1000, 4, 4, nil, false), ErrTooLarge},
		{"continuation without start", cont, ErrInvalid},
		{"fragments beyond bound", cat(odsFirst(1, 60, 4, 4, make([]byte, 40), false),
			segment(bitmapsubtest.SegODS, append([]byte{0, 1, 0, 0}, make([]byte, 40)...))), ErrTooLarge},
		{"cut header", []byte("PG\x00\x00"), ErrInvalid},
		{"cut payload", segment(bitmapsubtest.SegPDS, make([]byte, 7))[:16], ErrInvalid},
	}
	for _, c := range cases {
		if _, _, err := decodePGS(t, c.data); !errors.Is(err, c.want) {
			t.Fatalf("%s: got %v want %v", c.name, err, c.want)
		}
	}
	if _, err := DecodePGS(nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil args: %v", err)
	}
	if _, err := DecodePGS(errReader{}, func(Event) error { return nil }); err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("read error must pass through: %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestPGSObjectBounds(t *testing.T) {
	// More distinct objects than an epoch may hold.
	var many bytes.Buffer
	for i := range MaxEpochObjects + 1 {
		many.Write(odsFirst(uint16(i), 6, 1, 1, []byte{1, 0}, true))
	}
	if _, _, err := decodePGS(t, many.Bytes()); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("object count: %v", err)
	}
	// Replacing one object id repeatedly stays within the bound.
	var same bytes.Buffer
	for range MaxEpochObjects + 10 {
		same.Write(odsFirst(3, 6, 1, 1, []byte{1, 0}, true))
	}
	if _, _, err := decodePGS(t, same.Bytes()); err != nil {
		t.Fatalf("object replacement: %v", err)
	}
	// Total held data across objects is bounded.
	var big bytes.Buffer
	chunk := make([]byte, 0xFFFF-4)
	for i := range 3 {
		big.Write(odsFirst(uint16(i), 0xFFFFFF, 2400, 2300, nil, false))
		for range MaxObjectBytes / len(chunk) {
			big.Write(segment(bitmapsubtest.SegODS, append([]byte{0, byte(i), 0, 0}, chunk...)))
		}
	}
	if _, _, err := decodePGS(t, big.Bytes()); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("epoch bytes: %v", err)
	}
}

func TestPGSSkippedPictures(t *testing.T) {
	var buf bytes.Buffer
	w := &bitmapsubtest.PGSWriter{W: &buf}
	img := pattern(6, 3, 0)
	pal := grayPalette()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Missing palette id.
	must(w.PCS(time.Second, 1920, 1080, 0, 0x80, 9, []bitmapsubtest.PCSObject{{ID: 0}}))
	must(w.END(time.Second))
	// Incomplete object (first fragment only).
	must(w.PCS(2*time.Second, 1920, 1080, 0, 0x80, 0, []bitmapsubtest.PCSObject{{ID: 0}}))
	must(w.PDS(2*time.Second, 0, 0, pal))
	buf.Write(odsFirst(0, 20, 6, 3, []byte{1}, false))
	must(w.END(2 * time.Second))
	// Objects so far apart the canvas exceeds MaxCanvas.
	writeSet(t, w, 3*time.Second, 0x80, []bitmapsubtest.PCSObject{{ID: 0}, {ID: 1, X: 6000}}, pal, []pgsObj{{0, img}, {1, img}}, 0)
	// Crop rectangle outside the object.
	outside := image.Rect(50, 50, 60, 60)
	writeSet(t, w, 4*time.Second, 0, []bitmapsubtest.PCSObject{{ID: 0, Crop: &outside}}, nil, nil, 0)
	// Empty crop.
	empty := image.Rect(1, 1, 1, 1)
	writeSet(t, w, 5*time.Second, 0, []bitmapsubtest.PCSObject{{ID: 0, Crop: &empty}}, nil, nil, 0)
	// Corrupt RLE: a run wider than the object.
	must(w.PCS(6*time.Second, 1920, 1080, 0, 0x80, 0, []bitmapsubtest.PCSObject{{ID: 0}}))
	must(w.PDS(6*time.Second, 0, 0, pal))
	must(w.ODS(6*time.Second, 0, 0, 2, 2, []byte{0, 0x85, 1}, 0))
	must(w.END(6 * time.Second))
	// Unknown segment and an END without composition are ignored.
	buf.Write(segment(0x42, []byte{1, 2, 3}))
	buf.Write(segment(bitmapsubtest.SegEND, nil))
	// Finally a good picture.
	writeSet(t, w, 7*time.Second, 0x80, []bitmapsubtest.PCSObject{{ID: 0, X: 5, Y: 5}}, pal, []pgsObj{{0, img}}, 0)
	evs, st, err := decodePGS(t, buf.Bytes())
	if err != nil || len(evs) != 1 || st.Skipped != 6 {
		t.Fatalf("decode: %v %+v %d", err, st, len(evs))
	}
}

func TestPGSTruncatedPrefixes(t *testing.T) {
	data := encodePGS(t, []bitmapsubtest.Cue{
		{Start: time.Second, End: 2 * time.Second, Image: render(t, "Cut", 20, whiteOnBlack), X: 10, Y: 10},
		{Start: 3 * time.Second, End: 4 * time.Second, Image: render(t, "me", 20, whiteOnBlack), X: 10, Y: 10},
	})
	for n := range len(data) {
		_, _, err := decodePGS(t, data[:n])
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Fatalf("prefix %d: %v", n, err)
		}
	}
}
