package domain

import "errors"

var (
	ErrImageBusy        = errors.New("image processing busy")
	ErrImageUnavailable = errors.New("image unavailable")
	ErrImageTooLarge    = errors.New("image exceeds processing budget")
	ErrImageUnsupported = errors.New("image format unsupported")
)

// LocalImageSource is resolved from the authorized catalog. It is never a DTO
// or a client-supplied filesystem path.
type LocalImageSource struct {
	ItemID, LibraryID, SourceID, RootID, RootPath, MediaPath string
}

func (LocalImageSource) String() string   { return "local image source (redacted)" }
func (LocalImageSource) GoString() string { return "local image source (redacted)" }

type ImageRequest struct {
	Type, Format           string
	Width, Height, Quality int
}

// NormalizeImageRequest leaves quality zero for the configured default. A
// zero dimension means unconstrained in that direction, subject to the
// processor's output and memory limits. Images are never enlarged.
func NormalizeImageRequest(value ImageRequest) (ImageRequest, error) {
	if value.Type == "" {
		value.Type = "Primary"
	}
	if value.Format == "" {
		value.Format = "jpeg"
	}
	if value.Type != "Primary" || value.Format != "jpeg" || value.Width < 0 || value.Width > 2048 || value.Height < 0 || value.Height > 2048 || value.Quality < 0 || value.Quality > 100 {
		return ImageRequest{}, ErrInvalid
	}
	if value.Width == 0 && value.Height == 0 {
		value.Width, value.Height = 640, 640
	}
	return value, nil
}
