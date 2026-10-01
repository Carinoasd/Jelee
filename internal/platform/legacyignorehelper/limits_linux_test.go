package legacyignorehelper

import (
	"errors"
	"golang.org/x/sys/unix"
)

func verifyAllocationDenied() error {
	limit := unix.Rlimit{}
	if err := unix.Getrlimit(unix.RLIMIT_AS, &limit); err != nil {
		return err
	}
	if limit.Cur > MemoryBytes || limit.Max > MemoryBytes {
		return errors.New("address space limit missing")
	}
	// PROT_NONE exercises virtual address space accounting without touching or
	// allocating gigabytes of physical RAM.
	data, err := unix.Mmap(-1, 0, MemoryBytes+4096, unix.PROT_NONE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err == nil {
		unix.Munmap(data)
		return errors.New("oversized mapping accepted")
	}
	if !errors.Is(err, unix.ENOMEM) {
		return err
	}
	return nil
}
