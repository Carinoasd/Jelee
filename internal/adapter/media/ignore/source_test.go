package ignoresource

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

type sourceCounts struct{ roots, dirs, files, fullReads, readBytes, compiles, activeDirs, activeFiles atomic.Int64 }
type diskSourceHarness struct {
	counts       sourceCounts
	afterRead    func(string, int)
	beforeRule   func(string, int) error
	wrapFile     func(string, int, sourceFile) sourceFile
	closeFailure string
}

func (h *diskSourceHarness) access() sourceAccess {
	return sourceAccess{openRoot: func(path string) (directory, error) {
		d, err := openNativeRoot(path)
		if err != nil {
			return nil, err
		}
		pass := int(h.counts.roots.Add(1))
		h.counts.activeDirs.Add(1)
		return &countedDirectory{directory: d, h: h, pass: pass}, nil
	}, compile: func(ctx context.Context, s []ignore.Source, o ignore.Options) (*ignore.Program, error) {
		h.counts.compiles.Add(1)
		return ignore.Compile(ctx, s, o)
	}}
}

type countedDirectory struct {
	directory
	h        *diskSourceHarness
	path     string
	pass     int
	once     sync.Once
	closeErr error
}

func (d *countedDirectory) OpenDirectory(name string) (directory, error) {
	opened, err := d.directory.OpenDirectory(name)
	if err != nil {
		return nil, err
	}
	d.h.counts.dirs.Add(1)
	d.h.counts.activeDirs.Add(1)
	path := name
	if d.path != "" {
		path = d.path + "/" + name
	}
	return &countedDirectory{directory: opened, h: d.h, path: path, pass: d.pass}, nil
}
func (d *countedDirectory) OpenRule() (sourceFile, error) {
	name := ".jeleeignore"
	if d.path != "" {
		name = d.path + "/" + name
	}
	if d.h.beforeRule != nil {
		if err := d.h.beforeRule(name, d.pass); err != nil {
			return nil, err
		}
	}
	opened, err := d.directory.OpenRule()
	if err != nil {
		return nil, err
	}
	d.h.counts.files.Add(1)
	d.h.counts.activeFiles.Add(1)
	var f sourceFile = &countedSourceFile{sourceFile: opened, h: d.h, path: name, pass: d.pass}
	if d.h.wrapFile != nil {
		f = d.h.wrapFile(name, d.pass, f)
	}
	return f, nil
}
func (d *countedDirectory) Close() error {
	d.once.Do(func() {
		d.closeErr = d.directory.Close()
		d.h.counts.activeDirs.Add(-1)
		if d.h.closeFailure == "directory" {
			d.closeErr = errors.New("private injected directory close")
		}
	})
	return d.closeErr
}

type countedSourceFile struct {
	sourceFile
	h                            *diskSourceHarness
	path                         string
	pass                         int
	readOnce, eofOnce, closeOnce sync.Once
	closeErr                     error
}

func (f *countedSourceFile) Read(p []byte) (int, error) {
	n, err := f.sourceFile.Read(p)
	f.h.counts.readBytes.Add(int64(n))
	if n > 0 && f.h.afterRead != nil {
		f.readOnce.Do(func() { f.h.afterRead(f.path, f.pass) })
	}
	if err == io.EOF {
		f.eofOnce.Do(func() { f.h.counts.fullReads.Add(1) })
	}
	return n, err
}
func (f *countedSourceFile) Close() error {
	f.closeOnce.Do(func() {
		f.closeErr = f.sourceFile.Close()
		f.h.counts.activeFiles.Add(-1)
		if f.h.closeFailure == "file" {
			f.closeErr = errors.New("private injected file close")
		}
	})
	return f.closeErr
}
func (h *diskSourceHarness) closed(t *testing.T) {
	t.Helper()
	if h.counts.activeDirs.Load() != 0 || h.counts.activeFiles.Load() != 0 {
		t.Fatal("source call retained owned native handles")
	}
}
func writeRule(t *testing.T, root, relative, text string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func sourceFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "library")
	parent, child := "# root\n*.tmp\n", "!keep.tmp\n"
	writeRule(t, root, ".jeleeignore", parent)
	writeRule(t, root, "dir/.jeleeignore", child)
	return root, parent, child
}
func zeroObservation(t *testing.T, o Observation, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || !reflect.DeepEqual(o, Observation{}) {
		t.Fatalf("source failure must return fixed error and zero observation: got error type %T", err)
	}
}
func evaluateDisk(t *testing.T, r *Resolver, h *diskSourceHarness, root, candidate string) Observation {
	t.Helper()
	o, err := r.evaluate(context.Background(), root, candidate, ignore.File, ignore.Options{}, h.access())
	if err != nil {
		t.Fatalf("safe disk source rejected: %v", err)
	}
	h.closed(t)
	return o
}

