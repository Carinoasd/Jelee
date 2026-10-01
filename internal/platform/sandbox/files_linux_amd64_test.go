package sandbox

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPinnedFileRefusesNonRegularUnverifiedAndWrongArchitecture(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "static.elf")
	// A tiny ELF header is enough to test identity and architecture rejection;
	// it has no executable program segments and is never executed.
	header := make([]byte, 64)
	copy(header, "\x7fELF")
	header[4], header[5], header[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(header[16:], 2)
	binary.LittleEndian.PutUint16(header[18:], 62)
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	if err := os.WriteFile(good, header, 0600); err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]func([]byte){
		"invalid_magic":      func(data []byte) { data[0] = 'X' },
		"wrong_architecture": func(data []byte) { binary.LittleEndian.PutUint16(data[18:], 183) },
		"relocatable_object": func(data []byte) { binary.LittleEndian.PutUint16(data[16:], 1) },
	} {
		t.Run(name, func(t *testing.T) {
			data := append([]byte(nil), header...)
			mutation(data)
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := openPinnedELF(context.Background(), PinnedFile{Path: path, SHA256: hexDigest(data)}); err != ErrUnavailable {
				t.Fatal("invalid executable accepted")
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		if _, err := openPinnedELF(context.Background(), PinnedFile{Path: filepath.Join(root, "missing"), SHA256: hexDigest(header)}); err != ErrUnavailable {
			t.Fatal("missing executable accepted")
		}
	})
	t.Run("directory", func(t *testing.T) {
		if _, err := openPinnedELF(context.Background(), PinnedFile{Path: root, SHA256: hexDigest(header)}); err != ErrUnavailable {
			t.Fatal("directory accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		path := filepath.Join(root, "symbolic.elf")
		if err := os.Symlink(good, path); err != nil {
			t.Fatal(err)
		}
		if _, err := openPinnedELF(context.Background(), PinnedFile{Path: path, SHA256: hexDigest(header)}); err != ErrUnavailable {
			t.Fatal("noncanonical library path accepted")
		}
	})
	t.Run("digest_mismatch", func(t *testing.T) {
		if _, err := openPinnedELF(context.Background(), PinnedFile{Path: good, SHA256: strings.Repeat("0", 64)}); err != ErrUnavailable {
			t.Fatal("digest mismatch accepted")
		}
	})
	t.Run("empty", func(t *testing.T) {
		path := filepath.Join(root, "empty")
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := openPinnedELF(context.Background(), PinnedFile{Path: path, SHA256: hexDigest(nil)}); err != ErrUnavailable {
			t.Fatal("empty executable accepted")
		}
	})
	t.Run("oversized_sparse", func(t *testing.T) {
		path := filepath.Join(root, "oversized")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxPinnedFileBytes + 1); err != nil {
			file.Close()
			t.Fatal(err)
		}
		file.Close()
		if _, err := openPinnedELF(context.Background(), PinnedFile{Path: path, SHA256: hexDigest(nil)}); err != ErrUnavailable {
			t.Fatal("unbounded executable accepted")
		}
	})
	t.Run("fifo", func(t *testing.T) {
		path := filepath.Join(root, "fifo")
		if err := unix.Mkfifo(path, 0600); errors.Is(err, unix.EPERM) || errors.Is(err, unix.EOPNOTSUPP) {
			if os.Getenv("JELEE_REQUIRE_SANDBOX_TEST") == "true" {
				t.Fatal("native FIFO fixture unavailable")
			}
			t.Skip("fixture filesystem does not support FIFO; native Linux test required")
		} else if err != nil {
			t.Fatal(err)
		}
		if _, err := openPinnedELF(context.Background(), PinnedFile{Path: path, SHA256: hexDigest(nil)}); err != ErrUnavailable {
			t.Fatal("FIFO accepted or opened with a blocking read")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := openPinnedELF(ctx, PinnedFile{Path: good, SHA256: hexDigest(header)}); err != context.Canceled {
			t.Fatal("cancelled file verification proceeded")
		}
	})
}

func TestNewRefusesUntrustedPathsAndDependencyPolicy(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, path := range []string{"ffprobe", "/tools/../ffprobe", "/tools/other", "/tools/ffprobe\n", "/tools/\xff/ffprobe", "/tools:other/ffprobe"} {
		if _, err := New(context.Background(), Profile{FFprobePath: path}, Policy{FFprobeSHA256: digest}); err != ErrInvalid {
			t.Fatalf("unsafe path accepted: error=%v", err)
		}
	}
	profile := Profile{FFprobePath: "/tools/ffprobe"}
	for _, policy := range []Policy{
		{FFprobeSHA256: digest, Libraries: []PinnedFile{{Path: "/libs/a", SHA256: "bad"}}},
		{FFprobeSHA256: digest, Libraries: []PinnedFile{{Path: "relative", SHA256: digest}}},
		{FFprobeSHA256: digest, Libraries: []PinnedFile{{Path: "/libs/a", SHA256: digest}, {Path: "/libs/a", SHA256: digest}}},
		{FFprobeSHA256: digest, Libraries: make([]PinnedFile, 33)},
	} {
		if _, err := New(context.Background(), profile, policy); err != ErrInvalid {
			t.Fatal("untrusted dependency policy accepted")
		}
	}
	if _, err := New(nil, profile, Policy{FFprobeSHA256: digest}); err != ErrInvalid {
		t.Fatal("missing context accepted")
	}
}
