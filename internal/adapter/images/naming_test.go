package images

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func namedSlot(typ string, scope ImageScope, season, index int) ImageSlot {
	return ImageSlot{Type: typ, Scope: scope, Season: season, Index: index}
}

func TestRecognizeImageNamesGolden(t *testing.T) {
	movie := ImageNamingInput{Scope: ImageScopeMovie, VideoBase: "Film (2020)"}
	series := ImageNamingInput{Scope: ImageScopeSeries}
	season := ImageNamingInput{Scope: ImageScopeSeason, Season: 2}
	episode := ImageNamingInput{Scope: ImageScopeEpisode, VideoBase: "Show S01E02"}
	none := ImageSeasonNone
	for _, test := range []struct {
		name  string
		input ImageNamingInput
		slot  ImageSlot
	}{
		{"Film (2020)-poster.jpg", movie, namedSlot(ImageTypePrimary, ImageScopeMovie, none, 0)},
		{"film (2020)-POSTER.Png", movie, namedSlot(ImageTypePrimary, ImageScopeMovie, none, 0)},
		{"poster.jpg", movie, namedSlot(ImageTypePrimary, ImageScopeMovie, none, 0)},
		{"folder.jpeg", movie, namedSlot(ImageTypePrimary, ImageScopeMovie, none, 0)},
		{"cover.png", movie, namedSlot(ImageTypePrimary, ImageScopeMovie, none, 0)},
		{"movie.webp", movie, namedSlot(ImageTypePrimary, ImageScopeMovie, none, 0)},
		{"backdrop.avif", movie, namedSlot(ImageTypeBackdrop, ImageScopeMovie, none, 0)},
		{"fanart.gif", movie, namedSlot(ImageTypeBackdrop, ImageScopeMovie, none, 0)},
		{"fanart1.bmp", movie, namedSlot(ImageTypeBackdrop, ImageScopeMovie, none, 1)},
		{"FANART12.TIFF", movie, namedSlot(ImageTypeBackdrop, ImageScopeMovie, none, 12)},
		{"logo.png", movie, namedSlot(ImageTypeLogo, ImageScopeMovie, none, 0)},
		{"clearlogo.png", movie, namedSlot(ImageTypeClearLogo, ImageScopeMovie, none, 0)},
		{"banner.jpg", movie, namedSlot(ImageTypeBanner, ImageScopeMovie, none, 0)},
		{"clearart.png", movie, namedSlot(ImageTypeClearArt, ImageScopeMovie, none, 0)},
		{"landscape.jpg", movie, namedSlot(ImageTypeLandscape, ImageScopeMovie, none, 0)},
		{"thumb.jpg", movie, namedSlot(ImageTypeThumb, ImageScopeMovie, none, 0)},
		{"disc.png", movie, namedSlot(ImageTypeDisc, ImageScopeMovie, none, 0)},
		{"discart.png", movie, namedSlot(ImageTypeDisc, ImageScopeMovie, none, 0)},
		{"poster.jpg", ImageNamingInput{Scope: ImageScopeMovie}, namedSlot(ImageTypePrimary, ImageScopeMovie, none, 0)},
		{"series-poster.jpg", series, namedSlot(ImageTypePrimary, ImageScopeSeries, none, 0)},
		{"Poster.JPG", series, namedSlot(ImageTypePrimary, ImageScopeSeries, none, 0)},
		{"folder.jpg", series, namedSlot(ImageTypePrimary, ImageScopeSeries, none, 0)},
		{"fanart3.jpg", series, namedSlot(ImageTypeBackdrop, ImageScopeSeries, none, 3)},
		{"logo.png", series, namedSlot(ImageTypeLogo, ImageScopeSeries, none, 0)},
		{"clearart.png", series, namedSlot(ImageTypeClearArt, ImageScopeSeries, none, 0)},
		{"thumb.jpg", series, namedSlot(ImageTypeThumb, ImageScopeSeries, none, 0)},
		{"season01-poster.jpg", series, namedSlot(ImageTypePrimary, ImageScopeSeason, 1, 0)},
		{"Season01-Thumb.jpg", series, namedSlot(ImageTypeThumb, ImageScopeSeason, 1, 0)},
		{"season00-poster.jpg", series, namedSlot(ImageTypePrimary, ImageScopeSeason, 0, 0)},
		{"season12-fanart.jpg", series, namedSlot(ImageTypeBackdrop, ImageScopeSeason, 12, 0)},
		{"season123-banner.jpg", series, namedSlot(ImageTypeBanner, ImageScopeSeason, 123, 0)},
		{"season02-landscape.jpg", series, namedSlot(ImageTypeLandscape, ImageScopeSeason, 2, 0)},
		{"season-specials-poster.png", series, namedSlot(ImageTypePrimary, ImageScopeSeason, 0, 0)},
		{"season-all-poster.jpg", series, namedSlot(ImageTypePrimary, ImageScopeSeason, ImageSeasonAll, 0)},
		{"SEASON-ALL-BANNER.JPG", series, namedSlot(ImageTypeBanner, ImageScopeSeason, ImageSeasonAll, 0)},
		{"poster.jpg", season, namedSlot(ImageTypePrimary, ImageScopeSeason, 2, 0)},
		{"cover.jpg", season, namedSlot(ImageTypePrimary, ImageScopeSeason, 2, 0)},
		{"fanart2.jpg", season, namedSlot(ImageTypeBackdrop, ImageScopeSeason, 2, 2)},
		{"thumb.jpg", season, namedSlot(ImageTypeThumb, ImageScopeSeason, 2, 0)},
		{"poster.jpg", ImageNamingInput{Scope: ImageScopeSeason, Season: ImageSeasonNone}, namedSlot(ImageTypePrimary, ImageScopeSeason, none, 0)},
		{"Show S01E02-thumb.jpg", episode, namedSlot(ImageTypeThumb, ImageScopeEpisode, none, 0)},
		{"show s01e02-THUMB.webp", episode, namedSlot(ImageTypeThumb, ImageScopeEpisode, none, 0)},
		{"Show S01E02.episode-thumb.png", episode, namedSlot(ImageTypeThumb, ImageScopeEpisode, none, 0)},
	} {
		got, err := RecognizeImageNames([]string{"Film (2020).mkv", "notes.txt", test.name}, test.input)
		want := []NamedImage{{ImageSlot: test.slot, Name: test.name}}
		if err != nil || !slices.Equal(got.Images, want) || len(got.Conflicts) != 0 {
			t.Errorf("%s in %s: got %+v, %v", test.name, test.input.Scope, got, err)
		}
	}
}

