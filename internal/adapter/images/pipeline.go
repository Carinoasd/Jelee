package images

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"

	"github.com/HugoSmits86/nativewebp"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/webp"
)

// Crop modes of Options.Crop (G40.6). The empty mode is CropSmart.
const (
	CropSmart  = "smart"
	CropCenter = "center"
	CropOff    = "off"
)

func validCropMode(mode string) bool {
	return mode == "" || mode == CropSmart || mode == CropCenter || mode == CropOff
}

func validOutputFormats(formats []string) bool {
	if len(formats) > 2 {
		return false
	}
	seen := map[string]bool{}
	for _, format := range formats {
		if !domain.ValidImageOutputFormat(format) || seen[format] {
			return false
		}
		seen[format] = true
	}
	return true
}

// allowsFormat reports whether the configured output formats include
// format. No configured list allows every encoder the pipeline has.
func (p *Processor) allowsFormat(format string) bool {
	if len(p.options.OutputFormats) == 0 {
		return domain.ValidImageOutputFormat(format)
	}
	for _, allowed := range p.options.OutputFormats {
		if allowed == format {
			return true
		}
	}
	return false
}

func (p *Processor) cropMode() string {
	if p.options.Crop == "" {
		return CropSmart
	}
	return p.options.Crop
}

func imageContentType(format string) string {
	if format == domain.ImageFormatWebP {
		return "image/webp"
	}
	return "image/jpeg"
}

// renderPlan is the output of one representation: its size, the upright
// (display-oriented) source region it shows, and its encoding. A zero crop
// region is the whole image; a fill crop with smart placement only fixes
// the region's size here and its position after decoding.
type renderPlan struct {
	width, height int
	format        string
	quality       int
	crop          image.Rectangle
	smart         bool
}

// planOutput sizes the representation of a displayWidth x displayHeight
// image. A fit keeps the aspect ratio inside the requested box. A fill
// covers the box: its aspect ratio is the box's, the region is the largest
// part of the image with that ratio, and the output is never larger than
// that region (no enlargement) nor than maximum on either side.
func planOutput(displayWidth, displayHeight int, request domain.ImageRequest, maximum int, mode string) renderPlan {
	plan := renderPlan{format: request.Format, quality: request.Quality}
	if !request.Crop || mode == CropOff {
		plan.width, plan.height = targetSize(displayWidth, displayHeight, request, maximum)
		return plan
	}
	boxW, boxH := int64(request.Width), int64(request.Height)
	dw, dh := int64(displayWidth), int64(displayHeight)
	cropW, cropH := dw, dh
	if dw*boxH > dh*boxW {
		cropW = max(1, min(dw, dh*boxW/boxH))
	} else {
		cropH = max(1, min(dh, dw*boxH/boxW))
	}
	outW, outH := boxW, boxH
	// Shrink the box, keeping its ratio, until it fits both the output limit
	// and the cropped region.
	limitW, limitH := min(int64(maximum), cropW), min(int64(maximum), cropH)
	if outW > limitW || outH > limitH {
		if outW*limitH > outH*limitW {
			outH, outW = max(1, outH*limitW/outW), limitW
		} else {
			outW, outH = max(1, outW*limitH/outH), limitH
		}
	}
	plan.width, plan.height = int(min(outW, limitW)), int(min(outH, limitH))
	plan.crop = image.Rect(int((dw-cropW)/2), int((dh-cropH)/2), int((dw-cropW)/2+cropW), int((dh-cropH)/2+cropH))
	plan.smart = mode != CropCenter && (cropW < dw || cropH < dh)
	return plan
}

