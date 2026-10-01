package ignoresource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

// This fixture tests orchestration and cache ownership only. It is not evidence
// that an OS rejects symlinks, preserves handle identity, or cancels native I/O.
type resolverFixture struct {
	mu         sync.Mutex
	root       string
	rootID     fileIdentity
	dirs       map[string]bool
	rules      map[string][]byte
	openError  error
	ruleError  error
	readError  error
	closeError error
	compiles   atomic.Int64
	readBytes  atomic.Int64
	rootOpens  atomic.Int64
	fileOpens  atomic.Int64
	fileCloses atomic.Int64
	dirOpens   atomic.Int64
	dirCloses  atomic.Int64
}

func newResolverFixture(t testing.TB) *resolverFixture {
	t.Helper()
	return &resolverFixture{root: filepath.Join(t.TempDir(), "root"), rootID: fileIdentity{1}, dirs: map[string]bool{".": true}, rules: map[string][]byte{}}
}

func (f *resolverFixture) addDir(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(name, "/")
	for i := range parts {
		f.dirs[strings.Join(parts[:i+1], "/")] = true
	}
}

func (f *resolverFixture) setRule(dir, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules[dir] = []byte(text)
}

func (f *resolverFixture) access() sourceAccess {
	return sourceAccess{
		openRoot: func(string) (directory, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.rootOpens.Add(1)
			if f.openError != nil {
				return nil, f.openError
			}
			f.dirOpens.Add(1)
			return &resolverTestDir{fixture: f, path: ".", identity: f.rootID}, nil
		},
		compile: func(ctx context.Context, sources []ignore.Source, opts ignore.Options) (*ignore.Program, error) {
			f.compiles.Add(1)
			return ignore.Compile(ctx, sources, opts)
		},
	}
}

type resolverTestDir struct {
	fixture  *resolverFixture
	path     string
	identity fileIdentity
	once     sync.Once
}

func (d *resolverTestDir) Stat() (fileState, error) {
	return fileState{identity: d.identity, kind: nodeDirectory}, nil
}

func (d *resolverTestDir) OpenDirectory(child string) (directory, error) {
	f := d.fixture
	f.mu.Lock()
	defer f.mu.Unlock()
	path := child
	if d.path != "." {
		path = d.path + "/" + child
	}
	if !f.dirs[path] {
		return nil, ErrUnavailable
	}
	f.dirOpens.Add(1)
	id := sha256.Sum256(append(append([]byte(nil), f.rootID[:]...), []byte(path)...))
	return &resolverTestDir{fixture: f, path: path, identity: fileIdentity(id)}, nil
}

func (d *resolverTestDir) OpenRule() (sourceFile, error) {
	f := d.fixture
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ruleError != nil {
		return nil, f.ruleError
	}
	text, ok := f.rules[d.path]
	if !ok {
		return nil, errAbsent
	}
	f.fileOpens.Add(1)
	id := sha256.Sum256(append(append([]byte(nil), f.rootID[:]...), []byte(d.path+"/.jeleeignore")...))
	return &resolverTestFile{fixture: f, reader: bytes.NewReader(append([]byte(nil), text...)), state: fileState{identity: fileIdentity(id), size: int64(len(text)), modifiedUnixNano: 1000, kind: nodeRegular}, readError: f.readError, closeError: f.closeError}, nil
}

func (d *resolverTestDir) Close() error {
	d.once.Do(func() { d.fixture.dirCloses.Add(1) })
	return nil
}

type resolverTestFile struct {
	fixture               *resolverFixture
	reader                *bytes.Reader
	state                 fileState
	readError, closeError error
	once                  sync.Once
}

func (f *resolverTestFile) Read(p []byte) (int, error) {
	if f.readError != nil {
		return 0, f.readError
	}
	n, err := f.reader.Read(p)
	f.fixture.readBytes.Add(int64(n))
	return n, err
}
func (f *resolverTestFile) Stat() (fileState, error) { return f.state, nil }
func (f *resolverTestFile) Close() error {
	f.once.Do(func() { f.fixture.fileCloses.Add(1) })
	return f.closeError
}

func resolverRequireZero(t testing.TB, got Observation, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || !reflect.DeepEqual(got, Observation{}) {
		t.Fatalf("expected fixed error and zero observation: %v", err)
	}
}

