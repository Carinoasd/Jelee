package postgres

import (
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Planning of the G40.2/G40.3 scan ingestion. Everything here is pure: the
// caller reads the catalog and the published baseline, the planner decides
// which image rows each item should have, and the caller writes them.

// ingestItem is one catalog item of an ingestion page.
type ingestItem struct {
	id, kind string
	// parent is the series of a season.
	parent string
	// season is the season number of a Season item, when known.
	season *int
	// dir is the registered folder of a series or season.
	dir *ingestDir
	// sources are the media files of a movie or episode, ordered.
	sources []ingestSource
}

type ingestSource struct{ id, root, path string }

// ingestDir is a folder of one root; the root folder is "".
type ingestDir struct{ root, dir string }

type ingestFile struct {
	path           string
	size, modified int64
}

// ingestListing is the image files directly inside one folder of the
// published baseline.
type ingestListing struct {
	files []ingestFile
}

// ingestArt is one item's NFO art fact with the folder of its NFO file
// (nil when the NFO source is no longer known).
type ingestArt struct {
	entries []domain.NFOArtwork
	dir     *ingestDir
}

type ingestSlot struct {
	typ   string
	index int
	kind  string
}

// ingestWant is the planned row set of one item. complete reports whether
// every folder the item reads could be listed; protected slots had a
// letter-case conflict. Rows of an incomplete kind or a protected slot are
// kept even when the plan does not name them.
type ingestWant struct {
	inputs        map[ingestSlot]domain.ItemImageInput
	localComplete bool
	protected     map[ingestSlot]bool
}

// ingestInputs are the read facts a page plans from.
type ingestInputs struct {
	items    []ingestItem
	parents  map[string]*ingestDir // series folder by series ID
	listings map[ingestDir]ingestListing
	videos   map[ingestDir]int        // distinct videos (items) per movie folder
	art      map[string]ingestArt     // NFO art by item ID
	nfoFiles map[ingestDir]ingestFile // image files NFO art points at, keyed by root and path
}

func parentDirectory(relative string) string {
	dir := path.Dir(relative)
	if dir == "." {
		return ""
	}
	return dir
}

func registeredDirectory(relative string) string {
	if relative == "." {
		return ""
	}
	return relative
}

func videoBase(relative string) string {
	name := path.Base(relative)
	return strings.TrimSuffix(name, path.Ext(name))
}

// movieKind reports whether an item kind uses the movie naming rules.
func movieKind(kind string) bool { return kind == "Movie" || kind == "HomeVideo" }

// ingestDirectories lists the folders a page must read.
func ingestDirectories(items []ingestItem, parents map[string]*ingestDir) (all []ingestDir, movies []ingestDir) {
	seen, seenMovie := map[ingestDir]bool{}, map[ingestDir]bool{}
	add := func(d ingestDir) {
		if !seen[d] {
			seen[d] = true
			all = append(all, d)
		}
	}
	for _, item := range items {
		switch {
		case movieKind(item.kind) || item.kind == "Episode":
			for _, s := range item.sources {
				d := ingestDir{s.root, parentDirectory(s.path)}
				add(d)
				if movieKind(item.kind) && !seenMovie[d] {
					seenMovie[d] = true
					movies = append(movies, d)
				}
			}
		case item.kind == "Series" || item.kind == "Season":
			if item.dir != nil {
				add(*item.dir)
			}
			if item.kind == "Season" && parents[item.parent] != nil {
				add(*parents[item.parent])
			}
		}
	}
	return all, movies
}

func (w *ingestWant) add(slot ingestSlot, in domain.ItemImageInput) {
	if _, taken := w.inputs[slot]; taken || !domain.ValidItemImageInput(in) {
		return
	}
	w.inputs[slot] = in
}

// recognize lists one folder for an item. It returns nil and marks the
// item's local view incomplete when the folder cannot be used as a whole.
func (w *ingestWant) recognize(in ingestInputs, d ingestDir, naming images.ImageNamingInput, filter func(string) bool) []images.NamedImage {
	listing := in.listings[d]
	names := make([]string, 0, len(listing.files))
	for _, f := range listing.files {
		name := path.Base(f.path)
		if filter == nil || filter(name) {
			names = append(names, name)
		}
	}
	result, err := images.RecognizeImageNames(names, naming)
	if errors.Is(err, domain.ErrInvalid) && naming.Scope == images.ImageScopeMovie && naming.VideoBase != "" {
		// A video name the matcher cannot take still leaves the folder
		// names usable.
		naming.VideoBase = ""
		result, err = images.RecognizeImageNames(names, naming)
	}
	if err != nil {
		w.localComplete = false
		return nil
	}
	for _, slot := range result.Conflicts {
		w.protected[ingestSlot{slot.Type, slot.Index, domain.ImageSourceLocal}] = true
	}
	return result.Images
}

func (w *ingestWant) addLocal(in ingestInputs, item string, d ingestDir, image images.NamedImage) {
	for _, f := range in.listings[d].files {
		if path.Base(f.path) != image.Name {
			continue
		}
		size, modified := f.size, f.modified
		w.add(ingestSlot{image.Type, image.Index, domain.ImageSourceLocal}, domain.ItemImageInput{ItemID: item, Type: image.Type, Index: image.Index,
			SourceKind: domain.ImageSourceLocal, RootID: d.root, RelativePath: f.path, SourceModifiedUnixNano: &modified, SourceSize: &size})
		return
	}
}

// planItemImages decides the local, NFO and remote rows of every page item.
func planItemImages(in ingestInputs) map[string]*ingestWant {
	out := make(map[string]*ingestWant, len(in.items))
	for _, item := range in.items {
		w := &ingestWant{inputs: map[ingestSlot]domain.ItemImageInput{}, localComplete: true, protected: map[ingestSlot]bool{}}
		out[item.id] = w
		w.planLocal(in, item)
		w.planArt(in, item)
	}
	return out
}

func (w *ingestWant) planLocal(in ingestInputs, item ingestItem) {
	switch {
	case movieKind(item.kind):
		for _, s := range item.sources {
			d := ingestDir{s.root, parentDirectory(s.path)}
			base := videoBase(s.path)
			var filter func(string) bool
			// Shared names such as poster.jpg describe a folder, so they
			// are only taken when the folder holds this one movie; with
			// several, only <video>-poster names count.
			if in.videos[d] > 1 {
				filter = func(name string) bool {
					return len(name) > len(base)+1 && name[len(base)] == '-' && strings.EqualFold(name[:len(base)], base)
				}
			}
			for _, image := range w.recognize(in, d, images.ImageNamingInput{Scope: images.ImageScopeMovie, VideoBase: base}, filter) {
				w.addLocal(in, item.id, d, image)
			}
		}
	case item.kind == "Episode":
		for _, s := range item.sources {
			d := ingestDir{s.root, parentDirectory(s.path)}
			for _, image := range w.recognize(in, d, images.ImageNamingInput{Scope: images.ImageScopeEpisode, VideoBase: videoBase(s.path)}, nil) {
				w.addLocal(in, item.id, d, image)
			}
		}
	case item.kind == "Series":
		if item.dir == nil {
			return
		}
		for _, image := range w.recognize(in, *item.dir, images.ImageNamingInput{Scope: images.ImageScopeSeries}, nil) {
			if image.Scope == images.ImageScopeSeries {
				w.addLocal(in, item.id, *item.dir, image)
			}
		}
	case item.kind == "Season":
		number := images.ImageSeasonNone
		if item.season != nil && *item.season >= 0 && *item.season <= domain.ItemImageMaxIndex {
			number = *item.season
		}
		// The season folder's own names come first, then seasonNN-* of the
		// series folder, then season-all-* which applies to every season.
		if item.dir != nil {
			for _, image := range w.recognize(in, *item.dir, images.ImageNamingInput{Scope: images.ImageScopeSeason, Season: number}, nil) {
				w.addLocal(in, item.id, *item.dir, image)
			}
		}
		series := in.parents[item.parent]
		if series == nil {
			return
		}
		named := w.recognize(in, *series, images.ImageNamingInput{Scope: images.ImageScopeSeries}, nil)
		for _, wanted := range []int{number, images.ImageSeasonAll} {
			if wanted == images.ImageSeasonNone {
				continue
			}
			for _, image := range named {
				if image.Scope == images.ImageScopeSeason && image.Season == wanted {
					w.addLocal(in, item.id, *series, image)
				}
			}
		}
	}
}

// nfoArtType maps an NFO art kind to an image type. Episode NFO <thumb>
// (stored as poster) is the episode still, kept in the Thumb slot like the
// <video>-thumb file.
func nfoArtType(kind, itemKind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if itemKind == "Episode" && (kind == "poster" || kind == "thumb") {
		return "Thumb"
	}
	switch kind {
	case "poster":
		return "Primary"
	case "fanart", "backdrop":
		return "Backdrop"
	case "banner":
		return "Banner"
	case "clearart":
		return "ClearArt"
	case "clearlogo":
		return "ClearLogo"
	case "logo":
		return "Logo"
	case "landscape":
		return "Landscape"
	case "thumb":
		return "Thumb"
	case "disc", "discart":
		return "Disc"
	}
	return ""
}

// artEntries returns the NFO art that describes an item: its own NFO and,
// for a season, the season-bound entries of the series NFO. Series entries
// bound to a season belong to that season only.
func artEntries(in ingestInputs, item ingestItem) []ingestArtEntry {
	var out []ingestArtEntry
	own := in.art[item.id]
	for _, entry := range own.entries {
		if item.kind == "Series" && entry.Season != nil {
			continue
		}
		out = append(out, ingestArtEntry{entry, own.dir})
	}
	if item.kind == "Season" && item.season != nil && item.parent != "" {
		series := in.art[item.parent]
		for _, entry := range series.entries {
			if entry.Season != nil && *entry.Season == *item.season {
				out = append(out, ingestArtEntry{entry, series.dir})
			}
		}
	}
	return out
}

type ingestArtEntry struct {
	art domain.NFOArtwork
	dir *ingestDir
}

// nfoReference resolves an NFO art location. A URL is remote (never fetched
// here); anything else is a path relative to the NFO folder that must stay
// inside the root. ok is false for an unusable reference.
func nfoReference(location string, dir *ingestDir) (remote string, local ingestDir, ok bool) {
	location = strings.TrimSpace(location)
	lower := strings.ToLower(location)
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		return location, ingestDir{}, domain.ValidItemImageRemoteURL(location)
	}
	if dir == nil || location == "" || strings.Contains(location, "://") {
		return "", ingestDir{}, false
	}
	decoded, err := url.PathUnescape(location)
	if err != nil {
		return "", ingestDir{}, false
	}
	decoded = strings.ReplaceAll(decoded, "\\", "/")
	if path.IsAbs(decoded) || strings.Contains(decoded, ":") {
		return "", ingestDir{}, false
	}
	joined := path.Join(dir.dir, decoded)
	if joined == ".." || strings.HasPrefix(joined, "../") || !domain.ValidItemImageRelativePath(joined) || !domain.ValidItemImageFileName(joined) {
		return "", ingestDir{}, false
	}
	return "", ingestDir{dir.root, joined}, true
}

