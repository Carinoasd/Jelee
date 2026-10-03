package subtitles

import (
	"errors"
	"testing"
)

func TestDerivedUTF8Key(t *testing.T) {
	base := DerivedSource{LibraryID: "lib-1", RelPath: "Movies/A (2020)/A.zh.srt", Size: 4096, ModTimeUnixNS: 1700000000123456789}
	key, err := DerivedUTF8Key(base, GB18030)
	if err != nil || len(key) != 64 {
		t.Fatalf("key %q %v", key, err)
	}
	again, _ := DerivedUTF8Key(base, GB18030)
	if again != key {
		t.Fatal("key is not deterministic")
	}
	variants := map[string]func(*DerivedSource) Charset{
		"library": func(s *DerivedSource) Charset { s.LibraryID = "lib-2"; return GB18030 },
		"path":    func(s *DerivedSource) Charset { s.RelPath = "Movies/A (2020)/A.zh-Hans.srt"; return GB18030 },
		"size":    func(s *DerivedSource) Charset { s.Size++; return GB18030 },
		"mtime":   func(s *DerivedSource) Charset { s.ModTimeUnixNS++; return GB18030 },
		"digest":  func(s *DerivedSource) Charset { s.Digest = "sha256:00"; return GB18030 },
		"charset": func(s *DerivedSource) Charset { return Big5 },
		// Length prefixes keep field boundaries apart.
		"boundary": func(s *DerivedSource) Charset {
			s.LibraryID, s.RelPath = "lib-1Movies/", "A (2020)/A.zh.srt"
			return GB18030
		},
	}
	seen := map[string]string{key: "base"}
	for name, change := range variants {
		src := base
		cs := change(&src)
		k, err := DerivedUTF8Key(src, cs)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if prev, dup := seen[k]; dup {
			t.Errorf("%s collides with %s", name, prev)
		}
		seen[k] = name
	}
	for _, bad := range []DerivedSource{
		{RelPath: ""}, {RelPath: "/abs/a.srt"}, {RelPath: "../a.srt"}, {RelPath: ".."}, {RelPath: "a/../b.srt"},
		{RelPath: "a//b.srt"}, {RelPath: `a\b.srt`}, {RelPath: "a\x00.srt"}, {RelPath: "a\xff.srt"},
		{RelPath: "a.srt", Size: -1},
	} {
		if _, err := DerivedUTF8Key(bad, UTF8); !errors.Is(err, ErrInvalidDerivedSource) {
			t.Errorf("%q accepted: %v", bad.RelPath, err)
		}
	}
	if _, err := DerivedUTF8Key(base, CharsetUnknown); !errors.Is(err, ErrUnsupportedCharset) {
		t.Errorf("unknown charset: %v", err)
	}
}
