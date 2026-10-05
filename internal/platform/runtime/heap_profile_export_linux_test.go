//go:build jelee_probe_tests && linux

package runtime

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func prepareHeapProfileExport() {
	// Only the dedicated export child calls this. A closed controller pipe
	// must return the fixed I/O failure instead of terminating via SIGPIPE.
	signal.Ignore(unix.SIGPIPE)
}

func exportHeapProfile(directory, stage string, out io.Writer) (result error) {
	failed := errors.New(heapProfileExportFailure)
	if stage != "before" && stage != "after" {
		return failed
	}
	dir, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return failed
	}
	defer func() {
		if err := unix.Close(dir); err != nil {
			result = failed
		}
	}()
	name := stage + ".heap.pb.gz"
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return failed
	}
	file := os.NewFile(uintptr(fd), "heap-profile")
	if file == nil {
		_ = unix.Close(fd)
		return failed
	}
	defer func() {
		if err := file.Close(); err != nil {
			result = failed
		}
	}()
	var before, after, linked unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Size <= 0 || before.Size > heapProfileMaxBytes {
		return failed
	}
	if err := streamHeapProfile(out, file, before.Size); err != nil {
		return failed
	}
	if unix.Fstat(fd, &after) != nil || unix.Fstatat(dir, name, &linked, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return failed
	}
	stable := func(other unix.Stat_t) bool {
		return other.Dev == before.Dev && other.Ino == before.Ino && other.Mode == before.Mode &&
			other.Size == before.Size && other.Mtim == before.Mtim && other.Ctim == before.Ctim
	}
	if !stable(after) || !stable(linked) {
		return failed
	}
	return nil
}

func heapProfileExportTempDir(t *testing.T) string {
	t.Helper()
	// WSL checkouts may live on NTFS, which cannot create the FIFO that this
	// test must actually reject. Use an owned native Linux directory.
	directory, err := os.MkdirTemp("/tmp", "jelee-heap-export-test-")
	if err != nil {
		t.Fatal("create native export test directory")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error("remove owned export test directory")
		}
	})
	return directory
}

func TestHeapProfileExportLinuxRequiresFixedRegularFile(t *testing.T) {
	for _, mode := range []string{"valid", "maximum", "missing", "empty", "oversized", "symlink-file", "symlink-directory", "directory", "fifo", "invalid-stage", "short-write"} {
		t.Run(mode, func(t *testing.T) {
			directory := filepath.Join(heapProfileExportTempDir(t), "profiles")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "before.heap.pb.gz")
			content := []byte("actual profile bytes")
			switch mode {
			case "missing":
			case "empty", "oversized", "maximum":
				file, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				var size int64
				if mode == "oversized" {
					size = heapProfileMaxBytes + 1
				} else if mode == "maximum" {
					size = heapProfileMaxBytes
				}
				if err := file.Truncate(size); err != nil {
					file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink-file":
				target := filepath.Join(heapProfileExportTempDir(t), "private")
				if err := os.WriteFile(target, content, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(path, content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink-directory" {
				link := filepath.Join(heapProfileExportTempDir(t), "link")
				if err := os.Symlink(directory, link); err != nil {
					t.Fatal(err)
				}
				directory = link
			}
			stage := "before"
			if mode == "invalid-stage" {
				stage = "../private"
			}
			var output bytes.Buffer
			var destination io.Writer = &output
			if mode == "maximum" {
				destination = io.Discard
			} else if mode == "short-write" {
				destination = heapProfileShortWriter{}
			}
			err := exportHeapProfile(directory, stage, destination)
			valid := mode == "valid" || mode == "maximum"
			if (err == nil) != valid || mode == "valid" && !bytes.Equal(output.Bytes(), content) {
				t.Fatal("heap exporter accepted invalid evidence or changed its bytes", err)
			}
			if !valid && mode != "short-write" && output.Len() != 0 {
				t.Fatal("heap exporter wrote bytes before accepting the source")
			}
		})
	}
}

type heapProfileChangingWriter struct {
	change func()
	called bool
}

func (writer *heapProfileChangingWriter) Write(data []byte) (int, error) {
	if !writer.called {
		writer.called = true
		writer.change()
	}
	return len(data), nil
}

func TestHeapProfileExportLinuxRejectsSourceChanges(t *testing.T) {
	for _, mode := range []string{"replaced", "grown", "shortened"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "before.heap.pb.gz")
			if err := os.WriteFile(path, []byte("profile"), 0600); err != nil {
				t.Fatal(err)
			}
			out := &heapProfileChangingWriter{change: func() {
				if mode == "replaced" {
					if err := os.Rename(path, path+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("profile"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					size := int64(1)
					if mode == "grown" {
						size = 8
					}
					if err := os.Truncate(path, size); err != nil {
						t.Fatal(err)
					}
				}
			}}
			if err := exportHeapProfile(directory, "before", out); err == nil {
				t.Fatal("heap exporter accepted a changed source")
			}
		})
	}
}
