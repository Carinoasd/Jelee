package nfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func sourceFixture(t testing.TB, content []byte) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "library")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "電影 title.nfo"
	if err := os.WriteFile(filepath.Join(root, name), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, name
}

func sourceDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

type mutatingSourceWriter struct{}

func (mutatingSourceWriter) Write(data []byte) (int, error) {
	for i := range data {
		data[i] = '!' //nolint:staticcheck // SA1023: a hostile writer proves Source copies its bytes
	}
	return len(data), nil
}

func TestSourcePrivateOriginalHashAndIndependentParse(t *testing.T) {
	ctx := context.Background()
	content := []byte("\xef\xbb\xbf<movie><title>電影</title><genre>Drama</genre><!-- kept --></movie>")
	root, name := sourceFixture(t, content)
	before, err := os.Stat(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	source, err := ReadSource(ctx, root, name, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	stamp := source.Stamp()
	if stamp.Size != int64(len(content)) || stamp.ModifiedUnixNano != before.ModTime().UnixNano() || stamp.SHA256 != sourceDigest(content) || stamp.FingerprintVersion != SourceFingerprintVersion {
		t.Fatal("source stamp does not describe the original encoded bytes")
	}
	stamp.SHA256 = "caller edit"
	if source.Stamp().SHA256 != sourceDigest(content) {
		t.Fatal("stamp was not a value copy")
	}
	encoded, err := json.Marshal(source) //nolint:staticcheck // SA9005: proves Source marshals to {} and leaks nothing
	if err != nil || string(encoded) != "{}" {
		t.Fatal("source exposed its original bytes, stamp or path")
	}
	document, err := source.Parse(ctx)
	if err != nil || document.Metadata.Title != "電影" {
		t.Fatal("stable source did not parse")
	}
	document.Metadata.Title = "caller edit"
	document.Entries[0].Genres[0] = "caller edit"
	if err := document.WriteOriginal(ctx, mutatingSourceWriter{}); err != nil {
		t.Fatal(err)
	}
	var original bytes.Buffer
	if err := document.WriteOriginal(ctx, &original); err != nil || !bytes.Equal(original.Bytes(), content) {
		t.Fatal("caller writer modified the retained original")
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			parsed, err := source.Parse(ctx)
			if err != nil || parsed.Metadata.Title != "電影" || parsed.Metadata.Genres[0] != "Drama" {
				t.Error("parallel parse shared a mutable metadata view")
				return
			}
			parsed.Metadata.Genres[0] = "private edit"
			if err := parsed.WriteOriginal(ctx, mutatingSourceWriter{}); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	parsed, err := ReadFile(ctx, root, name, int64(len(content)))
	if err != nil || parsed.Metadata.Title != "電影" || source.Stamp().SHA256 != sourceDigest(content) {
		t.Fatal("ReadFile did not retain source semantics")
	}
	after, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || !bytes.Equal(after, content) {
		t.Fatal("reading modified the original file")
	}
	if err := os.Rename(root, root+".closed"); err != nil {
		t.Fatal("completed source retained an open root handle", err)
	}
	parsed, err = source.Parse(ctx)
	if err != nil || parsed.Metadata.Title != "電影" {
		t.Fatal("source depended on its old pathname after returning")
	}
}

func TestSourceHashesUnparsedBytesAndEveryMiddleByte(t *testing.T) {
	ctx := context.Background()
	for _, data := range [][]byte{nil, []byte("not XML"), []byte("\xff\xfe<\x00x\x00")} {
		root, name := sourceFixture(t, data)
		source, err := ReadSource(ctx, root, name, max(1, int64(len(data))))
		if err != nil || source.Stamp().SHA256 != sourceDigest(data) {
			t.Fatal("hash-only read required XML validity")
		}
		if document, err := source.Parse(ctx); document != nil || err == nil {
			t.Fatal("invalid XML became a parsed document")
		}
	}
	data := []byte(`<movie><plot>` + strings.Repeat("a", 128<<10) + `</plot></movie>`)
	root, name := sourceFixture(t, data)
	first, err := ReadSource(ctx, root, name, MaxAllowedBytes)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] = 'b'
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, name), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	second, err := ReadSource(ctx, root, name, MaxAllowedBytes)
	if err != nil || second.Stamp().Size != first.Stamp().Size || second.Stamp().ModifiedUnixNano != first.Stamp().ModifiedUnixNano || second.Stamp().SHA256 == first.Stamp().SHA256 || second.Stamp().SHA256 != sourceDigest(data) {
		t.Fatal("full hash missed a middle change with unchanged size/mtime")
	}
}

// File wrappers only belong to the current invocation, so tests can synchronize
// real filesystem changes without global hooks or scheduling sleeps.
type sourceTestRoot struct {
	sourceRoot
	wrap     func(sourceFile) sourceFile
	closeErr error
}

func (r sourceTestRoot) OpenFile(path string, flags int, mode os.FileMode) (sourceFile, error) {
	file, err := r.sourceRoot.OpenFile(path, flags, mode)
	if err == nil && r.wrap != nil {
		file = r.wrap(file)
	}
	return file, err
}

func (r sourceTestRoot) Close() error {
	err := r.sourceRoot.Close()
	if r.closeErr != nil {
		return r.closeErr
	}
	return err
}

type sourceTestFile struct {
	sourceFile
	afterRead func()
	once      sync.Once
	closeErr  error
}

func (f *sourceTestFile) Read(data []byte) (int, error) {
	n, err := f.sourceFile.Read(data)
	if n > 0 && f.afterRead != nil {
		f.once.Do(f.afterRead)
	}
	return n, err
}

func (f *sourceTestFile) Close() error {
	err := f.sourceFile.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return err
}

func sourceTestAccess(wrap func(sourceRoot, int) sourceRoot) sourceAccess {
	number := 0
	return sourceAccess{statRoot: os.Stat, openRoot: func(path string) (sourceRoot, error) {
		root, err := os.OpenRoot(path)
		if err != nil {
			return nil, err
		}
		number++
		return wrap(diskSourceRoot{root}, number), nil
	}}
}

func TestSourceDetectsActualChangesDuringRead(t *testing.T) {
	for _, change := range []string{"rewrite", "truncate", "append", "replace", "delete", "root-replace-same-file"} {
		t.Run(change, func(t *testing.T) {
			content := []byte(`<movie><plot>` + strings.Repeat("a", 2048) + `</plot></movie>`)
			root, name := sourceFixture(t, content)
			path := filepath.Join(root, name)
			rootRenameDenied := false
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			mutate := func() {
				var err error
				switch change {
				case "rewrite":
					copy(content, "changed")
					err = os.WriteFile(path, content, 0o600)
					if err == nil {
						err = os.Chtimes(path, before.ModTime(), before.ModTime().Add(2*time.Second))
					}
				case "truncate":
					err = os.Truncate(path, 1)
				case "append":
					err = os.WriteFile(path, append(content, '!'), 0o600)
				case "replace":
					err = os.Rename(path, path+".old")
					if err == nil {
						err = os.WriteFile(path, content, 0o600)
					}
					if err == nil {
						err = os.Chtimes(path, before.ModTime(), before.ModTime())
					}
				case "delete":
					err = os.Remove(path)
				case "root-replace-same-file":
					err = os.Rename(root, root+".old")
					if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(32)) {
						// Windows denies directory replacement while os.Root owns
						// its handle. Verify the original observation below.
						rootRenameDenied = true
						err = nil
						break
					}
					if err == nil {
						err = os.Mkdir(root, 0o700)
					}
					if err == nil {
						err = os.Link(filepath.Join(root+".old", name), path)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			access := sourceTestAccess(func(root sourceRoot, number int) sourceRoot {
				if number != 1 {
					return root
				}
				return sourceTestRoot{sourceRoot: root, wrap: func(file sourceFile) sourceFile {
					return &sourceTestFile{sourceFile: file, afterRead: mutate}
				}}
			})
			source, err := readSource(context.Background(), root, name, int64(len(content)), access)
			if rootRenameDenied {
				if err != nil || source.Stamp().SHA256 != sourceDigest(content) {
					t.Fatal("OS-protected root observation changed")
				}
				t.Log("Windows denied replacement of the held root directory; original bytes verified")
				return
			}
			if source != nil || !errors.Is(err, ErrChanged) || err.Error() != "nfo_changed" {
				t.Fatalf("observed change returned %v, source=%t", err, source != nil)
			}
		})
	}
}

func TestSourceChecksReopenedRootIdentityOnEveryPlatform(t *testing.T) {
	content := []byte("<movie/>")
	root, name := sourceFixture(t, content)
	other := t.TempDir()
	if err := os.Link(filepath.Join(root, name), filepath.Join(other, name)); err != nil {
		t.Fatal(err)
	}
	opened := 0
	access := sourceAccess{statRoot: os.Stat, openRoot: func(path string) (sourceRoot, error) {
		opened++
		if opened == 2 {
			path = other
		}
		root, err := os.OpenRoot(path)
		if err != nil {
			return nil, err
		}
		return diskSourceRoot{root}, nil
	}}
	if source, err := readSource(context.Background(), root, name, 8, access); source != nil || err != ErrChanged {
		t.Fatal("a different reopened root with the same file identity was accepted")
	}
}

func TestSourceBoundsInvalidPathsAndZeroValue(t *testing.T) {
	ctx := context.Background()
	root, name := sourceFixture(t, []byte("<movie/>"))
	for _, path := range []string{".", "../outside.nfo", "/absolute.nfo", "a//b.nfo", "a/../b.nfo", "a\\b.nfo", "C:bad.nfo", "a\nb.nfo", "a\tb.nfo", "a\x7fb.nfo", "a\x00b.nfo", "\xff.nfo"} {
		if source, err := ReadSource(ctx, root, path, DefaultMaxBytes); source != nil || !errors.Is(err, ErrNotFound) {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
	for _, maximum := range []int64{0, -1, MaxAllowedBytes + 1} {
		if source, err := ReadSource(ctx, root, name, maximum); source != nil || !errors.Is(err, ErrInvalidInput) {
			t.Fatal("invalid byte limit accepted")
		}
	}
	if source, err := ReadSource(ctx, root, name, 7); source != nil || !errors.Is(err, ErrTooLarge) {
		t.Fatal("oversized source read")
	}
	if source, err := ReadSource(ctx, root, name, 8); err != nil || source.Stamp().Size != 8 {
		t.Fatal("exact byte limit refused")
	}
	if source, err := ReadSource(nil, root, name, 8); source != nil || !errors.Is(err, ErrInvalidInput) {
		t.Fatal("nil context accepted")
	}
	if source, err := ReadSource(ctx, "relative-root", name, 8); source != nil || err != ErrNotFound {
		t.Fatal("relative root accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "directory.nfo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if source, err := ReadSource(ctx, root, "directory.nfo", 8); source != nil || err != ErrNotFound {
		t.Fatal("directory accepted as source")
	}
	for _, source := range []*Source{nil, {}} {
		if source.Stamp() != (SourceStamp{}) {
			t.Fatal("zero source returned a usable stamp")
		}
		if document, err := source.Parse(ctx); document != nil || !errors.Is(err, ErrInvalidInput) {
			t.Fatal("zero source parsed")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if source, err := ReadSource(cancelled, root, name, 0); source != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation did not win over invalid bounds")
	}
	source, err := ReadSource(ctx, root, name, 8)
	if err != nil {
		t.Fatal(err)
	}
	if document, err := source.Parse(nil); document != nil || err != ErrInvalidInput {
		t.Fatal("nil parse context accepted")
	}
	if document, err := source.Parse(cancelled); document != nil || err != context.Canceled {
		t.Fatal("cancelled parse returned a document")
	}
}

type failedSourceRead struct{ sourceFile }

func (f failedSourceRead) Read([]byte) (int, error) { return 0, errors.New("private IO path") }

type unrepresentableSourceTime struct{ os.FileInfo }

func (info unrepresentableSourceTime) ModTime() time.Time {
	return time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)
}

type invalidSourceStat struct{ sourceFile }

func (file invalidSourceStat) Stat() (os.FileInfo, error) {
	info, err := file.sourceFile.Stat()
	return unrepresentableSourceTime{info}, err
}

func TestSourceReadErrorsAndUnrepresentableTimestamp(t *testing.T) {
	for _, kind := range []string{"read-error", "timestamp"} {
		t.Run(kind, func(t *testing.T) {
			root, name := sourceFixture(t, []byte("<movie/>"))
			access := sourceTestAccess(func(root sourceRoot, _ int) sourceRoot {
				return sourceTestRoot{sourceRoot: root, wrap: func(file sourceFile) sourceFile {
					if kind == "read-error" {
						return failedSourceRead{file}
					}
					return invalidSourceStat{file}
				}}
			})
			want := ErrRead
			if kind == "timestamp" {
				want = ErrNotFound
			}
			if source, err := readSource(context.Background(), root, name, 8, access); source != nil || err != want {
				t.Fatal("source returned a partial result or unsafe error")
			}
		})
	}
}

func TestSourceCloseFailuresDiscardResultAndRemainSafe(t *testing.T) {
	for _, target := range []string{"initial-root", "initial-file", "reopened-root", "reopened-file"} {
		t.Run(target, func(t *testing.T) {
			root, name := sourceFixture(t, []byte("<movie/>"))
			access := sourceTestAccess(func(root sourceRoot, number int) sourceRoot {
				wrapped := sourceTestRoot{sourceRoot: root}
				if target == "initial-root" && number == 1 || target == "reopened-root" && number == 2 {
					wrapped.closeErr = errors.New("private OS path error")
				}
				if target == "initial-file" && number == 1 || target == "reopened-file" && number == 2 {
					wrapped.wrap = func(file sourceFile) sourceFile {
						return &sourceTestFile{sourceFile: file, closeErr: errors.New("private OS path error")}
					}
				}
				return wrapped
			})
			if source, err := readSource(context.Background(), root, name, 8, access); source != nil || err != ErrRead {
				t.Fatalf("close failure returned %v and source=%t", err, source != nil)
			}
		})
	}
}

type blockedSourceFile struct {
	sourceFile
	reading, closing, unblock chan struct{}
	closeCount                atomic.Int32
}

func (f *blockedSourceFile) Read([]byte) (int, error) {
	close(f.reading)
	<-f.closing
	return 0, io.ErrClosedPipe
}

func (f *blockedSourceFile) Close() error {
	f.closeCount.Add(1)
	close(f.closing)
	<-f.unblock
	_ = f.sourceFile.Close()
	return errors.New("private close failure")
}

func TestSourceCancellationClosesReadingFileAndJoinsCallback(t *testing.T) {
	root, name := sourceFixture(t, []byte("<movie/>"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	file := &blockedSourceFile{reading: make(chan struct{}), closing: make(chan struct{}), unblock: make(chan struct{})}
	access := sourceTestAccess(func(root sourceRoot, number int) sourceRoot {
		return sourceTestRoot{sourceRoot: root, wrap: func(opened sourceFile) sourceFile {
			file.sourceFile = opened
			return file
		}}
	})
	finished := make(chan error, 1)
	go func() {
		source, err := readSource(ctx, root, name, 8, access)
		if source != nil {
			finished <- errors.New("cancelled read returned source")
			return
		}
		finished <- err
	}()
	select {
	case <-file.reading:
	case <-time.After(5 * time.Second):
		t.Fatal("read did not start")
	}
	cancel()
	select {
	case <-file.closing:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not close owned file")
	}
	select {
	case <-finished:
		t.Fatal("reader returned before cancellation callback joined")
	default:
	}
	close(file.unblock)
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) || file.closeCount.Load() != 1 {
			t.Fatalf("cancellation lost priority or double close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read did not join after callback completed")
	}
}

func BenchmarkSourceHashAndParse(b *testing.B) {
	content := []byte(`<movie><title>Benchmark</title><plot>` + strings.Repeat("a", 16<<10) + `</plot></movie>`)
	root, name := sourceFixture(b, content)
	ctx := context.Background()
	b.Run("ReadSource", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(content)))
		for b.Loop() {
			if _, err := ReadSource(ctx, root, name, DefaultMaxBytes); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ReadFile", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(content)))
		for b.Loop() {
			if _, err := ReadFile(ctx, root, name, DefaultMaxBytes); err != nil {
				b.Fatal(err)
			}
		}
	})
}
