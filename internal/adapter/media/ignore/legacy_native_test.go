//go:build linux || windows

package ignoresource

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyNativeSourceIsSeparateAndReadonly(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{".ignore": "legacy", ".jeleeignore": "custom"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := openNativeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	budget := 100
	stamp, data, err := readLegacyRule(context.Background(), parent, &budget)
	if err != nil || string(data) != "legacy" || !stamp.present || stamp.digest != sha256.Sum256([]byte("legacy")) || budget != 94 {
		t.Fatal("wrong source or budget", err)
	}
	custom, bytes, err := readRule(context.Background(), parent, &budget)
	if err != nil || string(bytes) != "custom" || custom.state.identity == stamp.state.identity {
		t.Fatal("families confused", err)
	}
	file, err := parent.(interface{ OpenLegacyRule() (sourceFile, error) }).OpenLegacyRule()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := file.(*nativeFile).file.Write([]byte("bad")); err == nil || n != 0 {
		t.Fatal("legacy source writable")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".ignore")); err != nil {
		t.Fatal(err)
	}
	stamp, data, err = readLegacyRule(context.Background(), parent, &budget)
	if err != nil || stamp.present || len(data) != 0 {
		t.Fatal("missing source not absent", err)
	}
	if err := os.Mkdir(filepath.Join(root, ".ignore"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLegacyRule(context.Background(), parent, &budget); err != ErrUnsafe {
		t.Fatal("directory accepted", err)
	}
}
func TestLegacyNativeRevalidatesContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".ignore")
	if err := os.WriteFile(path, []byte("a.mkv"), 0600); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(1700000000, 0)
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := openNativeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	budget := 100
	before, _, err := readLegacyRule(context.Background(), parent, &budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("b.mkv"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, _, err := readLegacyRule(context.Background(), parent, &budget)
	if err != nil || before.state != after.state || before.digest == after.digest {
		t.Fatal("same metadata edit not observed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := readLegacyRule(ctx, parent, &budget); err != context.Canceled {
		t.Fatal("cancel ignored", err)
	}
	budget = 1
	if _, _, err := readLegacyRule(context.Background(), parent, &budget); err != ErrLimit {
		t.Fatal("byte budget ignored", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLegacyRule(context.Background(), parent, &budget); err != ErrRead {
		t.Fatal("closed parent reopened", err)
	}
}
