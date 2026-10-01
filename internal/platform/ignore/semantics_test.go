package ignore_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

type semanticQuery struct {
	Path string
	Kind ignore.Kind
	Want ignore.Match
}
type semanticCase struct {
	Name            string
	Sources         []ignore.Source
	Case            ignore.CaseMode
	Queries         []semanticQuery
	NativeNamesOnly bool
	OracleOnly      bool
}

func unmatched(path string, kind ignore.Kind) semanticQuery {
	return semanticQuery{path, kind, ignore.Match{}}
}
func decided(path string, kind ignore.Kind, outcome ignore.Outcome, source string, line int) semanticQuery {
	return semanticQuery{path, kind, ignore.Match{Outcome: outcome, Source: source, Line: line, MatchedPath: path}}
}
func rootMatch(path string, outcome ignore.Outcome) semanticQuery {
	return decided(path, ignore.File, outcome, ".jeleeignore", 1)
}
func blocked(path, ancestor, source string, line int) semanticQuery {
	return semanticQuery{path, ignore.File, ignore.Match{Outcome: ignore.Exclude, Source: source, Line: line, MatchedPath: ancestor, ParentBlocked: true}}
}
func single(name, pattern string, queries ...semanticQuery) semanticCase {
	return semanticCase{Name: name, Sources: []ignore.Source{{Path: ".jeleeignore", Text: []byte(pattern + "\n")}}, Queries: queries}
}

