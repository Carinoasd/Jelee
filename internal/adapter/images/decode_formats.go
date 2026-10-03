package images

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"io"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"
)

// WebP, GIF (first frame), BMP and TIFF use the pinned decoders of the Go
// SDK and golang.org/x/image v0.46.0. Each preflight below reads headers only
// and mirrors the decoder's own acceptance order, so the dimensions and pixel
// layout it reports are the ones the decoder will allocate for. AVIF/HEIF
// have no pinned decoder and are rejected as unsupported.

// maxAdditionalDimension bounds every axis of the new formats; JPEG has the
// same 16-bit limit by construction. Larger declarations are not decoded.
const maxAdditionalDimension = 1<<16 - 1

const (
	webpAlphaRaw      = 1
	webpAlphaLossless = 2
)

// vp8lHuffmanGroupBytes is the most one x/image VP8L Huffman group can hold:
// five hTree headers (24-byte slice and 128-entry uint32 LUT each) plus
// 2n-1 8-byte nodes for alphabets of 256+24+2048 (largest color cache), three
// of 256 and one of 40 symbols, plus the largest transient code-length slice.
const vp8lHuffmanGroupBytes = 5*(24+4<<7) + 8*((2*(256+24+2048)-1)+3*(2*256-1)+(2*40-1)) + 4*(256+24+2048)

// vp8lMaxHuffmanGroups is x/image's maxHuffImageSize; more groups are rejected.
const vp8lMaxHuffmanGroups = 2600

func sniffAdditionalFormat(header []byte) string {
	switch {
	case len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP":
		return "webp"
	case len(header) >= 6 && (string(header[:6]) == "GIF87a" || string(header[:6]) == "GIF89a"):
		return "gif"
	case len(header) >= 2 && string(header[:2]) == "BM":
		return "bmp"
	case len(header) >= 4 && (string(header[:4]) == "II*\x00" || string(header[:4]) == "MM\x00*"):
		return "tiff"
	case len(header) >= 12 && string(header[4:8]) == "ftyp":
		// ISO-BMFF stills (AVIF, HEIF) need a decoder this build does not pin.
		return "isobmff"
	}
	return ""
}

func inspectAdditionalFormat(ctx context.Context, source io.ReadSeeker, format string) (inspectedImage, error) {
	size, err := source.Seek(0, io.SeekEnd)
	if err != nil {
		return inspectedImage{}, err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return inspectedImage{}, err
	}
	var result inspectedImage
	switch format {
	case "webp":
		result, err = inspectWebP(ctx, source, size)
	case "gif":
		result, err = inspectGIF(ctx, source)
	case "bmp":
		result, err = inspectBMP(ctx, source, size)
	case "tiff":
		result, err = inspectTIFF(ctx, source, size)
	default:
		return inspectedImage{}, domain.ErrImageUnsupported
	}
	if err != nil {
		if ctx.Err() != nil {
			return inspectedImage{}, ctx.Err()
		}
		if errors.Is(err, domain.ErrImageUnsupported) || errors.Is(err, domain.ErrImageTooLarge) {
			return inspectedImage{}, err
		}
		// Truncated, inconsistent or malicious headers are all unavailable.
		return inspectedImage{}, domain.ErrImageUnavailable
	}
	if result.width <= 0 || result.height <= 0 {
		return inspectedImage{}, domain.ErrImageUnavailable
	}
	if result.width > maxAdditionalDimension || result.height > maxAdditionalDimension {
		return inspectedImage{}, domain.ErrImageTooLarge
	}
	return result, nil
}

