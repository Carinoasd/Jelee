package ignore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

const oracleStdoutLimit, oracleStderrLimit = 1 << 20, 64 << 10

type oracleBuffer struct {
	data     bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (w *oracleBuffer) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.data.Len() {
		w.overflow = true
		w.cancel()
		return 0, errors.New("oracle_output_limit")
	}
	return w.data.Write(p)
}

type gitOracle struct {
	git, base, empty, template string
	ctx                        context.Context
}

func (o gitOracle) run(t *testing.T, input []byte, args ...string) []byte {
	t.Helper()
	if len(input) > 256<<10 {
		t.Fatal("oracle stdin bound exceeded")
	}
	ctx, cancel := context.WithTimeout(o.ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.git, args...)
	// Deliberately do not inherit PATH, GIT_*, loader injection, user config,
	// credentials, prompts, hooks, templates, locale, or filesystem overrides.
	cmd.Env = []string{"HOME=" + o.base, "XDG_CONFIG_HOME=" + o.base, "GIT_CONFIG_GLOBAL=" + o.empty, "GIT_CONFIG_SYSTEM=" + o.empty, "GIT_CONFIG_NOSYSTEM=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT=0", "LANG=C", "LC_ALL=C", "TMPDIR=" + o.base, "TMP=" + o.base, "TEMP=" + o.base}
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"))
	}
	cmd.Stdin = bytes.NewReader(input)
	out := &oracleBuffer{limit: oracleStdoutLimit, cancel: cancel}
	stderr := &oracleBuffer{limit: oracleStderrLimit, cancel: cancel}
	cmd.Stdout, cmd.Stderr = out, stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run() // Wait joins the bounded stdout/stderr copy goroutines.
	if out.overflow || stderr.overflow {
		t.Fatal("oracle subprocess output exceeded bound")
	}
	if ctx.Err() != nil {
		t.Fatal("oracle subprocess deadline or suite deadline exceeded")
	}
	if err != nil {
		var exit *exec.ExitError
		isCheck := len(args) > 0 && args[len(args)-1] == "--stdin"
		if !isCheck || !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatal("oracle subprocess failed; raw stderr suppressed")
		}
	}
	return out.data.Bytes()
}

func oracleProjectRoot(t *testing.T) string {
	t.Helper()
	p, err := os.Getwd()
	if err != nil {
		t.Fatal("oracle working directory unavailable")
	}
	for range 8 {
		if b, e := os.ReadFile(filepath.Join(p, "go.mod")); e == nil && bytes.HasPrefix(b, []byte("module github.com/MoYuanCN/Jelee\n")) {
			return p
		}
		next := filepath.Dir(p)
		if next == p {
			break
		}
		p = next
	}
	t.Fatal("oracle requires the project workspace")
	return ""
}
func newGitOracle(t *testing.T, ctx context.Context, git string) gitOracle {
	t.Helper()
	parent := filepath.Join(oracleProjectRoot(t), ".testdata")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal("oracle workspace unavailable")
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("oracle parent must be a real directory")
	}
	base, err := os.MkdirTemp(parent, "ignore-oracle-")
	if err != nil {
		t.Fatal("oracle private directory unavailable")
	}
	rel, err := filepath.Rel(parent, base)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		t.Fatal("oracle directory escaped project workspace")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error("oracle private directory cleanup failed")
		}
	})
	empty, template := filepath.Join(base, "empty"), filepath.Join(base, "template")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal("oracle empty config unavailable")
	}
	if err := os.Mkdir(template, 0700); err != nil {
		t.Fatal("oracle empty template unavailable")
	}
	return gitOracle{git: git, base: base, empty: empty, template: template, ctx: ctx}
}
func oracleExecutable(t *testing.T) string {
	t.Helper()
	git := os.Getenv("JELEE_IGNORE_ORACLE_GIT")
	if git != "" && !filepath.IsAbs(git) {
		t.Fatal("explicit oracle Git must be an absolute path")
	}
	if git == "" {
		var err error
		git, err = exec.LookPath("git")
		if err != nil {
			if os.Getenv("JELEE_REQUIRE_IGNORE_ORACLE") == "true" {
				t.Fatal("required Git oracle unavailable")
			}
			t.Skip("Git oracle unavailable; not compatibility evidence")
		}
	}
	git, err := filepath.Abs(git)
	if err != nil {
		t.Fatal("oracle Git path unavailable")
	}
	git, err = filepath.EvalSymlinks(git)
	if err != nil {
		t.Fatal("oracle Git executable unavailable")
	}
	return git
}
func oracleExecutableHash(t *testing.T, git string) string {
	t.Helper()
	f, err := os.Open(git)
	if err != nil {
		t.Fatal("oracle executable unreadable")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() < 1 || st.Size() > 64<<20 {
		t.Fatal("oracle executable identity outside bounds")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (64<<20)+1))
	if err != nil || n != st.Size() {
		t.Fatal("oracle executable changed while hashing")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (o gitOracle) fixture(t *testing.T, index int, tc semanticCase) string {
	t.Helper()
	repo := filepath.Join(o.base, fmt.Sprintf("repo-%03d", index))
	o.run(t, nil, "-c", "init.defaultBranch=ignore-oracle", "init", "--quiet", "--template="+o.template, repo)
	// Only built-in init/check-ignore are executed. No add, commit or hook runs.
	if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0700); err != nil {
		t.Fatal("oracle info directory unavailable")
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), nil, 0600); err != nil {
		t.Fatal("oracle local excludes unavailable")
	}
	for _, s := range tc.Sources {
		p := filepath.Join(repo, filepath.FromSlash(strings.TrimSuffix(s.Path, ".jeleeignore")+".gitignore"))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal("oracle source directory unavailable")
		}
		if err := os.WriteFile(p, s.Text, 0600); err != nil {
			t.Fatal("oracle source fixture unavailable")
		}
	}
	for _, q := range tc.Queries {
		p := filepath.Join(repo, filepath.FromSlash(q.Path))
		dir := filepath.Dir(p)
		if q.Kind == ignore.Directory {
			dir = p
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal("oracle candidate directory unavailable")
		}
	}
	for _, q := range tc.Queries {
		if q.Kind == ignore.File {
			p := filepath.Join(repo, filepath.FromSlash(q.Path))
			if err := os.WriteFile(p, nil, 0600); err != nil {
				t.Fatal("oracle file fixture unsupported on this filesystem")
			}
		}
	}
	return repo
}

