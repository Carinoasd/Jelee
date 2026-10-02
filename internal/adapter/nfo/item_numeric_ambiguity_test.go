package nfo

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemFieldsRejectsRepeatedRuntime(t *testing.T) {
	rejectNumericAmbiguity(t, `<movie><title>T</title><runtime>92 min</runtime><RUNTIME>93 minutes</RUNTIME></movie>`)
}

func TestItemFieldsRejectsRatingAliasConflict(t *testing.T) {
	rejectNumericAmbiguity(t, `<movie><title>T</title><rating>8.5</rating><communityrating>9</communityrating></movie>`)
}

func TestItemFieldsRejectsRepeatedUserRating(t *testing.T) {
	rejectNumericAmbiguity(t, `<movie><title>T</title><userrating>8</userrating><USERRATING>9</USERRATING></movie>`)
}

func TestItemFieldsNumericSingletonsPreserveCompatibleValues(t *testing.T) {
	for _, tc := range []struct {
		content            string
		year, runtime      int
		rating, userRating float64
	}{
		{`<year>1</year><runtime>0</runtime><rating>0</rating><userrating>0</userrating>`, 1, 0, 0, 0},
		{`<year>9999</year><runtime>10000000 minutes</runtime><communityrating>10</communityrating><userrating>10</userrating>`, 9999, 10000000, 10, 10},
		{`<year>2024</year><runtime>92 min</runtime><rating>8,5</rating><userrating>9</userrating><ratings><rating name="imdb"><value>8.5</value></rating><rating name="critic" max="100"><value>95</value></rating></ratings>`, 2024, 92, 8.5, 9},
	} {
		root, name := sourceFixture(t, []byte(`<movie><title>T</title>`+tc.content+`</movie>`))
		reader, err := NewSummaryReader(DefaultMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
		if err != nil || len(fields.Fields) != 1 || fields.Fields[0].Value != "T" {
			t.Fatal("compatible numeric content rejected or invented text fields", err)
		}
		if fields.Version != domain.NFOItemNumericFieldsVersion || len(fields.Facts) != 2 || len(fields.NumberFacts) != 2 || fields.Facts[0] != (domain.NFOIntegerFact{Field: "year", Value: tc.year}) || fields.Facts[1] != (domain.NFOIntegerFact{Field: "runtimeMinutes", Value: tc.runtime}) || fields.NumberFacts[0] != (domain.NFONumberFact{Field: "rating", Value: tc.rating}) || fields.NumberFacts[1] != (domain.NFONumberFact{Field: "userRating", Value: tc.userRating}) {
			t.Fatal("compatible numeric values lost their scalar types")
		}
	}
}

func TestItemFieldsNumericSingletonsRejectEmptyAndIdenticalRepeats(t *testing.T) {
	for _, tag := range []string{"year", "runtime", "rating", "communityrating", "userrating"} {
		t.Run(tag, func(t *testing.T) {
			rejectNumericAmbiguity(t, fmt.Sprintf(`<movie><title>T</title><%s>1</%s><%s>1</%s></movie>`, tag, tag, tag, tag))
			rejectNumericAmbiguity(t, fmt.Sprintf(`<movie><title>T</title><%s/><%s>1</%s></movie>`, tag, tag, tag))
		})
	}
}

func rejectNumericAmbiguity(t *testing.T, xml string) {
	t.Helper()
	root, name := sourceFixture(t, []byte(xml))
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal("ambiguous numeric field silently accepted", err)
	}
}
