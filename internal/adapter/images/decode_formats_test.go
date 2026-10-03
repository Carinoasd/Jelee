package images

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

// 32x16 WebP images made by libwebp (Pillow 12.3): left half red, right half
// blue. The alpha variant has a transparent left half, a VP8X header and a
// VP8L-compressed ALPH chunk.
var (
	webpLossyFixture = []byte("\x52\x49\x46\x46\x4a\x00\x00\x00\x57\x45\x42\x50\x56\x50\x38\x20\x3e\x00\x00\x00\x90\x02\x00\x9d\x01\x2a\x20\x00\x10\x00\x02\xc0\x4c\x25\xa0\x02\x74\xca\x11\xc0\xde\x81\xf8\xab\xf8\x00\x04\x46\x30\x00\xfe\xee\x43\x1f\xec\x21\x35\x4d\x9d\xff\xed\x9d\x39\xa3\xfd\x6d\xbf\xcd\x57\x14\x77\xfd\xeb\x7f\x87\xa6\x59\xdf\xc3\x00\x00\x00")
	webpAlphaFixture = []byte("\x52\x49\x46\x46\x74\x00\x00\x00\x57\x45\x42\x50\x56\x50\x38\x58\x0a\x00\x00\x00\x10\x00\x00\x00\x1f\x00\x00\x0f\x00\x00\x41\x4c\x50\x48\x0f\x00\x00\x00\x01\x10\x12\x10\xfe\x9f\x16\x65\x2d\x22\x26\x80\x6c\xfe\x0b\x00\x56\x50\x38\x20\x3e\x00\x00\x00\x90\x02\x00\x9d\x01\x2a\x20\x00\x10\x00\x02\xc0\x4c\x25\xa0\x02\x74\xca\x11\xc0\xde\x81\xf8\xab\xf8\x00\x04\x46\x30\x00\xfe\xee\x43\x1f\xec\x21\x35\x4d\x9d\xff\xed\x9d\x39\xa3\xfd\x6d\xbf\xcd\x57\x14\x77\xfd\xeb\x7f\x87\xa6\x59\xdf\xc3\x00\x00\x00")
)

var (
	fixtureRed  = color.RGBA{255, 0, 0, 255}
	fixtureBlue = color.RGBA{0, 0, 255, 255}
)

func riffWebP(chunks ...[]byte) []byte {
	body := []byte("WEBP")
	for _, chunk := range chunks {
		body = append(body, chunk...)
	}
	return append(append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...), body...)
}

func webpChunk(kind string, payload []byte) []byte {
	chunk := append([]byte(kind), binary.LittleEndian.AppendUint32(nil, uint32(len(payload)))...)
	chunk = append(chunk, payload...)
	if len(payload)%2 == 1 {
		chunk = append(chunk, 0)
	}
	return chunk
}

func webpVP8X(flags byte, width, height int) []byte {
	payload := []byte{flags, 0, 0, 0, byte(width - 1), byte((width - 1) >> 8), byte((width - 1) >> 16), byte(height - 1), byte((height - 1) >> 8), byte((height - 1) >> 16)}
	return webpChunk("VP8X", payload)
}

// webpRawAlphaFixture reuses the lossy frame with an uncompressed ALPH plane:
// the left half transparent.
func webpRawAlphaFixture() []byte {
	alpha := []byte{0}
	for range 16 {
		for x := range 32 {
			alpha = append(alpha, byte(255*(x/16)))
		}
	}
	return riffWebP(webpVP8X(0x10, 32, 16), webpChunk("ALPH", alpha), webpLossyFixture[12:])
}

type vp8lBits struct {
	data []byte
	acc  uint64
	n    uint
}

func (w *vp8lBits) put(value uint32, bits uint) {
	w.acc |= uint64(value) << w.n
	for w.n += bits; w.n >= 8; w.n -= 8 {
		w.data = append(w.data, byte(w.acc))
		w.acc >>= 8
	}
}

