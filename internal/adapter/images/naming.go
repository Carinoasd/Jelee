package images

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ImageScope is the catalog level whose directory is being recognized. It
// decides which same-directory naming conventions apply (G40.2).
type ImageScope string

const (
	ImageScopeMovie   ImageScope = "movie"
	ImageScopeSeries  ImageScope = "series"
	ImageScopeSeason  ImageScope = "season"
	ImageScopeEpisode ImageScope = "episode"
)

// Image roles follow the G40.1 type names. Mapping to API image types is the
// caller's decision.
const (
	ImageTypePrimary   = "Primary"
	ImageTypeBackdrop  = "Backdrop"
	ImageTypeLogo      = "Logo"
	ImageTypeClearLogo = "ClearLogo"
	ImageTypeBanner    = "Banner"
	ImageTypeClearArt  = "ClearArt"
	ImageTypeLandscape = "Landscape"
	ImageTypeThumb     = "Thumb"
	ImageTypeDisc      = "Disc"
)

const (
	// ImageSeasonNone marks an image that is not bound to a season, or a
	// season directory whose number the caller does not know.
	ImageSeasonNone = -1
	// ImageSeasonAll marks season-all-* images that apply to every season.
	ImageSeasonAll = -2

	imageNameMaxBytes  = 255
	imageNameMaxNumber = 9999
)

// imageNameExtensions is also the extension priority within one alias.
var imageNameExtensions = [...]string{"jpg", "jpeg", "png", "webp", "avif", "gif", "bmp", "tiff"}

// ImageNamingInput describes the directory being recognized. VideoBase is the
// video file name without its extension; it is optional for movies, required
// for episodes and must be empty for series and season directories. Season is
// only read for ImageScopeSeason.
type ImageNamingInput struct {
	Scope     ImageScope
	VideoBase string
	Season    int
}

// ImageSlot identifies one image role. Season is ImageSeasonNone unless the
// image belongs to a season; Index is non-zero only for extra backdrops.
type ImageSlot struct {
	Type   string
	Scope  ImageScope
	Season int
	Index  int
}

// NamedImage is the selected directory entry for a slot. Name is the entry
// name exactly as listed.
type NamedImage struct {
	ImageSlot
	Name string
}

// ImageNaming lists selected images and the slots rejected because one of
// their candidates appeared more than once with different letter case.
type ImageNaming struct {
	Images    []NamedImage
	Conflicts []ImageSlot
}

type imageNameRule struct {
	typ  string
	rank int
}

// Fixed keywords per scope. Within one type a lower rank wins; more specific
// names rank first, matching the existing <base>-poster over poster order.
var imageNameRules = map[ImageScope]map[string]imageNameRule{
	ImageScopeMovie: {
		// rank 0 is <base>-poster.
		"poster": {ImageTypePrimary, 1}, "folder": {ImageTypePrimary, 2}, "cover": {ImageTypePrimary, 3}, "movie": {ImageTypePrimary, 4},
		"backdrop": {ImageTypeBackdrop, 0}, "fanart": {ImageTypeBackdrop, 1},
		"logo": {ImageTypeLogo, 0}, "clearlogo": {ImageTypeClearLogo, 0}, "banner": {ImageTypeBanner, 0}, "clearart": {ImageTypeClearArt, 0},
		"landscape": {ImageTypeLandscape, 0}, "thumb": {ImageTypeThumb, 0},
		"disc": {ImageTypeDisc, 0}, "discart": {ImageTypeDisc, 1},
	},
	ImageScopeSeries: {
		"series-poster": {ImageTypePrimary, 0}, "poster": {ImageTypePrimary, 1}, "folder": {ImageTypePrimary, 2}, "cover": {ImageTypePrimary, 3},
		"backdrop": {ImageTypeBackdrop, 0}, "fanart": {ImageTypeBackdrop, 1},
		"logo": {ImageTypeLogo, 0}, "clearlogo": {ImageTypeClearLogo, 0}, "banner": {ImageTypeBanner, 0}, "clearart": {ImageTypeClearArt, 0},
		"landscape": {ImageTypeLandscape, 0}, "thumb": {ImageTypeThumb, 0},
	},
	ImageScopeSeason: {
		"poster": {ImageTypePrimary, 0}, "folder": {ImageTypePrimary, 1}, "cover": {ImageTypePrimary, 2},
		"backdrop": {ImageTypeBackdrop, 0}, "fanart": {ImageTypeBackdrop, 1},
		"banner": {ImageTypeBanner, 0}, "landscape": {ImageTypeLandscape, 0}, "thumb": {ImageTypeThumb, 0},
	},
}

