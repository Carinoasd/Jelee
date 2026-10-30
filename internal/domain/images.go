package domain

import "errors"

var (
	ErrImageBusy        = errors.New("image processing busy")
	ErrImageUnavailable = errors.New("image unavailable")
	ErrImageTooLarge    = errors.New("image exceeds processing budget")
	ErrImageUnsupported = errors.New("image format unsupported")
	// ErrConfirmationRequired refuses a destructive change sent without the
	// explicit confirmation G40.11 asks for (replacing or deleting artwork).
	ErrConfirmationRequired = errors.New("confirmation required")
)

// LocalImageSource is resolved from the authorized catalog. It is never a DTO
// or a client-supplied filesystem path.
type LocalImageSource struct {
	ItemID, LibraryID, SourceID, RootID, RootPath, MediaPath string
}

func (LocalImageSource) String() string   { return "local image source (redacted)" }
func (LocalImageSource) GoString() string { return "local image source (redacted)" }

// Output formats of the image pipeline (G40.6). AVIF has no reliable pure
// Go encoder (ADR 0010): it is a known name that is always unsupported.
const (
	ImageFormatJPEG = "jpeg"
	ImageFormatWebP = "webp"
	ImageFormatAVIF = "avif"
)

// ValidImageOutputFormat reports whether the pipeline can encode format.
func ValidImageOutputFormat(format string) bool {
	return format == ImageFormatJPEG || format == ImageFormatWebP
}

type ImageRequest struct {
	Type, Format           string
	Width, Height, Quality int
	// Index selects a gallery slot; only Backdrop and Chapter use non-zero.
	Index int
	// Crop asks for a fill crop: the output covers the whole Width x Height
	// box (both required) and the part of the image outside it is cut off.
	// Without it the image is fitted inside the box.
	Crop bool
}

// NormalizeImageRequest leaves quality zero for the configured default. A
// zero dimension means unconstrained in that direction, subject to the
// processor's output and memory limits. Images are never enlarged. Every
// G40.1 type is accepted; Fanart is an alias of Backdrop. An empty format is
// JPEG; AVIF is ErrImageUnsupported and any other unknown name ErrInvalid.
func NormalizeImageRequest(value ImageRequest) (ImageRequest, error) {
	if value.Type == "" {
		value.Type = "Primary"
	}
	if value.Type == "Fanart" {
		value.Type = "Backdrop"
	}
	if value.Format == "" {
		value.Format = ImageFormatJPEG
	}
	if !ValidItemImageSlot(value.Type, value.Index) || value.Width < 0 || value.Width > 2048 || value.Height < 0 || value.Height > 2048 || value.Quality < 0 || value.Quality > 100 ||
		value.Crop && (value.Width == 0 || value.Height == 0) {
		return ImageRequest{}, ErrInvalid
	}
	if value.Format == ImageFormatAVIF {
		return ImageRequest{}, ErrImageUnsupported
	}
	if !ValidImageOutputFormat(value.Format) {
		return ImageRequest{}, ErrInvalid
	}
	if value.Width == 0 && value.Height == 0 {
		value.Width, value.Height = 640, 640
	}
	return value, nil
}
