package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeInput(t *testing.T, root, relative string, content []byte) string {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, content, 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestOpenReadOnlySeekableAndOriginalBytesUnchanged(t *testing.T) {
	root := t.TempDir()
	relative := "电影 分类/-protocol_whitelist ALL.mp4"
	content := bytes.Repeat([]byte("unaltered media bytes\x00\xff"), 4096)
	name := writeInput(t, root, relative, content)
	wantHash := sha256.Sum256(content)
	stamp := time.Unix(1720000000, 123456000)
	if err := os.Chtimes(name, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	wantInfo, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	input, err := Open(context.Background(), Source{RootPath: root, RelativePath: relative})
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if got := input.Metadata(); got.Size != int64(len(content)) || got.ModifiedUnixNano != wantInfo.ModTime().UnixNano() {
		t.Fatalf("opened metadata mismatch: %+v", got)
	}
	if _, err := input.Stdin().Write([]byte("must not write")); err == nil {
		t.Fatal("source descriptor is writable")
	}
	if _, err := input.Stdin().Seek(-8, io.SeekEnd); err != nil {
		t.Fatal("opened regular file is not seekable")
	}
	tail := make([]byte, 8)
	if _, err := io.ReadFull(input.Stdin(), tail); err != nil || !bytes.Equal(tail, content[len(content)-8:]) {
		t.Fatal("seek did not read expected tail")
	}
	if _, err := input.Stdin().Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, input.Stdin(), make([]byte, 32*1024)); err != nil || !bytes.Equal(hash.Sum(nil), wantHash[:]) {
		t.Fatal("input bytes changed")
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(name)
	if err != nil || sha256.Sum256(after) != wantHash {
		t.Fatal("source was modified")
	}
}

func TestDescriptorSurvivesSourcePathReplacement(t *testing.T) {
	root := t.TempDir()
	name := writeInput(t, root, "movie.mp4", []byte("opened original"))
	input, err := Open(context.Background(), Source{RootPath: root, RelativePath: "movie.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := os.Rename(name, filepath.Join(root, "original.mp4")); err != nil {
		t.Fatal("cannot replace opened source path: ", err)
	}
	writeInput(t, root, "movie.mp4", []byte("replacement must not be read"))
	data, err := io.ReadAll(input.Stdin())
	if err != nil || string(data) != "opened original" {
		t.Fatal("input followed the replaced pathname")
	}
}

func TestRealChildInheritsSeekableReadOnlyStdinWithoutSourceArgument(t *testing.T) {
	root := t.TempDir()
	name := writeInput(t, root, "untrusted spaces 片名.mp4", nil)
	file, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	const size = 64 << 20
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("tail"), size-4); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	input, err := Open(context.Background(), Source{RootPath: root, RelativePath: filepath.Base(name)})
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if input.Metadata().Size != size {
		t.Fatal("large source metadata incorrect")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestInputDescriptorChild$")
	cmd.Env = append(os.Environ(), "JELEE_PROBE_INPUT_HELPER=1")
	cmd.Stdin = input.Stdin()
	// Standalone coverage binaries may emit instrumentation diagnostics to
	// stderr. Only stdout carries this helper's descriptor-content result.
	output, err := cmd.Output()
	if err != nil || string(output) != "tail" {
		t.Fatalf("inherited stdin was not a seekable read-only file: err=%v output=%q", err, output)
	}
}

func TestInputDescriptorChild(t *testing.T) {
	if os.Getenv("JELEE_PROBE_INPUT_HELPER") != "1" {
		return
	}
	if _, err := os.Stdin.Write([]byte("forbidden")); err == nil {
		os.Exit(21)
	}
	if _, err := os.Stdin.Seek(-4, io.SeekEnd); err != nil {
		os.Exit(22)
	}
	var tail [4]byte
	if _, err := io.ReadFull(os.Stdin, tail[:]); err != nil {
		os.Exit(23)
	}
	if _, err := os.Stdout.Write(tail[:]); err != nil {
		os.Exit(24)
	}
	os.Exit(0)
}

func TestInvalidPathsAndUnavailableInputsReturnFixedErrors(t *testing.T) {
	root := t.TempDir()
	writeInput(t, root, "regular.mp4", []byte("content"))
	invalid := []string{"", ".", "..", "../regular.mp4", "/regular.mp4", "a//b", "a/./b", "a/../b", "a\\b", "file:regular.mp4", "a\x00b", "a\nb", "a\tb", "a\u0085b", "bad\xff", strings.Repeat("x", MaxPathBytes+1), strings.Repeat("a/", MaxDepth) + "b"}
	for _, relative := range invalid {
		if input, err := Open(context.Background(), Source{RootPath: root, RelativePath: relative}); input != nil || err != ErrInvalidInput {
			t.Fatalf("invalid path accepted or leaked: input=%v err=%v", input != nil, err)
		}
	}
	for _, relative := range []string{"missing", "regular.mp4/child"} {
		if input, err := Open(context.Background(), Source{RootPath: root, RelativePath: relative}); input != nil || err != ErrUnavailable {
			t.Fatalf("unavailable path error was not fixed: %v", err)
		}
	}
	for _, source := range []Source{{RootPath: "relative-root", RelativePath: "regular.mp4"}, {RootPath: root + "\n", RelativePath: "regular.mp4"}} {
		if _, err := Open(context.Background(), source); err != ErrInvalidInput {
			t.Fatalf("invalid root accepted: %v", err)
		}
	}
	if _, err := Open(nil, Source{RootPath: root, RelativePath: "regular.mp4"}); err != ErrInvalidInput {
		t.Fatal("nil context accepted")
	}
	if _, err := Open(context.Background(), Source{RootPath: filepath.Join(root, "regular.mp4"), RelativePath: "child"}); err != ErrUnavailable {
		t.Fatal("regular file accepted as root")
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), Source{RootPath: root, RelativePath: "directory"}); err != ErrUnavailable {
		t.Fatal("directory accepted as input")
	}
}

func TestVisibleSymlinksRefusedAndExternalPathsCannotEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeInput(t, root, "inside/original.mp4", []byte("inside"))
	writeInput(t, outside, "secret.mp4", []byte("secret"))
	for name, target := range map[string]string{"external.mp4": filepath.Join(outside, "secret.mp4"), "internal.mp4": filepath.Join(root, "inside", "original.mp4"), "outside-dir": outside, "inside-dir": filepath.Join(root, "inside")} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Skipf("symlink creation unavailable on this filesystem: %v", err)
		}
	}
	for _, relative := range []string{"external.mp4", "internal.mp4", "outside-dir/secret.mp4", "inside-dir/original.mp4"} {
		if _, err := Open(context.Background(), Source{RootPath: root, RelativePath: relative}); err != ErrUnavailable {
			t.Fatalf("visible symlink was not refused: %v", err)
		}
	}
	if _, err := Open(context.Background(), Source{RootPath: filepath.Join(root, "outside-dir"), RelativePath: "secret.mp4"}); err != ErrUnavailable {
		t.Fatal("symlink root accepted")
	}
}

