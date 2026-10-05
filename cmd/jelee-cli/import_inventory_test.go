package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestInventoryImportFileRecheck(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "film.mkv")
	if err := os.WriteFile(file, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	source := domain.InventoryImportSource{RootPath: root, Path: "film.mkv", Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano()}
	if !validInventoryImportFile(context.Background(), source) {
		t.Fatal("unchanged scan file rejected")
	}
	for name, change := range map[string]func(*domain.InventoryImportSource){
		"size":        func(v *domain.InventoryImportSource) { v.Size++ },
		"mtime":       func(v *domain.InventoryImportSource) { v.ModifiedUnixNano++ },
		"escape":      func(v *domain.InventoryImportSource) { v.Path = "../film.mkv" },
		"absolute":    func(v *domain.InventoryImportSource) { v.Path = file },
		"root":        func(v *domain.InventoryImportSource) { v.RootPath = "relative" },
		"unsupported": func(v *domain.InventoryImportSource) { v.Path = "film.nfo" },
	} {
		t.Run(name, func(t *testing.T) {
			value := source
			change(&value)
			if validInventoryImportFile(context.Background(), value) {
				t.Fatal("invalid source accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if validInventoryImportFile(ctx, source) {
		t.Fatal("cancel ignored")
	}
	if err := os.Chtimes(file, time.Now(), info.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if validInventoryImportFile(context.Background(), source) {
		t.Fatal("file modification not detected")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if validInventoryImportFile(context.Background(), source) {
		t.Fatal("missing file accepted")
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	if validInventoryImportFile(context.Background(), source) {
		t.Fatal("directory accepted as video")
	}
}

func TestInventoryImportRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	file := filepath.Join(outside, "film.mkv")
	if err := os.WriteFile(file, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable on this host")
	}
	source := domain.InventoryImportSource{RootPath: root, Path: "link/film.mkv", Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano()}
	if validInventoryImportFile(context.Background(), source) {
		t.Fatal("symlink directory accepted")
	}
}

func TestInventoryImportValidatesBeforeConfiguration(t *testing.T) {
	for _, args := range [][]string{nil, {"--job", "bad"}, {"--kind", "Season"}, {"--entry", "bad"}, {"--parent", "bad"}} {
		var output bytes.Buffer
		if code := runInventoryImport(context.Background(), args, &output, &output); code != 2 {
			t.Fatal("invalid request reached configuration", code)
		}
	}
}