// webpLosslessFixture writes a real VP8L stream: no transforms, no color
// cache, one Huffman group of "simple" codes. Red and blue each take one bit
// per pixel, so the 32x16 image is red on the left and blue on the right.
func webpLosslessFixture(width, height int) []byte {
	var w vp8lBits
	w.put(0x2f, 8)
	w.put(uint32(width-1), 14)
	w.put(uint32(height-1), 14)
	w.put(0, 1+3+1+1+1) // alpha hint, version, transform, color cache, meta
	one := func(symbol uint32) { w.put(1, 1); w.put(0, 1); w.put(1, 1); w.put(symbol, 8) }
	two := func(a, b uint32) { w.put(1, 1); w.put(1, 1); w.put(1, 1); w.put(a, 8); w.put(b, 8) }
	one(0)      // green
	two(255, 0) // red: code 0 = 255
	two(0, 255) // blue: code 0 = 0
	one(255)    // alpha
	one(0)      // distance
	for range height {
		for x := range width {
			bit := uint32(min(1, 2*x/width))
			w.put(bit, 1)
			w.put(bit, 1)
		}
	}
	w.put(0, 7)
	return riffWebP(webpChunk("VP8L", w.data))
}

func halvesImage(width, height int, left, right color.Color) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			if 2*x < width {
				m.Set(x, y, left)
			} else {
				m.Set(x, y, right)
			}
		}
	}
	return m
}

func gifFixture(t *testing.T, screen image.Rectangle, frame image.Rectangle, interlace bool) []byte {
	t.Helper()
	m := image.NewPaletted(frame, color.Palette{fixtureRed, fixtureBlue})
	for y := frame.Min.Y; y < frame.Max.Y; y++ {
		for x := frame.Min.X; x < frame.Max.X; x++ {
			if 2*(x-frame.Min.X) >= frame.Dx() {
				m.SetColorIndex(x, y, 1)
			}
		}
	}
	var out bytes.Buffer
	if err := gif.EncodeAll(&out, &gif.GIF{Image: []*image.Paletted{m}, Delay: []int{0}, Config: image.Config{Width: screen.Dx(), Height: screen.Dy()}}); err != nil {
		t.Fatal(err)
	}
	data := out.Bytes()
	if interlace {
		// Every row is identical, so flagging the descriptor as interlaced
		// still decodes to the same image through the uninterlace copy.
		index := bytes.IndexByte(data[13:], 0x2c) + 13
		data[index+9] |= 0x40
	}
	return data
}

func bmpFixture(t *testing.T, m image.Image) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := bmp.Encode(&out, m); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func tiffFixture(t *testing.T, m image.Image, compression tiff.CompressionType) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := tiff.Encode(&out, m, &tiff.Options{Compression: compression}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

type tiffEntry struct {
	tag, kind uint16
	values    []uint32
}

// handTIFF writes a little-endian, single-IFD TIFF. Values that do not fit
// in an entry are stored after the IFD; pixel data follows them.
func handTIFF(entries []tiffEntry, pixels []byte) []byte {
	slices.SortFunc(entries, func(a, b tiffEntry) int { return int(a.tag) - int(b.tag) })
	directory := 8
	extra := directory + 2 + 12*len(entries) + 4
	var tail []byte
	var table []byte
	for _, entry := range entries {
		item := binary.LittleEndian.AppendUint16(nil, entry.tag)
		item = binary.LittleEndian.AppendUint16(item, entry.kind)
		item = binary.LittleEndian.AppendUint32(item, uint32(len(entry.values)))
		var raw []byte
		for _, value := range entry.values {
			if entry.kind == tiffTypeShort {
				raw = binary.LittleEndian.AppendUint16(raw, uint16(value))
			} else {
				raw = binary.LittleEndian.AppendUint32(raw, value)
			}
		}
		if len(raw) <= 4 {
			item = append(item, append(raw, make([]byte, 4-len(raw))...)...)
		} else {
			item = binary.LittleEndian.AppendUint32(item, uint32(extra+len(tail)))
			tail = append(tail, raw...)
		}
		table = append(table, item...)
	}
	data := append([]byte("II*\x00"), binary.LittleEndian.AppendUint32(nil, uint32(directory))...)
	data = binary.LittleEndian.AppendUint16(data, uint16(len(entries)))
	data = append(data, table...)
	data = append(data, 0, 0, 0, 0)
	data = append(data, tail...)
	return append(data, pixels...)
}

// rgbTIFF describes an uncompressed 8-bit RGB strip TIFF whose pixels start
// at offset; pixels may be omitted for header-only preflight tests.
func rgbTIFF(width, height uint32, orientation uint16, compression uint32, extra []tiffEntry, pixels []byte) []byte {
	entries := []tiffEntry{
		{256, tiffTypeLong, []uint32{width}}, {257, tiffTypeLong, []uint32{height}},
		{258, tiffTypeShort, []uint32{8, 8, 8}}, {259, tiffTypeShort, []uint32{compression}},
		{262, tiffTypeShort, []uint32{2}}, {273, tiffTypeLong, []uint32{0}},
		{277, tiffTypeShort, []uint32{3}}, {278, tiffTypeLong, []uint32{height}},
		{279, tiffTypeLong, []uint32{width * height * 3}},
	}
	if orientation != 0 {
		entries = append(entries, tiffEntry{tiffTagOrient, tiffTypeShort, []uint32{uint32(orientation)}})
	}
	entries = append(entries, extra...)
	// The data offset depends only on the entry layout, so compute it once.
	header := handTIFF(slices.Clone(entries), nil)
	entries[5].values[0] = uint32(len(header))
	return handTIFF(entries, pixels)
}

func rgbPixels(m image.Image) []byte {
	var pixels []byte
	b := m.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.RGBAModel.Convert(m.At(x, y)).(color.RGBA)
			pixels = append(pixels, c.R, c.G, c.B)
		}
	}
	return pixels
}

