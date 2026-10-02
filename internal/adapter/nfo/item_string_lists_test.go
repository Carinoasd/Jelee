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

func TestItemStringListsOnlyObservationOwnsNestedValues(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	content := `<movie><genre>Drama</genre><genre>Mystery</genre><lockedfields>Genres</lockedfields></movie>`
	name := filepath.Join(scope.Source.RootPath, "folder", "Film.nfo")
	if err := os.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := reader.ObserveItemNFO(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	caller := observed.Selection()
	if caller.Fields.Version != domain.NFOItemListFieldsVersion || len(caller.Fields.Fields) != 0 || len(caller.Fields.Lists) != 1 || len(caller.Fields.Lists[0].Values) != 2 {
		t.Fatal("list-only observation did not retain typed values")
	}
	caller.Fields.Lists[0].Field = "caller"
	caller.Fields.Lists[0].Values[0] = "private edit"
	fresh := observed.Selection()
	if fresh.Fields.Lists[0].Field != "genres" || fresh.Fields.Lists[0].Values[0] != "Drama" || fresh.Fields.Lists[0].Values[1] != "Mystery" {
		t.Fatal("caller changed nested observation values")
	}
	if _, err := observed.Recheck(context.Background()); err != nil {
		t.Fatal("caller mutation changed source recheck", err)
	}
	if raw, err := os.ReadFile(name); err != nil || string(raw) != content {
		t.Fatal("list-only observation changed source bytes", err)
	}
}

func TestItemStringListsRejectOversizedValuesWithoutPartialProjection(t *testing.T) {
	for _, content := range []string{
		strings.Repeat(`<genre>Drama</genre>`, 129),
		`<genre>` + strings.Repeat("a", 1025) + `</genre>`,
		`<genre>` + strings.Repeat("字", 342) + `</genre>`,
		strings.Repeat(`<genre>`+strings.Repeat("a", 1024)+`</genre>`, 17),
	} {
		root, name := sourceFixture(t, []byte(`<movie><title>Movie</title>`+content+`</movie>`))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
		if !errors.Is(err, domain.ErrMetadataUnavailable) || fields.Version != "" || len(fields.Lists) != 0 || len(fields.Fields) != 0 {
			t.Fatal("oversized list produced partial metadata", err)
		}
	}
}
