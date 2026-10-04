package matroska

import "runtime"

// supportedPlatform is false on Windows: it has no POSIX permission bits to
// prove a cache root private, and the isolated mkvtoolnix runtime exists
// only for Linux (E4). Extraction stays off there; the routes answer like
// missing items and doctor reports the runtime as unavailable.
const supportedPlatform = runtime.GOOS != "windows"