// nfoReferences lists the root files the page's NFO art points at, so the
// caller can look them up in the baseline with one statement.
func nfoReferences(in ingestInputs) []ingestDir {
	seen := map[ingestDir]bool{}
	var out []ingestDir
	for _, item := range in.items {
		for _, entry := range artEntries(in, item) {
			if remote, local, ok := nfoReference(entry.art.Location, entry.dir); ok && remote == "" && !seen[local] {
				seen[local] = true
				out = append(out, local)
			}
		}
	}
	return out
}

func (w *ingestWant) planArt(in ingestInputs, item ingestItem) {
	backdrops := 0
	for _, entry := range artEntries(in, item) {
		typ := nfoArtType(entry.art.Kind, item.kind)
		if typ == "" {
			continue
		}
		input := domain.ItemImageInput{ItemID: item.id, Type: typ}
		remote, local, ok := nfoReference(entry.art.Location, entry.dir)
		switch {
		case !ok:
			continue
		case remote != "":
			input.SourceKind, input.RemoteURL = domain.ImageSourceRemote, remote
		default:
			// A reference to a file the scan did not see is skipped; the
			// next scan adds it once the file exists.
			f, found := in.nfoFiles[local]
			if !found {
				continue
			}
			size, modified := f.size, f.modified
			input.SourceKind, input.RootID, input.RelativePath = domain.ImageSourceNFO, local.root, local.dir
			input.SourceModifiedUnixNano, input.SourceSize = &modified, &size
		}
		if typ == "Backdrop" {
			// Usable fanart keeps its NFO order across files and URLs.
			if backdrops > domain.ItemImageMaxIndex {
				continue
			}
			input.Index = backdrops
			backdrops++
		}
		w.add(ingestSlot{typ, input.Index, input.SourceKind}, input)
	}
}

// decodeArtFact reads a stored art fact value; an unreadable value yields
// no entries.
func decodeArtFact(raw []byte) []domain.NFOArtwork {
	var entries []domain.NFOArtwork
	if json.Unmarshal(raw, &entries) != nil || !domain.ValidMetadataArtwork(entries) {
		return nil
	}
	return entries
}
