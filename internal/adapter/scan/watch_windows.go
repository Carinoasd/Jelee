//go:build windows

package scan

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"golang.org/x/sys/windows"
)

type directoryWatchSlot struct {
	handle     windows.Handle
	overlapped windows.Overlapped
	buffer     [512]uint64
	pin        runtime.Pinner
	pending    bool
}

type directoryWatchBackend struct {
	port  windows.Handle
	slots map[*windows.Overlapped]*directoryWatchSlot
}

func newDirectoryWatchBackend() (*directoryWatchBackend, error) {
	port, err := windows.CreateIoCompletionPort(windows.InvalidHandle, 0, 0, 1)
	if err != nil {
		return nil, domain.ErrScanUnavailable
	}
	return &directoryWatchBackend{port: port, slots: make(map[*windows.Overlapped]*directoryWatchSlot)}, nil
}

// An empty name relative to the secure directory handle opens that same
// object for asynchronous notification. No absolute pathname is re-resolved.
func (b *directoryWatchBackend) Add(file *os.File) error {
	var finalName [32768]uint16
	length, pathErr := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &finalName[0], uint32(len(finalName)), 0)
	if pathErr != nil || length == 0 || length >= uint32(len(finalName)) || strings.HasPrefix(strings.ToUpper(windows.UTF16ToString(finalName[:length])), `\\?\UNC\`) {
		return domain.ErrScanUnavailable
	}

	name, err := windows.NewNTUnicodeString("")
	if err != nil {
		return domain.ErrScanUnavailable
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(file.Fd()), ObjectName: name}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES, &attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	runtime.KeepAlive(file)
	if err != nil {
		return domain.ErrScanUnavailable
	}
	var original, opened windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &original) != nil || windows.GetFileInformationByHandle(handle, &opened) != nil || original.VolumeSerialNumber != opened.VolumeSerialNumber || original.FileIndexHigh != opened.FileIndexHigh || original.FileIndexLow != opened.FileIndexLow || opened.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return domain.ErrScanUnavailable
	}

	if _, err := windows.CreateIoCompletionPort(handle, b.port, 0, 0); err != nil {
		_ = windows.CloseHandle(handle)
		return domain.ErrScanUnavailable
	}
	slot := &directoryWatchSlot{handle: handle}
	slot.pin.Pin(&slot.overlapped)
	slot.pin.Pin(&slot.buffer[0])
	b.slots[&slot.overlapped] = slot
	if err := armDirectoryWatch(slot); err != nil {
		delete(b.slots, &slot.overlapped)
		slot.pin.Unpin()
		_ = windows.CloseHandle(handle)
		return err
	}
	return nil
}

func armDirectoryWatch(slot *directoryWatchSlot) error {
	mask := uint32(windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME | windows.FILE_NOTIFY_CHANGE_ATTRIBUTES | windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE | windows.FILE_NOTIFY_CHANGE_CREATION)
	err := windows.ReadDirectoryChanges(slot.handle, (*byte)(unsafe.Pointer(&slot.buffer[0])), 4096, false, mask, nil, &slot.overlapped, 0)
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return domain.ErrScanUnavailable
	}
	slot.pending = true
	return nil
}

func (b *directoryWatchBackend) Poll(ctx context.Context, wait time.Duration) (bool, bool, error) {
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	var size uint32
	var key uintptr
	var overlap *windows.Overlapped
	err := windows.GetQueuedCompletionStatus(b.port, &size, &key, &overlap, uint32(max(1, wait.Milliseconds())))
	if overlap == nil {
		if errors.Is(err, windows.WAIT_TIMEOUT) {
			return false, false, nil
		}
		return false, false, domain.ErrScanUnavailable
	}
	slot, ok := b.slots[overlap]
	if !ok {
		return false, false, domain.ErrScanUnavailable
	}
	slot.pending = false
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		// A watched child directory may have been removed. Reconcile from the
		// protected roots; a real permission failure is caught while rebuilding.
		return true, true, nil
	}
	if err != nil && !errors.Is(err, windows.ERROR_NOTIFY_ENUM_DIR) {
		return false, false, domain.ErrScanUnavailable
	}
	// Zero bytes or ENUM_DIR means overflow. A malformed notification also
	// requests reconciliation; no event-supplied pathname is used for file I/O.
	structure := size == 0 || err != nil || size > 4096
	if !structure {
		data := unsafe.Slice((*byte)(unsafe.Pointer(&slot.buffer[0])), int(size))
		for offset := uint32(0); offset < size; {
			if size-offset < 12 {
				structure = true
				break
			}
			next := binary.LittleEndian.Uint32(data[offset:])
			action := binary.LittleEndian.Uint32(data[offset+4:])
			if action != windows.FILE_ACTION_MODIFIED {
				structure = true
				break
			}
			if next == 0 {
				break
			}
			if next < 12 || next > size-offset {
				structure = true
				break
			}
			offset += next
		}
	}
	if err := armDirectoryWatch(slot); err != nil {
		return false, false, err
	}
	return true, structure, nil
}

func (b *directoryWatchBackend) Close() error {
	// Retain pinned buffers until every outstanding operation has completed.
	// Closing a completion port before draining would leave kernel pointers to
	// movable/reclaimed Go memory. Cancellation completions are consumed here.
	pending := 0
	for _, slot := range b.slots {
		if slot.pending {
			pending++
			_ = windows.CancelIoEx(slot.handle, &slot.overlapped)
		}
	}
	for pending > 0 {
		var size uint32
		var key uintptr
		var overlap *windows.Overlapped
		_ = windows.GetQueuedCompletionStatus(b.port, &size, &key, &overlap, 1000)
		if slot, ok := b.slots[overlap]; ok && slot.pending {
			slot.pending = false
			pending--
		}
	}
	var closeErr error
	for _, slot := range b.slots {
		if err := windows.CloseHandle(slot.handle); err != nil {
			closeErr = domain.ErrScanUnavailable
		}
		slot.pin.Unpin()
	}
	b.slots = nil
	if err := windows.CloseHandle(b.port); err != nil {
		closeErr = domain.ErrScanUnavailable
	}
	return closeErr
}
