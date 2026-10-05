//go:build windows || linux

package nfo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func nativeIdentityAt(t *testing.T, path string) nfoNativeIdentity {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("open native identity fixture")
	}
	defer file.Close()
	identity, err := observeNFONativeIdentity(file)
	if err != nil {
		t.Fatal("required native identity unavailable", err)
	}
	return identity
}

func checkNativeIdentityProcess(t *testing.T, root, target, saved string, equal bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNFONativeIdentityProcessHelper$")
	want := "different"
	if equal {
		want = "equal"
	}
	cmd.Env = append(os.Environ(), "JELEE_NFO_IDENTITY_TEST_ROOT="+root, "JELEE_NFO_IDENTITY_TEST_TARGET="+target, "JELEE_NFO_IDENTITY_TEST_RECORD="+saved, "JELEE_NFO_IDENTITY_TEST_WANT="+want)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native identity process failed: %v %s", err, out)
	}
}

func TestNFONativeIdentityAcrossProcessRenameAndWitness(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "movie.nfo")
	data := []byte("<movie><title>same</title></movie>")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{".", "parent", "parent/movie.nfo"} {
		identity := nativeIdentityAt(t, filepath.Join(root, target))
		saved := "identity-" + filepath.Base(target) + ".bin"
		if err := os.WriteFile(filepath.Join(root, saved), identity.record[:], 0600); err != nil {
			t.Fatal(err)
		}
		checkNativeIdentityProcess(t, root, target, saved, true)
	}
	// Content/mtime observations are separate from native object identity.
	if err := os.WriteFile(path, append(data, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Unix(1_700_000_000, 0), time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	checkNativeIdentityProcess(t, root, "parent/movie.nfo", "identity-movie.nfo.bin", true)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	stamp, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// A retained hardlink keeps this inode live after the original name changes.
	// This is a test fixture, not a production witness creation authority.
	pin := filepath.Join(parent, "owned-pin")
	if err := os.Link(path, pin); err != nil {
		t.Fatal("owned hardlink witness unavailable", err)
	}
	renamed := filepath.Join(parent, "renamed.nfo")
	if err := os.Rename(path, renamed); err != nil {
		t.Fatal(err)
	}
	checkNativeIdentityProcess(t, root, "parent/renamed.nfo", "identity-movie.nfo.bin", true)
	checkNativeIdentityProcess(t, root, "parent/owned-pin", "identity-movie.nfo.bin", true)
	if err := os.Remove(renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp.ModTime(), stamp.ModTime()); err != nil {
		t.Fatal(err)
	}
	checkNativeIdentityProcess(t, root, "parent/movie.nfo", "identity-movie.nfo.bin", false)
	checkNativeIdentityProcess(t, root, "parent/owned-pin", "identity-movie.nfo.bin", true)
	// Directory records also distinguish an equal-looking replacement parent.
	if err := os.Rename(parent, filepath.Join(root, "old-parent")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	checkNativeIdentityProcess(t, root, "parent", "identity-parent.bin", false)
	checkNativeIdentityProcess(t, root, "old-parent", "identity-parent.bin", true)
}

func TestNFONativeIdentityProcessHelper(t *testing.T) {
	root := os.Getenv("JELEE_NFO_IDENTITY_TEST_ROOT")
	if root == "" {
		return
	}
	directory, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal("open owned identity root")
	}
	defer directory.Close()
	data, err := directory.ReadFile(os.Getenv("JELEE_NFO_IDENTITY_TEST_RECORD"))
	if err != nil {
		t.Fatal("read private identity record")
	}
	saved, err := parseNFONativeIdentity(data)
	if err != nil {
		t.Fatal(err)
	}
	file, err := directory.Open(os.Getenv("JELEE_NFO_IDENTITY_TEST_TARGET"))
	if err != nil {
		t.Fatal("reopen identity target")
	}
	defer file.Close()
	current, err := observeNFONativeIdentity(file)
	if err != nil || (current == saved) != (os.Getenv("JELEE_NFO_IDENTITY_TEST_WANT") == "equal") {
		t.Fatal("persistent identity comparison did not match expected physical object", err)
	}
}

func TestNFONativeIdentityRejectsClosedHandle(t *testing.T) {
	if _, err := observeNFONativeIdentity(nil); err != errNativeIdentity {
		t.Fatal("nil handle accepted")
	}
	file, err := os.CreateTemp(t.TempDir(), "owned")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := observeNFONativeIdentity(file); err != errNativeIdentity {
		t.Fatal("closed handle accepted")
	}
}

func TestNFONativeIdentityRejectsPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := observeNFONativeIdentity(reader); err != errNativeIdentity {
		t.Fatal("non-filesystem handle accepted")
	}
}

func TestNFONativeIdentityRootReplacement(t *testing.T) {
	container := t.TempDir()
	root := filepath.Join(container, "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	identity := nativeIdentityAt(t, root)
	if err := os.WriteFile(filepath.Join(root, "identity.bin"), identity.record[:], 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(container, "old-root")); err != nil {
		t.Fatal(err)
	}
	checkNativeIdentityProcess(t, filepath.Join(container, "old-root"), ".", "identity.bin", true)
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "identity.bin"), identity.record[:], 0600); err != nil {
		t.Fatal(err)
	}
	checkNativeIdentityProcess(t, root, ".", "identity.bin", false)
}