func resolverEvaluate(t testing.TB, r *Resolver, f *resolverFixture, candidate string, kind ignore.Kind, opts ignore.Options) Observation {
	t.Helper()
	got, err := r.evaluate(context.Background(), f.root, candidate, kind, opts, f.access())
	if err != nil {
		t.Fatal(err)
	}
	if got.Token() == ([32]byte{}) {
		t.Fatal("successful observation has no identity")
	}
	return got
}

func TestResolverInvalidInputsReturnZeroBeforeOpeningAnything(t *testing.T) {
	f := newResolverFixture(t)
	r := NewResolver()
	for _, candidate := range []string{"", ".", "..", "/absolute", "a/../b", "a/./b", "a//b", "a/", `a\b`, "C:file", "a\x00b", "a\nb", string([]byte{0xff})} {
		got, err := r.evaluate(context.Background(), f.root, candidate, ignore.File, ignore.Options{}, f.access())
		resolverRequireZero(t, got, err, ErrInvalid)
	}
	for _, root := range []string{"", ".", "relative", f.root + "\x00", f.root + "\n", string([]byte{0xff})} {
		got, err := r.evaluate(context.Background(), root, "file", ignore.File, ignore.Options{}, f.access())
		resolverRequireZero(t, got, err, ErrInvalid)
	}
	for _, kind := range []ignore.Kind{0, 255} {
		got, err := r.evaluate(context.Background(), f.root, "file", kind, ignore.Options{}, f.access())
		resolverRequireZero(t, got, err, ErrInvalid)
	}
	got, err := r.evaluate(context.Background(), f.root, "file", ignore.File, ignore.Options{Case: 255}, f.access())
	resolverRequireZero(t, got, err, ErrInvalid)
	got, err = r.evaluate(nil, f.root, "file", ignore.File, ignore.Options{}, f.access())
	resolverRequireZero(t, got, err, ErrInvalid)
	var nilResolver *Resolver
	got, err = nilResolver.evaluate(context.Background(), f.root, "file", ignore.File, ignore.Options{}, f.access())
	resolverRequireZero(t, got, err, ErrInvalid)
	got, err = (&Resolver{}).evaluate(context.Background(), f.root, "file", ignore.File, ignore.Options{}, f.access())
	resolverRequireZero(t, got, err, ErrInvalid)
	if f.rootOpens.Load() != 0 {
		t.Fatal("invalid contract input reached filesystem")
	}
}

func TestResolverCallerCancellationAndDeadlineHaveZeroResults(t *testing.T) {
	f := newResolverFixture(t)
	r := NewResolver()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, ctx := range []context.Context{cancelled, expired} {
		got, err := r.evaluate(ctx, f.root, "file", ignore.File, ignore.Options{}, f.access())
		resolverRequireZero(t, got, err, ctx.Err())
	}
	if f.rootOpens.Load() != 0 {
		t.Fatal("pre-cancelled observation opened a resource")
	}
}

func TestResolverPathContractBoundariesBeforeNativeAccess(t *testing.T) {
	// The fake filesystem isolates byte/component API limits from smaller
	// host filename and native pathname limits; it proves no OS capacity claim.
	f := newResolverFixture(t)
	f.root += string(filepath.Separator) + strings.Repeat("r", MaxRootBytes-len(f.root)-1)
	r := NewResolver()
	if got := resolverEvaluate(t, r, f, strings.Repeat("f", ignore.MaxPathBytes), ignore.File, ignore.Options{}); got.Match.Outcome != ignore.Unmatched {
		t.Fatal("exact byte boundaries changed an empty rule set")
	}
	before := f.rootOpens.Load()
	for _, tc := range []struct{ root, candidate string }{
		{f.root + "r", "file"},
		{f.root, strings.Repeat("f", ignore.MaxPathBytes+1)},
		{f.root, strings.Repeat("a/", ignore.MaxPathComponents) + "f"},
	} {
		got, err := r.evaluate(context.Background(), tc.root, tc.candidate, ignore.File, ignore.Options{}, f.access())
		resolverRequireZero(t, got, err, ErrLimit)
	}
	if f.rootOpens.Load() != before {
		t.Fatal("an over-limit path reached native access")
	}
}

