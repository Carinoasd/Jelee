package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProtectedModeRequiresRootAndUnwritableGroupOther(t *testing.T) {
	for name, sample := range map[string]struct {
		uid       uint32
		mode      uint32
		directory bool
		want      bool
	}{
		"root_file":      {0, unix.S_IFREG | 0644, false, true},
		"readonly_file":  {0, unix.S_IFREG | 0444, false, true},
		"root_directory": {0, unix.S_IFDIR | 0755, true, true},
		"service_owner":  {65532, unix.S_IFREG | 0555, false, false},
		"group_writable": {0, unix.S_IFREG | 0664, false, false},
		"other_writable": {0, unix.S_IFREG | 0666, false, false},
		"sticky_shared":  {0, unix.S_IFDIR | 01777, true, false},
		"symlink":        {0, unix.S_IFLNK | 0755, false, false},
		"wrong_kind":     {0, unix.S_IFDIR | 0755, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := protectedMode(unix.Stat_t{Uid: sample.uid, Mode: sample.mode}, sample.directory); got != sample.want {
				t.Fatal("unsafe protected-file permission classification")
			}
		})
	}
	if writeDenied(-1) {
		t.Fatal("invalid handle was mistaken for affirmative write denial")
	}
}

func TestNativeProtectedFilesCheckInodeAncestorsAndCaller(t *testing.T) {
	root := os.Getenv("JELEE_SANDBOX_PROTECTED_TEST_DIR")
	if root == "" {
		if os.Getenv("JELEE_REQUIRE_SANDBOX_TEST") == "true" {
			t.Fatal("required native run must supply root-owned protected image fixtures")
		}
		t.Skip("protected path checks require explicit root-owned container fixtures")
	}
	requireNative(t)
	path := filepath.Join(root, "ffprobe")
	policy := Policy{FFprobeSHA256: fileDigest(t, path), RequireProtectedFiles: true}
	launcher, err := New(context.Background(), Profile{FFprobePath: path}, policy)
	if err != nil || !launcher.ProtectedFilesRequired() {
		t.Fatalf("protected image file refused: %v", err)
	}
	opened, err := openPinnedELF(context.Background(), PinnedFile{Path: path, SHA256: policy.FFprobeSHA256})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.file.Close()
	t.Run("different_helper_object", func(t *testing.T) {
		if verifyHelperExecutable(path) != ErrUnavailable {
			t.Fatal("protected executable that is not the running helper was accepted")
		}
	})
	t.Run("relative_helper", func(t *testing.T) {
		if verifyHelperExecutable("ffprobe") != ErrUnavailable {
			t.Fatal("relative helper was accepted")
		}
	})
	t.Run("changed_leaf_identity", func(t *testing.T) {
		changed := opened
		changed.path = filepath.Join(root, "other")
		if verifyProtectedFile(changed) != ErrUnavailable {
			t.Fatal("path resolving to another inode was accepted")
		}
	})
	t.Run("writable_ancestor", func(t *testing.T) {
		path := filepath.Join(filepath.Dir(root), "unsafe-parent", "ffprobe")
		if _, err := New(context.Background(), Profile{FFprobePath: path}, policy); err != ErrUnavailable {
			t.Fatal("world-writable ancestor accepted even on a read-only mount")
		}
	})
	t.Run("service_owned_file", func(t *testing.T) {
		copyPath := filepath.Join(t.TempDir(), "ffprobe")
		copyFixture(t, path, copyPath)
		if _, err := New(context.Background(), Profile{FFprobePath: copyPath}, policy); err != ErrUnavailable {
			t.Fatal("service-owned executable accepted as a production pin")
		}
	})
	t.Run("writable_inode", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "writeable-")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if writeDenied(int(file.Fd())) {
			t.Fatal("caller-writable inode mistaken for protected")
		}
	})
	if verifyProtectedFile(pinnedELF{}) != ErrUnavailable {
		t.Fatal("missing pinned inode accepted")
	}
}