func TestSourceColdWarmReadsAndSameMetadataMutation(t *testing.T) {
	root, parent, child := sourceFixture(t)
	// Keep identical prefix and suffix, so a prefix-only content sample cannot
	// satisfy the mutation contract. The changed rule lies in the middle.
	padding := "#" + strings.Repeat("x", 1023) + "\n"
	child = padding + child + padding
	writeRule(t, root, "dir/.jeleeignore", child)
	r := NewResolver()
	cold := &diskSourceHarness{}
	first := evaluateDisk(t, r, cold, root, "dir/keep.tmp")
	if first.Match.Outcome != ignore.Include || first.Match.Source != "dir/.jeleeignore" || cold.counts.fullReads.Load() != 4 || cold.counts.compiles.Load() != 2 || cold.counts.readBytes.Load() != 2*int64(len(parent)+len(child)) {
		t.Fatal("cold load did not read both passes and compile both prefixes")
	}
	warm := &diskSourceHarness{}
	second := evaluateDisk(t, r, warm, root, "dir/keep.tmp")
	if !reflect.DeepEqual(first, second) || warm.counts.fullReads.Load() != 4 || warm.counts.compiles.Load() != 0 || warm.counts.readBytes.Load() != cold.counts.readBytes.Load() {
		t.Fatal("warm cache skipped full source observation or recompiled unchanged prefixes")
	}
	for relative, original := range map[string]string{".jeleeignore": parent, "dir/.jeleeignore": child} {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil || !bytes.Equal(got, []byte(original)) {
			t.Fatal("cold/warm observation modified original source")
		}
	}
	file := filepath.Join(root, "dir", ".jeleeignore")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	replacement := padding + "keep.tmp \n" + padding
	if len(replacement) != len(child) {
		t.Fatal("invalid equal-size fixture")
	}
	writeRule(t, root, "dir/.jeleeignore", replacement)
	if err = os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(file)
	if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("fixture did not preserve size/mtime")
	}
	changed := &diskSourceHarness{}
	third := evaluateDisk(t, r, changed, root, "dir/keep.tmp")
	if third.Match.Outcome != ignore.Exclude || third.Token() == first.Token() || changed.counts.fullReads.Load() != 4 || changed.counts.compiles.Load() != 1 {
		t.Fatal("equal-metadata changed bytes reused old compiled rule")
	}
	for name, h := range map[string]*diskSourceHarness{"cold": cold, "warm": warm, "middle-change": changed} {
		t.Logf("source_io state=%s compiles=%d fullReads=%d readBytes=%d activeDirs=%d activeFiles=%d", name, h.counts.compiles.Load(), h.counts.fullReads.Load(), h.counts.readBytes.Load(), h.counts.activeDirs.Load(), h.counts.activeFiles.Load())
	}
	got, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(got, []byte(replacement)) {
		t.Fatal("readonly observer modified original source")
	}
	if err = os.Rename(root, root+"-closed"); err != nil {
		t.Fatal("successful source kept root handle open")
	}
}
func TestSourceAbsenceCreationDeletionAndPruning(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, "dir/.jeleeignore", "!keep.tmp\n")
	r := NewResolver()
	first := evaluateDisk(t, r, &diskSourceHarness{}, root, "dir/keep.tmp")
	if first.Match.Outcome != ignore.Include {
		t.Fatal("child source missing")
	}
	writeRule(t, root, ".jeleeignore", "dir/\n")
	pruned := &diskSourceHarness{beforeRule: func(name string, _ int) error {
		if name != ".jeleeignore" {
			t.Error("pruned source directory was visited")
		}
		return nil
	}}
	second := evaluateDisk(t, r, pruned, root, "dir/keep.tmp")
	if !second.Match.ParentBlocked || second.Match.MatchedPath != "dir" || second.Token() == first.Token() || pruned.counts.dirs.Load() != 0 || pruned.counts.fullReads.Load() != 2 {
		t.Fatal("new formerly absent ancestor did not prune before deeper IO")
	}
	if err := os.Remove(filepath.Join(root, ".jeleeignore")); err != nil {
		t.Fatal(err)
	}
	third := evaluateDisk(t, r, &diskSourceHarness{}, root, "dir/keep.tmp")
	if third.Match != first.Match {
		t.Fatal("deleted ancestor rule remained cached")
	}
	if err := os.Remove(filepath.Join(root, "dir", ".jeleeignore")); err != nil {
		t.Fatal(err)
	}
	fourth := evaluateDisk(t, r, &diskSourceHarness{}, root, "dir/keep.tmp")
	if fourth.Match != (ignore.Match{}) || fourth.Token() == third.Token() {
		t.Fatal("deleted child rule remained cached")
	}
	writeRule(t, root, ".jeleeignore", "dir/\n")
	if err := os.Remove(filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	if o := evaluateDisk(t, r, &diskSourceHarness{}, root, "dir/missing/deep.tmp"); !o.Match.ParentBlocked {
		t.Fatal("blocked descendant required nonexistent directories")
	}
}
func TestSourceFinalPassDetectsNewAbsentRule(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, "dir/.jeleeignore", "!keep.tmp\n")
	r := NewResolver()
	h := &diskSourceHarness{}
	h.beforeRule = func(name string, pass int) error {
		if name == ".jeleeignore" && pass == 2 {
			writeRule(t, root, ".jeleeignore", "dir/\n")
		}
		return nil
	}
	o, err := r.evaluate(context.Background(), root, "dir/keep.tmp", ignore.File, ignore.Options{}, h.access())
	zeroObservation(t, o, err, ErrChanged)
	h.closed(t)
	if next := evaluateDisk(t, r, &diskSourceHarness{}, root, "dir/keep.tmp"); !next.Match.ParentBlocked {
		t.Fatal("failed observation poisoned next load")
	}
}

