package ignore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func contractCompile(t testing.TB, text string) *Program {
	t.Helper()
	p, err := Compile(context.Background(), []Source{{Path: ".jeleeignore", Text: []byte(text)}}, Options{})
	if err != nil || p == nil {
		t.Fatalf("compile: %v", err)
	}
	return p
}

func contractMatch(t testing.TB, p *Program, name string, kind Kind, want Outcome) Match {
	t.Helper()
	m, err := p.Evaluate(context.Background(), name, kind)
	if err != nil || m.Outcome != want {
		t.Fatalf("match %q: %v %v", name, m, err)
	}
	return m
}

func TestContractEmptySourcesAndInvalidInputs(t *testing.T) {
	for _, sources := range [][]Source{nil, {}, {{Path: ".jeleeignore"}}} {
		p, err := Compile(context.Background(), sources, Options{})
		if err != nil || p == nil {
			t.Fatal("empty rules must be a valid unmatched program", err)
		}
		if got := contractMatch(t, p, "movie.mkv", File, Unmatched); got != (Match{}) {
			t.Fatal("unmatched result has invented provenance")
		}
	}
	invalidPaths := []string{"", ".", "..", "/absolute", "a/../b", "a/./b", "a//b", "a/", `a\b`, "C:movie", "a\x00b", "a\nb", "a\tb", "a\u0085b", string([]byte{0xff})}
	p := contractCompile(t, "*")
	for _, name := range invalidPaths {
		t.Run(fmt.Sprintf("path_%q", name), func(t *testing.T) {
			m, err := p.Evaluate(context.Background(), name, File)
			if !errors.Is(err, ErrInvalid) || m != (Match{}) {
				t.Fatalf("invalid path returned metadata: %v %v", m, err)
			}
		})
	}
	for _, kind := range []Kind{0, 255} {
		if m, err := p.Evaluate(context.Background(), "safe", kind); !errors.Is(err, ErrInvalid) || m != (Match{}) {
			t.Fatal("invalid kind accepted")
		}
	}
	for _, source := range []string{"", "rules", "x/.ignore", "/.jeleeignore", "../.jeleeignore", "x//.jeleeignore", `x\.jeleeignore`, "x:/.jeleeignore"} {
		if p, err := Compile(context.Background(), []Source{{Path: source}}, Options{}); !errors.Is(err, ErrInvalid) || p != nil {
			t.Fatalf("invalid source accepted: %q", source)
		}
	}
	if p, err := Compile(context.Background(), []Source{{Path: ".jeleeignore"}, {Path: ".jeleeignore"}}, Options{}); !errors.Is(err, ErrInvalid) || p != nil {
		t.Fatal("duplicate source accepted")
	}
	if p, err := Compile(context.Background(), nil, Options{Case: CaseMode(255)}); !errors.Is(err, ErrInvalid) || p != nil {
		t.Fatal("unknown case policy accepted")
	}
}

func TestContractContextAndErrorsAreZeroAndFixed(t *testing.T) {
	p := contractCompile(t, "secret*")
	var nilProgram *Program
	if m, err := nilProgram.Evaluate(context.Background(), "secret", File); !errors.Is(err, ErrInvalid) || m != (Match{}) {
		t.Fatal("nil program accepted")
	}
	if m, err := (&Program{}).Evaluate(context.Background(), "secret", File); !errors.Is(err, ErrInvalid) || m != (Match{}) {
		t.Fatal("uninitialized program accepted")
	}
	if d := nilProgram.Diagnostics(); d.Total != 0 || d.Truncated || len(d.Items) != 0 {
		t.Fatal("nil program invented diagnostics")
	}
	if m, err := p.Evaluate(nil, "secret", File); !errors.Is(err, ErrInvalid) || m != (Match{}) {
		t.Fatal("nil context accepted")
	}
	if p, err := Compile(nil, nil, Options{}); !errors.Is(err, ErrInvalid) || p != nil {
		t.Fatal("nil compile context accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, c := range []context.Context{cancelled, expired} {
		if got, err := Compile(c, []Source{{Path: ".jeleeignore", Text: []byte("*")}}, Options{}); !errors.Is(err, c.Err()) || got != nil {
			t.Fatal("compile did not preserve context error")
		}
		if got, err := p.Evaluate(c, "secret", File); !errors.Is(err, c.Err()) || got != (Match{}) {
			t.Fatal("evaluate did not preserve context error")
		}
	}
	for err, want := range map[error]string{ErrInvalid: "ignore_invalid_input", ErrLimit: "ignore_limit", ErrWorkLimit: "ignore_work_limit"} {
		if err.Error() != want {
			t.Fatal("error is not a fixed public classification")
		}
	}
}

func TestContractCompiledInputsAndDiagnosticsAreImmutable(t *testing.T) {
	text := []byte("*.nfo\n\\\n")
	sources := []Source{{Path: ".jeleeignore", Text: text}}
	p, err := Compile(context.Background(), sources, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := contractMatch(t, p, "movie.nfo", File, Exclude)
	for i := range text {
		text[i] = '#'
	}
	sources[0].Path = "changed/.jeleeignore"
	sources[0].Text = nil
	if got := contractMatch(t, p, "movie.nfo", File, Exclude); got != want {
		t.Fatal("caller mutation changed compiled program")
	}
	d := p.Diagnostics()
	if d.Total != 1 || d.Truncated || len(d.Items) != 1 || d.Items[0].Source != ".jeleeignore" || d.Items[0].Line != 2 || d.Items[0].Code != "invalid_pattern" {
		t.Fatalf("invalid rule diagnostic: %+v", d)
	}
	d.Items[0].Source, d.Items[0].Code = "injected", "secret"
	if got := p.Diagnostics(); got.Items[0].Source != ".jeleeignore" || got.Items[0].Code != "invalid_pattern" {
		t.Fatal("diagnostics returned mutable program memory")
	}
	secret := "credential-do-not-print"
	source := Source{Path: ".jeleeignore", Text: []byte(secret)}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, source), secret) || strings.Contains(fmt.Sprintf(format, contractCompile(t, secret)), secret) {
			t.Fatal("debug formatting exposed raw rules")
		}
	}
	encoded, err := json.Marshal(source)
	if err != nil || strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "Y3JlZGVudGlhb") {
		t.Fatal("JSON exposed raw rule bytes")
	}
}

