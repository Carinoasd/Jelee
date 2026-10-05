package bitmapsubtest_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub"
	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
)

var style = bitmapsubtest.Style{Fill: color.NRGBA{R: 255, G: 255, B: 255, A: 255}, Outline: color.NRGBA{A: 255}, OutlineWidth: 2}

// collection builds a TrueType collection from single fonts by copying each
// one and shifting its table offsets.
func collection(t *testing.T, fonts ...[]byte) []byte {
	t.Helper()
	head := 12 + 4*len(fonts)
	out := make([]byte, head)
	copy(out, "ttcf")
	binary.BigEndian.PutUint32(out[4:], 0x00010000)
	binary.BigEndian.PutUint32(out[8:], uint32(len(fonts)))
	for i, f := range fonts {
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		base := len(out)
		binary.BigEndian.PutUint32(out[12+4*i:], uint32(base))
		out = append(out, f...)
		n := int(binary.BigEndian.Uint16(f[4:]))
		for r := range n {
			p := out[base+12+16*r+8:]
			binary.BigEndian.PutUint32(p, binary.BigEndian.Uint32(p)+uint32(base))
		}
	}
	return out
}

func TestRenderText(t *testing.T) {
	img, err := bitmapsubtest.RenderText(nil, "Line one\nTwo", 32, style)
	if err != nil {
		t.Fatal(err)
	}
	if img.Rect.Dx() < 100 || img.Rect.Dy() < 64 {
		t.Fatalf("canvas %v", img.Rect)
	}
	// The outline is black and the fill white somewhere; the corner is clear.
	var fill, outline bool
	for i := 0; i < len(img.Pix); i += 4 {
		fill = fill || (img.Pix[i] == 255 && img.Pix[i+3] == 255)
		outline = outline || (img.Pix[i] == 0 && img.Pix[i+3] == 255)
	}
	if !fill || !outline || img.Pix[3] != 0 {
		t.Fatalf("fill %v outline %v corner alpha %d", fill, outline, img.Pix[3])
	}
	// A collection selects its face by index.
	ttc := collection(t, goregular.TTF, gobold.TTF)
	regular, err := bitmapsubtest.RenderText(ttc, "Face", 32, bitmapsubtest.Style{Fill: style.Fill})
	if err != nil {
		t.Fatal(err)
	}
	bold, err := bitmapsubtest.RenderText(ttc, "Face", 32, bitmapsubtest.Style{Fill: style.Fill, FontIndex: 1})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(regular.Pix, bold.Pix) {
		t.Fatal("collection index ignored")
	}
	bad := []struct {
		font []byte
		text string
		size float64
		st   bitmapsubtest.Style
	}{
		{nil, "", 20, style},
		{nil, "x", 0, style},
		{nil, "x", 20, bitmapsubtest.Style{OutlineWidth: -1}},
		{nil, "x", 20, bitmapsubtest.Style{Padding: 1000}},
		{[]byte("not a font"), "x", 20, style},
		{nil, "x", 20, bitmapsubtest.Style{FontIndex: 1}},
		{nil, strings.Repeat("W", 200), 200, style},
	}
	for i, c := range bad {
		if _, err := bitmapsubtest.RenderText(c.font, c.text, c.size, c.st); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestQuantizeManyColors(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range 64 * 64 {
		img.Pix[4*i], img.Pix[4*i+1], img.Pix[4*i+2], img.Pix[4*i+3] = uint8(i), uint8(i>>4), uint8(i*7), uint8(128+i%128)
	}
	idx, entries := bitmapsubtest.Quantize(img)
	if len(entries) > 256 || len(idx) != 64*64 || entries[0].A != 0 {
		t.Fatalf("entries %d", len(entries))
	}
	// Decodes with luma close to the source.
	var b bytes.Buffer
	cue := bitmapsubtest.Cue{Start: time.Second, End: 2 * time.Second, Image: img}
	if err := bitmapsubtest.EncodePGS(&b, 100, 100, []bitmapsubtest.Cue{cue}); err != nil {
		t.Fatal(err)
	}
	n := 0
	if _, err := bitmapsub.DecodePGS(&b, func(bitmapsub.Event) error { n++; return nil }); err != nil || n != 1 {
		t.Fatalf("decode: %v %d", err, n)
	}
}

func TestEncodeRLEForms(t *testing.T) {
	row := []uint8{5, 5, 0, 0, 7, 0}
	row = append(row, bytes.Repeat([]byte{0}, 100)...)
	row = append(row, bytes.Repeat([]byte{3}, 10)...)
	row = append(row, bytes.Repeat([]byte{4}, 300)...)
	got := bitmapsubtest.EncodeRLE(row, len(row), 1)
	want := []byte{5, 5, 0, 2, 7, 0, 0x40, 101, 0, 0x8A, 3, 0, 0xC1, 0x2C, 4, 0, 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("rle % x", got)
	}
}

type failWriter struct{ after int }

func (f *failWriter) Write(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, errors.New("full")
	}
	f.after--
	return len(p), nil
}

func TestEncodePGSErrors(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	ok := bitmapsubtest.Cue{Start: time.Second, End: 2 * time.Second, Image: img}
	cases := [][]bitmapsubtest.Cue{
		{{Start: time.Second, End: time.Second, Image: img}},
		{{Start: time.Second, End: 2 * time.Second}},
		{ok, {Start: 1500 * time.Millisecond, End: 3 * time.Second, Image: img}},
		{{Start: time.Second, End: 2 * time.Second, Image: img, X: 99}},
		{{Start: 0, End: 100 * time.Hour, Image: img}},
	}
	for i, c := range cases {
		if err := bitmapsubtest.EncodePGS(&bytes.Buffer{}, 100, 100, c); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	for n := range 16 {
		if err := bitmapsubtest.EncodePGS(&failWriter{after: n}, 100, 100, []bitmapsubtest.Cue{ok}); err == nil {
			t.Fatalf("write failure after %d not reported", n)
		}
	}
	p := &bitmapsubtest.PGSWriter{W: &bytes.Buffer{}}
	if p.Segment(-time.Second, bitmapsubtest.SegEND, nil) == nil || p.Segment(0, bitmapsubtest.SegEND, make([]byte, 0x10000)) == nil {
		t.Fatal("bad segment accepted")
	}
	if p.ODS(0, 0, 0, 1, 1, make([]byte, 0xFFFFFF), 0) == nil {
		t.Fatal("oversized object accepted")
	}
	if p.ODS(0, 0, 0, 1, 1, []byte{1}, 0) != nil {
		t.Fatal("small object refused")
	}
	if (&bitmapsubtest.PGSWriter{W: &failWriter{after: 1}}).ODS(0, 0, 0, 1, 1, []byte{1}, 0) == nil {
		t.Fatal("payload write failure not reported")
	}
}

func TestSPUAndPS(t *testing.T) {
	px := []uint8{1, 2, 3, 0}
	s := bitmapsubtest.SPU{Pixels: px, Width: 2, Height: 2, Palette: [4]uint8{0, 1, 2, 3}, Alpha: [4]uint8{0, 15, 15, 15}, Stop: time.Second}
	spu, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Pad the SPU so the last pack leaves room only for PES stuffing.
	padded := append(append([]byte{}, spu...), make([]byte, 2016-len(spu))...)
	binary.BigEndian.PutUint16(padded, uint16(len(padded)))
	var sub bytes.Buffer
	if _, err := bitmapsubtest.WritePS(&sub, time.Second, 0, padded); err != nil {
		t.Fatal(err)
	}
	if sub.Len() != bitmapsubtest.PackSize || bytes.Contains(sub.Bytes(), []byte{0, 0, 1, 0xBE}) {
		t.Fatalf("stuffed pack is %d bytes", sub.Len())
	}
	idx := "palette: " + strings.Repeat("ffffff, ", 15) + "ffffff\nid: en, index: 0\ntimestamp: 00:00:01:000, filepos: 000000000\n"
	n := 0
	if _, err := bitmapsub.DecodeVobSub(strings.NewReader(idx), bytes.NewReader(sub.Bytes()), int64(sub.Len()), func(e bitmapsub.Event) error {
		n++
		if e.Bitmap.Width != 2 || e.Bitmap.Alpha[3] != 0 || e.Bitmap.Alpha[0] != 255 {
			t.Fatalf("bitmap %+v", e.Bitmap)
		}
		return nil
	}); err != nil || n != 1 {
		t.Fatalf("decode: %v %d", err, n)
	}
	bad := []bitmapsubtest.SPU{
		{Pixels: px, Width: 3, Height: 2},
		{Pixels: px, Width: 2, Height: 2, X: 4095},
		{Pixels: px, Width: 2, Height: 2, Start: -time.Second},
		{Pixels: px, Width: 2, Height: 2, Stop: time.Hour},
		{Pixels: make([]uint8, 4000*40), Width: 4000, Height: 40, Stop: -1},
	}
	for i := range bad[4].Pixels {
		bad[4].Pixels[i] = uint8(i % 4)
	}
	for i, b := range bad {
		if _, err := b.Encode(); err == nil {
			t.Fatalf("SPU %d accepted", i)
		}
	}
	if _, err := bitmapsubtest.WritePS(&sub, 0, 32, spu); err == nil {
		t.Fatal("bad substream accepted")
	}
}

func TestEncodeVobSubPaletteAndErrors(t *testing.T) {
	// Eight differently colored cues need more than 16 palette entries.
	var cues []bitmapsubtest.Cue
	for i := range 8 {
		img, err := bitmapsubtest.RenderText(nil, "c", 20, bitmapsubtest.Style{
			Fill: color.NRGBA{R: uint8(30 * i), G: 255, B: uint8(255 - 30*i), A: 255}, Outline: color.NRGBA{R: uint8(20 * i), A: 255}, OutlineWidth: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		cues = append(cues, bitmapsubtest.Cue{Start: time.Duration(i) * time.Second, End: time.Duration(i)*time.Second + 500*time.Millisecond, Image: img})
	}
	var idx, sub bytes.Buffer
	if err := bitmapsubtest.EncodeVobSub(&idx, &sub, 720, 480, cues); err != nil {
		t.Fatal(err)
	}
	n := 0
	if _, err := bitmapsub.DecodeVobSub(&idx, bytes.NewReader(sub.Bytes()), int64(sub.Len()), func(bitmapsub.Event) error { n++; return nil }); err != nil || n != 8 {
		t.Fatalf("decode: %v %d", err, n)
	}
	img := cues[0].Image
	bad := [][]bitmapsubtest.Cue{
		{{Start: time.Second, End: time.Second, Image: img}},
		{{Start: time.Second, End: 2 * time.Second}},
		{{Start: time.Second, End: 2 * time.Second, Image: img, X: 719}},
		{{Start: time.Second, End: time.Hour, Image: img}},
	}
	for i, c := range bad {
		if err := bitmapsubtest.EncodeVobSub(&bytes.Buffer{}, &bytes.Buffer{}, 720, 480, c); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	if err := bitmapsubtest.EncodeVobSub(&failWriter{}, &bytes.Buffer{}, 720, 480, cues[:1]); err == nil {
		t.Fatal("index write failure not reported")
	}
	if err := bitmapsubtest.EncodeVobSub(&bytes.Buffer{}, &failWriter{}, 720, 480, cues[:1]); err == nil {
		t.Fatal("sub write failure not reported")
	}
}