func TestSourceDirectoryCandidateDoesNotReadItsOwnControlFile(t *testing.T) {
	root, _, _ := sourceFixture(t)
	writeRule(t, root, "dir/.jeleeignore", string([]byte{0xff, 0xfe, 0}))
	r := NewResolver()
	h := &diskSourceHarness{}
	o, err := r.evaluate(context.Background(), root, "dir", ignore.Directory, ignore.Options{}, h.access())
	if err != nil || o.Match != (ignore.Match{}) || h.counts.dirs.Load() != 0 || h.counts.fullReads.Load() != 2 {
		t.Fatal("directory candidate opened its own unreachable control file")
	}
	h.closed(t)
	failure := &diskSourceHarness{}
	o, err = r.evaluate(context.Background(), root, "dir/keep.tmp", ignore.File, ignore.Options{}, failure.access())
	zeroObservation(t, o, err, ErrInvalid)
	failure.closed(t)
}

func TestSourceRealFileBoundsAndNonregularLeaf(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	line := "#" + strings.Repeat("x", 1022) + "\n"
	text := strings.Repeat(line, ignore.MaxSourceBytes/len(line))
	if len(text) != ignore.MaxSourceBytes {
		t.Fatal("invalid limit fixture")
	}
	writeRule(t, root, ".jeleeignore", text)
	r := NewResolver()
	h := &diskSourceHarness{}
	if o := evaluateDisk(t, r, h, root, "file.tmp"); o.Match != (ignore.Match{}) || h.counts.readBytes.Load() != 2*ignore.MaxSourceBytes {
		t.Fatal("exactly bounded file not fully read")
	}
	writeRule(t, root, ".jeleeignore", text+"x")
	o, err := r.Evaluate(context.Background(), root, "file.tmp", ignore.File, ignore.Options{})
	zeroObservation(t, o, err, ErrLimit)
	if err := os.Remove(filepath.Join(root, ".jeleeignore")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".jeleeignore"), 0700); err != nil {
		t.Fatal(err)
	}
	o, err = r.Evaluate(context.Background(), root, "file.tmp", ignore.File, ignore.Options{})
	zeroObservation(t, o, err, ErrUnsafe)
}

