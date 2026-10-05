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

func TestItemActorsRejectAmbiguousChildFields(t *testing.T) {
	for _, actor := range []string{`<name>Actor A</name><name>Actor B</name>`, `<name>Actor</name><role>A</role><role>B</role>`, `<name>Actor</name><thumb>a.jpg</thumb><thumb>b.jpg</thumb>`, `<name>Actor</name><order>0</order><order>1</order>`} {
		content := `<movie><title>Movie</title><actor>` + actor + `</actor></movie>`
		root, name := sourceFixture(t, []byte(content))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
		if !errors.Is(err, domain.ErrMetadataUnavailable) || len(fields.Actors) != 0 || len(fields.Fields) != 0 {
			t.Fatal("ambiguous actor child was silently selected", err)
		}
		if raw, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(raw) != content {
			t.Fatal("rejected actor source changed", err)
		}
	}
}

func TestItemActorsOnlyObservationOwnsOptionalOrder(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	content := `<movie><actor><name>Same name</name><role>A</role><order>0</order></actor><actor><name>Same name</name><role>B</role></actor></movie>`
	name := filepath.Join(scope.Source.RootPath, "folder", "Film.nfo")
	if err := os.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := reader.ObserveItemNFO(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	caller := observed.Selection()
	if caller.Fields.Version != domain.NFOItemActorFieldsVersion || len(caller.Fields.Fields) != 0 || len(caller.Fields.Actors) != 2 || caller.Fields.Actors[0].Order == nil || *caller.Fields.Actors[0].Order != 0 || caller.Fields.Actors[1].Order != nil {
		t.Fatal("actor-only observation lost source order or missing order")
	}
	caller.Fields.Actors[0].Name = "private edit"
	*caller.Fields.Actors[0].Order = 99
	fresh := observed.Selection()
	if fresh.Fields.Actors[0].Name != "Same name" || *fresh.Fields.Actors[0].Order != 0 || fresh.Fields.Actors[1].Role != "B" {
		t.Fatal("caller changed owned actor observation")
	}
	if _, err := observed.Recheck(context.Background()); err != nil {
		t.Fatal("caller actor mutation changed recheck", err)
	}
}

func TestItemActorsRejectLimitsWithoutPartialProjection(t *testing.T) {
	for _, actors := range []string{strings.Repeat(`<actor><name>Actor</name></actor>`, 129), `<actor><name>` + strings.Repeat("a", 1025) + `</name></actor>`, strings.Repeat(`<actor><name>`+strings.Repeat("a", 1024)+`</name></actor>`, 17), `<actor><name>Actor</name><thumb>` + strings.Repeat("a", 4097) + `</thumb></actor>`} {
		root, name := sourceFixture(t, []byte(`<movie><title>Movie</title>`+actors+`</movie>`))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
		if !errors.Is(err, domain.ErrMetadataUnavailable) || len(fields.Actors) != 0 || len(fields.Fields) != 0 {
			t.Fatal("oversized actors returned partial metadata", err)
		}
	}
}
