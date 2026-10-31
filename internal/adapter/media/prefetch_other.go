//go:build !linux

package media

import "os"

// adviseWillNeed is a no-op without posix_fadvise; reads are unaffected.
func adviseWillNeed(*os.File, int64, int64) {}

// prefetchSupported reports whether adviseWillNeed reaches the kernel.
const prefetchSupported = false
