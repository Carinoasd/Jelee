package images

import (
	"context"
	"image"
	"image/color"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// sameSizeJPEGImage consumes a request-owned decoded image. Returned headers
// may share its pixels; callers must not use the original image afterwards.
func sameSizeJPEGImage(ctx context.Context, decoded image.Image) (image.Image, error) {
	if ctx == nil {
		return nil, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var rgba *image.RGBA
	switch source := decoded.(type) {
	case *image.YCbCr, *image.Gray:
		return source, nil
	case *image.RGBA:
		rgba = source
	case *image.NRGBA:
		rgba = &image.RGBA{Pix: source.Pix, Stride: source.Stride, Rect: source.Rect}
	case *image.RGBA64:
		rgba = &image.RGBA{Pix: source.Pix, Stride: source.Stride, Rect: source.Rect}
	case *image.NRGBA64:
		rgba = &image.RGBA{Pix: source.Pix, Stride: source.Stride, Rect: source.Rect}
	case *image.Gray16:
		gray := &image.Gray{Pix: source.Pix, Stride: source.Stride, Rect: source.Rect}
		for y := source.Rect.Min.Y; y < source.Rect.Max.Y; y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			offset := gray.PixOffset(source.Rect.Min.X, y)
			for x := source.Rect.Min.X; x < source.Rect.Max.X; x++ {
				gray.Pix[offset] = uint8(source.Gray16At(x, y).Y >> 8)
				offset++
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return gray, nil
	case *image.Paletted:
		if len(source.Palette) == 0 || len(source.Palette) > 256 {
			return nil, domain.ErrImageUnavailable
		}
		for i, pixel := range source.Palette {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if pixel == nil {
				return nil, domain.ErrImageUnavailable
			}
			r, g, b, a := pixel.RGBA()
			source.Palette[i] = sameSizeWhite(color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)})
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return source, nil
	default:
		// Keep new decoder representations behind an explicit memory review.
		return nil, domain.ErrImageUnavailable
	}
	source := decoded.(image.RGBA64Image)
	bounds := source.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset := rgba.PixOffset(bounds.Min.X, y)
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			// Read before writing. For 16-bit sources the 4-byte output is
			// behind the next 8-byte input pixel; original row stride stays.
			pixel := sameSizeWhite(source.RGBA64At(x, y))
			out := rgba.Pix[offset : offset+4 : offset+4]
			out[0], out[1], out[2], out[3] = pixel.R, pixel.G, pixel.B, 255
			offset += 4
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return rgba, nil
}

func sameSizeWhite(pixel color.RGBA64) color.RGBA {
	white := uint32(0xffff - pixel.A)
	return color.RGBA{
		R: uint8((uint32(pixel.R) + white) >> 8),
		G: uint8((uint32(pixel.G) + white) >> 8),
		B: uint8((uint32(pixel.B) + white) >> 8),
		A: 255,
	}
}