func TestRecognizeImageNamesIgnoresOtherScopesAndUnsupported(t *testing.T) {
	for _, test := range []struct {
		input ImageNamingInput
		names []string
	}{
		// Scope-specific names outside their scope.
		{ImageNamingInput{Scope: ImageScopeMovie, VideoBase: "Film"}, []string{"series-poster.jpg", "season01-poster.jpg", "season-all-poster.jpg", "Film-thumb.jpg", "Other-poster.jpg"}},
		{ImageNamingInput{Scope: ImageScopeSeries}, []string{"movie.jpg", "disc.png", "Film-poster.jpg", "-poster.jpg"}},
		{ImageNamingInput{Scope: ImageScopeSeason, Season: 1}, []string{"season01-poster.jpg", "series-poster.jpg", "logo.png", "movie.jpg"}},
		{ImageNamingInput{Scope: ImageScopeEpisode, VideoBase: "Ep"}, []string{"poster.jpg", "thumb.jpg", "Ep-poster.jpg", "Ep2-thumb.jpg", "-thumb.jpg", "Ep.jpg"}},
		// Unsupported extensions, including tif and heic accepted by the scanner.
		{ImageNamingInput{Scope: ImageScopeMovie}, []string{"poster.tif", "poster.heic", "poster.ico", "poster.txt", "poster", "poster.", ".jpg", "poster.jpg.bak"}},
		// Numbers with ambiguous or out-of-range spellings.
		{ImageNamingInput{Scope: ImageScopeSeries}, []string{"season1-poster.jpg", "season001-poster.jpg", "season012-poster.jpg", "season10000-poster.jpg", "season-poster.jpg", "seasonXY-poster.jpg", "season01-clearart.jpg", "season01.jpg", "season-specials.jpg"}},
		{ImageNamingInput{Scope: ImageScopeMovie}, []string{"fanart0.jpg", "fanart01.jpg", "fanart10000.jpg", "fanart-1.jpg", "fanartx.jpg", "backdrop1.jpg"}},
		// Unsafe names and non-ASCII look-alikes (U+017F long s, fullwidth).
		{ImageNamingInput{Scope: ImageScopeMovie}, []string{"../poster.jpg", "a/poster.jpg", "poster\\x.jpg", "c:poster.jpg", "post\x00er.jpg", "poster\n.jpg", "poster\x7f.jpg", "\xffposter.jpg", strings.Repeat("a", 252) + ".jpg", "poſter.jpg", "ｐoster.jpg", "", ".", ".."}},
	} {
		got, err := RecognizeImageNames(test.names, test.input)
		if err != nil || len(got.Images) != 0 || len(got.Conflicts) != 0 {
			t.Errorf("%s %q: got %+v, %v", test.input.Scope, test.names, got, err)
		}
	}
	// 255 bytes is the longest accepted name.
	long := strings.Repeat("a", 251) + "-poster.jpg"
	long = long[len(long)-255:]
	got, err := RecognizeImageNames([]string{long}, ImageNamingInput{Scope: ImageScopeMovie, VideoBase: long[:len(long)-len("-poster.jpg")]})
	if err != nil || len(got.Images) != 1 {
		t.Fatal("longest valid name rejected", got, err)
	}
}

