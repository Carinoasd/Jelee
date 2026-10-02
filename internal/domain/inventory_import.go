package domain

import (
	"path"
	"strings"
)

// InventoryImportSource is private repository state, never request JSON.
type InventoryImportSource struct {
	JobID, EntryID, LibraryID, RootID, RootPath, Path    string
	Size, ModifiedUnixNano, Generation, BaselineRevision int64
}

func (InventoryImportSource) String() string   { return "inventory import source (paths redacted)" }
func (InventoryImportSource) GoString() string { return "inventory import source (paths redacted)" }

// ImportVideoContentType restricts registration to the existing delivery types.
func ImportVideoContentType(relative string) string {
	if _, ok := AdjacentNFOPath(relative); !ok {
		return ""
	}
	switch strings.ToLower(path.Ext(relative)) {
	case ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".avi":
		return "video/x-msvideo"
	case ".ts":
		return "video/mp2t"
	}
	return ""
}