func TestContractIndependentProgramsAndParallelEvaluation(t *testing.T) {
	sources := []Source{{Path: ".jeleeignore", Text: []byte("*.NFO\n")}, {Path: "Series/.jeleeignore", Text: []byte("*.mkv\n")}}
	sensitive, err := Compile(context.Background(), sources, Options{})
	if err != nil {
		t.Fatal(err)
	}
	folded, err := Compile(context.Background(), sources, Options{Case: CaseASCIIInsensitive})
	if err != nil {
		t.Fatal(err)
	}
	contractMatch(t, sensitive, "a.nfo", File, Unmatched)
	contractMatch(t, folded, "a.nfo", File, Exclude)
	contractMatch(t, folded, "series/a.mkv", File, Unmatched) // Source activation stays byte-exact.
	contractMatch(t, folded, "Series/a.mkv", File, Exclude)
	var wg sync.WaitGroup
	failures := make(chan string, 32)
	for worker := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				p, expected := sensitive, Unmatched
				if worker%2 == 0 {
					p, expected = folded, Exclude
				}
				got, err := p.Evaluate(context.Background(), "a.nfo", File)
				if err != nil || got.Outcome != expected {
					failures <- "shared scratch or case policy changed result"
					return
				}
				d := p.Diagnostics()
				if d.Total != 0 {
					failures <- "shared diagnostics changed"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}

// Gate an in-flight context poll so real cancellation is deterministic, rather
// than hoping a sleep happens while the matcher is still busy.
type contractGateContext struct {
	context.Context
	calls  atomic.Int32
	enter  chan struct{}
	resume chan struct{}
}

func (c *contractGateContext) Err() error {
	if c.calls.Add(1) == 4 {
		close(c.enter)
		<-c.resume
	}
	return c.Context.Err()
}

func TestContractRealCancellationJoinsAndDoesNotPoisonProgram(t *testing.T) {
	p := contractCompile(t, strings.Repeat("*a*a*a*a*a*b\n", 512))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := &contractGateContext{Context: ctx, enter: make(chan struct{}), resume: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		got, err := p.Evaluate(gate, strings.Repeat("a", 512), File)
		if got != (Match{}) {
			result <- errors.New("cancelled evaluation returned partial decision")
			return
		}
		result <- err
	}()
	select {
	case <-gate.enter:
	case err := <-result:
		t.Fatalf("evaluation did not reach in-flight poll: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		close(gate.resume)
		t.Fatal("evaluation never polled context")
	}
	cancel()
	close(gate.resume)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("real cancellation was lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled evaluator did not join")
	}
	contractMatch(t, p, "aaaaab", File, Exclude)
}

func TestContractCompileCancellationReturnsNoSnapshotAndJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := &contractGateContext{Context: ctx, enter: make(chan struct{}), resume: make(chan struct{})}
	result := make(chan error, 1)
	text := []byte(contractComments(MaxSourceBytes))
	go func() {
		p, err := Compile(gate, []Source{{Path: ".jeleeignore", Text: text}}, Options{})
		if p != nil {
			result <- errors.New("cancelled compiler returned a partial snapshot")
			return
		}
		result <- err
	}()
	select {
	case <-gate.enter:
	case err := <-result:
		t.Fatalf("compile did not reach in-flight poll: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		close(gate.resume)
		t.Fatal("compiler never polled context")
	}
	cancel()
	close(gate.resume)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("compiler lost real cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled compiler did not join")
	}
	if p, err := Compile(context.Background(), []Source{{Path: ".jeleeignore", Text: text}}, Options{}); err != nil || p == nil {
		t.Fatal("cancelled call changed source values or later compilation", err)
	}
}

func TestContractProductionPackageHasOnlyPureDependencies(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate package")
	}
	files, err := os.ReadDir(filepath.Dir(filename))
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"bytes": true, "context": true, "errors": true, "fmt": true, "math": true, "math/bits": true, "sort": true, "slices": true, "strings": true, "unicode": true, "unicode/utf8": true, "unicode/utf16": true, "encoding/binary": true, "strconv": true, "path": true}
	count := 0
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
			continue
		}
		count++
		node, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(filename), file.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range node.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil || !allowed[name] {
				t.Errorf("%s imports a dependency outside the pure matcher boundary: %s", file.Name(), name)
			}
		}
	}
	if count == 0 {
		t.Fatal("architecture check examined no production source")
	}
}