type formatFixture struct {
	name  string
	data  []byte
	alpha bool // left half transparent: rendered white
}

func additionalFormatFixtures(t *testing.T) []formatFixture {
	halves := halvesImage(32, 16, fixtureRed, fixtureBlue)
	paletted := image.NewPaletted(image.Rect(0, 0, 32, 16), color.Palette{fixtureRed, fixtureBlue})
	for y := range 16 {
		for x := 16; x < 32; x++ {
			paletted.SetColorIndex(x, y, 1)
		}
	}
	// Not opaque, so the encoder writes 32 bits per pixel; a 40-byte
	// BITMAPINFOHEADER makes x/image ignore that alpha byte.
	nrgba := image.NewNRGBA(image.Rect(0, 0, 32, 16))
	for y := range 16 {
		for x := range 32 {
			nrgba.SetNRGBA(x, y, color.NRGBA{255 * uint8(1-x/16), 0, 255 * uint8(x/16), 254})
		}
	}
	return []formatFixture{
		{name: "webp-lossy", data: webpLossyFixture},
		{name: "webp-alpha-lossless", data: webpAlphaFixture, alpha: true},
		{name: "webp-alpha-raw", data: webpRawAlphaFixture(), alpha: true},
		{name: "webp-lossless", data: webpLosslessFixture(32, 16)},
		{name: "gif", data: gifFixture(t, image.Rect(0, 0, 32, 16), image.Rect(0, 0, 32, 16), false)},
		{name: "gif-offset-frame", data: gifFixture(t, image.Rect(0, 0, 40, 30), image.Rect(5, 7, 37, 23), false)},
		{name: "bmp-24", data: bmpFixture(t, halves)},
		{name: "bmp-8-paletted", data: bmpFixture(t, paletted)},
		{name: "bmp-32", data: bmpFixture(t, nrgba)},
		{name: "tiff-none", data: tiffFixture(t, halves, tiff.Uncompressed)},
		{name: "tiff-deflate", data: tiffFixture(t, halves, tiff.Deflate)},
		{name: "tiff-deflate-paletted", data: tiffFixture(t, paletted, tiff.Deflate)},
		{name: "tiff-hand-rgb", data: rgbTIFF(32, 16, 0, 1, nil, rgbPixels(halves))},
	}
}

func writeFormatFixture(t *testing.T, source domain.LocalImageSource, data []byte) {
	t.Helper()
	// Source selection is by name; decoding is by content, as in production.
	if err := os.WriteFile(filepath.Join(source.RootPath, "movie", "poster.png"), data, 0600); err != nil {
		t.Fatal("write format fixture")
	}
}