func TestRecognizeImageNamesPriorityAndCollisions(t *testing.T) {
	none := ImageSeasonNone
	movie := ImageNamingInput{Scope: ImageScopeMovie, VideoBase: "Film"}
	got, err := RecognizeImageNames([]string{
		"movie.jpg", "cover.jpg", "folder.jpg", "poster.tiff", "poster.png", "Film-poster.webp", "Film-poster.png",
		"fanart.jpg", "backdrop.png", "fanart1.png", "fanart1.jpeg",
		"discart.jpg", "disc.gif",
		"logo.png", "LOGO.png", "logo.jpg", "clearlogo.png",
		"thumb.jpg", "Thumb.jpg", "THUMB.JPG", "fanart2.jpg", "Fanart2.jpg",
	}, movie)
	if err != nil {
		t.Fatal(err)
	}
	want := []NamedImage{
		{namedSlot(ImageTypeBackdrop, ImageScopeMovie, none, 0), "backdrop.png"},
		{namedSlot(ImageTypeBackdrop, ImageScopeMovie, none, 1), "fanart1.jpeg"},
		{namedSlot(ImageTypeClearLogo, ImageScopeMovie, none, 0), "clearlogo.png"},
		{namedSlot(ImageTypeDisc, ImageScopeMovie, none, 0), "disc.gif"},
		{namedSlot(ImageTypePrimary, ImageScopeMovie, none, 0), "Film-poster.png"},
	}
	conflicts := []ImageSlot{
		namedSlot(ImageTypeBackdrop, ImageScopeMovie, none, 2),
		namedSlot(ImageTypeLogo, ImageScopeMovie, none, 0),
		namedSlot(ImageTypeThumb, ImageScopeMovie, none, 0),
	}
	if !slices.Equal(got.Images, want) || !slices.Equal(got.Conflicts, conflicts) {
		t.Fatalf("priority or collision mismatch: %+v", got)
	}
	// A lower-ranked case collision rejects the slot even when a better
	// candidate exists, matching the existing Primary selection rule.
	got, err = RecognizeImageNames([]string{"Film-poster.jpg", "cover.jpg", "COVER.jpg"}, movie)
	if err != nil || len(got.Images) != 0 || !slices.Equal(got.Conflicts, []ImageSlot{namedSlot(ImageTypePrimary, ImageScopeMovie, none, 0)}) {
		t.Fatalf("low-rank collision accepted: %+v", got)
	}
	// Base matching folds case, so differently cased base names collide.
	got, err = RecognizeImageNames([]string{"Ep-thumb.jpg", "ep-thumb.jpg", "Ep.episode-thumb.jpg"}, ImageNamingInput{Scope: ImageScopeEpisode, VideoBase: "EP"})
	if err != nil || len(got.Images) != 0 || len(got.Conflicts) != 1 {
		t.Fatalf("base collision accepted: %+v", got)
	}
	// Season spellings: seasonNN before season-specials; season-all is separate.
	got, err = RecognizeImageNames([]string{"season-specials-poster.jpg", "season00-poster.png", "season-all-poster.jpg", "season01-poster.jpg", "season01-thumb.jpg", "poster.jpg"}, ImageNamingInput{Scope: ImageScopeSeries})
	want = []NamedImage{
		{namedSlot(ImageTypePrimary, ImageScopeSeries, none, 0), "poster.jpg"},
		{namedSlot(ImageTypePrimary, ImageScopeSeason, ImageSeasonAll, 0), "season-all-poster.jpg"},
		{namedSlot(ImageTypePrimary, ImageScopeSeason, 0, 0), "season00-poster.png"},
		{namedSlot(ImageTypePrimary, ImageScopeSeason, 1, 0), "season01-poster.jpg"},
		{namedSlot(ImageTypeThumb, ImageScopeSeason, 1, 0), "season01-thumb.jpg"},
	}
	if err != nil || !slices.Equal(got.Images, want) || len(got.Conflicts) != 0 {
		t.Fatalf("season priority mismatch: %+v", got)
	}
	// Input order does not change the result.
	names := []string{"poster.png", "folder.jpg", "poster.jpg", "fanart.jpg", "Fanart.JPG"}
	first, _ := RecognizeImageNames(names, movie)
	slices.Reverse(names)
	second, _ := RecognizeImageNames(names, movie)
	if !slices.Equal(first.Images, second.Images) || !slices.Equal(first.Conflicts, second.Conflicts) || len(first.Images) != 1 || first.Images[0].Name != "poster.jpg" {
		t.Fatalf("order dependent result: %+v %+v", first, second)
	}
}

