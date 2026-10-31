package subtitles

import "runtime"

// supportedPlatform is false on Windows: it has no POSIX permission bits to
// prove the cache root private, so the derived UTF-8 cache stays off there
// like Matroska extraction and external subtitles are delivered as they are.
const supportedPlatform = runtime.GOOS != "windows"