// cropTransform maps decoded source coordinates (stored w x h) to a
// dstW x dstH output showing the upright region crop, as draw.Transform's
// s2d matrix. See orientationTransform for the meaning of orientation.
func cropTransform(orientation, w, h int, crop image.Rectangle, dstW, dstH int) f64.Aff3 {
	fw, fh := float64(w), float64(h)
	// (u, v) are upright coordinates before scaling: u = a*x + b*y + c,
	// v = d*x + e*y + f.
	var a, b, c, d, e, f float64
	switch orientation {
	case 2:
		a, c, e = -1, fw, 1
	case 3:
		a, c, e, f = -1, fw, -1, fh
	case 4:
		a, e, f = 1, -1, fh
	case 5:
		b, d = 1, 1
	case 6:
		b, c, d = -1, fh, 1
	case 7:
		b, c, d, f = -1, fh, -1, fw
	case 8:
		b, d, f = 1, -1, fw
	default:
		a, e = 1, 1
	}
	sx, sy := float64(dstW)/float64(crop.Dx()), float64(dstH)/float64(crop.Dy())
	return f64.Aff3{sx * a, sx * b, sx * (c - float64(crop.Min.X)), sy * d, sy * e, sy * (f - float64(crop.Min.Y))}
}

// storedPoint maps an upright pixel (u, v) of a stored w x h image back to
// its stored coordinates; the inverse of the orientation in cropTransform.
func storedPoint(orientation, w, h, u, v int) (int, int) {
	switch orientation {
	case 2:
		return w - 1 - u, v
	case 3:
		return w - 1 - u, h - 1 - v
	case 4:
		return u, h - 1 - v
	case 5:
		return v, u
	case 6:
		return v, h - 1 - u
	case 7:
		return w - 1 - v, h - 1 - u
	case 8:
		return w - 1 - v, u
	}
	return u, v
}

// Smart crop sampling grid: positions along the axis the crop slides on and
// across it. The grid bounds the work and memory whatever the image size.
const (
	smartCropAlong  = 96
	smartCropAcross = 48
)