func classifyHalf(t *testing.T, m image.Image, x, y int, alpha bool) {
	t.Helper()
	r, g, b, _ := m.At(x, y).RGBA()
	left := 2*x < m.Bounds().Dx()
	switch {
	case left && alpha:
		if r < 60000 || g < 60000 || b < 60000 {
			t.Errorf("transparent pixel (%d,%d) not white: %d %d %d", x, y, r>>8, g>>8, b>>8)
		}
	case left:
		if r < 50000 || g > 15000 || b > 15000 {
			t.Errorf("left pixel (%d,%d) not red: %d %d %d", x, y, r>>8, g>>8, b>>8)
		}
	default:
		if b < 50000 || r > 15000 || g > 15000 {
			t.Errorf("right pixel (%d,%d) not blue: %d %d %d", x, y, r>>8, g>>8, b>>8)
		}
	}
}

func TestImageAdditionalFormatsDecodeAndResize(t *testing.T) {
	for _, fixture := range additionalFormatFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			source, scratch := imageSourceFixture(t)
			writeFormatFixture(t, source, fixture.data)
			p := processorTestNew(t, processorTestOptions(scratch))
			for _, request := range []domain.ImageRequest{{Width: 16}, {}} {
				result, err := p.Render(context.Background(), source, request)
				if err != nil {
					t.Fatal("render", err)
				}
				want := image.Rect(0, 0, 32, 16)
				if request.Width == 16 {
					want = image.Rect(0, 0, 16, 8)
				}
				decoded, err := jpeg.Decode(result.Body)
				result.Body.Close()
				if err != nil || decoded.Bounds() != want || result.Width != want.Dx() || result.Height != want.Dy() {
					t.Fatal("wrong output", err, decoded.Bounds())
				}
				for _, x := range []int{want.Dx() / 4, 3 * want.Dx() / 4} {
					classifyHalf(t, decoded, x, want.Dy()/2, fixture.alpha)
				}
			}
			if s := p.Stats(); s.Decodes != 2 || s.Active != 0 {
				t.Fatalf("unexpected stats %+v", s)
			}
			imageScratchEmpty(t, scratch)
		})
	}
}

func TestImageGIFInterlacedFirstFrameEstimate(t *testing.T) {
	data := gifFixture(t, image.Rect(0, 0, 32, 16), image.Rect(0, 0, 32, 16), true)
	input, err := inspectImage(context.Background(), bytes.NewReader(data))
	if err != nil || !input.interlaced || input.width != 32 {
		t.Fatal("interlaced GIF preflight", err)
	}
	plain := input
	plain.interlaced = false
	a, errA := estimateImageBytes(input, 8, 8, 100, 1024)
	b, errB := estimateImageBytes(plain, 8, 8, 100, 1024)
	if errA != nil || errB != nil || a-b != 32*16 {
		t.Fatal("uninterlace copy not reserved")
	}
	decoded, err := decodeImage(context.Background(), bytes.NewReader(data), input)
	if err != nil || decoded.Bounds() != image.Rect(0, 0, 32, 16) {
		t.Fatal("interlaced GIF decode", err)
	}
}

