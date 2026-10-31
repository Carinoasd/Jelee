//go:build windows

package httpapi

import "syscall"

// setReceiveBuffer sets SO_RCVBUF on a socket that is not connected yet.
func setReceiveBuffer(fd uintptr, size int) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, size)
}
