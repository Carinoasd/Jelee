package logging

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	logFileMode = 0o600
	logDirMode  = 0o700
	backupStamp = "20060102T150405.000000000Z"
)

// RotateOptions configures a size and/or time rotated log file.
type RotateOptions struct {
	// Path is the absolute path of the active log file.
	Path string
	// MaxBytes rotates before a write would grow the file past this size.
	// Zero disables size rotation.
	MaxBytes int64
	// Interval rotates once the active file is this old. Zero disables time
	// rotation.
	Interval time.Duration
	// MaxBackups is the number of rotated files kept; older ones are removed.
	MaxBackups int
	// Compress gzips rotated files.
	Compress bool
}

// RotatingFile is an io.WriteCloser that owns one log file and its backups.
// Files are created with mode 0600 and existing files are narrowed to 0600.
type RotatingFile struct {
	opts   RotateOptions
	now    func() time.Time
	mu     sync.Mutex
	file   *os.File
	size   int64
	opened time.Time
}

// OpenRotatingFile opens (or creates) the active file.
func OpenRotatingFile(opts RotateOptions) (*RotatingFile, error) {
	return openRotatingFile(opts, time.Now)
}

func openRotatingFile(opts RotateOptions, now func() time.Time) (*RotatingFile, error) {
	if opts.Path == "" || !filepath.IsAbs(opts.Path) {
		return nil, errors.New("log file path must be absolute")
	}
	if opts.MaxBytes < 0 || opts.Interval < 0 || opts.MaxBackups < 0 {
		return nil, errors.New("invalid log rotation limits")
	}
	opts.Path = filepath.Clean(opts.Path)
	f := &RotatingFile{opts: opts, now: now}
	if err := f.open(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *RotatingFile) open() error {
	if err := os.MkdirAll(filepath.Dir(f.opts.Path), logDirMode); err != nil {
		return errors.New("cannot create log directory")
	}
	file, err := os.OpenFile(f.opts.Path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, logFileMode)
	if err != nil {
		return errors.New("cannot open log file")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return errors.New("log file is not a regular file")
	}
	if info.Mode().Perm() != logFileMode {
		if err := file.Chmod(logFileMode); err != nil {
			file.Close()
			return errors.New("cannot restrict log file permissions")
		}
	}
	f.file, f.size, f.opened = file, info.Size(), f.now()
	return nil
}

func (f *RotatingFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return 0, ErrClosed
	}
	if f.size > 0 && f.due(int64(len(p))) {
		if err := f.rotate(); err != nil {
			// Keep logging into the current file rather than losing records.
			if f.file == nil {
				return 0, err
			}
		}
	}
	n, err := f.file.Write(p)
	f.size += int64(n)
	return n, err
}

func (f *RotatingFile) due(next int64) bool {
	if f.opts.MaxBytes > 0 && f.size+next > f.opts.MaxBytes {
		return true
	}
	return f.opts.Interval > 0 && !f.now().Before(f.opened.Add(f.opts.Interval))
}

// Rotate forces a rotation, for example on an operator request.
func (f *RotatingFile) Rotate() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return ErrClosed
	}
	return f.rotate()
}

func (f *RotatingFile) rotate() error {
	if err := f.file.Close(); err != nil {
		f.file = nil
		return f.reopen(errors.New("cannot close log file"))
	}
	f.file = nil
	backup, err := f.backupName()
	if err != nil {
		return f.reopen(err)
	}
	if err := os.Rename(f.opts.Path, backup); err != nil {
		return f.reopen(errors.New("cannot rotate log file"))
	}
	if err := f.open(); err != nil {
		return err
	}
	if f.opts.Compress {
		if err := compressFile(backup); err != nil {
			return err
		}
	}
	return f.prune()
}

func (f *RotatingFile) reopen(cause error) error {
	if err := f.open(); err != nil {
		return err
	}
	return cause
}

func (f *RotatingFile) prefixExt() (string, string) {
	base := filepath.Base(f.opts.Path)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext) + "-", ext
}

func (f *RotatingFile) backupName() (string, error) {
	prefix, ext := f.prefixExt()
	stamp := f.now().UTC().Format(backupStamp)
	dir := filepath.Dir(f.opts.Path)
	for i := 0; i < 1000; i++ {
		// A fixed-width counter keeps lexical order equal to rotation order.
		name := prefix + stamp + fmt.Sprintf(".%03d", i) + ext
		candidate := filepath.Join(dir, name)
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			if _, err := os.Lstat(candidate + ".gz"); errors.Is(err, os.ErrNotExist) {
				return candidate, nil
			}
		}
	}
	return "", errors.New("cannot name rotated log file")
}

func compressFile(path string) error {
	in, err := os.Open(path) //nolint:gosec // G304: a rotated file inside the configured log directory
	if err != nil {
		return errors.New("cannot read rotated log file")
	}
	defer in.Close()
	out, err := os.OpenFile(path+".gz", os.O_WRONLY|os.O_CREATE|os.O_EXCL, logFileMode) //nolint:gosec // G304: a rotated file inside the configured log directory
	if err != nil {
		return errors.New("cannot create compressed log file")
	}
	gz := gzip.NewWriter(out)
	_, copyErr := io.Copy(gz, in)
	closeErr := gz.Close()
	syncErr := out.Sync()
	fileErr := out.Close()
	if copyErr != nil || closeErr != nil || syncErr != nil || fileErr != nil {
		os.Remove(path + ".gz")
		return errors.New("cannot compress rotated log file")
	}
	in.Close()
	return os.Remove(path)
}

// Backups lists rotated files, oldest first.
func (f *RotatingFile) Backups() ([]string, error) {
	prefix, ext := f.prefixExt()
	entries, err := os.ReadDir(filepath.Dir(f.opts.Path))
	if err != nil {
		return nil, errors.New("cannot list log directory")
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".gz")
		if !strings.HasSuffix(rest, ext) {
			continue
		}
		stamp := strings.TrimSuffix(rest, ext)
		if len(stamp) < len(backupStamp) {
			continue
		}
		if _, err := time.Parse(backupStamp, stamp[:len(backupStamp)]); err != nil {
			continue
		}
		names = append(names, filepath.Join(filepath.Dir(f.opts.Path), name))
	}
	sort.Strings(names)
	return names, nil
}

func (f *RotatingFile) prune() error {
	names, err := f.Backups()
	if err != nil {
		return err
	}
	var errs error
	for len(names) > f.opts.MaxBackups {
		if err := os.Remove(names[0]); err != nil {
			errs = errors.New("cannot remove expired log file")
		}
		names = names[1:]
	}
	return errs
}

func (f *RotatingFile) Sync() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return ErrClosed
	}
	return f.file.Sync()
}

func (f *RotatingFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return nil
	}
	syncErr := f.file.Sync()
	err := f.file.Close()
	f.file = nil
	if err != nil {
		return err
	}
	return syncErr
}
