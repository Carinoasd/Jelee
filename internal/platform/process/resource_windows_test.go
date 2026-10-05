package process

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

func resourceCount() int {
	var count uint32
	get := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")
	ok, _, _ := get.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count))) //nolint:gosec // G103: the Windows API takes a raw pointer to this fixed-layout value
	if ok == 0 {
		panic("cannot count process handles")
	}
	return int(count)
}
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	state, err := windows.WaitForSingleObject(h, 0)
	return err != nil || state != windows.WAIT_OBJECT_0
}
