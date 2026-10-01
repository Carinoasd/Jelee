package legacyignorehelper

import (
	"errors"
	"golang.org/x/sys/windows"
)

func verifyAllocationDenied() error {
	// The job must reject this committed allocation before any pages are touched.
	address, err := windows.VirtualAlloc(0, MemoryBytes+4096, windows.MEM_RESERVE|windows.MEM_COMMIT, windows.PAGE_READWRITE)
	if address != 0 {
		windows.VirtualFree(address, 0, windows.MEM_RELEASE)
		return errors.New("oversized commit accepted")
	}
	if err == nil {
		return errors.New("allocation returned no failure")
	}
	return nil
}