// oversizeFixtures declare dimensions whose decode would allocate far beyond
// the 16 MiB budget, while the files themselves stay tiny (BMP needs its
// complete pixel array to pass preflight, so it uses 1 bit per pixel).
func oversizeFixtures() map[string][]byte {
	vp8 := []byte{0x10, 0, 0, 0x9d, 0x01, 0x2a, 0xff, 0x3f, 0xff, 0x3f}
	vp8l := []byte{0x2f, 0xff, 0xff, 0xff, 0x0f}
	gifData := []byte("GIF89a\xff\xff\xff\xff\x80\x00\x00\x00\x00\x00\xff\xff\xff\x2c\x00\x00\x00\x00\xff\xff\xff\xff\x00")
	bmpData := make([]byte, 62+512*4096)
	copy(bmpData, "BM")
	binary.LittleEndian.PutUint32(bmpData[10:], 62)
	binary.LittleEndian.PutUint32(bmpData[14:], 40)
	binary.LittleEndian.PutUint32(bmpData[18:], 4096)
	binary.LittleEndian.PutUint32(bmpData[22:], 4096)
	binary.LittleEndian.PutUint16(bmpData[26:], 1)
	binary.LittleEndian.PutUint16(bmpData[28:], 1)
	binary.LittleEndian.PutUint32(bmpData[46:], 2)
	return map[string][]byte{
		"webp-lossy":     riffWebP(webpChunk("VP8 ", vp8)),
		"webp-lossless":  riffWebP(webpChunk("VP8L", vp8l)),
		"webp-vp8x":      riffWebP(webpVP8X(0, 16384, 16384), webpChunk("VP8L", vp8l)),
		"webp-raw-alpha": riffWebP(webpVP8X(0x10, 4096, 4096), webpChunk("ALPH", []byte{0}), webpChunk("VP8 ", []byte{0x10, 0, 0, 0x9d, 0x01, 0x2a, 0x00, 0x10, 0x00, 0x10})),
		"gif":            gifData,
		"bmp-1bit":       bmpData,
		"tiff":           rgbTIFF(60000, 60000, 0, 1, nil, nil),
		"tiff-strip":     rgbTIFF(2048, 2048, 0, 5, nil, nil),
	}
}

func TestImageAdditionalFormatsRejectOversizeBeforeDecode(t *testing.T) {
	for name, data := range oversizeFixtures() {
		t.Run(name, func(t *testing.T) {
			source, scratch := imageSourceFixture(t)
			writeFormatFixture(t, source, data)
			options := processorTestOptions(scratch)
			options.MaxImageBytes = 16 << 20
			p := processorTestNew(t, options)
			p.decode = func(context.Context, io.ReadSeeker, inspectedImage) (image.Image, error) {
				t.Error("over-budget image reached decoder")
				return nil, errors.New("unreachable")
			}
			if result, err := p.Render(context.Background(), source, domain.ImageRequest{Width: 16}); err != domain.ErrImageTooLarge || result.Body != nil {
				t.Fatal("oversized image did not fail before decode", err)
			}
			if p.Stats().Active != 0 || p.Stats().Decodes != 0 {
				t.Fatal("rejection leaked a slot or counted a decode")
			}
			imageScratchEmpty(t, scratch)
		})
	}
}

func TestImageAdditionalFormatPreflightAllocatesHeadersOnly(t *testing.T) {
	const runs = 20
	for name, data := range oversizeFixtures() {
		reader := bytes.NewReader(data)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for range runs {
			reader.Seek(0, io.SeekStart)
			input, err := inspectImage(context.Background(), reader)
			if err != nil {
				t.Fatal(name, err)
			}
			if estimate, err := estimateImageBytes(input, 16, 16, int64(len(data)), 2<<20); err == nil && estimate <= 16<<20 {
				t.Fatal(name, "oversized declaration fits the budget")
			}
		}
		runtime.ReadMemStats(&after)
		perRun := (after.TotalAlloc - before.TotalAlloc) / runs
		t.Logf("%s: %d bytes allocated per preflight", name, perRun)
		if perRun > 64<<10 {
			t.Fatal(name, "preflight allocated", perRun)
		}
	}
}

