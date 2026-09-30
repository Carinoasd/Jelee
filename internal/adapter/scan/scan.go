// Package scan enumerates one directory without reading or changing file content.
package scan

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// MaxDepth bounds path validation and directory lookup work independently of
// filesystem-specific limits. The configured root itself has depth zero.
const MaxDepth = 128

type Scanner struct{}

func New() *Scanner { return &Scanner{} }

// ValidateRoot checks that a local operator's absolute root can be opened as a
// directory. It neither creates the directory nor enumerates its contents.
func ValidateRoot(ctx context.Context, absolutePath string) error {
	file, err := openDirectory(ctx, absolutePath, ".")
	if err != nil {
		return err
	}
	defer file.Close()
	return ctx.Err()
}

// ScanDirectory reads immediate children only. Callback failures are returned
// unchanged to the private worker, which owns their safe public error mapping.
// A final, empty Done batch supplies the total number of skipped children.
func (*Scanner) ScanDirectory(ctx context.Context, directory domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
	if ctx == nil || emit == nil || !domain.ValidID(directory.RootID) {
		return domain.ErrScanUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := openDirectory(ctx, directory.RootPath, directory.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	return enumerate(ctx, file, directory, emit)
}

type directoryReader interface {
	ReadDir(int) ([]os.DirEntry, error)
	Close() error
}

func enumerate(ctx context.Context, file directoryReader, directory domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(closed)
		_ = file.Close()
	})
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	var skipped int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		children, readErr := file.ReadDir(domain.ScanBatchMaxEntries)
		if err := ctx.Err(); err != nil {
			return err
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return domain.ErrScanIO
		}
		batch := domain.ScanBatch{}
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := child.Name()
			if !validChildName(name) {
				skipped++
				continue
			}
			relative := name
			if directory.Path != "." {
				relative = directory.Path + "/" + name
			}
			if len(relative) > domain.ScanPathMaxBytes || pathDepth(relative) > MaxDepth {
				return domain.ErrScanLimit
			}
			// Files from os.Root retain handle-relative metadata on Linux, and
			// Windows ReadDir obtains metadata from the opened directory handle.
			// Do not replace this with os.Stat joined to an absolute pathname.
			info, err := child.Info()
			if err != nil {
				return scanError(ctx, domain.ErrScanIO)
			}
			switch info.Mode().Type() {
			case os.ModeDir:
				batch.Directories = append(batch.Directories, relative)
			case 0:
				modified := info.ModTime().UnixNano()
				if info.Size() < 0 || !time.Unix(0, modified).Equal(info.ModTime()) {
					return domain.ErrScanLimit
				}
				batch.Entries = append(batch.Entries, domain.InventoryEntry{
					RootID: directory.RootID, Path: relative, Kind: kind(name),
					Size: info.Size(), ModifiedUnixNano: modified,
				})
			default:
				// Includes all symbolic links, FIFOs, sockets, devices and
				// irregular/reparse entries. No file content is opened.
				skipped++
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(batch.Entries)+len(batch.Directories) > 0 {
			if err := emit(batch); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if errors.Is(readErr, io.EOF) {
			if err := emit(domain.ScanBatch{Done: true, Skipped: skipped}); err != nil {
				return err
			}
			return ctx.Err()
		}
	}
}

func validRelative(relative string) error {
	if len(relative) > domain.ScanPathMaxBytes || pathDepth(relative) > MaxDepth {
		return domain.ErrScanLimit
	}
	if !utf8.ValidString(relative) || !fs.ValidPath(relative) || strings.ContainsAny(relative, "\\:") || hasControl(relative) || !filepath.IsLocal(filepath.FromSlash(relative)) {
		return domain.ErrScanUnavailable
	}
	return nil
}

func validChildName(name string) bool {
	return name != "." && !strings.ContainsAny(name, "/\\:") && !hasControl(name) && utf8.ValidString(name) && fs.ValidPath(name) && filepath.IsLocal(name)
}

func hasControl(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func pathDepth(relative string) int {
	if relative == "." || relative == "" {
		return 0
	}
	return strings.Count(relative, "/") + 1
}

func kind(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".mp4", ".m4v", ".mkv", ".mov", ".avi", ".webm", ".ts", ".m2ts", ".mts", ".mpeg", ".mpg", ".m2v", ".vob", ".wmv", ".asf", ".flv", ".ogv", ".rm", ".rmvb", ".3gp", ".3g2", ".mxf":
		return "video"
	case ".nfo":
		return "nfo"
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif", ".heic", ".heif", ".bmp", ".tif", ".tiff", ".ico":
		return "image"
	default:
		return "other"
	}
}

func scanError(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}
