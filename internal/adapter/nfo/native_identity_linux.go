//go:build linux

package nfo

import (
	"encoding/binary"
	"os"

	"golang.org/x/sys/unix"
)

func observeNFONativeIdentity(file *os.File) (nfoNativeIdentity, error) {
	if file == nil {
		return nfoNativeIdentity{}, errNativeIdentity
	}
	control, err := file.SyscallConn()
	if err != nil {
		return nfoNativeIdentity{}, errNativeIdentity
	}
	var stat unix.Statx_t
	var statErr error
	err = control.Control(func(fd uintptr) {
		statErr = unix.Statx(int(fd), "", unix.AT_EMPTY_PATH, unix.STATX_TYPE|unix.STATX_INO|unix.STATX_BTIME, &stat)
	})
	const required = unix.STATX_TYPE | unix.STATX_INO | unix.STATX_BTIME
	if err != nil || statErr != nil || stat.Mask&required != required || stat.Btime.Nsec >= 1_000_000_000 {
		return nfoNativeIdentity{}, errNativeIdentity
	}
	var result nfoNativeIdentity
	result.record[0], result.record[1] = 1, 2
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		result.record[2] = 1
	case unix.S_IFDIR:
		result.record[2] = 2
	default:
		return nfoNativeIdentity{}, errNativeIdentity
	}
	binary.LittleEndian.PutUint32(result.record[8:12], stat.Dev_major)
	binary.LittleEndian.PutUint32(result.record[12:16], stat.Dev_minor)
	binary.LittleEndian.PutUint64(result.record[16:24], stat.Ino)
	binary.LittleEndian.PutUint64(result.record[32:40], uint64(stat.Btime.Sec))
	binary.LittleEndian.PutUint32(result.record[40:44], stat.Btime.Nsec)
	return result, nil
}