func TestImageAdditionalFormatsRejectMalformedHeaders(t *testing.T) {
	vp8 := webpLossyFixture[12:]
	lossless := webpLosslessFixture(32, 16)
	gifData := gifFixture(t, image.Rect(0, 0, 32, 16), image.Rect(0, 0, 32, 16), false)
	outside := slices.Clone(gifData)
	outside[6] = 8 // screen narrower than the frame
	noTable := []byte("GIF89a\x10\x00\x10\x00\x00\x00\x00\x2c\x00\x00\x00\x00\x10\x00\x10\x00\x00\x02\x02\x44\x01\x00\x3b")
	trailerOnly := []byte("GIF89a\x10\x00\x10\x00\x00\x00\x00\x3b")
	badExtension := []byte("GIF89a\x10\x00\x10\x00\x00\x00\x00\x21\x42\x00\x3b")
	zeroFrame := []byte("GIF89a\x10\x00\x10\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff\x2c\x00\x00\x00\x00\x00\x00\x10\x00\x00\x02\x02\x44\x01\x00\x3b")
	bmpData := bmpFixture(t, halvesImage(32, 16, fixtureRed, fixtureBlue))
	negative := slices.Clone(bmpData)
	binary.LittleEndian.PutUint32(negative[18:], 0xffffffe0)
	compressed := slices.Clone(bmpData)
	binary.LittleEndian.PutUint32(compressed[30:], 1) // RLE8
	tiffData := tiffFixture(t, halvesImage(32, 16, fixtureRed, fixtureBlue), tiff.Uncompressed)
	badDirectory := slices.Clone(tiffData)
	binary.LittleEndian.PutUint32(badDirectory[4:], 1<<30)
	for name, test := range map[string]struct {
		data []byte
		want error
	}{
		"webp-riff-longer-than-file":  {append(slices.Clone(webpLossyFixture[:8]), webpLossyFixture[8:60]...), domain.ErrImageUnavailable},
		"webp-chunk-longer-than-riff": {riffWebP(append([]byte("VP8 \xff\x00\x00\x00"), vp8[8:]...)), domain.ErrImageUnavailable},
		"webp-no-image-chunk":         {riffWebP(webpChunk("EXIF", []byte("meta"))), domain.ErrImageUnavailable},
		"webp-animation":              {riffWebP(webpVP8X(0x02, 32, 16), vp8), domain.ErrImageUnsupported},
		"webp-alph-without-flag":      {riffWebP(webpVP8X(0, 32, 16), webpChunk("ALPH", []byte{0}), vp8), domain.ErrImageUnavailable},
		"webp-alpha-flag-no-alph":     {riffWebP(webpVP8X(0x10, 32, 16), vp8), domain.ErrImageUnavailable},
		"webp-alph-bad-compression":   {riffWebP(webpVP8X(0x10, 32, 16), webpChunk("ALPH", []byte{2}), vp8), domain.ErrImageUnavailable},
		"webp-canvas-mismatch-vp8":    {riffWebP(webpVP8X(0, 4096, 4096), vp8), domain.ErrImageUnavailable},
		"webp-canvas-mismatch-vp8l":   {riffWebP(webpVP8X(0, 4096, 4096), lossless[12:]), domain.ErrImageUnavailable},
		"webp-canvas-overflow":        {riffWebP(webpVP8X(0, 1<<24, 1<<24), vp8), domain.ErrImageUnavailable},
		"webp-duplicate-vp8x":         {riffWebP(webpVP8X(0, 32, 16), webpVP8X(0, 32, 16), vp8), domain.ErrImageUnavailable},
		"webp-interframe":             {riffWebP(webpChunk("VP8 ", append([]byte{0x01}, vp8[9:]...))), domain.ErrImageUnsupported},
		"webp-bad-sync":               {riffWebP(webpChunk("VP8 ", []byte{0x10, 0, 0, 0x9d, 0x01, 0x2b, 32, 0, 16, 0})), domain.ErrImageUnavailable},
		"webp-partition-past-chunk":   {riffWebP(webpChunk("VP8 ", []byte{0xf0, 0xff, 0x00, 0x9d, 0x01, 0x2a, 32, 0, 16, 0})), domain.ErrImageUnavailable},
		"webp-zero-size":              {riffWebP(webpChunk("VP8 ", []byte{0x10, 0, 0, 0x9d, 0x01, 0x2a, 0, 0, 16, 0})), domain.ErrImageUnavailable},
		"webp-vp8l-version":           {riffWebP(webpChunk("VP8L", []byte{0x2f, 0x1f, 0xc0, 0x03, 0x20})), domain.ErrImageUnavailable},
		"gif-frame-outside-screen":    {outside, domain.ErrImageUnavailable},
		"gif-no-color-table":          {noTable, domain.ErrImageUnavailable},
		"gif-trailer-only":            {trailerOnly, domain.ErrImageUnavailable},
		"gif-unknown-extension":       {badExtension, domain.ErrImageUnavailable},
		"gif-zero-frame":              {zeroFrame, domain.ErrImageUnavailable},
		"bmp-negative-width":          {negative, domain.ErrImageUnsupported},
		"bmp-rle":                     {compressed, domain.ErrImageUnsupported},
		"bmp-truncated-pixels":        {bmpData[:len(bmpData)-1], domain.ErrImageUnavailable},
		"tiff-directory-past-end":     {badDirectory, domain.ErrImageUnavailable},
		"tiff-jpeg-compression":       {rgbTIFF(32, 16, 0, 7, nil, nil), domain.ErrImageUnsupported},
		"tiff-tiny-tiles":             {rgbTIFF(32, 16, 0, 1, []tiffEntry{{322, tiffTypeShort, []uint32{4}}, {323, tiffTypeShort, []uint32{4}}}, nil), domain.ErrImageUnavailable},
		"tiff-oversized-tile":         {rgbTIFF(32, 16, 0, 1, []tiffEntry{{322, tiffTypeLong, []uint32{4096}}, {323, tiffTypeLong, []uint32{16}}}, nil), domain.ErrImageUnavailable},
		"tiff-zero-size":              {rgbTIFF(0, 16, 0, 1, nil, nil), domain.ErrImageUnavailable},
		"avif":                        {[]byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf"), domain.ErrImageUnsupported},
		"heic":                        {[]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), domain.ErrImageUnsupported},
	} {
		if _, err := inspectImage(context.Background(), bytes.NewReader(test.data)); err != test.want {
			t.Errorf("%s: got %v want %v", name, err, test.want)
		}
	}
}