func TestSourceReopenedRootIdentityWithSameHardlinkedFile(t *testing.T) {
	base := filepath.Clean(t.TempDir())
	root := filepath.Join(base, "first")
	other := filepath.Join(base, "second")
	writeRule(t, root, ".jeleeignore", "*.tmp\n")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, ".jeleeignore"), filepath.Join(other, ".jeleeignore")); err != nil {
		t.Fatal("actual hardlink fixture failed")
	}
	a, _ := os.Stat(filepath.Join(root, ".jeleeignore"))
	b, _ := os.Stat(filepath.Join(other, ".jeleeignore"))
	if a == nil || b == nil || !os.SameFile(a, b) {
		t.Fatal("fixture must use identical source file identity")
	}
	h := &diskSourceHarness{}
	access := h.access()
	nativeOpen := access.openRoot
	calls := 0
	access.openRoot = func(path string) (directory, error) {
		calls++
		if calls == 2 {
			path = other
		}
		return nativeOpen(path)
	}
	o, err := NewResolver().evaluate(context.Background(), root, "file.tmp", ignore.File, ignore.Options{}, access)
	zeroObservation(t, o, err, ErrChanged)
	h.closed(t)
	if calls != 2 || h.counts.fullReads.Load() != 1 {
		t.Fatal("reopened root identity not checked before reading identical source again")
	}
}

func TestSourceRealReplacementsDuringRead(t *testing.T) {
	for _, change := range []string{"rewrite", "truncate", "append", "leaf-replace", "leaf-delete", "directory-replace", "root-replace"} {
		t.Run(change, func(t *testing.T) {
			root, _, child := sourceFixture(t)
			r := NewResolver()
			evaluateDisk(t, r, &diskSourceHarness{}, root, "dir/keep.tmp")
			file := filepath.Join(root, "dir", ".jeleeignore")
			before, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			mutated, protected := false, false
			h := &diskSourceHarness{}
			h.afterRead = func(name string, pass int) {
				if pass != 1 || name != "dir/.jeleeignore" || mutated || protected {
					return
				}
				var e error
				switch change {
				case "rewrite":
					e = os.WriteFile(file, []byte("keep.tmp \n"), 0600)
					if e == nil {
						mutated = true
						e = os.Chtimes(file, before.ModTime(), before.ModTime().Add(2*time.Second))
					}
				case "truncate":
					e = os.Truncate(file, 1)
				case "append":
					f, x := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
					e = x
					if e == nil {
						_, e = f.WriteString("# added\n")
						mutated = true
						f.Close()
					}
				case "leaf-replace":
					e = os.Rename(file, file+".old")
					if e == nil {
						mutated = true
						writeRule(t, root, "dir/.jeleeignore", child)
						e = os.Chtimes(file, before.ModTime(), before.ModTime())
					}
				case "leaf-delete":
					e = os.Remove(file)
				case "directory-replace":
					dir := filepath.Join(root, "dir")
					e = os.Rename(dir, dir+".old")
					if e == nil {
						writeRule(t, root, "dir/.jeleeignore", child)
					}
				case "root-replace":
					e = os.Rename(root, root+".old")
					if e == nil {
						writeRule(t, root, ".jeleeignore", "# root\n*.tmp\n")
						writeRule(t, root, "dir/.jeleeignore", child)
					}
				}
				if e != nil && !mutated && runtime.GOOS == "windows" && (errors.Is(e, syscall.Errno(32)) || errors.Is(e, os.ErrPermission)) {
					protected = true
					t.Log("Windows denied mutation while native handle was held")
					return
				}
				if e != nil {
					t.Fatal("fixture mutation failed")
				}
				mutated = true
			}
			o, err := r.evaluate(context.Background(), root, "dir/keep.tmp", ignore.File, ignore.Options{}, h.access())
			h.closed(t)
			if protected {
				if err != nil || o.Match.Outcome != ignore.Include {
					t.Fatal("protected unchanged source was not retained")
				}
				return
			}
			if !mutated {
				t.Fatal("mutation barrier was not reached")
			}
			zeroObservation(t, o, err, ErrChanged)
		})
	}
}

