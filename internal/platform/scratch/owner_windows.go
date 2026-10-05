package scratch

import (
	"errors"
	"io/fs"
	"strconv"

	"golang.org/x/sys/windows"
)

const stillActive = 259

func platformStart() (string, bool) {
	created, ok := creationTime(windows.CurrentProcess())
	if !ok {
		return "", false
	}
	return "c" + strconv.FormatUint(created, 10), true
}

func creationTime(process windows.Handle) (uint64, bool) {
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(process, &created, &exited, &kernel, &user) != nil {
		return 0, false
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), true
}

// Creation times are absolute, so PID reuse after a reboot is also detected.
func platformState(o owner) liveness {
	if len(o.start) < 2 || o.start[0] != 'c' || !allDigits(o.start[1:]) {
		return unknown
	}
	expected, err := strconv.ParseUint(o.start[1:], 10, 64)
	if err != nil {
		return unknown
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(o.pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return dead
	}
	if err != nil {
		return unknown
	}
	defer windows.CloseHandle(process)
	created, ok := creationTime(process)
	if !ok {
		return unknown
	}
	if created != expected {
		return dead
	}
	var code uint32
	if windows.GetExitCodeProcess(process, &code) != nil {
		return unknown
	}
	if code != stillActive {
		return dead
	}
	return alive
}

// Windows temporary roots are per-user profile directories protected by
// their DACL; there is no POSIX owner to compare.
func ownedByCurrentUser(fs.FileInfo) bool { return true }
