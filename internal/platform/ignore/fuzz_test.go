package ignore

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func fuzzFixedError(err error) bool {
	return errors.Is(err, ErrInvalid) || errors.Is(err, ErrLimit) || errors.Is(err, ErrWorkLimit)
}

func FuzzCompileBoundedText(f *testing.F) {
	for _, text := range [][]byte{
		{}, []byte("*.nfo\n!keep.nfo\n"), []byte("[[:digit:]]\n**/a\n\\\n"),
		[]byte("# comment\r\n\r\n片🎬.mkv\r\n"), {0xff, 0xfe, 0, 0xd8},
		contractUTF16("*.mkv\n", false),
	} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text []byte) {
		// Fuzzing exercises parser combinations; the larger hard boundaries have
		// deterministic tests, so fuzz workers need not allocate multi-MiB inputs.
		if len(text) > 32<<10 {
			t.Skip()
		}
		p, err := Compile(context.Background(), []Source{{Path: ".jeleeignore", Text: text}}, Options{})
		if err != nil {
			if p != nil || !fuzzFixedError(err) {
				t.Fatal("compile error leaked a partial program or an unfixed error")
			}
			return
		}
		if p == nil {
			t.Fatal("successful compile returned nil")
		}
		d := p.Diagnostics()
		if d.Total < len(d.Items) || len(d.Items) > MaxDiagnostics || d.Truncated != (d.Total > len(d.Items)) {
			t.Fatal("inconsistent diagnostic bounds")
		}
		for _, item := range d.Items {
			if item.Code != "invalid_pattern" || item.Source != ".jeleeignore" || item.Line < 1 || item.Line > MaxSourceLines {
				t.Fatal("unsafe diagnostic provenance")
			}
		}
		for _, path := range []string{"movie.nfo", "series/episode.mkv", "片🎬.mkv"} {
			first, firstErr := p.Evaluate(context.Background(), path, File)
			second, secondErr := p.Evaluate(context.Background(), path, File)
			if first != second || !errors.Is(firstErr, secondErr) {
				t.Fatal("immutable program is nondeterministic")
			}
			if firstErr != nil && (!fuzzFixedError(firstErr) || first != (Match{})) {
				t.Fatal("evaluation error returned a partial decision")
			}
		}
		if !reflect.DeepEqual(d, p.Diagnostics()) {
			t.Fatal("evaluation mutated diagnostics")
		}
	})
}

func FuzzEvaluateCanonicalPath(f *testing.F) {
	p := contractCompile(f, "*.nfo\n!keep.nfo\ncache/\n**/poster.?pg\n/[a-z]*.mkv\n")
	for _, path := range []string{"movie.nfo", "keep.nfo", "cache/movie.mkv", "series/poster.jpg", "片🎬.mkv", "a/../b", "a\\b", "\x00", ""} {
		f.Add(path, byte(File))
	}
	f.Add("cache", byte(Directory))
	f.Add("file", byte(0))
	f.Fuzz(func(t *testing.T, path string, kind byte) {
		if len(path) > 2*MaxPathBytes {
			t.Skip()
		}
		got, err := p.Evaluate(context.Background(), path, Kind(kind))
		if err != nil {
			if !fuzzFixedError(err) || got != (Match{}) {
				t.Fatal("invalid input returned a partial decision or an unfixed error")
			}
			return
		}
		if got.Outcome > Exclude {
			t.Fatal("unknown outcome")
		}
		if got.Outcome == Unmatched {
			if got != (Match{}) {
				t.Fatal("unmatched input has invented provenance")
			}
		} else if got.Source != ".jeleeignore" || got.Line < 1 || got.Line > 5 || got.MatchedPath == "" {
			t.Fatal("matched input has invalid provenance")
		}
		again, againErr := p.Evaluate(context.Background(), path, Kind(kind))
		if again != got || againErr != nil {
			t.Fatal("evaluation changed across identical calls")
		}
	})
}