func TestImageAdditionalFormatsTruncatedPrefixesFailSafely(t *testing.T) {
	for _, fixture := range additionalFormatFixtures(t) {
		rejected := 0
		for length := range len(fixture.data) {
			data := fixture.data[:length]
			input, err := inspectImage(context.Background(), bytes.NewReader(data))
			if err != nil {
				if err != domain.ErrImageUnavailable && err != domain.ErrImageUnsupported {
					t.Fatalf("%s[:%d]: unexpected preflight error %v", fixture.name, length, err)
				}
				rejected++
				continue
			}
			if _, err := estimateImageBytes(input, 16, 8, int64(length), 2<<20); err != nil {
				t.Fatalf("%s[:%d]: tiny declaration over budget", fixture.name, length)
			}
			decoded, err := decodeImage(context.Background(), bytes.NewReader(data), input)
			if err == nil && decoded.Bounds() != image.Rect(0, 0, input.width, input.height) {
				t.Fatalf("%s[:%d]: truncated decode returned wrong bounds", fixture.name, length)
			}
			if err == nil && !strings.HasPrefix(fixture.name, "gif") && !strings.HasPrefix(fixture.name, "tiff") {
				// GIF stops after its first frame; a TIFF may end after its
				// strips. Others must reject any missing byte.
				t.Fatalf("%s[:%d]: truncated input decoded", fixture.name, length)
			}
			if err != nil {
				rejected++
			}
		}
		if rejected == 0 {
			t.Fatal(fixture.name, "no prefix was rejected")
		}
	}
}

func TestImageAdditionalFormatsCancellation(t *testing.T) {
	for _, fixture := range additionalFormatFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := inspectImage(cancelled, bytes.NewReader(fixture.data)); err == nil {
				t.Fatal("preflight ignored cancellation")
			}
			source, scratch := imageSourceFixture(t)
			writeFormatFixture(t, source, fixture.data)
			p := processorTestNew(t, processorTestOptions(scratch))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.decode = func(ctx context.Context, r io.ReadSeeker, input inspectedImage) (image.Image, error) {
				cancel()
				return decodeImage(ctx, r, input)
			}
			if result, err := p.Render(ctx, source, domain.ImageRequest{Width: 16}); !errors.Is(err, context.Canceled) || result.Body != nil {
				t.Fatal("cancelled decode was not reported", err)
			}
			if p.Stats().Active != 0 {
				t.Fatal("cancelled decode kept its slot")
			}
			imageScratchEmpty(t, scratch)
		})
	}
}