func TestResolverCacheStillReadsAndReturnedDiagnosticsCannotMutateIt(t *testing.T) {
	f := newResolverFixture(t)
	f.setRule(".", "*.nfo\n\\\n")
	r := NewResolver()
	first := resolverEvaluate(t, r, f, "movie.nfo", ignore.File, ignore.Options{})
	if first.Match.Outcome != ignore.Exclude || first.Diagnostics.Total != 1 || len(first.Diagnostics.Items) != 1 {
		t.Fatal("invalid cold observation")
	}
	compiles, bytesBefore := f.compiles.Load(), f.readBytes.Load()
	first.Diagnostics.Items[0].Code = "caller-mutated"
	first.Diagnostics.Items[0].Source = "private-secret-path"
	second := resolverEvaluate(t, r, f, "movie.nfo", ignore.File, ignore.Options{})
	if f.compiles.Load() != compiles || f.readBytes.Load() <= bytesBefore {
		t.Fatal("warm cache recompiled or skipped fresh content observation")
	}
	if second.Diagnostics.Items[0].Code != "invalid_pattern" || second.Diagnostics.Items[0].Source != ".jeleeignore" || second.Token() != first.Token() {
		t.Fatal("returned diagnostics alias immutable cache")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, first), "private-secret-path") {
			t.Fatal("observation formatting exposed values")
		}
	}
	encoded, err := json.Marshal(second)
	if err != nil || bytes.Contains(encoded, []byte("token")) || bytes.Contains(encoded, []byte(f.root)) {
		t.Fatal("JSON exposed private source identity")
	}
	if f.fileOpens.Load() != f.fileCloses.Load() || f.dirOpens.Load() != f.dirCloses.Load() {
		t.Fatal("observation retained file or directory handles")
	}
}

func TestResolverCacheSeparatesRootAndCasePolicy(t *testing.T) {
	f := newResolverFixture(t)
	f.setRule(".", "*.NFO")
	r := NewResolver()
	sensitive := resolverEvaluate(t, r, f, "movie.nfo", ignore.File, ignore.Options{})
	folded := resolverEvaluate(t, r, f, "movie.nfo", ignore.File, ignore.Options{Case: ignore.CaseASCIIInsensitive})
	if sensitive.Match.Outcome != ignore.Unmatched || folded.Match.Outcome != ignore.Exclude || sensitive.Token() == folded.Token() {
		t.Fatal("matching policy reused an incompatible cache entry")
	}
	before := f.compiles.Load()
	f.mu.Lock()
	f.rootID = fileIdentity{9}
	f.mu.Unlock()
	replaced := resolverEvaluate(t, r, f, "movie.nfo", ignore.File, ignore.Options{})
	if f.compiles.Load() <= before || replaced.Token() == sensitive.Token() {
		t.Fatal("a different root reused stale source identity")
	}
}

func TestResolverWarmFailuresNeverFallbackAndErrorsAreFixed(t *testing.T) {
	for name, failure := range map[string]struct{ open, rule, read, close error }{
		"root unavailable": {open: ErrUnavailable},
		"unsafe rule":      {rule: ErrUnsafe},
		"read failure":     {read: errors.New("private C:/secret/.jeleeignore read failure")},
		"close failure":    {close: errors.New("private C:/secret/.jeleeignore")},
	} {
		t.Run(name, func(t *testing.T) {
			f := newResolverFixture(t)
			f.setRule(".", "*.nfo")
			r := NewResolver()
			resolverEvaluate(t, r, f, "movie.nfo", ignore.File, ignore.Options{})
			f.openError, f.ruleError, f.readError, f.closeError = failure.open, failure.rule, failure.read, failure.close
			got, err := r.evaluate(context.Background(), f.root, "movie.nfo", ignore.File, ignore.Options{}, f.access())
			if err == nil || !reflect.DeepEqual(got, Observation{}) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), f.root) {
				t.Fatal("warm error returned stale decision or raw details")
			}
			if !errors.Is(err, ErrRead) && !errors.Is(err, ErrUnsafe) && !errors.Is(err, ErrUnavailable) {
				t.Fatal("error outside the fixed reader contract", err)
			}
		})
	}
}