// Expected results are explicit corpus values, independent of matcher internals.
// Git differential tests below consume the same values and also compare their
// provenance against real ancestor-directory decisions from check-ignore.
func semanticCorpus() []semanticCase {
	E, I := ignore.Exclude, ignore.Include
	cases := []semanticCase{
		single("empty", "", unmatched("plain.txt", ignore.File)),
		single("comment", "# comment", unmatched("plain.txt", ignore.File)),
		single("escaped-comment", `\#note`, rootMatch("#note", E), unmatched("note", ignore.File)),
		single("escaped-negation", `\!note`, rootMatch("!note", E), unmatched("note", ignore.File)),
		single("leading-space-comment-is-literal", " #note", rootMatch(" #note", E), unmatched("#note", ignore.File)),
		single("unescaped-trailing-spaces", "name   ", rootMatch("name", E), unmatched("names", ignore.File)),
		single("internal-space", `a\ b`, rootMatch("a b", E), unmatched("ab", ignore.File)),
		single("trailing-unpaired-backslash", `foo\`, unmatched("foo", ignore.File), unmatched("foox", ignore.File)),
		single("bare-negation", "!", unmatched("a", ignore.File)),
		single("bare-root-slash", "/", unmatched("a", ignore.File)),
		single("anchored-file", "/file", rootMatch("file", E), unmatched("dir/file", ignore.File)),
		single("basename-any-depth", "file", rootMatch("file", E), rootMatch("dir/file", E), rootMatch("one/two/file", E)),
		single("middle-slash-anchors", "dir/file", rootMatch("dir/file", E), unmatched("other/dir/file", ignore.File)),
		single("directory-suffix-file", "cache/", unmatched("cache", ignore.File)),
		single("directory-suffix-directory", "cache/", decided("cache", ignore.Directory, E, ".jeleeignore", 1), blocked("cache/a", "cache", ".jeleeignore", 1), decided("nested/cache", ignore.Directory, E, ".jeleeignore", 1)),
		single("directory-without-suffix", "cache", decided("cache", ignore.Directory, E, ".jeleeignore", 1), blocked("cache/a", "cache", ".jeleeignore", 1)),
		single("ordinary-star", "a*b", rootMatch("ab", E), rootMatch("azb", E), unmatched("a/x/b", ignore.File)),
		single("ordinary-question", "a?b", rootMatch("axb", E), unmatched("ab", ignore.File), unmatched("axxb", ignore.File)),
		single("escaped-brackets", `a\[b\]`, rootMatch("a[b]", E), unmatched("ab", ignore.File)),
		single("no-brace-expansion", "{a,b}", rootMatch("{a,b}", E), unmatched("a", ignore.File), unmatched("b", ignore.File)),
		single("no-regex-expansion", "(foo|bar)", rootMatch("(foo|bar)", E), unmatched("foo", ignore.File)),
		single("globstar-leading", "**/target", rootMatch("target", E), rootMatch("a/target", E), rootMatch("a/b/target", E)),
		single("globstar-middle", "a/**/b", rootMatch("a/b", E), rootMatch("a/x/b", E), rootMatch("a/x/y/b", E), unmatched("x/a/b", ignore.File)),
		single("repeated-component-globstar", "a/**/**/b", rootMatch("a/b", E), rootMatch("a/x/b", E), rootMatch("a/x/y/b", E)),
		single("globstar-trailing", "abc/**", unmatched("abc", ignore.Directory), rootMatch("abc/file", E), blocked("abc/sub/file", "abc/sub", ".jeleeignore", 1)),
		single("three-stars-middle", "a/***/b", rootMatch("a/b", E), rootMatch("a/x/y/b", E)),
		single("four-stars-middle", "a/****/b", rootMatch("a/b", E), rootMatch("a/x/y/b", E)),
		single("three-stars-leading", "***/target", rootMatch("target", E), rootMatch("a/b/target", E)),
		single("three-stars-trailing", "abc/***", unmatched("abc", ignore.Directory), rootMatch("abc/file", E), blocked("abc/sub/file", "abc/sub", ".jeleeignore", 1)),
		single("stars-within-component", "ab**cd", rootMatch("abXXcd", E), unmatched("ab/XXcd", ignore.File)),
		single("globstar-escaped-slash", `a/**\/b`, unmatched("a/b", ignore.File), rootMatch("a/x/b", E), rootMatch("a/x/y/b", E)),
		single("literal-class", "[abc]", rootMatch("a", E), rootMatch("b", E), unmatched("d", ignore.File)),
		single("range-class", "[a-c]", rootMatch("a", E), rootMatch("b", E), rootMatch("c", E), unmatched("d", ignore.File)),
		single("reverse-range-retains-first", "[z-a]", rootMatch("z", E), unmatched("a", ignore.File), unmatched("m", ignore.File)),
		single("range-resets-previous", "[a-c-e]", rootMatch("a", E), rootMatch("c", E), rootMatch("-", E), rootMatch("e", E), unmatched("d", ignore.File)),
		single("leading-closing-bracket", "[]a]", rootMatch("]", E), rootMatch("a", E), unmatched("b", ignore.File)),
		single("leading-hyphen", "[-a]", rootMatch("-", E), rootMatch("a", E), unmatched("b", ignore.File)),
		single("trailing-hyphen", "[a-]", rootMatch("-", E), rootMatch("a", E), unmatched("b", ignore.File)),
		single("negated-class-bang", "[!a]", rootMatch("b", E), unmatched("a", ignore.File), unmatched("é", ignore.File)),
		single("negated-class-caret", "[^a]", rootMatch("b", E), unmatched("a", ignore.File)),
		single("malformed-open-class", "[", unmatched("[", ignore.File), unmatched("a", ignore.File)),
		single("malformed-unclosed-class", "[abc", unmatched("[abc", ignore.File), unmatched("a", ignore.File)),
		single("malformed-empty-class", "[]", unmatched("[]", ignore.File), unmatched("]", ignore.File)),
		single("unknown-posix-class", "[[:unknown:]]", unmatched("a", ignore.File), unmatched("5", ignore.File)),
		single("malformed-posix-fallback", "[[:digit]]", rootMatch("d]", E), rootMatch("[]", E), unmatched("5", ignore.File), unmatched("d", ignore.File)),
		single("utf8-question-is-byte", "?", unmatched("é", ignore.File), unmatched("中", ignore.File), rootMatch("x", E)),
		single("utf8-two-bytes", "??", rootMatch("é", E), unmatched("中", ignore.File)),
		single("utf8-three-bytes", "???", rootMatch("中", E), unmatched("é", ignore.File)),
		single("utf8-class-is-byte", "[é]", unmatched("é", ignore.File), unmatched("e", ignore.File)),
		single("unicode-literal-exact", "é", rootMatch("é", E), unmatched("É", ignore.File)),
		single("slash-not-cleaned", "dir//file", unmatched("dir/file", ignore.File)),
		single("dotdot-not-cleaned", "dir/../file", unmatched("file", ignore.File), unmatched("dir/file", ignore.File)),
		{Name: "last-match-and-physical-line", Sources: []ignore.Source{{Path: ".jeleeignore", Text: []byte("# header\n\n*.log\n!keep.log\nkeep.log\n!final.log\n")}}, Queries: []semanticQuery{decided("a.log", ignore.File, E, ".jeleeignore", 3), decided("keep.log", ignore.File, E, ".jeleeignore", 5), decided("final.log", ignore.File, I, ".jeleeignore", 6), unmatched("a.txt", ignore.File)}},
		{Name: "ancestor-prune", Sources: []ignore.Source{{Path: "blocked/.jeleeignore", Text: []byte("!keep.txt\n")}, {Path: ".jeleeignore", Text: []byte("blocked/\n!blocked/keep.txt\n")}}, Queries: []semanticQuery{blocked("blocked/keep.txt", "blocked", ".jeleeignore", 1)}},
		{Name: "ancestor-reinclude-activates-child", Sources: []ignore.Source{{Path: "dir/.jeleeignore", Text: []byte("*.tmp\n!keep.tmp\n")}, {Path: ".jeleeignore", Text: []byte("dir/\n!dir/\n")}}, Queries: []semanticQuery{decided("dir", ignore.Directory, I, ".jeleeignore", 2), decided("dir/a.tmp", ignore.File, E, "dir/.jeleeignore", 1), decided("dir/keep.tmp", ignore.File, I, "dir/.jeleeignore", 2), unmatched("dir/plain", ignore.File)}},
		{Name: "source-priority-and-siblings", Sources: []ignore.Source{{Path: "left/deep/.jeleeignore", Text: []byte("*.log\n")}, {Path: "right/.jeleeignore", Text: []byte("!*.log\n")}, {Path: ".jeleeignore", Text: []byte("*.log\n")}, {Path: "left/.jeleeignore", Text: []byte("!*.log\n")}}, Queries: []semanticQuery{rootMatch("root.log", E), decided("left/a.log", ignore.File, I, "left/.jeleeignore", 1), decided("left/deep/a.log", ignore.File, E, "left/deep/.jeleeignore", 1), decided("right/a.log", ignore.File, I, "right/.jeleeignore", 1), rootMatch("other/a.log", E)}},
		{Name: "empty-child-does-not-reset", Sources: []ignore.Source{{Path: ".jeleeignore", Text: []byte("*.log\n")}, {Path: "dir/.jeleeignore", Text: []byte("# empty\n")}}, Queries: []semanticQuery{rootMatch("dir/a.log", E)}},
		{Name: "child-cannot-affect-itself", Sources: []ignore.Source{{Path: "dir/.jeleeignore", Text: []byte("*\n")}}, Queries: []semanticQuery{unmatched("dir", ignore.Directory), decided("dir/file", ignore.File, E, "dir/.jeleeignore", 1)}},
	}
	for i := range cases {
		if cases[i].Name == "no-regex-expansion" {
			cases[i].NativeNamesOnly = true
		}
	}
	for _, item := range []struct{ name, pattern, yes, no string }{
		{"alnum", "[[:alnum:]]", "5", "-"}, {"alpha", "[[:alpha:]]", "a", "5"}, {"blank", "[[:blank:]]", " ", "a"}, {"cntrl", "[[:cntrl:]]", "", "a"}, {"digit", "[[:digit:]]", "5", "a"}, {"graph", "[[:graph:]]", "!", " "}, {"lower", "[[:lower:]]", "a", "A"}, {"print", "[[:print:]]", " ", "é"}, {"punct", "[[:punct:]]", "!", "a"}, {"space", "[[:space:]]", " ", "a"}, {"upper", "[[:upper:]]", "A", "a"}, {"xdigit", "[[:xdigit:]]", "F", "G"},
	} {
		q := []semanticQuery{unmatched(item.no, ignore.File)}
		if item.yes != "" {
			q = append(q, rootMatch(item.yes, E))
		}
		c := single("posix-"+item.name, item.pattern, q...)
		c.NativeNamesOnly = item.yes == " " || item.no == " "
		cases = append(cases, c)
	}
	for _, item := range []struct {
		name, pattern string
		queries       []semanticQuery
	}{
		{"literal", "AbC", []semanticQuery{rootMatch("aBc", E), rootMatch("ABC", E)}},
		{"uppercase-class-literal", "[A]", []semanticQuery{unmatched("a", ignore.File), unmatched("A", ignore.File)}},
		{"lowercase-class-literal", "[a]", []semanticQuery{rootMatch("a", E), rootMatch("A", E)}},
		{"uppercase-range", "[A-Z]", []semanticQuery{rootMatch("a", E), rootMatch("Z", E)}},
		{"upper-posix", "[[:upper:]]", []semanticQuery{rootMatch("a", E), rootMatch("A", E)}},
		{"lower-posix", "[[:lower:]]", []semanticQuery{rootMatch("a", E), rootMatch("A", E)}},
		{"unicode-no-fold", "é", []semanticQuery{rootMatch("é", E), unmatched("É", ignore.File)}},
	} {
		c := single("ascii-fold-"+item.name, item.pattern, item.queries...)
		c.Case = ignore.CaseASCIIInsensitive
		cases = append(cases, c)
	}
	for _, item := range []struct {
		name, pattern string
		queries       []semanticQuery
	}{
		{"escaped-tail-space", `name\ `, []semanticQuery{rootMatch("name ", E), unmatched("name", ignore.File)}},
		{"even-slashes-tail-space", `name\\ `, []semanticQuery{unmatched("name ", ignore.File), unmatched("name", ignore.File)}},
		{"odd-slashes-tail-space", `name\\\ `, []semanticQuery{unmatched("name ", ignore.File), unmatched("name", ignore.File)}},
	} {
		c := single(item.name, item.pattern, item.queries...)
		c.NativeNamesOnly = true
		cases = append(cases, c)
	}
	return cases
}

// A fixed Cartesian product exercises feature interactions independently of
// the handwritten golden values. Each result is compared directly with Git.
// Five glob contexts x six leaf patterns x two case modes = sixty groups;
// each group has sixty short candidates and two rule-source levels.
func combinationCorpus() []semanticCase {
	prefixes := []string{"", "**/", "top/**/", "top/*/", `top/**\/`}
	leaves := []string{`a[!x]?*.dat`, `x[[:digit:]][A-C].log`, `a\[b\].txt`, `[A]file`, `[a-c-e]*`, `\#*`}
	names := []string{"abc1.dat", "axc1.dat", "ab.dat", "abcd.dat", "x5A.log", "x7b.log", "a[b].txt", "Afile", "afile", "d-tail", "c-tail", "#note"}
	paths := []string{"", "top/", "top/mid/", "top/mid/deep/", "elsewhere/"}
	result := make([]semanticCase, 0, 60)
	for pi, prefix := range prefixes {
		for li, leaf := range leaves {
			for _, mode := range []ignore.CaseMode{ignore.CaseSensitive, ignore.CaseASCIIInsensitive} {
				c := semanticCase{Name: fmt.Sprintf("combination-%d-%d-%d", pi, li, mode), Case: mode, OracleOnly: true, Sources: []ignore.Source{{Path: ".jeleeignore", Text: []byte(prefix + leaf + "\n!top/keep.txt\n")}, {Path: "top/mid/.jeleeignore", Text: []byte("!*.dat\n")}}}
				for _, path := range paths {
					for _, name := range names {
						c.Queries = append(c.Queries, unmatched(path+name, ignore.File))
					}
				}
				result = append(result, c)
			}
		}
	}
	return result
}

func TestSemanticGolden(t *testing.T) {
	for _, tc := range semanticCorpus() {
		t.Run(tc.Name, func(t *testing.T) {
			p, err := ignore.Compile(context.Background(), tc.Sources, ignore.Options{Case: tc.Case})
			if err != nil {
				t.Fatal("golden compile rejected", err)
			}
			for i, q := range tc.Queries {
				got, err := p.Evaluate(context.Background(), q.Path, q.Kind)
				if err != nil || got != q.Want {
					t.Errorf("candidate %d: got %+v, want %+v, err=%v", i, got, q.Want, err)
				}
			}
		})
	}
}

func TestSourceActivationUsesExactDirectorySpelling(t *testing.T) {
	for _, mode := range []ignore.CaseMode{ignore.CaseSensitive, ignore.CaseASCIIInsensitive} {
		p, err := ignore.Compile(context.Background(), []ignore.Source{{Path: "Case/.jeleeignore", Text: []byte("FILE\n")}}, ignore.Options{Case: mode})
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Evaluate(context.Background(), "case/FILE", ignore.File)
		if err != nil || got != (ignore.Match{}) {
			t.Fatal("source activation folded directory spelling", got, err)
		}
		got, err = p.Evaluate(context.Background(), "Case/FILE", ignore.File)
		want := decided("Case/FILE", ignore.File, ignore.Exclude, "Case/.jeleeignore", 1).Want
		if err != nil || got != want {
			t.Fatal("exact source not activated", got, err)
		}
	}
}
