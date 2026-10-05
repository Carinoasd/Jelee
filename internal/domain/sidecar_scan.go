package domain

import (
	"path"
	"sort"
	"strings"
)

// SidecarInspectionBatch bounds one page of tracks whose charset and edge
// fingerprint are read after a catalog sync wrote them.
const SidecarInspectionBatch = 64

// SidecarScanFile is one inventory row considered for sidecar pairing. Kind
// is the inventory kind; only "video" rows are videos and only "other" rows
// can be sidecars.
type SidecarScanFile struct {
	Path             string
	Kind             string
	Size             int64
	ModifiedUnixNano int64
}

// SidecarScanMatch is one sidecar file paired with the video it belongs to.
type SidecarScanMatch struct {
	File  SidecarScanFile
	Track SidecarTrack
}

// sidecarDirectoryNames lists every subdirectory name that holds sidecars of
// either kind, compared in ASCII case only. Which kind a folder accepts is
// decided by ParseSidecarName.
var sidecarDirectoryNames = []string{"sub", "subs", "subtitle", "subtitles", "audio", "audios"}

func sidecarDirectoryName(name string) bool {
	for _, candidate := range sidecarDirectoryNames {
		if asciiEqualFold(name, candidate) {
			return true
		}
	}
	return false
}

// asciiEqualFold folds only ASCII letters, so "ſubs" is not "subs" here,
// exactly as in the database owner function.
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// SidecarOwnerDirectory returns the directory a scanned file is paired in:
// its parent folder, or the folder above when the parent is a sidecar folder
// such as "Subs" or "Audio". The root folder is "". It must stay identical to
// the database function inventory_sidecar_owner.
func SidecarOwnerDirectory(relative string) string {
	dir := path.Dir(relative)
	if dir == "." {
		return ""
	}
	if sidecarDirectoryName(path.Base(dir)) {
		dir = path.Dir(dir)
		if dir == "." {
			return ""
		}
	}
	return dir
}

// SidecarLookupDirectories returns the owner directories that hold every file
// a video in directory dir ("" or "." for the root) may pair with: dir itself
// and, when dir is itself named like a sidecar folder, its parent, because
// the video's siblings are then owned by that parent.
func SidecarLookupDirectories(dir string) []string {
	if dir == "." {
		dir = ""
	}
	owners := []string{dir}
	if dir != "" && sidecarDirectoryName(path.Base(dir)) {
		parent := path.Dir(dir)
		if parent == "." {
			parent = ""
		}
		owners = append(owners, parent)
	}
	return owners
}

// PairSidecarFiles pairs the external subtitle and audio files of directory
// dir ("" or "." for the root) with its videos (G15.3, G16.2). files may hold
// unrelated rows of one library root; only videos directly in dir and
// "other" files directly in dir or in one of its sidecar folders are used.
//
// Each sidecar goes to the video with the longest matching base name, so
// "Movie.Part2.en.srt" belongs to "Movie.Part2.mkv" and not "Movie.mkv"; a
// name that matches equal bases of two videos ("Movie.mkv" and "Movie.mp4")
// is ambiguous and paired with neither. A video file is never a sidecar.
// The result maps a video path to its sidecars ordered by path, capped at
// SidecarTracksPerSource.
func PairSidecarFiles(dir string, files []SidecarScanFile) map[string][]SidecarScanMatch {
	if dir == "" {
		dir = "."
	}
	var videos []string
	var bases []string
	for _, f := range files {
		if f.Kind == "video" && path.Dir(f.Path) == dir {
			name := path.Base(f.Path)
			base := strings.TrimSuffix(name, path.Ext(name))
			if base == "" {
				continue
			}
			videos = append(videos, f.Path)
			bases = append(bases, base)
		}
	}
	result := make(map[string][]SidecarScanMatch)
	if len(videos) == 0 {
		return result
	}
	for _, f := range files {
		if f.Kind != "other" {
			continue
		}
		parent, name := path.Dir(f.Path), path.Base(f.Path)
		subdir := ""
		if parent != dir {
			if path.Dir(parent) != dir || !sidecarDirectoryName(path.Base(parent)) {
				continue
			}
			subdir = path.Base(parent)
		}
		index, track, ok := SelectSidecarVideo(bases, name, subdir)
		if !ok {
			continue
		}
		result[videos[index]] = append(result[videos[index]], SidecarScanMatch{File: f, Track: track})
	}
	for video, matches := range result {
		sort.Slice(matches, func(i, j int) bool { return matches[i].File.Path < matches[j].File.Path })
		if len(matches) > SidecarTracksPerSource {
			matches = matches[:SidecarTracksPerSource]
		}
		result[video] = matches
	}
	return result
}

// SidecarInspectionTarget is one stored sidecar whose charset and edge
// fingerprint are still unknown. It carries the absolute library root and is
// redacted from diagnostics.
type SidecarInspectionTarget struct {
	ID, Kind, Format       string
	RootPath, RelativePath string
	Size, ModifiedUnixNano int64
}

func (SidecarInspectionTarget) String() string   { return "sidecar inspection (redacted)" }
func (SidecarInspectionTarget) GoString() string { return "sidecar inspection (redacted)" }

// SidecarInspection is what a bounded read of one sidecar found. Size and
// ModifiedUnixNano are the stamp the read was made against; a row whose
// stamp changed since is left for the next scan. Charset is empty when the
// file is not text or detection had low confidence.
type SidecarInspection struct {
	ID                     string
	Size, ModifiedUnixNano int64
	Charset                string
	Fingerprint            []byte
}

// ValidSidecarInspection mirrors the database checks for a recorded result.
func ValidSidecarInspection(v SidecarInspection) bool {
	return ValidID(v.ID) && v.Size >= 0 && len(v.Fingerprint) == SidecarFingerprintBytes &&
		(v.Charset == "" || validSidecarCharset(v.Charset))
}
