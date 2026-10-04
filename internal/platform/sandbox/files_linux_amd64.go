package sandbox

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxPinnedFileBytes = 512 << 20

type pinnedELF struct {
	file        *os.File
	path        string
	interpreter string
	needed      []string
	soname      string
}

type prepared struct {
	tool        pinnedELF
	libraries   []pinnedELF
	interpreter int
}

func (p *prepared) close() {
	if p.tool.file != nil {
		_ = p.tool.file.Close()
	}
	for _, library := range p.libraries {
		_ = library.file.Close()
	}
}

func verifyProfile(ctx context.Context, profile Profile, policy Policy) error {
	p, err := prepare(ctx, profile, policy)
	if err == nil {
		p.close()
	}
	return err
}

func prepare(ctx context.Context, profile Profile, policy Policy) (_ *prepared, resultErr error) {
	p := &prepared{interpreter: -1}
	defer func() {
		if resultErr != nil {
			p.close()
		}
	}()
	var err error
	p.tool, err = openPinnedELF(ctx, PinnedFile{Path: profile.FFprobePath, SHA256: policy.FFprobeSHA256})
	if err != nil {
		return nil, err
	}
	if policy.RequireProtectedFiles && verifyProtectedFile(p.tool) != nil {
		return nil, ErrUnavailable
	}
	info, err := p.tool.file.Stat()
	if err != nil || info.Mode()&0111 == 0 {
		return nil, ErrUnavailable
	}
	byName := make(map[string]int, len(policy.Libraries))
	for _, dependency := range policy.Libraries {
		library, err := openPinnedELF(ctx, dependency)
		if err != nil {
			return nil, err
		}
		p.libraries = append(p.libraries, library)
		if policy.RequireProtectedFiles && verifyProtectedFile(library) != nil {
			return nil, ErrUnavailable
		}
		index := len(p.libraries) - 1
		if library.soname == "" {
			return nil, ErrUnavailable
		}
		if _, duplicate := byName[library.soname]; duplicate {
			return nil, ErrUnavailable
		}
		byName[library.soname] = index
	}
	used := make(map[int]bool, len(p.libraries))
	queue := append([]string(nil), p.tool.needed...)
	if p.tool.interpreter != "" {
		interpreter, err := filepath.EvalSymlinks(p.tool.interpreter)
		if err != nil {
			return nil, ErrUnavailable
		}
		for index, library := range p.libraries {
			if interpreter == library.path {
				p.interpreter = index
				used[index] = true
				queue = append(queue, library.needed...)
				break
			}
		}
		if p.interpreter < 0 {
			return nil, ErrUnavailable
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		index, found := byName[name]
		if !found {
			return nil, ErrUnavailable
		}
		if used[index] {
			continue
		}
		used[index] = true
		queue = append(queue, p.libraries[index].needed...)
	}
	// Even correctly hashed unrelated files cannot expand the read policy.
	if len(used) != len(p.libraries) {
		return nil, ErrUnavailable
	}
	return p, nil
}

// openPinnedFile opens one canonical regular file without following links
// and verifies its digest. The returned descriptor is the verified object.
func openPinnedFile(ctx context.Context, pin PinnedFile) (_ *os.File, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(pin.Path)
	if err != nil || canonical != pin.Path {
		return nil, ErrUnavailable
	}
	fd, err := unix.Open(pin.Path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	file := os.NewFile(uintptr(fd), "approved media tool file")
	defer func() {
		if resultErr != nil {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxPinnedFileBytes {
		return nil, ErrUnavailable
	}
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > maxPinnedFileBytes {
				return nil, ErrUnavailable
			}
			_, _ = hash.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, ErrUnavailable
		}
	}
	after, err := file.Stat()
	if err != nil || total != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || hex.EncodeToString(hash.Sum(nil)) != pin.SHA256 {
		return nil, ErrUnavailable
	}
	return file, nil
}

// openDataFiles opens and verifies the OCR language data grants. Each is a
// pinned regular file; production also requires protected ownership.
func openDataFiles(ctx context.Context, pins []PinnedFile, protected bool) (_ []*os.File, resultErr error) {
	var files []*os.File
	defer func() {
		if resultErr != nil {
			closeFiles(files)
		}
	}()
	for _, pin := range pins {
		file, err := openPinnedFile(ctx, pin)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
		if protected && verifyProtectedFile(pinnedELF{file: file, path: pin.Path}) != nil {
			return nil, ErrUnavailable
		}
	}
	return files, nil
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

func verifyDataFiles(ctx context.Context, pins []PinnedFile, protected bool) error {
	files, err := openDataFiles(ctx, pins, protected)
	closeFiles(files)
	return err
}

func openPinnedELF(ctx context.Context, pin PinnedFile) (_ pinnedELF, resultErr error) {
	file, err := openPinnedFile(ctx, pin)
	if err != nil {
		return pinnedELF{}, err
	}
	defer func() {
		if resultErr != nil {
			_ = file.Close()
		}
	}()
	parsed, err := elf.NewFile(file)
	if err != nil || parsed.Class != elf.ELFCLASS64 || parsed.Machine != elf.EM_X86_64 || (parsed.Type != elf.ET_EXEC && parsed.Type != elf.ET_DYN) {
		return pinnedELF{}, ErrUnavailable
	}
	result := pinnedELF{file: file, path: pin.Path}
	result.needed, err = parsed.DynString(elf.DT_NEEDED)
	if err != nil || len(result.needed) > 32 {
		return pinnedELF{}, ErrUnavailable
	}
	for _, needed := range result.needed {
		if needed == "" || len(needed) > 255 || strings.ContainsAny(needed, "/\\:\x00") {
			return pinnedELF{}, ErrUnavailable
		}
	}
	names, err := parsed.DynString(elf.DT_SONAME)
	if err != nil || len(names) > 1 {
		return pinnedELF{}, ErrUnavailable
	}
	if len(names) == 1 {
		result.soname = names[0]
	}
	for _, program := range parsed.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		if result.interpreter != "" || program.Filesz < 2 || program.Filesz > 4096 {
			return pinnedELF{}, ErrUnavailable
		}
		data, err := io.ReadAll(io.LimitReader(program.Open(), 4097))
		if err != nil || len(data) != int(program.Filesz) || data[len(data)-1] != 0 {
			return pinnedELF{}, ErrUnavailable
		}
		result.interpreter = string(data[:len(data)-1])
		if !validPath(result.interpreter) {
			return pinnedELF{}, ErrUnavailable
		}
	}
	return result, nil
}
