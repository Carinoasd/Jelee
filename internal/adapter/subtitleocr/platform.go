package subtitleocr

import "runtime"

// supportedPlatform is false on Windows: it has no POSIX permission bits to
// prove the picture, work and cache directories private, and the isolated
// Tesseract runtime exists only for Linux amd64 (G15.6). OCR stays off
// there; the routes answer like missing items.
const supportedPlatform = runtime.GOOS != "windows"
