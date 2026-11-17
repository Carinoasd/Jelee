package media

import (
	"os"
	"strconv"
	"strings"
)

// prefetch gives read-ahead advice for a GET that opened file (G10.6): the
// first PrefetchBytes from the first requested offset and, for a request
// from the start, the recorded seek index while the file still has the
// stamp it was read against. Advice never changes what is sent; errors are
// ignored, and a malformed Range is left to http.ServeContent.
func (h *Handler) prefetch(file *os.File, info os.FileInfo, rangeHeader string, index SeekIndex) {
	limit := h.options.PrefetchBytes
	size := info.Size()
	if limit <= 0 || size <= 0 {
		return
	}
	start, ok := firstRangeStart(rangeHeader, size)
	if !ok {
		return
	}
	h.advise(file, start, min(limit, size-start))
	if start == 0 && index.Length > 0 && index.Size == size && index.ModifiedUnixNano == info.ModTime().UnixNano() &&
		index.Offset >= 0 && index.Offset <= size-index.Length {
		h.advise(file, index.Offset, min(index.Length, prefetchIndexMax))
	}
}

// firstRangeStart returns the first byte offset a GET will read: zero
// without a Range header, else the start of its first range. Only a
// syntactically plain first range is used.
func firstRangeStart(header string, size int64) (int64, bool) {
	if header == "" {
		return 0, true
	}
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok {
		return 0, false
	}
	spec, _, _ = strings.Cut(spec, ",")
	first, last, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		return 0, false
	}
	if first == "" {
		suffix, err := strconv.ParseInt(last, 10, 64)
		if err != nil || suffix <= 0 {
			return 0, false
		}
		return max(size-suffix, 0), true
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, false
	}
	return start, true
}
