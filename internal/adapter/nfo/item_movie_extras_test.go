package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemMovieExtrasOwnValuesAndRecheck(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	content := `<movie><dateadded>2024-02-29T12:34:56.123+08:00</dateadded><trailer>trailers/a.mp4</trailer><trailer>plugin://video/trailer</trailer><thumb season="0" preview="preview.jpg">poster.jpg</thumb><fanart><thumb season="2">fanart.jpg</thumb></fanart><art><clearlogo season="3">logo.png</clearlogo></art><poster>other.jpg</poster></movie>`
	if err := os.WriteFile(filepath.Join(scope.Source.RootPath, "folder", "Film.nfo"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := reader.ObserveItemNFO(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	caller := observed.Selection()
	f := caller.Fields
	if f.Version != domain.NFOItemMovieFieldsVersion || f.DateAdded != "2024-02-29T12:34:56.123+08:00" || len(f.Trailers) != 2 || f.Trailers[1] != "plugin://video/trailer" || len(f.Art) != 4 {
		t.Fatal("movie-only observation lost date or ordered references")
	}
	for i, season := range []int{0, 2, 3} {
		if f.Art[i].Season == nil || *f.Art[i].Season != season {
			t.Fatal("movie artwork lost explicit season", i)
		}
	}
	if f.Art[3].Season != nil || f.Art[0].Preview != "preview.jpg" {
		t.Fatal("movie artwork invented season or lost preview")
	}
	caller.Fields.Trailers[0] = "caller edit"
	caller.Fields.Art[0].Location = "caller.jpg"
	*caller.Fields.Art[0].Season = 9
	owned := observed.Selection().Fields
	if owned.Trailers[0] != "trailers/a.mp4" || owned.Art[0].Location != "poster.jpg" || *owned.Art[0].Season != 0 {
		t.Fatal("caller mutated owned movie metadata")
	}
	if _, err := observed.Recheck(context.Background()); err != nil {
		t.Fatal("caller mutation broke movie recheck", err)
	}
}

func TestItemMovieExtrasRejectInvalidOrAmbiguous(t *testing.T) {
	for _, content := range []string{
		`<dateadded>2024-01-01</dateadded><dateadded>2025-01-01</dateadded>`,
		`<dateadded>2023-02-29</dateadded>`,
		`<dateadded>2024-01-01T24:00:00Z</dateadded>`,
		`<thumb season="invalid">poster.jpg</thumb>`,
		`<thumb season="-1">poster.jpg</thumb>`,
		`<thumb season="1000001">poster.jpg</thumb>`,
		`<thumb season="0" SEASON="1">poster.jpg</thumb>`,
		`<thumb preview="a.jpg" PREVIEW="b.jpg">poster.jpg</thumb>`,
		`<thumb aspect="poster" ASPECT="banner">poster.jpg</thumb>`,
		`<thumb aspect="poster" type="banner">poster.jpg</thumb>`,
		`<fanart><thumb season="invalid">fanart.jpg</thumb></fanart>`,
		`<art><poster season="invalid">poster.jpg</poster></art>`,
		strings.Repeat(`<trailer>a.mp4</trailer>`, 129),
		`<trailer>` + strings.Repeat("a", 4097) + `</trailer>`,
		strings.Repeat(`<trailer>`+strings.Repeat("a", 4096)+`</trailer>`, 5),
		strings.Repeat(`<poster>a.jpg</poster>`, 129),
	} {
		t.Run(content[:min(len(content), 72)], func(t *testing.T) {
			root, name := sourceFixture(t, []byte(`<movie><title>Movie</title>`+content+`</movie>`))
			reader, _ := NewSummaryReader(DefaultMaxBytes)
			f, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
			if !errors.Is(err, domain.ErrMetadataUnavailable) || len(f.Fields) != 0 || f.DateAdded != "" || len(f.Trailers) != 0 || len(f.Art) != 0 {
				t.Fatal("invalid movie extras returned a partial projection", err)
			}
		})
	}
}

func TestItemGlobalLockProtectsAllMovieFields(t *testing.T) {
	root, name := sourceFixture(t, []byte(`<movie><lockdata>true</lockdata></movie>`))
	reader, _ := NewSummaryReader(DefaultMaxBytes)
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range domain.NFOItemFieldNames(domain.NFOItemMovieFieldsVersion) {
		if !domain.NFOFieldLocked(fields, field) {
			t.Fatal("global NFO lock omitted supported movie field", field)
		}
	}
	if fields.Version != domain.NFOItemMovieFieldsVersion || len(fields.Fields) != 0 || len(fields.Facts) != 0 || len(fields.NumberFacts) != 0 || fields.Collection != nil || fields.DateAdded != "" || len(fields.Trailers) != 0 || len(fields.Art) != 0 {
		t.Fatal("global lock invented metadata values")
	}
}