// inspectWebP walks RIFF chunks exactly as x/image/webp does, stopping at the
// first VP8 or VP8L chunk. It never reads chunk payloads beyond 10 bytes.
func inspectWebP(ctx context.Context, source io.ReadSeeker, size int64) (inspectedImage, error) {
	r := bufio.NewReaderSize(contextImageReader{ctx, source}, 4<<10)
	var header [12]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return inspectedImage{}, err
	}
	riffLength := int64(binary.LittleEndian.Uint32(header[4:8]))
	// The decoder would stop at a short file only after allocating.
	if riffLength < 4 || riffLength+8 > size {
		return inspectedImage{}, domain.ErrImageUnavailable
	}
	remaining := riffLength - 4
	result := inspectedImage{format: "webp"}
	var canvasW, canvasH int
	seenVP8X, wantAlpha, padded := false, false, false
	var payload [10]byte
	for {
		if padded {
			if remaining == 0 {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			if _, err := r.Discard(1); err != nil {
				return inspectedImage{}, err
			}
			remaining--
		}
		if remaining < 8 {
			// Includes reaching the end without an image chunk.
			return inspectedImage{}, domain.ErrImageUnavailable
		}
		var chunk [8]byte
		if _, err := io.ReadFull(r, chunk[:]); err != nil {
			return inspectedImage{}, err
		}
		remaining -= 8
		length := int64(binary.LittleEndian.Uint32(chunk[4:8]))
		if length > remaining {
			return inspectedImage{}, domain.ErrImageUnavailable
		}
		padded = length&1 == 1
		consumed := int64(0)
		switch string(chunk[:4]) {
		case "VP8X":
			if seenVP8X || length != 10 {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			if _, err := io.ReadFull(r, payload[:10]); err != nil {
				return inspectedImage{}, err
			}
			consumed = 10
			if payload[0]&0x02 != 0 {
				// Animation: x/image cannot decode ANIM/ANMF frames.
				return inspectedImage{}, domain.ErrImageUnsupported
			}
			seenVP8X, wantAlpha = true, payload[0]&0x10 != 0
			canvasW = 1 + (int(payload[4]) | int(payload[5])<<8 | int(payload[6])<<16)
			canvasH = 1 + (int(payload[7]) | int(payload[8])<<8 | int(payload[9])<<16)
			if int64(canvasW)*int64(canvasH) > 1<<31-1 {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
		case "ALPH":
			// The decoder sizes the alpha plane from VP8X before it has seen
			// the VP8 frame; the frame must then match the same canvas.
			if !wantAlpha || length < 1 {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			wantAlpha = false
			if _, err := io.ReadFull(r, payload[:1]); err != nil {
				return inspectedImage{}, err
			}
			consumed = 1
			switch payload[0] & 0x03 {
			case 0:
				result.alpha = webpAlphaRaw
			case 1:
				result.alpha = webpAlphaLossless
			default:
				return inspectedImage{}, domain.ErrImageUnavailable
			}
		case "VP8 ":
			if wantAlpha || length < 10 {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			if _, err := io.ReadFull(r, payload[:10]); err != nil {
				return inspectedImage{}, err
			}
			if payload[0]&1 != 0 {
				// Interframes need golden/altref buffers x/image lacks.
				return inspectedImage{}, domain.ErrImageUnsupported
			}
			firstPartition := int64(payload[0])>>5 | int64(payload[1])<<3 | int64(payload[2])<<11
			if payload[3] != 0x9d || payload[4] != 0x01 || payload[5] != 0x2a || firstPartition > length-10 {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			result.width = int(payload[7]&0x3f)<<8 | int(payload[6])
			result.height = int(payload[9]&0x3f)<<8 | int(payload[8])
			if seenVP8X && (result.width != canvasW || result.height != canvasH) {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			return result, nil
		case "VP8L":
			if result.alpha != 0 || length < 5 {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			if _, err := io.ReadFull(r, payload[:5]); err != nil {
				return inspectedImage{}, err
			}
			bits := binary.LittleEndian.Uint32(payload[1:5])
			if payload[0] != 0x2f || bits>>29 != 0 {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			result.width, result.height, result.lossless = 1+int(bits&0x3fff), 1+int(bits>>14&0x3fff), true
			if seenVP8X && (result.width != canvasW || result.height != canvasH) {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			return result, nil
		}
		if _, err := r.Discard(int(length - consumed)); err != nil {
			return inspectedImage{}, err
		}
		remaining -= length
	}
}

// inspectGIF reads the screen descriptor, skips color tables and extension
// sub-blocks, and stops at the first image descriptor without touching LZW
// data. Only that first frame is decoded and rendered.
func inspectGIF(ctx context.Context, source io.ReadSeeker) (inspectedImage, error) {
	r := bufio.NewReaderSize(contextImageReader{ctx, source}, 4<<10)
	var header [13]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return inspectedImage{}, err
	}
	screenW := int(binary.LittleEndian.Uint16(header[6:8]))
	screenH := int(binary.LittleEndian.Uint16(header[8:10]))
	globalTable := header[10]&0x80 != 0
	if globalTable {
		if _, err := r.Discard(3 << (1 + header[10]&7)); err != nil {
			return inspectedImage{}, err
		}
	}
	skipBlocks := func() error {
		for {
			size, err := r.ReadByte()
			if err != nil || size == 0 {
				return err
			}
			if _, err := r.Discard(int(size)); err != nil {
				return err
			}
		}
	}
	for {
		block, err := r.ReadByte()
		if err != nil {
			return inspectedImage{}, err
		}
		switch block {
		case 0x21:
			label, err := r.ReadByte()
			if err != nil {
				return inspectedImage{}, err
			}
			var control [6]byte
			switch label {
			case 0xf9:
				if _, err := io.ReadFull(r, control[:]); err != nil {
					return inspectedImage{}, err
				}
				if control[0] != 4 || control[5] != 0 {
					return inspectedImage{}, domain.ErrImageUnavailable
				}
				continue
			case 0x01:
				_, err = r.Discard(13)
			case 0xfe:
			case 0xff:
				var size byte
				if size, err = r.ReadByte(); err == nil {
					_, err = r.Discard(int(size))
				}
			default:
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			if err == nil {
				err = skipBlocks()
			}
			if err != nil {
				return inspectedImage{}, err
			}
		case 0x2c:
			var descriptor [9]byte
			if _, err := io.ReadFull(r, descriptor[:]); err != nil {
				return inspectedImage{}, err
			}
			left, top := int(binary.LittleEndian.Uint16(descriptor[0:2])), int(binary.LittleEndian.Uint16(descriptor[2:4]))
			width, height := int(binary.LittleEndian.Uint16(descriptor[4:6])), int(binary.LittleEndian.Uint16(descriptor[6:8]))
			if left+width > screenW || top+height > screenH || !globalTable && descriptor[8]&0x80 == 0 {
				return inspectedImage{}, domain.ErrImageUnavailable
			}
			return inspectedImage{format: "gif", width: width, height: height, interlaced: descriptor[8]&0x40 != 0, pixelBytes: 1}, nil
		default:
			// Includes a trailer before any image.
			return inspectedImage{}, domain.ErrImageUnavailable
		}
	}
}

// inspectBMP lets x/image validate the header, then requires the complete
// uncompressed pixel array to be present before any bitmap is allocated.
func inspectBMP(ctx context.Context, source io.ReadSeeker, size int64) (inspectedImage, error) {
	var header [30]byte
	if _, err := io.ReadFull(contextImageReader{ctx, source}, header[:]); err != nil {
		return inspectedImage{}, err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return inspectedImage{}, err
	}
	configuration, err := bmp.DecodeConfig(contextImageReader{ctx, source})
	if errors.Is(err, bmp.ErrUnsupported) {
		return inspectedImage{}, domain.ErrImageUnsupported
	}
	if err != nil {
		return inspectedImage{}, err
	}
	width, height := int64(configuration.Width), int64(configuration.Height)
	offset := int64(binary.LittleEndian.Uint32(header[10:14]))
	result := inspectedImage{format: "bmp", width: configuration.Width, height: configuration.Height, pixelBytes: 4}
	switch bpp := int64(binary.LittleEndian.Uint16(header[28:30])); bpp {
	case 1, 2, 4, 8:
		perByte := 8 / bpp
		result.pixelBytes, result.rowBytes = 1, ((width+perByte-1)/perByte+3)&^3
	case 24:
		result.rowBytes = (3*width + 3) &^ 3
	case 32:
		result.rowBytes = 4 * width
	default:
		return inspectedImage{}, domain.ErrImageUnsupported
	}
	if width == 0 || height == 0 || result.width > maxAdditionalDimension || result.height > maxAdditionalDimension {
		return result, nil // rejected by the caller's dimension checks
	}
	if offset+result.rowBytes*height > size {
		return inspectedImage{}, domain.ErrImageUnavailable
	}
	return result, nil
}

// inspectTIFF validates the first IFD with x/image's own header parser, then
// reads only the layout tags it uses to size strips or tiles.
func inspectTIFF(ctx context.Context, source io.ReadSeeker, size int64) (inspectedImage, error) {
	reader := &imageReaderAt{ctx: ctx, source: source, size: size}
	configuration, err := tiff.DecodeConfig(reader)
	var unsupported tiff.UnsupportedError
	if errors.As(err, &unsupported) {
		return inspectedImage{}, domain.ErrImageUnsupported
	}
	if err != nil {
		return inspectedImage{}, err
	}
	result := inspectedImage{format: "tiff", width: configuration.Width, height: configuration.Height}
	switch configuration.ColorModel {
	case color.GrayModel:
		result.pixelBytes = 1
	case color.Gray16Model:
		result.pixelBytes = 2
	case color.RGBAModel, color.NRGBAModel:
		result.pixelBytes = 4
	case color.RGBA64Model, color.NRGBA64Model:
		result.pixelBytes = 8
	default:
		if _, ok := configuration.ColorModel.(color.Palette); !ok {
			return inspectedImage{}, domain.ErrImageUnsupported
		}
		result.pixelBytes = 1
	}
	if result.width > maxAdditionalDimension || result.height > maxAdditionalDimension {
		return result, nil // rejected by the caller's dimension checks
	}
	var header [8]byte
	if _, err := reader.ReadAt(header[:], 0); err != nil {
		return inspectedImage{}, err
	}
	var order binary.ByteOrder = binary.LittleEndian
	if header[0] == 'M' {
		order = binary.BigEndian
	}
	directory := int64(order.Uint32(header[4:8]))
	var count [2]byte
	if _, err := reader.ReadAt(count[:], directory); err != nil {
		return inspectedImage{}, err
	}
	result.entries = int64(order.Uint16(count[:]))
	entries := bufio.NewReaderSize(io.NewSectionReader(reader, directory+2, 12*result.entries), 4<<10)
	values := map[uint16]int64{}
	for range result.entries {
		var entry [12]byte
		if _, err := io.ReadFull(entries, entry[:]); err != nil {
			return inspectedImage{}, err
		}
		switch tag := order.Uint16(entry[:2]); tag {
		case tiffTagOrient:
			result.orientation = validOrientation(tiffInlineScalar(order, entry[:]))
		case 259, 278, 322, 323: // Compression, RowsPerStrip, TileWidth, TileLength
			value, err := tiffFirstValue(reader, order, entry[:])
			if err != nil {
				return inspectedImage{}, err
			}
			values[tag] = value
		}
	}
	switch values[259] {
	case 0, 1, 3, 4, 5, 8, 32773, 32946: // none, CCITT G3/G4, LZW, Deflate, PackBits
	default:
		return inspectedImage{}, domain.ErrImageUnsupported
	}
	width, height := int64(result.width), int64(result.height)
	blockW, blockH := width, height
	if values[322] != 0 {
		blockW, blockH = values[322], values[323]
		if blockW < 8 || blockH < 8 || (blockW-width > 16 || blockH-height > 16) && (blockW > 1024 || blockH > 1024) {
			return inspectedImage{}, domain.ErrImageUnavailable
		}
		if blockW > maxAdditionalDimension+16 || blockH > maxAdditionalDimension+16 {
			return inspectedImage{}, domain.ErrImageTooLarge
		}
	} else if rows := values[278]; rows > 0 && rows < height {
		blockH = rows
	}
	result.blockPixels = blockW * blockH
	result.blocks = ((width + blockW - 1) / blockW) * ((height + blockH - 1) / blockH)
	return result, nil
}

// tiffFirstValue mirrors x/image's firstVal for one BYTE/SHORT/LONG entry.
// Out-of-line values cost one bounded 4-byte read at the stored offset.
func tiffFirstValue(reader io.ReaderAt, order binary.ByteOrder, entry []byte) (int64, error) {
	if order.Uint32(entry[4:8]) == 0 {
		return 0, nil
	}
	if value := tiffInlineScalar(order, entry); value >= 0 {
		return value, nil
	}
	var width int
	switch order.Uint16(entry[2:4]) {
	case tiffTypeByte:
		width = 1
	case tiffTypeShort:
		width = 2
	case tiffTypeLong:
		width = 4
	default:
		return 0, domain.ErrImageUnsupported
	}
	var data [4]byte
	if _, err := reader.ReadAt(data[:width], int64(order.Uint32(entry[8:12]))); err != nil {
		return 0, err
	}
	switch width {
	case 1:
		return int64(data[0]), nil
	case 2:
		return int64(order.Uint16(data[:2])), nil
	}
	return int64(order.Uint32(data[:4])), nil
}

// estimateAdditionalFormatBytes returns the decoder-side allocation bound;
// estimateImageBytes adds the staged source, output and fixed overhead.
//
//   - WebP lossy: x/image pads YCbCr 4:2:0 planes to 16-pixel macroblocks
//     (384 bytes) plus a 4-byte filter entry per macroblock, and copies every
//     partition of the VP8 chunk (at most the source size). A raw ALPH plane
//     adds w*h; a VP8L-compressed one adds that plane plus a VP8L decode.
//   - WebP lossless (VP8L): the ARGB plane, a second plane when color
//     indexing unpacks a bundled width (at most half again), three
//     quarter-resolution sub-images (predictor, cross-color, Huffman meta
//     image) and up to 2600 worst-case Huffman groups. The group term is
//     what makes large lossless images exceed the default budget.
//   - GIF: one paletted frame, twice when interlaced (uninterlace copies).
//   - BMP: the paletted (1 byte) or RGBA/NRGBA (4 bytes) image plus one row.
//   - TIFF: the image at its pixel size, plus per-block buffers for 8 bytes
//     per pixel with bytes.Buffer growth (old and new storage, three times),
//     a 10 MiB chunk for large uncompressed reads, and the offset/count tables.
func estimateAdditionalFormatBytes(input inspectedImage, sourceBytes int64) (int64, bool) {
	ok := true
	mul := func(values ...int64) int64 {
		result := int64(1)
		for _, value := range values {
			var valid bool
			result, valid = checkedMultiply(result, value)
			ok = ok && valid
		}
		return result
	}
	sum := func(values ...int64) int64 {
		result, valid := checkedSum(values...)
		ok = ok && valid
		return result
	}
	w, h := int64(input.width), int64(input.height)
	if w <= 0 || h <= 0 || w > maxAdditionalDimension || h > maxAdditionalDimension {
		return 0, false
	}
	pixels := mul(w, h)
	vp8l := func() int64 {
		tiles := mul((w+3)/4, (h+3)/4)
		return sum(mul(pixels, 4), mul((w+1)/2, h, 4), mul(tiles, 12), mul(min(tiles, vp8lMaxHuffmanGroups), vp8lHuffmanGroupBytes), 64<<10)
	}
	var total int64
	switch input.format {
	case "webp":
		if input.lossless {
			total = vp8l()
			break
		}
		total = sum(mul((w+15)/16, (h+15)/16, 384+4), sourceBytes, 64<<10)
		switch input.alpha {
		case webpAlphaRaw:
			total = sum(total, pixels)
		case webpAlphaLossless:
			total = sum(total, pixels, vp8l())
		}
	case "gif":
		frames := int64(1)
		if input.interlaced {
			frames = 2
		}
		total = sum(mul(pixels, frames), 64<<10)
	case "bmp":
		if input.pixelBytes < 1 || input.pixelBytes > 4 || input.rowBytes < 0 {
			return 0, false
		}
		total = sum(mul(pixels, input.pixelBytes), input.rowBytes, 64<<10)
	case "tiff":
		if input.pixelBytes < 1 || input.pixelBytes > 8 || input.blockPixels < 1 || input.blocks < 1 || input.entries < 0 {
			return 0, false
		}
		block := mul(input.blockPixels, 8)
		total = sum(mul(pixels, input.pixelBytes), mul(block, 3), min(block, 10<<20), mul(input.blocks, 24), mul(input.entries, 24), 128<<10)
	default:
		return 0, false
	}
	return total, ok
}

func decodeAdditionalFormat(ctx context.Context, source io.ReadSeeker, input inspectedImage) (image.Image, error) {
	reader := contextImageReader{ctx, source}
	switch input.format {
	case "webp":
		return webp.Decode(reader)
	case "gif":
		decoded, err := gif.Decode(reader)
		if err != nil {
			return nil, err
		}
		frame, ok := decoded.(*image.Paletted)
		if !ok {
			return nil, domain.ErrImageUnavailable
		}
		// Render the first frame by itself. Pix offsets are relative to
		// Rect.Min, so moving the origin to zero keeps the same pixels.
		frame.Rect = frame.Rect.Sub(frame.Rect.Min)
		return frame, nil
	case "bmp":
		return bmp.Decode(bufio.NewReaderSize(reader, 32<<10))
	case "tiff":
		size, err := source.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, err
		}
		// Without io.ReaderAt, x/image/tiff buffers the whole file in memory.
		return tiff.Decode(&imageReaderAt{ctx: ctx, source: source, size: size})
	}
	return nil, domain.ErrImageUnsupported
}

// imageReaderAt gives x/image/tiff random access to the staged file through
// its context-checked ReadSeeker. It is used by one goroutine at a time.
type imageReaderAt struct {
	ctx    context.Context
	source io.ReadSeeker
	size   int64
	offset int64
}

func (r *imageReaderAt) ReadAt(data []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if offset < 0 {
		return 0, domain.ErrImageUnavailable
	}
	if offset >= r.size {
		return 0, io.EOF
	}
	want, short := data, false
	if int64(len(data)) > r.size-offset {
		want, short = data[:r.size-offset], true
	}
	if _, err := r.source.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := io.ReadFull(contextImageReader{r.ctx, r.source}, want)
	if err != nil {
		return n, err
	}
	if short {
		return n, io.EOF
	}
	return n, nil
}

func (r *imageReaderAt) Read(data []byte) (int, error) {
	n, err := r.ReadAt(data, r.offset)
	r.offset += int64(n)
	return n, err
}

// flattenNYCbCrA composites a request-owned WebP image over white inside its
// own planes, so the same-size path keeps one full-size bitmap. White is
// (255, 128, 128) in JFIF YCbCr, an affine image of RGB, so blending there
// equals blending in RGB. Each chroma sample uses the mean alpha of the luma
// pixels it covers; that is exact wherever alpha is uniform within it.
func flattenNYCbCrA(ctx context.Context, source *image.NYCbCrA) (image.Image, error) {
	hs, vs := 1, 1
	switch source.SubsampleRatio {
	case image.YCbCrSubsampleRatio444:
	case image.YCbCrSubsampleRatio422:
		hs = 2
	case image.YCbCrSubsampleRatio420:
		hs, vs = 2, 2
	case image.YCbCrSubsampleRatio440:
		vs = 2
	case image.YCbCrSubsampleRatio411:
		hs = 4
	case image.YCbCrSubsampleRatio410:
		hs, vs = 4, 2
	default:
		return nil, domain.ErrImageUnavailable
	}
	bounds := source.Rect
	if bounds.Empty() {
		return nil, domain.ErrImageUnavailable
	}
	blend := func(value, alpha, background uint32) uint8 {
		return uint8((value*alpha + background*(255-alpha) + 127) / 255)
	}
	// Chroma first: it reads alpha, which the luma pass leaves unchanged.
	for cy := bounds.Min.Y / vs; cy <= (bounds.Max.Y-1)/vs; cy++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		y0, y1 := max(cy*vs, bounds.Min.Y), min(cy*vs+vs, bounds.Max.Y)
		for cx := bounds.Min.X / hs; cx <= (bounds.Max.X-1)/hs; cx++ {
			x0, x1 := max(cx*hs, bounds.Min.X), min(cx*hs+hs, bounds.Max.X)
			var alpha, count uint32
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					alpha += uint32(source.A[source.AOffset(x, y)])
					count++
				}
			}
			alpha = (alpha + count/2) / count
			offset := source.COffset(x0, y0)
			source.Cb[offset] = blend(uint32(source.Cb[offset]), alpha, 128)
			source.Cr[offset] = blend(uint32(source.Cr[offset]), alpha, 128)
		}
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			offset := source.YOffset(x, y)
			source.Y[offset] = blend(uint32(source.Y[offset]), uint32(source.A[source.AOffset(x, y)]), 255)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &source.YCbCr, nil
}
