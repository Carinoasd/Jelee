//go:build unix

package httpapi

import "syscall"

// setReceiveBuffer sets SO_RCVBUF on a socket that is not connected yet.
func setReceiveBuffer(fd uintptr, size int) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, size)
}
