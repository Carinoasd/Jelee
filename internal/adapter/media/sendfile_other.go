//go:build !linux

package media

// zeroCopySupported is false where the file offset only moves after a whole
// sendfile/TransmitFile call: the watchdog could not see progress, so every
// stream uses the buffered copy loop.
const zeroCopySupported = false