// smartCrop slides the region of plan along its free axis to the window
// with the most edge detail: the summed luminance differences between
// neighbouring samples of a coarse grid. Ties keep the window nearest the
// centre, so flat images crop like CropCenter. Only the decoded image is
// read; nothing is allocated in proportion to it.
func smartCrop(ctx context.Context, decoded image.Image, input inspectedImage, crop image.Rectangle) (image.Rectangle, error) {
	dw, dh := input.displaySize()
	horizontal := crop.Dx() < dw
	length, window, across := dh, crop.Dy(), dw
	if horizontal {
		length, window, across = dw, crop.Dx(), dh
	}
	if window >= length {
		return crop, nil
	}
	along, wide := min(length, smartCropAlong), min(across, smartCropAcross)
	// energy[i] is the detail of the first i rows of the grid: the
	// luminance steps to the previous row and within the row.
	energy := make([]int, 1, smartCropAlong+1)
	var previous, row [smartCropAcross]int
	for i := range along {
		if err := ctx.Err(); err != nil {
			return crop, err
		}
		sum, left := 0, 0
		for k := range wide {
			p, q := (2*i+1)*length/(2*along), (2*k+1)*across/(2*wide)
			u, v := q, p
			if horizontal {
				u, v = p, q
			}
			x, y := storedPoint(input.orientation, input.width, input.height, u, v)
			row[k] = whiteLuma(decoded.At(x+decoded.Bounds().Min.X, y+decoded.Bounds().Min.Y))
			if i > 0 {
				sum += absInt(row[k] - previous[k])
			}
			if k > 0 {
				sum += absInt(row[k] - left)
			}
			left = row[k]
		}
		energy = append(energy, energy[len(energy)-1]+sum)
		previous = row
	}
	span := max(1, min(along, (window*along+length/2)/length))
	centre := (along - span) / 2
	best, bestEnergy := centre, energy[centre+span]-energy[centre]
	for start := 0; start+span <= along; start++ {
		value := energy[start+span] - energy[start]
		if value > bestEnergy || value == bestEnergy && absInt(start-centre) < absInt(best-centre) {
			best, bestEnergy = start, value
		}
	}
	// Centre the region on the chosen window.
	offset := ((2*best+span)*length/(2*along) - window/2)
	offset = max(0, min(length-window, offset))
	if horizontal {
		return image.Rect(offset, crop.Min.Y, offset+window, crop.Max.Y), nil
	}
	return image.Rect(crop.Min.X, offset, crop.Max.X, offset+window), nil
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// whiteRGB composites a pixel on white, as the JPEG output does.
func whiteRGB(c color.Color) (r, g, b uint32) {
	r, g, b, a := c.RGBA()
	white := 0xffff - a
	return (r + white) >> 8, (g + white) >> 8, (b + white) >> 8
}

func whiteLuma(c color.Color) int {
	r, g, b := whiteRGB(c)
	return int((299*r + 587*g + 114*b) / 1000)
}

// colorSamples bounds the pixels read for the G40.7 colors.
const colorSamples = 64

// imageColors returns the average and the dominant 0xRRGGBB color of a
// decoded image composited on white, from at most colorSamples² evenly
// spaced pixels. The dominant color is the mean of the most populated cell
// of a 16-level-per-channel histogram; ties keep the lower cell. Orientation
// does not change either value.
func imageColors(ctx context.Context, decoded image.Image) (average, dominant int, err error) {
	bounds := decoded.Bounds()
	if bounds.Empty() {
		return 0, 0, domain.ErrImageUnavailable
	}
	columns, rows := min(bounds.Dx(), colorSamples), min(bounds.Dy(), colorSamples)
	// At most colorSamples² samples of at most 255: every sum fits uint32.
	var counts [4096]uint32
	var sums [4096][3]uint32
	var total [3]uint32
	var samples uint32
	for j := 0; j < rows; j++ {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		y := bounds.Min.Y + (2*j+1)*bounds.Dy()/(2*rows)
		for i := 0; i < columns; i++ {
			x := bounds.Min.X + (2*i+1)*bounds.Dx()/(2*columns)
			r, g, b := whiteRGB(decoded.At(x, y))
			total[0], total[1], total[2] = total[0]+r, total[1]+g, total[2]+b
			samples++
			cell := r>>4<<8 | g>>4<<4 | b>>4
			counts[cell]++
			sums[cell][0], sums[cell][1], sums[cell][2] = sums[cell][0]+r, sums[cell][1]+g, sums[cell][2]+b
		}
	}
	average = int((total[0]+samples/2)/samples)<<16 | int((total[1]+samples/2)/samples)<<8 | int((total[2]+samples/2)/samples)
	best := 0
	for cell := range counts {
		if counts[cell] > counts[best] {
			best = cell
		}
	}
	n := counts[best]
	dominant = int((sums[best][0]+n/2)/n)<<16 | int((sums[best][1]+n/2)/n)<<8 | int((sums[best][2]+n/2)/n)
	return average, dominant, nil
}

// webpEncodeBytes is the working memory of the lossless WebP encoder for an
// output of pixels pixels: its own NRGBA and flattened copies, match chain
// and symbol arrays (about 73 bytes per pixel measured with nativewebp
// v1.3.0, rounded up to 80) plus a fixed 2 MiB hash head and table slack.
func webpEncodeBytes(pixels int64) (int64, bool) {
	value, ok := checkedMultiply(pixels, 80)
	if !ok {
		return 0, false
	}
	return checkedSum(value, 4<<20)
}

// encodeImage writes the representation in its format. nativewebp ignores
// write errors and may panic on input it does not expect; both become a
// failed render instead of a truncated or missing response.
func encodeImage(output *boundedImageWriter, img image.Image, plan renderPlan) (err error) {
	if plan.format != domain.ImageFormatWebP {
		return jpeg.Encode(output, img, &jpeg.Options{Quality: plan.quality})
	}
	defer func() {
		if recover() != nil {
			err = domain.ErrImageUnavailable
		}
	}()
	if err := nativewebp.Encode(output, img, nil); err != nil {
		return domain.ErrImageUnavailable
	}
	return output.err
}

// decodeVariantConfig checks a stored variant's header in its format.
func decodeVariantConfig(reader io.Reader, format string) (image.Config, error) {
	if format == domain.ImageFormatWebP {
		return webp.DecodeConfig(reader)
	}
	return jpeg.DecodeConfig(reader)
}