func TestResolverTwoAdmissionsThirdBusyAndCancellationJoins(t *testing.T) {
	f := newResolverFixture(t)
	f.setRule(".", "*.nfo")
	r := NewResolver()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, MaxConcurrent)
	finished := make(chan error, MaxConcurrent)
	access := f.access()
	access.compile = func(ctx context.Context, _ []ignore.Source, _ ignore.Options) (*ignore.Program, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	for range MaxConcurrent {
		go func() {
			got, err := r.evaluate(ctx, f.root, "movie.nfo", ignore.File, ignore.Options{}, access)
			if !reflect.DeepEqual(got, Observation{}) {
				finished <- errors.New("cancelled call returned a decision")
				return
			}
			finished <- err
		}()
	}
	for range MaxConcurrent {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("admitted work did not reach compile barrier")
		}
	}
	third := make(chan error, 1)
	go func() {
		got, err := r.evaluate(context.Background(), f.root, "movie.nfo", ignore.File, ignore.Options{}, f.access())
		if !reflect.DeepEqual(got, Observation{}) {
			third <- errors.New("busy call returned a decision")
			return
		}
		third <- err
	}()
	select {
	case err := <-third:
		if !errors.Is(err, ErrBusy) {
			t.Fatal("full capacity did not fail immediately", err)
		}
	case <-time.After(time.Second):
		t.Fatal("full capacity queued a third operation")
	}
	cancel()
	for range MaxConcurrent {
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled work did not join")
		}
	}
	if len(r.cache.entries) != 0 {
		t.Fatal("cancelled compilation published cache entries")
	}
	if got := resolverEvaluate(t, r, f, "movie.nfo", ignore.File, ignore.Options{}); got.Match.Outcome != ignore.Exclude {
		t.Fatal("admission not released after cancellation")
	}
}

func TestResolverCancellationAfterSuccessfulCompileDoesNotPublish(t *testing.T) {
	f := newResolverFixture(t)
	f.setRule(".", "*.nfo")
	r := NewResolver()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	access := f.access()
	compile := access.compile
	access.compile = func(ctx context.Context, sources []ignore.Source, opts ignore.Options) (*ignore.Program, error) {
		p, err := compile(ctx, sources, opts)
		cancel()
		return p, err
	}
	got, err := r.evaluate(ctx, f.root, "movie.nfo", ignore.File, ignore.Options{}, access)
	resolverRequireZero(t, got, err, context.Canceled)
	if len(r.cache.entries) != 0 || r.cache.weight != 0 {
		t.Fatal("cancelled successful compile escaped pending cache")
	}
	resolverEvaluate(t, r, f, "movie.nfo", ignore.File, ignore.Options{})
	if f.compiles.Load() != 2 {
		t.Fatal("later call reused an unpublished program")
	}
}

func TestResolverSourceLimitIsPerChainNotWholeLibrary(t *testing.T) {
	f := newResolverFixture(t)
	r := NewResolver()
	for i := range ignore.MaxSources + 1 {
		dir := fmt.Sprintf("d%03d", i)
		f.addDir(dir)
		f.setRule(dir, "*.nfo")
	}
	for i := range ignore.MaxSources + 1 {
		path := fmt.Sprintf("d%03d/movie.nfo", i)
		if got := resolverEvaluate(t, r, f, path, ignore.File, ignore.Options{}); got.Match.Outcome != ignore.Exclude {
			t.Fatal("independent chain was not observed")
		}
	}
	if len(r.cache.entries) > MaxCacheEntries || r.cache.weight > MaxCacheWeight {
		t.Fatal("many valid chains grew the cache beyond its bounds")
	}
}

func TestResolverControlPathLimitCannotSilentlyDropARequiredCheck(t *testing.T) {
	f := newResolverFixture(t)
	dir := strings.Repeat("a", ignore.MaxPathBytes-len("/.jeleeignore")+1)
	f.addDir(dir)
	r := NewResolver()
	got, err := r.evaluate(context.Background(), f.root, dir+"/f", ignore.File, ignore.Options{}, f.access())
	resolverRequireZero(t, got, err, ErrLimit)
	if len(r.cache.entries) != 0 {
		t.Fatal("failed control-path check published an incomplete chain")
	}
}

func resolverCommentBytes(size int) string {
	var text strings.Builder
	for size > 0 {
		n := min(size, 1024)
		text.WriteByte('#')
		if n > 1 {
			text.WriteString(strings.Repeat("a", n-2))
			text.WriteByte('\n')
		}
		size -= n
	}
	return text.String()
}

