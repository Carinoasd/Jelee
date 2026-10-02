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

func TestItemIdentifiersOnlyObservationOwnsValues(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	content := `<movie><uniqueid type="IMDB" default="true">tt1234567</uniqueid><id>tt1234567</id><uniqueid type="custom">vendor-42</uniqueid></movie>`
	name := filepath.Join(scope.Source.RootPath, "folder", "Film.nfo")
	if err := os.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := reader.ObserveItemNFO(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	caller := observed.Selection()
	if caller.Fields.Version != domain.NFOItemIdentifierFieldsVersion || len(caller.Fields.Fields) != 0 || len(caller.Fields.UniqueIDs) != 3 || caller.Fields.UniqueIDs[0].Type != "imdb" || !caller.Fields.UniqueIDs[0].Default || caller.Fields.UniqueIDs[1].Value != "tt1234567" || caller.Fields.UniqueIDs[2].Type != "custom" {
		t.Fatal("identifier-only source lost aliases, order or default")
	}
	caller.Fields.UniqueIDs[0].Value = "private edit"
	caller.Fields.UniqueIDs[0].Default = false
	fresh := observed.Selection()
	if fresh.Fields.UniqueIDs[0].Value != "tt1234567" || !fresh.Fields.UniqueIDs[0].Default {
		t.Fatal("caller changed owned identifiers")
	}
	if _, err := observed.Recheck(context.Background()); err != nil {
		t.Fatal("caller identifier mutation changed recheck", err)
	}
}

func TestItemIdentifiersRejectLimitsWithoutPartialProjection(t *testing.T) {
	for _, ids := range []string{
		strings.Repeat(`<uniqueid type="custom">same</uniqueid>`, 129),
		`<uniqueid type="` + strings.Repeat("a", 65) + `">value</uniqueid>`,
		`<uniqueid type="custom">` + strings.Repeat("a", 1025) + `</uniqueid>`,
		strings.Repeat(`<uniqueid type="custom">`+strings.Repeat("a", 1024)+`</uniqueid>`, 16),
		`<uniqueid type="imdb">tt1</uniqueid><id>tt2</id>`,
	} {
		content := `<movie><title>Movie</title>` + ids + `</movie>`
		root, name := sourceFixture(t, []byte(content))
		reader, _ := NewSummaryReader(DefaultMaxBytes)
		fields, err := reader.ReadItemFields(context.Background(), domain.NFOSource{RootPath: root, RelativePath: name}, "Movie")
		if !errors.Is(err, domain.ErrMetadataUnavailable) || len(fields.UniqueIDs) != 0 || len(fields.Fields) != 0 {
			t.Fatal("invalid identifiers returned partial projection", err)
		}
		if raw, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(raw) != content {
			t.Fatal("rejected identifiers changed source", err)
		}
	}
}
