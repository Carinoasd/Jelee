//go:build jelee_probe_tests && linux

package runtime

import (
	"os"
	"strconv"
	"syscall"
	"time"
)

// Docker supplies a blocking FIFO as stdout. Reopen that same FIFO with its
// own nonblocking file description so Go's poller can enforce write deadlines.
// Closing this handle must not close the testing package's original stdout.
func openImagesSoakOutput(source *os.File) (*os.File, error) {
	if source == nil {
		return nil, errImagesSoakStream
	}
	before, err := source.Stat()
	if err != nil || before.Mode()&os.ModeNamedPipe == 0 {
		return nil, errImagesSoakStream
	}
	output, err := os.OpenFile("/proc/self/fd/"+strconv.FormatUint(uint64(source.Fd()), 10), os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errImagesSoakStream
	}
	after, err := output.Stat()
	if err != nil || !os.SameFile(before, after) || output.SetWriteDeadline(time.Now().Add(time.Second)) != nil || output.SetWriteDeadline(time.Time{}) != nil {
		output.Close()
		return nil, errImagesSoakStream
	}
	return output, nil
}