// Suffixes of season-scoped names found in a series directory.
var imageSeasonSuffixes = map[string]string{
	"poster": ImageTypePrimary, "thumb": ImageTypeThumb, "fanart": ImageTypeBackdrop, "banner": ImageTypeBanner, "landscape": ImageTypeLandscape,
}

type imageNameCandidates struct {
	best     string
	priority int
	seen     map[int]string
	conflict bool
}

// RecognizeImageNames maps one complete directory listing to image slots. It
// is pure: it reads no files and trusts no name. Invalid, unsupported or
// unrecognized names are ignored. Bounds match listImageCandidates and fail
// the whole listing rather than using a partial one.
//
// Keywords match ASCII case-insensitively; VideoBase uses strings.EqualFold.
// Different aliases or extensions for one slot resolve by fixed priority
// (alias rank, then imageNameExtensions order). The same alias and extension
// appearing more than once in different case rejects the whole slot, even if
// it would not have been selected.
func RecognizeImageNames(names []string, input ImageNamingInput) (ImageNaming, error) {
	if !validImageNamingInput(input) {
		return ImageNaming{}, domain.ErrInvalid
	}
	if len(names) > imageDirectoryEntries {
		return ImageNaming{}, domain.ErrImageTooLarge
	}
	nameBytes := 0
	for _, name := range names {
		nameBytes += len(name)
		if nameBytes > imageDirectoryNameBytes {
			return ImageNaming{}, domain.ErrImageTooLarge
		}
	}
	slots := map[ImageSlot]*imageNameCandidates{}
	for _, name := range names {
		stem, extension, ok := splitImageName(name)
		if !ok {
			continue
		}
		slot, rank, ok := matchImageStem(stem, input)
		if !ok {
			continue
		}
		priority := rank*len(imageNameExtensions) + extension
		state := slots[slot]
		if state == nil {
			state = &imageNameCandidates{priority: priority, best: name, seen: map[int]string{}}
			slots[slot] = state
		}
		if _, exists := state.seen[priority]; exists {
			state.conflict = true
			continue
		}
		state.seen[priority] = name
		if priority < state.priority {
			state.priority, state.best = priority, name
		}
	}
	var result ImageNaming
	for slot, state := range slots {
		if state.conflict {
			result.Conflicts = append(result.Conflicts, slot)
		} else {
			result.Images = append(result.Images, NamedImage{ImageSlot: slot, Name: state.best})
		}
	}
	slices.SortFunc(result.Images, func(a, b NamedImage) int { return compareImageSlots(a.ImageSlot, b.ImageSlot) })
	slices.SortFunc(result.Conflicts, compareImageSlots)
	return result, nil
}

// imageNameCandidate is a cheap listing filter: a safe entry name with a
// supported image extension. Recognition still decides the role.
func imageNameCandidate(name string) bool {
	_, _, ok := splitImageName(name)
	return ok
}

func validImageNamingInput(input ImageNamingInput) bool {
	if input.VideoBase != "" && !validImageEntryName(input.VideoBase) {
		return false
	}
	switch input.Scope {
	case ImageScopeMovie:
		return true
	case ImageScopeEpisode:
		return input.VideoBase != ""
	case ImageScopeSeries:
		return input.VideoBase == ""
	case ImageScopeSeason:
		return input.VideoBase == "" && (input.Season == ImageSeasonNone || input.Season >= 0 && input.Season <= imageNameMaxNumber)
	}
	return false
}

// validImageEntryName accepts only a single, printable path element.
func validImageEntryName(name string) bool {
	return name != "" && len(name) <= imageNameMaxBytes && name != "." && name != ".." && utf8.ValidString(name) &&
		!strings.ContainsAny(name, "/\\:") && !strings.ContainsFunc(name, unicode.IsControl)
}