func TestCancellationAndConcurrentCloseReleaseTheInput(t *testing.T) {
	root := t.TempDir()
	writeInput(t, root, "movie.mp4", []byte("unchanged"))
	ctx, cancel := context.WithCancel(context.Background())
	input, err := Open(ctx, Source{RootPath: root, RelativePath: "movie.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-input.cancelDone:
	case <-time.After(5 * time.Second):
		t.Fatal("context cancellation did not close input")
	}
	if _, err := input.Stdin().Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatal("cancelled input remains open")
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := input.Close(); err != nil {
				t.Error("concurrent close returned error")
			}
		})
	}
	wg.Wait()
	if _, err := Open(ctx, Source{RootPath: root, RelativePath: "movie.mp4"}); err != context.Canceled {
		t.Fatal("cancelled context accepted")
	}
	var empty Input
	var absent *Input
	if empty.Close() != nil || absent.Close() != nil || absent.Stdin() != nil || absent.Metadata() != (Metadata{}) {
		t.Fatal("empty input cleanup unsafe")
	}
}

func TestUnexpectedDescriptorCloseDoesNotLeakSourcePath(t *testing.T) {
	root := t.TempDir()
	writeInput(t, root, "private-source.mp4", []byte("unchanged"))
	input, err := Open(context.Background(), Source{RootPath: root, RelativePath: "private-source.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	// A caller must not close the borrowed handle, but if it does, cleanup must
	// still return a fixed error instead of an os.PathError containing its name.
	if err := input.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := input.Close(); err != ErrUnavailable {
			t.Fatalf("cleanup did not sanitize the close failure: %v", err)
		}
	}
}

func TestFixedFDArgumentsDoNotEnableSecondaryInputProtocols(t *testing.T) {
	args := FFprobeFDArguments()
	options := map[string]string{}
	for index := 0; index+1 < len(args); index++ {
		if strings.HasPrefix(args[index], "-") && !strings.HasPrefix(args[index+1], "-") {
			options[args[index]] = args[index+1]
		}
	}
	if options["-protocol_whitelist"] != "fd" || options["-i"] != "fd:" || options["-enable_drefs"] != "0" {
		t.Fatal("path or secondary protocols enabled")
	}
	formats := strings.Split(options["-format_whitelist"], ",")
	allowed := map[string]bool{"matroska": true, "webm": true, "mov": true, "mp4": true, "m4a": true, "3gp": true, "3g2": true, "mj2": true, "avi": true, "mpegts": true, "mpeg": true, "mpegvideo": true, "flv": true, "ogg": true}
	for _, format := range formats {
		if !allowed[format] {
			t.Fatalf("unreviewed demuxer %q", format)
		}
	}
	if len(formats) == 0 || options["-of"] != "json" || options["-max_streams"] == "" || options["-max_alloc"] == "" {
		t.Fatal("fixed resource/output policy missing")
	}
	before := FFprobeFDArguments()
	args[0] = "-report"
	if !reflect.DeepEqual(before, FFprobeFDArguments()) {
		t.Fatal("caller mutated shared command policy")
	}
}
