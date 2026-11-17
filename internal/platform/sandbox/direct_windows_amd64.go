package sandbox

import (
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// directBuild selects the Windows direct launcher: the parent verifies and
// pins ffprobe.exe, and process starts it under Confinement (ADR 0011).
const directBuild = true

// maxImageBytes bounds the hashed tool image, as for the Linux ELF checks.
const maxImageBytes = 512 << 20

// newDirect validates the Windows profile and pins the tool image. The
// returned launcher keeps the image open without FILE_SHARE_WRITE or
// FILE_SHARE_DELETE, so the bytes hashed here are the bytes CreateProcess
// maps for as long as the launcher lives: the file cannot be written,
// renamed or deleted, and its directories cannot be renamed meanwhile.
func newDirect(ctx context.Context, profile Profile, policy Policy) (*Launcher, error) {
	if !ValidWindowsToolPath(profile.FFprobePath, "ffprobe.exe") || !validDirectPolicy(policy) {
		return nil, ErrInvalid
	}
	image, err := pinImage(ctx, profile.FFprobePath, policy.FFprobeSHA256)
	if err != nil {
		return nil, err
	}
	return &Launcher{executable: profile.FFprobePath, ffprobePath: profile.FFprobePath, protected: policy.RequireProtectedFiles, direct: true, image: image}, nil
}

func pinImage(ctx context.Context, location, digest string) (_ *os.File, resultErr error) {
	name, err := windows.UTF16PtrFromString(location)
	if err != nil {
		return nil, ErrInvalid
	}
	// OPEN_REPARSE_POINT opens a symbolic link or junction leaf itself, which
	// the attribute check below then refuses instead of following it.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	file := os.NewFile(uintptr(handle), location)
	defer func() {
		if resultErr != nil {
			_ = file.Close()
		}
	}()
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &info) != nil ||
		info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return nil, ErrUnavailable
	}
	size := int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow)
	if size < 64 || size > maxImageBytes {
		return nil, ErrUnavailable
	}
	// A junction or symbolic link in an ancestor resolves elsewhere; the
	// final path must be the requested one (case-insensitive, DOS volume).
	if final, ok := finalPath(handle); !ok || !strings.EqualFold(final, location) {
		return nil, ErrUnavailable
	}
	if err := hashImage(ctx, file, size, digest); err != nil {
		return nil, err
	}
	executable, err := pe.NewFile(io.NewSectionReader(file, 0, size))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = executable.Close() }()
	if executable.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || executable.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 || executable.Characteristics&pe.IMAGE_FILE_DLL != 0 {
		return nil, ErrUnavailable
	}
	return file, nil
}

func finalPath(handle windows.Handle) (string, bool) {
	var buffer [windows.MAX_LONG_PATH]uint16
	// Flags 0: FILE_NAME_NORMALIZED with VOLUME_NAME_DOS (\\?\C:\...).
	n, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil || n == 0 || int(n) >= len(buffer) {
		return "", false
	}
	value := windows.UTF16ToString(buffer[:n])
	trimmed, found := strings.CutPrefix(value, `\\?\`)
	if !found || strings.HasPrefix(trimmed, `UNC\`) {
		return "", false
	}
	return trimmed, true
}

func hashImage(ctx context.Context, file *os.File, size int64, digest string) error {
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := file.ReadAt(buffer, total)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
			total += int64(n)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || total > size {
			return ErrUnavailable
		}
	}
	if total != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return ErrUnavailable
	}
	return nil
}
