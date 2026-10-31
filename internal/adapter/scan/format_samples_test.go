//go:build linux || windows

package scan

import (
	"context"
	"crypto/sha256"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/formatsamples"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Every sample format is recognised by name next to the video and in the
// conventional subdirectory, with its language, and is paired with the right
// video (G15.1, G15.3, G16.1, G16.2; W10-14).
func TestFormatSamplesNamesAndPairing(t *testing.T) {
	type named struct {
		name, kind, format, language, title string
	}
	var samples []named
	for _, s := range formatsamples.Subtitles() {
		samples = append(samples, named{s.Name("Movie (2001)"), domain.SidecarKindSubtitle, s.Format, s.Language, strings.TrimSuffix(s.File, path.Ext(s.File))})
	}
	for _, a := range formatsamples.AudioSpecs() {
		samples = append(samples, named{"Movie (2001)." + a.File, domain.SidecarKindAudio, a.Format, "", ""})
	}
	// Formats without a sample still name a track.
	for format := range formatsamples.MissingAudio {
		samples = append(samples, named{"Movie (2001)." + format, domain.SidecarKindAudio, format, "", ""})
	}
	for _, s := range samples {
		for _, dir := range []string{"", "Subs", "Audio"} {
			track, ok := domain.ParseSidecarName("Movie (2001)", s.name, dir)
			wantDir := map[string]string{domain.SidecarKindSubtitle: "Subs", domain.SidecarKindAudio: "Audio"}[s.kind]
			if (dir == "" || dir == wantDir) != ok {
				t.Errorf("%s in %q: accepted=%v", s.name, dir, ok)
				continue
			}
			if ok && s.title != "" && track.Title != s.title {
				t.Errorf("%s: title %q", s.name, track.Title)
			}
			if ok && (track.Kind != s.kind || track.Format != s.format || track.Language != s.language) {
				t.Errorf("%s in %q: %+v", s.name, dir, track)
			}
		}
	}

	// A season folder with two episodes: every file goes to its own video,
	// including "Part" names that would also match the shorter base.
	var files []domain.SidecarScanFile
	files = append(files, domain.SidecarScanFile{Path: "Show/Movie (2001).mkv", Kind: "video"}, domain.SidecarScanFile{Path: "Show/Movie (2001).Part2.mkv", Kind: "video"})
	for _, s := range samples {
		files = append(files, domain.SidecarScanFile{Path: "Show/" + s.name, Kind: "other"})
	}
	files = append(files, domain.SidecarScanFile{Path: "Show/Movie (2001).Part2.en.srt", Kind: "other"})
	matches := domain.PairSidecarFiles("Show", files)
	if len(matches["Show/Movie (2001).Part2.mkv"]) != 1 {
		t.Fatalf("part two sidecars: %+v", matches["Show/Movie (2001).Part2.mkv"])
	}
	var paired []string
	for _, m := range matches["Show/Movie (2001).mkv"] {
		paired = append(paired, path.Base(m.File.Path))
	}
	for _, s := range samples {
		if !slices.Contains(paired, s.name) && len(paired) < domain.SidecarTracksPerSource {
			t.Errorf("%s was not paired with its video", s.name)
		}
	}
}

// The scan's inspection finds the right character set and ".sub" kind of
// every subtitle sample, never alters the file, and reads audio samples only
// for their fingerprint.
func TestFormatSamplesInspection(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	for _, s := range formatsamples.Subtitles() {
		relative := "Movie/" + s.Name("Movie")
		writeInspectFile(t, root, relative, s.Data)
		before := sha256.Sum256(s.Data)
		got, err := SidecarInspector{}.InspectSidecar(ctx, inspectTarget(t, root, relative, domain.SidecarKindSubtitle, s.Format))
		if err != nil {
			t.Errorf("%s: %v", s.File, err)
			continue
		}
		if got.Charset != s.Charset || got.SubtitleKind != s.SubtitleKind || len(got.Fingerprint) != domain.SidecarFingerprintBytes || !domain.ValidSidecarInspection(got) {
			t.Errorf("%s: charset %q kind %q (want %q %q)", s.File, got.Charset, got.SubtitleKind, s.Charset, s.SubtitleKind)
		}
		if after, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative))); err != nil || sha256.Sum256(after) != before {
			t.Errorf("%s: inspection changed the original", s.File)
		}
		// Only ".sub" content is classified; the kind comes from the bytes
		// whatever the charset detector says about them.
		if s.Format != "sub" && got.SubtitleKind != "" {
			t.Errorf("%s: content kind %q for a %s file", s.File, got.SubtitleKind, s.Format)
		}
	}
	for _, a := range formatsamples.AudioSpecs() {
		if !a.Handmade {
			continue // encoded samples need ffmpeg; their inspection is the same edge fingerprint
		}
		relative := "Movie/Movie." + a.File
		writeInspectFile(t, root, relative, formatsamples.Mp3())
		got, err := SidecarInspector{}.InspectSidecar(ctx, inspectTarget(t, root, relative, domain.SidecarKindAudio, a.Format))
		if err != nil || got.Charset != "" || got.SubtitleKind != "" || len(got.Fingerprint) != domain.SidecarFingerprintBytes {
			t.Errorf("%s: %v %+v", a.File, err, got)
		}
	}
	// Every charset the database accepts has a sample.
	seen := map[string]bool{}
	for _, s := range formatsamples.Subtitles() {
		seen[s.Charset] = true
	}
	for _, charset := range domain.SidecarCharsets {
		if !seen[charset] {
			t.Errorf("no sample for charset %s", charset)
		}
	}
	// A sample can be told from the next one by name alone.
	names := map[string]bool{}
	for _, s := range formatsamples.Subtitles() {
		if key := strings.ToLower(s.Name("v")); names[key] {
			t.Errorf("duplicate sample name %s", key)
		} else {
			names[key] = true
		}
	}
}