type oracleDecision struct {
	outcome ignore.Outcome
	source  string
	line    int
}

func (o gitOracle) check(t *testing.T, repo string, mode ignore.CaseMode, queries []semanticQuery) map[string]oracleDecision {
	t.Helper()
	kinds := map[string]ignore.Kind{}
	for _, q := range queries {
		if prior, ok := kinds[q.Path]; ok && prior != q.Kind {
			t.Fatal("conflicting oracle fixture kinds")
		}
		kinds[q.Path] = q.Kind
		for parent := strings.LastIndexByte(q.Path, '/'); parent >= 0; parent = strings.LastIndexByte(q.Path[:parent], '/') {
			kinds[q.Path[:parent]] = ignore.Directory
		}
	}
	if len(kinds) > 256 {
		t.Fatal("oracle candidate batch bound exceeded")
	}
	paths := make([]string, 0, len(kinds))
	for p := range kinds {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var input bytes.Buffer
	for _, p := range paths {
		input.WriteString(p)
		// The fixture already makes directory kind real. A trailing slash asks
		// Git about a different lexical path (and can activate that directory's
		// own rules), whereas Evaluate requires canonical paths without it.
		input.WriteByte(0)
	}
	out := o.run(t, input.Bytes(), "-C", repo, "-c", "core.excludesFile="+o.empty, "-c", "core.ignoreCase="+strconv.FormatBool(mode == ignore.CaseASCIIInsensitive), "-c", "core.autocrlf=false", "check-ignore", "--no-index", "--verbose", "--non-matching", "-z", "--stdin")
	fields := bytes.Split(out, []byte{0})
	if len(fields) != 4*len(paths)+1 || len(fields[len(fields)-1]) != 0 {
		t.Fatal("oracle response framing mismatch")
	}
	result := make(map[string]oracleDecision, len(paths))
	for i, p := range paths {
		f := fields[i*4 : i*4+4]
		returned := strings.TrimSuffix(string(f[3]), "/")
		if returned != p {
			t.Fatal("oracle changed candidate order")
		}
		d := oracleDecision{}
		if len(f[0]) == 0 {
			if len(f[1]) != 0 || len(f[2]) != 0 {
				t.Fatal("oracle nonmatching framing mismatch")
			}
		} else {
			source := filepath.ToSlash(string(f[0]))
			if !strings.HasSuffix(source, ".gitignore") || filepath.IsAbs(source) || strings.HasPrefix(source, "../") {
				t.Fatal("oracle used unexpected ignore source")
			}
			d.source = strings.TrimSuffix(source, ".gitignore") + ".jeleeignore"
			line, err := strconv.Atoi(string(f[1]))
			if err != nil || line < 1 || line > 4096 {
				t.Fatal("oracle physical line outside fixture bounds")
			}
			d.line = line
			d.outcome = ignore.Exclude
			if bytes.HasPrefix(f[2], []byte("!")) {
				d.outcome = ignore.Include
			}
		}
		result[p] = d
	}
	return result
}
func normalizedOracle(q semanticQuery, all map[string]oracleDecision) ignore.Match {
	for i := 0; i < len(q.Path); i++ {
		if q.Path[i] == '/' {
			ancestor := q.Path[:i]
			d := all[ancestor]
			if d.outcome == ignore.Exclude {
				return ignore.Match{Outcome: ignore.Exclude, Source: d.source, Line: d.line, MatchedPath: ancestor, ParentBlocked: true}
			}
		}
	}
	d := all[q.Path]
	if d.outcome == ignore.Unmatched {
		return ignore.Match{}
	}
	return ignore.Match{Outcome: d.outcome, Source: d.source, Line: d.line, MatchedPath: q.Path}
}

func TestGitOracle(t *testing.T) {
	git := oracleExecutable(t)
	hash := oracleExecutableHash(t, git)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	o := newGitOracle(t, ctx, git)
	version := strings.TrimSpace(string(o.run(t, nil, "--version")))
	if !strings.HasPrefix(version, "git version ") || len(version) > 128 || strings.ContainsAny(version, "\r\n\x00") {
		t.Fatal("oracle version response rejected")
	}
	golden := semanticCorpus()
	corpus := append(golden, combinationCorpus()...)
	// Source.Text is intentionally excluded by its production JSON tags. Hash
	// explicit test-only source records so rules participate in corpus identity.
	type rawSource struct {
		Path string
		Text []byte
	}
	type rawCase struct {
		Name            string
		Case            ignore.CaseMode
		Sources         []rawSource
		Queries         []semanticQuery
		NativeNamesOnly bool
		OracleOnly      bool
	}
	raw := make([]rawCase, 0, len(corpus))
	for _, tc := range corpus {
		r := rawCase{Name: tc.Name, Case: tc.Case, Queries: tc.Queries, NativeNamesOnly: tc.NativeNamesOnly, OracleOnly: tc.OracleOnly}
		for _, s := range tc.Sources {
			r.Sources = append(r.Sources, rawSource{s.Path, s.Text})
		}
		raw = append(raw, r)
	}
	corpusBytes, err := json.Marshal(raw)
	if err != nil {
		t.Fatal("oracle corpus serialization failed")
	}
	digest := sha256.Sum256(corpusBytes)
	groups, candidates := 0, 0
	omitted := []string{}
	for index, tc := range corpus {
		if runtime.GOOS == "windows" && tc.NativeNamesOnly {
			omitted = append(omitted, tc.Name)
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			repo := o.fixture(t, index, tc)              //nolint:contextcheck // Git oracle helpers bound each command with their own timeout
			got := o.check(t, repo, tc.Case, tc.Queries) //nolint:contextcheck // Git oracle helpers bound each command with their own timeout
			p, err := ignore.Compile(ctx, tc.Sources, ignore.Options{Case: tc.Case})
			if err != nil {
				t.Fatal("matcher rejected oracle corpus", err)
			}
			for i, q := range tc.Queries {
				oracle := normalizedOracle(q, got)
				if !tc.OracleOnly && oracle != q.Want {
					t.Errorf("oracle disagrees with golden candidate %d: got %+v, want %+v", i, oracle, q.Want)
				}
				actual, err := p.Evaluate(ctx, q.Path, q.Kind)
				if err != nil || actual != oracle {
					t.Errorf("matcher differs from Git candidate %d: got %+v, oracle %+v, error %v", i, actual, oracle, err)
				}
			}
		})
		groups++
		candidates += len(tc.Queries)
	}
	// Source activation follows real filesystem lookup in Git. The pure API
	// deliberately uses exact source path values on every OS; report aliasing.
	activation := semanticCase{Sources: []ignore.Source{{Path: "Case/.jeleeignore", Text: []byte("*\n")}}, Queries: []semanticQuery{unmatched("Case/file", ignore.File), unmatched("case/file", ignore.File)}}
	repo := o.fixture(t, len(corpus), activation)
	lookupAliases := false
	if _, err := os.Stat(filepath.Join(repo, "case", ".gitignore")); err == nil {
		lookupAliases = true
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oracle source activation stat failed")
	}
	for _, mode := range []ignore.CaseMode{ignore.CaseSensitive, ignore.CaseASCIIInsensitive} {
		decisions := o.check(t, repo, mode, activation.Queries)
		if decisions["Case/file"].outcome != ignore.Exclude {
			t.Fatal("oracle source activation canary failed")
		}
		want := ignore.Unmatched
		if lookupAliases {
			want = ignore.Exclude
		}
		if decisions["case/file"].outcome != want {
			t.Fatal("oracle source activation differs from filesystem lookup")
		}
	}
	if after := oracleExecutableHash(t, git); after != hash {
		t.Fatal("oracle executable identity changed during suite")
	}
	report := struct {
		SchemaVersion                                                        int
		GitPath, GitVersion, GitSHA256, Platform, CorpusSHA256               string
		Groups, Candidates, GoldenGroups, GeneratedGroups                    int
		PlatformOmissions                                                    []string
		SourceLookupAliasesCase                                              bool
		SourceContract                                                       string
		ProcessTimeoutSeconds, SuiteTimeoutSeconds, StdoutLimit, StderrLimit int
		Passed                                                               bool
	}{1, git, version, hash, runtime.GOOS + "-" + runtime.GOARCH, hex.EncodeToString(digest[:]), groups, candidates, len(golden) - len(omitted), len(corpus) - len(golden), omitted, lookupAliases, "exact canonical source paths; filesystem case aliasing is not part of matcher values", 5, 60, oracleStdoutLimit, oracleStderrLimit, !t.Failed()}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal("oracle report encoding failed")
	}
	t.Logf("IGNORE_ORACLE_REPORT %s", b)
}
