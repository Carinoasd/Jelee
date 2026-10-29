package nfo

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// The illegal-path issue class (G39.11). Item selection and the library
// validation job both refuse to read an NFO at an unsafe location; the
// refusals below are the same ones, now carrying a fixed reason code so a
// refusal can be reported as an issue instead of disappearing as
// "unavailable". A reason never loosens a refusal: every error built here is
// still domain.ErrNFOInputUnavailable to errors.Is.

// pathIssueError is an input refusal with one of the domain.NFOPath* codes.
type pathIssueError struct{ code string }

func (e *pathIssueError) Error() string { return "nfo path refused: " + e.code }

// Is keeps the refusal a domain.ErrNFOInputUnavailable for every caller that
// does not care why.
func (e *pathIssueError) Is(target error) bool { return target == domain.ErrNFOInputUnavailable }

func pathIssue(code string) error { return &pathIssueError{code: code} }

// pathIssueCode returns the code of a path refusal, or "" for any other error.
func pathIssueCode(err error) string {
	var refused *pathIssueError
	if errors.As(err, &refused) {
		return refused.code
	}
	return ""
}

// lexicalPathCode classifies a library-relative path without touching the
// file system. It returns "" for a path that can be opened.
func lexicalPathCode(relative string) string {
	if len(relative) > domain.ScanPathMaxBytes {
		return domain.NFOPathTooLong
	}
	elements := strings.Split(relative, "/")
	for _, element := range elements {
		if len(element) > 255 {
			return domain.NFOPathTooLong
		}
	}
	if path.IsAbs(relative) || slicesContain(elements, "..") {
		return domain.NFOPathOutsideRoot
	}
	if relative == "" || relative == "." || !utf8.ValidString(relative) || strings.ContainsAny(relative, "\\:") || strings.ContainsFunc(relative, unicode.IsControl) || !fs.ValidPath(relative) || !filepath.IsLocal(filepath.FromSlash(relative)) {
		return domain.NFOPathInvalidName
	}
	return ""
}

func slicesContain(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// InspectNFOPath reports why the NFO at relative (slash separated, below the
// absolute root rootAbs) must not be read: one of the domain.NFOPath* codes,
// or "" when the location is acceptable or something else (a missing file, an
// unusable root) is wrong, which the ordinary read reports as unavailable.
//
// Nothing is opened but directories, and no file content is read. The checks
// are the refusals of item selection: every element must be a real directory
// (a symbolic link is refused even when it stays inside the root), the NFO
// must be a regular file that is not a link, and no other entry of its
// directory may have the same name when case is ignored.
func InspectNFOPath(ctx context.Context, rootAbs, relative string) (string, error) {
	if ctx == nil {
		return "", domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if code := lexicalPathCode(relative); code != "" {
		return code, nil
	}
	if !filepath.IsAbs(rootAbs) {
		return "", nil
	}
	rootPath := filepath.Clean(rootAbs)
	info, err := os.Lstat(rootPath)
	if err != nil {
		return "", nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return domain.NFOPathElementSymlink, nil
	}
	if !info.IsDir() {
		return "", nil
	}
	root, err := os.OpenRoot(rootPath + string(os.PathSeparator) + ".")
	if err != nil {
		return "", nil
	}
	defer func() { _ = root.Close() }()
	elements := strings.Split(relative, "/")
	for i := range elements {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		info, err := root.Lstat(filepath.FromSlash(strings.Join(elements[:i+1], "/")))
		if err != nil {
			return "", nil
		}
		last := i == len(elements)-1
		switch {
		case info.Mode()&os.ModeSymlink != 0 && last:
			return domain.NFOPathSymlink, nil
		case info.Mode()&os.ModeSymlink != 0:
			return domain.NFOPathElementSymlink, nil
		case last && !info.Mode().IsRegular(), !last && !info.IsDir():
			return domain.NFOPathNotRegular, nil
		}
	}
	return caseCollision(ctx, root, relative)
}

// caseCollision reports domain.NFOPathCaseCollision when another entry of the
// directory of relative has the same name ignoring case. The listing is read
// in pages and bounded like item selection; a directory over the bound, or one
// that cannot be listed, is left to the ordinary read.
func caseCollision(ctx context.Context, root *os.Root, relative string) (string, error) {
	directory, err := root.OpenRoot(filepath.FromSlash(path.Dir(relative)))
	if err != nil {
		return "", nil
	}
	defer func() { _ = directory.Close() }()
	listing, err := directory.Open(".")
	if err != nil {
		return "", nil
	}
	defer func() { _ = listing.Close() }()
	name := path.Base(relative)
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		entries, err := listing.ReadDir(256)
		count += len(entries)
		if count > maxItemDirectoryEntries {
			return "", nil
		}
		for _, entry := range entries {
			if entry.Name() != name && strings.EqualFold(entry.Name(), name) {
				return domain.NFOPathCaseCollision, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		if err != nil {
			return "", nil
		}
	}
}
