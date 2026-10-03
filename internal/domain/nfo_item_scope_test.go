package domain

import "testing"

func TestAdjacentNFOPathRejectsUnsafeAndAmbiguousNames(t *testing.T) {
	for _, name := range []string{"", ".", "../film.mp4", "/film.mp4", "a/../film.mp4", "a//film.mp4", `a\film.mp4`, "C:film.mp4", "a/film", "film.nfo", "film.NFO", "film.", ".mp4", "film\x00.mp4", "film\n.mp4"} {
		if _, ok := AdjacentNFOPath(name); ok {
			t.Fatalf("unsafe name accepted: %q", name)
		}
	}
	for name, want := range map[string]string{"電影.mp4": "電影.nfo", "dir/Film.part1.MKV": "dir/Film.part1.nfo"} {
		if actual, ok := AdjacentNFOPath(name); !ok || actual != want {
			t.Fatal("adjacent name differs")
		}
	}
}