func TestImageAdditionalFormatEstimates(t *testing.T) {
	for _, input := range []inspectedImage{
		{format: "webp", width: 1 << 17, height: 1},
		{format: "gif", width: 0, height: 1},
		{format: "bmp", width: 16, height: 16, pixelBytes: 0},
		{format: "tiff", width: 16, height: 16, pixelBytes: 4},
	} {
		if _, err := estimateImageBytes(input, 8, 8, 100, 1024); err != domain.ErrImageTooLarge {
			t.Fatalf("invalid declaration accepted: %+v", input)
		}
	}
	base := func(input inspectedImage) int64 {
		total, err := estimateImageBytes(input, 8, 8, 100, 1024)
		if err != nil {
			t.Fatal(err)
		}
		return total
	}
	lossy := inspectedImage{format: "webp", width: 32, height: 16}
	raw, compressed := lossy, lossy
	raw.alpha, compressed.alpha = webpAlphaRaw, webpAlphaLossless
	if base(raw)-base(lossy) != 32*16 || base(compressed) <= base(raw)+vp8lHuffmanGroupBytes {
		t.Fatal("WebP alpha planes not reserved")
	}
	// Lossy macroblocks are padded to 16 pixels.
	if base(inspectedImage{format: "webp", width: 17, height: 16})-base(lossy) != 0 ||
		base(inspectedImage{format: "webp", width: 33, height: 16})-base(lossy) != 388 {
		t.Fatal("VP8 macroblock padding not reserved")
	}
	// A 1024x1024 lossless image reserves the worst-case Huffman groups.
	big := inspectedImage{format: "webp", width: 1024, height: 1024, lossless: true}
	if base(big) < vp8lMaxHuffmanGroups*vp8lHuffmanGroupBytes+6*1024*1024 {
		t.Fatal("VP8L worst case not reserved")
	}
	tiffInput := inspectedImage{format: "tiff", width: 100, height: 100, pixelBytes: 4, blockPixels: 100 * 10, blocks: 10, entries: 12}
	tiled := tiffInput
	tiled.blockPixels = 100 * 100
	if base(tiled)-base(tiffInput) != 4*8*(100*100-100*10) {
		t.Fatal("TIFF block buffers not reserved")
	}
}

func TestImageWebPSameSizeAlphaKeepsOneBitmap(t *testing.T) {
	input, err := inspectImage(context.Background(), bytes.NewReader(webpAlphaFixture))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeImage(context.Background(), bytes.NewReader(webpAlphaFixture), input)
	if err != nil {
		t.Fatal(err)
	}
	original, ok := decoded.(*image.NYCbCrA)
	if !ok {
		t.Fatalf("unexpected decoder type %T", decoded)
	}
	reference := image.NewRGBA(original.Rect)
	for y := range 16 {
		for x := range 32 {
			reference.Set(x, y, color.White)
		}
	}
	// Reference composite from an independent copy, before flattening.
	copied := *original
	copied.Y, copied.Cb, copied.Cr, copied.A = slices.Clone(original.Y), slices.Clone(original.Cb), slices.Clone(original.Cr), slices.Clone(original.A)
	for y := range 16 {
		for x := range 32 {
			r, g, b, a := copied.At(x, y).RGBA()
			white := 0xffff - a
			reference.SetRGBA(x, y, color.RGBA{uint8((r + white) >> 8), uint8((g + white) >> 8), uint8((b + white) >> 8), 255})
		}
	}
	flat, err := sameSizeJPEGImage(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	ycbcr, ok := flat.(*image.YCbCr)
	if !ok || &ycbcr.Y[0] != &original.Y[0] || &ycbcr.Cb[0] != &original.Cb[0] {
		t.Fatal("flattening copied the planes")
	}
	for y := range 16 {
		for x := range 32 {
			r1, g1, b1, _ := ycbcr.At(x, y).RGBA()
			r2, g2, b2, _ := reference.At(x, y).RGBA()
			for _, pair := range [][2]uint32{{r1, r2}, {g1, g2}, {b1, b2}} {
				if diff := int(pair[0]>>8) - int(pair[1]>>8); diff > 3 || diff < -3 {
					t.Fatalf("pixel (%d,%d) differs from white composite: %d", x, y, diff)
				}
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := flattenNYCbCrA(ctx, &copied); err != context.Canceled {
		t.Fatal("flattening ignored cancellation")
	}
}
