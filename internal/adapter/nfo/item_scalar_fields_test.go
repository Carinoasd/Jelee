package nfo

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemFieldsProjectsNFOSortTitle(t *testing.T) {
	root, name := sourceFixture(t, []byte(`<movie><title>Display title</title><sorttitle>Sorting title</sorttitle></movie>`))
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range fields.Fields {
		if field.Field == "sortTitle" && field.Value == "Sorting title" {
			return
		}
	}
	t.Fatal("supported NFO sorttitle was dropped from confirmed item projection")
}

func TestItemFieldsRejectsConflictingSortTitleAliases(t *testing.T) {
	root, name := sourceFixture(t, []byte(`<movie><sorttitle>First</sorttitle><sortname>Second</sortname></movie>`))
	reader, err := NewSummaryReader(DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
	if !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal("sort title aliases silently chose a value", err)
	}
}
