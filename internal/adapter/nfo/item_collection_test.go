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

func TestItemCollectionRejectAmbiguousSources(t *testing.T) {
	for _, content := range []string{
		`<set>One</set><collection>Two</collection>`,
		`<set><name>One</name><name>Two</name></set>`,
		`<collection><name>One</name><overview>A</overview><overview>B</overview></collection>`,
	} {
		content = `<movie><title>Movie</title>` + content + `</movie>`
		root, name := sourceFixture(t, []byte(content))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
		if !errors.Is(err, domain.ErrMetadataUnavailable) || fields.Collection != nil || len(fields.Fields) != 0 {
			t.Fatal("ambiguous collection was silently selected", err)
		}
		if raw, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(raw) != content {
			t.Fatal("rejected collection source changed", err)
		}
	}
}

func TestItemCollectionOwnsStructureAndRechecks(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	content := `<movie><set><name>Collection A</name><overview>Plot</overview></set></movie>`
	if err := os.WriteFile(filepath.Join(scope.Source.RootPath, "folder", "Film.nfo"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := reader.ObserveItemNFO(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	caller := observed.Selection()
	if caller.Fields.Version != domain.NFOItemCollectionFieldsVersion || len(caller.Fields.Fields) != 0 || caller.Fields.Collection == nil || *caller.Fields.Collection != (domain.NFOCollection{Name: "Collection A", Overview: "Plot"}) {
		t.Fatal("collection-only observation lost structure")
	}
	caller.Fields.Collection.Name = "caller edit"
	caller.Fields.Collection.Overview = "caller plot"
	if got := observed.Selection().Fields.Collection; got.Name != "Collection A" || got.Overview != "Plot" {
		t.Fatal("caller modified owned collection")
	}
	if _, err := observed.Recheck(context.Background()); err != nil {
		t.Fatal("caller edit broke recheck", err)
	}
}

func TestItemCollectionAliasesAndLimits(t *testing.T) {
	for _, content := range []string{`<set>Collection A</set>`, `<collection>Collection A</collection>`, `<collection><name>Collection A</name></collection>`} {
		root, name := sourceFixture(t, []byte(`<movie>`+content+`</movie>`))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
		if err != nil || fields.Collection == nil || fields.Collection.Name != "Collection A" || fields.Collection.Overview != "" {
			t.Fatal("collection alias lost value", err)
		}
	}
	for _, content := range []string{
		`<set><name>` + strings.Repeat("a", 1025) + `</name></set>`,
		`<set><name>A</name><overview>` + strings.Repeat("a", 16385) + `</overview></set>`,
		`<set><overview>Missing name</overview></set>`,
		`<set>Direct text<name>Other name</name></set>`,
		`<set><other>Not a name</other></set>`,
	} {
		root, name := sourceFixture(t, []byte(`<movie><title>Movie</title>`+content+`</movie>`))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
		if !errors.Is(err, domain.ErrMetadataUnavailable) || fields.Collection != nil || len(fields.Fields) != 0 {
			t.Fatal("invalid collection returned partial projection", err)
		}
	}
}
