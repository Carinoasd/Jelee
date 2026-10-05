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

func TestItemRatingsRejectAmbiguousValue(t *testing.T) {
	content := `<movie><title>Movie</title><ratings><rating name="imdb"><value>1</value><value>2</value></rating></ratings></movie>`
	root, name := sourceFixture(t, []byte(content))
	reader, _ := NewSummaryReader(DefaultMaxBytes)
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if !errors.Is(err, domain.ErrMetadataUnavailable) || len(fields.Ratings) != 0 || len(fields.Fields) != 0 {
		t.Fatal("ambiguous rating value was silently selected", err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(raw) != content {
		t.Fatal("rejected rating source changed", err)
	}
}

func TestItemRatingsOnlyObservationOwnsOptionalNumbers(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	content := `<movie><ratings><rating name="same" max="100"><value>85</value><votes>0</votes></rating><rating name="same"><value>0</value></rating><rating><value>1</value></rating></ratings></movie>`
	if err := os.WriteFile(filepath.Join(scope.Source.RootPath, "folder", "Film.nfo"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := reader.ObserveItemNFO(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	caller := observed.Selection()
	if caller.Fields.Version != domain.NFOItemRatingFieldsVersion || len(caller.Fields.Fields) != 0 || len(caller.Fields.Ratings) != 3 || caller.Fields.Ratings[0].Max == nil || *caller.Fields.Ratings[0].Max != 100 || caller.Fields.Ratings[0].Votes == nil || *caller.Fields.Ratings[0].Votes != 0 || caller.Fields.Ratings[1].Max != nil || caller.Fields.Ratings[1].Votes != nil || caller.Fields.Ratings[2].Name != "" {
		t.Fatal("rating-only observation lost original scale, missing numbers or unnamed source")
	}
	*caller.Fields.Ratings[0].Max = 99
	*caller.Fields.Ratings[0].Votes = 999
	caller.Fields.Ratings[0].Name = "private edit"
	fresh := observed.Selection()
	if *fresh.Fields.Ratings[0].Max != 100 || *fresh.Fields.Ratings[0].Votes != 0 || fresh.Fields.Ratings[0].Name != "same" {
		t.Fatal("caller changed owned rating numbers")
	}
	if _, err := observed.Recheck(context.Background()); err != nil {
		t.Fatal("caller rating edit changed recheck", err)
	}
}

func TestItemRatingsRejectLimitsWithoutPartialProjection(t *testing.T) {
	for _, ratings := range []string{
		strings.Repeat(`<rating><value>1</value></rating>`, 129),
		`<rating name="` + strings.Repeat("a", 1025) + `"><value>1</value></rating>`,
		strings.Repeat(`<rating name="`+strings.Repeat("a", 1024)+`"><value>1</value></rating>`, 17),
		`<rating max="0"><value>0</value></rating>`,
		`<rating max="100"><value>101</value></rating>`,
		`<rating><value>NaN</value></rating>`,
		`<rating><value>1</value><votes>2147483648</votes></rating>`,
		`<rating><value>1</value><votes>0</votes><votes>1</votes></rating>`,
	} {
		content := `<movie><title>Movie</title><ratings>` + ratings + `</ratings></movie>`
		root, name := sourceFixture(t, []byte(content))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
		if !errors.Is(err, domain.ErrMetadataUnavailable) || len(fields.Ratings) != 0 || len(fields.Fields) != 0 {
			t.Fatal("invalid ratings returned partial projection", err)
		}
	}
}

func TestItemRatingsRejectAmbiguousScaleAttribute(t *testing.T) {
	content := `<movie><title>Movie</title><ratings><rating name="imdb" max="10" MAX="100"><value>5</value></rating></ratings></movie>`
	root, name := sourceFixture(t, []byte(content))
	reader, _ := NewSummaryReader(DefaultMaxBytes)
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if !errors.Is(err, domain.ErrMetadataUnavailable) || len(fields.Ratings) != 0 {
		t.Fatal("ambiguous rating scale was silently selected", err)
	}
}