func TestRecognizeImageNamesBoundsAndInput(t *testing.T) {
	names := make([]string, imageDirectoryEntries)
	for i := range names {
		names[i] = fmt.Sprintf("f%05d", i)
	}
	names[len(names)-1] = "poster.jpg"
	if got, err := RecognizeImageNames(names, ImageNamingInput{Scope: ImageScopeMovie}); err != nil || len(got.Images) != 1 {
		t.Fatal("full bounded listing rejected", err)
	}
	if got, err := RecognizeImageNames(append(names, "x"), ImageNamingInput{Scope: ImageScopeMovie}); err != domain.ErrImageTooLarge || got.Images != nil {
		t.Fatal("entry bound not enforced", err)
	}
	// Invalid names still count towards the byte bound.
	heavy := []string{"poster.jpg", strings.Repeat("x", imageDirectoryNameBytes-len("poster.jpg")+1)}
	if _, err := RecognizeImageNames(heavy, ImageNamingInput{Scope: ImageScopeMovie}); err != domain.ErrImageTooLarge {
		t.Fatal("name byte bound not enforced", err)
	}
	if _, err := RecognizeImageNames(heavy[:1], ImageNamingInput{Scope: ImageScopeMovie}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []ImageNamingInput{
		{}, {Scope: "Movie"}, {Scope: "library"},
		{Scope: ImageScopeEpisode},
		{Scope: ImageScopeSeries, VideoBase: "Film"},
		{Scope: ImageScopeSeason, VideoBase: "Film", Season: 1},
		{Scope: ImageScopeSeason, Season: -2}, {Scope: ImageScopeSeason, Season: 10000},
		{Scope: ImageScopeMovie, VideoBase: "a/b"}, {Scope: ImageScopeMovie, VideoBase: "..\x00"}, {Scope: ImageScopeEpisode, VideoBase: strings.Repeat("a", 256)},
	} {
		if _, err := RecognizeImageNames([]string{"poster.jpg"}, input); err != domain.ErrInvalid {
			t.Errorf("accepted invalid input %+v", input)
		}
	}
}

func TestListImageCandidatesFeedsRecognizer(t *testing.T) {
	directory := filepath.Join(imageSourceTestBase(t), "Show")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal("create listing fixture")
	}
	for _, name := range []string{"poster.jpg", "season01-poster.png", "Fanart1.JPG", "notes.txt", "Show.nfo"} {
		imageWrite(t, filepath.Join(directory, name), name)
	}
	list := func() ([]string, error) {
		root, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal("open listing root")
		}
		defer func() { _ = root.Close() }()
		listing, err := root.Open(".")
		if err != nil {
			t.Fatal("open listing")
		}
		defer func() { _ = listing.Close() }()
		var names []string
		err = listImageCandidates(context.Background(), root, listing, imageNameCandidate, func(name string) error {
			names = append(names, name)
			return nil
		})
		return names, err
	}
	names, err := list()
	slices.Sort(names)
	if err != nil || !slices.Equal(names, []string{"Fanart1.JPG", "poster.jpg", "season01-poster.png"}) {
		t.Fatal("listing filter mismatch", names, err)
	}
	got, err := RecognizeImageNames(names, ImageNamingInput{Scope: ImageScopeSeries})
	if err != nil || len(got.Images) != 3 {
		t.Fatal("recognition of listed names", got, err)
	}
	// A matching non-regular entry fails the listing instead of being skipped.
	if err := os.Mkdir(filepath.Join(directory, "folder.jpg"), 0700); err != nil {
		t.Fatal("create directory candidate")
	}
	if _, err := list(); err != domain.ErrImageUnavailable {
		t.Fatal("non-regular candidate accepted", err)
	}
	if err := os.Remove(filepath.Join(directory, "folder.jpg")); err != nil {
		t.Fatal("remove directory candidate")
	}
	for i := 0; len(names)+i <= imageDirectoryEntries+2; i++ {
		imageWrite(t, filepath.Join(directory, fmt.Sprintf("extra-%05d", i)), "")
	}
	if _, err := list(); !errors.Is(err, domain.ErrImageTooLarge) {
		t.Fatal("listing entry bound not enforced", err)
	}
}