type injectedReadFailure struct{ sourceFile }

func (injectedReadFailure) Read([]byte) (int, error) {
	return 0, errors.New("private source content and host path must not escape")
}
func TestSourceWarmCacheDoesNotHideReadOrCloseFailures(t *testing.T) {
	for _, fault := range []string{"read", "file-close", "directory-close", "permission"} {
		t.Run(fault, func(t *testing.T) {
			root, _, _ := sourceFixture(t)
			r := NewResolver()
			evaluateDisk(t, r, &diskSourceHarness{}, root, "dir/keep.tmp")
			h := &diskSourceHarness{}
			switch fault {
			case "read":
				h.wrapFile = func(_ string, _ int, f sourceFile) sourceFile { return injectedReadFailure{f} }
			case "file-close":
				h.closeFailure = "file"
			case "directory-close":
				h.closeFailure = "directory"
			case "permission":
				h.beforeRule = func(string, int) error { return ErrRead }
			}
			o, err := r.evaluate(context.Background(), root, "dir/keep.tmp", ignore.File, ignore.Options{}, h.access())
			zeroObservation(t, o, err, ErrRead)
			h.closed(t)
			if err == nil || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "private source") {
				t.Fatal("raw source error leaked")
			}
			if recovered := evaluateDisk(t, r, &diskSourceHarness{}, root, "dir/keep.tmp"); recovered.Match.Outcome != ignore.Include {
				t.Fatal("failed warm observation poisoned valid cache")
			}
		})
	}
}

type blockedSourceFile struct {
	sourceFile
	reading, closing, unblock chan struct{}
	readOnce, closeOnce       sync.Once
	closeCalls                atomic.Int64
	closeErr                  error
}

func (f *blockedSourceFile) Read([]byte) (int, error) {
	f.readOnce.Do(func() { close(f.reading) })
	<-f.closing
	return 0, io.ErrClosedPipe
}
func (f *blockedSourceFile) Close() error {
	f.closeCalls.Add(1)
	f.closeOnce.Do(func() { f.closeErr = f.sourceFile.Close(); close(f.closing); <-f.unblock })
	return f.closeErr
}
func TestSourceCancellationClosesActualFileAndJoins(t *testing.T) {
	root, _, _ := sourceFixture(t)
	r := NewResolver()
	h := &diskSourceHarness{}
	blocked := &blockedSourceFile{reading: make(chan struct{}), closing: make(chan struct{}), unblock: make(chan struct{})}
	h.wrapFile = func(_ string, pass int, f sourceFile) sourceFile {
		if pass == 1 {
			blocked.sourceFile = f
			return blocked
		}
		return f
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	exited := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() {
		cancel()
		release.Do(func() { close(blocked.unblock) })
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("source goroutine remained after cancellation cleanup")
		}
	})
	go func() {
		defer close(exited)
		o, err := r.evaluate(ctx, root, "dir/keep.tmp", ignore.File, ignore.Options{}, h.access())
		if !reflect.DeepEqual(o, Observation{}) {
			done <- errors.New("cancelled read returned observation")
			return
		}
		done <- err
	}()
	select {
	case <-blocked.reading:
	case <-time.After(5 * time.Second):
		t.Fatal("actual source read did not begin")
	}
	cancel()
	select {
	case <-blocked.closing:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not close actual file")
	}
	select {
	case <-done:
		t.Fatal("returned before close callback joined")
	default:
	}
	release.Do(func() { close(blocked.unblock) })
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || blocked.closeCalls.Load() != 1 {
			t.Fatal("cancel priority or exactly-once close failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("source cancellation did not join")
	}
	h.closed(t)
	if next := evaluateDisk(t, r, &diskSourceHarness{}, root, "dir/keep.tmp"); next.Match.Outcome != ignore.Include {
		t.Fatal("cancelled call stranded admission/resource")
	}
}
