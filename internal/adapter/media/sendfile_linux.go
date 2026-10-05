package media

// zeroCopySupported enables the zero-copy path. Linux sendfile advances the
// source file offset as it sends, which is how the progress watchdog observes
// a transfer that runs inside one call.
const zeroCopySupported = true