func TestResolverCompileAggregateBoundaryAndPlusOne(t *testing.T) {
	for _, over := range []bool{false, true} {
		t.Run(fmt.Sprintf("over-%t", over), func(t *testing.T) {
			f := newResolverFixture(t)
			r := NewResolver()
			path := strings.TrimSuffix(strings.Repeat("a/", ignore.MaxPathComponents-1), "/")
			f.addDir(path)
			firstBytes, secondBytes := ignore.MaxSourceBytes, 0
			if over {
				// 128*(256KiB-126) + 127*127 = 32MiB+1. Every source,
				// individual Compile and observed byte total remains legal.
				firstBytes, secondBytes = ignore.MaxSourceBytes-126, 127
			}
			f.setRule(".", resolverCommentBytes(firstBytes))
			for depth := 1; depth < ignore.MaxPathComponents; depth++ {
				dir := strings.TrimSuffix(strings.Repeat("a/", depth), "/")
				text := ""
				if depth == 1 {
					text = resolverCommentBytes(secondBytes)
				}
				f.setRule(dir, text)
			}
			got, err := r.evaluate(context.Background(), f.root, path+"/f", ignore.File, ignore.Options{}, f.access())
			if over {
				resolverRequireZero(t, got, err, ErrWorkLimit)
				if len(r.cache.entries) != 0 {
					t.Fatal("over-budget chain published provisional entries")
				}
			} else if err != nil || got.Match.Outcome != ignore.Unmatched || f.compiles.Load() != ignore.MaxSources {
				t.Fatal("exact combined compile input boundary rejected", err, f.compiles.Load())
			}
		})
	}
}

func TestResolverReaderBudgetExactAndPlusOne(t *testing.T) {
	// This is the shared reader budget, not a claim that a cold resolver can
	// accept sixteen full-size sources: repeated Compile inputs hit their
	// separate 32 MiB budget before that particular chain is complete.
	f := newResolverFixture(t)
	f.setRule(".", resolverCommentBytes(ignore.MaxSourceBytes))
	dir, err := f.access().openRoot(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	remaining := ignore.MaxTotalSourceBytes
	for range ignore.MaxTotalSourceBytes / ignore.MaxSourceBytes {
		stamp, raw, err := readRule(context.Background(), dir, &remaining)
		if err != nil || !stamp.present || len(raw) != ignore.MaxSourceBytes || stamp.digest != sha256.Sum256(raw) {
			t.Fatal("legal reader budget rejected", err)
		}
	}
	if remaining != 0 || f.readBytes.Load() != ignore.MaxTotalSourceBytes {
		t.Fatal("reader did not consume exactly the shared budget")
	}
	f.setRule(".", "#")
	stamp, raw, err := readRule(context.Background(), dir, &remaining)
	if !errors.Is(err, ErrLimit) || stamp != (sourceStamp{}) || raw != nil || remaining != 0 {
		t.Fatal("one extra byte escaped the aggregate limit", err)
	}
	f.mu.Lock()
	delete(f.rules, ".")
	f.mu.Unlock()
	stamp, raw, err = readRule(context.Background(), dir, &remaining)
	if err != nil || stamp != (sourceStamp{}) || raw != nil {
		t.Fatal("confirmed absence incorrectly consumed the exhausted byte budget", err)
	}
	f.setRule(".", resolverCommentBytes(ignore.MaxSourceBytes+1))
	remaining = ignore.MaxTotalSourceBytes
	readBefore := f.readBytes.Load()
	stamp, raw, err = readRule(context.Background(), dir, &remaining)
	if !errors.Is(err, ErrLimit) || stamp != (sourceStamp{}) || raw != nil || f.readBytes.Load() != readBefore {
		t.Fatal("oversized individual source was read or exposed", err)
	}
	if f.fileOpens.Load() != f.fileCloses.Load() {
		t.Fatal("budget rejection leaked its opened file")
	}
}

func TestResolverWarmCompilerFailuresReturnFixedErrors(t *testing.T) {
	for _, tc := range []struct {
		name, replacement string
		compileError      error
		want              error
	}{
		{name: "invalid-encoding", replacement: string([]byte{0xff}), want: ErrInvalid},
		{name: "physical-line-limit", replacement: strings.Repeat("#\n", ignore.MaxSourceLines+1), want: ErrLimit},
		{name: "wrapped-work-limit", replacement: "*.jpg", compileError: fmt.Errorf("private rule content at private-path: %w", ignore.ErrWorkLimit), want: ErrWorkLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResolverFixture(t)
			f.setRule(".", "*.nfo")
			r := NewResolver()
			resolverEvaluate(t, r, f, "movie.nfo", ignore.File, ignore.Options{})
			entries, weight := len(r.cache.entries), r.cache.weight
			f.setRule(".", tc.replacement)
			access := f.access()
			if tc.compileError != nil {
				access.compile = func(context.Context, []ignore.Source, ignore.Options) (*ignore.Program, error) {
					return nil, tc.compileError
				}
			}
			got, err := r.evaluate(context.Background(), f.root, "movie.nfo", ignore.File, ignore.Options{}, access)
			resolverRequireZero(t, got, err, tc.want)
			if err != tc.want || len(r.cache.entries) != entries || r.cache.weight != weight {
				t.Fatal("compiler error escaped sanitization or published a failed program")
			}
			if f.fileOpens.Load() != f.fileCloses.Load() || f.dirOpens.Load() != f.dirCloses.Load() {
				t.Fatal("compiler failure leaked source resources")
			}
		})
	}
}