// splitImageName returns the stem and extension priority of a safe name.
func splitImageName(name string) (string, int, bool) {
	if !validImageEntryName(name) {
		return "", 0, false
	}
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 {
		return "", 0, false
	}
	extension := asciiLowerImageName(name[dot+1:])
	for i, supported := range imageNameExtensions {
		if extension == supported {
			return name[:dot], i, true
		}
	}
	return "", 0, false
}

func matchImageStem(stem string, input ImageNamingInput) (ImageSlot, int, bool) {
	lower := asciiLowerImageName(stem)
	plain := ImageSlot{Scope: input.Scope, Season: ImageSeasonNone}
	switch input.Scope {
	case ImageScopeEpisode:
		for rank, suffix := range [...]string{"-thumb", ".episode-thumb"} {
			if imageStemHasBase(stem, lower, suffix, input.VideoBase) {
				plain.Type = ImageTypeThumb
				return plain, rank, true
			}
		}
		return ImageSlot{}, 0, false
	case ImageScopeMovie:
		if input.VideoBase != "" && imageStemHasBase(stem, lower, "-poster", input.VideoBase) {
			plain.Type = ImageTypePrimary
			return plain, 0, true
		}
	case ImageScopeSeries:
		if slot, rank, ok := matchImageSeasonStem(lower); ok {
			return slot, rank, true
		}
	case ImageScopeSeason:
		plain.Season = input.Season
	}
	if rule, ok := imageNameRules[input.Scope][lower]; ok {
		plain.Type = rule.typ
		return plain, rule.rank, true
	}
	if digits, ok := strings.CutPrefix(lower, "fanart"); ok {
		if index, ok := parseImageNameNumber(digits, 1); ok && index > 0 {
			plain.Type, plain.Index = ImageTypeBackdrop, index
			return plain, 0, true
		}
	}
	return ImageSlot{}, 0, false
}

// matchImageSeasonStem recognizes seasonNN-*, season-specials-* (season 0)
// and season-all-* in a series directory.
func matchImageSeasonStem(lower string) (ImageSlot, int, bool) {
	rest, ok := strings.CutPrefix(lower, "season")
	if !ok {
		return ImageSlot{}, 0, false
	}
	season, rank := 0, 0
	if suffix, ok := strings.CutPrefix(rest, "-specials-"); ok {
		rest, rank = suffix, 1
	} else if suffix, ok := strings.CutPrefix(rest, "-all-"); ok {
		rest, season = suffix, ImageSeasonAll
	} else {
		digits, suffix, ok := strings.Cut(rest, "-")
		if !ok {
			return ImageSlot{}, 0, false
		}
		if season, ok = parseImageNameNumber(digits, 2); !ok {
			return ImageSlot{}, 0, false
		}
		rest = suffix
	}
	typ, ok := imageSeasonSuffixes[rest]
	if !ok {
		return ImageSlot{}, 0, false
	}
	return ImageSlot{Type: typ, Scope: ImageScopeSeason, Season: season}, rank, true
}

// parseImageNameNumber accepts exactly one spelling per number: at least
// width digits, zero padded to width, with no other leading zero, so two
// names never denote the same slot by spelling alone.
func parseImageNameNumber(digits string, width int) (int, bool) {
	if len(digits) < width || len(digits) > 4 || len(digits) > width && digits[0] == '0' {
		return 0, false
	}
	value := 0
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
		value = value*10 + int(digits[i]-'0')
	}
	return value, value <= imageNameMaxNumber
}

func imageStemHasBase(stem, lower, suffix, base string) bool {
	return len(stem) > len(suffix) && strings.HasSuffix(lower, suffix) && strings.EqualFold(stem[:len(stem)-len(suffix)], base)
}

// asciiLowerImageName folds ASCII letters only and keeps byte offsets, so
// non-ASCII look-alikes never match a keyword.
func asciiLowerImageName(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, text)
}

var imageScopeOrder = map[ImageScope]int{ImageScopeMovie: 0, ImageScopeSeries: 1, ImageScopeSeason: 2, ImageScopeEpisode: 3}

func compareImageSlots(a, b ImageSlot) int {
	return cmp.Or(cmp.Compare(imageScopeOrder[a.Scope], imageScopeOrder[b.Scope]), cmp.Compare(a.Season, b.Season), strings.Compare(a.Type, b.Type), cmp.Compare(a.Index, b.Index))
}
