package images

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

var quadrantColors = [4]color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {255, 255, 0, 255}}

// quadrantImage is red top-left, green top-right, blue bottom-left and
// yellow bottom-right.
func quadrantImage(width, height int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			m.SetRGBA(x, y, quadrantColors[2*(2*y/height)+2*x/width])
		}
	}
	return m
}

// exifOrder lets fixtures write either byte order.
type exifOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

func exifPayload(order exifOrder, entries ...[12]byte) []byte {
	data := []byte("II*\x00")
	if order == binary.BigEndian {
		data = []byte("MM\x00*")
	}
	data = order.AppendUint32(data, 8)
	data = order.AppendUint16(data, uint16(len(entries)))
	for _, entry := range entries {
		data = append(data, entry[:]...)
	}
	return order.AppendUint32(data, 0)
}

func exifEntry(order exifOrder, tag, kind uint16, count uint32, value uint16) [12]byte {
	var entry [12]byte
	order.PutUint16(entry[0:2], tag)
	order.PutUint16(entry[2:4], kind)
	order.PutUint32(entry[4:8], count)
	order.PutUint16(entry[8:10], value)
	return entry
}

// jpegWithExif inserts an APP1 Exif segment directly after SOI.
func jpegWithExif(t *testing.T, m image.Image, payload []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := jpeg.Encode(&out, m, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	segment := append([]byte("Exif\x00\x00"), payload...)
	app1 := append([]byte{0xff, 0xe1, byte((len(segment) + 2) >> 8), byte(len(segment) + 2)}, segment...)
	data := out.Bytes()
	return append(append(append([]byte{}, data[:2]...), app1...), data[2:]...)
}

// uprightSource is an independent statement of the EXIF table: it returns
// the stored pixel shown at upright position (x, y) of a w x h stored image.
func uprightSource(orientation, x, y, w, h int) (int, int) {
	switch orientation {
	case 2:
		return w - 1 - x, y
	case 3:
		return w - 1 - x, h - 1 - y
	case 4:
		return x, h - 1 - y
	case 5:
		return y, x
	case 6:
		return y, h - 1 - x
	case 7:
		return w - 1 - y, h - 1 - x
	case 8:
		return w - 1 - y, x
	}
	return x, y
}

func nearestQuadrant(c color.Color) int {
	r, g, b, _ := c.RGBA()
	best, distance := -1, int64(1)<<62
	for index, q := range quadrantColors {
		dr, dg, db := int64(r>>8)-int64(q.R), int64(g>>8)-int64(q.G), int64(b>>8)-int64(q.B)
		if d := dr*dr + dg*dg + db*db; d < distance {
			best, distance = index, d
		}
	}
	return best
}

func checkUpright(t *testing.T, decoded image.Image, orientation, w, h int) {
	t.Helper()
	out := decoded.Bounds()
	for _, point := range [][2]float64{{0.25, 0.25}, {0.75, 0.25}, {0.25, 0.75}, {0.75, 0.75}} {
		ox, oy := int(point[0]*float64(out.Dx())), int(point[1]*float64(out.Dy()))
		// Map the output sample to upright full-size coordinates, then to
		// the stored pixel the EXIF table says must appear there.
		uprightW, uprightH := w, h
		if orientation >= 5 {
			uprightW, uprightH = h, w
		}
		ux, uy := int(point[0]*float64(uprightW)), int(point[1]*float64(uprightH))
		sx, sy := uprightSource(orientation, ux, uy, w, h)
		want := 2*(2*sy/h) + 2*sx/w
		if got := nearestQuadrant(decoded.At(ox, oy)); got != want {
			t.Errorf("orientation %d at (%d,%d): quadrant %d want %d", orientation, ox, oy, got, want)
		}
	}
}

func TestImageEXIFOrientationAllEight(t *testing.T) {
	const w, h = 64, 32
	source := quadrantImage(w, h)
	for orientation := 1; orientation <= 8; orientation++ {
		order := exifOrder(binary.LittleEndian)
		if orientation%2 == 0 {
			order = binary.BigEndian
		}
		data := jpegWithExif(t, source, exifPayload(order,
			exifEntry(order, 0x010f, 2, 4, 0), // Make, before the orientation tag
			exifEntry(order, tiffTagOrient, tiffTypeShort, 1, uint16(orientation))))
		t.Run(fmt.Sprint(orientation), func(t *testing.T) {
			input, err := inspectImage(context.Background(), bytes.NewReader(data))
			if err != nil || input.orientation != orientation || input.width != w || input.height != h {
				t.Fatal("orientation preflight", err, input.orientation)
			}
			uprightW, uprightH := w, h
			if orientation >= 5 {
				uprightW, uprightH = h, w
			}
			src, scratch := imageSourceFixture(t)
			imageWrite(t, src.RootPath+"/movie/poster.jpg", string(data))
			p := processorTestNew(t, processorTestOptions(scratch))
			for _, request := range []domain.ImageRequest{{}, {Width: uprightW / 2}} {
				result, err := p.Render(context.Background(), src, request)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := jpeg.Decode(result.Body)
				result.Body.Close()
				wantW, wantH := uprightW, uprightH
				if request.Width != 0 {
					wantW, wantH = uprightW/2, uprightH/2
				}
				if err != nil || decoded.Bounds() != image.Rect(0, 0, wantW, wantH) || result.Width != wantW || result.Height != wantH {
					t.Fatal("upright output size", err, decoded.Bounds())
				}
				checkUpright(t, decoded, orientation, w, h)
			}
		})
	}
}

func TestImageTIFFOrientation(t *testing.T) {
	const w, h = 32, 16
	data := rgbTIFF(w, h, 6, 1, nil, rgbPixels(quadrantImage(w, h)))
	input, err := inspectImage(context.Background(), bytes.NewReader(data))
	if err != nil || input.orientation != 6 {
		t.Fatal("TIFF orientation preflight", err, input.orientation)
	}
	source, scratch := imageSourceFixture(t)
	writeFormatFixture(t, source, data)
	p := processorTestNew(t, processorTestOptions(scratch))
	result, err := p.Render(context.Background(), source, domain.ImageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	decoded, err := jpeg.Decode(result.Body)
	if err != nil || decoded.Bounds() != image.Rect(0, 0, h, w) {
		t.Fatal("rotated TIFF size", err)
	}
	checkUpright(t, decoded, 6, w, h)
}

func TestImageEXIFOrientationParserIsBounded(t *testing.T) {
	le := binary.LittleEndian
	valid := exifPayload(le, exifEntry(le, tiffTagOrient, tiffTypeShort, 1, 6))
	if exifOrientation(valid) != 6 {
		t.Fatal("valid orientation rejected")
	}
	badOffset := append([]byte{}, valid...)
	le.PutUint32(badOffset[4:8], uint32(len(valid)))
	hugeCount := append([]byte{}, valid...)
	le.PutUint16(hugeCount[8:10], 0xffff)
	hugeCount = hugeCount[:len(hugeCount)-4] // the only entry is still complete
	lowOffset := append([]byte{}, valid...)
	le.PutUint32(lowOffset[4:8], 2)
	for name, payload := range map[string][]byte{
		"empty":            nil,
		"short":            valid[:7],
		"bad-order":        append([]byte("XX*\x00"), valid[4:]...),
		"offset-past-end":  badOffset,
		"offset-in-header": lowOffset,
		"truncated-entry":  valid[:len(valid)-6],
		"zero":             exifPayload(le, exifEntry(le, tiffTagOrient, tiffTypeShort, 1, 0)),
		"nine":             exifPayload(le, exifEntry(le, tiffTagOrient, tiffTypeShort, 1, 9)),
		"no-count":         exifPayload(le, exifEntry(le, tiffTagOrient, tiffTypeShort, 0, 6)),
		"ascii":            exifPayload(le, exifEntry(le, tiffTagOrient, 2, 1, 6)),
		"out-of-line":      exifPayload(le, exifEntry(le, tiffTagOrient, tiffTypeShort, 3, 6)),
		"absent":           exifPayload(le, exifEntry(le, 0x010f, 2, 1, 6)),
	} {
		if got := exifOrientation(payload); got != 0 {
			t.Errorf("%s: orientation %d accepted", name, got)
		}
	}
	if exifOrientation(hugeCount) != 6 {
		t.Fatal("complete entries before a truncated directory were ignored")
	}
	// A JPEG whose Exif segment declares an absurd IFD still decodes upright.
	data := jpegWithExif(t, quadrantImage(16, 16), badOffset)
	input, err := inspectImage(context.Background(), bytes.NewReader(data))
	if err != nil || input.orientation != 0 {
		t.Fatal("malformed Exif changed preflight", err)
	}
}
