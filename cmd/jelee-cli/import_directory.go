package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func validImportDirectory(rootPath, relative string) bool {
	if !filepath.IsAbs(rootPath) || !domain.ValidDirectorySourcePath(relative) {
		return false
	}
	info, err := os.Lstat(filepath.Clean(rootPath))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return false
	}
	defer root.Close()
	parts := strings.Split(relative, "/")
	for i := range parts {
		info, err := root.Lstat(filepath.FromSlash(strings.Join(parts[:i+1], "/")))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}
