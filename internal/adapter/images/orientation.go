package images

import (
	"encoding/binary"

	"golang.org/x/image/math/f64"
)

// TIFF field types that may hold a scalar orientation or layout value.
const (
	tiffTypeByte  = 1
	tiffTypeShort = 3
	tiffTypeLong  = 4
	tiffTagOrient = 0x0112
)

// exifOrientation reads tag 0x0112 from IFD0 of a TIFF-structured Exif
// payload (the bytes after "Exif\0\0"). It never follows an offset outside
// data, never reads beyond the declared entries, and returns 0 for anything
// absent, malformed or outside 1-8, which renders as the identity.
func exifOrientation(data []byte) int {
	if len(data) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch string(data[:4]) {
	case "II*\x00":
		order = binary.LittleEndian
	case "MM\x00*":
		order = binary.BigEndian
	default:
		return 0
	}
	offset := uint64(order.Uint32(data[4:8]))
	if offset < 8 || offset > uint64(len(data))-2 {
		return 0
	}
	count := uint64(order.Uint16(data[offset : offset+2]))
	entries := data[offset+2:]
	// A truncated directory is scanned only as far as complete entries go.
	count = min(count, uint64(len(entries)/12))
	for index := uint64(0); index < count; index++ {
		entry := entries[12*index : 12*index+12]
		if order.Uint16(entry[:2]) == tiffTagOrient {
			return validOrientation(tiffInlineScalar(order, entry))
		}
	}
	return 0
}

// tiffInlineScalar returns the first value of a BYTE, SHORT or LONG entry
// whose values are stored inside the 12-byte entry itself, or -1.
func tiffInlineScalar(order binary.ByteOrder, entry []byte) int64 {
	count := order.Uint32(entry[4:8])
	if count < 1 {
		return -1
	}
	switch order.Uint16(entry[2:4]) {
	case tiffTypeByte:
		if count <= 4 {
			return int64(entry[8])
		}
	case tiffTypeShort:
		if count <= 2 {
			return int64(order.Uint16(entry[8:10]))
		}
	case tiffTypeLong:
		if count == 1 {
			return int64(order.Uint32(entry[8:12]))
		}
	}
	return -1
}

func validOrientation(value int64) int {
	if value < 1 || value > 8 {
		return 0
	}
	return int(value)
}

// oriented reports whether rendering must rotate or mirror the decoded image.
func (i inspectedImage) oriented() bool { return i.orientation > 1 }

// displaySize is the upright size: orientations 5-8 swap the stored axes.
func (i inspectedImage) displaySize() (int, int) {
	if i.orientation >= 5 {
		return i.height, i.width
	}
	return i.width, i.height
}

// orientationTransform maps decoded source coordinates (stored w x h, origin
// at zero) to an upright dstW x dstH output, as draw.Transform's s2d matrix.
// The EXIF meaning of each value: 2 mirror horizontally, 3 rotate 180°,
// 4 mirror vertically, 5 transpose, 6 rotate 90° clockwise, 7 transverse,
// 8 rotate 90° counter-clockwise.
func orientationTransform(orientation, w, h, dstW, dstH int) f64.Aff3 {
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
	uprightW, uprightH := fw, fh
	if orientation >= 5 {
		uprightW, uprightH = fh, fw
	}
	sx, sy := float64(dstW)/uprightW, float64(dstH)/uprightH
	return f64.Aff3{sx * a, sx * b, sx * c, sy * d, sy * e, sy * f}
}