func TestResolverUsesOneDeadlineForAllCompilations(t *testing.T) {
	for _, shorter := range []bool{false, true} {
		t.Run(fmt.Sprintf("caller-shorter-%t", shorter), func(t *testing.T) {
			f := newResolverFixture(t)
			f.addDir("a")
			f.setRule(".", "# root")
			f.setRule("a", "# child")
			ctx := context.Background()
			var callerDeadline time.Time
			if shorter {
				callerDeadline = time.Now().Add(10 * time.Second)
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, callerDeadline)
				defer cancel()
			}
			access := f.access()
			compile := access.compile
			var deadlines []time.Time
			access.compile = func(ctx context.Context, sources []ignore.Source, opts ignore.Options) (*ignore.Program, error) {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("compilation has no deadline")
				}
				deadlines = append(deadlines, deadline)
				return compile(ctx, sources, opts)
			}
			before := time.Now()
			_, err := NewResolver().evaluate(ctx, f.root, "a/file", ignore.File, ignore.Options{}, access)
			after := time.Now()
			if err != nil || len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
				t.Fatal("one call did not share a single deadline", err)
			}
			if shorter {
				if !deadlines[0].Equal(callerDeadline) {
					t.Fatal("resolver extended the caller deadline")
				}
			} else if deadlines[0].Before(before.Add(MaxDuration)) || deadlines[0].After(after.Add(MaxDuration)) {
				t.Fatal("resolver deadline is not bounded by the declared duration")
			}
		})
	}
}

type resolverCloseNotice struct {
	directory
	closed chan struct{}
	once   sync.Once
}

func (d *resolverCloseNotice) Close() error {
	err := d.directory.Close()
	d.once.Do(func() { close(d.closed) })
	return err
}

func TestResolverCancellationWhilePublicationWaitsDoesNotPublish(t *testing.T) {
	f := newResolverFixture(t)
	f.setRule(".", "*.nfo")
	r := NewResolver()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	access := f.access()
	openRoot, compile := access.openRoot, access.compile
	closed := make(chan struct{})
	first := true
	access.openRoot = func(root string) (directory, error) {
		dir, err := openRoot(root)
		if first && err == nil {
			first = false
			return &resolverCloseNotice{directory: dir, closed: closed}, nil
		}
		return dir, err
	}
	locked := make(chan struct{})
	access.compile = func(ctx context.Context, sources []ignore.Source, opts ignore.Options) (*ignore.Program, error) {
		p, err := compile(ctx, sources, opts)
		r.mu.Lock() // The initial cache lookup has already completed.
		close(locked)
		return p, err
	}
	type outcome struct {
		observation Observation
		err         error
	}
	returned := make(chan outcome, 1)
	go func() {
		got, err := r.evaluate(ctx, f.root, "movie.nfo", ignore.File, ignore.Options{}, access)
		returned <- outcome{got, err}
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("compiler did not reach publication gate")
	}
	var unlock sync.Once
	defer unlock.Do(r.mu.Unlock)
	select {
	case <-closed: // Last directory closed: all filesystem work is complete.
	case <-time.After(5 * time.Second):
		t.Fatal("resolver did not finish and close both observation passes")
	}
	cancel()
	unlock.Do(r.mu.Unlock)
	select {
	case got := <-returned:
		resolverRequireZero(t, got.observation, got.err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled publication did not join")
	}
	if len(r.cache.entries) != 0 || r.cache.weight != 0 || len(r.slots) != 0 {
		t.Fatal("cancelled publication retained a cache entry or admission")
	}
	if f.fileOpens.Load() != f.fileCloses.Load() || f.dirOpens.Load() != f.dirCloses.Load() {
		t.Fatal("publication wait leaked a resource")
	}
}
